# Release Gate: step-dispatch per-step workdir creation

Date: 2026-07-19
Deployer: gascity/deployer
Deploy bead: `ga-hilw2y`
Source implementation bead: `ga-zogqc1.1`
Source review bead: `ga-rcj2zt`

## Candidate

- PR branch: `builder/ga-zogqc1.1-per-step-workdir`
- Reviewed source commit: `82c7c5bf536fe40f910fcdeb9a130ac2f22dbecd`
- Branch HEAD after bounded self-rebase attempt: `cd0c0ee3e63b760af21d4a766860303abc0a9b56`
- Base checked: `origin/main` at `6632416c70f7382dd19e7b2f717319837181b171`
- Reviewer-visible delta:
  - `cmd/gc/session_lifecycle_parallel.go`
  - `cmd/gc/session_lifecycle_parallel_test.go`

## Gate Inputs

- `docs/PROJECT_MANIFEST.md` is not present in this worktree; release criteria were evaluated against the deployer role's explicit seven-point release gate.
- `TESTING.md` was read before selecting the local runner. The test gate was not reached because criterion 6 failed first.
- `.worktree-stale` is not present in the target branch worktree.

## Gate Results

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 6 | Branch diverges cleanly from main | FAIL | Evaluated first. Before repair, `git merge-base --is-ancestor origin/main builder/ga-zogqc1.1-per-step-workdir` returned non-zero while `git rev-list --left-right --count origin/main...builder/ga-zogqc1.1-per-step-workdir` reported `5 1`. The required bounded helper was invoked from a clean checkout with `source scripts/rebase-resolve-lib.sh; attempt_bounded_self_rebase builder/ga-zogqc1.1-per-step-workdir main` and returned `RC=13`. Its contract defines `13` as "rebased cleanly but the force-with-lease push was rejected"; a follow-up dry-run captured the concrete push rejection: `stale info` for `builder/ga-zogqc1.1-per-step-workdir -> builder/ga-zogqc1.1-per-step-workdir`. Per deployer guardrail, any non-zero helper result routes back to builder and stops the gate. |
| 1 | Review PASS present | SKIPPED | Fail-fast after criterion 6. Review context was read before the criterion-6 repair attempt; `ga-rcj2zt` is closed with `VERDICT: PASS` for the reviewed commit, but this criterion is not counted because the gate stopped on criterion 6. |
| 2 | Acceptance criteria met | SKIPPED | Fail-fast after criterion 6. |
| 3 | Tests pass | SKIPPED | Fail-fast after criterion 6; no release-gate test run was started on this branch. |
| 4 | No high-severity review findings open | SKIPPED | Fail-fast after criterion 6. |
| 5 | Final branch is clean | SKIPPED | Fail-fast after criterion 6. The target worktree was clean before the helper invocation and before writing this checklist. |
| 7 | Single feature theme | SKIPPED | Fail-fast after criterion 6. The visible delta is limited to `cmd/gc/session_lifecycle_parallel.go` and `cmd/gc/session_lifecycle_parallel_test.go`, but this criterion is not counted because the gate stopped on criterion 6. |

## Validation

- `git merge-tree --write-tree origin/main builder/ga-zogqc1.1-per-step-workdir` - produced tree `30c866313e28fb3389b814cccb094193a281dd39` with no content conflicts before the helper attempt.
- `source scripts/rebase-resolve-lib.sh; attempt_bounded_self_rebase builder/ga-zogqc1.1-per-step-workdir main` - FAIL, returned `RC=13`.
- `GIT_TERMINAL_PROMPT=0 git push --dry-run --force-with-lease origin builder/ga-zogqc1.1-per-step-workdir` - FAIL, rejected with `stale info`.

## Deploy Decision

FAIL. Do not push or open a PR. Route `ga-hilw2y` back to builder with `ready-to-build` so the branch can be refreshed and pushed from the owner seat.
