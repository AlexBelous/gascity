# P4 sessions+waits — seam plan (slice 1 spec)

Evidence-grade inventory of the sessions-class persistence edge and the
structural decision for the P4 slices. Derived from a full read of
`internal/session/{info_store,store,info_codec,info_apply_patch,list_all,
wait_store,waits,create,lifecycle_projection,pending_create_lease,
circuit_state}.go`, `internal/session/manager.go` (bead-op sites),
`cmd/gc/{class_store,session_beads,doctor_session_model,adoption_barrier,
session_wake,cmd_wait,cmd_session}.go`, the reconciler tick-budget test, and
repo-wide construction-root / consumer sweeps (2026-07-22).

## What the class is

ClassSessions covers TWO bead shapes in one store file (design:
`.gc/store/sessions.db`, prefix `gcs`):

- **Session beads**: type `"session"` (`session.BeadType`), label
  `gc:session` (`LabelSession`), plus `agent:<name>` (front-door create) or
  `template:<name>` (Manager creates, manager.go:899/1142) labels.
  Metadata is an OPEN key vocabulary: 99 codec keys (info_codec.go:41-230)
  PLUS non-codec keys that must still round-trip — the nine
  `session_circuit_*` keys (circuit_state.go:17-37), `env.*`,
  `archived_at`, drain stamps, `wake_requested_at`, wait-lookup-cap
  diagnostics, etc.
- **Wait beads**: type `"gate"` (`WaitBeadType`; legacy reads accept
  `"wait"`), labels `gc:wait` + `session:<sessionID>`, ~15 metadata keys
  (WaitInfoFromBead, waits.go:137), Note in bead Description.
  `dep_ids` reference WORK-store beads by id (cross-store by-id, no dep
  edges — per design).

## Today's persistence edge (what the seam must preserve)

The codec is ALREADY confined to `internal/session` — there is no bd codec
to move (unlike P2/P3). The edge is the generic bead-op vocabulary spoken by
three actors against a resolved sessions-class `beads.Store`:

1. **`*session.Store`** (front door, `beads.SessionStore` wrapper):
   Get/List/ListByMetadata/ListByLabel + SetMetadata(Batch)/Update/Close/
   CloseAll/Create — read half info_store.go, write half store.go, waits
   wait_store.go, union family list_all.go.
2. **`session.Manager`** (`m.store beads.Store`, manager.go:560): raw
   Create (:899/:1142, `template:` label), 15×SetMetadata,
   10×SetMetadataBatch, Update, Close, Get, List.
3. **Package funcs**: `ListAllSessionBeads(store, base)` (list_all.go:38),
   `RepairEmptyType(store, &b)`.

Exhaustive op set (grep-verified over internal/session non-test):
`Create, Get, List, ListByMetadata, ListByLabel, SetMetadata,
SetMetadataBatch, Update{Metadata,Status,Type,Labels,RemoveLabels},
Close, CloseAll` — plus the OPTIONAL `CachedList` capability assertion
(list_all.go:160/371, CacheFirst dashboard tier; falls through when absent).

