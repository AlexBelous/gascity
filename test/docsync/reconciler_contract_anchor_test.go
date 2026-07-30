//go:build reconciler_contract

package docsync

import (
	"encoding/json"
	"fmt"
	"os"
	pathpkg "path"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

const (
	candidateCommit     = "8941d74287bed229261fe56c246845d4db67f6cd"
	candidateTree       = "5cb7a6244b11961cc2387c663d73912f41c6d7df"
	implementationPlan  = "baa2cb29560d4d5161c33a880a2eba7fe04938bd"
	acceptanceMatrix    = "f960809fd9a28316a08d25c785e4284190fe0950"
	candidateManifest   = "87e2f7acd36bdca96fcfecd090257409d8e31ea5"
	candidateVerifier   = "07496a454de90bf1f1cc6727f9ba754ced644ec8"
	candidateEvidence   = "eefc251794e2a9a2420a034bd4ddcfce865eb947"
	ratificationCommit  = "7bf106acdcd23caf423d5d4a2322af2638cfb60b"
	ratificationTree    = "e32dacde926f7e678b03abf857b2e199491ecebe"
	ratificationRecord  = "2fef0322954c06408c669022a9303662244abe9c"
	contractWorkflow    = ".github/workflows/reconciler-contract.yml"
	contractRecord      = ".github/reconciler-contract/RATIFIED_V1.json"
	changedFilesEnv     = "RECONCILER_CONTRACT_CHANGED_FILES"
	changedFileCountEnv = "RECONCILER_CONTRACT_CHANGED_FILE_COUNT"
)

var fullObjectID = regexp.MustCompile(`^[0-9a-f]{40}$`)

type contractRecordV1 struct {
	SchemaVersion   int      `json:"schema_version"`
	Kind            string   `json:"kind"`
	ProtectedPaths  []string `json:"protected_paths"`
	FrozenCandidate struct {
		Commit       string `json:"commit"`
		Tree         string `json:"tree"`
		Plan         string `json:"plan_blob"`
		Matrix       string `json:"matrix_blob"`
		Manifest     string `json:"manifest_blob"`
		Verifier     string `json:"verifier_blob"`
		EvidenceTree string `json:"evidence_tree"`
	} `json:"frozen_candidate"`
	SemanticRatification struct {
		Commit string `json:"commit"`
		Tree   string `json:"tree"`
		Record string `json:"record_blob"`
		Plan   string `json:"plan_blob"`
		Matrix string `json:"matrix_blob"`
	} `json:"semantic_ratification"`
}

type changedFileRecord struct {
	Filename         string `json:"filename"`
	PreviousFilename string `json:"previous_filename"`
	Status           string `json:"status"`
}

var expectedProtectedPaths = []string{
	".github/CODEOWNERS",
	".github/reconciler-contract/**",
	".github/workflows/reconciler-contract.yml",
	"test/docsync/reconciler_contract_anchor_test.go",
	"engdocs/plans/reconciler-redesign/**",
}

func readReconcilerContractRecord(t *testing.T) contractRecordV1 {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(), contractRecord))
	if err != nil {
		t.Fatalf("read %s: %v", contractRecord, err)
	}
	var record contractRecordV1
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("parse %s: %v", contractRecord, err)
	}
	return record
}

func TestReconcilerContractPinsExactImmutableObjects(t *testing.T) {
	record := readReconcilerContractRecord(t)
	if record.SchemaVersion != 1 || record.Kind != "reconciler-contract-ratification" {
		t.Fatalf("unexpected contract record identity: version=%d kind=%q", record.SchemaVersion, record.Kind)
	}

	got := []string{
		record.FrozenCandidate.Commit, record.FrozenCandidate.Tree,
		record.FrozenCandidate.Plan, record.FrozenCandidate.Matrix,
		record.FrozenCandidate.Manifest, record.FrozenCandidate.Verifier, record.FrozenCandidate.EvidenceTree,
		record.SemanticRatification.Commit, record.SemanticRatification.Tree, record.SemanticRatification.Record,
		record.SemanticRatification.Plan, record.SemanticRatification.Matrix,
	}
	want := []string{
		candidateCommit, candidateTree, implementationPlan, acceptanceMatrix, candidateManifest, candidateVerifier, candidateEvidence,
		ratificationCommit, ratificationTree, ratificationRecord, implementationPlan, acceptanceMatrix,
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("pinned object %d = %q, want %q", i, got[i], want[i])
		}
		if !fullObjectID.MatchString(got[i]) {
			t.Errorf("pinned object %d must be a full immutable object ID, got %q", i, got[i])
		}
	}
	if !reflect.DeepEqual(record.ProtectedPaths, expectedProtectedPaths) {
		t.Errorf("protected paths = %q, want exact base-owned set %q", record.ProtectedPaths, expectedProtectedPaths)
	}
}

