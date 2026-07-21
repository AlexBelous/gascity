# P2 nudges — seam plan (slice 1 spec)

Evidence-grade inventory of the two-tier nudge machinery, and the exact
extraction plan for the P2 slice-1 backend seam. Derived from a full read of
`internal/nudgequeue/{state,store,waits}.go`, `cmd/gc/{cmd_nudge,
nudge_dispatcher,nudge_beads,nudge_mail_sweep}.go`, `cmd/gc/cmd_wait.go`,
`internal/session/submit.go`, `internal/nudgepoller/poller.go`, and the
test suites (2026-07-21).

## Today's architecture (what the seam must preserve)

- **Queue authority**: flock'd `<city>/.gc/runtime/nudges/state.json` —
  three buckets (`Pending`/`InFlight`/`Dead` of `nudgequeue.Item`). Every
  mutation is a closure over `*State` inside `nudgequeue.WithState`
  (state.go:90-122: blocking `LOCK_EX` on `state.lock`, load, fn, atomic
  rewrite). `LoadState` reads WITHOUT the lock (dirty reads by design at
  three read-only sites).
- **Shadow beads** (`gc:nudge` label, type `chore`) are observability only —
  "THE BEAD IS A SHADOW" (store.go:14-29). Typed front door
  `nudgequeue.Store` confines the codec: `Save` (create-if-absent, returns
  beadID), `Terminalize` (stamp 7 keys + Close, BeadID-first with label-find
  fallback, missing tolerated), `RollbackEnqueue`, `SweepStale`,
  `StaleShadowsBefore` (live-ID exclusion inside), `Find` /
  `FindIncludingTerminal`, all nil-receiver-safe no-ops.
- **The queue OPS live in cmd/gc/cmd_nudge.go**, each a closure through
  `withNudgeQueueState` (2474-2476):

| op (cmd_nudge.go) | line | what happens inside the flock txn |
|---|---|---|
| `claimDueQueuedNudgesMatching` | 1831 | maintenance passes (below), then matching due Pending → InFlight (`ClaimedAt=now`, `LeaseUntil=now+2m`) |
| `enqueueQueuedNudgeWithStore` | 2016 | `front.Save` (shadow, beadID onto item), supersession (same agent+source+ref → `Terminalize("superseded")`, error ABORTS txn), append Pending; on txn failure `RollbackEnqueue` of the leaked bead; wake-socket ping AFTER (outside) |
| `ackQueuedNudgesWithOutcome` | 2116 | remove ids from InFlight + `Terminalize(outcome, reason, boundary)` inside txn (error aborts) |
| `releaseQueuedNudgeClaims` | 2170 | InFlight → Pending (undelivered / session gone) |
| `recordQueuedNudgeFailureDetailed` | 2236 | `failedQueuedNudge` (2302): fence-mismatch → instant Dead; attempts≥5 or expired → Dead; else requeue `DeliverAfter=now+15s`; bead `Terminalize` OUTSIDE the lock, best-effort (dead-letter transition is authoritative, 2288-2293) |
| `listQueuedNudges` / `...ForTarget` | 1875/1913 | read-only snapshot |
| `rollbackQueuedNudge` | 1955 | remove by id + `Terminalize`; fallback Terminalize when not found |

- **Maintenance passes** run inside every claim/enqueue txn:
  `recoverExpiredInFlightNudges` (2364: expired lease → Pending; expired TTL
  → Dead + best-effort Terminalize), `pruneExpiredQueuedNudges` (2339),
  `pruneDeadQueuedNudges` (2398: prune only when a CONFIRMED terminal bead
  exists — `FindIncludingTerminal`, lookup errors fail OPEN — and DeadAt
  past 1h). Foreground enqueue caps maintenance at 2s
  (`nudgeEnqueueMaintenanceBudget`); other callers effectively unbounded.
- **Claim matching** is a cmd/gc concern today: `queuedNudgeClaimableForTarget`
  (1747) matches item.Agent against `target.queueKeys()` (alias history +
  session id + qualified name) and enforces the fence claim gate;
  `queuedNudgeMatchesTargetFence` (1737) is the delivery gate
  (session_id/continuation_epoch equality); fence mismatches are
  dead-lettered instantly (`errNudgeSessionFenceMismatch`).
- **Constants** (cmd_nudge.go:37-57): TTL 24h, claim lease 2m, retry delay
  15s, max attempts 5, dead retention 1h, poll interval 2s, quiescence 3s,
  start grace 5m.
- **Other writers/readers of the raw state**:
  - `internal/session/submit.go:543-570` — deferred submit: Manager imports
    nudgequeue, appends Item{Source:"session", SessionID, epoch, TTL 24h,
    no BeadID} via `WithState`; NO shadow, NO wake ping; spawns `gc nudge
    poll` sidecar.
  - `nudge_dispatcher.go:122` — supervisor pass `LoadState` (read-only) to
    build pendingAgents (due Pending + expired-lease InFlight).
  - `cmd_order.go:1892` + `city_runtime.go:1596` — sweeps `LoadState` for
    the live-ID exclusion set (fail-closed on error).
  - `nudgequeue/waits.go WithdrawWaitNudges` — snapshot txn → bead
    terminalize (outside lock, proven by queueLockDetectStore test) →
    remove txn; partial-failure semantics pinned by tests.
