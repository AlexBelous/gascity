package nudgesdb

// Nudges-class backend routing (engdocs/design/infra-class-sqlite-stores.md,
// Nudges section): when [beads.classes.nudges] selects the sqlite backend AND
// the city's nudges migration has completed (the migrated marker exists),
// every nudge-queue front door routes to the embedded class store at
// .gc/store/nudges.db. Until both hold, routing is the byte-identical
// two-tier file shape — the migrated marker, not the binary version, decides
// routing, so a mixed-version window never splits the class across two
// backends.
//
// Unlike the orders routing (which lives in cmd/gc because only cmd/gc and
// the API construct order front doors), the nudges resolver lives here so all
// three queue producers — cmd/gc, internal/session's deferred submit, and
// internal/api's wait-nudge withdraw — share ONE routing decision without
// threading config through the session manager. The marker-first check keeps
// unmigrated cities config-load-free; only a marked city pays the config read
// that arbitrates the rollback escape hatch (marker present, knob flipped
// back to "bd").

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/fsys"
	"github.com/gastownhall/gascity/internal/nudgequeue"
)

// StoreDir returns the per-class embedded-store directory for a city.
func StoreDir(cityPath string) string {
	return filepath.Join(cityPath, ".gc", "store")
}

// StorePath returns the nudges class store file for a city.
func StorePath(cityPath string) string {
	return filepath.Join(StoreDir(cityPath), "nudges.db")
}

// MigratedMarkerPath returns the marker file whose presence commits the city
// to sqlite-backed nudges routing. The migration slice writes it after the
// legacy-queue import completes (immediately, on a city with no legacy queue).
func MigratedMarkerPath(cityPath string) string {
	return filepath.Join(StoreDir(cityPath), "nudges.migrated")
}

// Routed reports whether the nudges class routes to the embedded store for
// this city: the migrated marker exists AND [beads.classes.nudges] selects
// the sqlite backend. cfg may be nil, in which case the city config is loaded
// from disk (config.LoadWithIncludes — the same layered load the cmd/gc
// loaders wrap; the extras they add never touch the [beads] section) behind
// a city.toml mtime+size cache so long-lived routed processes (the 2s
// poller, the controller tick) do not re-parse the pack graph per queue op.
// Only an ABSENT marker means "not migrated"; any other stat failure — like
// a config-load failure on a marked city — is an error, not a bd fallback:
// guessing "file" there would land writes where a routed reader never looks.
func Routed(cityPath string, cfg *config.City) (bool, error) {
	if cityPath == "" {
		return false, nil
	}
	if _, err := os.Stat(MigratedMarkerPath(cityPath)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("checking nudges migrated marker: %w", err)
	}
	if cfg != nil {
		return cfg.Beads.ClassBackend(config.BeadClassNudges) == config.BeadsClassBackendSQLite, nil
	}
	sqlite, err := sqliteBackendConfigured(cityPath)
	if err != nil {
		return false, fmt.Errorf("resolving nudges-class routing for migrated city: %w", err)
	}
	return sqlite, nil
}

// routedConfigCache memoizes the self-loaded [beads.classes.nudges] decision
// per city, keyed by city.toml's (mtime, size). A knob flip rewrites
// city.toml and invalidates the entry; edits confined to included fragments
// are picked up on the next process start or city.toml touch (documented
// limitation — the backend knob lives in city.toml).
var routedConfigCache struct {
	mu     sync.Mutex
	byPath map[string]routedConfigEntry
}

type routedConfigEntry struct {
	modTime time.Time
	size    int64
	sqlite  bool
}

