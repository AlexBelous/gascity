# Release Gate: push-ownership guard reconciliation

- Deploy bead: `ga-1hc9k3`
- Source/review lineage: `ga-z3bhzw` / `ga-0jpfvu`
- Reviewed commit: `b38534a4b394dd77c47e69f3438e7d2928b9d505`
- Deploy branch: `deploy/ga-1hc9k3-gate`
- Base checked: `origin/main` at `25eb009e87b1b2a9b3b5031615996120562cb0b8`
- Evaluated: 2026-07-25

`docs/PROJECT_MANIFEST.md` is not present in this checkout, so this gate uses
the deployer role's release criteria plus the repository gates in `AGENTS.md`
and `TESTING.md`.

## Summary

PASS. This single-theme change reconciles the push ownership guard's
assignee-fallback retry with its multi-level bead-ID parsing. The guard remains
fail-closed and both callers retain the unchanged
`assert_bead_still_claimed` contract.

## Criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| 6 | Branch diverges cleanly from main | PASS | Evaluated first. `git fetch origin main`; `git merge-tree --write-tree origin/main b38534a4b` exited 0 and produced tree `4c9993200d10d03acae637ed31b2124b0fab2e75`; `git diff --check origin/main...b38534a4b` produced no output. No self-rebase was required. |
| 1 | Review PASS present | PASS | Closed review bead `ga-0jpfvu` records `Verdict: PASS` for exact commit `b38534a4b394dd77c47e69f3438e7d2928b9d505`. |
| 2 | Acceptance criteria met | PASS | `git diff --exit-code 4cd597c9d b38534a4b -- scripts/push-ownership-guard.sh scripts/test-push-ownership-guard.sh` exited 0, proving zero semantic drift from the previously reviewed implementation. The diff retains `_pog_branch_bead_id`, `_pog_assignee_bead_id`, and `_pog_check_bead_claimed`; uses the repeatable `ga-[0-9a-z]{6}(\.[0-9]+)*` suffix; retries only when the primary branch bead is terminal; and keeps both callers on `assert_bead_still_claimed`. The dedicated guard and Layer-B suites passed 22/22 and 23/23. |
| 3 | Tests pass | PASS | On the exact reviewed SHA: Bash syntax and ShellCheck passed; `go test ./scripts/...` passed; the two previously flaky regressions passed together; `go build ./...` passed; `go vet ./...` passed; and the first `make test-fast-parallel` attempt passed all 8 fast jobs. |
| 4 | No high-severity review findings open | PASS | `ga-0jpfvu` is closed with a PASS verdict and states that there are no open questions on the diff. The carried-forward security/spec/coverage review reported no blocker or coverage gap. |
| 5 | Final branch is clean | PASS | Before adding this checklist, `git status --porcelain=v1` produced no output at the reviewed SHA. This checklist is committed as the only deployer-added change before push. |
| 7 | Single feature theme | PASS | `git diff --name-status origin/main...b38534a4b` lists only `scripts/push-ownership-guard.sh` and `scripts/test-push-ownership-guard.sh`; both belong to the push ownership guard subsystem. |

## Test Log Summary

- `bash -n scripts/push-ownership-guard.sh scripts/test-push-ownership-guard.sh scripts/rebase-resolve-lib.sh scripts/test-rebase-resolve.sh`: PASS
- `shellcheck scripts/push-ownership-guard.sh scripts/test-push-ownership-guard.sh scripts/rebase-resolve-lib.sh scripts/test-rebase-resolve.sh`: PASS
- `bash scripts/test-push-ownership-guard.sh`: PASS, 22 passed and 0 failed
- `bash scripts/test-rebase-resolve.sh`: PASS, 23 passed and 0 failed
- `go test ./scripts/...`: PASS
- `go test ./cmd/gc -run '^(TestCmdStopWallClockTimeoutBoundsDirectStop|TestCityRuntimeRun_PanicInStartupDoesNotShutdownCity)$' -count=1`: PASS
- `go build ./...`: PASS
- `go vet ./...`: PASS
- `make test-fast-parallel`: PASS, 8/8 fast jobs passed on the first attempt

## Supplemental evaluation: guard header refresh

- Deploy bead: `ga-qvl7oc`
- Review bead: `ga-jkkrbe`
- Reviewed commit: `2ebc7803491a0a5c35738d70ab08c3cc63763a2b`
- Parent / prior PR head: `19141edb18306dffcc009c04aa10683e0bcaa676`
- Base checked: `origin/main` at `bbb7354377996e553e1e1dd4821ade2afd6ab1a1`
- Evaluated: 2026-07-25

