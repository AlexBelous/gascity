package main

// What the relic note is allowed to remember, and what it must not.
//
// The note exists to keep a migrated city from re-scanning its whole binding in
// front of every one-shot command. The rows here hold its one asymmetry in
// place: TRUE is remembered because nothing deletes a relic, FALSE never is
// because a migration from another build can falsify it, and neither direction
// can retire a probe the live census would have kept.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/storeref"
)

// infraBindingRef is the ref the census keys its note by on a whole-split city.
func infraBindingRef() string {
	return string(storeref.ClassRef(infrastructureClasses()))
}

func TestBootRecordsThatABindingHoldsRelics(t *testing.T) {
	cityPath, cfg, source, _ := convergedInfraCity(t)
	if len(infraStoreFingerprint(t, source)) == 0 {
		t.Fatal("the converged fixture migrated nothing, so there is no relic for the boot to record")
	}

	var stderr bytes.Buffer
	routes, err := storageBootGate(cityPath, cfg, "gc start", nil, &stderr)
	if err != nil {
		t.Fatalf("booting a converged city: %v (stderr: %s)", err, stderr.String())
	}
	t.Cleanup(func() { _ = routes.close() })

	known, err := readRelicCensusMemo(cityPath)
	if err != nil {
		t.Fatalf("reading the note the boot should have written: %v", err)
	}
	if !known[infraBindingRef()] {
		t.Errorf("the boot scanned the whole binding, found a relic, and wrote nothing down; every later process pays the same scan for the same answer (note holds %v)", known)
	}
}

// The other direction, which is the one that could lose a bead.
//
// A clean binding must stay unrecorded. Writing FALSE down would be a claim
// about the future — and `gc storage migrate`, running from a build that has
// never heard of this file, is exactly the thing that falsifies it. The city
// would then answer by-id reads without ever probing the binding its beads were
// just carried into.
func TestBootDoesNotRecordACleanBinding(t *testing.T) {
	cityPath, cfg := convergedEmptyInfraCity(t)

	var stderr bytes.Buffer
	routes, err := storageBootGate(cityPath, cfg, "gc start", nil, &stderr)
	if err != nil {
		t.Fatalf("booting a clean converged city: %v (stderr: %s)", err, stderr.String())
	}
	t.Cleanup(func() { _ = routes.close() })
	if soleBinding(t, routes).HasLegacyResidents {
		t.Fatal("the clean fixture reported relics, so this row cannot tell an unrecorded FALSE from an unreached one")
	}

	known, err := readRelicCensusMemo(cityPath)
	if err != nil {
		t.Fatalf("reading the relic note: %v", err)
	}
	if len(known) != 0 {
		t.Errorf("the boot recorded a verdict for a clean binding (%v); a remembered clean is a promise about migrations this build will never see", known)
	}
	if _, err := os.Stat(relicCensusMemoPath(cityPath)); err == nil {
		t.Error("the boot wrote a relic note for a city with nothing to remember")
	}
}

// The proof the note is actually READ, and the only one that does not need a
// counting store to make it.
//
// The city here is clean, so a live census answers FALSE. The note says TRUE.
// If the boot comes back TRUE the scan cannot have run — no reading of that
// binding produces this verdict. Deleting the read in censusBindingRelics turns
// this row red and leaves every other row in the tree green.
func TestBootTrustsTheNoteInsteadOfScanning(t *testing.T) {
	cityPath, cfg := convergedEmptyInfraCity(t)
	if err := writeRelicCensusMemo(cityPath, map[string]bool{infraBindingRef(): true}); err != nil {
		t.Fatalf("planting the note: %v", err)
	}

	var stderr bytes.Buffer
	routes, err := storageBootGate(cityPath, cfg, "gc start", nil, &stderr)
	if err != nil {
		t.Fatalf("booting a converged city: %v (stderr: %s)", err, stderr.String())
	}
	t.Cleanup(func() { _ = routes.close() })

	binding := soleBinding(t, routes)
	if !binding.MintsReserved {
		t.Fatal("the booted binding does not mint inside its own namespaces, so the census skipped it and this row proves nothing about the note")
	}
	if !binding.HasLegacyResidents {
		t.Error("the boot censused a binding a note already had an answer for; the scan the note exists to skip is still being paid")
	}
}

// A note nobody can parse is a slow boot, not a broken one.
//
// Every failure here has to land on "not known", because that is the answer
// that keeps probing. The row uses a city that really does hold a relic so a
// wrong verdict is a lost bead rather than a slow read, and it insists the
// operator hears about it — a cache that silently stops working is a
// performance regression with no symptom.
func TestACorruptNoteFallsBackToScanningAndSaysSo(t *testing.T) {
	cityPath, cfg, source, _ := convergedInfraCity(t)
	carried := infraStoreFingerprint(t, source)
	if len(carried) == 0 {
		t.Fatal("the converged fixture migrated nothing, so a wrong verdict here would cost nothing and prove nothing")
	}
	path := relicCensusMemoPath(cityPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("making the note's directory: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("planting a corrupt note: %v", err)
	}

	var stderr bytes.Buffer
	routes, err := storageBootGate(cityPath, cfg, "gc start", nil, &stderr)
	if err != nil {
		t.Fatalf("booting a city with an unreadable relic note: %v (stderr: %s)", err, stderr.String())
	}
	t.Cleanup(func() { _ = routes.close() })

	if !soleBinding(t, routes).HasLegacyResidents {
		t.Errorf("an unreadable note retired the probe on a binding holding %v", carried)
	}
	if got := stderr.String(); !strings.Contains(got, "relic-census note") {
		t.Errorf("the boot fell back to a full scan on every command without telling anyone:\n%s", got)
	}
}
