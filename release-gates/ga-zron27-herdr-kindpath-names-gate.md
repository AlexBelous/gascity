# Release Gate: ga-zron27 - herdr kind-path name isolation

Deploy bead: `ga-zron27`  
Review bead: `ga-hmd2gu`  
Source build bead: `ga-fh1flg`  
Reviewed content commit: `2ec870e236eb32fef0fa7af56281981b34f2afce`  
Evaluated rebased commit: `62ee9f212bc33051f6e1e1a9375e86bee0e1db72`  
Local deploy branch: `deploy/ga-zron27-gate-r3-20260820`  
Base: `origin/main@7c817e0640fae801631043005f1d54b17ce3e97c`  
Gate evaluated: 2026-08-20  
Verdict: **FAIL**

`docs/PROJECT_MANIFEST.md` is not present in this checkout. This gate uses
the release criteria in the deployer prompt together with `TESTING.md` and
`engdocs/contributors/release-gate-criteria-conventions.md`.

## Criteria

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 1 | Review PASS present | PASS | Closed review bead `ga-hmd2gu` records round-2 `REVIEW VERDICT: PASS`. `git diff --exit-code 2ec870e236eb32fef0fa7af56281981b34f2afce 62ee9f212bc33051f6e1e1a9375e86bee0e1db72 -- internal/runtime/herdr/kindpath_live_test.go internal/runtime/herdr/panebinding_provider_test.go` returned 0, proving the rebased candidate's reviewed content is byte-identical. |
| 2 | Acceptance criteria met | PASS | The PID-plus-atomic-counter name generator and both required hermetic proofs are present. `TestKindPathNamesAreUnique` and `TestKindPathNamesWorkThroughFakeProviderLifecycle` both passed in a fresh focused run. |
| 3 | Tests pass | **FAIL** | The guarded push's `make test-fast-parallel` run failed in `unit-core` because the diff-owned `TestProviderLiveClaudeKindPath` returned `agent_pane_busy` for unique agent `kindsmoke-3319807-4` targeting pane `w1:p1`. The other nine emitted fast-gate logs passed. A diff-owned failure is a hard failure and cannot be attributed or retried into green; `waiver_ref: none`. Required broader jobs were skipped fail-fast. |
| 4 | No high-severity review findings open | PASS | The round-2 review verdict is PASS with no unresolved HIGH finding. |
| 5 | Final branch is clean | PASS | `git status --short --branch` was clean on the rebased deploy branch before this gate record was written. No source edits were made by deployer. |
| 6 | Branch diverges cleanly from main | PASS | The bounded replay completed without conflict. `git merge-base --is-ancestor origin/main 62ee9f212bc33051f6e1e1a9375e86bee0e1db72` returned 0 and the merge base is exactly `origin/main@7c817e0640fae801631043005f1d54b17ce3e97c`. The subsequent guarded push was rejected because its pre-push test gate failed, so no remote deploy branch was created. |
| 7 | Single feature theme | PASS | The three rebased commits touch only two `internal/runtime/herdr` test files and one theme: isolating kind-path lifecycle test names and proving those names through the provider lifecycle. |

## Criterion 3 evidence

The pre-push fast gate wrote logs to `/var/tmp/gc-local-tests.bdq9Kc`.
`unit-core.log` records:

```text
--- FAIL: TestProviderLiveClaudeKindPath (0.33s)
kindpath_live_test.go:127: Start: herdr: start "kindsmoke-3319807-4":
agent_pane_busy: agent target pane w1:p1 is not an available shell
```

Focused hermetic verification on the same rebased SHA passed:

```text
TestKindPathNamesAreUnique: PASS
TestKindPathNamesWorkThroughFakeProviderLifecycle: PASS
```

This is the same lower-level pane-availability failure recorded in the prior
gate and tracked by `ga-nqlb8q`, but the tracker is not a waiver. The modified
live test itself failed in this release evaluation, so the non-diff-owned
failure attribution protocol does not apply.

## Disposition

- No remote deploy branch, PR, clearance status, or merge was created.
- Return to builder: the modified live test must produce a real PASS under the
  required gate, or merge authority must record an explicit waiver. The
  deployer cannot grant that waiver.
