# Release gate: route-assignee liveness reconciliation

- Deploy bead: `ga-r22k2y`
- Deploy mode: `remote`
- Source branch: `builder/ga-van9d5`
- Reviewed commit: `1e5506c4ec4506bf5c1ae71c467ff0ec0d9a3f0c`
- Rework handoff commit: `eae1fa25e5104981e881f28f29d236d3e5eb45db`
- Base: `origin/main@f4546f132e2b25e3ea8c9721c1b3cae32e15ff35`
- Overall verdict: **FAIL**

## Gate evidence

| # | Criterion | Result | Evidence |
|---|---|---|---|
| 1 | Review PASS present | SKIPPED | Fail-fast after criterion 6. The existing review PASS remains recorded on `ga-hh94r3`. |
| 2 | Acceptance criteria met | SKIPPED | Fail-fast after criterion 6. |
| 3 | Tests pass | SKIPPED | Fail-fast after criterion 6; no deploy-gate test command was run in this attempt. `diff_tests_executed: not run (criterion 6 failed first)`. `waiver_ref: none`. |
| 4 | No high-severity review findings open | SKIPPED | Fail-fast after criterion 6. |
| 5 | Final branch is clean | SKIPPED | Fail-fast after criterion 6. The bounded self-rebase started from a clean worktree. |
| 6 | Branch diverges cleanly from main | **FAIL** | `builder/ga-van9d5@eae1fa25e5104981e881f28f29d236d3e5eb45db` was 9 commits behind `origin/main`. The required `attempt_bounded_self_rebase builder/ga-van9d5 main` rebased locally to `5804a223820fbcab81e8a9fc35fca4d8e9f19d9c`, but its force-with-lease push was rejected (`rc=13`). The remote branch remained at `eae1fa25e5104981e881f28f29d236d3e5eb45db`. Per the bounded-helper contract, no manual push or conflict handling is permitted. |
| 7 | Single feature theme | SKIPPED | Fail-fast after criterion 6. |

## Preflight

GitHub's commit-to-pull-request lookup returned no PR for either the reviewed
commit or the rework handoff commit. This is not an already-merged
reconciliation case.

## Required next action

The builder must reconcile the rejected remote update, publish a current
branch head without bypassing concurrent work, and return the bead for a fresh
deploy gate.
