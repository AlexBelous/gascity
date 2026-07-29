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
	searchDir := filepath.Join(filepath.Clean(filepath.Join(cwd, "..", "..")), "internal", "bootstrap", "packs", "core", "formulas")

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

			repo := filepath.Join(t.TempDir(), "repo")
			origin := filepath.Join(t.TempDir(), "origin.git")
			testutil.RunCommandAtProcessCWD(t, append(os.Environ(),
				"FORMULA_REPO="+repo,
				"FORMULA_ORIGIN="+origin,
			), "bash", "-euo", "pipefail", "-c", `
git init "$FORMULA_REPO"
git -C "$FORMULA_REPO" config user.email test@test.com
git -C "$FORMULA_REPO" config user.name Test
git -C "$FORMULA_REPO" commit --allow-empty -m init
git -C "$FORMULA_REPO" branch -M main
git init --bare "$FORMULA_ORIGIN"
git -C "$FORMULA_REPO" remote add origin "$FORMULA_ORIGIN"
git -C "$FORMULA_REPO" push -u origin main
`)

			logPath := filepath.Join(t.TempDir(), "commands.log")
			binDir := t.TempDir()
			realGit := testutil.LookPath(t, "git")
			writeExecutable(t, filepath.Join(binDir, "git"), `#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "worktree" && "${2:-}" == "add" ]]; then
  printf 'RAW_GIT_WORKTREE_ADD %q\n' "$*" >> "$COMMAND_LOG"
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
  else
    printf '%s\n' '[{"metadata":{}}]'
  fi
  exit 0
fi
if [[ "${1:-}" == "worktree" && "${2:-}" == "ensure" ]]; then
  shift 2
  repo=""
  root=""
  path=""
  branch=""
  base_ref=""
  base_sha=""
  bead=""
  store_ref=""
  creator=""
  owner=""
  generation=""
  lifecycle=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --repo) repo="$2"; shift 2 ;;
      --root) root="$2"; shift 2 ;;
      --path) path="$2"; shift 2 ;;
      --branch) branch="$2"; shift 2 ;;
      --base) base_ref="$2"; shift 2 ;;
      --base-sha) base_sha="$2"; shift 2 ;;
      --bead) bead="$2"; shift 2 ;;
      --store-ref) store_ref="$2"; shift 2 ;;
      --creator) creator="$2"; shift 2 ;;
      --owner) owner="$2"; shift 2 ;;
      --generation) generation="$2"; shift 2 ;;
      --lifecycle) lifecycle="$2"; shift 2 ;;
      --json) shift ;;
      *) printf 'unexpected ensure argument: %s\n' "$1" >&2; exit 2 ;;
    esac
  done
  for value in "$repo" "$root" "$path" "$branch" "$base_ref" "$bead" "$store_ref" "$creator" "$owner" "$generation" "$lifecycle"; do
    [[ -n "$value" ]] || { printf 'incomplete ensure contract\n' >&2; exit 3; }
  done
  if [[ -z "$base_sha" ]]; then
    base_sha=$("$REAL_GIT" -C "$repo" rev-parse --verify "${base_ref}^{commit}")
  fi
  mkdir -p "$root"
  "$REAL_GIT" -C "$repo" worktree add -b "$branch" "$path" "$base_sha" >/dev/null
  jq -n \
    --arg path "$path" --arg branch "$branch" --arg bead "$bead" \
    --arg store_ref "$store_ref" --arg base_ref "$base_ref" --arg base_sha "$base_sha" \
    --arg creator "$creator" --arg owner "$owner" --arg generation "$generation" \
    --arg lifecycle "$lifecycle" \
    '{path:$path,branch:$branch,provenance:{bead_id:$bead,store_ref:$store_ref,base_ref:$base_ref,base_sha:$base_sha,creator:$creator,owner:$owner,generation:$generation,lifecycle:$lifecycle}}'
  exit 0
fi
if [[ "${1:-}" == "bd" && "${2:-}" == "update" ]]; then
  exit 0
fi
printf 'unexpected gc command: %s\n' "$*" >&2
exit 4
`)

			testutil.RunCommandAtProcessCWD(t, append(os.Environ(),
				"COMMAND_LOG="+logPath,
				"FORMULA_REPO="+repo,
				"GC_BEAD_ID=gc-step",
				"REAL_GIT="+realGit,
				"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
			), "bash", "-euo", "pipefail", "-c", `cd "$FORMULA_REPO"`+"\n"+script)
			logBytes, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatalf("ReadFile command log: %v", err)
			}
			log := string(logBytes)
			if strings.Contains(log, "RAW_GIT_WORKTREE_ADD") {
				t.Fatalf("formula bypassed transactional owner:\n%s", log)
			}
			if !strings.Contains(log, "gc worktree ensure") {
				t.Fatalf("formula did not invoke transactional owner:\n%s", log)
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
