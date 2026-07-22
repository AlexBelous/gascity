package main

// The bulletproof-upgrade contract for the messaging class, mirroring the
// nudges migration gates: fresh cities flip immediately; a populated city's
// bd truth imports verbatim (with the design's age drops); an interrupted
// pre-marker attempt plus retry never resurrects consumed state (reset +
// re-sync); a migrated city's later boots merge-import stragglers before
// clearing bd residue; and any store failure aborts BEFORE the marker so
// the city stays wholly on bd.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	messagingdb "github.com/gastownhall/gascity/internal/classdb/messaging"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/extmsg"
	"github.com/gastownhall/gascity/internal/mail/beadmail"
)

// migrationCity builds an UNMARKED city configured for sqlite messaging,
// with the bd-store open seam pinned to one shared MemStore.
func migrationCity(t *testing.T) (string, *config.City, beads.Store) {
	t.Helper()
	cityPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(cityPath, "city.toml"), []byte("[workspace]\nname = \"test\"\n\n[beads.classes.messaging]\nbackend = \"sqlite\"\n"), 0o644); err != nil {
		t.Fatalf("writing city.toml: %v", err)
	}
	cfg := &config.City{Beads: config.BeadsConfig{Classes: map[string]config.BeadClassConfig{
		config.BeadClassMessaging: {Backend: config.BeadsClassBackendSQLite},
	}}}
	bd := beads.NewMemStore()
	prev := openMessagingClassMigrationStore
	openMessagingClassMigrationStore = func(string) (beads.Store, error) { return bd, nil }
	t.Cleanup(func() { openMessagingClassMigrationStore = prev })
	return cityPath, cfg, bd
}

func migrationRef(conv string) extmsg.ConversationRef {
	return extmsg.ConversationRef{
		ScopeID:        "city",
		Provider:       "slack",
		AccountID:      "acct-mig",
		ConversationID: conv,
		Kind:           extmsg.ConversationDM,
	}
}

func migrationController() extmsg.Caller {
	return extmsg.Caller{Kind: extmsg.CallerController, ID: "migrate-test"}
}

func TestMessagingMigrationFreshCityFlipsImmediately(t *testing.T) {
	cityPath, cfg, _ := migrationCity(t)
	if !ensureMessagingClassMigrated(cityPath, cfg, os.Stderr) {
		t.Fatal("ensureMessagingClassMigrated = false on a fresh city")
	}
	if _, err := os.Stat(messagingdb.MigratedMarkerPath(cityPath)); err != nil {
		t.Fatalf("migrated marker missing after fresh-city flip: %v", err)
	}
	routed, err := messagingdb.Routed(cityPath, cfg)
	if err != nil || !routed {
		t.Fatalf("Routed after fresh flip = %v, %v; want true", routed, err)
	}
	// Idempotent: a second boot short-circuits on the marker.
	if !ensureMessagingClassMigrated(cityPath, cfg, os.Stderr) {
		t.Fatal("second ensure = false on a marked city")
	}
}

