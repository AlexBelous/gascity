package sessionsdb

// Sessions-class backend routing (engdocs/design/infra-class-sqlite-stores.md,
// Sessions + waits section): when [beads.classes.sessions] selects the
// sqlite backend AND the city's sessions migration has completed (the
// migrated marker exists), every sessions-class bead op — session lifecycle
// rows and durable waits — routes to the embedded class store at
// .gc/store/sessions.db. Until both hold, routing is the byte-identical bd
// shape (optionally shadow-teed). The migrated marker, not the binary
// version, decides routing, so a mixed-version window never splits the
// class across two backends.
//
// The marker-first check keeps unmigrated cities config-load-free; only a
// marked city pays the config read that arbitrates the rollback escape
// hatch (marker present, knob flipped back to "bd"). Only an ABSENT marker
// means "not migrated"; any other stat failure is an error, not a bd
// fallback — guessing "bd" there would land writes where a routed reader
// never looks (the nudges-review ENOENT-only lesson).

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

// MigratedMarkerPath returns the marker file whose presence commits the
// city to sqlite-backed sessions routing. The migration slice writes it
// after the legacy import (open sessions + waits, ids preserved)
// completes — immediately, on a city with no legacy session beads.
func MigratedMarkerPath(cityPath string) string {
	return filepath.Join(StoreDir(cityPath), "sessions.migrated")
}

// Routed reports whether the sessions class routes to the embedded store
// for this city: the migrated marker exists AND [beads.classes.sessions]
// selects the sqlite backend. cfg may be nil, in which case the city
// config is loaded from disk behind a city.toml mtime+size cache (the
// messaging/nudges precedent).
func Routed(cityPath string, cfg *config.City) (bool, error) {
	if cityPath == "" {
		return false, nil
	}
	if _, err := os.Stat(MigratedMarkerPath(cityPath)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("checking sessions migrated marker: %w", err)
	}
	if cfg != nil {
		return cfg.Beads.ClassBackend(config.BeadClassSessions) == config.BeadsClassBackendSQLite, nil
	}
	sqlite, err := sqliteBackendConfigured(cityPath)
	if err != nil {
		return false, fmt.Errorf("resolving sessions-class routing for migrated city: %w", err)
	}
	return sqlite, nil
}

// routedConfigCache memoizes the self-loaded [beads.classes.sessions]
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
	sqlite := cfg.Beads.ClassBackend(config.BeadClassSessions) == config.BeadsClassBackendSQLite
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

// RoutedStoreFor resolves a city's sessions-class routing and, when routed,
// the process-shared store handle. Fail-closed: a marked city whose routing
// cannot be resolved or whose store cannot open returns the error — callers
// must NOT fall back to bd, which a routed reader never sees.
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
