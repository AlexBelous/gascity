package main

// Work-bead topology markers + per-scope provenance stamps
// (engdocs/design/beads-work-topology.md, "Config surface" one-way-doors +
// "Topology-aware canonicalization"):
//
//   - .gc/store/work.unified / work.remote — the two CITY markers that record a
//     completed work-scope unify or remote migration (the drain-source ledger).
//   - <scope>/.beads/gc-work-topology.json — a per-SCOPE provenance stamp written
//     ONLY when a topology-driven canonicalization re-points that scope. Stamps
//     live in the scope's own files so they survive marker loss, and are the
//     POSITIVE evidence the marker-less one-way guard keys on (never a bare
//     database-name coincidence).
//
// This slice ships the readers/writers, the residue append, and the stamp; the
// markers are only ever created by the (future) unify and remote migration
// slices. Marker discipline: atomic temp+rename writes (fsys.WriteFileAtomic),
// ENOENT-only existence checks (any other stat/read/parse failure surfaces as
// an error, never a silent "absent"), and a cross-process flock guarding every
// residue read-modify-write.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	nudgesdb "github.com/gastownhall/gascity/internal/classdb/nudges"
	"github.com/gastownhall/gascity/internal/fsys"
)

// workTopologyMarkerKind discriminates the two work-topology markers/stamps.
type workTopologyMarkerKind string

const (
	// workMarkerKindUnified records that every rig scope's work beads were
	// merged into the city scope's database ([beads.work] scope="unified").
	workMarkerKindUnified workTopologyMarkerKind = "unified"
	// workMarkerKindRemote records that the unified work database was moved
	// to a remote dolt:// endpoint ([beads.work] target="dolt://...").
	workMarkerKindRemote workTopologyMarkerKind = "remote"
)

// workUnifiedMarkerPath returns the marker whose presence commits the city to
// the unified work-scope topology (all rig work beads in the city database).
func workUnifiedMarkerPath(cityPath string) string {
	return filepath.Join(nudgesdb.StoreDir(cityPath), "work.unified")
}

// workRemoteMarkerPath returns the marker whose presence commits the city to a
// remote work-DB target.
func workRemoteMarkerPath(cityPath string) string {
	return filepath.Join(nudgesdb.StoreDir(cityPath), "work.remote")
}

// workTopologyTarget is the recorded remote endpoint a remote marker pins.
// It is nil on the unified marker (the city's managed-local Dolt is the
// target). Host/Port are strings so they compare directly against the
// contract.DoltConnectionTarget the live resolvers return.
type workTopologyTarget struct {
	Host     string `json:"host"`
	Port     string `json:"port"`
	Database string `json:"database"`
}

// workResidueSource is one OLD per-scope database identity the canonicalization
// re-pointed away from — the drain source the (future) residue-convergence pass
// reads until Drained flips true. Host is stored already-canonicalized (loopback
// aliases folded) so the same physical database is never recorded twice.
type workResidueSource struct {
	Scope      string    `json:"scope"` // "hq" for the city, else the rig name
	Host       string    `json:"host"`
	Port       string    `json:"port"`
	Database   string    `json:"database"`
	Drained    bool      `json:"drained"`
	RecordedAt time.Time `json:"recorded_at"`
}

// workTopologyCounts carries import/verify tallies the migration slices fill.
// Zero here in this slice; present so the payload schema is stable.
type workTopologyCounts struct {
	Imported int `json:"imported,omitempty"`
	Verified int `json:"verified,omitempty"`
}

