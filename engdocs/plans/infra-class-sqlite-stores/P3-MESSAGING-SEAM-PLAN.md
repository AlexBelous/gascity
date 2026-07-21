# P3 messaging — seam plan (mail half)

Evidence-grade inventory of the mail persistence machinery and the exact
extraction plan for the P3 backend seam. Derived from a full read of
`internal/mail/{mail.go, resolve.go, beadmail/beadmail.go,
mailtest/conformance.go}` plus a consumer sweep over cmd/gc + internal/api
(2026-07-21).

## The one structural decision (read this first)

**ClassMessaging = mail messages AND every `gc:extmsg-*` record**
(coordclass classify.go:119; extmsg services are constructed with
`resolveMailMessagesStore` at api_state.go:215/:734). The class must
relocate atomically — one knob, one marker — or by-id routing and the
residue story split. Therefore:

- The mail store (slices 1–2) lands **DARK**: the backend seam plus
  `internal/classdb/messaging`'s `messages` table, proven by the
  conformance suite, with NO routing flip.
- extmsg's typed tables land next (their own seam-plan doc + slices) into
  the SAME `messaging.db` file.
- Only THEN one wiring + migration pair flips
  `[beads.classes.messaging]` + `.gc/store/messaging.migrated`
  (orders/nudges pattern), importing open mail + extmsg actives together
  (design "Migration & cutover" row 3).

Do NOT flip `sqliteCapableBeadClasses[BeadClassMessaging]` before the
extmsg tables exist.

## Today's architecture (what the seam must preserve)

- **The codec is already confined** to `internal/mail/beadmail`
  (`createMessageBead` :198, `beadToMessage` :1228) — but there is no
  narrow store seam: `beadmail.Provider` holds `beads.Store` directly
  (`store` = messages, `sessionStore` = addressing). Messages are
  `Type="message"` beads, `Ephemeral: true` (wisp tier), so every list
  read is `TierBoth` (+`Live` for the inbox scan) and the purge is
  `TierWisps`.
- **Addressing already rides the sessions class**: every route expansion /
  sender resolution reads `p.sessionStore` (recipientRoutes :988,
  resolveSenderRoute :211). The relocation moves ONLY the message store.
- **The message-store op inventory** (every `p.store` touch):

| op | beadmail site | store call |
|---|---|---|
| create (subject/body/from/to + thread/reply-to/handoff labels + display metadata, ephemeral) | createMessageBead :198, Reply :585 | Create |
| get by id | Get/Read/MarkRead/MarkUnread/Archive/Reply/Thread/ArchiveInjectedAutoHandoffs | Get |
| set read/unread | Read :290, MarkRead :311, MarkUnread :326 | Update(±"read" label + mail.read meta) |
| eager delete (archive) | Archive :357/:365, ArchiveMatching :431, ArchiveInjectedAutoHandoffs :464 | Delete |
| list open for routes (nil = all) | messageCandidatesAll :1197 | List(Type, Status open, TierBoth, Live, Assignees/AllowScan) |
| list thread | Thread :689 | List(Label thread:, Type, SortCreatedAsc, TierBoth) |
| list read before cutoff | readMessagesBefore :799 | List(Type, Label read, CreatedBefore, Limit, SortCreatedAsc, TierBoth) |
| close with reason (retention) | SweepReadMessagesBefore :853/:857 | SetMetadata(close_reason) + Close |
| purge read wisps (+dep hygiene) | PurgeReadMessageWisps :897 | List(TierWisps, meta mail.read) + DepList/DepRemove/DepAdd/Delete |

