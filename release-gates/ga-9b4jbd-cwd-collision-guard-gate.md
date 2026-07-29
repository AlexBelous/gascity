# Release gate: cwd-collision guard false-positive fix

- Deploy bead: `ga-9b4jbd`
- Reviewed source: `d279aa6d7dd4d277db12c34fe422e3e204ac1eb3`
- Source branch: `builder/ga-esrrot` (provenance only)
- Current base: `origin/main` at `30df2e64db3afd11bd18b4fc2cdd61c20b061f69`
- Overall verdict: **FAIL**

Criterion 6 was evaluated first. Its failure triggered the required fail-fast
path, so the remaining criteria were not evaluated.

| # | Criterion | Verdict | Evidence |
|---|---|---|---|
| 1 | Review PASS present | SKIPPED | Not evaluated after criterion 6 failed. |
| 2 | Acceptance criteria met | SKIPPED | Not evaluated after criterion 6 failed. |
| 3 | Tests pass | SKIPPED | Not run after criterion 6 failed. No test result or zero-failure count is claimed. |
| 4 | No high-severity review findings open | SKIPPED | Not evaluated after criterion 6 failed. |
| 5 | Final branch is clean | SKIPPED | Not evaluated after criterion 6 failed. |
| 6 | Branch diverges cleanly from main | **FAIL** | `git merge-tree --write-tree origin/main d279aa6d7dd4d277db12c34fe422e3e204ac1eb3` reported structural rename/rename, rename/delete, modify/delete, and content conflicts in generated dashboard assets and `internal/api/dashboardspa/dist/index.html`. Divergence was 3 base-only and 13 source-only commits, with merge base `4d4fa9f63aa65af26a7026d7e144bcc5933cfb27`. The prescribed `attempt_bounded_self_rebase builder/ga-esrrot main` returned `12`, meaning the conflicts were outside the conservative trivial-conflict classifier; it aborted cleanly and did not push. |
| 7 | Single feature theme | SKIPPED | Not evaluated after criterion 6 failed. |

## Required follow-up

Return the work to the builder to rebase the feature branch on current
`origin/main`, regenerate the dashboard artifacts from the rebased source, and
resubmit the exact new tip for review and release evaluation.
