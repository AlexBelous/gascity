---
title: "Infra-Class SQLite Stores"
---

| Field | Value |
|---|---|
| Status | Proposed |
| Date | 2026-07-21 |
| Author(s) | Claude Fable 5 (design), Julian Knutsen (decisions) |
| Relates to | `engdocs/design/graph-store-backend-selection.md` (the graph-class ADR, in history at `09830032e`), `engdocs/plans/store-domain-objects/` (the typed front doors this design builds on), `internal/coordclass` (the class boundary) |
| Supersedes | nothing — this is the first backend spec for the non-graph infra classes |

## Summary

Move the four non-work, non-graph coordination classes — **messaging** (mail +
extmsg), **sessions** (lifecycle + durable waits), **orders** (dispatch
tracking), and **nudges** (delivery queue) — off the bd/Dolt beads store and
onto **dedicated embedded SQLite stores** (pure-Go `modernc.org/sqlite`,
already a direct dependency), one file per class under `.gc/store/`, with
**schemas and indexes derived from each class's actual observed usage** rather
than the generic beads model. Work beads (the real backlog, the arguments to
`gc sling`) and graph beads (formula-v2 topology; covered by its own ADR) stay
on beads and are out of scope.

The stores plug into seams that already exist on main: `resolveClassStore`
(`cmd/gc/class_store.go:231`, identity today), the `config.BeadClass*`
constants, the reserved id prefixes (`gcm`/`gcs`/`gco`/`gcn`,
`internal/config/reserved_prefixes.go`), and — decisively — the
store-domain-objects migration, which already confines bead de/serialization
to the store edge and gives every class a typed front door
(`session.Info`, `mail.Message`, `orders.OrderRun`, `nudgequeue.NudgeShadow`,
`session.WaitInfo`; census-enforced by `cmd/gc/typedclass_edge_guard_test.go`).
The new stores implement those **domain surfaces directly with typed columns**.
No bead envelope, no labels-as-state, no generic `List`.

Ratified decisions (2026-07-21):

1. **Access model: direct multi-process WAL.** Every process (controller, CLI
   one-shots, prompt hooks, sidecars) opens the `.db` directly. Invariants move
   from in-process locks into the schema (UNIQUE constraints, transactions,
   `UPDATE … RETURNING`). API-first routing remains an optimization when the
   controller is up. This deliberately diverges from the graph ADR's
   controller-only rule: graph writers are controller-only; these classes are
   written from the every-prompt nudge drain, CLI fallbacks, order condition
   subprocesses, and controller-less legacy cities.
2. **Layout: one file per class, city-scoped** — `.gc/store/{messaging,
   sessions,orders,nudges}.db`. Orders drops per-rig store federation; rig
   identity is already encoded in `scoped_name`. Per-class cutover, rollback,
   backup, and schema migration stay independent.
3. **Nudges: merge both tiers.** One SQLite queue replaces the flock-guarded
   `state.json` *and* the shadow beads. The wake socket stays (transport, not
   storage).
4. **bd surface: full story.** Stores own their retention/TTL; the two
   core-pack scripts that reach into infra beads are rewritten onto `gc`
   commands; `gc bd` gains a fail-closed guard for infra-class writes; the
   `gc bd show` read federation (split branch, `124bca8c3`) is ported and
   generalized across all reserved class prefixes.

## Why now, and why this survives

