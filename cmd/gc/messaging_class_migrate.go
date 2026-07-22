package main

// Seamless bd→sqlite messaging migration (design "Seamless upgrade" +
// "Migration & cutover" row 3: import open mail + extmsg actives; drop
// >30d unread, >TTL read). On controller boot with
// [beads.classes.messaging] backend="sqlite" and no migrated marker, the
// class store is RESET and the bd store's current truth imported — open
// mail (minus the age drops) and the extmsg actives (plus each
// conversation's generation-ceiling ended binding) — copy-verified, then
// the marker is written (flipping routing for every process from that
// instant, mail and extmsg together — the atomic class relocation), and
// the legacy bd residue is cleared in the background. The whole flow is
// idempotent and recomputed from live state (reset-then-import, INSERT OR
// IGNORE, atomic marker write, converging residue sweep), so an
// interrupted first boot simply resumes and a retry never resurrects
// consumed mail or ended bindings.

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	messagingdb "github.com/gastownhall/gascity/internal/classdb/messaging"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/extmsg"
	"github.com/gastownhall/gascity/internal/mail/beadmail"
)

// openMessagingClassMigrationStore is the bd-store open seam for the
// migration and residue sweep (overridden by tests, mirroring
// openNudgeClassMigrationStore).
var openMessagingClassMigrationStore func(cityPath string) (beads.Store, error) = func(cityPath string) (beads.Store, error) {
	return openStoreAtForCity(cityPath, cityPath)
}

const (
	// messagingUnreadImportTTL is the design's unread-mail age drop at
	// import (and the store sweeper's ongoing unread TTL): unread bd mail
	// older than this is dead weight that today leaks forever.
	messagingUnreadImportTTL = 30 * 24 * time.Hour
	// messagingExtmsgRetentionTTL ages terminal extmsg rows (ended
	// bindings — sparing each conversation's generation ceiling — closed
	// delivery contexts, participants, memberships) out of the class store.
	messagingExtmsgRetentionTTL = 30 * 24 * time.Hour
	// messagingTranscriptDefaultKeep bounds each conversation's retained
	// transcript entries when its state row carries no explicit
	// max_retained_entries.
	messagingTranscriptDefaultKeep = 10000
	// messagingRetentionSweepInterval is the cadence of the routed class
	// store's retention sweep on the controller.
	messagingRetentionSweepInterval = 15 * time.Minute
	// legacyMessagingResidueOpenGrace protects a not-yet-upgraded process's
	// in-flight bd writes during the mixed-version window: the residue sweep
	// leaves OPEN bd records younger than this alone unless the class store
	// already owns their id (the next boot's import-then-sweep converges
	// them).
	legacyMessagingResidueOpenGrace = 10 * time.Minute
)

// messagingClassMigrationResult summarizes one import pass.
type messagingClassMigrationResult struct {
	mailImported   int
	mailDropped    int
	extmsgImported int
	endedDropped   int
}

// ensureMessagingClassMigrated runs the messaging-class migration when the
// config selects the sqlite backend and the migrated marker is absent:
// reset → import (open mail + extmsg actives) → copy-verify → marker →
// straggler re-import. Returns whether routing is (now) committed to the
// class store. Any failure aborts BEFORE the marker is written, so the
// city stays wholly on bd and the next boot retries — a partial import
// must never flip routing (mail and conversations would vanish from the
// routed readers).
func ensureMessagingClassMigrated(cityPath string, cfg *config.City, stderr io.Writer) bool {
	if cfg == nil || cfg.Beads.ClassBackend(config.BeadClassMessaging) != config.BeadsClassBackendSQLite {
		return false
	}
	if _, err := os.Stat(messagingdb.MigratedMarkerPath(cityPath)); err == nil {
		return true
	}
	class, err := messagingdb.SharedStoreFor(cityPath)
	if err != nil {
		fmt.Fprintf(stderr, "gc start: messaging class migration: %v\n", err) //nolint:errcheck // best-effort stderr
		return false
	}
	store, err := openMessagingClassMigrationStore(cityPath)
	if err != nil {
		fmt.Fprintf(stderr, "gc start: messaging class migration: %v\n", err) //nolint:errcheck // best-effort stderr
		return false
	}
	defer closeBeadStoreHandle(store) //nolint:errcheck // best-effort close

	result, err := migrateMessagingIntoClassStore(class, store, cfg, cityPath, time.Now())
	if err != nil {
		fmt.Fprintf(stderr, "gc start: messaging class migration: %v\n", err) //nolint:errcheck // best-effort stderr
		return false
	}
	if err := writeMessagingMigratedMarkerFile(cityPath); err != nil {
		fmt.Fprintf(stderr, "gc start: messaging class migration: %v\n", err) //nolint:errcheck // best-effort stderr
		return false
	}
	// Straggler pass: a message sent or a binding written between the
	// import and the marker flip merge-imports here (INSERT OR IGNORE keeps
	// it idempotent). Best-effort — anything still missed is imported by a
	// later boot's residue sweep before that sweep clears it.
	if stragglers, err := importMessagingSnapshot(class, store, cfg, cityPath, time.Now(), false); err == nil {
		result.mailImported += stragglers.mailImported
		result.extmsgImported += stragglers.extmsgImported
	}
	fmt.Fprintf(stderr, "gc start: messaging class migrated to %s (%d messages, %d extmsg records imported; %d aged-out messages, %d superseded bindings dropped)\n", //nolint:errcheck // best-effort stderr
		messagingdb.StorePath(cityPath), result.mailImported, result.extmsgImported, result.mailDropped, result.endedDropped)
	return true
}

