# De-conditionalize Store Capabilities (ga-f7v2ft.164)

Owner directive (Julian, 2026-08-19, cutover gate): *"we should just require
all stores to implement the features we need instead of having conditional
checks everywhere."* Born from ga-f7v2ft.162: `ResolveConditionalWriter`
returned nil on a store that **implements** `ConditionalWriter`, purely for a
missing mode stamp — the capability existed; the conditional lattice hid it.

Citations are at lane HEAD `c18d788114` (rec/ga88-continue). The ga-f7v2ft.162
narrow slice is in flight in this worktree and ships first (§5 step 0); where
it changes a cited site, the row says so. This is a design doc — no code here.

## 1. Inventory

82 capability-conditional sites surveyed across beads/store/reconciler.
In scope for deletion or mandate: **47** (§1.1–§1.4). Triaged out: ~35 (§1.5).

### 1.1 The conditional-writes lattice — 27 sites (DELETE)

The lattice is: config enum → rollout flag → factory stamp → per-resolve
mode×capability matrix → per-call-site trio handling → degrade event.
Six concepts to express one question the contract answers statically.

**Policy/config plumbing (6):**

| Site | What it does |
|---|---|
| `internal/config/config.go:1402`, `:4624-4634` | `beads.conditional_writes` enum off\|auto\|require, default **off** |
| `internal/config/compose.go:1042-1049` | fragment-compose precedence guard for the key |
| `internal/rollout/registry.go:24`, `resolve.go:39` | flag registration + env override |
| `internal/rollout/flag_beads_conditional_writes.go` (39 LOC) | typed accessor |

**The seam (5):** `internal/beads/conditional_writes_resolve.go` (353 LOC):
`condWritesStamp` carrier (`:37-175`), capability prober iface (`:160-170`),
`ResolveConditionalWriter` mode matrix (`:260-315`) — ModeUnset/off →
`(nil,nil,nil)` **silently, no probe** (`:264-271`); auto∧incapable → degrade
diagnostic + once-latched event; require∧incapable → typed refusal. Plus
`conditional_writes_inspect.go` (133 LOC) — which reports a backing without
the inspector as **`Capable=true`** (`:100-131`, vacuous capability on the
status wire) — and the degrade event payload (`internal/events/
rollout_payloads.go`) and emitters (`cmd/gc/store_rollout.go`).

**Factory stamping (2):** `internal/beads/factory.go:194-216`
(`stampedResult`: ModeUnset→Off "can never raise enforcement") and `:220-240`
(`unstampableResult`: refuse under require, warn under auto). The .162 defect:
`internal/storebinding/sqlite/beads_engine.go:61` opens the engine outside the
factory, so nothing stamps — `require` on a split city silently ran unfenced.
The .162 slice closes this hole from the other side, per the owner directive:
rather than plumbing the stamp out to the binding opener, the session class
states a requirement (`beads.RequiredConditionalWriter`, capability only — no
mode, no stamp) and a store that cannot meet it is a named error. The stamping
hole survives for every OTHER class until step 5 retires the lattice.

**Resolve call sites (11), each handling the `(writer, diag, err)` trio:**
`cmd/gc/city_runtime_session_start.go:121,1167` (keyed StatusWriter),
`cmd/gc/session_reconcile.go:1010` (legacy heal), `cmd/gc/session_wake.go:111`,
`cmd/gc/api_state.go:1383` (preflight), `internal/dispatch/control.go:337`,
`internal/dispatch/drain.go:1265,1359`, `internal/molecule/molecule.go:292,579`,
`internal/session/trigger_binding.go:73`, `internal/session/wait_store.go:145`.

**Nil-writer degradation arms in the keyed reconciler (5):**