- **Semantics that MUST survive** (pinned by beadmail_test.go /
  beadmail_retention_test.go / mailtest/conformance.go):
  - `mail.read` metadata WINS over the "read" label (beadToMessage
    :1237-1243) → collapses to the sqlite `read` column.
  - The 6b0eb0d6b addressability contract: retention-swept mail
    (`close_reason == RetentionSweepCloseReason`) stays addressable by
    direct id (Get/Read/Reply/Mark*) but leaves the aggregate views;
    user-removed mail (row deleted; legacy closed-without-the-reason) is
    not-found (`isRemovedMessageBead` :632). Maps to
    `status='closed' ∧ close_reason=…` vs row-deleted.
  - Wrong-type errors: Get/Thread/Archive on a non-message bead error
    with the bead's actual type (bd only — sqlite cannot see foreign
    beads and reports not-found; conformance runs through mail.Provider
    so this divergence is misuse-only, documented here).
  - Reply routes to `mail.from_session_id` first, display fallback; the
    display-vs-raw From/To split is the *_addr vs *_display columns.
  - Events fire ABOVE beadmail (CLI cmd_mail.go, API handler_mail.go,
    handoff) — the seam moves no emission.
- **Retention/purge callers** (must keep working on both backends when
  the wiring slice lands): nudge-mail sweep mail leg
  (`SweepReadMessagesBefore` via nudge_mail_sweep.go:97/:142, always-on
  watchdog + `gc order sweep-nudge-mail`), wisp GC
  (`PurgeReadMessageWisps` wisp_gc.go:202, TTL = `[mail] retention_ttl`).
- **Counts are scan-and-tally today** (CountRecipients :727 pulls all
  open candidates). "Native counts" (P3 work plan) = a backend count op;
  bd impl keeps the identical scan.
- **Raw consumers** (the "raw wraps" analogue — revisit at the wiring
  slice, none block the dark slices): coordclass classifier;
  hook-claim's message skip (becomes a defensive no-op post-flip);
  doctor_backlog_depth's `IsMessageBead` bucket; reaper.sh's
  `issue_type='message'` Dolt filters and the `mail_wisps` count (goes
  to zero post-flip — reaper update belongs to the migration slice);
  `order_dispatch.go:1697` IsMessageBead.
- **No unread TTL exists today** — the design's 30d unread TTL is
  net-new behavior for the sqlite store's sweeper (do NOT add it to the
  bd backend; it activates with the flip).
- **priority:/cc: labels are decode-only** with no in-tree producer; the
  schema drops them (design open question 2). The bd backend keeps
  decoding; the sqlite backend returns zero values. HandoffIntent
  .ExtraLabels: in-tree callers only ever pass the two flag labels
  (`gc:auto-handoff`, `gc:archive-after-inject`) → they become the two
  flag columns; other extra labels are bd-only (documented, dropped by
  the sqlite backend).

## Slice 1: the seam (byte-identical)

Introduce an unexported `messagesBackend` interface inside beadmail
(orders/nudges pattern: unexported type, exported method names, another
package satisfies it structurally):

```go
// Record: the persistence-edge view of one stored message — the design's
// row shape plus the bd-compat decode-only fields.
type Record struct {
    ID, ThreadID, ReplyToID              string
    FromAddr, ToAddr                     string // raw bead.From / Assignee
    FromSessionID, FromDisplay           string
    ToSessionID, ToDisplay               string
    Subject, Body                        string
    CreatedAt                            time.Time
    Read, Open                           bool
    CloseReason                          string
    AutoHandoff, ArchiveAfterInject      bool
    Priority                             int      // bd decode-only
    CC                                   []string // bd decode-only
}

// NewMessage: the create shape (Send/SendHandoff/Reply funnel here).
type NewMessage struct {
    Subject, Body, From, To                          string
    ThreadID, ReplyToID                              string
    FromSessionID, FromDisplay, ToSessionID, ToDisplay string
    AutoHandoff, ArchiveAfterInject                  bool
    ExtraLabels                                      []string // bd-only passthrough
}

type messagesBackend interface {
    Create(msg NewMessage) (Record, error)
    Get(id string) (Record, bool, error) // NotAMessageError for bd wrong-type
    SetRead(id string, read bool) error
    Delete(id string) error
    ListOpenForRecipients(routes []string, includeRead bool) ([]Record, error) // nil = all
    ListThread(threadID string) ([]Record, error) // all statuses; caller filters
    CountOpenForRecipients(routes []string) (total, unread int, err error)
    ListReadCreatedBefore(before time.Time, limit int) ([]Record, error)
    CloseReadWithReason(id, reason string) error
    PurgeReadCreatedBefore(cutoff time.Time) (int, error)
}
```

