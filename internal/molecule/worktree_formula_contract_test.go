package molecule

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/formula"
	"github.com/gastownhall/gascity/internal/formulatest"
	"github.com/gastownhall/gascity/internal/testutil"
)

func TestProductionWorkspaceSetupFormulasUseTransactionalWorktreeOwner(t *testing.T) {
	formulatest.EnableV2ForTest(t)

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	sourceRoot := filepath.Clean(filepath.Join(cwd, "..", ".."))
	searchDir := filepath.Join(sourceRoot, "internal", "bootstrap", "packs", "core", "formulas")

	for _, formulaName := range []string{"mol-scoped-work", "mol-polecat-commit"} {
		t.Run(formulaName, func(t *testing.T) {
			recipe, err := formula.Compile(context.Background(), formulaName, []string{searchDir}, nil)
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			plan, _, _, err := buildRecipeApplyPlan(recipe, Options{Vars: map[string]string{
				"base_branch":   "main",
				"convoy_id":     "convoy-test",
				"setup_command": "test -d .",
			}})
			if err != nil {
				t.Fatalf("buildRecipeApplyPlan: %v", err)
			}

			description := workspaceSetupDescription(t, plan.Nodes)
			script := strings.Join(fencedBashBlocks(description), "\n")
			if strings.TrimSpace(script) == "" {
				t.Fatal("materialized workspace-setup has no executable bash contract")
			}
			cleanupDescription := workspaceCleanupDescription(t, plan.Nodes)
			cleanupScript := strings.Join(worktreeCleanupBlocks(cleanupDescription), "\n")
			if strings.TrimSpace(cleanupScript) == "" {
				t.Fatal("materialized plan has no executable worktree cleanup contract")
			}

			repo := filepath.Join(t.TempDir(), "repo")
			origin := filepath.Join(t.TempDir(), "origin.git")
			executor := filepath.Join(t.TempDir(), "executor")
			testutil.RunCommandAtProcessCWD(t, append(os.Environ(),
				"FORMULA_REPO="+repo,
				"FORMULA_ORIGIN="+origin,
				"FORMULA_EXECUTOR="+executor,
			), "bash", "-euo", "pipefail", "-c", `
git init "$FORMULA_REPO"
git -C "$FORMULA_REPO" config user.email test@test.com
git -C "$FORMULA_REPO" config user.name Test
git -C "$FORMULA_REPO" commit --allow-empty -m init
git -C "$FORMULA_REPO" branch -M main
git init --bare "$FORMULA_ORIGIN"
git -C "$FORMULA_REPO" remote add origin "$FORMULA_ORIGIN"
git -C "$FORMULA_REPO" push -u origin main
git -C "$FORMULA_REPO" worktree add -b test/executor "$FORMULA_EXECUTOR" main
`)

			logPath := filepath.Join(t.TempDir(), "commands.log")
			statePath := filepath.Join(t.TempDir(), "published-worktree")
			clearFailurePath := filepath.Join(t.TempDir(), "clear-failed-once")
			binDir := t.TempDir()
			realGC := filepath.Join(t.TempDir(), "gc")
			testutil.RunCommandAtProcessCWD(t, os.Environ(), "go", "-C", sourceRoot, "build", "-o", realGC, "./cmd/gc")
			realGit := testutil.LookPath(t, "git")
			writeExecutable(t, filepath.Join(binDir, "git"), `#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "worktree" && ( "${2:-}" == "add" || "${2:-}" == "remove" ) && "${GC_OWNER_DELEGATED:-}" != "1" ]]; then
  printf 'RAW_GIT_WORKTREE_%s %q\n' "${2^^}" "$*" >> "$COMMAND_LOG"
  exit 91
fi
exec "$REAL_GIT" "$@"
`)
			writeExecutable(t, filepath.Join(binDir, "gc"), `#!/usr/bin/env bash
set -euo pipefail
printf 'gc' >> "$COMMAND_LOG"
printf ' %q' "$@" >> "$COMMAND_LOG"
printf '\n' >> "$COMMAND_LOG"
if [[ "${1:-}" == "convoy" && "${2:-}" == "status" ]]; then
  printf '%s\n' '{"children":[{"id":"gc-work"}]}'
  exit 0
fi
if [[ "${1:-}" == "bd" && "${2:-}" == "show" ]]; then
  if [[ "${3:-}" == "gc-step" ]]; then
    printf '%s\n' '[{"metadata":{"gc.root_store_ref":"rig:gascity"}}]'
  elif [[ -f "$PUBLISHED_STATE" ]]; then
    base_sha=$("$REAL_GIT" -C "$FORMULA_REPO" rev-parse --verify 'main^{commit}')
    jq -n \
      --arg path "${FORMULA_REPO}-worktrees/gc-work" \
      --arg branch "work/gc-work" \
      --arg repo "$FORMULA_REPO" \
      --arg root "${FORMULA_REPO}-worktrees" \
      --arg base_ref "origin/main" \
      --arg base_sha "$base_sha" \
      --arg creator "formula:$FORMULA_NAME" \
      --arg generation "formula:$FORMULA_NAME:gc-work" \
      '[
        {
          metadata: {
            "gc.work_dir": $path,
            "work_dir": $path,
            "gc.work_branch": $branch,
            "gc.worktree_repo": $repo,
            "gc.worktree_root": $root,
            "gc.worktree_base_ref": $base_ref,
            "gc.worktree_base_sha": $base_sha,
            "gc.worktree_creator": $creator,
            "gc.worktree_owner": $creator,
            "gc.worktree_generation": $generation,
            "gc.worktree_lifecycle": "active"
          }
        }
      ]'
  else
    printf '%s\n' '[{"metadata":{}}]'
  fi
  exit 0
fi
if [[ "${1:-}" == "worktree" && ( "${2:-}" == "ensure" || "${2:-}" == "cleanup" ) ]]; then
  export GC_OWNER_DELEGATED=1
  exec "$REAL_GC" "$@"
fi
if [[ "${1:-}" == "bd" && "${2:-}" == "update" ]]; then
  if [[ " $* " == *" --set-metadata "* ]]; then
    : > "$PUBLISHED_STATE"
    exit 0
  fi
  if [[ " $* " == *" --unset-metadata "* ]]; then
    if [[ "${FAIL_CLEAR_ONCE:-}" == "1" && ! -f "$CLEAR_FAILURE_STATE" ]]; then
      : > "$CLEAR_FAILURE_STATE"
      exit 88
    fi
    rm -f "$PUBLISHED_STATE"
    exit 0
  fi
  exit 0
fi
printf 'unexpected gc command: %s\n' "$*" >&2
exit 4
`)

			env := append(os.Environ(),
				"COMMAND_LOG="+logPath,
				"FORMULA_EXECUTOR="+executor,
				"FORMULA_REPO="+repo,
				"FORMULA_NAME="+formulaName,
				"GC_BEAD_ID=gc-step",
				"GC_JSON_CONTRACT_STRICT=1",
				"PUBLISHED_STATE="+statePath,
				"CLEAR_FAILURE_STATE="+clearFailurePath,
				"REAL_GC="+realGC,
				"REAL_GIT="+realGit,
				"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
			)
			testutil.RunCommandAtProcessCWD(t, env, "bash", "-euo", "pipefail", "-c",
				`cd "$FORMULA_EXECUTOR"`+"\n"+script)
			worktreePath := repo + "-worktrees/gc-work"
			if _, err := os.Stat(worktreePath); err != nil {
				t.Fatalf("owner did not create stable-root worktree %s: %v", worktreePath, err)
			}

			dirtyMarker := filepath.Join(worktreePath, "retained-wip.txt")
			if err := os.WriteFile(dirtyMarker, []byte("retain"), 0o644); err != nil {
				t.Fatalf("WriteFile dirty marker: %v", err)
			}
			dirtyReport := filepath.Join(t.TempDir(), "cleanup-report.json")
			dirtyCleanup := append([]string(nil), env...)
			dirtyCleanup = append(dirtyCleanup,
				"FORMULA_CLEANUP_SCRIPT="+cleanupScript,
				"FORMULA_CLEANUP_REPORT="+dirtyReport,
			)
			testutil.RunCommandAtProcessCWD(t, dirtyCleanup, "bash", "-euo", "pipefail", "-c", `
set +e
bash -c "$FORMULA_CLEANUP_SCRIPT" > "$FORMULA_CLEANUP_REPORT"
code=$?
test "$code" -ne 0
jq -e '.cleanup_pending == true and .error.code == "dirty_worktree"' "$FORMULA_CLEANUP_REPORT" >/dev/null
`)
			if got, err := os.ReadFile(dirtyMarker); err != nil || string(got) != "retain" {
				t.Fatalf("formula cleanup changed dirty WIP: data=%q err=%v", got, err)
			}
			if _, err := os.Stat(statePath); err != nil {
				t.Fatalf("dirty cleanup cleared published provenance: %v", err)
			}
			if err := os.Remove(dirtyMarker); err != nil {
				t.Fatalf("Remove dirty marker: %v", err)
			}

			firstCleanup := append([]string(nil), env...)
			firstCleanup = append(firstCleanup, "FAIL_CLEAR_ONCE=1", "FORMULA_CLEANUP_SCRIPT="+cleanupScript)
			testutil.RunCommandAtProcessCWD(t, firstCleanup, "bash", "-euo", "pipefail", "-c", `
set +e
bash -c "$FORMULA_CLEANUP_SCRIPT"
code=$?
test "$code" -eq 88
`)
			if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
				t.Fatalf("owner cleanup did not remove worktree before simulated metadata failure: %v", err)
			}
			if _, err := os.Stat(statePath); err != nil {
				t.Fatalf("simulated metadata failure did not preserve provenance for retry: %v", err)
			}
			testutil.RunCommandAtProcessCWD(t, firstCleanup, "bash", "-euo", "pipefail", "-c",
				`bash -c "$FORMULA_CLEANUP_SCRIPT"`)
			if _, err := os.Stat(statePath); !os.IsNotExist(err) {
				t.Fatalf("successful retry did not clear published provenance: %v", err)
			}

			logBytes, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatalf("ReadFile command log: %v", err)
			}
			log := string(logBytes)
			if strings.Contains(log, "RAW_GIT_WORKTREE_") {
				t.Fatalf("formula bypassed transactional owner:\n%s", log)
			}
			if !strings.Contains(log, "gc worktree ensure") {
				t.Fatalf("formula did not invoke transactional owner:\n%s", log)
			}
			if strings.Count(log, "gc worktree cleanup") != 3 {
				t.Fatalf("formula cleanup did not retain dirty WIP and retry through transactional owner:\n%s", log)
			}
			updateLine := lineContaining(log, "gc bd update gc-work")
			for _, key := range []string{
				"gc.work_dir=", "work_dir=", "gc.work_branch=", "gc.worktree_repo=",
				"gc.worktree_root=", "gc.worktree_base_ref=", "gc.worktree_base_sha=",
				"gc.worktree_creator=", "gc.worktree_owner=", "gc.worktree_generation=",
				"gc.worktree_lifecycle=",
			} {
				if !strings.Contains(updateLine, key) {
					t.Fatalf("atomic metadata update missing %s:\n%s", key, updateLine)
				}
			}
			cleanupLines := linesContaining(log, "gc bd update gc-work --unset-metadata")
			if len(cleanupLines) != 2 {
				t.Fatalf("cleanup did not use one atomic metadata clear per attempt:\n%s", log)
			}
			for _, cleanupLine := range cleanupLines {
				for _, key := range []string{
					"gc.work_dir", "work_dir", "gc.work_branch", "gc.worktree_repo",
					"gc.worktree_root", "gc.worktree_base_ref", "gc.worktree_base_sha",
					"gc.worktree_creator", "gc.worktree_owner", "gc.worktree_generation",
					"gc.worktree_lifecycle",
				} {
					if !strings.Contains(cleanupLine, "--unset-metadata "+key) {
						t.Fatalf("atomic metadata cleanup missing %s:\n%s", key, cleanupLine)
					}
				}
			}
		})
	}
}

