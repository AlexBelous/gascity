# Release Gate: ga-vnw04q dead-assignee pool-wake fallback

Evaluated: 2026-07-24T23:20:00Z

- Deploy bead: `ga-vnw04q`
- Source bead: `ga-nnjcuc.1`
- Source branch: `builder/ga-nnjcuc.1` (provenance only)
- Candidate commit: `5f977aa23e762e95bc556fd1dae8539df3734ebb`
- Base checked: `origin/main` at `bd7c9dac0b305b94893f3382f66cb129536d1be4`
- Deploy branch: `deploy/ga-vnw04q-gate`
- Release criteria source: deployer gate prompt. `docs/PROJECT_MANIFEST.md` is not present in this checkout.

## Summary

This gate evaluates the dead-assignee pool-wake fallback. The change teaches
pool demand calculation to treat ready work assigned to a closed/dead session
as demand for that session's configured template when the mapping is
unambiguous, so a pool can wake a replacement worker instead of leaving the
work stranded.

The candidate is the split single-theme branch from the previously bundled
`ga-nnjcuc` work. The independent push-ownership-guard regex fix is excluded
from this branch and ships separately.

## Gate Results

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 6 | Branch diverges cleanly from main | PASS | `git merge-tree --write-tree origin/main 5f977aa23e762e95bc556fd1dae8539df3734ebb` returned 0 and produced merged tree `483c46c2f3d9e5caffa528c585a9a8881bd758f3`. `git diff --check origin/main...5f977aa23e762e95bc556fd1dae8539df3734ebb` produced no output. |
| 1 | Review PASS present | PASS | Deploy bead `ga-vnw04q` records reviewer PASS. Source bead `ga-nnjcuc.1` notes contain `REVIEWER VERDICT: PASS` and `Verdict: PASS`, with reviewer evidence for diff scope, tests, and security review. |
| 2 | Acceptance criteria met | PASS | Candidate starts from current `origin/main`, contains only the dead-assignee pool-wake fallback theme, excludes the push-ownership-guard files, records branch/commit/test evidence, and preserves the source dependency trail. The implementation maps unambiguous closed-session assignees back to configured templates and keeps route-template precedence. |
| 3 | Tests pass | PASS | Focused `cmd/gc` acceptance tests passed; `go build ./...` passed; `go vet ./...` passed; `make test-fast-parallel` passed all 8 fast jobs. |
| 4 | No high-severity review findings open | PASS | Reviewer notes disclose one non-blocking scalability concern tracked separately as `ga-cge2ii`. `bd list --status open --limit 0 | rg -i 'ga-vnw04q|ga-nnjcuc\.1|HIGH|request-changes'` found only sling helper beads, not open HIGH/request-changes findings. |
| 5 | Final branch is clean | PASS | Gate evidence is committed on `deploy/ga-vnw04q-gate`; `git status --short` is clean after the gate commit. |
| 7 | Single feature theme | PASS | The commit set is one release theme: dead-assignee pool-wake fallback. The diff is scoped to `cmd/gc` demand/pool state code and its tests. |

## Commit Set

| Commit | Summary |
|--------|---------|
| `57a9045be` | `test: cover dead assignee stranded demand` |
| `566a679b3` | `test(pool): red - dead-assignee pool-wake fallback (refs ga-o3ko1j.4.3)` |
| `dff4334d8` | `chore(test): drop FR-5 payload test superseded by closed sibling ga-o3ko1j.4.4` |
| `5f977aa23` | `feat: green - Implement dead-assignee pool-wake fallback (refs ga-o3ko1j.4.3)` |

## Commands Run

```text
gc prime
gh auth status
bd show ga-vnw04q
bd show ga-nnjcuc.1
git fetch origin main --quiet
git merge-tree --write-tree origin/main 5f977aa23e762e95bc556fd1dae8539df3734ebb
git diff --check origin/main...5f977aa23e762e95bc556fd1dae8539df3734ebb
git log --oneline $(git merge-base origin/main 5f977aa23e762e95bc556fd1dae8539df3734ebb)..5f977aa23e762e95bc556fd1dae8539df3734ebb
git diff --stat $(git merge-base origin/main 5f977aa23e762e95bc556fd1dae8539df3734ebb)..5f977aa23e762e95bc556fd1dae8539df3734ebb
TMPDIR=/var/tmp env -u GC_AGENT -u GC_ALIAS -u GC_TEMPLATE go test ./cmd/gc/... -run 'TestFilterAssignedWorkBeadsForPoolDemand|TestBuildDesiredState|TestDeadAssignee' -count=1 -v
TMPDIR=/var/tmp env -u GC_AGENT -u GC_ALIAS -u GC_TEMPLATE go build ./...
TMPDIR=/var/tmp env -u GC_AGENT -u GC_ALIAS -u GC_TEMPLATE go vet ./...
TMPDIR=/var/tmp env -u GC_AGENT -u GC_ALIAS -u GC_TEMPLATE make test-fast-parallel
bd list --status open --limit 0 | rg -i -- 'ga-vnw04q|ga-nnjcuc\.1|HIGH|request-changes'
```

## Test Output Summary

```text
Focused cmd/gc acceptance suite:
PASS
ok  	github.com/gastownhall/gascity/cmd/gc	2.043s

go build ./...
PASS

go vet ./...
PASS

make test-fast-parallel:
[fsys-darwin-compile] ok
[unit-cmd-gc-1-of-6] ok
[unit-cmd-gc-2-of-6] ok
[unit-cmd-gc-3-of-6] ok
[unit-cmd-gc-4-of-6] ok
[unit-cmd-gc-5-of-6] ok
[unit-cmd-gc-6-of-6] ok
[unit-core] ok
All fast jobs passed
```

## Acceptance Mapping

| Done-when item | Result | Evidence |
|---|---|---|
| Candidate starts from current `origin/main` | PASS | Merge base for the candidate is `bac288647e0bbbbe2e68bdbe588709eb2827f5ee`, and the candidate merges cleanly into current `origin/main` `bd7c9dac0`. |
| Only dead-assignee fallback behavior is included | PASS | Diff is limited to `cmd/gc/assigned_work_scope.go`, `cmd/gc/assigned_work_scope_test.go`, `cmd/gc/build_desired_state.go`, `cmd/gc/city_runtime.go`, `cmd/gc/cmd_start.go`, `cmd/gc/dead_assignee_demand.go`, `cmd/gc/dead_assignee_stranded_demand_test.go`, and `cmd/gc/pool_desired_state.go`. |
| Push-ownership-guard regex fix is excluded | PASS | `scripts/push-ownership-guard.sh` and `scripts/test-push-ownership-guard.sh` are absent from the candidate diff. |
| Branch, commit, changed files, and test evidence recorded | PASS | Recorded in this gate file and in source bead `ga-nnjcuc.1`. |
| Dependency on final validation trail is preserved | PASS | Source bead notes document the preserved `ga-o3ko1j.4.5` dependency trail; deploy branch does not alter bead dependency metadata. |
| Fresh review/deploy handoff exists | PASS | Source bead `ga-nnjcuc.1` has reviewer PASS and created this deploy bead, `ga-vnw04q`. |