func TestMessagingMigrationImportsBdTruth(t *testing.T) {
	cityPath, cfg, bd := migrationCity(t)
	ctx := context.Background()

	// Seed mail: one unread, one read.
	mailProv := beadmail.NewWithStores(bd, bd)
	unreadMsg, err := mailProv.Send("boot/alpha", "boot/beta", "hello", "unread body")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	readMsg, err := mailProv.Send("boot/alpha", "boot/beta", "seen", "read body")
	if err != nil {
		t.Fatalf("Send(read): %v", err)
	}
	if err := mailProv.MarkRead(readMsg.ID); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}

	// Seed extmsg: a handed-off conversation (ended gen1 + active gen2), a
	// group with a participant, and transcript traffic.
	svc := extmsg.NewServices(bd)
	ref := migrationRef("C-migrate")
	if _, err := svc.Bindings.Bind(ctx, migrationController(), extmsg.BindInput{
		Conversation: ref, SessionID: "sess-1", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if _, err := svc.Bindings.Bind(ctx, migrationController(), extmsg.BindInput{
		Conversation: ref, SessionID: "sess-2", Replace: true, Now: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Bind(replace): %v", err)
	}
	groupRef := migrationRef("C-migrate-room")
	group, err := svc.Groups.EnsureGroup(ctx, migrationController(), extmsg.EnsureGroupInput{
		RootConversation: groupRef, Mode: extmsg.GroupModeLauncher, DefaultHandle: "alpha",
	})
	if err != nil {
		t.Fatalf("EnsureGroup: %v", err)
	}
	if _, err := svc.Groups.UpsertParticipant(ctx, migrationController(), extmsg.UpsertParticipantInput{
		GroupID: group.ID, Handle: "alpha", SessionID: "sess-g",
	}); err != nil {
		t.Fatalf("UpsertParticipant: %v", err)
	}
	if _, err := svc.Transcript.Append(ctx, extmsg.AppendTranscriptInput{
		Caller: migrationController(), Conversation: groupRef,
		Kind: extmsg.TranscriptMessageInbound, Text: "history line",
		ProviderMessageID: "pm-mig-1", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	if !ensureMessagingClassMigrated(cityPath, cfg, os.Stderr) {
		t.Fatal("ensureMessagingClassMigrated = false")
	}

	class, err := messagingdb.SharedStoreFor(cityPath)
	if err != nil {
		t.Fatalf("SharedStoreFor: %v", err)
	}
	// Mail crossed with read state intact.
	routedProv := beadmail.NewWithBackend(class, bd)
	gotUnread, err := routedProv.Get(unreadMsg.ID)
	if err != nil || gotUnread.Read {
		t.Fatalf("imported unread = %+v, %v; want unread", gotUnread, err)
	}
	gotRead, err := routedProv.Get(readMsg.ID)
	if err != nil || !gotRead.Read {
		t.Fatalf("imported read = %+v, %v; want read", gotRead, err)
	}

	// The active binding crossed AND the generation ceiling survived: a
	// post-cutover handoff mints gen 3, never a colliding gen 2.
	routedSvc := extmsg.NewServicesWithBackend(class, beads.NewMemStore())
	active, err := routedSvc.Bindings.ResolveByConversation(ctx, ref)
	if err != nil || active == nil || active.SessionID != "sess-2" || active.BindingGeneration != 2 {
		t.Fatalf("routed active binding = %+v, %v; want sess-2 gen 2", active, err)
	}
	next, err := routedSvc.Bindings.Bind(ctx, migrationController(), extmsg.BindInput{
		Conversation: ref, SessionID: "sess-3", Replace: true, Now: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("post-cutover Bind: %v", err)
	}
	if next.BindingGeneration != 3 {
		t.Fatalf("post-cutover generation = %d, want 3 (ceiling preserved)", next.BindingGeneration)
	}

	// Group routing and transcript history crossed.
	route, err := routedSvc.Groups.ResolveInbound(ctx, extmsg.ExternalInboundMessage{Conversation: groupRef, Text: "hi"})
	if err != nil || route.Match != extmsg.GroupRouteDefault || route.TargetSessionID != "sess-g" {
		t.Fatalf("routed group route = %+v, %v; want default sess-g", route, err)
	}
	entries, err := routedSvc.Transcript.List(ctx, extmsg.ListTranscriptInput{Caller: migrationController(), Conversation: groupRef})
	if err != nil || len(entries) != 1 || entries[0].Text != "history line" {
		t.Fatalf("routed transcript = %+v, %v; want the imported entry", entries, err)
	}
	// The imported allocator head continues, not restarts.
	appended, err := routedSvc.Transcript.Append(ctx, extmsg.AppendTranscriptInput{
		Caller: migrationController(), Conversation: groupRef,
		Kind: extmsg.TranscriptMessageInbound, Text: "post-cutover line", CreatedAt: time.Now().UTC(),
	})
	if err != nil || appended.Sequence != 2 {
		t.Fatalf("post-cutover append = %+v, %v; want sequence 2", appended, err)
	}
}

func TestMessagingMigrationRetryDoesNotResurrectConsumedState(t *testing.T) {
	cityPath, cfg, bd := migrationCity(t)
	ctx := context.Background()

	mailProv := beadmail.NewWithStores(bd, bd)
	doomed, err := mailProv.Send("boot/alpha", "boot/beta", "doomed", "will be archived")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	kept, err := mailProv.Send("boot/alpha", "boot/beta", "kept", "will be read")
	if err != nil {
		t.Fatalf("Send(kept): %v", err)
	}
	svc := extmsg.NewServices(bd)
	ref := migrationRef("C-retry")
	if _, err := svc.Bindings.Bind(ctx, migrationController(), extmsg.BindInput{
		Conversation: ref, SessionID: "sess-r", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	// First attempt imports but CRASHES before the marker (simulated by
	// running only the pre-marker import).
	class, err := messagingdb.SharedStoreFor(cityPath)
	if err != nil {
		t.Fatalf("SharedStoreFor: %v", err)
	}
	if _, err := migrateMessagingIntoClassStore(class, bd, cfg, cityPath, time.Now()); err != nil {
		t.Fatalf("pre-marker migration attempt: %v", err)
	}
	if _, ok, err := class.Get(doomed.ID); err != nil || !ok {
		t.Fatalf("first attempt did not import the doomed message (ok=%v err=%v)", ok, err)
	}

	// The still-bd city moves on: the doomed message is archived, the kept
	// one read, the conversation unbound.
	if err := mailProv.Archive(doomed.ID); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if err := mailProv.MarkRead(kept.ID); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	if _, err := svc.Bindings.Unbind(ctx, migrationController(), extmsg.UnbindInput{
		Conversation: &ref, Now: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Unbind: %v", err)
	}

	// The retry re-syncs to bd's CURRENT truth: no resurrection.
	if !ensureMessagingClassMigrated(cityPath, cfg, os.Stderr) {
		t.Fatal("ensureMessagingClassMigrated = false on retry")
	}
	if _, ok, err := class.Get(doomed.ID); err != nil || ok {
		t.Fatalf("archived message resurrected by retry (ok=%v err=%v)", ok, err)
	}
	gotKept, ok, err := class.Get(kept.ID)
	if err != nil || !ok || !gotKept.Read {
		t.Fatalf("kept message after retry = %+v ok=%v err=%v; want read", gotKept, ok, err)
	}
	routedSvc := extmsg.NewServicesWithBackend(class, beads.NewMemStore())
	active, err := routedSvc.Bindings.ResolveByConversation(ctx, ref)
	if err != nil {
		t.Fatalf("ResolveByConversation: %v", err)
	}
	if active != nil {
		t.Fatalf("unbound conversation resurrected as %+v", active)
	}
}

func TestMessagingResidueSweepImportsStragglersThenClears(t *testing.T) {
	cityPath, cfg, bd := migrationCity(t)
	ctx := context.Background()

	if !ensureMessagingClassMigrated(cityPath, cfg, os.Stderr) {
		t.Fatal("ensureMessagingClassMigrated = false")
	}

	// A mixed-version old binary keeps writing to bd AFTER the flip.
	mailProv := beadmail.NewWithStores(bd, bd)
	straggler, err := mailProv.Send("boot/alpha", "boot/beta", "straggler", "raced the marker")
	if err != nil {
		t.Fatalf("Send(straggler): %v", err)
	}
	svc := extmsg.NewServices(bd)
	ref := migrationRef("C-straggler")
	if _, err := svc.Bindings.Bind(ctx, migrationController(), extmsg.BindInput{
		Conversation: ref, SessionID: "sess-s", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Bind(straggler): %v", err)
	}

	// The next boot's residue sweep merge-imports, then clears bd.
	sweepLegacyMessagingResidue(cityPath, cfg, os.Stderr)

	class, err := messagingdb.SharedStoreFor(cityPath)
	if err != nil {
		t.Fatalf("SharedStoreFor: %v", err)
	}
	if _, ok, err := class.Get(straggler.ID); err != nil || !ok {
		t.Fatalf("straggler message not merged into the class store (ok=%v err=%v)", ok, err)
	}
	routedSvc := extmsg.NewServicesWithBackend(class, beads.NewMemStore())
	active, err := routedSvc.Bindings.ResolveByConversation(ctx, ref)
	if err != nil || active == nil || active.SessionID != "sess-s" {
		t.Fatalf("straggler binding not merged = %+v, %v", active, err)
	}
	// The bd copies are gone (owned by the class store → cleared even
	// inside the open grace window).
	residue, err := beadmail.ExportResidueMessageBeads(beads.MailStore{Store: bd})
	if err != nil {
		t.Fatalf("ExportResidueMessageBeads: %v", err)
	}
	if len(residue) != 0 {
		t.Fatalf("bd message residue after sweep = %+v, want none", residue)
	}
	extmsgResidue, err := extmsg.ExportResidueBeadIDs(bd)
	if err != nil {
		t.Fatalf("ExportResidueBeadIDs: %v", err)
	}
	if len(extmsgResidue) != 0 {
		t.Fatalf("bd extmsg residue after sweep = %+v, want none", extmsgResidue)
	}
}

func TestMessagingMigrationAbortsBeforeMarkerWhenStoreUnavailable(t *testing.T) {
	cityPath, cfg, _ := migrationCity(t)
	// A directory where the database file should be makes Open fail.
	if err := os.MkdirAll(messagingdb.StorePath(cityPath), 0o755); err != nil {
		t.Fatalf("blocking store path: %v", err)
	}
	if ensureMessagingClassMigrated(cityPath, cfg, os.Stderr) {
		t.Fatal("ensureMessagingClassMigrated = true with an unopenable class store")
	}
	if _, err := os.Stat(messagingdb.MigratedMarkerPath(cityPath)); !os.IsNotExist(err) {
		t.Fatalf("marker written despite aborted migration (stat err %v)", err)
	}
}

func TestMessagingImportAgeDrops(t *testing.T) {
	now := time.Now().UTC()
	fresh := beadmail.Record{CreatedAt: now.Add(-time.Hour)}
	oldUnread := beadmail.Record{CreatedAt: now.Add(-31 * 24 * time.Hour)}
	oldRead := beadmail.Record{CreatedAt: now.Add(-2 * time.Hour), Read: true}
	if messagingImportDropsMessage(fresh, now, time.Hour) {
		t.Fatal("fresh unread dropped")
	}
	if !messagingImportDropsMessage(oldUnread, now, 0) {
		t.Fatal(">30d unread kept")
	}
	if !messagingImportDropsMessage(oldRead, now, time.Hour) {
		t.Fatal(">TTL read kept")
	}
	if messagingImportDropsMessage(oldRead, now, 0) {
		t.Fatal("read mail dropped with no retention TTL configured")
	}
	freshRead := beadmail.Record{CreatedAt: now.Add(-time.Minute), Read: true}
	if messagingImportDropsMessage(freshRead, now, time.Hour) {
		t.Fatal("fresh read dropped")
	}
}