| Site | Degrades to | How silent | Who notices |
|---|---|---|---|
| `session_deadline_reconcile.go:276-280,422-427` | D-DEADLINE sleep patch via unfenced front door | silent (design-sanctioned) | nobody — legacy-equivalent bytes |
| `session_zombie_reconcile.go:209-213` | D-ZOMBIE terminal mark unfenced | silent | nobody |
| `session_start_reconcile.go:1841-1846,2589-2605` | status heal **skipped** when writer nil | silent skip | heal simply doesn't happen |
| `session_start_reconcile.go:2029-2052` | drain-ack handback to legacy per admission; under require: refuse → **drains wedge** | trace-only (`refusal=unavailable`) | operator reading `gc trace`, or a wedged drain |
| `session_reconcile.go:1010-1030` | legacy heal unfenced | silent | nobody |

**Operator surfaces that enumerate the wrong set (3):**
`cmd/gc/api_state.go:1339-1355` preflight probes work stores only, require-only,
and runs before `storageRoutes` install (`city_runtime.go:560-568`);
`cmd/gc/api_state_conditional_writes.go:44-57` §12.5 status block short-circuits
on off and lists city+rig work stores only — a split city under require showed
`effective=active` while every session-class fence was absent (.162 defect 2;
the slice adds `storage/<binding>` enumeration).

### 1.2 Event-recorder wiring conditionals — 12 sites (MANDATE)

bead.* emission is exclusively a CachingStore write-path feature
(`internal/beads/caching_store_writes.go` notifyChange → recorder, wired in
`wrapWithCachingStore`, `cmd/gc/api_state.go:310-359`). Everything not wrapped
is event-silent:

- `cmd/gc/storage_boot.go:498-510` — `openStorageRoutes` returns the bare
  engine; relocated session-class writes emit **no** bead.* (.162 defect 3,
  NOT closed by the in-flight slice — the slice requires the fence, it does
  not wrap).
  Consequence: `admitSessionStartEvent` (`api_state_session_start.go:180-205`)
  and the bead-event tick Poke (`api_state.go:752-754`) never fire for session
  rows; keyed admission degrades to patrol cadence. Who notices: only S4-style
  MTTR comparison — no error, no event, ever.
- `cmd/gc/class_store.go:285-293` — `resolveClassStore` ignores its `rec` arg;
  the comment at `:151-153` claiming relocated session writes emit bead.* is
  **stale**.
- `cmd/gc/class_store.go:22-27` — `observeSessionWaitCensus` requires a
  CachingStore; split store → `ErrCacheUnavailable` forever.
- `ErrCacheUnavailable` consumers that silently absorb a permanently
  cache-less store: `cmd/gc/build_desired_state.go:1974,2012,2174` (live
  full-hydration, ~2.5s perf cliff), `cmd/gc/city_runtime_wait_dependency_index.go:66,82,125,161`
  (shadow index never converges, never logged).
- Nil-recorder → `events.Discard` silent-drop family: `internal/executionevent/projector.go:59,270,377`,
  `internal/worker/handle_construct.go:56`, plus ~14 `cmd/gc`/`internal/api`
  sites (representative: `session_start_reconcile.go:1826`,
  `api_state.go:3176`). These are constructor-wiring conditionals, not
  operator-visible states.

### 1.3 `daemon.session_reconciler` capability arms — 5 sites (COLLAPSE)

`internal/config/config.go:2489` (off|auto|require, default off; boot-latched).
Mode-switch consumers: `cmd/gc/city_runtime_session_start.go:55-93` (keyed
session-start ownership; DegradeLoud → stderr + legacy), `cmd/gc/city_runtime_nudge.go:31-64`
(same shape for the nudge controller; UseLegacy arm is a bare `return nil`),
`cmd/gc/city_runtime_wait_dependency_index.go:467-489` (shadow sink never
installed; acquire error **swallowed** at `:476`), `session_reconciler_trace_cmd.go:157`
(render). The capability predicates here are snapshot/config coherence — NOT
store capability — except where per-family arms consume the nil StatusWriter
(§1.1). Post-WE this flag becomes a deprecation warning (WE-SIGNOFF §
deletion ledger); this design does not touch its schedule.

### 1.4 bd backend capability negotiation — 3 mechanisms (MOVE TO OPEN)

- `internal/beads/bdstore_conditional.go:61-107` — four-verb `--help` probe for
  `--if-revision`, **lazy on first conditional write**, memoized; probe failure
  = incapable, reported only to the first caller.
