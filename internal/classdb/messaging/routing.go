package messagingdb

// Messaging-class backend routing (engdocs/design/infra-class-sqlite-stores.md,
// Messaging section): when [beads.classes.messaging] selects the sqlite
// backend AND the city's messaging migration has completed (the migrated
// marker exists), every messaging front door — mail message persistence AND
// all extmsg record persistence — routes to the embedded class store at
// .gc/store/messaging.db. Until both hold, routing is the byte-identical bd
// shape. The migrated marker, not the binary version, decides routing, so a
// mixed-version window never splits the class across two backends; and the
// class relocates atomically (one knob, one marker) because mail and extmsg
// share this ONE decision — the P3 atomic-flip ruling.
//
// The resolver lives here (the nudges precedent) because more than one
// package produces messaging-class traffic: cmd/gc constructs the mail
// providers and extmsg services, while internal/api's session-continuity
// repair calls the extmsg repair funcs directly. The marker-first check
// keeps unmigrated cities config-load-free; only a marked city pays the
// config read that arbitrates the rollback escape hatch (marker present,
// knob flipped back to "bd").

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/fsys"
)

// StoreDir returns the per-class embedded-store directory for a city.
func StoreDir(cityPath string) string {
	return filepath.Join(cityPath, ".gc", "store")
}

// StorePath returns the messaging class store file for a city.
func StorePath(cityPath string) string {
	return filepath.Join(StoreDir(cityPath), "messaging.db")
}

// MigratedMarkerPath returns the marker file whose presence commits the city
// to sqlite-backed messaging routing. The migration slice writes it after
// the legacy import (open mail + extmsg actives) completes — immediately, on
// a city with no legacy messaging records.
func MigratedMarkerPath(cityPath string) string {
	return filepath.Join(StoreDir(cityPath), "messaging.migrated")
}

// Routed reports whether the messaging class routes to the embedded store
// for this city: the migrated marker exists AND [beads.classes.messaging]
// selects the sqlite backend. cfg may be nil, in which case the city config
// is loaded from disk (config.LoadWithIncludes — the same layered load the
// cmd/gc loaders wrap; the extras they add never touch the [beads] section)
// behind a city.toml mtime+size cache so long-lived routed processes do not
// re-parse the pack graph per operation. Only an ABSENT marker means "not
// migrated"; any other stat failure — like a config-load failure on a marked
// city — is an error, not a bd fallback: guessing "bd" there would land
// writes where a routed reader never looks.
func Routed(cityPath string, cfg *config.City) (bool, error) {
	if cityPath == "" {
		return false, nil
	}
	if _, err := os.Stat(MigratedMarkerPath(cityPath)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("checking messaging migrated marker: %w", err)
	}
	if cfg != nil {
		return cfg.Beads.ClassBackend(config.BeadClassMessaging) == config.BeadsClassBackendSQLite, nil
	}
	sqlite, err := sqliteBackendConfigured(cityPath)
	if err != nil {
		return false, fmt.Errorf("resolving messaging-class routing for migrated city: %w", err)
	}
	return sqlite, nil
}

// routedConfigCache memoizes the self-loaded [beads.classes.messaging]
// decision per city, keyed by city.toml's (mtime, size). A knob flip
// rewrites city.toml and invalidates the entry; edits confined to included
// fragments are picked up on the next process start or city.toml touch
// (documented limitation — the backend knob lives in city.toml).
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
	sqlite := cfg.Beads.ClassBackend(config.BeadClassMessaging) == config.BeadsClassBackendSQLite
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

// sharedHandles is the process-wide cache of open messaging class stores,
// one per database path. Handles live for the process: the controller's
// persistent handle per the design, and for CLI one-shots the process exits
// promptly — the G0 SIGKILL gate proves WAL durability never depends on a
// clean close. Connections open lazily, so a one-shot pays for the
// connections it uses, not the pool cap. One handle per db also gives the
// extmsg services one conversation lock pool per city
// (extmsg.NewServicesWithBackend pools by backend identity).
var sharedHandles struct {
	mu     sync.Mutex
	byPath map[string]*Store
}

// SharedStoreFor returns the process-shared handle for a city's messaging
// class store, opening (and migrating the schema) on first use.
func SharedStoreFor(cityPath string) (*Store, error) {
	path := StorePath(cityPath)
	sharedHandles.mu.Lock()
	defer sharedHandles.mu.Unlock()
	if st, ok := sharedHandles.byPath[path]; ok {
		return st, nil
	}
	st, err := Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening messaging class store: %w", err)
	}
	if sharedHandles.byPath == nil {
		sharedHandles.byPath = make(map[string]*Store)
	}
	sharedHandles.byPath[path] = st
	return st, nil
}

// RoutedStoreFor resolves a city's messaging-class routing and, when
// routed, the process-shared store handle. Fail-closed: a marked city whose
// routing cannot be resolved or whose store cannot open returns the error —
// callers must NOT fall back to bd, which a routed reader never sees.
func RoutedStoreFor(cityPath string, cfg *config.City) (*Store, bool, error) {
	routed, err := Routed(cityPath, cfg)
	if err != nil || !routed {
		return nil, false, err
	}
	st, err := SharedStoreFor(cityPath)
	if err != nil {
		return nil, false, err
	}
	return st, true, nil
}
