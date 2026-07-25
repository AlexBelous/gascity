package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWorkTopologyMarkerRoundTrip(t *testing.T) {
	city := t.TempDir()
	path := workRemoteMarkerPath(city)
	m := &workTopologyMarker{
		Kind:       workMarkerKindRemote,
		RecordedAt: time.Now().UTC(),
		Target:     &workTopologyTarget{Host: "10.0.0.5", Port: "3306", Database: "orgdb"},
		ResidueSources: []workResidueSource{
			{Scope: "fe", Host: "127.0.0.1", Port: "3311", Database: "fe"},
		},
	}
	if err := writeWorkTopologyMarker(path, m); err != nil {
		t.Fatalf("writeWorkTopologyMarker: %v", err)
	}
	got, ok, err := readWorkTopologyMarker(path)
	if err != nil || !ok {
		t.Fatalf("readWorkTopologyMarker = (ok=%v, err=%v)", ok, err)
	}
	if got.Kind != workMarkerKindRemote {
		t.Fatalf("kind = %q, want remote", got.Kind)
	}
	if got.Target == nil || got.Target.Database != "orgdb" || got.Target.Host != "10.0.0.5" || got.Target.Port != "3306" {
		t.Fatalf("target = %+v", got.Target)
	}
	if len(got.ResidueSources) != 1 || got.ResidueSources[0].Database != "fe" {
		t.Fatalf("residue sources = %+v", got.ResidueSources)
	}
}

func TestReadWorkTopologyMarkerENOENTOnly(t *testing.T) {
	city := t.TempDir()

	// Missing marker → absent, no error.
	if m, ok, err := readWorkTopologyMarker(workUnifiedMarkerPath(city)); m != nil || ok || err != nil {
		t.Fatalf("missing marker = (%v, %v, %v), want (nil, false, nil)", m, ok, err)
	}

	// A non-ENOENT read failure (marker path is a directory) must surface as an
	// error, never a silent "absent".
	dirMarker := workUnifiedMarkerPath(city)
	if err := os.MkdirAll(dirMarker, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := readWorkTopologyMarker(dirMarker); err == nil || ok {
		t.Fatalf("directory marker must surface an error, got (ok=%v, err=%v)", ok, err)
	}

	// Unparseable JSON must surface as an error too.
	bad := workRemoteMarkerPath(city)
	if err := os.MkdirAll(filepath.Dir(bad), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := readWorkTopologyMarker(bad); err == nil || ok {
		t.Fatalf("invalid json marker must surface an error, got (ok=%v, err=%v)", ok, err)
	}
}

func TestAppendWorkResidueSource(t *testing.T) {
	city := t.TempDir()
	path := workUnifiedMarkerPath(city)

	// Appending to a marker that does not exist is an error — markers are only
	// created by the migration slices.
	if err := appendWorkResidueSource(path, workResidueSource{Scope: "fe", Database: "fe"}); err == nil {
		t.Fatal("append to missing marker should error")
	}

	if err := writeWorkTopologyMarker(path, &workTopologyMarker{Kind: workMarkerKindUnified, RecordedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	src := workResidueSource{Scope: "fe", Host: "127.0.0.1", Port: "3311", Database: "fe"}
	if err := appendWorkResidueSource(path, src); err != nil {
		t.Fatalf("appendWorkResidueSource: %v", err)
	}
	// Same physical identity under a different scope label must not duplicate,
	// and must not reset the existing entry.
	if err := appendWorkResidueSource(path, workResidueSource{Scope: "fe-alias", Host: "127.0.0.1", Port: "3311", Database: "fe"}); err != nil {
		t.Fatalf("appendWorkResidueSource(dup): %v", err)
	}
	m, _, err := readWorkTopologyMarker(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.ResidueSources) != 1 {
		t.Fatalf("residue sources = %d, want 1 (deduped by identity)", len(m.ResidueSources))
	}
	if m.ResidueSources[0].Scope != "fe" {
		t.Fatalf("first residue scope = %q, want fe (original preserved)", m.ResidueSources[0].Scope)
	}
	if m.ResidueSources[0].RecordedAt.IsZero() {
		t.Fatal("append should stamp RecordedAt when zero")
	}
	if got := m.undrainedResidueCount(); got != 1 {
		t.Fatalf("undrainedResidueCount = %d, want 1", got)
	}

	// A genuinely different database records a second source.
	if err := appendWorkResidueSource(path, workResidueSource{Scope: "be", Host: "127.0.0.1", Port: "3312", Database: "be"}); err != nil {
		t.Fatal(err)
	}
	m, _, _ = readWorkTopologyMarker(path)
	if len(m.ResidueSources) != 2 {
		t.Fatalf("residue sources = %d, want 2", len(m.ResidueSources))
	}
}
