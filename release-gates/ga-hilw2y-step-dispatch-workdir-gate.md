# Release Gate: step-dispatch per-step workdir creation

Date: 2026-07-21
Deployer: gascity/deployer
Deploy bead: `ga-hilw2y`
Source implementation bead: `ga-zogqc1.1`
Source review bead: `ga-rcj2zt`

## Candidate

- Source branch: `builder/ga-zogqc1.1-per-step-workdir`
- Original reviewed source commit: `82c7c5bf536fe40f910fcdeb9a130ac2f22dbecd`
- Rebased source commit after helper attempt: `8a42eb6b4826b2322c434d0a94958625f4e9dcb5`
- Local branch tip after helper attempt: `9e137efe557393577bb1763dc075bec8a9048189`
- Remote branch tip after helper attempt: `6db9495b8b598529dcbddee85b8a6a935b87e775`
- Base checked: `origin/main` at `944d117d507fb7595d5bae961decde16ae46f0eb`
- Reviewer-visible code delta:
  - `cmd/gc/session_lifecycle_parallel.go`
  - `cmd/gc/session_lifecycle_parallel_test.go`

## Gate Inputs

- `docs/PROJECT_MANIFEST.md` is not present in this worktree; release criteria were evaluated against the deployer role's explicit seven-point release gate.
- `TESTING.md` was consulted for the local test-runner convention. The test gate was not reached because criterion 6 failed first.
- `.worktree-stale` is not present in the deployer worktree.
- The working tree for `builder/ga-zogqc1.1-per-step-workdir` was clean before the bounded helper ran.

## Gate Results

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 6 | Branch diverges cleanly from main | FAIL | Evaluated first. `origin/main` had moved past the previously pushed builder tip, so the required bounded helper was invoked from the clean internally owned branch with `HOME=/home/jaword GOFLAGS=-parallel=4 bash -lc '. scripts/rebase-resolve-lib.sh; attempt_bounded_self_rebase "builder/ga-zogqc1.1-per-step-workdir" main'`. The helper returned `RC=13`, whose contract is "rebased cleanly but the force-with-lease push was rejected." Local `HEAD` became `9e137efe557393577bb1763dc075bec8a9048189`; the remote branch remained `6db9495b8b598529dcbddee85b8a6a935b87e775`. Per deployer guardrail, any helper result other than 0 or 20 is a criterion-6 gate failure and routes back to builder. |
| 1 | Review PASS present | SKIPPED | Fail-fast after criterion 6. The bead carries reviewer PASS context from `ga-rcj2zt`, but this criterion is not counted because the gate stopped on criterion 6. |
| 2 | Acceptance criteria met | SKIPPED | Fail-fast after criterion 6. |
| 3 | Tests pass | SKIPPED | Fail-fast after criterion 6; no release-gate test run was started. |
| 4 | No high-severity review findings open | SKIPPED | Fail-fast after criterion 6. |
| 5 | Final branch is clean | SKIPPED | Fail-fast after criterion 6. The target worktree was clean before writing this checklist. |
| 7 | Single feature theme | SKIPPED | Fail-fast after criterion 6. The code delta is limited to session lifecycle workdir creation and its tests, but this criterion is not counted because the gate stopped on criterion 6. |

## Validation

- `git rev-list --left-right --count origin/main...HEAD` after the helper attempt: `0 3`.
- `git diff --name-only origin/main...HEAD` after the helper attempt:
  - `cmd/gc/session_lifecycle_parallel.go`
  - `cmd/gc/session_lifecycle_parallel_test.go`
  - `release-gates/ga-hilw2y-step-dispatch-workdir-gate.md`
- `git ls-remote origin refs/heads/builder/ga-zogqc1.1-per-step-workdir` after the helper attempt: `6db9495b8b598529dcbddee85b8a6a935b87e775`.

## Deploy Decision

FAIL. Do not push a deploy branch or open a PR. Route `ga-hilw2y` back to builder with `ready-to-build` so the branch owner can refresh/push the rebased branch state and return it for a full release-gate pass.