- `bdstore_conditional.go:109-119` — runtime unsupported latch (bd downgraded
  in place), process-lifetime.
- `bdstore_ready_projection.go:77-95` — version-compare gate; older bd silently
  loses the SQL ready-projection fast path (perf-only; stays).

Hard constraint the contract must respect: the pinned schema-v59 bd ships
**no `--if-revision` on any verb** (`bdstore_atomic_close.go:16-19`; beads#4682
unlanded/rescoped). A bare `BdStore` genuinely cannot fence the revision trio
today. `NativeDoltStore`, `SQLiteStore`, `MemStore`, `FileStore`, and
`CachingStore` (forwarding) all implement `ConditionalWriter` at HEAD
(`*_conditional.go` assertion blocks); BdStore is selected as the WORK engine
only when foreign executable bd hooks force the native store aside
(`internal/beads/factory.go:143-152` — itself a silent capability downgrade,
WARN→DEBUG at `:351-360`).

### 1.5 Long tail — triaged OUT of this design (~35 sites)

The optional-interface idiom (`, ok := store.(X)`) is legitimate where the
fallback is **semantically identical** (perf fast paths: `beads.Counter`→List
scan, batch→loop). Seven sites where the fallback *changes semantics* are
flagged for a follow-up bead, not this contract: `cmd/gc/bead_policy_store.go:262-265,403-406`
(storage class silently dropped), `internal/sling/sling_core.go:1443-1450`
(different membership semantics), `internal/sourceworkflow/sourceworkflow.go:495`
+ `internal/extmsg/binding_service.go:330` (non-atomic close fallback),
`internal/api/cache_liveness.go:42-60` (non-caching store passes the 503 gate
unconditionally), `internal/api/huma_handlers_beads.go:732-739` (reparent
returns 200 without projection wait), `cmd/gc/pool_session_name.go:449-456`
(non-atomic release recheck). One counter-model already in-tree:
`internal/storebinding/sqlite/beads_provider.go:605-625` refuses to hand out
the front door on an undeclared capability — loud, at startup. That is the
shape this design generalizes.

## 2. The required contract

**Recommendation: capability becomes a per-class contract checked at open and
at controller boot. No mode, no per-resolve probing, no degradation.**

| Store class | Serving engines today | Mandatory interfaces |
|---|---|---|
| **session/infra** (coordclass sessions, graph, mail, orders, nudges — via `resolveClassStore`/`storageRoutes`) | SQLiteStore (split binding), NativeDoltStore or CachingStore-wrapped work store (unsplit) | `ConditionalWriter`, `AtomicConditionalCloser`, revisioned reads (`Revision != 0` on rows it serves), **recorder-wired emission** (CachingStore wrap or equivalent notifyChange) |
| **work** (city/rig/HQ ledgers) | CachingStore over BdStore \| NativeDoltStore \| SQLiteStore | `ConditionalWriter` where the engine is gc-owned (NativeDolt, SQLite); BdStore joins when beads ships the fence flag (§4); recorder-wired CachingStore (already universal on the factory path, `api_state.go:310-359`) |
| **read facades / exec** (DoltliteReadStore reads, exec.Store) | — | out of contract; DoltliteReadStore's write path inherits its embedded BdStore's rules; exec.Store never serves coordination classes |

**Enforcement — both points, deliberately:**

1. **Open-time (factory + binding opener).** `OpenStoreAtForCity` and the
   storage-binding `EngineOpener` outlet (`openStorageRoutes`) refuse to
   return a store that will serve a session/infra class without the contract.
   The check is the contract itself, not a mode stamp: the .162 slice
   deliberately did NOT add a stamping seam here, because a store's capability
   is what it implements. BdStore's four-verb probe runs
   **eagerly at open** (today: lazily on first write, §1.4), so "bd too old"
   is an open error, not a first-write surprise. This covers one-shot CLI
   opens that never reach controller boot.