The prior SQLite coordination store (deleted by #3151) was removed for
governance — a parallel backend with no seam, no conformance, no ownership —
**not** for failing (it hit every latency target: point-read p99 1.09 ms,
FilterScan p99 1.48 ms, 0 errors over 151,580 ops). The graph ADR's survival
formula now holds for these classes too:

| what killed it | what exists now |
|---|---|
| no interface-first migration story | typed domain front doors on main + `resolveClassStore` per-class dispatch |
| no conformance tests | `mailtest`-style provider conformance; `feat/split-store-conformance`'s topology suite to port |
| no ownership story | `coordclass` — each class has one owning subsystem |
| "no parallel control planes" | routed classes behind the one classify boundary, not a competing authority |

Its two rules carry over, adapted:
**wrap, don't widen** — each store implements only its class's observed
surface (the wide 19-method `StoreAdapter` SPI from
`beeac65b7^:internal/benchmarks/coordstore/adapter.go` is a completeness
*checklist*, never an interface). **Embed, don't daemonize** — no daemon, no
IPC; the file is the store. Multi-process opens replace the controller-only
rule for the reasons above.

What this buys, per the 2026-05-22 census (`09830032e:engdocs/coordination-store/discovery.md`;
re-measure before freezing targets):

- **Mail** is ~75,000:1 read-dominated (~150 inbox reads/s full-scanning open
  messages, cache-bypassed with `Live:true`). Becomes one indexed
  `(to_addr, status, read)` lookup.
- **Order-tracking** is ~3,500 beads/day of churn whose per-tick reads are two
  O(all-tracking) label scans every 30 s plus per-order max() folds. Becomes
  indexed `MAX(created_at)` / `MAX(seq)` point queries — the exact aggregate
  `DoltliteReadStore.loadOrderRuns` already implements but that has zero
  production callers (`internal/beads/doltlite_read_store.go:414`).
- **Sessions** get true `SetMetadataBatch` atomicity (today documented
  non-atomic on bd/exec — `internal/beads/beads.go:622` — worked around by
  `UpdateMetadataInfo`), plus a retention path that cannot exist today.
- **Nudges** delete the entire two-tier coherence apparatus: flock txn
  protocol, `RollbackEnqueue`, repair-on-prune, sweep-with-live-exclusion,
  the ≥20-char `close_reason` floor.
- The work store's CachingStore reconcile shrinks: infra classes are exactly
  the high-churn short-lived noise inflating its scan counts and cadence.
- End state: bd/Dolt holds only work beads (and graph until its own ADR
  lands). That is the "remove our dependence on beads" line for infra.

## Relationship to the graph class (out of scope here, converging)

Graph is excluded from this design by scoping — it travels with work beads —
but it is **not** staying on bd: its own ADR
(`09830032e:engdocs/design/graph-store-backend-selection.md`, Proposed)
already selects an embedded modernc SQLite store for `ClassGraph`, owned by
the controller at the anticipated `.gc/beads.sqlite` location, behind the
narrow `GraphStore` seam. The two tracks are independent — neither depends on
the other landing first — but they are designed to compose:

- **Same dispatch, same prefixes, same federation.** Both plug into
  `resolveClassStore` (graph's resolver arm already exists and is
  deliberately event-silent), both mint reserved-prefix ids (`gcg-` alongside
  `gcm-/gcs-/gco-/gcn-`), and the generalized `gc bd show` federation in this
  design loops over *all* reserved class prefixes — it subsumes the
  graph-only shim from `124bca8c3` rather than sitting beside it.
- **Shared substrate on offer.** `classdb/core` is the graph store's natural
  substrate when that ADR executes: the pieces this design deliberately does
  *not* port from the deleted store (deps table, `Ready()` with the
  blocking-dep subquery, main/wisp tiers, CAS claim) are exactly the
  graph-specific extensions the graph store adds on top of core.
- **Different access model, on purpose.** Graph is planned as
  controller-embedded, but note its writers are *not* controller-only:
  besides the dispatcher and molecule engine, **normal workers claim, update,
  and close graph-class beads** — `ClassifyGraphPlan` routes a formula pour
  wholesale, so the work-typed executable steps embedded in molecules are
  graph-class, surface through `Ready()` + `gc.routed_to`, and are claimed by
  `gc hook --claim` (today a direct `bd update --claim` from the worker's CLI
  subprocess), while the control dispatcher claims the `gc.kind` control lane
  via its store-scoped claim loop. The graph ADR handles this by
  *prescribing* that `gc ready`/`gc hook` route through the controller API
  (its `ReadyCandidates` + CAS-`Claim` gap items exist for exactly these
  worker claims) — viable for graph because graph work only progresses while
  a controller is running. The infra classes use direct multi-process WAL
  instead because their writers include prompt hooks, CLI fallbacks, and
  controller-less cities where no controller is guaranteed. Do not conflate
  the two disciplines; each is documented at its seam.
- **"bd doesn't support SQLite" is not a blocker — for either track.** bd
  never grows a SQLite backend. `beads.Store` is a *gascity* Go interface;
  `BdStore` (fork/exec bd) is just one implementation, and the deleted store
  was already a complete in-process SQLite `beads.Store` (Ready with
  blocking-dep subquery, deps, CAS, tiers, metadata). Graph keeps full beads
  *semantics* by implementing that surface natively; the bd-*CLI* touchpoints
  are replaced by gc-native paths the split branch already built: a composite
  `claimableStore` + top-level `gc ready` fanning work ∪ graph and merging in
  canonical (priority, created_at, id) order (`63235fe0a`, landmine #2a); the
  Go-composed work_query/count-form scripts re-routed from bare `bd` to
  `gc ready` on split cities (`2ed2fc961`, #2b — `workquery.go` is the single
  composition point, so prompts need no edits); the worker claim mutation
  routed to the owning store by reserved prefix (#2c); and HTTP ready/list
  federation (`c224a9792`, #13). R2.3's `bd` PATH-shim remains the
  last-resort compat for any bare-`bd` surface that cannot be re-pointed.
  This design's infra classes need *none* of that machinery — no worker
  claims a mail/session/order/nudge bead — which is exactly why they can
  drop generic beads behavior while graph cannot.
- **No dep edge ever spans stores.** A dep row names both endpoints in one
  store's deps table, so a cross-store edge cannot express blocking
  faithfully: bd silently stores it as a non-blocking `depends_on_external`
  row (the parent shows READY mid-DAG and can double-execute) while
  doltlite/MemStore readers fail closed (parent stranded). The split design's
  replacement (landmine #4, `eae511422`): within-graph deps — the whole
  molecule topology, which is why pours classify wholesale — stay native in
  the graph store where Ready's blocking subquery sees them; cross-boundary
  relationships become **metadata linkage enforced at the composite seam**
  (`gc.attached_workflow_root` on the work parent, `gc.attach_bead_id`/
  `gc.attach_store_ref` back-linkage on the graph root; `claimableStore.Ready`
  withholds the parent while the root is open in its owning store, fails loud
  on dangling markers). Conformance pins the rule: `splittest.StrictStore`
  makes `DepAdd` resolve both endpoints bd-shaped, and "strict cross-store
  dep rejection" is one of the topology invariants. The infra classes in this
  design sidestep the problem entirely — they create **no dep edges at all**
  (waits carry `dep_ids` as CSV metadata by design; wait↔nudge, orders, and
  extmsg couplings are all by-id references resolved through the owning
  store's front door).
- **End state.** Once *both* tracks land, bd/Dolt holds only work beads —
  the full "remove our dependence on beads" line. This design gets infra
  there; the graph ADR gets graph there.

## Shared substrate: `internal/classdb`

New fork-owned package tree (upstream-alignment: new files, no broad edits to
upstream-owned code):

```
internal/classdb/
  core/       open/close, PRAGMAs, migrations, busy-retry, sweeper, health
  orders/     ordersdb.Store   (implements the internal/orders persistence edge)
  nudges/     nudgesdb.Store   (implements nudgequeue persistence + queue)
  messaging/  maildb.Store, extmsgdb.Store
  sessions/   sessionsdb.Store, waitsdb (same file, separate tables)
```

`core` ports ~60% of the deleted store verbatim
(`ba607c16d^:internal/beads/sqlite_store.go`): dual-handle open (1 write conn
`MaxOpenConns=1`; read pool of 8 for the controller — CLI one-shots use a
single short-lived conn), `journal_mode=WAL`, `synchronous=FULL`,
`busy_timeout=5000` + app-level `retryOnBusy` (3×150 ms), `foreign_keys=ON`,
idempotent `CREATE TABLE IF NOT EXISTS` + `PRAGMA user_version` migrations,
atomic tx helper, retention sweeper scaffold (30 s cadence, per-class
policies), `closeOnce`, `Ping`/size health surface. Explicitly **not** ported:
the deps table, `Ready()`, tier columns, the bead-JSON blob, CAS release —
graph/work concerns.

**Gate G0 (before anything ships):** prove modernc multi-process WAL on Linux
with the revived chaos harness (`beeac65b7^:internal/benchmarks/coordstore/`
chaos trio: re-exec child host, kill mid-workload, verify every acked write
survives reopen) extended to concurrent multi-process writers. Fallback if
shm-WAL misbehaves under modernc: `journal_mode=DELETE` (plain POSIX-lock
rollback journal — correct multi-process, slightly worse read concurrency,
fine at these volumes).

## Per-class specifications

Interfaces below are derived strictly from the exhaustive call-site
inventories (every `beads.Store` method each subsystem actually calls, cited
in the inventory reports). Each store also implements
`IDPrefix() string` returning its reserved prefix, mints new ids as
`<prefix>-<seq>`, and **accepts legacy-prefixed ids on import** so migrated
references (wait `dep_ids`, `nudge_id` stamps, session ids in runtime state)
stay valid. `storeref.Resolve`'s probe-all fallback covers legacy ids on the
federation path.

### Orders (`gco`, first to cut over)

Today: one bead per fire, `order-tracking` + `order-run:<scoped>` labels,
outcome as one-of-seven label families, cursor as `order:<scoped>`+`seq:<N>`
labels, created-open-before-goroutine as the single-flight marker, CreatedAt
as the cooldown clock, NoHistory tier, close-verify 3×/25 ms retries to
tolerate Dolt write lag.

```sql
CREATE TABLE order_run (
  id           TEXT PRIMARY KEY,
  scoped_name  TEXT NOT NULL,          -- "<name>" or "<name>:rig:<rig>"
  created_at   INTEGER NOT NULL,       -- cooldown clock
  updated_at   INTEGER NOT NULL,       -- retention reference
  open         INTEGER NOT NULL,       -- single-flight marker
  outcome      TEXT NOT NULL DEFAULT '',  -- exec|exec-failed|exec-env-failed|wisp|wisp-failed|wisp-canceled|trigger-env-failed
  seq          INTEGER NOT NULL DEFAULT 0,-- event-cursor high water
  close_reason TEXT NOT NULL DEFAULT '',
  sweep_by     TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_run_scoped_created ON order_run(scoped_name, created_at DESC, id DESC);
CREATE INDEX idx_run_open           ON order_run(scoped_name) WHERE open=1;
CREATE INDEX idx_run_stale          ON order_run(created_at)  WHERE open=1;
CREATE INDEX idx_run_retention      ON order_run(open, scoped_name, updated_at);
```

Interface = today's `orders.Store` surface (`CreateRun`, `CreateRunClosed`,
`SetOutcome`, `SetCursor`, `MarkFailed` (one atomic UPDATE), `CloseRun(s)`,
`DeleteRun`, `LastRun`, `Cursor`, `HasOpenTracking`, `OpenRuns`,
`RecentRuns(All)`, `StaleOpenRuns`, `OrphanedOpenRuns` (excludes
trigger-env-failed), `ClosedRunsForRetention`, `ListTracking`,
`LatestOpenRun`, `Get`, `RunDetail`) — returning `orders.OrderRun` directly.
The per-tick `OpenRuns` + `RecentRunsAll(256)` scans collapse to
`SELECT MAX(created_at) … WHERE scoped_name=?` and
`EXISTS(… open=1)` point queries.

Load-bearing invariants to reproduce exactly:
- **At-most-one-extra-fire on crash**: row durable before the dispatch
  goroutine launches; startup orphan sweep closes leftover-open rows
  (`cmd/gc/city_runtime.go:270` semantics unchanged).
- **Cursor-before-side-effect**: `SetCursor` commits before the command runs.
- **Ordering tie-break**: created-DESC with **id-DESC tie-break** (the
  `nativeCreatedLimitPushdown` contract,
  `internal/beads/native_dolt_store.go:2016`) — baked into
  `idx_run_scoped_created`.
- The close-verify retry loop and the ≥20-char `close_reason` floor become
  dead code (synchronous local commits); remove at the call sites, don't port.

**Cross-class boundary:** the `order-run:`/`seq:` labels stamped on graph wisp
roots stay graph-class. Cooldown and cursor become authoritative in
`order_run` (the tracking-bead leg already receives every `SetCursor`);
migration seeds `seq` from the union of both legs. `HasOpenWork`'s wisp-subtree
walk remains a graph/work-store read — it is genuinely a graph question and its
store does not move. Retention: 7 d `delete_after_close`, retain-last-10 per
`scoped_name` (existing config), enforced by the store's sweeper.

### Nudges (`gcn`, merged queue)

One table replaces `state.json` buckets and shadow beads:

```sql
CREATE TABLE nudges (
  id            TEXT PRIMARY KEY,           -- "nudge-<12hex>" or deterministic "wait-<id>-<epoch>-<attempt>"
  agent         TEXT NOT NULL,
  session_id    TEXT NOT NULL DEFAULT '',
  continuation_epoch TEXT NOT NULL DEFAULT '',
  source        TEXT NOT NULL,              -- session|mail|wait|sling
  message       TEXT NOT NULL,
  ref_kind      TEXT NOT NULL DEFAULT '',
  ref_id        TEXT NOT NULL DEFAULT '',
  created_at    INTEGER NOT NULL,
  deliver_after INTEGER NOT NULL,
  expires_at    INTEGER NOT NULL,
  attempts      INTEGER NOT NULL DEFAULT 0,
  last_attempt_at INTEGER, last_error TEXT NOT NULL DEFAULT '',
  claimed_at    INTEGER, lease_until INTEGER, dead_at INTEGER,
  queue_state   TEXT NOT NULL CHECK(queue_state IN ('pending','in_flight','dead','terminal')),
  terminal_state TEXT NOT NULL DEFAULT '',  -- accepted_for_injection|injected|expired|failed|superseded|gc-swept
  terminal_reason TEXT NOT NULL DEFAULT '',
  commit_boundary TEXT NOT NULL DEFAULT '',
  terminal_at   INTEGER
);
CREATE INDEX idx_nudges_claim     ON nudges(queue_state, agent, deliver_after);
CREATE INDEX idx_nudges_lease     ON nudges(queue_state, lease_until);
CREATE INDEX idx_nudges_ref       ON nudges(agent, source, ref_kind, ref_id) WHERE queue_state IN ('pending','in_flight');
CREATE INDEX idx_nudges_retention ON nudges(queue_state, terminal_at);
CREATE INDEX idx_nudges_expiry    ON nudges(queue_state, expires_at);
```

- Claim = one `UPDATE … SET queue_state='in_flight', claimed_at=?,
  lease_until=? WHERE queue_state='pending' AND deliver_after<=? AND agent IN
  (…) RETURNING *` — note claim matches a **set** of queue keys (alias
  history, session id, qualified name), not one agent string.
- Supersession = one UPDATE over `idx_nudges_ref`; **preserve the semantics,
  not just the schema**: a superseded in-flight delivery may still complete
  (at most one redundant delivery; ack no-ops), and dead-letter stamping on
  failure must not roll back the dead-letter itself — do not "fix" these into
  stricter ordering or the InFlight/Pending bounce returns.
- Enqueue+shadow become one INSERT: `Save`'s `beadID` return,
  `RollbackEnqueue`, `SweepStale`, `StaleShadowsBefore` all delete.
  `Find`/`FindIncludingTerminal`/`WithdrawByIDs` (the surfaces waits actually
  need) remain, now point reads.
- The `session.Manager` deferred-submit path (writes `state.json` directly
  with no shadow today, `internal/session/submit.go:544`) folds into the
  store with a proper `source` value.
- Wake socket (`.gc/runtime/nudges/wake.sock`) and per-session poll cadence
  unchanged. Session fence (`session_id`/`continuation_epoch` vs live target)
  unchanged.
- Retention: terminal rows deleted past a configurable TTL (default 24 h;
  dead-bucket 1 h behavior preserved via `dead_at`). New typed events
  `nudge.queued` / `nudge.delivered` / `nudge.dead` replace the incidental
  bead.* observability (today internal/events has no nudge event at all).

### Messaging (`gcm`) — mail table + extmsg typed tables, one file

**Mail** (read-poll dominated; today ephemeral wisp beads):

```sql
CREATE TABLE messages (
  id TEXT PRIMARY KEY, thread_id TEXT NOT NULL, reply_to_id TEXT DEFAULT '',
  from_addr TEXT NOT NULL, to_addr TEXT NOT NULL,
  from_session_id TEXT DEFAULT '', from_display TEXT DEFAULT '',
  to_session_id TEXT DEFAULT '', to_display TEXT DEFAULT '',
  subject TEXT NOT NULL, body TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  read INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'open',      -- open | closed (retention-swept)
  close_reason TEXT NOT NULL DEFAULT '',
  auto_handoff INTEGER NOT NULL DEFAULT 0,  -- gc:auto-handoff / gc:archive-after-inject
  archive_after_inject INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_msg_inbox  ON messages(to_addr, status, read);
CREATE INDEX idx_msg_thread ON messages(thread_id, created_at, id);
CREATE INDEX idx_msg_sweep  ON messages(read, status, created_at);
```

Interface = the `beadmail` persistence edge (`Create`, `Get`, `SetRead`,
`Delete`, `ListOpenForRecipients(routes, includeRead)`, `ListThread`,
`CountForRecipients`, `ListReadCreatedBefore`, `CloseReadWithReason`,
`PurgeReadCreatedBefore`) returning `mail.Message`. Recipient-route expansion
keeps reading the *sessions* store — addressing is a session concern, not a
message-store concern. Semantics to preserve: `mail.read` metadata-wins
precedence becomes simply the `read` column; the `6b0eb0d6b` distinction
(retention-swept mail stays addressable by id; user-removed mail is
not-found) maps to `status='closed' ∧ close_reason=RetentionSweepCloseReason`
vs row-deleted. New: an **unread-mail TTL** (prescribed 30 d; today unread
mail leaks forever, +200/day at census) in the store sweeper.

**extmsg** (pure label-index KV today; in-process per-conversation mutex pool
keyed by store-pointer identity — invalid under multi-process access, so the
invariants move into the schema): typed tables `bindings`,
`delivery_contexts`, `groups`, `participants`, `memberships`,
`transcript_state`, `transcript_entries`, sharing conversation-identity
columns `(scope_id, provider, account_id, conversation_id,
parent_conversation_id, kind)` with composite indexes replacing the sha256
locator labels. The decode-time "invariant violation" checks become real
constraints:

- `UNIQUE(conv-cols) ` on `transcript_state`; `next_sequence` allocated via
  `UPDATE … RETURNING` in a transaction (replaces the lock-held counter).
- `UNIQUE(conv-cols, provider_message_id)` on `transcript_entries` (inbound
  idempotency — today only detected on read).
- `UNIQUE(conv-cols, session_id) WHERE status='open'` on `memberships`;
  at-most-one-active-binding enforced by a partial unique index rather than
  the Tx + compensating-order fallback.
- The 64-entry transcript "buckets" disappear — `idx(conv-cols, sequence)`
  makes range scans direct.
- New: transcript retention — `max_retained_entries` (written as 0 and never
  enforced today) becomes a real pruning knob; closed bindings/memberships
  get a TTL. extmsg currently never deletes anything and grows unbounded.

The bind/rebind multi-record commit keeps its transaction (now a real SQLite
tx). `gc extmsg` is already a pure API client; `gc mail`'s direct-open
fallback and `gc order sweep-nudge-mail` use short-lived WAL opens.

### Sessions + waits (`gcs`, last to cut over, shadow-write gated)

The durability-critical class: open session rows are the projection the
reconciler re-derives all state from after a controller restart
(`internal/session/lifecycle_projection.go`), converging in 30–60 s.

```sql
CREATE TABLE sessions (
  id TEXT PRIMARY KEY, title TEXT DEFAULT '',
  agent_name TEXT DEFAULT '', template TEXT DEFAULT '', session_name TEXT DEFAULT '',
  status TEXT NOT NULL DEFAULT 'open' CHECK(status IN ('open','closed')),
  state TEXT DEFAULT '',                    -- hot lifecycle key
  configured_named_identity TEXT DEFAULT '', pool_slot TEXT DEFAULT '',
  generation TEXT DEFAULT '', instance_token TEXT DEFAULT '',
  pending_create_claim TEXT DEFAULT '', pending_create_started_at INTEGER,
  last_woke_at INTEGER, created_at INTEGER NOT NULL, closed_at INTEGER,
  meta TEXT NOT NULL DEFAULT '{}'           -- remaining ~75 low-churn codec keys, JSON
);
CREATE INDEX idx_sessions_open   ON sessions(status, state);
CREATE INDEX idx_sessions_name   ON sessions(session_name) WHERE status='open';
CREATE INDEX idx_sessions_ident  ON sessions(configured_named_identity) WHERE status='open';
CREATE INDEX idx_sessions_created ON sessions(created_at);

CREATE TABLE session_circuit (                -- the deliberate off-Info sidecar
  session_id TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
  state TEXT DEFAULT '', restarts_json TEXT DEFAULT '[]',
  last_restart INTEGER, last_progress INTEGER, last_observed INTEGER,
  progress_signature TEXT DEFAULT '', opened_at INTEGER,
  open_restart_count INTEGER DEFAULT 0, reset_generation INTEGER DEFAULT 0
);

CREATE TABLE waits (
  id TEXT PRIMARY KEY, session_id TEXT NOT NULL, session_name TEXT DEFAULT '',
  kind TEXT DEFAULT 'deps', state TEXT NOT NULL,  -- pending|ready|closed|canceled|expired|failed
  dep_mode TEXT DEFAULT 'all', dep_ids TEXT DEFAULT '',  -- CSV, as today
  registered_epoch TEXT DEFAULT '', delivery_attempt INTEGER DEFAULT 1,
  nudge_id TEXT DEFAULT '', note TEXT DEFAULT '',
  status TEXT NOT NULL DEFAULT 'open',
  expires_at INTEGER, created_at INTEGER NOT NULL,
  ready_at INTEGER, closed_at INTEGER, failed_at INTEGER,
  expired_at INTEGER, canceled_at INTEGER,
  last_error TEXT DEFAULT '', commit_boundary TEXT DEFAULT ''
);
CREATE INDEX idx_waits_session ON waits(session_id, status);
CREATE INDEX idx_waits_open    ON waits(status);
CREATE INDEX idx_waits_nudge   ON waits(nudge_id);
```

- Hot columns are exactly the keys the reconciler union, adoption barrier,
  and create/wake fences filter on; the remaining ~75 codec keys ride the
  `meta` JSON column. The Type+Label union list collapses to one indexed
  query.
- **`SetMetadataBatch` becomes genuinely atomic** (one UPDATE). The
  `UpdateMetadataInfo` single-op workaround becomes redundant — retire it
  deliberately; never rely on the old partial-write behavior.
- Close's two-op window (stamp `ClosePatch`, then `Close`) becomes one
  transaction.
- The reconciler's 0-Get tick budget and `SetFingerprint`-over-all-metadata
  are pinned invariants (`session_reconciler_tick_budget_test.go`): the
  snapshot query returns full rows, and the fingerprint is computed
  edge-side as today. Later optimization (not this design): a store-native
  change token.
- **New retention**: closed sessions purgeable after a configurable TTL
  (default 7 d) — a DELETE path that cannot exist today.
- Waits↔nudges stays a **cross-file eventual-consistency** handoff (point
  reads by `nudge_id`, retry next tick) — exactly today's semantics; there is
  no shared transaction now either.
- `doctor_session_model.go` re-implements the raw union and must migrate in
  lockstep.

## Cross-cutting integration

**Config.** New `[beads.classes.<name>]` table on `config.BeadsConfig`:
`backend = "bd" (default) | "sqlite"`, resolved inside `resolveClassStore`
(read side) and `beadPolicyStore.createTarget`/`graphApplierFor` (create side)
— both keyed off `coordclass.Classify`, the single boundary. Unknown backend
values hard-error, following the removed-provider precedent
(`cmd/gc/main.go:1372`).

**Prerequisite call-site fixes (P0, before any relocation).** Several
resolver call sites defeat the seam today: `openNudgeBeadStore` passes nil
`cfg` (the per-prompt-hook drain and the `cmd_wait.go:1392` withdraw path
would silently resolve to the work store); `cliSessionStore` and
`openCityMailProvider` pass nil recorder. Thread real `cfg`+`rec` through all
of them. Fix the extmsg reapers' miswired handles: they take
`beads.SessionStore` but mutate messaging beads
(`cmd/gc/extmsg_binding_reaper.go`) — give them an explicit two-handle
signature (sessions read, messaging write). Upgrade
`config.ValidateRigs` to **hard-reject** rig/HQ prefixes that collide with
reserved class prefixes (advisory warning today).

**Events.** All four classes go **bead.*-silent**; their semantic channels
(`mail.*`, `extmsg.*`, `session.*`, `order.fired/completed/failed`) are
emitted by callers and survive unchanged. Add `nudge.*` typed events (gap
today). Verification item before cutover: confirm no live consumer needs
bead.* for these classes — dashboard SSE's convoy stream is RootBeadID-guarded
(graph-only), but audit `runBeadCloseAutoclose` and any bead.*-keyed watcher.

**Cache.** Relocated classes leave the CachingStore universe: reconcile scans
shrink (they were the churn), and every `CachedList`/`CachedReady` consumer of
a relocated class must re-point to the class store — a cache miss looks like
"no beads", not an error, so this is audited per class, not assumed. Sessions'
`CacheFirst` dashboard tier falls through to the store (an indexed local read;
the cache existed because bd forks were slow).

**Counters/status.** The messaging store implements native counts for the
city-wide unread badge (`handler_status.go:898`); `statusWorkExcludedTypes`
becomes moot for relocated classes. Other stores expose the few counts their
surfaces need — none implement the generic `Counter` capability.

**Doctor / storehealth / maintenance / backup.**
- `doctor_backlog_depth.go`'s notification/control-plane buckets (which read
  three infra classes out of one work-store scan) retire; per-class health
  moves to the stores (open sessions, unread mail, open tracking, queue
  depth).
- `storehealth` extends `StorePath`/`WalkSize` to include `.gc/store/*.db`.
- The maintenance loop adds per-file `wal_checkpoint(TRUNCATE)` + periodic
  `VACUUM` (today only Dolt gets maintenance; new files would otherwise get
  none).
- The managed-backup manifest (mol-dog-backup) must enumerate `.gc/store/` —
  external coordination item.

**Generic read surfaces.** `GET /beads`, `bd list`, and the `BeadStores()`
fan-out silently lose relocated classes. Accepted, with two mitigations:
per-class API endpoints already exist for the surfaces that matter (mail,
sessions, waits, orders feed), and the `gc bd show` federation below covers
residual by-id reads.

## The bd / raw-prompt story

Inventory result: **no shipped prompt type-targets an infra bead via bd**;
every hit is indirect through a variable id, concentrated in two core-pack
health-patrol scripts. Identity (agent/role/rig) records classify to
`ClassWork` by default and have no live bd creation site in this tree — they
stay in bd, zero work. The full story, as ratified:

1. **Stores own retention** (specified per class above) — this deletes the
   *reason* `reaper.sh` reaches into raw Dolt SQL (`DELETE FROM issues WHERE
   issue_type='session'`, the `gc:nudge` expiry JOIN, `issue_type='message'`
   filters — all of which would otherwise become silent no-ops post-split).
2. **Rewrite the two scripts** (CI-guarded, shipped via go:embed, so exact
   string assertions in `pack_assets_test.go` et al. move with them):
   `orphan-sweep.sh`'s `gc bd show "$session_id"` → `gc session show --json`
   (add if missing); `reaper.sh`'s nudge close + session/message SQL → the
   stores' own reaping plus `gc session prune` extended to closed session
   rows.
3. **Fail-closed `gc bd` write guard**: `doBd` refuses `create`/`update`/
   `close` targeting infra classes (reserved-prefix id, or a create carrying
   an infra type/label) with a message naming the `gc` replacement — a stray
   prompt can no longer mint a divergent infra bead in the work db.
4. **Generalized read federation**: port `cmd_bd_show_fed.go` (`124bca8c3`)
   and widen the gate from `BeadClassGraph` to a loop over
   `config.ReservedClassPrefixes()`, backed by per-class by-id GET endpoints.
   Preserve its 404-vs-error discipline (a genuine 404 renders bd's "no issue
   found"; any other error surfaces distinctly — the root-loss lesson).
   Reads only; writes stay guarded.

## Migration & cutover

Per-class, independent, reversible — provider swap plus a bounded import, in
risk order:

| order | class | migration | gate |
|---|---|---|---|
| 1 | orders | import closed runs ≤7 d + any open markers; seed `seq` from tracking∪wisp-root legs | chaos: at-most-one-extra-fire |
| 2 | nudges | drain-or-import live queue items; shadow history ≤24 h | chaos: acked-write survival; every-prompt drain soak |
| 3 | messaging | import open mail + extmsg actives; drop >30 d unread, >TTL read | mail-poll bench; extmsg UNIQUE backfill clean |
| 4 | sessions | full import of open beads + waits (ids preserved); **24–48 h shadow-write with zero-discrepancy diff** (R2.3 protocol) | restart-projection equivalence; 0-Get tick budget holds |

Mechanics: port the `gc migrate infra-store` slice from
`feat/split-store-conformance` (`cmd_migrate_infra_store.go`) adapted to
bd→sqlite via the domain codecs — owner-gated stop-the-world per class,
idempotent/resumable with **no status file** (plan recomputed from live
state), copy-verify-delete, boundary authority = `coordclass.Classify`,
reconciliation ledger (`work_after == work_before − moved`). Rollback per
class = flip the config knob back + replay-or-accept-loss per class's
disposability (48 h Dolt cold-backup window for sessions; the ephemeral
classes are within-TTL disposable). Shadow-write applies to sessions only.

## Testing & proof

- **Domain conformance**: each front door already has or gets a conformance
  suite run against *both* backends during transition (`mailtest/conformance.go`
  is the existing pattern; add equivalents for orders/nudges/sessions).
- **Topology conformance**: port `splittest` + the 11-invariant × 2-topology
  suite and static guards from `feat/split-store-conformance`, adapted to
  per-class sqlite backends (single-store collapse must stay byte-identical
  while flags are off).
- **Crash/chaos**: revive the coordstore chaos harness (G0) for the
  durability contracts: orders at-most-one-extra-fire, sessions
  restart-projection, nudges acked-write survival, multi-process concurrent
  writers.
- **Bench gate** (ADR "must prove, not assume"): revive workload/scorecard
  runner with a Dolt/current-backend baseline; ship gates = census targets
  (point p99 ≤1 ms, filter ≤10 ms, writes ≤5 ms) on current hardware, plus
  the two hot paths: mail inbox poll, orders tick.
- **Census refresh**: re-measure per-class volumes before freezing retention
  and index choices (2026-05-22 census is stale; nudges were fs-only then).

## Work plan

- **P0 — prerequisites** (no behavior change): thread cfg/rec through nil
  call sites; extmsg reaper two-handle fix; `ValidateRigs` hard-reject;
  `[beads.classes]` config parsing (all values still "bd"); G0 multi-process
  WAL validation + chaos harness revival; port splittest scaffolding.
- **P1 — `classdb/core` + orders** end-to-end behind
  `[beads.classes.orders] backend="sqlite"`: store, migration slice,
  doctor/storehealth/maintenance extension, retention, cutover on a dev city.
- **P2 — nudges** merged queue + `nudge.*` events + reaper.sh nudge-leg
  rewrite + deferred-submit fold-in.
- **P3 — messaging**: mail table + retention (incl. unread TTL) + native
  counts; then extmsg typed tables + constraints + transcript pruning.
- **P4 — sessions+waits**: store + shadow-write gate + reconciler/doctor
  lockstep + `gc session show/prune` + orphan-sweep.sh rewrite.
- **P5 — bd surface + cleanup**: `gc bd` write guard + generalized read
  federation; retire dead code (close-verify retries, close_reason floors,
  `UpdateMetadataInfo`, doctor buckets, doltlite order-run cache,
  nudge two-tier machinery); docs; final census.

Each phase lands as reviewable slices on this branch; every phase leaves the
tree shippable with all flags defaulting to "bd".

## Remaining open questions

1. Does anything consume bead.* events for these classes (the
   `runBeadCloseAutoclose` audit) — verification task in P0, expected answer
   "no".
2. Are the decode-only mail `priority:`/`cc:` labels populated by any exec
   provider in production? (Schema currently drops them.)
3. Exact retention defaults (nudge terminal TTL, session purge TTL,
   transcript `max_retained_entries`) — proposed above, cheap to change.
4. mol-dog-backup manifest ownership for `.gc/store/` (external repo).

## References

- Deleted store: `git show ba607c16d^:internal/beads/sqlite_store.go`
- Graph ADR: `git show 09830032e:engdocs/design/graph-store-backend-selection.md`
- Census + rounds: `git show 09830032e:engdocs/coordination-store/{discovery.md,round2/}`
- SPI checklist: `git show beeac65b7^:internal/benchmarks/coordstore/adapter.go`
- Bench/chaos harness: `beeac65b7^:internal/benchmarks/coordstore/`
- Conformance + migration to port: `feat/split-store-conformance`
  (`internal/beads/splittest`, `cmd/gc/split_topology_conformance_test.go`,
  `cmd/gc/cmd_migrate_infra_store.go`); resolver seams:
  `feat/domain-infra-store-split:cmd/gc/class_store.go`
- bd federation to port: `124bca8c3` (`cmd_bd_show_fed.go`), `5a09b4cdf`
- Typed front doors: `engdocs/plans/store-domain-objects/` (on main)
