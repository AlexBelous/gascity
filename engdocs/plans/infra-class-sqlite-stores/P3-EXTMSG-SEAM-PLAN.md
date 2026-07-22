# P3 messaging — extmsg seam plan

Evidence-grade inventory of the extmsg persistence machinery and the
extraction plan for the typed-table slices. Derived from a full read of
`internal/extmsg/{types,labels,helpers,services,errors,binding_service,
delivery_service,group_service,transcript_service,inbound,outbound,
binding_reaper,live_session,events,time,adapter_registry,system_reminder}.go`
plus a repo-wide consumer sweep (2026-07-22). Companion to
`P3-MESSAGING-SEAM-PLAN.md` (the mail half); the atomic-flip decision there
is LAW: these tables land in the SAME `messaging.db`, dark, before the one
wiring+migration pair relocates ClassMessaging as a whole.

## Today's architecture (what the seam must preserve)

extmsg is **pure label-indexed KV over beads**: seven record kinds, each a
`Type: "task"` bead (NOT ephemeral — unlike mail wisps, these are durable
beads; plain `List`, no wisp tiers) whose fields live in `Metadata` and
whose lookups ride sha256 locator labels (`labels.go`, `hashJoin` over the
normalized conversation identity tuple). The codec is already confined:
`decode*Bead` / `encodeMetadataFields` pairs per record kind, all in the
service files. User metadata passes through under a `meta.` key prefix
(`encodeMetadataFields` / `decodePrefixedMetadata`).

### The seven record kinds (bd encodings, verbatim)

Conversation identity = the normalized 6-tuple `(scope_id, provider,
account_id, conversation_id, parent_conversation_id, kind)`; every
conversation-scoped record stores it as six metadata keys and hashes it
into its locator labels. `normalizeConversationRef` lowercases
provider/kind and trims all parts — applied at every service entry, so
backends always see normalized refs.

| record | base labels | locator labels | metadata fields (beyond conv 6-tuple + schema_version) | notes |
|---|---|---|---|---|
| binding | `gc:extmsg-binding` (added twice — literal + `labelBindingBase` are the same string) | conv; session-id; session-NAME (stable, survives respawn); agent | session_id, session_name, agent_name, binding_generation, bound_at, expires_at, last_touched_at, created_by_kind, created_by_id | status: bead open=active, closed=ended. Exactly one of session_id/agent_name set. History (incl. ended) is load-bearing: `nextBindingGeneration` = MAX over ALL rows +1 |
| delivery context | `gc:extmsg-delivery` (×2) | route (conv+session hash); session-id | session_id, binding_generation, last_published_at, last_message_id, source_session_id | eager-closed when stale/duplicate/cleared |
| group | `gc:extmsg-group` (×2) | root-conv | mode, default_handle, last_addressed_handle, fanout_enabled, fanout_allow_untargeted, fanout_max_peer_triggered_publishes, fanout_max_total_peer_deliveries | one open group per root conv (decode-time invariant) |
| participant | `gc:extmsg-participant` + `gc:extmsg-group-participant` (the ONE kind whose two base labels differ — `hasLabel` filters on the former, reaper lists by the latter) | group-id; session-id; session-NAME | group_id, handle, session_id, session_name, public, previous_session_id_pending_cleanup (comma-joined sorted set) | handle normalized lowercase; one open participant per (group, handle) |
| transcript entry | `gc:extmsg-transcript` (×2) | conv; bucket (conv + seq/64); provider-msg (conv + provider_message_id) | sequence, kind, provenance, provider_message_id, explicit_target, reply_to_message_id, source_session_id, created_at, actor_json, attachments_json | **Text lives in bead Description**, not metadata. Buckets of 64 exist only to bound label scans — they die in SQL |
| membership | `gc:extmsg-membership` (×2) | conv; exact (conv+session); session-id | session_id, joined_at, joined_sequence, last_read_sequence, membership_backfill_policy, manual_backfill_policy, membership_owner_kinds (comma-joined sorted), closed_at (stamped at close) | one OPEN membership per (conv, session); owner algebra (manual/binding/group refcount) is service logic |
| transcript state | `gc:extmsg-transcript-state` (×2) | state (conv) | next_sequence, earliest_available_sequence, hydration_status, oldest_hydrated_message_id, max_retained_entries | one per conv; next_sequence is the allocator; max_retained_entries written as 0, never enforced |

