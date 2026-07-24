# Release Gate: ga-jpcfm3 worktree liveness gate and dry-run reaper

Branch: `deploy/ga-jpcfm3-gate`
Reviewed source commit: `747494717f4a4ec72edefc54278b62b2ce1e775e`
Source bead: `ga-e2g9do`
Deploy bead: `ga-jpcfm3`
Existing PR: https://github.com/gastownhall/gascity/pull/4598
Current `origin/main` at gate time: `97e1cb5272a41f21efd7e137a143c35cf34cc713`
Merge base for reviewed source: `d5fbb58c983251bfe9df8c53be1b86ab6bef6408`

`docs/PROJECT_MANIFEST.md` is not present in this checkout, so this gate uses
the deployer release criteria table plus the repository testing guidance in
`TESTING.md`.

## Scope

This change hardens the closed-bead worktree reaper so closed status and a
git-clean tree are no longer enough to delete a per-bead worktree. The reaper
now discovers nested worktrees through the owning rig repository's
`git worktree list --porcelain`, checks live process working directories via
`/proc/<pid>/cwd`, cross-checks active session worker directories, and fails
closed when liveness cannot be determined.

It also adds the staged rollout knob
`AutoReapClosedBeadWorktreesDryRun` /
`auto_reap_closed_bead_worktrees_dry_run`. Dry-run runs the full
classification path and emits `bead.worktree.reap_skipped` events for both
protected and would-reap candidates, but removes nothing. Real reaping
supersedes dry-run when both knobs are enabled.

Changed in the reviewed source commit:

- `cmd/gc/bead_worktree_liveness.go`: live process cwd collection,
  active-session directory collection, and worktree containment matching.
- `cmd/gc/bead_worktree_reaper.go`: nested worktree discovery through the
  rig repo, fail-closed liveness gate, dry-run classification, and event
  reporting.
- `cmd/gc/city_runtime.go`: runtime wiring for enabled and dry-run reaper
  modes.
- `cmd/gc/cmd_session.go`: JSON session list now includes `worker_dir`, used
  to map active sessions to their canonical per-bead worktrees.
- `internal/config/config.go`: daemon dry-run config field and nil-safe
  accessor.
- `docs/reference/config.md` and `docs/reference/schema/city-schema.*`:
  generated config documentation/schema for the new knob.
- Tests in `cmd/gc/*_test.go` and `internal/config/config_test.go` cover the
  new behavior.

## Criteria

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 1 | Review PASS present | PASS | Review bead `ga-1oz9gp` records `PASS` for commit `747494717f4a4ec72edefc54278b62b2ce1e775e`; deploy bead `ga-jpcfm3` carries the reviewed commit and routes it to deployer. |
| 2 | Acceptance criteria met | PASS | The reaper discovers any-depth per-bead worktrees via the owning rig repo; protects live worktrees by `/proc/<pid>/cwd` and active session `worker_dir`; protects every candidate when the liveness scan is unavailable; preserves existing git-safety gates; adds default-false dry-run config with docs/schema and real-reap-overrides-dry-run behavior. |
| 3 | Tests pass | PASS | See test log below. |
| 4 | No high-severity review findings open | PASS | Review bead `ga-1oz9gp` reports no blockers or HIGH findings; its noted local shard failures were independently classified there as pre-existing/environmental, and this gate's fresh fast suite passed cleanly. |
| 5 | Final branch is clean | PASS | `git status --short --branch` was clean on `deploy/ga-jpcfm3-gate` before adding this gate. Final cleanliness is verified after committing this gate file. |
| 6 | Branch diverges cleanly from main | PASS | `git merge-tree --write-tree origin/main HEAD` returned exit 0 with tree `453c51e3ef37cf1d0d7a334fc3720de949fe0f03`; PR #4598 is `OPEN`, `MERGEABLE`, and `CLEAN` at head `747494717f4a4ec72edefc54278b62b2ce1e775e`. |
| 7 | Single feature theme | PASS | One subsystem/theme: safe staged rollout of closed-bead worktree reaping. The touched code is confined to the reaper/liveness/runtime session metadata/config surfaces needed for that behavior plus generated docs/schema. |

## Test Log

- PASS: `make test-fast-parallel`
  - `All fast jobs passed`
- PASS: `go vet ./...`
  - exit 0, zero output
- PASS: `make check-schema`
  - regenerated config/schema/CLI docs with no resulting diff
- PASS: `go test ./cmd/gc ./internal/config -run 'Test(WorktreeIsLive_|CollectLiveWorktreeState_|LiveSessionWorktreeDirs_|ReapClosedBeadWorktrees_|CityRuntimeTick_.*ClosedBeadWorktreeReap|CityRuntimeTick_DryRunReapDeletesNothing|DaemonAutoReapClosedBeadWorktreesDryRun)' -count=1`
  - `ok github.com/gastownhall/gascity/cmd/gc 0.638s`
  - `ok github.com/gastownhall/gascity/internal/config 0.024s`
- PASS: `git diff --check d5fbb58c983251bfe9df8c53be1b86ab6bef6408..747494717f4a4ec72edefc54278b62b2ce1e775e`
  - exit 0, zero output

## PR Handling

The deployer contract normally opens the PR from this isolated deploy branch.
This bead already had PR #4598 open before release evaluation, authored by our
team and verified at the reviewed source SHA. To avoid a duplicate PR for the
same code, this gate uses `deploy/ga-jpcfm3-gate` as durable gate evidence and
routes the existing PR #4598 to mayor after this branch is pushed.
