# Release gate: formula `GC_RIG` scope resolution

- Deploy bead: `ga-djfr2g`
- Build bead: `ga-fstubn`
- Reviewed source: `6c5712c0f58d8c7e4605dc2b145cbb96ef9e772f`
- Gate base: `origin/main@682a0726f5ad20cedd39e3b97e0f9d6f7fa7b919`
- Evaluation date: 2026-07-30
- Disposition: **PASS**

## Gate checklist

| # | Criterion | Result | Evidence |
|---|---|---|---|
| 1 | Review PASS present | **PASS** | `ga-djfr2g` records `verdict: pass` after an independent review at the reviewed source SHA. |
| 2 | Acceptance criteria met | **PASS** | Focused tests pass for valid `GC_RIG` routing outside a registered rig path, explicit `--rig` precedence, invalid/unbound `GC_RIG` warning plus cwd/city fallback, unchanged behavior when `GC_RIG` is unset, and rig-scoped formula variables. The implementation is shared by formula show, catalog, cook, and version-check call sites. |
| 3 | Tests pass | **PASS** | `go build ./...` passed; `go vet ./...` passed; `go test -count=1 ./cmd/gc/... -run 'TestResolveFormulaScope\|TestRigFormulaVarsForScope' -v` passed, 14/14 (including the new `--city` city-pin and precedence-gap tests); `make test-fast-parallel` passed all 10 jobs. All commands ran at the reviewed source SHA. |
| 4 | No high-severity review findings open | **PASS** | Reviewer notes report no style, security, or specification findings and no blocking findings; unresolved HIGH count is 0. |
| 5 | Final branch is clean | **PASS** | `git status --porcelain` was empty at the reviewed source SHA before this gate record was created. |
| 6 | Branch diverges cleanly from main | **PASS** | Evaluated first and rechecked after tests. `git merge-tree --write-tree origin/main 6c5712c0f58d8c7e4605dc2b145cbb96ef9e772f` exited 0 against the gate base and produced tree `b6eb17bd687e0a2391e3e2726fb58bd70897d0a3`; no self-rebase was required. |
| 7 | Single feature theme | **PASS** | The three-commit diff (RED `62d4260e0`, GREEN `6c5712c0f`, and this gate-doc refresh) is confined to `cmd/gc/cmd_formula.go`, `cmd/gc/cmd_formula_test.go`, and this gate doc, implementing, testing, and recording one formula scope-resolution behavior — including the restored `--city` scope pin. |

## Acceptance evidence

- `GC_RIG` is consulted after explicit `--rig` and before cwd-based discovery.
- A valid bound rig selects its store root, formula layers, and formula variables even when the agent worktree is outside the rig path.
- An unknown or unbound `GC_RIG` does not make formula commands unusable: resolution falls through and emits a warning naming the discarded value and selected scope.
- Existing cwd and city fallback behavior remains in place when `GC_RIG` is unset.
- An explicit `--city` pins city scope ahead of `GC_RIG` and cwd discovery.
- No configuration schema, API wire shape, migration, or new dependency is introduced.
