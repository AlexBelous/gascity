package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

// The convergence re-check reads every infrastructure bead out of the work
// store and looks each one up in the binding. On a busy city that is most of
// the CPU a one-shot command spends, and a daemon that shells out to gc every
// few seconds pays it on every call. A one-shot command may therefore reuse a
// CLEAN verdict for a short window, and only while it would re-derive to the
// same answer.
//
// A source bead is stranded only when the binding lacks it AND the manifest
// never recorded it. So the verdict is a function of three things: the source's
// infrastructure ids, the manifest, and the binding's membership of the source
// ids OUTSIDE the manifest (the "unproven" ids). The binding's other rows —
// which the city rewrites every few seconds — cannot change it. A verdict is
// reused when:
//
//   - the work store's working-set hash (Dolt DOLT_HASHOF_DB, which moves on
//     any write, committed or not) is the one the clean check started from, so
//     the source ids are the same;
//   - the manifest and the marker have the same size and modification time;
//   - every recorded unproven id is still in the binding, re-read now;
//   - the verdict is at most infraConvergenceVerdictTTL old.
//
// Anything else — a store that cannot report a fingerprint, a stamp that cannot
// be read, a failed membership read, a stranded or failed check — runs the full
// check. A stranded verdict is never recorded, so the refusal and its
// instruction are always re-derived. The controller's boot does not come
// through here; it always checks in full.
const infraConvergenceVerdictTTL = 60 * time.Second

// infraConvergenceCacheEnv set to "off" disables the reuse.
const infraConvergenceCacheEnv = "GC_INFRA_CONVERGENCE_CACHE"

// infraConvergenceMaxUnproven bounds the ids a verdict carries. A city with
// more unproven ids than this is not in a state worth short-circuiting.
const infraConvergenceMaxUnproven = 1024

const infraConvergenceVerdictVersion = 1

type infraConvergenceFileStamp struct {
	Exists  bool  `json:"exists"`
	Size    int64 `json:"size"`
	ModNano int64 `json:"mod_nano"`
}

type infraConvergenceVerdict struct {
	Version           int                                  `json:"version"`
	Binding           string                               `json:"binding"`
	Database          string                               `json:"database"`
	SourceFingerprint string                               `json:"source_fingerprint"`
	Files             map[string]infraConvergenceFileStamp `json:"files"`
	ProvenBeads       int                                  `json:"proven_beads"`
	Unproven          []string                             `json:"unproven"`
	CheckedAtNano     int64                                `json:"checked_at_nano"`
}

// infraConvergenceNow is the clock the window is measured on. Overridden by tests.
var infraConvergenceNow = time.Now

func infraConvergenceVerdictPath(cityPath string) string {
	return filepath.Join(cityPath, ".gc", "runtime", "infra-convergence-verdict.json")
}

// checkInfraClassConvergenceForCLI is checkInfraClassConvergence for one-shot
// commands: the same verdicts, with a clean one reused under the rules above.
func checkInfraClassConvergenceForCLI(cityPath string, cfg *config.City, logPrefix string, stderr io.Writer) infraMigrationReport {
	return checkInfraClassConvergenceWithReader(cityPath, cfg, logPrefix, stderr, classifyInfraContainmentGapCached)
}

