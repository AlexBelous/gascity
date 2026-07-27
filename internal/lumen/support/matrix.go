// Package support records the pinned Lumen semantic surface without depending
// on the engine that will eventually implement it.
package support

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

// UpstreamCommit is the Formula Language revision represented by matrix.json.
const UpstreamCommit = "faff38c7a04e41815d6e63051ff126137d5820b9"

const routesSHA256 = "0f3da01d959831ee8a2002fa71835d094e89098d7b96ccd42ef4ae98c32a03fa"

//go:embed testdata/matrix.json
var embedded []byte

//go:embed testdata/routes.json
var embeddedRoutes []byte

// Counts describes the active pinned form inventory.
type Counts struct {
	Total                 int `json:"total"`
	Selected              int `json:"selected"`
	SelectedUnimplemented int `json:"selectedUnimplemented"`
	Deferred              int `json:"deferred"`
	RetiredDiagnostic     int `json:"retiredDiagnostic"`
	Intended              int `json:"intended"`
}

// Matrix is the compact, generated Lumen support surface.
type Matrix struct {
	Schema     string     `json:"schema"`
	Upstream   Upstream   `json:"upstream"`
	Artifacts  []Artifact `json:"artifacts"`
	Rows       []Row      `json:"rows"`
	EngineLaws []Law      `json:"engineLaws"`
	HostLaws   []Law      `json:"hostLaws"`
	NonClaims  []NonClaim `json:"nonClaims"`
}

// Upstream identifies the offline-verifiable authority revision.
type Upstream struct {
	Commit string `json:"commit"`
}

// Artifact is a content-addressed authority input.
type Artifact struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// Row is one active or diagnostic selected-surface form.
type Row struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Citation  string `json:"citation"`
	PlanKey   string `json:"planKey,omitempty"`
	ProofKind string `json:"proofKind,omitempty"`
	NonClaim  string `json:"nonClaim,omitempty"`
}

