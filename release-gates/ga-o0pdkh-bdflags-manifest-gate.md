# Release Gate: synchronize the bd flag manifest with bd v1.1.0

Deploy bead: ga-o0pdkh
Source implementation bead: ga-gqxh5s
Review bead: ga-i8rhmj
Post-review fix bead: ga-d5m4ac
Deploy branch: deploy/ga-o0pdkh-gate
Reviewed source: 3289b5673985f237fe655170162b89a715b84269
Current PR source: 570e38be5ce7cdf45cb0efc6a911c6fa70170e87
Reviewed branch: builder/ga-gqxh5s (provenance only)
Base checked: origin/main at b63623d08ecf565de82c226f0af1ca2fc359d45d
Merge base: cb456b85ecd923186a50493074f8b8a4c75d7eac
Release criteria source: active deployer gate criteria; docs/PROJECT_MANIFEST.md is not present on origin/main.

## Gate Summary

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 1 | Review PASS present | FAIL | Review bead ga-i8rhmj is closed with reason `pass`, but its verdict covers 3289b5673985f237fe655170162b89a715b84269. The current PR head adds post-review fix 570e38be5ce7cdf45cb0efc6a911c6fa70170e87; no reviewer PASS for that commit is recorded. |
| 2 | Acceptance criteria met | PASS | See the acceptance table below. The live manifest freshness test passed all 17 known bd subcommands against both relevant bd builds: the released v1.1.0 source at beads 8e4e59d39 and the fleet build 0954be416. |
| 3 | Tests pass | FAIL | `make test-fast-parallel` completed 9/10 jobs successfully and failed `unit-cmd-gc-4-of-6`: `TestCityRuntimeForceShutdownTearsDownAfterLateAsyncSweep` reported `force shutdown missed the late async-started runtime`. The failure is outside this diff, but TESTING.md forbids retrying a deterministic product-test failure into green. The focused `internal/bdflags` integration-tagged suite passed against each supported bd build with 53 PASS, 0 FAIL, 0 SKIP. `diff_tests_executed`: `TestGlobalValueFlagsIsComplete` PASS; `TestGlobalBoolFlagsIsComplete` PASS. `skip_justification`: none; zero skips. `waiver_ref`: none. |
| 4 | No high-severity review findings open | PASS | ga-i8rhmj records `style_findings: none`, `security_findings: none`, and no unresolved HIGH finding. The missing review coverage for 570e38be is recorded separately under criterion 1. |
| 5 | Final branch is clean | PASS | The isolated worktree at 570e38be5ce7cdf45cb0efc6a911c6fa70170e87 was clean before this checklist update; `git diff --check origin/main...HEAD` passed. |
| 6 | Branch diverges cleanly from main | PASS | The already-merged preflight found PR 5284 open, not merged. `git merge-tree --write-tree origin/main origin/deploy/ga-o0pdkh-gate` exited 0 and produced tree d2b460c7663ba0412a89d41b5266bdd0fc0533b3. No self-rebase was needed. |
| 7 | Single feature theme | PASS | The feature commits touch only `internal/bdflags/bdflags.go` and `internal/bdflags/bdargs_test.go`, updating one static CLI-flag manifest and its pinned completeness tests. |

## Acceptance Evidence

| Acceptance item | Result | Evidence |
|-----------------|--------|----------|
| Reflect every supported bd flag for all 17 known subcommands without removing real coverage. | PASS | `TestBdFlagManifestCurrent` passed all 17 subcommands against both supported builds. The released v1.1.0 source at beads 8e4e59d39 still exposes persistent `--profile`; fleet build 0954be416 exposes its newer `--cpu-profile` spelling. The manifest intentionally retains both as a compatibility superset. |
| Pass the live freshness contract. | PASS | `go test -count=1 -tags integration -json ./internal/bdflags/...` completed 53 PASS, 0 FAIL, 0 SKIP against each bd build. `TestBdFlagManifestCurrent`, `TestGlobalValueFlagsIsComplete`, and `TestGlobalBoolFlagsIsComplete` all passed in both runs. |
| Keep manifest provenance and compatibility intent explicit. | PASS | `internal/bdflags/bdflags.go` identifies the fleet source as 2026-08-13, bd v1.1.0 build 0954be416, and documents that the bool table is a superset across the released and fleet builds, including the pre-rename `--profile` spelling. |

## Commands Run

```text
gh pr view 5284 --json state,mergedAt,mergeCommit,author,headRefOid
git merge-tree --write-tree origin/main origin/deploy/ga-o0pdkh-gate
git diff --check origin/main...HEAD
TMPDIR=/var/tmp DOCKER_HOST=unix:///run/user/1000/podman/podman.sock TESTCONTAINERS_RYUK_DISABLED=true make test-fast-parallel
go test -count=1 -tags integration -json ./internal/bdflags/...
PATH=<bd-built-from-beads-8e4e59d39>:$PATH go test -count=1 -tags integration -json ./internal/bdflags/...
```

The rootless Podman socket was present and exported before criterion 3. This diff and its owned tests are pure Go and did not require a container. The aggregate failure log is `/var/tmp/gc-local-tests.YNRDJr/unit-cmd-gc-4-of-6.log` in the deployer environment.

## Touched Files

```text
internal/bdflags/bdargs_test.go
internal/bdflags/bdflags.go
```
