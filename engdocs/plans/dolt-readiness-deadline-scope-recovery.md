# Dolt Readiness Deadline Scope Recovery

Owner: `gascity/pm`  
Created: 2026-08-06  
Root bead: `ga-nq4ih7`  
Source review: `ga-qogjk9`  
Priority: P2

## Goal

Recover the rejected Dolt readiness-deadline release candidate as one clean,
single-theme change. The deploy gate correctly stopped the reviewed source at
`b581872dab4a1f22d2875d10c08eab6b1ab7a036` because it bundled two
independently shippable changes.

The recovery preserves the reviewed readiness behavior while excluding the
unrelated environment-golden update. That side fix already exists on current
`origin/main`, so it needs verification rather than another release candidate.

Tracker import was checked during PM intake. No `tracker-to-beads` or sibling
tracker skill is installed in this worktree, so the import step was a no-op.
The `actual` CLI is also unavailable; the checked-in `AGENTS.md`, source beads,
release-gate artifact, and current repository state supplied the requirements.

## Grounded Evidence

- `7c8adf8ba` changes only `test/integration/dolt_config_test.go`, replacing the
  hardcoded 15-second readiness deadline and message with
  `doltServerStartupLimit`.
- `b581872da` adds `GC_TRANSCRIPT_META_ENABLED` to
  `internal/testenv/testdata/gc_env_read_baseline.golden` and is unrelated to
  the readiness behavior.
- Current `origin/main` still has the hardcoded 15-second readiness deadline,
  so the `7c8adf8ba` behavior has not landed.
- Current `origin/main` already contains the environment-golden entry through
  independent commits `578f76a40` and `1078d71f7`. The remaining difference
  from `b581872da` is ordering, not a missing environment variable.
- The failed gate is recorded in
  `release-gates/ga-nq4ih7-dolt-readiness-deadline-scope-gate.md` on commit
  `379916d20`. No deploy PR was opened from the mixed source.

## Work Packages

| Bead | Route | Outcome |
| --- | --- | --- |
| `ga-nq4ih7.1` | `gascity/builder` | Recreate only the reviewed readiness-deadline behavior on a clean branch from current `origin/main`, test it, and push the isolated SHA. |
| `ga-nq4ih7.2` | `gascity/builder` | Verify the environment-golden fix already landed independently and close evidence-only; create a separate branch only if the verification disproves that conclusion. |
| `ga-nq4ih7.3` | `gascity/builder` | After `.1` and `.2`, create and route the review handoff for the isolated readiness SHA. |

Each bead carries its measurable acceptance criteria in the bead record. All
three use `ready-to-build` and `source:actual-pm`; none inherits the root's
`needs-deploy` or `needs-pm` labels.

## Dependency Graph

```text
ga-nq4ih7.1  isolate readiness fix ──┐
                                     ├──> ga-nq4ih7.3  review/deploy handoff
ga-nq4ih7.2  reconcile golden fix ───┘
```

The first two packages are independent. The handoff package remains blocked
until both establish a clean release boundary.

## Release Boundaries

The new readiness candidate must:

- start from current `origin/main`;
- change only `test/integration/dolt_config_test.go`;
- preserve the reviewed two-line semantic change from `7c8adf8ba`;
- leave `internal/testenv/testdata/gc_env_read_baseline.golden` unchanged; and
- use a fresh isolated branch and exact SHA for review and deploy.

The new candidate must not use `b581872da` or the failed deploy branch as its
source. Those refs remain provenance for the rejected mixed-theme candidate.

## Risks and Controls

| Risk | Control |
| --- | --- |
| The already-landed golden fix is duplicated in the new candidate. | `ga-nq4ih7.2` verifies the current-main state, and `ga-nq4ih7.3` requires a one-file diff before review. |
| Rebuilding from the old reviewed tip omits current-main fixes. | `ga-nq4ih7.1` starts from current `origin/main` and reapplies only the reviewed readiness behavior. |
| A changed isolated commit bypasses review because the mixed tip already passed. | `ga-nq4ih7.3` creates a new review bead for the exact isolated SHA before deploy. |
| The failed mixed source is accidentally reused downstream. | Every child names `b581872da` as provenance-only and forbids it as the new review or deploy source. |

## Completion

This PM root is complete when the plan is committed and verified, the three
children and their dependency edges exist, and the children are routed to the
builder. Product delivery completes later when the isolated SHA passes review
and the standard deploy gate.