// Law is a non-row engine or host execution obligation.
type Law struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`
	Citations []string `json:"citations"`
	PlanKey   string   `json:"planKey"`
	ProofKind string   `json:"proofKind"`
}

// NonClaim records a surface that must reject rather than receive guessed semantics.
type NonClaim struct {
	ID               string   `json:"id"`
	Citations        []string `json:"citations"`
	Disposition      string   `json:"disposition"`
	RejectingPlanKey string   `json:"rejectingPlanKey,omitempty"`
	Reason           string   `json:"reason"`
}

type routeCatalog struct {
	SchemaVersion string     `json:"schemaVersion"`
	Routes        []Route    `json:"routes"`
	EngineLaws    []Law      `json:"engineLaws"`
	HostLaws      []Law      `json:"hostLaws"`
	NonClaims     []NonClaim `json:"nonClaims"`
}

// Route assigns one intended source form to its implementation and proof slice.
type Route struct {
	ID        string `json:"id"`
	PlanKey   string `json:"planKey"`
	ProofKind string `json:"proofKind"`
}

// Load reads and validates the checked-in matrix without accessing a checkout or network.
func Load() (*Matrix, error) {
	var matrix Matrix
	decoder := json.NewDecoder(bytes.NewReader(embedded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&matrix); err != nil {
		return nil, fmt.Errorf("decode Lumen support matrix: %w", err)
	}
	if err := matrix.Validate(); err != nil {
		return nil, err
	}
	return &matrix, nil
}

// Counts returns the status totals for this matrix.
func (m *Matrix) Counts() Counts {
	c := Counts{Total: len(m.Rows)}
	for _, row := range m.Rows {
		switch row.Status {
		case "selected":
			c.Selected++
			c.Intended++
		case "selected-unimplemented":
			c.SelectedUnimplemented++
			c.Intended++
		case "deferred":
			c.Deferred++
		case "retired-diagnostic":
			c.RetiredDiagnostic++
		}
	}
	return c
}

// HasFailClosedNonClaim reports whether key is explicitly excluded.
func (m *Matrix) HasFailClosedNonClaim(key string) bool {
	for _, claim := range m.NonClaims {
		if claim.ID == key {
			return true
		}
	}
	return false
}

// Validate checks counts, one-owner routing, host law closure, and non-claim closure.
func (m *Matrix) Validate() error {
	routes, err := loadRoutes()
	if err != nil {
		return err
	}
	if m.Schema != "gascity.lumen.support/v1" {
		return fmt.Errorf("support matrix schema = %q", m.Schema)
	}
	if m.Upstream.Commit != UpstreamCommit {
		return fmt.Errorf("support matrix commit = %q, want %q", m.Upstream.Commit, UpstreamCommit)
	}
	if m.Counts() != (Counts{Total: 129, Selected: 94, SelectedUnimplemented: 26, Deferred: 1, RetiredDiagnostic: 8, Intended: 120}) {
		return fmt.Errorf("support matrix counts = %+v", m.Counts())
	}
	if err := validateArtifacts(m.Artifacts); err != nil {
		return err
	}
	routeByID := map[string]Route{}
	for _, route := range routes.Routes {
		if route.ID == "" || route.PlanKey == "" || (route.ProofKind != "go" && route.ProofKind != "compiler") || routeByID[route.ID].ID != "" {
			return fmt.Errorf("route lacks unique id, plan, and proof kind: %+v", route)
		}
		routeByID[route.ID] = route
	}
	seen := map[string]bool{}
	seenIntended := map[string]bool{}
	for _, row := range m.Rows {
		if row.ID == "" || seen[row.ID] {
			return fmt.Errorf("duplicate or empty support row %q", row.ID)
		}
		seen[row.ID] = true
		if row.Citation == "" {
			return fmt.Errorf("support row %q lacks citation", row.ID)
		}
		if row.Status == "selected" || row.Status == "selected-unimplemented" {
			route := routeByID[row.ID]
			if row.PlanKey == "" || row.NonClaim != "" || route.PlanKey != row.PlanKey || route.ProofKind != row.ProofKind {
				return fmt.Errorf("intended row %q does not have exactly one plan owner", row.ID)
			}
			seenIntended[row.ID] = true
		} else if row.PlanKey != "" || row.ProofKind != "" || row.NonClaim == "" {
			return fmt.Errorf("non-intended row %q is not fail-closed", row.ID)
		}
	}
	if len(seenIntended) != len(routeByID) {
		return fmt.Errorf("routes have %d rows, matrix has %d intended rows", len(routeByID), len(seenIntended))
	}
	for id := range routeByID {
		if !seenIntended[id] {
			return fmt.Errorf("route %q is not an intended matrix row", id)
		}
	}
	if err := validateLaws("engine", m.EngineLaws, routes.EngineLaws); err != nil {
		return err
	}
	if err := validateLaws("host", m.HostLaws, routes.HostLaws); err != nil {
		return err
	}
	if err := validateNonClaims(m.NonClaims, routes.NonClaims); err != nil {
		return err
	}
	return nil
}

func loadRoutes() (routeCatalog, error) {
	if hash(embeddedRoutes) != routesSHA256 {
		return routeCatalog{}, fmt.Errorf("reviewed Lumen routes hash differs from %s", routesSHA256)
	}
	var routes routeCatalog
	decoder := json.NewDecoder(bytes.NewReader(embeddedRoutes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&routes); err != nil {
		return routeCatalog{}, fmt.Errorf("decode Lumen routes: %w", err)
	}
	if routes.SchemaVersion != "gascity.lumen.routes/v1" {
		return routeCatalog{}, fmt.Errorf("lumen routes schema = %q", routes.SchemaVersion)
	}
	return routes, nil
}

func validateArtifacts(artifacts []Artifact) error {
	want := make([]Artifact, 0, len(authorityPaths))
	for _, path := range authorityPaths {
		want = append(want, Artifact{Path: path, SHA256: pinnedAuthorityHashes[path]})
	}
	if !reflect.DeepEqual(artifacts, want) {
		return fmt.Errorf("matrix artifacts do not match the pinned 17-path authority set")
	}
	return nil
}

func validateLaws(kind string, got, want []Law) error {
	seen := map[string]bool{}
	for _, law := range got {
		if law.ID == "" || seen[law.ID] || law.Kind != kind || law.PlanKey == "" || len(law.Citations) == 0 || (law.ProofKind != "go" && law.ProofKind != "compiler") {
			return fmt.Errorf("%s law is incomplete: %+v", kind, law)
		}
		seen[law.ID] = true
	}
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("%s law cohort differs from reviewed routes", kind)
	}
	return nil
}

func validateNonClaims(got, want []NonClaim) error {
	seen := map[string]bool{}
	for _, claim := range got {
		if claim.ID == "" || claim.Reason == "" || len(claim.Citations) == 0 || seen[claim.ID] {
			return fmt.Errorf("non-claim lacks unique id, reason, and citation: %+v", claim)
		}
		if claim.Disposition == "reject-before-genesis" && claim.RejectingPlanKey == "" {
			return fmt.Errorf("rejecting non-claim %q lacks an implementation owner", claim.ID)
		}
		if claim.Disposition != "reject-before-genesis" && claim.Disposition != "not-exposed" {
			return fmt.Errorf("non-claim %q disposition = %q", claim.ID, claim.Disposition)
		}
		seen[claim.ID] = true
	}
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("non-claim cohort differs from reviewed routes")
	}
	return nil
}

type manifest struct {
	SchemaVersion string        `json:"schemaVersion"`
	Release       string        `json:"release"`
	Status        string        `json:"status"`
	SourceTruth   []string      `json:"sourceTruth"`
	Rows          []manifestRow `json:"rows"`
}
type manifestRow struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	AnchorSource string `json:"anchorSource"`
}

// Build extracts canonical compact rows from syntax.lumen and its derivative manifest.
func Build(syntax, manifestJSON []byte) ([]byte, error) {
	forms, err := extractSyntaxForms(syntax)
	if err != nil {
		return nil, err
	}
	var derivative manifest
	if err := json.Unmarshal(manifestJSON, &derivative); err != nil {
		return nil, fmt.Errorf("decode derivative manifest: %w", err)
	}
	if derivative.SchemaVersion != "lumen-selected-surface-manifest/v0" || derivative.Release != "0.2.3" || derivative.Status != "canonical-seed" || !reflect.DeepEqual(derivative.SourceTruth, []string{"docs/spec/ir.lumen", "docs/spec/syntax.lumen"}) {
		return nil, fmt.Errorf("unexpected derivative manifest identity")
	}
	byID := map[string]manifestRow{}
	for _, row := range derivative.Rows {
		if row.ID == "" || byID[row.ID].ID != "" {
			return nil, fmt.Errorf("duplicate or empty derivative manifest row %q", row.ID)
		}
		byID[row.ID] = row
	}
	rows := make([]Row, 0, len(forms)+8)
	missing := map[string]bool{}
	sourceIDs := map[string]bool{}
	for _, form := range forms {
		sourceIDs[form.ID] = true
		row, present := byID[form.ID]
		if !present {
			missing[form.ID] = true
			if !reconciledOmission[form.ID] {
				return nil, fmt.Errorf("source form %q is missing from derivative manifest", form.ID)
			}
			row = manifestRow{ID: form.ID, Status: form.Status, AnchorSource: "syntax.lumen"}
		}
		if row.AnchorSource != "syntax.lumen" {
			return nil, fmt.Errorf("source form %q has derivative anchor %q, want syntax.lumen", form.ID, row.AnchorSource)
		}
		if row.Status != form.Status {
			return nil, fmt.Errorf("source form %q status = %q, derivative = %q", form.ID, form.Status, row.Status)
		}
		rows = append(rows, Row{ID: form.ID, Status: form.Status, Citation: "docs/spec/syntax.lumen#form-id:" + form.ID})
	}
	if hasKnownReconciliationForm(sourceIDs) {
		for id := range reconciledOmission {
			if !sourceIDs[id] || !missing[id] {
				return nil, fmt.Errorf("reconciliation omission %q is stale or incomplete", id)
			}
		}
	}
	for _, row := range derivative.Rows {
		switch row.AnchorSource {
		case "syntax.lumen":
			if !sourceIDs[row.ID] {
				return nil, fmt.Errorf("derivative syntax row %q has no source form", row.ID)
			}
		case "parser-diagnostic":
			if row.Status != "retired-diagnostic" {
				return nil, fmt.Errorf("parser diagnostic row %q status = %q", row.ID, row.Status)
			}
			if sourceIDs[row.ID] {
				return nil, fmt.Errorf("parser diagnostic row %q duplicates a source form", row.ID)
			}
			rows = append(rows, Row{ID: row.ID, Status: row.Status, Citation: "docs/workstreams/lumen-0.2.3-selected-surface-manifest.json#" + row.ID})
		default:
			return nil, fmt.Errorf("derivative row %q has unsupported anchor source %q", row.ID, row.AnchorSource)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return json.MarshalIndent(struct {
		Rows []Row `json:"rows"`
	}{Rows: rows}, "", "  ")
}

// Generate reads a checked-out pinned upstream repository and returns the
// canonical support matrix. It performs no network access.
func Generate(root string) ([]byte, error) {
	head, err := gitHead(root)
	if err != nil {
		return nil, err
	}
	if err := validateUpstreamHead(head); err != nil {
		return nil, err
	}
	if err := ensureCleanAuthority(root); err != nil {
		return nil, err
	}
	corpusPath := "docs/recovery/specification/corpus-manifest.json"
	corpus, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(corpusPath)))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", corpusPath, err)
	}
	if err := validateCorpusAuthority(root, corpus); err != nil {
		return nil, err
	}
	syntaxPath := "docs/spec/syntax.lumen"
	manifestPath := "docs/workstreams/lumen-0.2.3-selected-surface-manifest.json"
	syntax, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(syntaxPath)))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", syntaxPath, err)
	}
	derivative, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(manifestPath)))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", manifestPath, err)
	}
	compact, err := Build(syntax, derivative)
	if err != nil {
		return nil, err
	}
	var extracted struct {
		Rows []Row `json:"rows"`
	}
	if err := json.Unmarshal(compact, &extracted); err != nil {
		return nil, err
	}
	artifacts := make([]Artifact, 0, len(authorityPaths))
	for _, name := range authorityPaths {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			return nil, fmt.Errorf("read authority input %s: %w", name, err)
		}
		if err := validateAuthorityInput(name, data); err != nil {
			return nil, err
		}
		artifacts = append(artifacts, Artifact{Path: name, SHA256: hash(data)})
	}
	return canonicalMatrix(extracted.Rows, artifacts)
}

func validateAuthorityInput(path string, data []byte) error {
	want, pinned := pinnedAuthorityHashes[path]
	if !pinned {
		return fmt.Errorf("authority input %s is not pinned", path)
	}
	if got := hash(data); got != want {
		return fmt.Errorf("authority input %s hash = %s, want %s", path, got, want)
	}
	return nil
}

type syntaxForm struct{ ID, Status string }

type corpusManifest struct {
	SchemaVersion    int                `json:"schemaVersion"`
	Status           string             `json:"status"`
	MembershipClosed bool               `json:"membershipClosed"`
	Vocabulary       []corpusMember     `json:"vocabulary"`
	Laws             []corpusMember     `json:"laws"`
	Governance       []governanceMember `json:"governance"`
}

type corpusMember struct {
	Path          string `json:"path"`
	RecoveredBlob string `json:"recoveredBlob"`
}

type governanceMember struct {
	Path                            string `json:"path"`
	ContentBlob                     string `json:"contentBlob"`
	NormativeSpecificationSetMember bool   `json:"normativeSpecificationSetMember"`
}

func validateCorpusAuthority(root string, data []byte) error {
	var corpus corpusManifest
	if err := json.Unmarshal(data, &corpus); err != nil {
		return fmt.Errorf("decode corpus manifest: %w", err)
	}
	if corpus.SchemaVersion != 1 || corpus.Status != "ACTIVE" || !corpus.MembershipClosed || len(corpus.Vocabulary) != 3 || len(corpus.Laws) != 9 {
		return fmt.Errorf("corpus manifest is not the active closed 3+9 specification set")
	}
	paths := map[string]bool{"docs/recovery/specification/corpus-manifest.json": true, "docs/workstreams/lumen-0.2.3-selected-surface-manifest.json": true, "scripts/check-lumen-selected-surface-manifest.mjs": true}
	if err := validateCorpusMembers(root, paths, corpus.Vocabulary, func(member corpusMember) string { return member.RecoveredBlob }); err != nil {
		return err
	}
	if err := validateCorpusMembers(root, paths, corpus.Laws, func(member corpusMember) string { return member.RecoveredBlob }); err != nil {
		return err
	}
	governanceCount := 0
	for _, member := range corpus.Governance {
		if !member.NormativeSpecificationSetMember {
			continue
		}
		governanceCount++
		if member.Path == "" || member.ContentBlob == "" || paths[member.Path] {
			return fmt.Errorf("invalid normative governance member %q", member.Path)
		}
		blob, err := gitBlob(root, member.Path)
		if err != nil || blob != member.ContentBlob {
			return fmt.Errorf("governance member %q does not match content blob", member.Path)
		}
		paths[member.Path] = true
	}
	if governanceCount != 2 || len(paths) != len(authorityPaths) {
		return fmt.Errorf("corpus authority membership is not the exact 17-path set")
	}
	for _, path := range authorityPaths {
		if !paths[path] {
			return fmt.Errorf("authority path %q is not in the recovered corpus", path)
		}
	}
	return nil
}

func validateCorpusMembers(root string, paths map[string]bool, members []corpusMember, blob func(corpusMember) string) error {
	for _, member := range members {
		if member.Path == "" || blob(member) == "" || paths[member.Path] {
			return fmt.Errorf("invalid recovered corpus member %q", member.Path)
		}
		got, err := gitBlob(root, member.Path)
		if err != nil || got != blob(member) {
			return fmt.Errorf("recovered corpus member %q does not match its blob", member.Path)
		}
		paths[member.Path] = true
	}
	return nil
}

func extractSyntaxForms(source []byte) ([]syntaxForm, error) {
	var metadata []string
	seen := map[string]bool{}
	var forms []syntaxForm
	for _, line := range strings.Split(string(source), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(strings.TrimSpace(line), "///") {
			metadata = append(metadata, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "///")))
			continue
		}
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		var ids []string
		status := "selected"
		for _, item := range metadata {
			key, value, ok := strings.Cut(item, ":")
			if !ok {
				continue
			}
			switch strings.TrimSpace(key) {
			case "form-id":
				ids = append(ids, strings.TrimSpace(value))
			case "form-status":
				status = strings.TrimSpace(value)
			}
		}
		for _, id := range ids {
			if seen[id] {
				return nil, fmt.Errorf("duplicate syntax form ID %q", id)
			}
			seen[id] = true
			forms = append(forms, syntaxForm{ID: id, Status: status})
		}
		metadata = nil
	}
	return forms, nil
}

func gitHead(root string) (string, error) {
	output, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("read upstream HEAD: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

func gitBlob(root, path string) (string, error) {
	output, err := exec.Command("git", "-C", root, "rev-parse", "HEAD:"+path).Output()
	if err != nil {
		return "", fmt.Errorf("read upstream blob for %s: %w", path, err)
	}
	return strings.TrimSpace(string(output)), nil
}

func ensureCleanAuthority(root string) error {
	args := append([]string{"-C", root, "diff", "--quiet", "HEAD", "--"}, authorityPaths...)
	if err := exec.Command("git", args...).Run(); err != nil {
		exitError := &exec.ExitError{}
		if errors.As(err, &exitError) {
			return fmt.Errorf("pinned authority inputs differ from HEAD")
		}
		return fmt.Errorf("verify pinned authority inputs: %w", err)
	}
	return nil
}

func validateUpstreamHead(head string) error {
	if head != UpstreamCommit {
		return fmt.Errorf("upstream HEAD = %q, want %q", head, UpstreamCommit)
	}
	return nil
}

func hasKnownReconciliationForm(sourceIDs map[string]bool) bool {
	for id := range reconciledOmission {
		if sourceIDs[id] {
			return true
		}
	}
	return false
}

var reconciledOmission = map[string]bool{
	"syntax.scope-anchor.caller": true,
	"type.handle":                true,
	"expr.handle-construct":      true,
	"expr.channel-sink":          true,
}

var authorityPaths = []string{
	"docs/recovery/specification/corpus-manifest.json",
	"docs/spec/syntax.lumen", "docs/spec/ir.lumen", "docs/spec/prelude.lumen", "docs/spec/modules.md", "docs/spec/types.md", "docs/spec/expressions.md", "docs/spec/execution-model.md", "docs/spec/interactions.md", "docs/spec/agents.md", "docs/spec/metaprogramming.md", "docs/spec/packages.md", "docs/spec/runtime.md",
	"docs/spec/specifying-lumen.md", "docs/spec/syntax-ir-derivation-contract.md",
	"docs/workstreams/lumen-0.2.3-selected-surface-manifest.json", "scripts/check-lumen-selected-surface-manifest.mjs",
}

var pinnedAuthorityHashes = map[string]string{
	"docs/recovery/specification/corpus-manifest.json":            "2ac80f0a12761080ebf92abc786b4aa010920aea0b21c28401c009db4ea5f2b1",
	"docs/spec/syntax.lumen":                                      "b2d0d68ae311ca50d2127a1e83a017dbc6b18848eaff9fd10a5b045ee50f6c75",
	"docs/spec/ir.lumen":                                          "358ab65c56e5d0be305623dc93c0c0a41aef762bea118a6a7b75426ad1f52eb2",
	"docs/spec/prelude.lumen":                                     "c47e19d611d7864d135182aa454ae11094cdba2f970a056f2d7486530d9a95b6",
	"docs/spec/modules.md":                                        "bd3c21a5db2cef200315bbeba992e898c9c62deb925312c4a679561c9e6838ea",
	"docs/spec/types.md":                                          "ef3383f6f6f66d62978595a69aedd89a6682cfbc7bef335ee312601d993c6666",
	"docs/spec/expressions.md":                                    "e4783b4dd71429009699f0e928b23680b11dbb863bbe133ba681e20d0f07a90e",
	"docs/spec/execution-model.md":                                "4fb45b062e0347eab1708366836e3afaafe85c4ffb327920af6cc355d44dd1da",
	"docs/spec/interactions.md":                                   "7137e1053e0f5c122a58de1c405ea68b405979dbd277230a89e535c97dbfb275",
	"docs/spec/agents.md":                                         "16b860e68fc774b7817b2c2a337b3ebf18a04bbf443cb6a4fc8a7ef1d2bb2068",
	"docs/spec/metaprogramming.md":                                "4f07b4c53296e1cd929c8d971f42b58595e5ad301e00b4067ad7e7ae2cae3a34",
	"docs/spec/packages.md":                                       "a04f222c9b59939fea04730558cbc636ff9457dd950a87d5a23077ebf0b81497",
	"docs/spec/runtime.md":                                        "8e4c2cd3193925e1ecd0fa492a3d5ee84cfefa150adc619769f82ec0fd6d9100",
	"docs/spec/specifying-lumen.md":                               "1d296b9d5e380a1e227c1af89b0a626e22bf138a8efdfcce9a3b4d8f2d8edb15",
	"docs/spec/syntax-ir-derivation-contract.md":                  "c05dfa66605ba6898c72a5a15e8b776a4f8a17b8ea2cca749e1e857d12e6a4b5",
	"docs/workstreams/lumen-0.2.3-selected-surface-manifest.json": "6a16bde3c4426e420d3b04d3e2c71eff1a0a84a2e3f584fe07d52982e6c9229c",
	"scripts/check-lumen-selected-surface-manifest.mjs":           "917945d5415d656241c4a7403a598d63995796d70986576bf5e02c4e3d42ea5b",
}

// CheckRuntimeImports rejects dependencies from runtime packages on this evidence package.
func CheckRuntimeImports(filesystem fs.FS) error {
	return fs.WalkDir(filesystem, "internal/lumen", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && (name == "internal/lumen/support" || strings.HasPrefix(name, "internal/lumen/support/")) {
			return fs.SkipDir
		}
		if entry.IsDir() || !strings.HasSuffix(name, ".go") {
			return nil
		}
		data, err := fs.ReadFile(filesystem, name)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), "github.com/gastownhall/gascity/internal/lumen/support") {
			return fmt.Errorf("runtime source %s imports lumen support", name)
		}
		return nil
	})
}

func hash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func nonClaimFor(status string) string {
	if status == "deferred" {
		return "upstream-deferred"
	}
	return "retired-diagnostic"
}

func canonicalMatrix(rows []Row, artifacts []Artifact) ([]byte, error) {
	routes, err := loadRoutes()
	if err != nil {
		return nil, err
	}
	routeByID := make(map[string]Route, len(routes.Routes))
	for _, route := range routes.Routes {
		if routeByID[route.ID].ID != "" {
			return nil, fmt.Errorf("duplicate route %q", route.ID)
		}
		routeByID[route.ID] = route
	}
	for i := range rows {
		if route, intended := routeByID[rows[i].ID]; intended {
			rows[i].PlanKey = route.PlanKey
			rows[i].ProofKind = route.ProofKind
		} else {
			rows[i].NonClaim = nonClaimFor(rows[i].Status)
		}
	}
	m := Matrix{
		Schema:     "gascity.lumen.support/v1",
		Upstream:   Upstream{Commit: UpstreamCommit},
		Artifacts:  artifacts,
		Rows:       rows,
		EngineLaws: routes.EngineLaws,
		HostLaws:   routes.HostLaws,
		NonClaims:  routes.NonClaims,
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return json.MarshalIndent(m, "", "  ")
}