// workTopologyMarker is the JSON payload persisted at work.unified /
// work.remote. Every field beyond Kind/RecordedAt is filled progressively by
// the migration slices; this slice reads and appends residue sources.
type workTopologyMarker struct {
	Kind       workTopologyMarkerKind `json:"kind"`
	RecordedAt time.Time              `json:"recorded_at"`
	// Target is the recorded remote endpoint (remote marker only).
	Target *workTopologyTarget `json:"target,omitempty"`
	// ResidueSources lists the old per-source database identities the
	// canonicalization re-pointed away from; a source counts as drained only
	// once its rows (and edges) are all present-or-older in the shared DB.
	ResidueSources []workResidueSource `json:"residue_sources,omitempty"`
	// SkippedDependencies summarizes dep edges the copy could not apply
	// (dangling/cross-type). Filled by the migration slices.
	SkippedDependencies []beads.DepPair `json:"skipped_dependencies,omitempty"`
	// Counts carries import/verify tallies the migration slices fill.
	Counts workTopologyCounts `json:"counts,omitempty"`
}

// undrainedResidueCount returns the number of residue sources not yet drained.
func (m *workTopologyMarker) undrainedResidueCount() int {
	if m == nil {
		return 0
	}
	n := 0
	for _, s := range m.ResidueSources {
		if !s.Drained {
			n++
		}
	}
	return n
}

// workMarkerFileAbsent reports whether a read error means the marker/stamp file
// definitively does not exist: a plain ENOENT, or an ENOTDIR from a parent path
// component not being a directory (a malformed .beads that is a file, not a dir —
// no marker/stamp can live under it). Every other fault (EACCES, EIO, a corrupt
// payload) surfaces, so a routed city never treats a transient fault as absent.
func workMarkerFileAbsent(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// readWorkTopologyMarker reads a work-topology marker with ENOENT-only
// discipline: a missing file returns (nil, false, nil); every other stat,
// read, or parse failure returns an error so callers never treat a transient
// fault as "no marker".
func readWorkTopologyMarker(path string) (*workTopologyMarker, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if workMarkerFileAbsent(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("reading work topology marker %s: %w", path, err)
	}
	var m workTopologyMarker
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, false, fmt.Errorf("parsing work topology marker %s: %w", path, err)
	}
	return &m, true, nil
}

// writeWorkTopologyMarker writes a work-topology marker atomically
// (temp+rename via fsys.WriteFileAtomic), creating .gc/store/ if needed.
//
// Callers that read-modify-write a marker (residue append, drain-flag flips,
// the migration's marker creation) MUST hold the cross-process lock via
// withWorkMarkerLock so a concurrent write is never lost to a blind rename.
func writeWorkTopologyMarker(path string, m *workTopologyMarker) error {
	if m == nil {
		return fmt.Errorf("writing work topology marker %s: nil payload", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("writing work topology marker %s: %w", path, err)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("writing work topology marker %s: %w", path, err)
	}
	data = append(data, '\n')
	if err := fsys.WriteFileAtomic(fsys.OSFS{}, path, data, 0o644); err != nil {
		return fmt.Errorf("writing work topology marker %s: %w", path, err)
	}
	return nil
}

// writeWorkTopologyMarkerLocked writes a whole marker under the cross-process
// lock. Future migration/drain writers that replace the marker payload MUST use
// this (or withWorkMarkerLock directly) so a concurrent residue append from a
// second process is never clobbered by a blind rename.
//
//nolint:unused // exported-for-future-writers: the (future) unify/remote migration and residue-drain slices replace the marker payload through this locked writer.
func writeWorkTopologyMarkerLocked(path string, m *workTopologyMarker) error {
	return withWorkMarkerLock(path, func() error { return writeWorkTopologyMarker(path, m) })
}

// workResidueAppendMu is the in-process fast path; the cross-process
// correctness comes from withWorkMarkerLock's flock.
var workResidueAppendMu sync.Mutex

// withWorkMarkerLock runs fn while holding an exclusive advisory lock on a
// sibling .lock file, so an interleaving read-modify-write from an independent
// process (controller boot/reload vs an operator `gc rig add` vs the API
// rig-provision) is a real cross-process critical section — never a
// last-writer-wins rename that strands a residue source. Mirrors
// withNudgePollerPIDLock.
func withWorkMarkerLock(markerPath string, fn func() error) error {
	lockPath := markerPath + ".lock"
	if err := os.MkdirAll(filepath.Dir(markerPath), 0o755); err != nil {
		return fmt.Errorf("creating work marker dir: %w", err)
	}
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("opening work marker lock: %w", err)
	}
	defer lockFile.Close() //nolint:errcheck
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("locking work marker: %w", err)
	}
	defer syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN) //nolint:errcheck
	return fn()
}

