# Release gate: push-ownership guard reconciliation

- Deploy bead: `ga-5r2m5t`
- Source bead: `ga-z3bhzw`
- Reviewed commit: `4cd597c9da5ace340e3636e4ddb26608aba47e2e`
- Evaluation date: 2026-07-24
- Verdict: **FAIL**

Criterion 6 was evaluated first, as required. The remaining criteria were
evaluated only after the reviewed commit proved conflict-free with current
`origin/main`.

| # | Criterion | Result | Evidence |
|---|---|---|---|
| 1 | Review PASS present | **PASS** | `ga-z3bhzw` notes contain `=== REVIEWER VERDICT: PASS ===` from `gascity/reviewer` for reviewed commit `4cd597c9da5ace340e3636e4ddb26608aba47e2e`. The reviewer independently ran the focused guard, Layer-B, build, vet, static, and scope checks. |
| 2 | Acceptance criteria met | **PASS** | The diff is limited to `scripts/push-ownership-guard.sh` and `scripts/test-push-ownership-guard.sh` (193 insertions, 75 deletions). The three-function split, rc=2-only assignee fallback, multi-level bead-ID regex `(\.[0-9]+)*`, and all three required regression tests are present. Focused verification passed: guard `22/22`, Layer-B resolver `23/23`, `go test ./scripts/...`, `shellcheck`, `gofmt`, dangling-reference scan, `go build ./...`, and `go vet ./...`. |
| 3 | Tests pass | **FAIL** | `make test-fast-parallel` did not pass. The nested worktree run reproduced the tracked ambient-city hermeticity defect `ga-y4se3w` / `ga-6mi6p2` in `TestErrorReturningSessionProviderFactoriesPreserveSuccessBehavior/default`. On the identical reviewed SHA in a clean `/var/tmp` worktree, that focused test passed 5/5, but the full fast suite still failed in `TestCmdStopWaitsForStandaloneControllerExit` (`unit-cmd-gc-3-of-6`, timed out waiting for a stop) and `TestProxyProcessDisablesProductMetrics/absent_opt-out_is_added` (`unit-core`, `ServeHTTP returned false, want true`). Six of eight fast jobs passed. No approved waiver was found for the two clean-worktree failures, so the worst result remains FAIL under `TESTING.md`. Logs: `/var/tmp/gc-local-tests.nSG7ee/` and `/var/tmp/gc-local-tests.1ffU7W/`. |
| 4 | No high-severity review findings open | **PASS** | The reviewer recorded a full PASS after an OWASP/security walk and reported no coverage gap or unresolved high-severity finding. Unresolved HIGH count: 0. |
| 5 | Final branch is clean | **PASS** | The reviewed source tree was clean before this checklist was created. This checklist is the only deployer-authored artifact on the local isolated gate branch. |
| 6 | Branch diverges cleanly from main | **PASS** | Against `origin/main@4873ef3d59da36afa8b7e6c009f8ebc0551af713`, `git merge-tree --write-tree --messages origin/main 4cd597c9d...` exited 0 and produced tree `da1fd0316b17091c9260c83bdba1dc122db4e536`. Merge base: `80e5166473033b9f2807dad048ddcb70dfc3b86e`; divergence: main 56 commits, reviewed branch 2 commits. No self-rebase was needed. |
| 7 | Single feature theme | **PASS** | Both commits and both changed files belong to one subsystem and one behavior: push-ownership validation plus its regression harness. |

## Disposition

Criterion 3 is a technical gate failure. Do not push this gate branch and do
not open a pull request. Return `ga-5r2m5t` to `gascity/builder` with the
failing test names and log paths above.