func TestReconcilerContractRejectsSeededProtectedPathMutations(t *testing.T) {
	for _, changed := range []string{
		".github/CODEOWNERS",
		".github/reconciler-contract/RATIFIED_V1.json",
		".github/workflows/reconciler-contract.yml",
		"test/docsync/reconciler_contract_anchor_test.go",
		"engdocs/plans/reconciler-redesign/IMPLEMENTATION_PLAN.md",
	} {
		t.Run(strings.ReplaceAll(changed, "/", "_"), func(t *testing.T) {
			if got, ok := protectedContractMutation(expectedProtectedPaths, []changedFileRecord{{Filename: changed}}); !ok || got != changed {
				t.Fatalf("protected mutation %q was accepted", changed)
			}
		})
	}
}

func TestReconcilerContractAllowsUnprotectedPathMutations(t *testing.T) {
	changed := []string{
		"docs/getting-started/overview.md",
		".github/workflows/ci.yml",
		"test/docsync/docsync_test.go",
		"engdocs/plans/another-project/PLAN.md",
	}
	files := make([]changedFileRecord, len(changed))
	for i, filename := range changed {
		files[i].Filename = filename
	}
	if got, ok := protectedContractMutation(expectedProtectedPaths, files); ok {
		t.Fatalf("unprotected mutation %q was rejected", got)
	}
}