// appendWorkResidueSource records one observed source identity in the marker's
// residue list via a cross-process-locked read-modify-write. A source already
// present (matched on canonicalized host+port+database) is left untouched so its
// Drained flag is never reset by a later re-point pass. Returns an error if the
// marker is missing (only the migration slices create it) or unreadable.
func appendWorkResidueSource(path string, src workResidueSource) error {
	workResidueAppendMu.Lock()
	defer workResidueAppendMu.Unlock()

	src.Host = canonicalWorkHost(src.Host, src.Port) // store canonicalized (F8)
	return withWorkMarkerLock(path, func() error {
		m, ok, err := readWorkTopologyMarker(path)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("appending residue source to %s: marker does not exist", path)
		}
		for _, existing := range m.ResidueSources {
			if sameWorkResidueIdentity(existing, src) {
				return nil // already recorded; preserve its drain state
			}
		}
		if src.RecordedAt.IsZero() {
			src.RecordedAt = time.Now().UTC()
		}
		m.ResidueSources = append(m.ResidueSources, src)
		return writeWorkTopologyMarker(path, m)
	})
}

// sameWorkResidueIdentity reports whether two residue sources name the same
// physical database endpoint (scope label is not part of the identity — the
// same old database must not be recorded twice under two labels or two loopback
// spellings).
func sameWorkResidueIdentity(a, b workResidueSource) bool {
	return canonicalWorkHost(a.Host, a.Port) == canonicalWorkHost(b.Host, b.Port) &&
		strings.TrimSpace(a.Port) == strings.TrimSpace(b.Port) &&
		strings.TrimSpace(a.Database) == strings.TrimSpace(b.Database)
}

// ── per-scope provenance stamp ────────────────────────────────────────────

// workTopologyStampPath returns the per-scope provenance stamp file.
func workTopologyStampPath(scopeRoot string) string {
	return filepath.Join(scopeRoot, ".beads", "gc-work-topology.json")
}

// workTopologyStamp is the durable per-scope evidence that a topology-driven
// canonicalization re-pointed this scope. It lives in the scope's own .beads
// dir so it survives loss of the city markers, and is the positive provenance
// the marker-less one-way guard keys on.
type workTopologyStamp struct {
	Kind       workTopologyMarkerKind `json:"kind"`     // unified | remote
	Database   string                 `json:"database"` // shared db this scope points at
	Host       string                 `json:"host,omitempty"`
	Port       string                 `json:"port,omitempty"`
	RecordedAt time.Time              `json:"recorded_at"`
}

// readWorkTopologyStamp reads a scope's provenance stamp with ENOENT-only
// discipline (absent → nil,false,nil; any other fault surfaces).
func readWorkTopologyStamp(scopeRoot string) (*workTopologyStamp, bool, error) {
	data, err := os.ReadFile(workTopologyStampPath(scopeRoot))
	if err != nil {
		if workMarkerFileAbsent(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("reading work topology stamp for %s: %w", scopeRoot, err)
	}
	var s workTopologyStamp
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, false, fmt.Errorf("parsing work topology stamp for %s: %w", scopeRoot, err)
	}
	return &s, true, nil
}

// writeWorkTopologyStamp writes a scope's provenance stamp atomically.
func writeWorkTopologyStamp(scopeRoot string, s *workTopologyStamp) error {
	path := workTopologyStampPath(scopeRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("writing work topology stamp for %s: %w", scopeRoot, err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("writing work topology stamp for %s: %w", scopeRoot, err)
	}
	data = append(data, '\n')
	if err := fsys.WriteFileAtomic(fsys.OSFS{}, path, data, 0o644); err != nil {
		return fmt.Errorf("writing work topology stamp for %s: %w", scopeRoot, err)
	}
	return nil
}
