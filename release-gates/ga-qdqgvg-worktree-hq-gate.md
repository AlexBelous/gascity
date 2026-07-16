# Release Gate: ga-qdqgvg - gc worktree hq

Gate evaluated: 2026-07-16T04:35-04:38 America/Los_Angeles

PR: https://github.com/gastownhall/gascity/pull/4243
Branch: `builder4/ga-gr9pm9.1-worktree-hq-v7`
Base: `origin/main` at `d1b7c04262e44a4eaef160feafb6c74675991022`
Head before gate artifact: `d90d7536d9d883c17d89c22fdca36929d480f0a6`
Head after gate artifact: this commit, the pushed branch tip containing this file

Note: `docs/PROJECT_MANIFEST.md` is not present in this checkout or on
`origin/main`; this gate uses the deployer runbook's seven release criteria.

## Gate Result

PASS

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 6 | Branch diverges cleanly from main | PASS | Before the gate artifact, `git rev-list --left-right --count origin/main...HEAD` returned `0 5` and GitHub reported `mergeable=MERGEABLE`, `mergeStateStatus=CLEAN`. After committing this gate artifact, `git rev-list --left-right --count origin/main...HEAD` returned `0 6` and `git merge-tree --write-tree origin/main HEAD` returned successfully. `git ls-remote origin refs/heads/main refs/heads/builder4/ga-gr9pm9.1-worktree-hq-v7` confirmed remote main and the PR branch had not moved during the gate. |
| 1 | Review PASS present | PASS | Review bead `ga-i4f22b` is closed with close reason `pass` and notes `VERDICT: PASS`; it explicitly covers PR #4243 and confirms the current file set matches the reviewed work plus mechanical regenerated artifacts. |
| 2 | Acceptance criteria met | PASS | Diff is one feature set: `gc worktree hq <bead-id>` command, HQ-bucket closed-bead worktree reaping, rig-scope redirect handling, and CWE-22 path traversal rejection. Evidence: 5 commits ahead of main; 17 files changed; focused tests listed below passed, including worktree HQ, reaper, rig-scope, path traversal, and resource-census ledger checks. |
| 3 | Tests pass | PASS | Local gate commands all passed: `go build ./...`; `go vet ./...`; `go test -count=1 ./cmd/gc/... -run 'Worktree|Reaper|RigScope|IsStrictlyUnderDir|WithHQBeadStore|ExtractBeadIDFromWorktreeName' -v`; `go test -count=1 ./internal/testpolicy/resourcecensus -run '^TestRepositoryLedgerMatchesCensusAndDocumentation$' -v`; `LOCAL_TEST_JOBS=16 make test-fast-parallel` (all 8 fast jobs passed). GitHub status rollup for head `d90d7536d9d883c17d89c22fdca36929d480f0a6` also shows required CI checks successful. |
| 4 | No high-severity review findings open | PASS | `ga-i4f22b` has no open HIGH findings; PR #4243 has no GitHub comments or reviews from external contributors; review notes record PASS with no requested changes. |
| 5 | Final branch is clean | PASS | Gate worktree was clean before writing this file (`git status --short --branch` printed only `## HEAD (no branch)`). After committing this gate artifact, `git status --short --branch` again printed only `## HEAD (no branch)`. |
| 7 | Single feature theme | PASS | All hand-written changes support the same worktree-HQ workflow and its cleanup/safety prerequisites. Generated metrics, schema, docs, and resource-ledger changes are mechanical consequences of adding the new command and tests; no independent feature theme is present. |

## Candidate Commit Set

| Commit | Purpose |
|---|---|
| `a87ad5c89` | Reap closed-bead worktrees under the HQ bucket too. |
| `e2e0e92f` | Treat a `.beads/redirect` to the city's own `.beads` as no-rig. |
| `6676e92b` | Add `gc worktree hq <bead-id>` for HQ-targeting bead work. |
| `7d2def79` | Reject path-traversal bead IDs in `gc worktree hq`. |
| `d90d7536` | Sync generated command census and resource-census ledger for the new command/tests. |

## Diff Scope

Candidate payload before adding this gate artifact, from
`git diff --stat origin/main...d90d7536d9d883c17d89c22fdca36929d480f0a6`:

```text
 TESTING.md                                   |   4 +-
 cmd/gc/bead_worktree_reaper.go               |  20 +++
 cmd/gc/bead_worktree_reaper_test.go          | 131 +++++++++++++++
 cmd/gc/city_runtime.go                       |   2 +-
 cmd/gc/cmd_worktree.go                       | 134 ++++++++++++++++
 cmd/gc/cmd_worktree_test.go                  | 231 +++++++++++++++++++++++++++
 cmd/gc/main.go                               |   1 +
 cmd/gc/metrics_census_gen.go                 |   3 +
 cmd/gc/productmetrics_command_census.json    |  35 +++-
 cmd/gc/rig_scope_resolution.go               |   7 +
 cmd/gc/rig_scope_resolution_test.go          |  73 +++++++++
 docs/reference/cli.md                        |  30 ++++
 internal/productmetrics/command_ids_gen.go   |   4 +-
 internal/productmetrics/event_test.go        |   4 +-
 internal/testpolicy/resourcecensus/census.go |   8 +-
 schemas/metrics/example/result.schema.json   |   3 +-
 test/test-resources.toml                     |   8 +-
 17 files changed, 682 insertions(+), 16 deletions(-)
```

## Test Evidence

```text
HOME=$(getent passwd "$(whoami)" | cut -d: -f6) go build ./...
PASS

HOME=$(getent passwd "$(whoami)" | cut -d: -f6) go vet ./...
PASS

HOME=$(getent passwd "$(whoami)" | cut -d: -f6) go test -count=1 ./cmd/gc/... -run 'Worktree|Reaper|RigScope|IsStrictlyUnderDir|WithHQBeadStore|ExtractBeadIDFromWorktreeName' -v
PASS

HOME=$(getent passwd "$(whoami)" | cut -d: -f6) go test -count=1 ./internal/testpolicy/resourcecensus -run '^TestRepositoryLedgerMatchesCensusAndDocumentation$' -v
PASS

HOME=$(getent passwd "$(whoami)" | cut -d: -f6) LOCAL_TEST_JOBS=16 make test-fast-parallel
All fast jobs passed
```