### Service layering — what stays ABOVE the seam

All of this is judgment/algebra over decoded records and MUST NOT move
into backends:

- validation + normalization (`validateConversationRef`, handle/caller
  normalization), authorization (`authorizeMutation`,
  `requireControllerCaller`);
- **binding selection + expiry cascade**: `selectActiveBinding` (skip
  ended, expire past-`expires_at` records via callback: remove
  binding-owned membership → clear delivery → close binding, with
  membership re-ensure compensation on close failure);
- routing precedence (binding → group → default route, `inbound.go`) and
  outbound authorization (binding ownership / agent resolution / group
  participant fallback, `outbound.go`);
- membership **owner algebra** (`addMembershipOwner` /
  `removeMembershipOwner` / `effectiveMembershipBackfillPolicy` /
  `manualBackfillMetadataValue`) and the empty-owners legacy substitution
  in `removeMembershipLocked`;
- hydration state machine gates (pending-blocks-live-append,
  live-traffic-blocks-begin, pending-required-for-complete/failed);
- session-liveness overlay + stable-name capture (`live_session.go`:
  `overlayLiveSessionID`, `sessionNameForSelector`) — reads the
  SESSION-class store (`sessionStore`), never the record store; the
  two-store split is already wired (`NewServicesWithSessionStore`,
  pinned by `two_store_test.go`);
- participant pending-cleanup bookkeeping and
  `migrateParticipantGroupMembership` sequencing (retry-idempotent
  handover, retired-label retention until membership migration commits);
- the reapers' scan/decide loops (`binding_reaper.go`) and the
  Reassign/Close repair flows;
- event emission (`inbound.go`/`outbound.go` + API handlers) — all above
  the persistence edge; **no new event types needed for this slice**;
- the conversation lock pool (see concurrency stance below).

### The in-process lock pool (and why it can stay)

`sharedBindingLockPools` is a process-global `sync.Map` keyed by **store
pointer identity** (`bindingLockPoolKey`); every conversation-scoped
mutation runs under `withBindingLock(conversationLockKey(ref))`, plus
per-label locks for delivery routes and per-group mutation locks. This is
in-process-only serialization — meaningless across processes.

**Writer topology (verified by repo-wide import sweep):** every extmsg
record mutation runs in the controller process:

- controller services: `cmd/gc/api_state.go:214` (boot) and `:733`
  (reload) — `NewServicesWithSessionStore(resolveMailMessagesStore(…),
  resolveSessionStore(…))`;
- reconciler-tick reapers: `cmd/gc/city_runtime.go:1248/:1255` →
  `extmsg.ReapStaleBindings/ReapStaleParticipants` (already two-handle:
  `mailBeadStore()` records, `sessionsBeadStore()` liveness — the design's
  P0 "reaper two-handle fix" is DONE);
- session-repair cascades: `cmd/gc/session_beads.go` +
  `cmd/gc/session_lifecycle_parallel.go` +
  `internal/api/session_resolution.go` (controller/API process);
- `gc extmsg` CLI (`cmd_extmsg.go`) is a **pure API client**; the other
  cmd/gc imports (`cmd_mail.go`, `cmd_nudge.go`, `wisp_step_inject.go`)
  use only `SanitizeForSystemReminder`;
- `internal/api/dashport_support.go:199` seeds a mem-store preview state,
  never a real city db.

