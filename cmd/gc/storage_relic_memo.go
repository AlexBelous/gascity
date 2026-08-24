package main

// Remembering that a binding holds relics, so only the first process pays to
// find out.
//
// The boot census lists the binding with AllowScan and IncludeClosed, so it
// costs the binding's whole history rather than its working set, and the
// one-shot CLI funnel pays it per process — in front of `gc bd`'s by-id door,
// the busiest one-shot route there is. Measured on a real sqlite binding
// (internal/storeref/relic_census_bench_test.go): 7ms at 1k rows, 82ms at 10k,
// linear in rows. A city with a year of graph history pays that before it
// answers anything.
//
// Only the TRUE verdict is remembered, and that asymmetry is the whole design.
// Nothing ever deletes a relic — `gc storage migrate` copies rows across under
// their original ids and never deletes the source — so "this binding has held a
// resident" is MONOTONE and cannot go stale. A remembered FALSE is the opposite:
// it would have to stay true about the future, and the only thing that can
// falsify it (a migration into a binding this city already serves) can run from
// another build that has never heard of this file. So a clean binding is
// censused live, every process, forever.
//
// That leaves this note unable to lose a bead in any direction. A stale entry —
// an operator who rebuilt the binding from scratch, say — keeps a probe alive
// that could have retired: the reads stay correct and cost one extra leg. An
// absent or corrupt note costs a scan. Nothing here can retire a probe the
// census would have kept.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/gastownhall/gascity/internal/fsys"
)

// relicCensusMemo is the on-disk form: the class-binding refs a census has
// OBSERVED to hold a bead outside the namespaces that binding declares.
//
// A ref absent from this list means "not known", never "known clean" — the two
// are the same to a reader that censuses on absence, and only one of them is
// safe to write down.
type relicCensusMemo struct {
	BindingsWithResidents []string `json:"bindings_with_residents"`
}

// relicCensusMemoPath is where the note lives, under the city's own .gc.
func relicCensusMemoPath(cityPath string) string {
	return filepath.Join(cityPath, ".gc", "storage-relic-census.json")
}

// readRelicCensusMemo returns the refs a previous process observed to hold
// relics.
//
// An absent note is not an error: it is a city no process has censused yet, and
// the answer is an empty set. A corrupt one IS reported — but it returns the
// empty set alongside the error rather than refusing, because every path out of
// here that answers "not known" is the safe one. Refusing the boot over an
// unreadable cache would turn an optimization into an outage.
func readRelicCensusMemo(cityPath string) (map[string]bool, error) {
	data, err := os.ReadFile(relicCensusMemoPath(cityPath))
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return map[string]bool{}, err
	}
	var memo relicCensusMemo
	if err := json.Unmarshal(data, &memo); err != nil {
		return map[string]bool{}, fmt.Errorf("decoding %s: %w", relicCensusMemoPath(cityPath), err)
	}
	known := make(map[string]bool, len(memo.BindingsWithResidents))
	for _, ref := range memo.BindingsWithResidents {
		known[ref] = true
	}
	return known, nil
}

// writeRelicCensusMemo records the refs known to hold relics, sorted so two
// processes that learned the same thing write the same bytes.
func writeRelicCensusMemo(cityPath string, known map[string]bool) error {
	refs := make([]string, 0, len(known))
	for ref, dirty := range known {
		if dirty {
			refs = append(refs, ref)
		}
	}
	sort.Strings(refs)
	data, err := json.MarshalIndent(relicCensusMemo{BindingsWithResidents: refs}, "", "  ")
	if err != nil {
		return err
	}
	path := relicCensusMemoPath(cityPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return fsys.WriteFileAtomic(fsys.OSFS{}, path, append(data, '\n'), 0o644)
}