// classifyInfraContainmentGapCached is classifyInfraContainmentGap with a
// clean verdict reused while it still holds.
func classifyInfraContainmentGapCached(cityPath string, target infraBindingTarget, proven map[string]bool) (infraContainmentGap, error) {
	if os.Getenv(infraConvergenceCacheEnv) == "off" {
		return classifyInfraContainmentGap(cityPath, target, proven)
	}
	source, err := openInfraMigrationSource(cityPath)
	if err != nil {
		return infraContainmentGap{}, fmt.Errorf("opening work store: %w", err)
	}
	defer closeBeadStoreHandle(source) //nolint:errcheck // best-effort close

	// Both stamps are taken BEFORE the source is read, so a write that lands
	// while the check runs leaves the recorded stamp stale and the next call
	// re-checks.
	fingerprint := infraSourceFingerprint(source)
	files, filesOK := infraConvergenceFileStamps(target)
	cacheable := fingerprint != "" && filesOK
	path := infraConvergenceVerdictPath(cityPath)
	if cacheable && infraConvergenceVerdictHolds(path, target, fingerprint, files, len(proven)) {
		return infraContainmentGap{}, nil
	}

	ids, err := readInfraContainmentIDs(source)
	if err != nil {
		return infraContainmentGap{}, err
	}
	gap, err := classifyInfraContainmentGapFromIDs(ids, target, proven)
	if err != nil || len(gap.Stranded) > 0 || !cacheable {
		return gap, err
	}
	unproven := make([]string, 0)
	for _, id := range ids {
		if !proven[id] {
			unproven = append(unproven, id)
		}
	}
	if len(unproven) > infraConvergenceMaxUnproven {
		return gap, nil
	}
	sort.Strings(unproven)
	writeInfraConvergenceVerdict(path, infraConvergenceVerdict{
		Version:           infraConvergenceVerdictVersion,
		Binding:           target.Binding,
		Database:          target.Database,
		SourceFingerprint: fingerprint,
		Files:             files,
		ProvenBeads:       len(proven),
		Unproven:          unproven,
		CheckedAtNano:     infraConvergenceNow().UnixNano(),
	})
	return gap, nil
}

// infraSourceFingerprint returns "" when the store cannot vouch that nothing
// changed; the caller then never reuses and never records a verdict.
func infraSourceFingerprint(source beads.Store) string {
	fp, ok := source.(beads.ClassificationFingerprinter)
	if !ok {
		return ""
	}
	hash, err := fp.ClassificationFingerprint()
	if err != nil {
		return ""
	}
	return hash
}

// infraConvergenceFileStamps stamps the manifest and the marker. A missing
// file is a stamp of its own; any other stat failure makes the verdict
// uncacheable.
func infraConvergenceFileStamps(target infraBindingTarget) (map[string]infraConvergenceFileStamp, bool) {
	paths := []string{target.ManifestPath(), target.MarkerPath()}
	stamps := make(map[string]infraConvergenceFileStamp, len(paths))
	for _, p := range paths {
		info, err := os.Stat(p)
		switch {
		case err == nil:
			stamps[p] = infraConvergenceFileStamp{Exists: true, Size: info.Size(), ModNano: info.ModTime().UnixNano()}
		case errors.Is(err, os.ErrNotExist):
			stamps[p] = infraConvergenceFileStamp{}
		default:
			return nil, false
		}
	}
	return stamps, true
}

func infraConvergenceVerdictHolds(path string, target infraBindingTarget, fingerprint string, files map[string]infraConvergenceFileStamp, provenBeads int) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var v infraConvergenceVerdict
	if err := json.Unmarshal(data, &v); err != nil {
		return false
	}
	if v.Version != infraConvergenceVerdictVersion ||
		v.Binding != target.Binding ||
		v.Database != target.Database ||
		v.SourceFingerprint != fingerprint ||
		v.ProvenBeads != provenBeads ||
		len(v.Unproven) > infraConvergenceMaxUnproven ||
		len(v.Files) != len(files) {
		return false
	}
	for p, want := range files {
		if got, ok := v.Files[p]; !ok || got != want {
			return false
		}
	}
	age := infraConvergenceNow().Sub(time.Unix(0, v.CheckedAtNano))
	if age < 0 || age > infraConvergenceVerdictTTL {
		return false
	}
	return infraUnprovenStillBound(target, v.Unproven)
}

// infraUnprovenStillBound re-reads the binding for the only ids whose absence
// there would strand them. Any failure to read counts as "not proven".
func infraUnprovenStillBound(target infraBindingTarget, unproven []string) bool {
	if len(unproven) == 0 {
		return true
	}
	destination, err := openInfraBindingReadOnly(target)
	if err != nil {
		return false
	}
	defer closeBeadStoreHandle(destination) //nolint:errcheck // best-effort close
	rows := make([]beads.Bead, len(unproven))
	for i, id := range unproven {
		rows[i].ID = id
	}
	have, err := infraDestinationMembership(destination, rows)
	if err != nil {
		return false
	}
	for _, id := range unproven {
		if !have[id] {
			return false
		}
	}
	return true
}

// writeInfraConvergenceVerdict records a clean verdict atomically. Failing to
// record it only costs the next call a full check, so errors are dropped.
func writeInfraConvergenceVerdict(path string, v infraConvergenceVerdict) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
	}
}