PASS. The reviewed follow-up refreshes only the header comments in
`scripts/push-ownership-guard.sh` so they describe the existing
branch-bead/assignee-bead retry contract. It intentionally documents, without
resolving, the open authorization-semantics question tracked on `ga-1hc9k3`.
The sibling mutation-proof test (`ga-3vvi6r`) is not included in this
supplement because it has not completed builder/reviewer routing.

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| 6 | Branch diverges cleanly from main | PASS | Evaluated first. The reviewed commit is a direct child of the prior PR head. `git merge-tree --write-tree origin/main 2ebc78034` exited 0 and produced tree `b7b2cd3f4e75c945761901508fa6d95c338429e0`; `git diff --check` produced no output. |
| 1 | Review PASS present | PASS | Closed review bead `ga-jkkrbe` records `REVIEW VERDICT: PASS` for exact commit `2ebc7803491a0a5c35738d70ab08c3cc63763a2b`. |
| 2 | Acceptance criteria met | PASS | An executable-line diff check confirmed every added/removed line is a comment or blank line. The refreshed header accurately documents branch-first resolution, the terminal-status-only (`rc=2`) fallback, the non-retryable `rc=1` states, and the intentionally unresolved lack of a relatedness check. |
| 3 | Tests pass | PASS | On the exact reviewed SHA: Bash syntax and ShellCheck passed; the ownership-guard suite passed 22/22; `go test ./scripts/...`, `go build ./...`, and `go vet ./...` passed; and the first `make test-fast-parallel` attempt passed all 8 fast jobs. |
| 4 | No high-severity review findings open | PASS | `ga-jkkrbe` is closed with a PASS verdict, reports no inaccuracies or scope creep, and identifies no security finding because executable behavior is unchanged. |
| 5 | Final branch is clean | PASS | `git status --porcelain=v1` produced no output at the reviewed SHA before this gate supplement was added. |
| 7 | Single feature theme | PASS | The supplemental commit touches only the push ownership guard's header; the existing PR remains confined to the same guard and its tests. |

## Supplemental evaluation: mutation-proof retry test

- Deploy bead: `ga-2bf7oo`
- Review bead: `ga-r7c5gl`
- Reviewed commit: `3391c40f2fb399add86aa18831a1e7287885899d`
- Parent / prior PR head: `54170bf9c4e801c1f10405a06bbe103340c5f4ac`
- Combined candidate: `d2b354aafec461dd0d88343971e0986158eb62dc`
- Base checked: `origin/main` at `8a6809e8fa6dc71faaa04e6a5ee4822dce130275`
- Evaluated: 2026-07-26

PASS. The reviewed follow-up adds three regression cases that prove a
reassigned, rerouted, or held branch bead cannot be authorized by a distinct
valid fallback assignment. It pins the current `rc=2`-only retry invariant
without changing the guard itself or resolving the separate
authorization-relatedness design question on `ga-1hc9k3`.

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| 6 | Branch diverges cleanly from main | PASS | Evaluated first after applying the reviewed patch to the live PR head. `git merge-tree --write-tree origin/main d2b354aaf` exited 0 and produced tree `095611af5412988c901a7e4df70a5d8cb2e949d9`; `git diff --check` produced no output. |
| 1 | Review PASS present | PASS | Closed review bead `ga-r7c5gl` records `REVIEW VERDICT: PASS` for exact source commit `3391c40f2fb399add86aa18831a1e7287885899d`. The reviewer also applied that test file to the PR lineage after GAP 2 (`2ebc78034`) and independently re-ran both the unmutated and mutation-proof checks on the combined tree. The patch cherry-picked without conflict onto the live PR head as `d2b354aaf`. |
| 2 | Acceptance criteria met | PASS | The delta is exactly 60 additive lines in `scripts/test-push-ownership-guard.sh`: three cases cover reassigned, rerouted, and `hold:mayor` branch beads while a distinct valid fallback exists. The unchanged guard passes all 25 cases. The review independently proved the test's sensitivity by changing the retry condition from `rc -eq 2` to `rc -ne 0`: exactly the three new cases failed and all 22 prior cases passed. |
| 3 | Tests pass | PASS | On combined candidate `d2b354aaf`: Bash syntax and ShellCheck passed; the ownership-guard suite passed 25/25; `go test ./scripts/...`, `go build ./...`, `go vet ./...`, and `make test-fast-parallel` passed. |
| 4 | No high-severity review findings open | PASS | `ga-r7c5gl` is closed with a PASS verdict and reports no inaccuracies, scope creep, security finding, or coverage gap. PR #4651 had zero comments and zero reviews when this supplement was evaluated. |
| 5 | Final branch is clean | PASS | `git status --porcelain=v1` produced no output at combined candidate `d2b354aaf` before this gate supplement was added. |
| 7 | Single feature theme | PASS | The supplemental patch touches only the push ownership guard's existing shell regression suite; PR #4651 remains confined to the same guard subsystem and its tests. |
