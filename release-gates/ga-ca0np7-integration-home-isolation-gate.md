# Release Gate: integration HOME isolation ancestry-fix retry

- Deploy bead: `ga-ca0np7`
- Review bead: `ga-20x17h`
- Reviewed source: `254dbabb5b3295ce800870576cdef4a6791ab2d5`
- Evaluated source after bounded self-rebase: `c0358b4fadb06fea51301c1ab4a3bb119656d9ba`
- Base: `origin/main@7c817e0640fae801631043005f1d54b17ce3e97c`
- Deploy branch: `deploy/ga-20x17h-gate`
- Verdict: **FAIL**

The ancestry-fix retry resolves the prior traceability rejection: both commits
in the evaluated range cite accepted source beads. The release remains blocked
because required integration failures overlap the changed package and shared
harness path, so criterion 3 cannot attribute or waive them.

## Gate checklist

| # | Criterion | Result | Evidence |
|---|---|---|---|
| 1 | Review PASS present | **PASS** | `ga-20x17h` records `verdict: pass` for reviewed source `254dbabb5b`; it independently verified the identical reviewed tree, the ancestry-fix message, style, security, and targeted tests. |
| 2 | Acceptance criteria met | **PASS** | The combined change pins real HOME only for `gc`/supervisor subprocess fixtures, re-isolates standalone `bd` subprocess HOME to the caller-owned directory, and tightens the empty-home guard. The two changed tests and three directly related tests all passed by name with zero skips. The evaluated commits cite `ga-vvnov6` and `ga-9lh4m0`, resolving the prior ancestry-scope failure. |
| 3 | Tests pass | **FAIL** | The required 40-job local CI union completed **27 PASS / 13 FAIL jobs**. Ten failing top-level tests are in `test/integration`, the same package as the changed `integration_test.go`; nine use the exact beads#4566 bootstrap signature and one is the tracked external-host init timeout. The diff also changes shared `integrationEnv` HOME projection used by fixture subprocesses, so there is a plausible mechanism and package-path overlap. Attribution clauses 3 and 4 are unsatisfied. The mayor standing authorization on `ga-lpfjhc` explicitly excludes diffs that can touch store bootstrap, so no waiver applies. The failures are preserved below. `waiver_ref: none`. |
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

`waiver_ref`: none. The `ga-lpfjhc` standing authorization does not cover this
same-package/shared-bootstrap-harness diff.

## Preserved full-union failures

Criterion 3 is blocked by these `test/integration` failures because the
candidate changes that package and its shared subprocess environment helper:

- `TestPersonalWorkFormulaCompileAndRun`
- `TestAdoptPRFormulaRetriesTransientReviewerStep`
- `TestRetryManagedPooledWorkerRecoversClaimedAttemptAfterCrash`
- `TestAdoptPRFormulaSoftFailsGeminiAfterTransientRetries`
- `TestGraphWorkflowSuccessPath`
- `TestHumaBinary_SessionMessageAsync`
- `TestCleanInstallTutorialPath`
- `TestGCLiveContract_BeadsAndEvents`
- `TestHumaBinary_CityCreateAsync`
- `TestDoltConfigWiringExternalHost`

The first nine carry the exact `ga-lpfjhc` / gastownhall/beads#4566 signature;
the last is tracked by `ga-gajll3`. Their trackers establish recurrence but do
not erase the candidate's path overlap or plausible shared-harness mechanism.

Five additional failures are structurally outside the candidate and tracked:

- `TestCmdMailInbox_NormalizesCanonicalManagedProviderEnvAndReadsInbox` — `ga-uswva7`.
- `TestBdRigWorktreeStoreConsistentAcrossRawBdGcBdAndProviderStore` — `ga-227zz7`.
- `TestBdFlagManifestCurrent` — `ga-f0uceo`.
- `TestGetKeyBinding_CapturesDefaultBinding` and `TestGetKeyBinding_CapturesDefaultBindingWithArgs` — `ga-afqddr` / `ga-k3fxvj`.

No PR was opened. The candidate-only deploy branch remains pushed for audit;
this FAIL record is committed locally and is not pushed.