// migrateMessagingIntoClassStore imports the bd store's current messaging
// truth. It first RESETS the class store: an interrupted earlier attempt
// left committed rows behind while the still-bd city consumed mail, ended
// bindings, or advanced transcripts past them — re-syncing to the bd
// store's current truth keeps the retry from resurrecting a consumed
// message or a displaced binding. This runs strictly before the marker, so
// the bd store is still the authority being copied. Every imported id is
// read back from the class store (copy-verify) before the caller may flip
// the marker.
func migrateMessagingIntoClassStore(class *messagingdb.Store, store beads.Store, cfg *config.City, cityPath string, now time.Time) (messagingClassMigrationResult, error) {
	if err := class.ResetForMigration(); err != nil {
		return messagingClassMigrationResult{}, err
	}
	return importMessagingSnapshot(class, store, cfg, cityPath, now, true)
}

// importMessagingSnapshot imports one read of the bd store's messaging
// records (open mail minus the age drops; extmsg actives plus each
// conversation's generation-ceiling ended binding). INSERT OR IGNORE keeps
// re-imports idempotent. verify re-reads every imported id from the class
// store — the copy-verify gate the pre-marker migration requires.
func importMessagingSnapshot(class *messagingdb.Store, store beads.Store, cfg *config.City, cityPath string, now time.Time, verify bool) (messagingClassMigrationResult, error) {
	result := messagingClassMigrationResult{}
	msgStore := beads.MailStore{Store: resolveMailMessagesStore(store, cfg, cityPath, nil)}

	records, err := beadmail.ExportOpenMessages(msgStore)
	if err != nil {
		return result, fmt.Errorf("reading legacy mail: %w", err)
	}
	readTTL := time.Duration(0)
	if cfg != nil {
		if ttl, err := cfg.Mail.RetentionTTLDuration(); err == nil {
			readTTL = ttl
		}
	}
	var mailIDs []string
	for _, rec := range records {
		if messagingImportDropsMessage(rec, now, readTTL) {
			result.mailDropped++
			continue
		}
		if err := class.ImportMessage(rec); err != nil {
			return result, fmt.Errorf("importing message %q: %w", rec.ID, err)
		}
		result.mailImported++
		mailIDs = append(mailIDs, rec.ID)
	}

	export, err := extmsg.ExportRecords(msgStore.Store)
	if err != nil {
		return result, fmt.Errorf("reading legacy extmsg records: %w", err)
	}
	imported, dropped, err := importExtmsgExport(class, export)
	result.extmsgImported = len(imported)
	result.endedDropped = dropped
	if err != nil {
		return result, err
	}

	if verify {
		for _, id := range mailIDs {
			if _, ok, err := class.Get(id); err != nil || !ok {
				return result, fmt.Errorf("verifying imported message %q: found=%v err=%w", id, ok, err)
			}
		}
		for _, id := range imported {
			if ok, err := class.ExtmsgRecordExists(id); err != nil || !ok {
				return result, fmt.Errorf("verifying imported extmsg record %q: found=%v err=%w", id, ok, err)
			}
		}
	}
	return result, nil
}

// messagingImportDropsMessage is the design's age drop at import: unread
// mail past the 30d unread TTL, and read mail past the configured [mail]
// retention_ttl (0 = no read drop), stays behind on the bd side.
func messagingImportDropsMessage(rec beadmail.Record, now time.Time, readTTL time.Duration) bool {
	age := now.Sub(rec.CreatedAt)
	if !rec.Read && age > messagingUnreadImportTTL {
		return true
	}
	return rec.Read && readTTL > 0 && age > readTTL
}