Query shapes actually issued (the audited List surface):
- Union legs: `{Type: session}` ∪ `{Label: gc:session}` (list_all.go:47-60;
  doctor's inline copy doctor_session_model.go:152-155).
- `{Label: gc:wait, Limit: 1001, SortCreatedDesc}` (listWaitsByLabel).
- `{Status:"open", Label:"session:<id>", Limit:1001, SortCreatedDesc}`
  (WaitsForSession).
- `{Metadata:{"session_name": X}, Live:true}` (HasOpenSessionNamed — the
  adoption barrier's existence probe, list_all.go:419).
- `ListByLabel(gc:session)` (city-stop sweep), `ListByMetadata(filters)`
  (session-log workdir fallback).

### Load-bearing invariants (pinned by tests)

- **0-Get tick budget**: `TestReconcileSessionBeadsFastPathGetBudget`
  (cmd/gc/session_reconciler_tick_budget_test.go:50, wantGets=0) — one
  healthy reconciler tick issues ZERO store Gets; the snapshot comes from
  `ListAllForReconcileWithFingerprint` and every mutation folds locally
  (ApplyPatch/ApplyPatchInfo/MarkClosed).
- **`SetFingerprint` hashes ALL metadata** (list_all.go:256): ID + Status +
  Assignee + every metadata key/value, computed at the store edge from raw
  rows. THE reason the sqlite row must reconstitute the FULL metadata map
  (hot columns merged with meta JSON) byte-identically — the fingerprint
  cannot be derived from Info.
- **Create echo == Get** (beadstest `CreateEchoMatchesGetOnMetadata`):
  CreateSessionInfo/Manager project the Create RETURN, never re-Get.
- **Union semantics**: type-leg catches label-lost beads, label-leg catches
  empty-type repairable beads; `IsSessionBeadOrRepairable` filter; canonical
  (created_at, id) sort; post-union Limit
  (TestListAllMatchesListAllSessionBeads et al.).
- **Empty-string metadata clears** (`TestMetadataEmptyStringClearContract`)
  — patches write "" verbatim and it reads back as "" (observationally
  cleared). The sqlite meta merge must implement bd's same semantics.
- **`UpdateMetadataInfo`** (store.go:101) exists ONLY because bd/exec
  decomposes SetMetadataBatch per key. On sqlite ApplyPatch is genuinely
  atomic; UpdateMetadataInfo retires at P5 (design), NOT during P4.
- **Wait terminal writes are SetMetadataBatch THEN Close** (wait_store.go:
  193) — two ops today, one tx on sqlite; callers observe outcomes only.
- Wake/create/adoption fences compare `instance_token`, `generation`,
  `pending_create_claim`, `state`, `session_name` (pending_create_lease.go,
  session_wake.go:36-75, adoption_barrier.go:290) — all metadata reads off
  fetched rows plus the ONE store-level Metadata filter (session_name).

### Hot columns (evidence for the design's schema)

Store-level filters: Type, Label (gc:session / gc:wait / session:<id>),
Status, `session_name` (the one Metadata filter). Decision-hot metadata
(~18 keys = fences + the 13 `LifecycleInputFromMetadata` keys,
lifecycle_projection.go:200-217): `state, sleep_reason,
continuity_eligible, configured_named_identity, configured_named_session,
held_until, quarantined_until, pending_create_claim,
pending_create_started_at, last_woke_at, session_key, started_config_hash,
pin_awake, wake_request, instance_token, generation, session_name,
pool_slot`. Everything else (≈75 codec keys + open-ended non-codec keys)
rides the meta JSON column. `session_circuit_*` (9 keys) → the
`session_circuit` sidecar table per design; `CircuitStateFromMetadata`
reads them off the same row the reconcile feed carries (ReconcileSession,
list_all.go:196 — one read, both projections; the sidecar must join back
into that single-read feed).

## Construction roots & routing surface (wiring slice inventory)

The routing seam ALREADY exists: `resolveSessionStore`
(cmd/gc/class_store.go:269 → resolveClassStore:231, identity today).
Three resident roots feed everything:

| root | site | residence |
|---|---|---|
| A `CityRuntime.sessionsBeadStore()` | class_store.go:110 | controller |
| B `controllerState.SessionsBeadStore()` | api_state.go:1449 (api.State:148) | API |
| C `cliSessionStore(store,cfg,cityPath)` | cli_session_store.go:20 (rec=nil) | CLI one-shots |

`sessionFrontDoor(store)` (session_beads.go:2394) is the one raw→front-door
constructor; worker factory threads the store into `session.Manager`
(internal/worker/factory.go:55, api/worker_factory.go:16,
cmd/gc/worker_handle.go:78, api/session_manager.go:11). Guard tests
already police the CLI roots: `TestSessionRelocationRootsRouteThroughSessionClassStore`
(frontdoor_di_guard_test.go:410, 22-file routed list) and
`TestClassResolversNeverCalledWithNilConfig`.

**Bypass gaps the wiring slice must close** (today they read the work
store directly; post-flip they'd read bd residue — the #1939 shape):

1. `cmd/gc/doctor_session_model.go:32` — opens the raw city store and runs
   its own inline union (:127-173). Must route + stay in lockstep (design).
2. `cmd/gc/cmd_mail.go:930/:951/:1203` — MailboxAddress lookups build
   `session.NewStore` from a raw store param (only :1162 routes).
3. `internal/api`: `handler_beads.go:85/:112`, `handler_status.go:513`,
   `session_resolution.go:184/:449/:608` wrap caller-passed raw stores —
   audit each caller passes the routed handle (state.SessionsBeadStore).
4. The messaging seam's session-liveness legs already call
   `resolveSessionStore` (messaging_class_store.go:68/:75, class_store.go:
   290) — they inherit routing for free; verify in tests.
5. `handler_agent_output*.go` threads `CityBeadStore()` into a manager for
   log paths — session-bead writes? Audit at wiring; likely log-only.

## THE structural decision (ratified for slice 2)

**The sessions backend seam is the `beads.Store` interface itself, narrowed
to the audited op/query surface above. `internal/classdb/sessions`
(package `sessionsdb`) implements that surface as a class-scoped,
bead-shaped store — typed hot columns + labels + meta JSON inside — and
slots into `resolveSessionStore` unchanged.**

DELIBERATE DEVIATION from the P2/P3 pattern (new domain-shaped backend
interface + moved codec): recorded, review-worthy, and justified because

- the domain codec is already confined package-side (info_codec / waits /
  circuit_state); there is nothing to move — a `sessionsBackend` interface
  would just re-state the bead ops under new names;
- `session.Manager`, the worker factory, `api.State`, and ~40 API handler
  sites all thread `beads.Store`/`beads.SessionStore` HANDLES; re-plumbing
  them onto a new interface is a giant diff with zero behavioral gain,
  and `resolveSessionStore` (returning `beads.Store`) already exists as
  the single class-dispatch point with guard tests;
- `SetFingerprint`'s all-metadata contract forces the store to speak the
  full open-vocabulary bead row anyway — a typed-only interface cannot.

Wrap-don't-widen still holds op-wise: sessionsdb implements ONLY the ops
and query shapes in the audited surface; every other `beads.Store` method
and unsupported ListQuery shape fails LOUD (`errors.ErrUnsupported`-style,
named error), never silently returns empty. The typed-column benefit the
design wants is preserved INTERNALLY: hot keys are real columns feeding
the design's indexes; reads reconstitute `beads.Bead` rows (metadata =
hot ∪ circuit-sidecar ∪ meta-JSON) so the existing codec, fingerprint,
and conformance oracles run unchanged over both backends.

Not-wrapping hazard honored: sessionsdb is a DISTINCT handle resolved at
the roots, never a wrapper over the work store; optional capabilities
(`CachedList`) simply don't assert (CacheFirst falls through to a local
indexed read — design: "the cache existed because bd forks were slow").

## Slice plan

- **Slice 2 — sessionsdb store** (`feat(classdb)`): migrations v1 over
  `classdb/core`: `sessions` (hot columns per design + `labels` TEXT
  (JSON array) + `meta` TEXT JSON + `assignee`), `session_circuit`
  sidecar, `waits` (design schema + labels/meta for the retry-clone
  passthrough), `id_seq` mint (`gcs-<n>`, legacy ids accepted on import).
  Implements the audited `beads.Store` subset; unsupported ops/shapes fail
  loud. Meta merge = bd SetMetadataBatch semantics (empty-string writes
  verbatim). Conformance: both-backend suites through the PUBLIC
  `session.Store` + `ListAllSessionBeads` + Manager-level create paths,
  covering the union trap rows (label-lost, empty-type), fingerprint
  byte-equality, wait lifecycle (register→ready→dispatch→finalize, retry
  clone, reassign, cancel-collect-nudge-ids), circuit round-trip, echo==Get.
  Crash gate (integration): SIGKILL after acked Create/ApplyPatch; reopen;
  `ListAllForReconcileWithFingerprint` equivalence — the restart-projection
  survival contract. Census bump (three artifacts, currently 534/165).
- **Slice 3 — shadow-write gate** (`feat(sessions)`): sessions-only extra
  stage per design. A teeing layer AT THE RESOLVER (not a wrapper class
  handed to capability-asserting paths — it must forward `CachedList` by
  embedding): primary bd, shadow sessionsdb, config-gated
  (`[beads.classes.sessions] shadow = true` or equivalent), plus
  `gc doctor`-surfaced diff (row-set + fingerprint comparison) for the
  design's 24-48h zero-discrepancy soak (R2.3). Shadow failures log, never
  fail the primary write.
- **Slice 4 — wiring flip + migration** (`feat(sessions)`, may split):
  ratchet flip + config acceptance; `sessionsdb` routing (marker-FIRST
  `.gc/store/sessions.migrated`, ENOENT-only stat check, config cache,
  fail-CLOSED on open error at every root); route roots A/B/C + close
  bypass gaps 1-5; `gc session show [--json]` (NEW — orphan-sweep.sh's
  replacement for `gc bd show <session-id>`); orphan-sweep.sh rewrite +
  embed-guard needles; `gc session prune` extended to closed-session
  purge; store retention sweeper (closed-session TTL default 7d, closed
  waits swept with their sessions; reaper.sh session-SQL leg rewritten
  onto it); `ensureSessionsClassMigrated` on controller boot (FULL import
  of open session beads + open/recent waits, ids preserved,
  reset→import→copy-verify→atomic marker→straggler import-then-sweep,
  abort-before-marker on ANY failure — P2/P3 lessons are LAW); doctor
  lockstep (doctor_session_model routed + migration-state surface);
  upgrade-flow tests (fresh-flip idempotence, bd-truth import,
  no-resurrection retry, straggler, abort-before-marker, closed-TTL drop
  matrix).

## Gotchas specific to sessions

- **Waits' dep reads are WORK-class**: cmd_wait.go's dep-satisfaction
  checks Get WORK beads (`waitDependencyReader`) — that store does NOT
  route; only the wait bead itself moves.
- **Wait↔nudge coupling is already routed** (P2): `nudge_id` point reads
  go through the nudges class store; nothing changes here, but the
  migration must preserve `nudge_id` stamps verbatim or wait finalization
  wedges (the P2 history-import lesson).
- **`gc session prune` CLOSES today, never deletes** (manager.go:1785).
  The new purge TTL is net-new DELETE behavior — keep it store-side
  (sweeper) + explicit prune extension; do not change prune's default
  close semantics.
- **reaper.sh's raw-SQL session delete** (reaper.sh:1101-1134, active only
  when `GC_REAPER_SESSION_BEAD_PATTERN=""`) silently no-ops post-flip —
  the store sweeper replaces it; the `gc bd prune --pattern` default path
  targets work-prefixed ids and is unaffected.
- **orphan-sweep.sh liveness probe** reads `issue_type=="session"`,
  `metadata.state/closed`, id/session_name/alias/agent_name match
  (:229-254) — `gc session show --json` must expose exactly those fields.
- **Manager close-path compensations**: create failure does
  `m.store.Close(b.ID)` best-effort (manager.go:917 etc.) — the sqlite
  Close must be idempotent on just-created rows.
- **`session_name` default fallback**: Manager writes `session_name` via a
  separate SetMetadata AFTER Create when no explicit name (manager.go:912)
  — a crash window today; do NOT "fix" into the create tx at the seam
  slice (behavior-preserving first; note for later).
- **`assignee`**: SetFingerprint hashes Assignee and doctor keys ownership
  off it — the sessions row needs the column even though the design sketch
  omits it.
- **CloseAll(ids, metadata)** (wait cancel bulk) is part of the audited
  surface — one tx on sqlite, N ops on bd; observable outcomes only.
- Seam interfaces need EXPORTED method names/types (handoff LAW) —
  satisfied trivially here: the seam IS `beads.Store`.