func workspaceSetupDescription(t *testing.T, nodes []beads.GraphApplyNode) string {
	t.Helper()
	for _, node := range nodes {
		if strings.Contains(node.Key, "workspace-setup") && strings.Contains(node.Description, "git fetch") {
			var step struct {
				Description string `json:"description"`
			}
			if json.Unmarshal([]byte(node.Description), &step) == nil && step.Description != "" {
				return step.Description
			}
			return node.Description
		}
	}
	t.Fatal("materialized plan has no workspace-setup attempt")
	return ""
}

func workspaceCleanupDescription(t *testing.T, nodes []beads.GraphApplyNode) string {
	t.Helper()
	for _, node := range nodes {
		description := node.Description
		var step struct {
			Description string `json:"description"`
		}
		if json.Unmarshal([]byte(node.Description), &step) == nil && step.Description != "" {
			description = step.Description
		}
		if strings.Contains(description, "gc worktree cleanup") {
			return description
		}
	}
	t.Fatal("materialized plan has no transactional worktree cleanup attempt")
	return ""
}

func worktreeCleanupBlocks(description string) []string {
	var cleanupBlocks []string
	for _, block := range fencedBashBlocks(description) {
		if strings.Contains(block, "gc worktree cleanup") {
			cleanupBlocks = append(cleanupBlocks, block)
		}
	}
	return cleanupBlocks
}

func fencedBashBlocks(description string) []string {
	const open = "```bash"
	const fenceClose = "```"
	var blocks []string
	for {
		start := strings.Index(description, open)
		if start < 0 {
			return blocks
		}
		description = description[start+len(open):]
		end := strings.Index(description, fenceClose)
		if end < 0 {
			return blocks
		}
		blocks = append(blocks, description[:end])
		description = description[end+len(fenceClose):]
	}
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

func lineContaining(text, needle string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return ""
}

func linesContaining(text, needle string) []string {
	var matches []string
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, needle) {
			matches = append(matches, line)
		}
	}
	return matches
}
