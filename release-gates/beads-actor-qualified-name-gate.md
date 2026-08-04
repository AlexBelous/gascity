# Release gate: align BEADS_ACTOR with the stable agent alias

- Deploy bead: `ga-d6zqfj`
- Build bead: `ga-jav9u9`
- Source review: `ga-mfccbk`
- Reviewed commit: `941812ce44b193ebfc3ab3903861bcf79467e3e3`
- Remediated remote source tip: `1e1b25ca62e1ff6a259fe829bd20ce1fe73d1b77`
- Main evaluated: `origin/main@e4c62220f24c7fb6db5c2454dd2c54b7edb20d07`
- Source branch: `builder/ga-jav9u9`
- Planned deploy branch: `deploy/ga-d6zqfj-gate`
- Evaluated: `2026-08-03`
- Overall verdict: **FAIL**

`docs/PROJECT_MANIFEST.md` is not present at the evaluated commit. This
checklist therefore applies the deployer role's seven release criteria and
`engdocs/contributors/release-gate-criteria-conventions.md`.

| # | Criterion | Result | Evidence |
|---|---|---|---|
| 6 | Branch diverges cleanly from main | **FAIL** | Evaluated first after fetching `origin/main` and `origin/builder/ga-jav9u9`. The remote source tip `1e1b25ca62e1ff6a259fe829bd20ce1fe73d1b77` does not contain `origin/main@e4c62220f24c7fb6db5c2454dd2c54b7edb20d07`. The required `attempt_bounded_self_rebase builder/ga-jav9u9 main` rebased locally to `b716a7e9d15e5a290ee532441f665869d37bf0e2` but returned `rc=14`: `push-ownership-guard` resolved closed build bead `ga-jav9u9` from the branch name and blocked the required force-with-lease push. `origin/builder/ga-jav9u9` remained at `1e1b25ca62e1ff6a259fe829bd20ce1fe73d1b77`; the unsuccessful local rebase emitted no authoritative `AFTER_SHA` and cannot satisfy the gate. |
| 1 | Review PASS present | **SKIPPED** | Fail-fast after criterion 6. |
| 2 | Acceptance criteria met | **SKIPPED** | Fail-fast after criterion 6. |
| 3 | Tests pass | **SKIPPED** | Fail-fast after criterion 6; no test command was run on a shippable source SHA. |
| 4 | No high-severity review findings open | **SKIPPED** | Fail-fast after criterion 6. |
| 5 | Final branch is clean | **SKIPPED** | Fail-fast after criterion 6. |
| 7 | Single feature theme | **SKIPPED** | Fail-fast after criterion 6. |

## Required remediation

The builder must publish a source tip containing current `origin/main` and
route the deploy bead back for a full gate rerun. No isolated deploy branch was
pushed and no pull request was opened.