- **Wake socket**: transport only (`state.go:163`); ping after enqueue
  (cmd/gc only); supervisor listener coalesces into `nudgeWakeCh`.
- **Wait coupling**: deterministic ids `wait-<id>-<epoch>-<attempt>`
  (cmd_wait.go:1335); enqueue guarded by `front.Find`; wait finalization
  reads `FindIncludingTerminal` terminal fields; `nextWaitDeliveryAttempt`
  increments only when the prior shadow is absent/terminal.

## Slice 1: the seam (byte-identical)

Introduce a domain front door `nudgequeue.Queue` owning the queue ops, with
an unexported backend interface (exported methods, orders-pattern):

```go
// queueBackend: the nudge-queue persistence authority.
type queueBackend interface {
    Enqueue(item Item, opts EnqueueOpts) (EnqueueResult, error)   // supersession + shadow inside
    ClaimDue(target ClaimTarget, now time.Time, deadline time.Time) ([]Item, error)
    Ack(ids []string, outcome, reason, boundary string, now time.Time) error
    ReleaseClaims(ids []string) error
    RecordFailure(item Item, deliveryErr error, now time.Time) error
    List() (State, error)                       // snapshot reads
    ListForTarget(target ClaimTarget) ([]Item, error)
    Rollback(id string, now time.Time) error
    WithdrawWaitNudges(ids []string, now time.Time) error
    // shadow/observability reads stay: Find / FindIncludingTerminal /
    // StaleShadowsBefore / SweepStale (merged into the row on sqlite)
}

// ClaimTarget: plain values extracted from cmd/gc's nudgeTarget.
type ClaimTarget struct {
    QueueKeys         []string // alias history ∪ session id ∪ qualified name
    SessionID         string
    ContinuationEpoch string
}
```

The two-tier impl (`fileQueue`? name TBD) moves the cmd_nudge.go op bodies
verbatim (flock closures + shadow front door + maintenance passes +
constants). cmd/gc keeps thin wrappers under the EXISTING function names so
the ~90 cmd/gc test call sites stay untouched (the orders slice-3 pattern);
cmd/gc also keeps: store opening, wake-socket ping, poller spawning, session
observation/fences at delivery time, and the wait-blocked split (reads
gc:wait beads — session class).

`session.Manager` deferred submit switches from raw `WithState` to
`Queue.Enqueue` with `Source:"session"` (fold-in per design; behavior today
= no shadow, no ping — preserve exactly, via EnqueueOpts).

Read-only `LoadState` sites (dispatcher pass, sweep live-ID sets) route
through `Queue.List()`.

## Slice 2+ (per design, unchanged)

sqlite backend = ONE `nudges` table (design schema): enqueue = one INSERT
(no shadow, no rollback), claim = UPDATE…RETURNING over the queue-key set,
ack/fail/withdraw = terminal-state UPDATEs (rows stop being deleted on ack —
`queue_state='terminal'`), supersession = one UPDATE over `idx_nudges_ref`
preserving at-most-one-redundant-delivery. `Find`/`FindIncludingTerminal`
become row reads; `StaleShadowsBefore`/`SweepStale` die (retention = the
store's own `core.StartSweeper`: terminal TTL 24h, dead bucket 1h). Then
wiring behind `[beads.classes.nudges]` + `.gc/store/nudges.migrated`
(orders pattern), migration (drain-or-import), `nudge.*` typed events,
reaper.sh nudge-leg rewrite.

## Gotchas specific to nudges

- `openNudgeBeadStore` SWALLOWS open errors (returns zero NudgesStore) and
  discards config-load errors — the hottest CLI root (every prompt hook).
  Routing must keep the fail-open delivery semantics for bd but fail CLOSED
  for a routed city (orders pattern) — reconcile carefully.
- Several sites wrap the RAW store bypassing `resolveNudgesStore`
  (cmd_wait.go:715, 1000, 1142; cmd_order.go:1907, 1925) — byte-identical
  today, but the wiring slice must route them or they split the class.
- `LoadState` dirty reads (no lock) at 3 sites — sqlite reads are naturally
  consistent; do not add locking semantics the callers don't have.
- The withdraw two-phase (snapshot → bead-terminal outside lock → remove)
  has pinned partial-failure and same-ID-reenqueue-survives tests
  (waits_test.go:311/349/413/464) — the sqlite impl must preserve those
  observable outcomes even though the mechanics collapse to row updates.
- Supersession failure ABORTS enqueue today (error propagates out of the
  txn); dead-letter stamping is deliberately NOT rolled back. Preserve, do
  not "fix".