Therefore: **the lock pool remains the serialization mechanism, above the
seam.** The sqlite backend additionally (a) runs every composite write in
one immediate transaction and (b) carries the invariants as UNIQUE
constraints, so a cross-process double-writer (mid-upgrade old binary,
future regression) produces a rejected write instead of today's failure
mode — a decode-time `ErrInvariantViolation` ("multiple active bindings")
that poisons every later resolve of that conversation. Bonus fix: pointer
keying fragments pools today when the same city store reaches services
through different wrapper values; the process-wide cached class-store
handle (orders/nudges pattern) gives one pool per db path.

## The message-store op inventory (every `s.store` touch)

Bindings (`binding_service.go`):

| flow | store ops (bd today) |
|---|---|
| Bind → create | under conv lock: List(conv, IncludeClosed) history → selectActive(expiry cascade) → **store.Tx**: [Close displaced] + Create binding + membership ensure writes via `membershipWriter` (+ Create state on first touch) — the #3735 one-commit coalesce |
| Bind → rebind (same target) | **store.Tx**: Update (backfill session-name label+meta) + SetMetadataBatch (expires/touched/meta) + membership ensure |
| Bind → handoff (Replace) | delivery.Clear (List route + Close) → SetMetadata touched → remove displaced membership → atomic-store branch: the create-Tx carries the displaced Close; non-atomic branch: Close then create (documented partial-failure states) |
| ResolveByConversation | List(conv, IncludeClosed) + expiry cascade; then session-store overlay |
| ListBySession | List(session-id label) → per-conv resolveActive |
| Touch | Get + debounced SetMetadata(last_touched_at) |
| Unbind | seeds: List(conv history) OR List(session-id/agent label); per seed under conv lock: history → selectActive → SetMetadata + remove membership + Close (re-ensure on close failure) → delivery.Clear |
| ReassignSessionBindings (pkg fn) | List(old-session-id label); per binding under conv lock: Get, dup-check List(conv, IncludeClosed), membership ensure/remove, Update(labels session-id swap + metadata), delivery.Clear, Close dup |
| CloseSessionBindings (pkg fn) | Unbind(sessionID) + List(participant-session label)→RemoveParticipant each + ListConversationsBySession→RemoveMembership each |
| reaper `ReapStaleBindings` | List(`gc:extmsg-binding` label, ALL) + per-record repair via the above |

Delivery (`delivery_service.go`): Record = conv lock → revalidate active
binding (session_id + generation match, else `ErrBindingMismatch`) → route
label lock → List(route) → Update title + SetMetadataBatch existing, else
Create. Resolve = same gating → keep the matching-generation context,
Close stale/duplicate ones. ClearForConversation = List(route) → Close
each.

Groups (`group_service.go`): EnsureGroup = root lock → List(root) →
Update+SetMetadataBatch | Create. UpsertParticipant = Get group →
participants-mutation lock → List(group label, IncludeClosed) → match
handle → Update(labels session swap)+SetMetadataBatch | Create →
`migrateParticipantGroupMembership` (transcript EnsureMembership +
RemoveMembership per retired id + SetMetadata pending-cleanup writeback).
RemoveParticipant = Close each matching + refcount actives +
RemoveMembership per orphaned session. ResolveInbound/ResolveOutbound =
findGroupByRoot + listParticipants (+ session-store overlay) — read-only.
UpdateCursor = SetMetadata(last_addressed_handle). ReapStaleParticipants =
List(`gc:extmsg-group-participant`, ALL) + ReassignSessionParticipants
(Update swap-with-retained-retired-label → membership migration → Update
drop-retired-label).

Transcript (`transcript_service.go`): Append = conv lock → ensureState
(List(state) | Create) → hydration gate → provider-msg dedupe
(List(provider-msg label), return existing) → **Create entry THEN
SetMetadataBatch state (next_sequence++, earliest floor)** — two
non-atomic writes today; a crash between them re-issues the same sequence
on the next append. List/ListBackfill = state + bucket-label scans with
in-memory sort/limit (asc/desc). Ensure/Update/RemoveMembership =
find-exact-label + owner algebra + Create/SetMetadataBatch/(SetMetadata
closed_at + Close). Ack = SetMetadata(last_read_sequence), gated ≤ head.
Hydration Begin/Complete/Failed = SetMetadataBatch on state. State = read.

