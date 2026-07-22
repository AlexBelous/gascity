# Next-session prompt (paste this to continue)

Continue the **infra-class SQLite stores** track in the existing worktree
`/data/projects/gascity/.claude/worktrees/sqlit` (branch `worktree-sqlit`,
pushed as `origin/feat/infra-class-sqlite-stores`). Do NOT create a new
worktree; run everything from that directory.

**P5 bd surface + cleanup is COMPLETE** (write guard, gc bd show
federation, infra-class-migration doctor check + orders ENOENT fix,
class-store maintenance loop + storehealth TotalSize, doltlite order-run
cache retired, mail storeless leg routed). Read HANDOFF.md "P5 bd surface
+ cleanup — DONE" first.

**What remains on this track (in order):**

1. **Operational: the mc shadow soak** — `[beads.classes.sessions]
   shadow = true` on mc, watch `gc doctor` checks `sessions-shadow` AND
   the new `infra-class-migration` for 24–48h of zero discrepancy, THEN
   flip `backend = "sqlite"` per class (drop shadow; the combination is
   rejected). The ephemeral classes (orders/nudges/messaging) can flip
   without a soak. This is the gate for everything below.
2. **splittest topology port — needs a design pass first.** The
   feat/split-store-conformance harness targets the work/infra-graph
   split (config.InfraScopePrefix; ready/claim/residence federation).
   Per-class stores are a different shape (sessions fails loud on Ready;
   the other three aren't beads.Stores). Decide which of the 11
   invariants translate, then port the smallest proven slice. Gate: GA
   backend-default flips, not per-city opt-in.
3. **Chaos gates**: orders at-most-one-extra-fire, nudges acked-write
   survival under multi-process writers, the every-prompt drain soak —
   the design's "Testing & proof" section beyond the existing per-class
   crash tests.
4. **Final census** on a live migrated city before freezing retention +
   index choices.
5. **bd-leg retirement (one release cycle after GA)**: the verified-LIVE
   list in HANDOFF.md (close-verify retries, close_reason constants,
   UpdateMetadataInfo, doctor_backlog_depth infra predicates, nudge
   two-tier file machinery) becomes deletable only when the bd arm for
   infra classes is removed.

**Discipline / gotchas (all recorded in HANDOFF.md):** sharded test
targets only; long git-commit/push timeouts (pre-commit vets + lint +
docsync, pre-push runs the full fast suite; golangci-lint lock
collisions — wait and retry; pinned 2.9.0 built with the repo
toolchain); umask 022 for suites+push; new subprocess tests need
integration tag + THREE census artifacts (535/166 now) with files `git
add`ed first; never time.Sleep in tests; memstore does NOT honor
explicit Create ids but bd and sessionsdb do; the doctor name-set golden
(cmd/gc/testdata/doctor_check_names.golden) must gain any new check's
name. A NEW `gc` subcommand ripples through THREE more artifacts: add it
to `cmd/gc/productmetrics_command_census.json` (strictly path-sorted;
take `next_id`, bump it), regenerate via `go run
./cmd/gen-command-census`, and bump the catalog-size pin in
`internal/productmetrics/event_test.go` (currently 192). A cmd/gc test
using `t.Setenv` grows the environment ratchet — bump BOTH the Small and
source scopes across census.go + test/test-resources.toml + TESTING.md
(currently 4340/4346 calls, 206 files). Push to
`origin/feat/infra-class-sqlite-stores` before ending the session.
