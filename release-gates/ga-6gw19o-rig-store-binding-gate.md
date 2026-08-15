# Release gate: detect missing rig store bindings

**Deploy bead:** `ga-6gw19o`

**Review bead:** `ga-f7kdwt`

**Reviewed source:** `f9ff6389168078e1b4f3383a783bc416ed832480`

**Base checked:** `origin/main@f4b84050a49c010dffede3706f1444eb89ec31a3`

**Deploy mode:** remote

**Overall result:** **FAIL**

The mandatory already-merged pre-flight found no pull request carrying the
reviewed commit. The normal gate therefore proceeded to criterion 6.

`docs/PROJECT_MANIFEST.md` is absent from this checkout, so there are no
additional repository-local release criteria beyond the active deployer gate
criteria and the testing policy in `TESTING.md`.

## Gate criteria

| # | Criterion | Result | Evidence |
|---|---|---|---|
| 1 | Review PASS present | SKIPPED | Fail-fast after criterion 6; the reviewer evidence was not re-evaluated as a substitute gate. |
| 2 | Acceptance criteria met | SKIPPED | Fail-fast after criterion 6; acceptance criteria were not re-evaluated. |
| 3 | Tests pass | SKIPPED | Fail-fast after criterion 6. No test command ran, so there are no PASS/FAIL/SKIP counts. `diff_tests_executed: not run (criterion 6 fail-fast)`; `waiver_ref: none`. |
| 4 | No high-severity review findings open | SKIPPED | Fail-fast after criterion 6; review findings were not re-evaluated. |
| 5 | Final branch is clean | SKIPPED | Fail-fast after criterion 6; final-branch cleanliness was not scored. |
| 6 | Branch diverges cleanly from main | **FAIL** | `git merge-tree --write-tree origin/main f9ff6389168078e1b4f3383a783bc416ed832480` reported content conflicts in `TESTING.md`, `internal/testpolicy/resourcecensus/census.go`, and `test/test-resources.toml`. The prescribed `attempt_bounded_self_rebase builder/ga-e5lyfu main` returned rc 12 under Bash, aborted cleanly, and left both local and remote source tips unchanged at the reviewed SHA. |
| 7 | Single feature theme | SKIPPED | Fail-fast after criterion 6; theme was not re-evaluated. |

## Criterion 6 evidence

```text
merge base: d7e1c3aea47ebe910e7301c844ad488fa1142020
base:       origin/main@f4b84050a49c010dffede3706f1444eb89ec31a3
source:     f9ff6389168078e1b4f3383a783bc416ed832480

git merge-tree --write-tree origin/main f9ff6389168078e1b4f3383a783bc416ed832480
CONFLICT (content): Merge conflict in TESTING.md
CONFLICT (content): Merge conflict in internal/testpolicy/resourcecensus/census.go
CONFLICT (content): Merge conflict in test/test-resources.toml

attempt_bounded_self_rebase builder/ga-e5lyfu main
bounded_self_rebase_rc=12
local branch tip after abort:  f9ff6389168078e1b4f3383a783bc416ed832480
remote branch tip after abort: f9ff6389168078e1b4f3383a783bc416ed832480
```

The conflicts are not within the helper's provably trivial resolution set.
The builder must produce a fresh candidate on current `origin/main`, update the
resource-census ledger from the resulting tree, and send that exact SHA through
review again.

## Disposition

Technical gate FAIL on criterion 6. Criteria 1–5 and 7 were skipped by the
required fail-fast ordering. No tests ran, no branch was pushed, no PR was
opened, and no deploy-clearance status was posted. Route `ga-6gw19o` back to
the builder.