- bd backend (`beadStore{store beads.Store}`) = the MOVED bodies:
  codec + tier flags + dep-hygiene purge + wrong-type errors, verbatim.
  The `isRemovedMessageBead` gate and Thread's open-only filtering stay
  in the Provider (shared semantics over Record.Open/CloseReason).
- Provider constructors keep every signature (New/NewWithStores/
  NewCached/NewCachedWithStores build the bd backend internally); add
  `NewWithBackend(backend, sessionStore)` taking the unexported
  interface. The ~69+73 test construction sites stay untouched.
- Package retention funcs (SweepReadMessagesBefore/CountReadMessagesBefore/
  PurgeReadMessageWisps) keep their `beads.MailStore` signatures as the
  bd surface; bodies route through the bd backend so the op set is the
  seam's. Backend-shaped twins for the routed callers come with the
  wiring slice, not now.
- `mailtest.RunProviderTests` + the beadmail suites are the byte-compat
  proof (no assertion changes).

## Slice 2: `internal/classdb/messaging` (package `messagingdb`)

- The design's `messages` table + 3 indexes over `internal/classdb/core`;
  id mint `gcm-<n>` (same-tx counter, orders pattern); implements
  `messagesBackend` structurally + `ImportMessage` (verbatim ids/clocks,
  INSERT OR IGNORE) for the later migration.
- Native counts: `CountOpenForRecipients` = one SELECT COUNT.
- Retention for the sqlite store (design): read mail close→purge maps to
  `CloseReadWithReason`/`PurgeReadCreatedBefore` on rows; the NEW 30d
  unread TTL = `SweepUnreadBefore` (+Count twin) used by the store's own
  sweeper — implemented here, activated only at the flip.
- Conformance: `mailtest.RunProviderTests` over
  `beadmail.NewWithBackend(sqliteBackend, memSessionStore)` PLUS the
  retention contract (swept-still-addressable, purge) as a both-backend
  suite in classdb/messaging. Crash gate: acked Send survives SIGKILL
  (core re-exec pattern; integration tag; bump the THREE census
  artifacts 532/164 → 533/165, `git add` the new file first).

## Later slices (recorded, not this session's scope)

3. extmsg typed tables (own seam-plan doc; same db file; the in-process
   mutex pool invariants become UNIQUE constraints).
4. Wiring: ratchet flip + config acceptance test +
   `messaging.migrated` marker + routing at the FOUR construction roots
   (newCityMailProvider, openCityMailProvider, cmd_handoff.go:338,
   nudge-mail sweep mail leg + wisp GC leg + extmsg services) +
   seam-guard test; fail-closed via an erroring backend
   (NewUnavailableQueue analogue). API needs no provider seam (it
   consumes state.MailProvider, which the controller builds routed).
5. Migration: import open mail + extmsg actives (drop >30d unread,
   >TTL read per design) + marker + residue + reaper.sh mail_wisps
   count + doctor/backlog raw-consumer touch-ups.

## Gotchas specific to mail

- `NewCached*` caches SESSION beads only — the cache is addressing-side
  and backend-agnostic; do not move it behind the seam.
- `Archive` on an already-closed bead still Deletes then reports
  ErrAlreadyArchived (:356-364) — preserve the double-step exactly.
- `Thread` accepts a message id OR a bare thread id (Get fallback on
  not-found) — resolution stays in the Provider over backend.Get.
- `messageCandidatesAll` sets `Live: true` (fresh wisp reads) and
  `AllowScan` only for the all-routes form — bd backend must keep both.
- `deriveReplyTitle` / title-from-body truncation happen ABOVE the
  backend (Provider) — do not duplicate in backends.
- The exec/fake providers bypass beadmail entirely ([mail] provider
  knob) — the seam only touches the beadmail default.
- beadmail imports internal/session (addressing) — classdb/messaging
  must NOT import beadmail (cycle: beadmail will import it? NO —
  wiring happens in cmd/gc, which hands the sqlite backend to
  NewWithBackend; classdb/messaging imports beadmail ONLY for the
  Record/NewMessage types… that is a cycle-free direction: messaging →
  beadmail → {beads, mail, session}. Verify no reverse import sneaks in.)