`membershipWriter` (Create + SetMetadataBatch, satisfied by both
`beads.Store` and `beads.Tx`) is how membership/state writes ride the
bind transaction — the seam must preserve this coalescing.

## Invariants → constraints (the design's mandate, grounded)

Today every invariant is detected at DECODE time (read-side error after
corruption already happened). In `messaging.db` they become schema:

| today's decode-time check | constraint |
|---|---|
| `selectActiveBinding`: "multiple active bindings" | partial `UNIQUE(conv-cols) WHERE status='active'` on `bindings` |
| `findGroupByRoot`: "multiple groups" | partial `UNIQUE(conv-cols) WHERE status='open'` on `groups` |
| `listParticipants`: "duplicate participants for handle" | partial `UNIQUE(group_id, handle) WHERE status='open'` on `participants` |
| `findActiveMembershipLocked` / `ListMemberships`: "duplicate memberships" | partial `UNIQUE(conv-cols, session_id) WHERE status='open'` on `memberships` |
| `findStateLocked`: "multiple transcript states" | `UNIQUE(conv-cols)` on `transcript_state` (design) |
| `findTranscriptByProviderMessageLocked`: "duplicate transcript provider message" | partial `UNIQUE(conv-cols, provider_message_id) WHERE provider_message_id != ''` on `transcript_entries` (empty provider ids stay unconstrained — outbound entries may lack one and dedupe is skipped for them today) |
| `next_sequence` lock-held read-then-bump | `UPDATE transcript_state SET next_sequence = next_sequence + 1 … RETURNING`, in the SAME tx as the entry INSERT (also closes today's crash window) |
| delivery duplicate-context cleanup in Resolve | `UNIQUE(conv-cols, session_id)` on `delivery_contexts` + UPSERT in Record; Resolve's dup-Close branch becomes bd-backend-only |

Constraint-violation mapping: a UNIQUE hit on the active-binding index
surfaces as `ErrBindingConflict` (the service already owns that error for
the racing-bind case); other violations map to `ErrInvariantViolation`.

**binding_generation monotonicity** must survive retention: delivery
gating compares generations (`ErrBindingMismatch`), and
`nextBindingGeneration` is MAX over ALL binding history. Sweeping ended
bindings could lower the MAX and mint a colliding generation that a stale
delivery context falsely matches. Rule (orders precedent — "the newest
run carries the max seq"): the ended-bindings retention sweep always
spares, per conversation, the row with the max generation.

## Schema (extends messagingdb migrations, version-gated → Version 2)

Per the design sketch, plus deviations ratified here (precedent: nudges'
`bead_id` column, mail's `*_addr/*_display` splits):

- Seven tables: `bindings`, `delivery_contexts`, `groups`,
  `participants`, `memberships`, `transcript_state`,
  `transcript_entries`, sharing the conv-identity columns; composite
  indexes replace the sha256 locator labels (`idx(conv-cols)` per table;
  `idx(conv-cols, sequence)` on entries kills the 64-bucket scheme;
  `idx(session_id)` on bindings/participants/memberships/delivery for the
  by-session scans; `idx(agent_name)` partial on bindings for Unbind's
  agent seeds; `idx(group_id)` on participants).
- **`meta TEXT NOT NULL DEFAULT '{}'`** on every table — the `meta.`
  passthrough map as JSON (deviation from the sketch; the records expose
  `Metadata` and the API writes it, so it cannot be dropped).
- `schema_version INTEGER` per row (records carry it; import preserves).
- Per-kind columns mirror the metadata tables above 1:1; transcript
  `text` is its own column (bd: bead Description); `actor_json` /
  `attachments_json` stay JSON TEXT (edge serialization stays at the
  edge); owner kinds / pending-cleanup stay the canonical sorted
  comma-joined TEXT encodings (service-side codec already normalizes —
  do NOT invent a join table).
- Status enums: bindings `active|ended` (+ `ended_at`), groups/
  participants/memberships `open|closed` (+ `closed_at`); memberships
  already stamp `closed_at` today.
- Id mint: `gcx-<n>`? NO — the class mint is `gcm-<n>` (one prefix per
  CLASS; the messages table already mints `gcm`). All seven tables share
  the existing same-tx `id_seq` counter; legacy bead ids remain valid row
  keys (import preserves them verbatim).
- Timestamps: INTEGER UnixNano via the existing `nanos()` (zero = 0),
  matching the messages table.

## The seam (slice 1: refactor(extmsg), byte-identical)

Unexported `fabricBackend` interface inside extmsg (orders/nudges/
beadmail pattern: unexported type, exported method names, another package
satisfies structurally). The bd backend (`backend_bead.go`) = the MOVED
label/metadata codec bodies, verbatim, including the doubled base labels
and the participant label quirk. Services keep `beads.Store` only for the
bd construction paths; `sessionStore` stays a raw `beads.Store` (liveness
reads are session-class, not ours).

Backend traffic uses the EXISTING exported record types
(`SessionBindingRecord`, `DeliveryContextRecord`,
`ConversationGroupRecord`, `ConversationGroupParticipant`,
`ConversationTranscriptRecord`, `ConversationMembershipRecord`,
`ConversationTranscriptStateRecord`) — they are already pure
persistence-edge shapes; decode stays total.

Op families (granularity is slice-1 latitude; these four properties are
NOT negotiable):

1. **Codec confined**: no label string or metadata key crosses the seam.
2. **The composite writes stay single-commit**, expressed as one backend
   op each, with membership/state sub-writes threaded the way
   `membershipWriter` does today (service precomputes the owner-algebra
   outcome under the conversation lock; backend applies atomically):
   - `CreateBinding(rec, displaceID, stateCreate, membershipChange)` —
     bind/handoff (#3735 coalesce; sqlite: one tx, and the non-atomic
     handoff branch in `handoffActiveBindingLocked` becomes
     bd-backend-only via the existing `StoreSupportsAtomicTx` seam);
   - `RefreshBinding(id, nameBackfill, metaKVs, expires, touched,
     membershipChange)` — rebind;
   - `AppendTranscript(entry)` — INSERT + `next_sequence`
     UPDATE…RETURNING in one tx (fixes the bd crash window; bd impl keeps
     today's two writes verbatim);
   - `UpsertDeliveryContext(rec)` — bd: find-update-else-create; sqlite:
     UPSERT.
3. **Reads are typed**: `BindingHistory(ref)`, `ActiveBindings()`,
   `BindingsBySession/Agent`, `GetBinding`, delivery
   `ContextsForRoute(ref, sessionID)`, `GroupByRoot`, `GroupByID`,
   `ParticipantsByGroup(incl. closed)` / `ParticipantsBySession`,
   `MembershipExact` / `MembershipsByConversation` /
   `MembershipsBySession`, `TranscriptState`,
   `TranscriptRange(ref, afterSeq, limit, order)` (subsumes bucket
   walking — bd impl keeps the bucket scans internally),
   `TranscriptByProviderMessage`.
4. **Single-record writes stay narrow**: `TouchBinding`, `CloseBinding`,
   `CloseDeliveryContext`, `UpdateGroupCursor`, `CloseParticipant`,
   `UpdateParticipant(labels-swap semantics as typed session/name
   fields)`, `SetParticipantPendingCleanup`, membership
   create/update-owners/close, `AckMembership`, hydration-state updates.

Construction: `NewServicesWithBackend(backend, sessionStore)` added next
to the existing constructors (callable cross-package with a structural
implementation — the `orders.NewStoreWithTracking` precedent). The
package-level funcs (`ReassignSessionBindings`,
`ReassignSessionParticipants`, `CloseSessionBindings`, `ReapStale*`,
`NewGroupService`, `NewServices`) keep their `beads.Store` signatures as
the bd surface; routed twins come at the wiring slice, not now. Lock-pool
keying gains a backend-identity variant (`sharedBindingLockPoolForKey`)
so a backend-constructed service shares one pool per db handle.

Proof: the full `extmsg_test.go` (3229 lines) + `two_store_test.go` +
reaper/rebind/binding-survival suites run untouched — the byte-compat
gate. Package test seams stay package-level vars (`timeNow`,
`resolveLiveSessionID`, `newReassignmentTranscript`).

## Slice 2: `internal/classdb/messaging` extmsg tables

- Migration Version 2 in `messagingdb.migrations()` (core is
  version-gated; existing dark messages stores upgrade in place).
- `*messagingdb.Store` implements `fabricBackend` structurally (same
  Store as the mail backend — ONE handle per db, ONE lock-pool key, and
  the wiring slice hands the same handle to `beadmail.NewWithBackend`
  and `extmsg.NewServicesWithBackend`).
- `ImportX` primitives per record kind (verbatim ids/clocks/generations,
  INSERT OR IGNORE — interrupted-migration resume, the orders/nudges
  pattern), plus an import path for legacy `closed` records that the
  migration chooses to carry (ended bindings for generation floors; see
  monotonicity rule — importing the max-generation ended binding per
  conversation is REQUIRED when no active one exists).
- Conformance (`conformance_test.go`, nudges' `eachBackend` pattern):
  both backends through the PUBLIC `extmsg.Services` surface —
  bd leg = `NewServicesWithSessionStore(mem, mem)`, sqlite leg =
  `NewServicesWithBackend(store, mem-session-store)`. Cases: bind /
  rebind-backfill / handoff-displace / expiry-cascade / unbind cascade;
  delivery record-resolve-mismatch-clear; group ensure / participant
  upsert-migrate / remove-refcount; transcript append-sequence-dedupe /
  list asc+desc+limit / backfill policies / ack gates; hydration gates;
  ReassignSessionBindings / ReassignSessionParticipants (incl. the
  pending-cleanup retry shape) / CloseSessionBindings zombie sweep;
  two-store liveness (msg records never in session store).
- Crash gate (integration tag): acked Bind and acked Append survive
  SIGKILL (core re-exec pattern); census bump THREE artifacts with new
  files `git add`ed first.
- **Retention, implemented here, DORMANT until the flip** (extmsg never
  deletes anything today — the leak is the design's motivation):
  - transcript pruning: `max_retained_entries` becomes real — per-conv
    knob on the state row, 0 = store default (propose 10,000; design
    open question 3 says cheap to change); prune advances
    `earliest_available_sequence` (List/ListBackfill already clamp to
    it, so readers are ready);
  - ended bindings / closed participants+memberships / closed delivery
    contexts: TTL-swept (propose 30 d), sparing the max-generation ended
    binding per conversation (monotonicity rule);
  - exposed as `SweepExtmsgRetention` folded into the store's
    `SweepUnreadBefore`-adjacent retention surface so the ONE controller
    sweeper (wiring slice) covers mail + extmsg together.

## Wiring-slice inventory additions (recorded now, executed later)

Beyond the mail-half list in `P3-MESSAGING-SEAM-PLAN.md`:

- `cmd/gc/api_state.go:214` and `:733` — the two service constructions
  route: marker+config → `NewServicesWithBackend(sharedHandle, routed
  session store)`, else today's call; fail-closed erroring backend on
  open failure (nudges' `NewUnavailableQueue` analogue).
- **The single-store repair call sites pass the WRONG class store
  post-flip** (today they work only because all classes share one
  store): `cmd/gc/session_beads.go:1275/:1278` (via
  `reassignStateAssignedToRetiredSessionBead`, callers :574/:675) and
  `:1302` (via `cancelStateAssignedToRetiredSessionBead`, callers :760,
  :2413 — which passes `sessFront.Store().Store`, a SESSION-typed
  store! — :2999), `cmd/gc/session_lifecycle_parallel.go:2519/:2523`,
  `internal/api/session_resolution.go:186/:189`. All three pkg funcs
  mutate ONLY messaging-class records (no liveness reads inside —
  verified), so post-flip on a migrated city they would List an empty
  label set and silently no-op: zombie memberships, never-reassigned
  bindings, the exact #1939 regression. The wiring slice MUST give these
  call sites routed messaging handles (routed twins of the three pkg
  funcs, or route through a shared resolver). Since `internal/api` is a
  second package producing record traffic, the nudges precedent
  (resolver in the classdb package) likely applies — decide there.
- `cmd/gc/extmsg_binding_reaper.go` already takes the routed
  `mailBeadStore()`; it needs the backend-routed twin of `ReapStale*`
  when the marker is set (bd-store leg keeps the current signature).
- `internal/api/dashport_support.go:199` stays bd/mem (seeded preview).
- `gc extmsg` CLI: no change (API client).
- No reaper.sh work for extmsg (its filters are mail/session/nudge
  scoped; extmsg beads were never reaped — retention is net-new in the
  store).
- Residue sweep (migration slice): bd-side `gc:extmsg-*` beads are
  durable task beads (not wisps) — plain-List deletion by base labels,
  same closed ∪ class-owned ∪ open-past-grace shape as nudges.

## Gotchas specific to extmsg

- `Append`'s state bump is non-atomic with the entry Create in bd —
  preserve verbatim in the bd backend; the sqlite backend's single-tx
  form is the ratified-better shape (nudges dead-letter precedent).
  Do NOT "fix" the bd leg.
- `handoffActiveBindingLocked`'s two branches key off
  `beads.StoreSupportsAtomicTx(s.store)`; after the seam this becomes a
  backend capability — sqlite always atomic, bd keeps both branches and
  their documented partial-failure recovery states.
- `EnsureMembership` on an EXISTING membership updated through a
  transaction reads pre-commit state on re-fetch
  (`ensureMembershipLockedWriter` doc) — callers passing a tx discard
  the return; keep that contract in the backend split.
- `Thread`-style dual resolution does not exist here, but
  `UpsertParticipant` lists `IncludeClosed` and skips closed rows —
  the sqlite reads filter `status='open'` EXCEPT `BindingHistory`
  (generations need ended rows) and Unbind's conv-seed path (filters
  active in service).
- `decodeParticipantBead` never errors (`//nolint:unparam`) — keep the
  error in the seam signature anyway; sqlite scan can fail.
- `recordLabels`' add/remove reconciliation is bd-codec — participant
  session swaps cross the seam as typed old/new session id+name fields.
- `conversationLockKey` == the binding conv label (hash) — the lock pool
  keys on it; fine for both backends (pure function of the ref).
- `ReapStaleBindings` lists label `gc:extmsg-binding` WITHOUT
  IncludeClosed (open only) but `ReapStaleParticipants` filters
  status inline — mirror exactly in backend reads
  (`ActiveBindings()` / `OpenParticipants()`).
- classdb/messaging already imports beadmail; adding extmsg imports is
  the same cycle-free direction (messaging → extmsg → {beads, session,
  events}). Verify no reverse import — extmsg must NEVER import
  classdb/messaging.
- `coordclass` classifies by the `gc:extmsg-` label prefix
  (`internal/coordclass/classify.go` — `labelExtmsgPrefix`), covering
  all seven kinds including the bare `gc:extmsg-participant` label; the
  by-id route covers minted `gcm-` ids. No classifier change needed.
- New conformance/crash test files: integration tag where subprocesses
  spawn, three census artifacts, `go run scripts/add-testenv-import.go`
  for any new package, no `time.Sleep`.
