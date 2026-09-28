package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
)

// fingerprintedSource is a work store that reports a working-set fingerprint
// and counts the full reads the containment check makes.
type fingerprintedSource struct {
	beads.Store
	fingerprint string
	fpErr       error
	lists       int
}

func (s *fingerprintedSource) ClassificationFingerprint() (string, error) {
	return s.fingerprint, s.fpErr
}

func (s *fingerprintedSource) List(q beads.ListQuery) ([]beads.Bead, error) {
	s.lists++
	return s.Store.List(q)
}

func setupInfraConvergenceCache(t *testing.T) (string, infraBindingTarget, *fingerprintedSource, *time.Time) {
	t.Helper()
	cityPath := t.TempDir()
	root := t.TempDir()
	target := infraBindingTarget{
		Binding:  "infra",
		Root:     root,
		Dir:      root,
		Database: filepath.Join(root, "beads.db"),
	}
	for _, p := range []string{target.Database, target.ManifestPath(), target.MarkerPath()} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	source := &fingerprintedSource{Store: beads.NewMemStore(), fingerprint: "hash-1"}
	prevOpen := openInfraMigrationSource
	openInfraMigrationSource = func(string) (beads.Store, error) { return source, nil }
	now := time.Unix(1_800_000_000, 0)
	prevNow := infraConvergenceNow
	infraConvergenceNow = func() time.Time { return now }
	t.Cleanup(func() {
		openInfraMigrationSource = prevOpen
		infraConvergenceNow = prevNow
	})
	return cityPath, target, source, &now
}

func runCachedGap(t *testing.T, cityPath string, target infraBindingTarget, proven map[string]bool) infraContainmentGap {
	t.Helper()
	gap, err := classifyInfraContainmentGapCached(cityPath, target, proven)
	if err != nil {
		t.Fatalf("classifyInfraContainmentGapCached: %v", err)
	}
	return gap
}

func TestInfraConvergenceCacheReusesCleanVerdict(t *testing.T) {
	cityPath, target, source, _ := setupInfraConvergenceCache(t)
	runCachedGap(t, cityPath, target, map[string]bool{})
	runCachedGap(t, cityPath, target, map[string]bool{})
	if source.lists != 1 {
		t.Fatalf("full reads = %d, want 1 (second call reuses the verdict)", source.lists)
	}
}

// The binding is rewritten every few seconds by a live city. Rows outside the
// unproven set cannot change the verdict, so its files are not part of it.
func TestInfraConvergenceCacheIgnoresBindingChurn(t *testing.T) {
	cityPath, target, source, _ := setupInfraConvergenceCache(t)
	runCachedGap(t, cityPath, target, map[string]bool{})
	if err := os.WriteFile(target.Database+"-wal", []byte("w"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target.Database, []byte("xy"), 0o600); err != nil {
		t.Fatal(err)
	}
	runCachedGap(t, cityPath, target, map[string]bool{})
	if source.lists != 1 {
		t.Fatalf("full reads = %d, want 1 (binding churn outside the unproven set)", source.lists)
	}
}

func TestInfraConvergenceCacheInvalidation(t *testing.T) {
	cases := map[string]func(t *testing.T, target infraBindingTarget, source *fingerprintedSource, now *time.Time){
		"source write": func(_ *testing.T, _ infraBindingTarget, source *fingerprintedSource, _ *time.Time) {
			source.fingerprint = "hash-2"
		},
		"fingerprint error": func(_ *testing.T, _ infraBindingTarget, source *fingerprintedSource, _ *time.Time) {
			source.fpErr = errors.New("boom")
		},
		"window expired": func(_ *testing.T, _ infraBindingTarget, _ *fingerprintedSource, now *time.Time) {
			*now = now.Add(infraConvergenceVerdictTTL + time.Second)
		},
		"clock went back": func(_ *testing.T, _ infraBindingTarget, _ *fingerprintedSource, now *time.Time) {
			*now = now.Add(-time.Second)
		},
		"manifest rewritten": func(t *testing.T, target infraBindingTarget, _ *fingerprintedSource, _ *time.Time) {
			later := time.Now().Add(time.Hour)
			if err := os.Chtimes(target.ManifestPath(), later, later); err != nil {
				t.Fatal(err)
			}
		},
		"marker removed": func(t *testing.T, target infraBindingTarget, _ *fingerprintedSource, _ *time.Time) {
			if err := os.Remove(target.MarkerPath()); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			cityPath, target, source, now := setupInfraConvergenceCache(t)
			runCachedGap(t, cityPath, target, map[string]bool{})
			change(t, target, source, now)
			runCachedGap(t, cityPath, target, map[string]bool{})
			if source.lists != 2 {
				t.Fatalf("full reads = %d, want 2 (change must force a full check)", source.lists)
			}
		})
	}
}

