# Release Gate: Push Ownership Guard Assignee Fallback

Deploy bead: ga-j7y8e3
Reviewed work bead: ga-fip9ps.3
Reviewed source commit: 6a85f186a5daf0711882d5fd202325a9e14c9033
Deploy branch: deploy/ga-j7y8e3-gate
Base checked: origin/main at 98c30c2fe1fab09291685300198a37e7a4e4ab92

Note: docs/PROJECT_MANIFEST.md is not present in this checkout; the deployer
prompt release-gate criteria were applied.

## Gate Results

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 6 | Branch diverges cleanly from main | PASS | Evaluated first against the reviewed source commit with `git merge-tree --write-tree origin/main 6a85f186a5daf0711882d5fd202325a9e14c9033`; re-verified after the gate commit with `git merge-tree --write-tree origin/main HEAD`, which exited 0 with no conflicts. |
| 1 | Review PASS present | PASS | `bd show ga-fip9ps.3` contains `Automated review (gascity/reviewer) - VERDICT: PASS` for commit `6a85f186a5daf0711882d5fd202325a9e14c9033`. |
| 2 | Acceptance criteria met | PASS | Diff is limited to `scripts/push-ownership-guard.sh` and `scripts/test-push-ownership-guard.sh`. The guard now retries the full claim check against the session assignee bead only when the branch-encoded bead is terminal/closed; reassigned, rerouted, held, unreachable, timeout, and unparseable cases remain fail-closed. The fake `bd show` harness now supports id-specific responses for branch-vs-assignee fallback tests. |
| 3 | Tests pass | PASS | `scripts/test-push-ownership-guard.sh` passed 21/21. `go test ./scripts/...` passed. `shellcheck scripts/push-ownership-guard.sh scripts/test-push-ownership-guard.sh` passed. `gofmt -l scripts/` returned no files. `go vet ./...` passed. `make test-fast-parallel` passed all 8 fast shards. |
| 4 | No high-severity review findings open | PASS | Reviewer notes list only two informational non-blocking observations and state no security defects found. Unresolved HIGH finding count: 0. |
| 5 | Final branch is clean | PASS | Before writing this gate file, `git status --short --branch` in `/var/tmp/codex-gascity-ga-j7y8e3.mFa5R6` showed a clean `deploy/ga-j7y8e3-gate` worktree at the reviewed commit. Final clean status is checked after committing this gate file. |
| 7 | Single feature theme | PASS | Single commit touches one subsystem: the push ownership guard and its dedicated test harness. No independent feature themes are bundled. |

## Acceptance Checks

- New fallback allow case is covered by `test_allow_fallback_when_branch_bead_closed_but_assignee_bead_valid`.
- Invalid fallback candidate still blocks via `test_block_when_branch_bead_closed_and_assignee_bead_also_invalid`.
- Existing stale-claim protections remain covered by the closed, reassigned, rerouted, hold, bd-unreachable, timeout, and hook push tests.
- Branch mismatch behavior remains warning-only, while branch-name resolution still wins unless the branch bead specifically fails due to terminal status.

## Command Evidence

```text
scripts/test-push-ownership-guard.sh
pass=21 fail=0

go test ./scripts/...
ok  	github.com/gastownhall/gascity/scripts	15.850s
ok  	github.com/gastownhall/gascity/scripts/cipolicy	(cached)

shellcheck scripts/push-ownership-guard.sh scripts/test-push-ownership-guard.sh
PASS

gofmt -l scripts/
PASS (no output)

go vet ./...
PASS

make test-fast-parallel
All fast jobs passed
```