2. **Controller boot preflight.** Replaces `preflightConditionalWrites`
   (`api_state.go:1339-1355`): enumerate **every** store the controller
   serves — city, rigs, routed engines (`storageRoutes.openedEngines()`, from
   the .162 slice), class stores — assert the class contract *including
   recorder wiring* (wiring is only knowable at boot). Runs unconditionally —
   no mode gate.

**Failure shape (exact):** boot/open error, never a warning:

```
store contract violation: store "storage/sessions" (SQLiteStore) serving
class "sessions" is missing AtomicConditionalCloser: <probe reason>
remediation: <named action, e.g. upgrade bd to a --if-revision build, or
relocate class "sessions" to a [storage] binding>
```

One line per violation, all violations listed, then refuse start. `gc doctor`
gets the same check. The §12.5 status block shrinks to a static
`contract: enforced` line plus the store enumeration (verdicts are no longer
a runtime question).

## 3. Mode semantics after deletion

**`beads.conditional_writes` is retired.** Parse-and-warn (repo precedent:
WE-SIGNOFF — "deprecation warning, not silent removal"), value ignored.
Fenced writes are unconditional wherever the contract mandates the writer;
`ResolveConditionalWriter`'s trio collapses to `ConditionalWriterFor` (already
in `beads.go:309`) whose `!ok` is a boot-impossible invariant, not a branch.
The gate package (`internal/rollout/gate`) stays — other flags use it.

**The meaning change to state plainly:** today's *default is off* — every city
that never set the key (including mc, per the .161 C2 probe: `mode:off,
origin:builtin`) runs plain unconditional writes. After deletion those cities
run fenced writes everywhere the contract applies. The WD.15 campaign
certified the fenced composition (`conditional_writes=require`, WE-SIGNOFF
header) — deletion makes the certified composition the only composition.