func TestInfraConvergenceCacheNeedsFingerprint(t *testing.T) {
	cityPath, target, _, _ := setupInfraConvergenceCache(t)
	plain := &plainCountingSource{Store: beads.NewMemStore()}
	openInfraMigrationSource = func(string) (beads.Store, error) { return plain, nil }
	runCachedGap(t, cityPath, target, map[string]bool{})
	runCachedGap(t, cityPath, target, map[string]bool{})
	if plain.lists != 2 {
		t.Fatalf("full reads = %d, want 2 (no fingerprint, no reuse)", plain.lists)
	}
	if _, err := os.Stat(infraConvergenceVerdictPath(cityPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("verdict recorded without a fingerprint: %v", err)
	}
}

type plainCountingSource struct {
	beads.Store
	lists int
}

func (s *plainCountingSource) List(q beads.ListQuery) ([]beads.Bead, error) {
	s.lists++
	return s.Store.List(q)
}

// On a really converged city: a stranded write is reported, never recorded,
// and never masked by the verdict recorded before it.
func TestInfraConvergenceCacheNeverMasksAStrandedWrite(t *testing.T) {
	cityPath, _, source, target := convergedRecoveryCity(t)
	fp := &fingerprintedSource{Store: source, fingerprint: "before"}
	openInfraMigrationSource = func(string) (beads.Store, error) { return fp, nil }
	proven, recorded, err := readInfraCopyManifest(target)
	if err != nil || !recorded {
		t.Fatalf("readInfraCopyManifest: recorded=%v err=%v", recorded, err)
	}

	if gap := runCachedGap(t, cityPath, target, proven); len(gap.Stranded) != 0 {
		t.Fatalf("converged city stranded = %v, want none", gap.Stranded)
	}
	if _, err := os.Stat(infraConvergenceVerdictPath(cityPath)); err != nil {
		t.Fatalf("clean verdict not recorded: %v", err)
	}

	stray := mustCreateInfraBead(t, source, beads.Bead{Title: "post-cutover session", Type: "session", Labels: []string{"gc:session"}})
	fp.fingerprint = "after"
	gap := runCachedGap(t, cityPath, target, proven)
	if len(gap.Stranded) != 1 || gap.Stranded[0] != stray.ID {
		t.Fatalf("stranded = %v, want [%s]", gap.Stranded, stray.ID)
	}
	gap = runCachedGap(t, cityPath, target, proven)
	if len(gap.Stranded) != 1 {
		t.Fatalf("second call stranded = %v, want the stray still named", gap.Stranded)
	}
}

// The unproven ids are the ones the binding must still hold; a verdict that
// names one the binding lacks does not hold.
func TestInfraConvergenceCacheRechecksUnprovenMembership(t *testing.T) {
	_, _, _, target := convergedRecoveryCity(t)
	proven, _, err := readInfraCopyManifest(target)
	if err != nil {
		t.Fatal(err)
	}
	var held string
	for id := range proven {
		held = id
		break
	}
	if held == "" {
		t.Fatal("converged city has an empty manifest")
	}
	if !infraUnprovenStillBound(target, []string{held}) {
		t.Fatalf("%s is in the binding but was reported missing", held)
	}
	if infraUnprovenStillBound(target, []string{held, "gc-nope-404"}) {
		t.Fatal("an id the binding lacks was reported bound")
	}
}
