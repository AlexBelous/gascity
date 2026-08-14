# Release Gate: synchronize the bd flag manifest with bd v1.1.0

Deploy bead: ga-o0pdkh
Source implementation bead: ga-gqxh5s
Review bead: ga-i8rhmj
Deploy branch: deploy/ga-o0pdkh-gate
Reviewed source: 3289b5673985f237fe655170162b89a715b84269
Reviewed branch: builder/ga-gqxh5s (provenance only)
Base checked: origin/main at b63623d08ecf565de82c226f0af1ca2fc359d45d
Merge base: cb456b85ecd923186a50493074f8b8a4c75d7eac
Release criteria source: active deployer gate criteria; docs/PROJECT_MANIFEST.md is not present on origin/main.

## Gate Summary

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 1 | Review PASS present | PASS | Review bead ga-i8rhmj is closed with reason `pass`; its notes record `verdict: pass` for the exact reviewed source and report no style or security findings. |
| 2 | Acceptance criteria met | PASS | See the acceptance table below. The live manifest freshness test passed all 17 known bd subcommands against bd 1.1.0 build 0954be416. |
| 3 | Tests pass | PASS | On the exact reviewed source, `go build ./...` and `go vet ./...` exited 0, formatting was clean, and `make test-fast-parallel` passed 10/10 jobs. The full `internal/bdflags` integration-tagged package run completed 26 top-level tests: 26 PASS, 0 FAIL, 0 SKIP. `TestBdFlagManifestCurrent` separately passed all 17 subcommand cases. `diff_tests_executed`: `TestGlobalValueFlagsIsComplete` PASS; `TestGlobalBoolFlagsIsComplete` PASS. `skip_justification`: none; zero skips. `waiver_ref`: none needed. |
| 4 | No high-severity review findings open | PASS | ga-i8rhmj records `style_findings: none`, `security_findings: none`, and no unresolved HIGH finding. |
| 5 | Final branch is clean | PASS | The isolated worktree at the reviewed source reported no path changes before this checklist was written; `git diff --check` also passed. Cleanliness is rechecked after the gate commit and before push. |
| 6 | Branch diverges cleanly from main | PASS | The already-merged preflight found no PR carrying 3289b567. `git merge-tree --write-tree origin/main 3289b567...` exited 0 and produced tree 27716356558e2ffd09f07d1e1a5a6faa3b9bd3c2. No self-rebase was needed. |
| 7 | Single feature theme | PASS | The two feature commits touch only `internal/bdflags/bdflags.go` and `internal/bdflags/bdargs_test.go`, updating one static CLI-flag manifest and its pinned completeness tests. |

## Acceptance Evidence

| Acceptance item | Result | Evidence |
|-----------------|--------|----------|
| Reflect every installed bd flag for all 17 known subcommands without removing real coverage. | PASS | `TestBdFlagManifestCurrent` passed `close`, `create`, `delete`, dependency, gate, list, molecule, ready, reopen, show, and update cases. The installed binary reports `bd version 1.1.0 (0954be416)`; its global help includes `--cpu-profile`, `--database`, `--mem-profile`, and `--no-color`. A direct `bd --profile` check returns `unknown flag`, confirming the removed manifest entry was not real coverage. |
| Pass the live freshness contract. | PASS | `go test -count=1 -tags integration -run '^TestBdFlagManifestCurrent$' -v ./internal/bdflags/...` passed the parent test and all 17 named subtests with 0 failures and 0 skips. |
| Update manifest provenance to the installed build. | PASS | `internal/bdflags/bdflags.go` identifies the source as 2026-08-13, bd v1.1.0, build 0954be416, matching `bd version`. |

## Commands Run

```text
gh api repos/gastownhall/gascity/commits/3289b5673985f237fe655170162b89a715b84269/pulls
git merge-tree --write-tree origin/main 3289b5673985f237fe655170162b89a715b84269
go build ./...
go vet ./...
gofmt -l internal/bdflags
go test -count=1 -tags integration -json ./internal/bdflags/...
go test -count=1 -tags integration -run '^TestBdFlagManifestCurrent$' -v ./internal/bdflags/...
TMPDIR=/var/tmp EXTRA_TEST_ENV="DOCKER_HOST=unix:///run/user/1000/podman/podman.sock TESTCONTAINERS_RYUK_DISABLED=true" make test-fast-parallel
git diff --check
```

The rootless Podman socket was present and exported before criterion 3. This diff and its owned tests are pure Go and did not require a container.

## Publication Hook Follow-up

An additional pre-push invocation reran the 10-job fast suite with three concurrent workers and reported one failure in `TestCityRuntimeForceShutdownTearsDownAfterLateAsyncSweep`. That timing-sensitive `cmd/gc` lifecycle test is outside this two-file `internal/bdflags` diff and had passed in the criterion-3 run. An immediate focused rerun completed 50/50 iterations successfully:

```text
TMPDIR=/var/tmp GC_FAST_UNIT=1 go test -count=50 -run '^TestCityRuntimeForceShutdownTearsDownAfterLateAsyncSweep$' ./cmd/gc
```

The first full gate remains the criterion-3 result; publication still requires a fresh successful pre-push hook.

## Touched Files

```text
internal/bdflags/bdargs_test.go
internal/bdflags/bdflags.go
```