**What WD.15 certified under `auto` that changes meaning:** nothing executes
differently, because the campaign store (managed Dolt, native) was capable —
`StatusWriter` was always non-nil, so the §1.1 degradation arms **never ran in
the certified composition**. They are uncertified code; deleting them removes
paths the campaign never exercised. Two consequences to flag anyway:
(1) `persistExactSessionDeadlineSleep`'s front-door arm
(`session_deadline_reconcile.go:422-427`) was *sanctioned by design comment*
(`:276-280`) as legacy-equivalent — after deletion a lost CAS surfaces as a
retryable error instead of an unconditional apply; level-triggered re-detection
absorbs it, but the trace shape changes. (2) `.161 C3` ("require forbidden on
split topology — drain-ack wedges") becomes obsolete: the wedge existed only
because resolution could fail on a capable store.

**`daemon.session_reconciler` `auto` vs `require` afterward:** store
capability exits the definition entirely. What remains of `auto`:
(a) boot-time snapshot/config coherence fallback to legacy
(`city_runtime_session_start.go:78-84`) — dies with legacy at WE;
(b) **row-level** legacy ownership — unrevisioned or foreign-shaped rows
(e.g. drain-ack's "not an exact open revisioned record",
`session_start_reconcile.go:2053-2055`) stay legacy-owned per row. That is the
only durable `auto` semantic: legacy ownership of legacy *rows*, never of
stores. `require` = keyed owns everything or refuses — and is safe on every
topology once the contract holds.

## 4. Upstream alignment

Everything in scope — `internal/beads`, `internal/storebinding`, `cmd/gc` —
is origin-owned (gastownhall/gascity; verified `git ls-tree origin/main`).
This is **not** fork-drift surface; it is an upstreamable series:

- **Upstream (propose as one small series):** the class-contract preflight,
  eager BdStore probe at open, recorder-wiring mandate for routed engines,
  degradation-arm deletion, lattice retirement with deprecation warning.
  Each step is deletion-heavy and idiom-preserving; nothing references T3 or
  DoltLite specifics.
- **Blocked upstream dependency:** BdStore's revision trio needs beads to ship
  the fence flag (beads#4682 rescoped to metadata-CAS only; #4697 claim_fence).
  Until then the contract's work-store row for bare BdStore is "refuse when
  asked to serve a session/infra class; work-class fences unavailable, stated
  at boot" — no silent anything.
- **Fork/enterprise side (stays out of upstream):** the gc-enterprise
  five-store layout (`sessions.db` with `{sessions,waits,id_seq}` — enterprise
  delta per .161) must conform before it upgrades past step 3 (§5): whatever
  opens those databases must serve session/infra classes from contract-complete
  stores, which is a property of the store rather than of a seam the enterprise
  opener has to call. The deploy-line rebase ask in .161 is unchanged.

## 5. Migration order (gated on the cutover soak)

The soak (.161) runs on **current semantics**. Steps 1-2 are additive and
soak-safe; steps 3-5 land only after the owner closes the soak.

| Step | Change | Test story | Blast radius |
|---|---|---|---|
| 0 (landed, `3e9141da82`) | .162 slice: the session class REQUIRES conditional writes (`beads.RequiredConditionalWriter`, capability only) instead of resolving the mode; unconditional boot preflight + `storage/<binding>` and `sessions (required)` rows in §12.5 | `storage_boot_conditional_writes_test.go`, `sqlite_store_conditional_capability_internal_test.go` | split cities fence on every mode incl. off; one of step 5's 11 call sites (`city_runtime_session_start.go:121`) already converted |
| 1 (landed) | Contract preflight in **WARN** mode: `preflightStoreContract` enumerates every store × class the controller serves — city, rigs, routed engines — and warns one line per store and missing capability, naming store/kind/classes/capability/remediation. Runs from `setControllerState`, beside the .162 session-class ERROR | `store_contract_preflight_test.go`: non-conforming fixture, conforming split-city control, routed-engine enumeration, WARN-not-refuse, boot call site | zero behavior change; log noise only |
| 2 (landed) | Recorder-wire routed engines at the seam that opens them (`newCityRuntime` → `storageRoutes.withControllerEmission(p.Rec)`) + fix the stale `class_store.go` / `api_state.go` accessor comments | `class_store_emit_controller_test.go`: split-city write → emission → recorder → bead-event watcher → `admitSessionStartEvent`, with the single-store city as the control topology | split cities: bead.* volume appears; keyed admission leaves patrol cadence (S4 tax removed) |
| 3 | Eager BdStore probe at open; preflight WARN→**ERROR** (boot refusal) | open-time refusal test per engine; upgrade-path test for old bd | any deployment with unfenceable session-class store fails at boot with named remediation — release note; mc/enterprise must conform first (§4) |
| 4 | Delete degradation arms: deadline/zombie front-door arms, heal-skip arms, drain-ack handback; `StatusWriter` becomes non-optional in `exactSessionStartParams` | rerun D-family fixtures (`cmd_perf_parity_join_corpus_test.go`); delete arm tests | keyed paths only; certified composition unchanged (§3) |
| 5 | Retire the lattice: flag→deprecation warning, delete seam modes/stamp/prober/degrade event/§12.5 verdicts; `ResolveConditionalWriter`→`ConditionalWriterFor` at all 11 call sites | deprecation-warning test; grep-guard that no `conditional_writes` mode survives outside the warning | default-off cities flip to fenced (§3) — the owner-stated intent |

Step 5 is where "auto collapses toward require" becomes literal: the words
disappear from config for beads writes. `daemon.session_reconciler` is
untouched here (WE owns its retirement).

### What steps 1-2 did differently from this table's first draft

Two corrections, recorded because the difference matters to whoever reads
§1.2 next:

1. **Emission arrived as the equivalent notifyChange, not a CachingStore wrap**
   (§2's own "CachingStore wrap **or equivalent notifyChange**"). The routed
   engine is wrapped in the `emittingClassStore` the one-shot CLI already used
   for the identical defect — capability-complete against both engines by a
   pinned test, canonical payloads, no read-path change — parameterized on an
   emit target so the two processes share one implementation. A read cache in
   front of the session/graph store is not additive: it changes read freshness
   and adds a per-cycle reconcile scan, which step 1-2's soak-safe mandate
   forbids, and `CachingStore` drops engine methods the split path asserts on
   (`SupportsEphemeralGraphApply`, `ApplyGraphPlanWithStorage`, the sequence
   floor, ...).
2. **`observeSessionWaitCensus` is therefore still `ErrCacheUnavailable` on a
   split city**, and so are the other §1.2 `ErrCacheUnavailable` consumers
   (`build_desired_state.go`, `city_runtime_wait_dependency_index.go`). That is
   the READ-cache half of §1.2 and it is unclosed: it wants either a
   capability-complete cache over the binding or a cache-free census, and it is
   a perf/fast-path question rather than a correctness or admission-latency one.
   Filed separately; it is not a prerequisite for steps 3-5.

One hazard found and closed while wiring step 2, worth naming because §1.1
predicted its shape: wrapping the store the session class resolves to put a
layer between the .162 requirement and the engine that answers it, and the
wrapper forwards the fenced trio — so it answered "capable" for a backing that
could not fence, exactly the vacuous capability §1.1 flags on the status wire.
`beads.ConditionalWritesCapabilityTargeter` points every capability question
(boot requirement, status inspection) at the engine while the WRITER stays the
wrapper; redirecting resolution instead would have handed callers the bare
engine and made every fenced write, terminal close included, silent again.

## 6. Deletion dividend

Measured against HEAD (`wc -l` / diff-scope estimates):

| Deleted | Prod LOC | Test LOC |
|---|---|---|
| `conditional_writes_resolve.go` mode matrix + stamp + prober (keep targeter ~60) | ~290 | 537 (`resolve_internal_test`) |
| `conditional_writes_inspect.go` verdict machinery | ~100 | 147 + 61 (`city_status_conditional_writes_test`) |
| flag + registry + env plumbing | ~55 | ~80 |
| factory mode branches (`stampedResult`/`unstampableResult` mode cells) | ~80 | ~60 |
| degrade event + emitters (`rollout_payloads.go`, `store_rollout.go`, lazy emitter) | ~100 | ~80 |
| §12.5 wire block → static line | ~110 | ~100 |
| per-store stamp embeds + carrier glue (4 stores + CachingStore forwarding) | ~120 | 80 (`sqlite capability test`) |
| reconciler degradation arms (5 sites) | ~70 | ~250 (arm tests) |
| trio-handling at 11 call sites → direct use | ~60 | ~100 |
| **Total deleted** | **~985** | **~1,495** |
| New: contract preflight + open refusal + conformance additions | +250 | +300 |
| **Net** | **~ −735 prod** | **~ −1,200 test** |

Concepts: **6 deleted** (mode lattice, stamp carrier, resolve-time prober,
degrade event + latch, unstampable-open outcomes, per-resolve diagnostics),
**1 added** (class contract at boot). The primitive test
(`engdocs/contributors/primitive-test.md`) rules for this: `auto`'s
degrade-or-not cell is an `if incapable then fallback` judgment call living in
Go — the contract moves the decision to configuration-time fact, and Go goes
back to transport.

## 7. Decisions (crisp)

1. **Retire `beads.conditional_writes`** — deprecation warning, value ignored;
   fenced writes are contract behavior, not policy (§3).
2. **Class contract enforced at open AND boot, unconditionally** — refusal
   names store, class, capability, remediation; `gc doctor` mirrors it (§2).
3. **Session/infra classes may only be served by contract-complete stores**;
   bare BdStore refuses that role until beads ships the fence flag (§2, §4).
4. **Routed engines get recorder-wired** — event silence on split cities is a
   contract violation, not a wiring accident (§1.2, step 2).
5. **Degradation arms are deleted, not preserved behind require** — they are
   uncertified code the campaign never ran (§3, step 4).
6. **`auto` (session_reconciler) keeps exactly one semantic**: row-level
   legacy ownership. Store-level degradation is inexpressible (§3).
7. **Long-tail optional interfaces stay** where fallbacks are semantically
   identical; the seven semantic-drift sites get a follow-up bead, not this
   contract (§1.5).