func TestReconcilerContractChangedFilesJSONFailsClosed(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		_, err := readChangedFileRecords(filepath.Join(t.TempDir(), "missing.json"))
		if err == nil {
			t.Fatal("missing changed-files JSON was accepted")
		}
	})
	for name, data := range map[string]string{
		"malformed": "[",
		"null":      "null",
		"null page": ` [null]`,
		"object":    `{"filename":".github/CODEOWNERS"}`,
		"page":      `[{"filename":".github/CODEOWNERS"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "changed-files.json")
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatalf("write changed-files fixture: %v", err)
			}
			if _, err := readChangedFileRecords(path); err == nil {
				t.Fatalf("%s changed-files JSON was accepted", name)
			}
		})
	}
}

func TestReconcilerContractChangedFileRecordsFailClosed(t *testing.T) {
	for name, data := range map[string]string{
		"null record":                       `[[null]]`,
		"empty object":                      `[[{}]]`,
		"empty filename":                    `[[{"filename":"","status":"modified"}]]`,
		"absolute filename":                 `[[{"filename":"/.github/CODEOWNERS","status":"modified"}]]`,
		"parent filename":                   `[[{"filename":"../.github/CODEOWNERS","status":"modified"}]]`,
		"noncanonical filename":             `[[{"filename":"docs/../.github/CODEOWNERS","status":"modified"}]]`,
		"backslash filename":                `[[{"filename":".github\\CODEOWNERS","status":"modified"}]]`,
		"empty status":                      `[[{"filename":"docs/changed.md","status":""}]]`,
		"unknown status":                    `[[{"filename":"docs/changed.md","status":"deleted"}]]`,
		"renamed missing previous filename": `[[{"filename":"docs/renamed.md","status":"renamed"}]]`,
		"renamed empty previous filename":   `[[{"filename":"docs/renamed.md","previous_filename":"","status":"renamed"}]]`,
		"renamed unsafe previous filename":  `[[{"filename":"docs/renamed.md","previous_filename":"../.github/CODEOWNERS","status":"renamed"}]]`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "changed-files.json")
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatalf("write changed-files fixture: %v", err)
			}
			if _, err := readChangedFileRecords(path); err == nil {
				t.Fatalf("%s changed-file record was accepted", name)
			}
		})
	}
}

func TestReconcilerContractAcceptsGitHubChangedFileStatuses(t *testing.T) {
	for _, status := range []string{
		"added", "removed", "modified", "copied", "changed", "unchanged",
	} {
		t.Run(status, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "changed-files.json")
			data := fmt.Sprintf(`[[{"filename":"docs/%s.md","status":%q}]]`, status, status)
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatalf("write changed-files fixture: %v", err)
			}
			files, err := readChangedFileRecords(path)
			if err != nil {
				t.Fatalf("supported status %q was rejected: %v", status, err)
			}
			if len(files) != 1 || files[0].Status != status {
				t.Fatalf("changed file records = %#v, want one %q record", files, status)
			}
		})
	}
}

func TestReconcilerContractMalformedRecordCannotHideBehindMatchingCount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "changed-files.json")
	if err := os.WriteFile(path, []byte(`[[null]]`), 0o600); err != nil {
		t.Fatalf("write changed-files fixture: %v", err)
	}
	files, readErr := readChangedFileRecords(path)
	if readErr == nil {
		countErr := validateChangedFileCount("1", len(files))
		t.Fatalf("malformed record was accepted before matching count validation: count error=%v", countErr)
	}
}

func TestReconcilerContractChangedFilesRetainRenameSources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "changed-files.json")
	data := `[[{"filename":"docs/renamed.md","previous_filename":".github/CODEOWNERS","status":"renamed"}]]`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write changed-files fixture: %v", err)
	}

	files, err := readChangedFileRecords(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []changedFileRecord{{Filename: "docs/renamed.md", PreviousFilename: ".github/CODEOWNERS", Status: "renamed"}}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("changed file records = %#v, want %#v", files, want)
	}
	if got, ok := protectedContractMutation(expectedProtectedPaths, files); !ok || got != ".github/CODEOWNERS" {
		t.Fatalf("protected rename source was accepted: path=%q protected=%t", got, ok)
	}
}

func TestReconcilerContractProtectsGlobRootAndDescendants(t *testing.T) {
	for _, changed := range []string{
		".github/reconciler-contract",
		".github/reconciler-contract/RATIFIED_V1.json",
		"engdocs/plans/reconciler-redesign",
		"engdocs/plans/reconciler-redesign/IMPLEMENTATION_PLAN.md",
	} {
		t.Run(strings.ReplaceAll(changed, "/", "_"), func(t *testing.T) {
			if got, ok := protectedContractMutation(expectedProtectedPaths, []changedFileRecord{{Filename: changed}}); !ok || got != changed {
				t.Fatalf("protected glob path %q was accepted", changed)
			}
		})
	}
}

func TestReconcilerContractChangedFileCountFailsClosed(t *testing.T) {
	seeded := make([]changedFileRecord, 3000)
	for i := range seeded {
		seeded[i].Filename = fmt.Sprintf("docs/changed-%d.md", i)
		seeded[i].Status = "modified"
	}
	data, err := json.Marshal([][]changedFileRecord{seeded})
	if err != nil {
		t.Fatalf("marshal changed-files fixture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "changed-files.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write changed-files fixture: %v", err)
	}
	collected, err := readChangedFileRecords(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(collected), 3000; got != want {
		t.Fatalf("collected changed-file count = %d, want %d", got, want)
	}

	for _, tc := range []struct {
		name     string
		reported string
		wantErr  bool
	}{
		{name: "exact", reported: "3000"},
		{name: "reported exceeds collected", reported: "3001", wantErr: true},
		{name: "reported below collected", reported: "2999", wantErr: true},
		{name: "malformed reported count", reported: "three-thousand", wantErr: true},
		{name: "negative reported count", reported: "-1", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateChangedFileCount(tc.reported, len(collected))
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateChangedFileCount(%q, %d) error = %v, want error=%t", tc.reported, len(collected), err, tc.wantErr)
			}
		})
	}
}

func TestReconcilerContractTriggeringPRDoesNotMutateProtectedPaths(t *testing.T) {
	path := os.Getenv(changedFilesEnv)
	if path == "" {
		if os.Getenv("GITHUB_ACTIONS") == "true" {
			t.Fatalf("%s is required in GitHub Actions", changedFilesEnv)
		}
		return
	}
	changed, err := readChangedFileRecords(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateChangedFileCount(os.Getenv(changedFileCountEnv), len(changed)); err != nil {
		t.Fatal(err)
	}
	record := readReconcilerContractRecord(t)
	if changedPath, ok := protectedContractMutation(record.ProtectedPaths, changed); ok {
		t.Fatalf("triggering pull request mutates protected reconciler contract path %q", changedPath)
	}
}

func TestReconcilerContractWorkflowStaysTrustedAndFailClosed(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(), contractWorkflow))
	if err != nil {
		t.Fatalf("read %s: %v", contractWorkflow, err)
	}
	workflow := string(data)
	for _, want := range []string{
		"pull_request_target:", "contents: read", "pull-requests: read", "persist-credentials: false",
		"ref: ${{ github.sha }}",
		"gh api --paginate --slurp \\",
		"repos/${GITHUB_REPOSITORY}/pulls/${PR_NUMBER}/files?per_page=100",
		changedFilesEnv + ": ${{ runner.temp }}/reconciler-contract-changed-files.json",
		changedFileCountEnv + ": ${{ github.event.pull_request.changed_files }}",
		"git fetch --no-tags origin " + candidateCommit,
		"git fetch --no-tags origin " + ratificationCommit,
		"git cat-file -e \"${object}^{object}\"",
		"go test -tags reconciler_contract -count=1 ./test/docsync",
	} {
		if !strings.Contains(workflow, want) {
			t.Errorf("%s must contain %q", contractWorkflow, want)
		}
	}
	if strings.Count(workflow, "uses: actions/checkout@v4") != 1 || strings.Count(workflow, "persist-credentials: false") != 1 {
		t.Errorf("%s must use exactly one credential-free trusted-base checkout", contractWorkflow)
	}
	if strings.Count(workflow, candidateCommit) != 2 {
		t.Errorf("%s may reference the frozen candidate commit only for fixed fetch and object verification", contractWorkflow)
	}
	for _, object := range []string{
		candidateCommit, candidateTree, implementationPlan, acceptanceMatrix, candidateManifest, candidateVerifier, candidateEvidence,
		ratificationCommit, ratificationTree, ratificationRecord,
	} {
		if !strings.Contains(workflow, object) {
			t.Errorf("%s must fail if pinned object %s is unavailable", contractWorkflow, object)
		}
	}
	for _, forbidden := range []string{
		"${{ github.event.pull_request.head.sha }}", "refs/pull/", "pull_request:",
		"working-directory: .reconciler-candidate", "./.reconciler-candidate",
		"git checkout ", "git show ", "continue-on-error: true", "|| true", " -run ",
		"paths:", "paths-ignore:", ": write", "--jq",
	} {
		if strings.Contains(workflow, forbidden) {
			t.Errorf("%s must not contain %q", contractWorkflow, forbidden)
		}
	}
	if strings.Count(workflow, "permissions:") != 1 {
		t.Errorf("%s must have one explicit minimal permissions block", contractWorkflow)
	}
	if !strings.Contains(workflow, "permissions:\n  contents: read\n  pull-requests: read\n\njobs:") {
		t.Errorf("%s must grant only contents:read and pull-requests:read", contractWorkflow)
	}
	for _, env := range []string{"GIT_REPLACE_REF_BASE", "GIT_REPLACE_REF_OBJECT", "GIT_GRAFT_FILE"} {
		if strings.Contains(workflow, env+"=") || !strings.Contains(workflow, "-u "+env) {
			t.Errorf("%s must scrub %s without setting it", contractWorkflow, env)
		}
	}
}

func TestReconcilerContractProtectedPathsHaveAdminOwners(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(), ".github/CODEOWNERS"))
	if err != nil {
		t.Fatalf("read .github/CODEOWNERS: %v", err)
	}
	owners := string(data)
	for _, path := range []string{
		"/.github/CODEOWNERS",
		"/.github/reconciler-contract/",
		"/.github/workflows/reconciler-contract.yml",
		"/test/docsync/reconciler_contract_anchor_test.go",
		"/engdocs/plans/reconciler-redesign/",
	} {
		line := ""
		for _, candidate := range strings.Split(owners, "\n") {
			if strings.HasPrefix(candidate, path+" ") {
				line = candidate
				break
			}
		}
		if !strings.Contains(line, "@gastownhall/gascity-admin") {
			t.Errorf("CODEOWNERS must protect %s with @gastownhall/gascity-admin", path)
		}
	}
}

func protectedContractMutation(patterns []string, changedFiles []changedFileRecord) (string, bool) {
	for _, changedFile := range changedFiles {
		changedPaths := []string{changedFile.Filename}
		if changedFile.Status == "renamed" {
			changedPaths = append(changedPaths, changedFile.PreviousFilename)
		}
		for _, changed := range changedPaths {
			for _, pattern := range patterns {
				if strings.HasSuffix(pattern, "/**") {
					root := strings.TrimSuffix(pattern, "/**")
					if changed == root || strings.HasPrefix(changed, root+"/") {
						return changed, true
					}
					continue
				}
				if changed == pattern {
					return changed, true
				}
			}
		}
	}
	return "", false
}

func readChangedFileRecords(path string) ([]changedFileRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read triggering pull request files: %w", err)
	}
	var pages [][]*changedFileRecord
	if err := json.Unmarshal(data, &pages); err != nil {
		return nil, fmt.Errorf("parse triggering pull request files: %w", err)
	}
	if pages == nil {
		return nil, fmt.Errorf("parse triggering pull request files: expected paginated JSON array")
	}
	var files []changedFileRecord
	for pageIndex, page := range pages {
		if page == nil {
			return nil, fmt.Errorf("parse triggering pull request files: expected paginated JSON arrays")
		}
		for recordIndex, file := range page {
			if file == nil {
				return nil, fmt.Errorf("parse triggering pull request files: page %d record %d is null", pageIndex, recordIndex)
			}
			if err := validateChangedFileRecord(*file); err != nil {
				return nil, fmt.Errorf("parse triggering pull request files: page %d record %d: %w", pageIndex, recordIndex, err)
			}
			files = append(files, *file)
		}
	}
	return files, nil
}

func validateChangedFileRecord(file changedFileRecord) error {
	if err := validateGitHubFilename(file.Filename); err != nil {
		return fmt.Errorf("invalid filename: %w", err)
	}
	switch file.Status {
	case "added", "removed", "modified", "renamed", "copied", "changed", "unchanged":
	default:
		return fmt.Errorf("unknown status %q", file.Status)
	}
	if file.Status == "renamed" {
		if err := validateGitHubFilename(file.PreviousFilename); err != nil {
			return fmt.Errorf("invalid previous_filename for renamed file: %w", err)
		}
	}
	return nil
}

func validateGitHubFilename(filename string) error {
	switch {
	case filename == "":
		return fmt.Errorf("must not be empty")
	case filename == ".":
		return fmt.Errorf("must name a file")
	case pathpkg.IsAbs(filename):
		return fmt.Errorf("must be repository-relative")
	case filename == ".." || strings.HasPrefix(filename, "../"):
		return fmt.Errorf("must not escape the repository")
	case pathpkg.Clean(filename) != filename:
		return fmt.Errorf("must be canonical")
	case strings.Contains(filename, `\`):
		return fmt.Errorf("must use slash separators")
	case strings.IndexFunc(filename, unicode.IsControl) >= 0:
		return fmt.Errorf("must not contain control characters")
	default:
		return nil
	}
}

func validateChangedFileCount(reported string, collected int) error {
	if collected < 0 {
		return fmt.Errorf("collected triggering pull request files must not be negative")
	}
	expected, err := strconv.Atoi(reported)
	if err != nil || expected < 0 {
		return fmt.Errorf("parse triggering pull request changed_files count %q", reported)
	}
	if expected != collected {
		return fmt.Errorf("triggering pull request changed_files count mismatch: GitHub reported %d, collected %d", expected, collected)
	}
	return nil
}