func sqliteBackendConfigured(cityPath string) (bool, error) {
	tomlPath := filepath.Join(cityPath, "city.toml")
	info, statErr := os.Stat(tomlPath)
	if statErr == nil {
		routedConfigCache.mu.Lock()
		entry, ok := routedConfigCache.byPath[tomlPath]
		routedConfigCache.mu.Unlock()
		if ok && entry.modTime.Equal(info.ModTime()) && entry.size == info.Size() {
			return entry.sqlite, nil
		}
	}
	cfg, _, err := config.LoadWithIncludes(fsys.OSFS{}, tomlPath)
	if err != nil {
		return false, err
	}
	sqlite := cfg.Beads.ClassBackend(config.BeadClassNudges) == config.BeadsClassBackendSQLite
	if statErr == nil {
		routedConfigCache.mu.Lock()
		if routedConfigCache.byPath == nil {
			routedConfigCache.byPath = make(map[string]routedConfigEntry)
		}
		routedConfigCache.byPath[tomlPath] = routedConfigEntry{modTime: info.ModTime(), size: info.Size(), sqlite: sqlite}
		routedConfigCache.mu.Unlock()
	}
	return sqlite, nil
}

// sharedHandles is the process-wide cache of open nudges class stores, one
// per database path. Handles live for the process: the controller's
// persistent handle per the design, and for CLI one-shots the process exits
// promptly — the G0 SIGKILL gate proves WAL durability never depends on a
// clean close. Connections open lazily, so a one-shot pays for the
// connections it uses, not the pool cap.
var sharedHandles struct {
	mu     sync.Mutex
	byPath map[string]*Store
}

// SharedStoreFor returns the process-shared handle for a city's nudges class
// store, opening (and migrating the schema) on first use.
func SharedStoreFor(cityPath string) (*Store, error) {
	path := StorePath(cityPath)
	sharedHandles.mu.Lock()
	defer sharedHandles.mu.Unlock()
	if st, ok := sharedHandles.byPath[path]; ok {
		return st, nil
	}
	st, err := Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening nudges class store: %w", err)
	}
	if sharedHandles.byPath == nil {
		sharedHandles.byPath = make(map[string]*Store)
	}
	sharedHandles.byPath[path] = st
	return st, nil
}

// QueueForCity resolves a city's nudge-queue front door: the embedded class
// store when routing is active, else the two-tier file backend with the
// caller's shadow opener (nil = no shadow store; shadow writes no-op — the
// session deferred-submit shape). Fail-closed: when the city is marked
// migrated but routing cannot be resolved or the class store cannot open,
// every operation on the returned queue fails with the cause rather than
// silently falling back to the file backend.
func QueueForCity(cityPath string, openShadow nudgequeue.ShadowStoreOpener) *nudgequeue.Queue {
	routed, err := Routed(cityPath, nil)
	if err != nil {
		return nudgequeue.NewUnavailableQueue(err)
	}
	if !routed {
		return nudgequeue.NewFileQueue(cityPath, openShadow)
	}
	st, err := SharedStoreFor(cityPath)
	if err != nil {
		return nudgequeue.NewUnavailableQueue(err)
	}
	return nudgequeue.NewQueueWithBackend(st)
}

// Shadow projects a queue record onto the nudgequeue.NudgeShadow view the
// wait paths consume, replacing the shadow-bead reads on a routed city. The
// mapping mirrors the file backend's observable shadow lifecycle: a live
// (pending/in-flight) item reads as an open "queued" shadow; dead and
// terminal rows read as closed shadows carrying the terminal stamps (the
// file backend stamped dead items' shadows best-effort at dead-letter time;
// here the stamp is atomic with the transition, so it is always present).
func (r TerminalRecord) Shadow() nudgequeue.NudgeShadow {
	shadow := nudgequeue.NudgeShadow{
		ID:             r.Item.ID,
		BeadID:         r.Item.BeadID,
		Open:           r.QueueState == statePending || r.QueueState == stateInFlight,
		State:          "queued",
		TerminalReason: r.TerminalReason,
		CommitBoundary: r.CommitBoundary,
		Reference:      r.Item.Reference,
		Agent:          r.Item.Agent,
		SessionID:      r.Item.SessionID,
		Source:         r.Item.Source,
		Message:        r.Item.Message,
		DeliverAfter:   r.Item.DeliverAfter,
		ExpiresAt:      r.Item.ExpiresAt,
	}
	if r.TerminalState != "" {
		shadow.State = r.TerminalState
	}
	return shadow
}