// importExtmsgExport imports one extmsg export: every active binding, each
// conversation's generation-ceiling ended binding (delivery gating and
// generation minting stay monotonic across the cutover; other ended
// bindings are dropped), and the open rows of every other family. Returns
// the imported ids and the superseded-ended-binding drop count.
func importExtmsgExport(class *messagingdb.Store, export extmsg.MigrationExport) ([]string, int, error) {
	var imported []string
	dropped := 0
	ceiling := map[extmsg.ConversationRef]extmsg.BindingExportRecord{}
	for _, rec := range export.Bindings {
		if rec.Status == extmsg.BindingActive {
			continue
		}
		best, ok := ceiling[rec.Conversation]
		if !ok || rec.BindingGeneration > best.BindingGeneration ||
			(rec.BindingGeneration == best.BindingGeneration && rec.ID > best.ID) {
			ceiling[rec.Conversation] = rec
		}
	}
	for _, rec := range export.Bindings {
		if rec.Status != extmsg.BindingActive {
			if best, ok := ceiling[rec.Conversation]; !ok || best.ID != rec.ID {
				dropped++
				continue
			}
		}
		if err := class.ImportBinding(rec.SessionBindingRecord, rec.LastTouchedAt); err != nil {
			return imported, dropped, fmt.Errorf("importing binding %q: %w", rec.ID, err)
		}
		imported = append(imported, rec.ID)
	}
	for _, rec := range export.Deliveries {
		if err := class.ImportDeliveryContext(rec); err != nil {
			return imported, dropped, fmt.Errorf("importing delivery context %q: %w", rec.ID, err)
		}
		imported = append(imported, rec.ID)
	}
	for _, rec := range export.Groups {
		if err := class.ImportGroup(rec); err != nil {
			return imported, dropped, fmt.Errorf("importing group %q: %w", rec.ID, err)
		}
		imported = append(imported, rec.ID)
	}
	for _, rec := range export.Participants {
		if err := class.ImportParticipant(rec); err != nil {
			return imported, dropped, fmt.Errorf("importing participant %q: %w", rec.ID, err)
		}
		imported = append(imported, rec.ID)
	}
	for _, rec := range export.Memberships {
		if err := class.ImportMembership(rec); err != nil {
			return imported, dropped, fmt.Errorf("importing membership %q: %w", rec.ID, err)
		}
		imported = append(imported, rec.ID)
	}
	for _, rec := range export.States {
		if err := class.ImportTranscriptState(rec); err != nil {
			return imported, dropped, fmt.Errorf("importing transcript state %q: %w", rec.ID, err)
		}
		imported = append(imported, rec.ID)
	}
	for _, rec := range export.Entries {
		if err := class.ImportTranscriptEntry(rec); err != nil {
			return imported, dropped, fmt.Errorf("importing transcript entry %q: %w", rec.ID, err)
		}
		imported = append(imported, rec.ID)
	}
	return imported, dropped, nil
}

