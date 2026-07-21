# Next-session prompt (paste this to continue)

Continue the **infra-class SQLite stores** track in the existing worktree
`/data/projects/gascity/.claude/worktrees/sqlit` (branch `worktree-sqlit`,
pushed as `origin/feat/infra-class-sqlite-stores`). Do NOT create a new
worktree; run everything from that directory.

**First, read (in this order):**
1. `engdocs/plans/infra-class-sqlite-stores/HANDOFF.md` — state, next steps,
   gotchas. START HERE. P1 orders AND P2 nudges are COMPLETE (both classes
   route behind config + migrated marker, with seamless boot migration,
   residue sweeps, typed events for nudges, and store-owned retention).
2. `engdocs/design/infra-class-sqlite-stores.md` — the authoritative design;
   the Messaging section is the spec for the next phase (P3).

**Then execute P3 messaging, one slice at a time, TDD, one commit per
slice, following the P1/P2 pattern** (study `internal/classdb/orders`,
`internal/classdb/nudges` — including `routing.go`'s shared-resolver
deviation and why it exists — plus `cmd/gc/order_class_store.go`,
`cmd/gc/nudge_class_store.go`, and both `*_class_migrate.go` files before
writing anything):

1. **Domain backend seam**: the `beadmail` persistence edge is already the
   front door (design lists the exact method set). Inventory its
   construction sites and the mail.read metadata-wins precedence first;
   write the seam plan doc like `P2-NUDGES-SEAM-PLAN.md` before coding.
2. **`internal/classdb/messaging`**: the design's `messages` table +
   indexes over `internal/classdb/core`; both-backend conformance through
   the public mail surface (`mailtest/conformance.go` is the pattern);
   crash gate via core's re-exec pattern (integration tag + three-artifact
   census bump). Preserve the `6b0eb0d6b` retention-swept-vs-user-removed
   distinction and add the 30d unread TTL (new behavior, design-prescribed).
3. **Wiring** behind `[beads.classes.messaging]` + `.gc/store/messaging.migrated`
   (ratchet flip, fail-closed roots, seam-guard test). Note
   `resolveMailMessagesStore` already routes every construction — messaging
   relocation may fit `resolveClassStore` directly (it returns a
   beads.Store), unlike nudges; decide against the routed-wrapper hazard
   note in HANDOFF.md before committing to that route.
4. **Migration + marker + residue**; extmsg typed tables follow as their
   own slices per the design.

**Discipline / gotchas (all recorded in HANDOFF.md):**
- Sharded test targets only (never monolithic `go test ./cmd/gc`); give
  `git commit` a long timeout (the pre-commit hook runs `go vet ./...` and
  dashboard checks when API schema changes).
- This box's default `umask 002` fails `TestWriteRunMap*` everywhere incl.
  clean main — run full suites and `git push` under `(umask 022 && …)`.
- New subprocess tests: `//go:build integration` + the THREE lockstepped
  census artifacts; `git add` new test files before running the census.
- Never `time.Sleep` in tests; never wrap a beads.Store that flows into
  capability-asserting paths — thread routing explicitly.
- New event types ripple: KnownEventTypes + RegisterPayload + `go run
  ./cmd/genspec` + `go generate ./internal/api/genclient` + `npm run
  generate:client` (in internal/api/dashboardspa/web) + `make
  dashboard-check` (rebuilds dist/).
- Push to `origin/feat/infra-class-sqlite-stores` before ending the session.
