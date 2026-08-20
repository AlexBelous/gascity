# Release Gate: integration HOME isolation ancestry-fix retry

- Deploy bead: `ga-ca0np7`
- Review bead: `ga-20x17h`
- Reviewed source: `254dbabb5b3295ce800870576cdef4a6791ab2d5`
- Evaluated source after bounded self-rebase: `c0358b4fadb06fea51301c1ab4a3bb119656d9ba`
- Base: `origin/main@7c817e0640fae801631043005f1d54b17ce3e97c`
- Deploy branch: `deploy/ga-20x17h-gate`
- Verdict: **FAIL — NINE FAILURES WAIVED; ONE BLOCKER REMAINS**

The ancestry-fix retry resolves the prior traceability rejection: both commits
in the evaluated range cite accepted source beads. The mayor's standing
`ga-lpfjhc` authorization waives the nine exact beads#4566 failures after a
mechanism trace established that this diff cannot alter Dolt's explicitly set
config/schema root. The release remains blocked by the tenth integration
failure, `TestDoltConfigWiringExternalHost`: it has a different signature,
has no exact-base reproduction, and overlaps the changed package. No
waiver covers that failure.

## Gate checklist

| # | Criterion | Result | Evidence |
|---|---|---|---|
| 1 | Review PASS present | **PASS** | `ga-20x17h` records `verdict: pass` for reviewed source `254dbabb5b`; it independently verified the identical reviewed tree, the ancestry-fix message, style, security, and targeted tests. |
| 2 | Acceptance criteria met | **PASS** | The combined change pins real HOME only for `gc`/supervisor subprocess fixtures, re-isolates standalone `bd` subprocess HOME to the caller-owned directory, and tightens the empty-home guard. The two changed tests and three directly related tests all passed by name with zero skips. The evaluated commits cite `ga-vvnov6` and `ga-9lh4m0`, resolving the prior ancestry-scope failure. |
| 3 | Tests pass | **FAIL — PARTIAL WAIVER** | The required 40-job local CI union completed **27 PASS / 13 FAIL jobs**. Nine `test/integration` failures carry the exact beads#4566 signature and are preserved as **FAIL — WAIVED** under the mayor's `ga-lpfjhc` standing authorization: the builder's recorded mechanism trace shows the diff replaces only `HOME`, while every affected fixture explicitly sets unchanged `GC_HOME`/`DOLT_ROOT_PATH`, so it cannot alter schema migration or store bootstrap. `TestDoltConfigWiringExternalHost` remains **FAIL**: it has the distinct tracked `ga-gajll3` timeout signature, lacks an exact-`origin/main` reproduction, and its `test/integration` package overlaps the diff. Attribution clauses (iii) and (iv) are therefore unsatisfied, and `ga-lpfjhc` cannot waive a different signature. `waiver_ref: ga-lpfjhc mayor ruling 2026-08-18 for the nine exact beads#4566 failures; none for TestDoltConfigWiringExternalHost`. |
| 4 | No high-severity review findings open | **PASS** | `ga-20x17h` reports no style or security findings. One pre-existing low-severity fail-open branch coverage gap is explicitly non-blocking. Unresolved HIGH count: `0`. |
| 5 | Final branch is clean | **PASS** | Before adding this record, the branch was clean and tracked `origin/deploy/ga-20x17h-gate`. `git diff --check`, affected formatting, `go build ./...`, `go vet ./...`, and integration-tagged vet all passed. Repository hooks resolve to `.githooks`. |
| 6 | Branch diverges cleanly from main | **PASS** | Reviewed source `254dbabb5b` rebased without conflict onto `origin/main@7c817e0640`, producing `c0358b4fad`. An initial zsh source of the bash helper failed to load its push guard only after completing the clean local rebase and therefore did not push; the correct bash invocation then returned the documented no-op result because current main was already an ancestor. The exact candidate was subsequently pushed under the recorded non-diff fast-failure attribution. |
| 7 | Single feature theme | **PASS** | The sole changed file, `test/integration/integration_test.go`, contains one integration-harness HOME-isolation theme and its tests. No independent feature is bundled. |

## Test evidence

Environment for the required local CI union:

- `DOCKER_HOST=unix:///run/user/1000/podman/podman.sock`
- `TESTCONTAINERS_RYUK_DISABLED=true`

Commands and results:

- `GC_SESSION=subprocess go test -tags integration -run '^(TestStandaloneBDEnvForDirIsolatesHome|TestIntegrationEnvForPinsRealHome|TestStandaloneBDEnvAllowsBDAutoStart|TestStandaloneBdEnvIsolatesAmbientDoltConfig|TestUsesStandaloneBDWorkspaceKeepsFileProviderOnShim)$' -v ./test/integration/... -count=1 -timeout 5m`: **5 PASS / 0 FAIL / 0 SKIP**.
- `EXTRA_TEST_ENV="DOCKER_HOST=unix:///run/user/1000/podman/podman.sock TESTCONTAINERS_RYUK_DISABLED=true" make test-local-full-parallel`: **27 PASS jobs / 13 FAIL jobs**. Logs: `/var/tmp/gc-local-tests.6i2mXO`.
- Guarded pre-push fast gate: **9 PASS jobs / 1 FAIL — ATTRIBUTED** (`TestProviderLiveClaudeKindPath`, `ga-nqlb8q`, no `test/integration` path or mechanism overlap). Log: `/var/tmp/gc-local-tests.i4POu1/unit-core.log`.
- `go build ./...`: PASS.
- `go vet ./...`: PASS.
- `go vet -tags integration ./test/integration/...`: PASS.
- `LINT_CHANGED_SCOPE=tracked LINT_CHANGED_REF=origin/main make lint-affected`: PASS, 0 issues in `./test/integration`.
- `LINT_CHANGED_SCOPE=tracked LINT_CHANGED_REF=origin/main make fmt-check-changed`: PASS.
- `git diff --check origin/main...HEAD`: PASS.

`diff_tests_executed`:

- `TestIntegrationEnvForPinsRealHome`: PASS.
- `TestStandaloneBDEnvForDirIsolatesHome`: PASS.

Related unchanged tests executed in the same focused command:

- `TestStandaloneBDEnvAllowsBDAutoStart`: PASS.
- `TestStandaloneBdEnvIsolatesAmbientDoltConfig`: PASS.
- `TestUsesStandaloneBDWorkspaceKeepsFileProviderOnShim`: PASS.

`test_counts`: required union `27 PASS jobs / 13 FAIL`; diff-owned tests
`2 PASS / 0 FAIL / 0 SKIP`.

`skip_justification`: not applicable — zero diff-owned skips.

`waiver_ref`: `ga-lpfjhc` mayor ruling 2026-08-18 covers the nine exact
beads#4566 failures after the recorded condition-(b) mechanism trace. It does
not cover `TestDoltConfigWiringExternalHost`, whose distinct timeout failure
still has no waiver.

## Preserved full-union failures

The following exact beads#4566 failures are preserved as **FAIL — WAIVED**
under `ga-lpfjhc`; they are not rewritten green:

- `TestPersonalWorkFormulaCompileAndRun`
- `TestAdoptPRFormulaRetriesTransientReviewerStep`
- `TestRetryManagedPooledWorkerRecoversClaimedAttemptAfterCrash`
- `TestAdoptPRFormulaSoftFailsGeminiAfterTransientRetries`
- `TestGraphWorkflowSuccessPath`
- `TestHumaBinary_SessionMessageAsync`
- `TestCleanInstallTutorialPath`
- `TestGCLiveContract_BeadsAndEvents`
- `TestHumaBinary_CityCreateAsync`

The remaining blocker is `TestDoltConfigWiringExternalHost`, tracked by
`ga-gajll3`. Its tracker establishes recurrence, but no exact-base reproduction
is recorded and the candidate changes the same package, so attribution clauses
(iii) and (iv) remain unsatisfied. Because this is not a beads#4566 signature,
the `ga-lpfjhc` standing authorization cannot waive it.

Five additional failures are structurally outside the candidate and tracked:

- `TestCmdMailInbox_NormalizesCanonicalManagedProviderEnvAndReadsInbox` — `ga-uswva7`.
- `TestBdRigWorktreeStoreConsistentAcrossRawBdGcBdAndProviderStore` — `ga-227zz7`.
- `TestBdFlagManifestCurrent` — `ga-f0uceo`.
- `TestGetKeyBinding_CapturesDefaultBinding` and `TestGetKeyBinding_CapturesDefaultBindingWithArgs` — `ga-afqddr` / `ga-k3fxvj`.

No PR was opened. The candidate-only deploy branch remains pushed for audit;
this FAIL record is committed locally and is not pushed.
