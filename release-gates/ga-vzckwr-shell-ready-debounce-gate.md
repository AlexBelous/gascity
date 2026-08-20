# Release gate: debounce WaitForShellReady against transient shell exec

- Deploy bead: `ga-vzckwr`
- Build bead: `ga-14s617`
- Review bead: `ga-jzj77d`
- Reviewed commit: `0d32682856769173a1ad5fc5f7778f64435659f6`
- Gate base: `origin/main` at `af85466e7d9ee5274036105d063ee8ab54cecf00`
- Evaluated: 2026-08-19
- Result: **PASS**

Criterion 6 was evaluated first, as required. The remaining criteria were
then evaluated in numeric order. This is a second gate attempt on the same
deploy bead: an earlier attempt FAILED criterion 6 (bounded self-rebase
returned rc 10 because the source branch was checked out in a different,
concurrently-used worktree) and is recorded at
`release-gates/ga-vzckwr-tmux-shell-readiness-gate.md` on the abandoned
branch `deploy/ga-vzckwr-gate-fail-20260819`. This attempt used a disposable
worktree (`/var/tmp/gc-builder2-ga-vzckwr`) not shared with any other
session, and rebased cleanly.

| # | Criterion | Result | Evidence |
|---|---|---|---|
| 1 | Review PASS present | **PASS** | Review bead `ga-jzj77d` is closed with reason `pass`; its notes record `verdict: pass`, `deploy_bead: ga-vzckwr`, and `deploy_commit: 0d32682856769173a1ad5fc5f7778f64435659f6`, matching this gate's reviewed commit exactly. Notes state "No requested changes." |
| 2 | Acceptance criteria met | **PASS** | The reviewed diff adds `shellReadyStableChecks = 3` to `internal/runtime/tmux/tmux.go`, requiring 3 consecutive identical `GetPaneCommand` matches before `WaitForShellReady` declares a pane's shell ready (previously trusted the first match, which a transient wrapper shell mid-`exec` could falsify under parallel shard load — root cause of the `TestIsAgentRunning` flake this bead targets). New regression test `TestWaitForShellReady_TransientShellBeforeExec` exercises a fixture (`sh -c 'read -t 0.15 dummy; exec zsh'`) that reports `sh` for ~150ms before exec'ing into `zsh`, and asserts the call blocks for the full transient window. `scripts/runtime-tmux-tests.manifest` and `scripts/runtime_tmux_manifest_test.go` were updated in the same commit to keep the new test enrolled in the CI inventory gate (total manifest 368→369, integration-only 118→119, six-way shard partition rebalanced). `git diff 0d32682856769173a1ad5fc5f7778f64435659f6..HEAD -- internal/runtime/tmux/tmux.go internal/runtime/tmux/tmux_test.go scripts/runtime-tmux-tests.manifest scripts/runtime_tmux_manifest_test.go` is empty, confirming the rebased tip is byte-identical to the reviewed commit on every diff-touched file. |
| 3 | Tests pass | **PASS** | `make test-cmd-gc-process-parallel` (satisfies the `cmd_gc_process` CI job for this diff's `internal/**` paths, including `TestTutorial01` under `GC_FAST_UNIT=0`): clean, all 6 shards plus `productmetrics-testhook` `ok`. `make test-fast-parallel`: final run clean, 10/10 jobs `ok` (`ga-vzckwr-fast-parallel-attempt4.log`, "All fast jobs passed"). An earlier run in this same session failed solely on the `unit-core` job's `TestProviderLiveClaudeKindPath` (`internal/runtime/herdr`, "herdr server ... did not become ready"; `ga-vzckwr-fast-parallel-attempt3.log`), reproduced standalone (`ga-vzckwr-herdr-isolated.log`) and confirmed pre-existing/environmental — tracked `ga-nqlb8q`, a sandbox `exec.Command`-spawn readiness timing flake in an unrelated package, whose own differential diagnosis against an untouched sibling test proves it is not diff-related. Real CI-equivalent coverage for the `integration` job's `packages-runtime-tmux-N-of-6` matrix (CI's 6-way shard split) was reproduced locally via the documented 3-way local partition (`./scripts/test-integration-shard packages-runtime-tmux-{1,2,3}-of-3`, run concurrently, same underlying per-package inventory): shard 2 of 3 clean; shards 1 and 3 each show exactly the same 2 pre-existing failures, `TestGetKeyBinding_CapturesDefaultBinding` and `TestGetKeyBinding_CapturesDefaultBindingWithArgs` (tracked `ga-afqddr` and `ga-k3fxvj` — host tmux 3.7b returns an empty default keytable; both trackers predate this diff, created 2026-08-15, and are confirmed recurring across multiple unrelated diffs' gate runs) — zero new or diff-related failures in any shard. The original unsharded 369-test `internal/runtime/tmux` package run corroborates: `TestWaitForShellReady_TransientShellBeforeExec` passes, and the only failures are the same 2 tracked `GetKeyBinding` cases plus `TestHiddenAttachedClientCanSendText` (zsh-newuser-install first-run wizard consuming keystrokes when the session's `HOME` lacks zsh dotfiles — newly filed `ga-1wilql`, confirmed unrelated: this diff touches only `WaitForShellReady`, nothing in the hidden-attach-client path; the review bead independently reproduced the identical failure 4/4 times against pristine `origin/main` via `git stash`). Reviewer's own notes additionally record `internal/runtime/tmux` (including the new test) passing cleanly across 5 independent `make test-fast-parallel` runs during review. `go build ./...` and `go vet ./internal/runtime/tmux/...` both clean post-rebase. Not run locally: the `worker`/`worker_phase2` CI jobs' live-provider conformance suites (`worker-core-claude`/`codex`/`gemini`, `make test-worker-core PROFILE=<provider>/tmux-cli`) — these require live provider CLI credentials unavailable in this sandbox; disclosed here rather than waived. |
| 4 | No high-severity review findings open | **PASS** | `ga-jzj77d` notes record "No requested changes" and close reason `pass`. The two self-identified process issues surfaced during review (a forbidden `time.Sleep` in the new test's first draft, tripping the fixed-sleep census; the new test's initial absence from the tmux manifest) were both fixed within the reviewed commit itself — neither was left open. |
| 5 | Final branch is clean | **PASS** | `git status --short` is empty at tip `80b5805694efb8c8589aae6fdd54e711edff7baf` (only this gate-evidence commit pending). |
| 6 | Branch diverges cleanly from main | **PASS** | `origin/main` advanced from `8c73625b97` (the failed first attempt's stale base) to `af85466e7d9ee5274036105d063ee8ab54cecf00` during this session. This attempt rebased `0d32682856769173a1ad5fc5f7778f64435659f6` onto the current tip in an isolated, unshared worktree (`git rebase origin/main`, zero conflicts, new tip `80b5805694efb8c8589aae6fdd54e711edff7baf`). `git merge-tree --write-tree origin/main 80b5805694efb8c8589aae6fdd54e711edff7baf` exited 0 and produced tree `81c878d41c6fc35385fcad6e8ce14b1bf4457761`; no further self-rebase is pending. |
| 7 | Single feature theme | **PASS** | The two-commit, 4-file diff (`internal/runtime/tmux/tmux.go`, `internal/runtime/tmux/tmux_test.go`, `scripts/runtime-tmux-tests.manifest`, `scripts/runtime_tmux_manifest_test.go`) is scoped entirely to the `WaitForShellReady` debounce fix and its resource-census/manifest bookkeeping. |

## Gate decision

The reviewed change introduces no new or diff-related test failures against
current `origin/main`, satisfies its acceptance evidence with a byte-identical
rebase, and remains conflict-free. Every failure observed across all suite
runs this session (`ga-nqlb8q`, `ga-afqddr`, `ga-k3fxvj`, `ga-1wilql`) is
independently tracked, predates this diff, and is confirmed unrelated to it.
It is eligible for an isolated deploy branch and pull request. The
`worker`/`worker_phase2` live-provider CI jobs could not be reproduced
locally (no provider credentials in this sandbox) and are disclosed above
rather than waived.
