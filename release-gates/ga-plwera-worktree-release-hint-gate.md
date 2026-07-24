# Release Gate: optional worktree release hint

Deploy bead: ga-plwera
Source bead: ga-vzt5pq.2
Reviewed source: fe0d831ce9df79bc811fd117cd0815b04b72e2c0
Source branch: builder/ga-vzt5pq.2 (provenance only)

## Summary

This single-bead release adds an optional `gc.work_dir_released_at` metadata hint
when `workflow-finalize` completes with a Pass outcome and the workflow root
carries `gc.work_dir`. The hint is best-effort and non-authoritative; the
borrow-veto reaper scan remains the safety gate before any worktree is reclaimed.

`docs/PROJECT_MANIFEST.md` is not present in this gascity worktree or rig root;
the gate uses the deployer prompt's seven release criteria.

## Criteria

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 6 | Branch diverges cleanly from main | PASS | Evaluated first. `git fetch origin main` succeeded. `git merge-tree --write-tree origin/main fe0d831ce9df79bc811fd117cd0815b04b72e2c0` exited 0 and produced tree `fad20f8caf5afb079212f8d4e04e05bfa08ae2b9`, so the reviewed source merges with current `origin/main` without conflicts. No self-rebase was attempted. |
| 1 | Review PASS present | PASS | Deploy bead `ga-plwera` says reviewer `gascity/reviewer` reviewed and PASSED. Source bead `ga-vzt5pq.2` notes contain `REVIEWER VERDICT: PASS`. Review step `ga-laiw55` is closed with "Review complete" and records build/vet/test evidence. |
| 2 | Acceptance criteria met | PASS | The reviewed diff from merge base `d5fbb58c983251bfe9df8c53be1b86ab6bef6408` to `fe0d831c` touches exactly `internal/beadmeta/keys.go`, `internal/dispatch/runtime.go`, and `internal/dispatch/runtime_test.go`. It adds `beadmeta.WorkDirReleasedAtMetadataKey`, registers it in `KnownMetadataKeys`, stamps it only after `workflow-finalize` resolves `OutcomePass`, no-ops when root `gc.work_dir` is empty, and swallows/traces read/write errors. |
| 3 | Tests pass | PASS | `go build ./...` passed. `go vet ./...` passed. `go test -count=1 ./internal/dispatch/... ./internal/beadmeta/...` passed. Smoke `go test -count=1 -v ./internal/dispatch -run '^TestProcessWorkflowFinalize(StampsWorkDirReleasedAtOnPass|DoesNotStampWorkDirReleasedAtOnFailure|SkipsWorkDirReleasedAtWhenNoWorkDir|WorkDirReleaseStampFailureDoesNotAbortFinalize)$'` passed all four new tests. `gofmt -l internal/beadmeta/keys.go internal/dispatch/runtime.go internal/dispatch/runtime_test.go` produced no output. `git diff --check d5fbb58c983251bfe9df8c53be1b86ab6bef6408..fe0d831ce9df79bc811fd117cd0815b04b72e2c0` produced no output. Broad `make test-fast-parallel` was run and failed only in `cmd/gc` tests outside the diff: deterministic main-baseline `TestErrorReturningSessionProviderFactoriesPreserveSuccessBehavior/default` (tracked as `ga-y4se3w`, reproduced alone) and contention flake `TestCityRuntimeRun_PanicInStartupDoesNotShutdownCity` (tracked as `ga-oe89yz` / `ga-1ah24y`, passed alone). No changed-package or feature-smoke test failed. |
| 4 | No high-severity review findings open | PASS | Source bead notes record "OWASP SECURITY WALK - no findings" and no minor blocking observations. `bd search ga-vzt5pq.2` returned only the deploy bead. No linked HIGH finding bead is open for this change. |
| 5 | Final branch is clean | PASS | Reviewed source was clean at `fe0d831c` before writing this gate. After committing the gate file, `git status --porcelain=v1` produced no output and `core.hooksPath` was `.githooks`. |
| 7 | Single feature theme | PASS | The commit set is two commits under one subsystem theme: workflow-finalize emits a non-authoritative worktree release hint and tests its Pass-only/best-effort semantics. It does not include independent user-facing behavior or unrelated packages. |

## Test Runs

| Command | Result |
|---|---|
| `go build ./...` | PASS |
| `go vet ./...` | PASS |
| `go test -count=1 ./internal/dispatch/... ./internal/beadmeta/...` | PASS |
| `go test -count=1 -v ./internal/dispatch -run '^TestProcessWorkflowFinalize(StampsWorkDirReleasedAtOnPass|DoesNotStampWorkDirReleasedAtOnFailure|SkipsWorkDirReleasedAtWhenNoWorkDir|WorkDirReleaseStampFailureDoesNotAbortFinalize)$'` | PASS |
| `gofmt -l internal/beadmeta/keys.go internal/dispatch/runtime.go internal/dispatch/runtime_test.go` | PASS, no output |
| `git diff --check d5fbb58c983251bfe9df8c53be1b86ab6bef6408..fe0d831ce9df79bc811fd117cd0815b04b72e2c0` | PASS, no output |
| `make test-fast-parallel` | Baseline exception: failed only in known unrelated `cmd/gc` tests tracked as `ga-y4se3w`, `ga-oe89yz`, and `ga-1ah24y`; changed-package tests and feature smoke passed. |

## Reviewed Commits

| Commit | Purpose |
|---|---|
| bcf686889a7f34140a27783ecad9187bb6824338 | RED tests for the workflow-finalize worktree release hint. |
| fe0d831ce9df79bc811fd117cd0815b04b72e2c0 | GREEN implementation for `gc.work_dir_released_at`. |