// writeMessagingMigratedMarkerFile atomically writes the migrated marker
// that commits the city to class-store routing for the WHOLE class.
func writeMessagingMigratedMarkerFile(cityPath string) error {
	dir := messagingdb.StoreDir(cityPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("writing messaging migrated marker: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "messaging.migrated.tmp*")
	if err != nil {
		return fmt.Errorf("writing messaging migrated marker: %w", err)
	}
	name := tmp.Name()
	if _, err := fmt.Fprintf(tmp, "messaging class migrated %s\n", time.Now().UTC().Format(time.RFC3339)); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("writing messaging migrated marker: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("writing messaging migrated marker: %w", err)
	}
	if err := os.Rename(name, messagingdb.MigratedMarkerPath(cityPath)); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("writing messaging migrated marker: %w", err)
	}
	return nil
}

// sweepLegacyMessagingResidue converges the bd store's messaging residue on
// a MIGRATED city with the documented import-then-sweep: it first
// merge-imports any bd message or extmsg record the class store does not
// yet own (a write that raced the marker flip, or a mixed-version old
// binary's — without this, such records would be stranded in bd forever,
// since routed readers never look there), then deletes bd copies the class
// store owns, closed bd records, and open bd records past the grace
// window. Deleting converges across boots, so a kill mid-sweep costs
// nothing.
func sweepLegacyMessagingResidue(cityPath string, cfg *config.City, stderr io.Writer) {
	routed, err := messagingdb.Routed(cityPath, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "gc: messaging legacy residue sweep: %v\n", err) //nolint:errcheck // best-effort stderr
		return
	}
	if !routed {
		return
	}
	class, err := messagingdb.SharedStoreFor(cityPath)
	if err != nil {
		fmt.Fprintf(stderr, "gc: messaging legacy residue sweep: %v\n", err) //nolint:errcheck // best-effort stderr
		return
	}
	store, err := openMessagingClassMigrationStore(cityPath)
	if err != nil {
		fmt.Fprintf(stderr, "gc: messaging legacy residue sweep: %v\n", err) //nolint:errcheck // best-effort stderr
		return
	}
	defer closeBeadStoreHandle(store) //nolint:errcheck // best-effort close

	if stragglers, err := importMessagingSnapshot(class, store, cfg, cityPath, time.Now(), false); err != nil {
		// Import failure must skip the sweep below: sweeping without the
		// import could strand an unimported record — retry next boot.
		fmt.Fprintf(stderr, "gc: messaging legacy residue sweep: importing stragglers: %v\n", err) //nolint:errcheck // best-effort stderr
		return
	} else if stragglers.mailImported+stragglers.extmsgImported > 0 {
		fmt.Fprintf(stderr, "gc: messaging legacy residue sweep: merged %d messages / %d extmsg records into the class store\n", stragglers.mailImported, stragglers.extmsgImported) //nolint:errcheck // best-effort stderr
	}

	msgStore := beads.MailStore{Store: resolveMailMessagesStore(store, cfg, cityPath, nil)}
	now := time.Now()
	var ids []string
	mailResidue, err := beadmail.ExportResidueMessageBeads(msgStore)
	if err != nil {
		fmt.Fprintf(stderr, "gc: messaging legacy residue sweep: %v\n", err) //nolint:errcheck // best-effort stderr
		return
	}
	for _, bead := range mailResidue {
		if keep, err := spareResidueBead(class, bead.ID, bead.Open, bead.CreatedAt, now); err != nil || keep {
			continue
		}
		ids = append(ids, bead.ID)
	}
	extmsgResidue, err := extmsg.ExportResidueBeadIDs(msgStore.Store)
	if err != nil {
		fmt.Fprintf(stderr, "gc: messaging legacy residue sweep: %v\n", err) //nolint:errcheck // best-effort stderr
		return
	}
	for _, bead := range extmsgResidue {
		if keep, err := spareResidueBead(class, bead.ID, bead.Open, bead.CreatedAt, now); err != nil || keep {
			continue
		}
		ids = append(ids, bead.ID)
	}
	deleted, err := deleteLegacyOrderTrackingBeads(store, ids)
	if err != nil {
		fmt.Fprintf(stderr, "gc: messaging legacy residue sweep (deleted %d): %v\n", deleted, err) //nolint:errcheck // best-effort stderr
	} else if deleted > 0 {
		fmt.Fprintf(stderr, "gc: messaging legacy residue sweep: cleared %d migrated bd beads\n", deleted) //nolint:errcheck // best-effort stderr
	}
}

// spareResidueBead reports whether a bd residue bead must survive this
// sweep: only an OPEN bead inside the mixed-version grace window that the
// class store does not own yet — the next boot's import-then-sweep
// converges it. Everything else (closed, class-owned, or aged open) is
// deletable residue.
func spareResidueBead(class *messagingdb.Store, id string, open bool, createdAt time.Time, now time.Time) (bool, error) {
	if !open || now.Sub(createdAt) >= legacyMessagingResidueOpenGrace {
		return false, nil
	}
	if owned, err := ownedByMessagingClass(class, id); err != nil {
		return true, err
	} else if owned {
		return false, nil
	}
	return true, nil
}

// ownedByMessagingClass reports whether the class store owns id in either
// half (a message row or any extmsg table row).
func ownedByMessagingClass(class *messagingdb.Store, id string) (bool, error) {
	if _, ok, err := class.Get(id); err != nil {
		return false, err
	} else if ok {
		return true, nil
	}
	return class.ExtmsgRecordExists(id)
}

// startMessagingRetentionSweeper starts the routed class store's retention
// loop on the controller (idempotent per process-shared handle): the
// design's net-new unread-mail TTL, the extmsg terminal-row TTL, and
// transcript pruning — the paths that run with only the controller alive
// (SDK self-sufficiency; the read-mail close→purge legs ride the
// nudge-mail sweep watchdog and wisp GC).
func startMessagingRetentionSweeper(cityPath string, stderr io.Writer) {
	class, err := messagingdb.SharedStoreFor(cityPath)
	if err != nil {
		fmt.Fprintf(stderr, "gc start: messaging retention sweeper: %v\n", err) //nolint:errcheck // best-effort stderr
		return
	}
	class.StartRetentionSweeper(messagingRetentionSweepInterval, messagingUnreadImportTTL, messagingExtmsgRetentionTTL, messagingTranscriptDefaultKeep, stderr)
}
