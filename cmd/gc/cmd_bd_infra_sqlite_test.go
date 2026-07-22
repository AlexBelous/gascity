package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
)

// seedSQLiteInfraScopeMarkers writes the canonical .beads/config.yaml and
// metadata.json (backend=sqlite) for a split city's infra scope so
// cityHasInfraStore + cityInfraScopeIsSQLite report true and openStoreAtForCity
// opens the embedded sqlite store.
func seedSQLiteInfraScopeMarkers(t *testing.T, cityPath string) {
	t.Helper()
	dir := filepath.Join(infraScopeRoot(cityPath), ".beads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir infra .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("issue_prefix: gcg\ndatabase: gcg\n"), 0o644); err != nil {
		t.Fatalf("write infra config.yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), []byte(`{"backend":"sqlite","database":"gcg","issue_prefix":"gcg"}`), 0o644); err != nil {
		t.Fatalf("write infra metadata.json: %v", err)
	}
}

func seedBdInfraSqliteBead(t *testing.T, cityPath string, target execStoreTarget, bead beads.Bead) beads.Bead {
	t.Helper()
	store, err := openStoreAtForCity(target.ScopeRoot, cityPath)
	if err != nil {
		t.Fatalf("open embedded sqlite infra store: %v", err)
	}
	defer func() { _ = closeBeadStoreHandle(store) }()
	created, err := store.Create(bead)
	if err != nil {
		t.Fatalf("seed bead: %v", err)
	}
	return created
}

func getBdInfraSqliteBead(t *testing.T, cityPath string, target execStoreTarget, id string) beads.Bead {
	t.Helper()
	store, err := openStoreAtForCity(target.ScopeRoot, cityPath)
	if err != nil {
		t.Fatalf("reopen embedded sqlite infra store: %v", err)
	}
	defer func() { _ = closeBeadStoreHandle(store) }()
	got, err := store.Get(id)
	if err != nil {
		t.Fatalf("Get %s: %v", id, err)
	}
	return got
}

// TestBdInfraSqliteCloseMutatesEmbeddedStoreAndEmits proves `bd close gcg-X` on a
// split city's embedded sqlite infra scope closes the bead IN-PROCESS (bd cannot
// reach the store) and appends bead.closed to the city's events.jsonl.
func TestBdInfraSqliteCloseMutatesEmbeddedStoreAndEmits(t *testing.T) {
	cfg := &config.City{}
	cityPath := t.TempDir()
	seedSQLiteInfraScopeMarkers(t, cityPath)
	target := bdInfraScopeTarget(cityPath)
	created := seedBdInfraSqliteBead(t, cityPath, target, beads.Bead{Title: "step", Type: "task"})

	var out, errb bytes.Buffer
	code, handled := maybeRouteBdInfraSqliteMutation(cityPath, cfg, target, []string{"close", created.ID}, &out, &errb)
	if !handled {
		t.Fatalf("close on a sqlite infra scope was not handled in-process")
	}
	if code != 0 {
		t.Fatalf("close exit code = %d, stderr=%q", code, errb.String())
	}

	if got := getBdInfraSqliteBead(t, cityPath, target, created.ID); got.Status != "closed" {
		t.Fatalf("bead %s status = %q in the embedded store, want closed", created.ID, got.Status)
	}
	evts := readEmittedEvents(t, cityPath)
	if n := len(eventsOfType(evts, events.BeadClosed)); n != 1 {
		t.Fatalf("bead.closed count = %d, want 1", n)
	}
	if e := eventsOfType(evts, events.BeadClosed)[0]; e.Subject != created.ID {
		t.Fatalf("bead.closed subject = %q, want %q", e.Subject, created.ID)
	}
}

// TestBdInfraSqliteUpdateSetMetadataMutatesAndEmits proves
// `bd update gcg-X --set-metadata k=v` on the embedded sqlite infra scope stamps
// the metadata in-process and appends bead.updated.
func TestBdInfraSqliteUpdateSetMetadataMutatesAndEmits(t *testing.T) {
	cfg := &config.City{}
	cityPath := t.TempDir()
	seedSQLiteInfraScopeMarkers(t, cityPath)
	target := bdInfraScopeTarget(cityPath)
	created := seedBdInfraSqliteBead(t, cityPath, target, beads.Bead{Title: "review step", Type: "task"})

	var out, errb bytes.Buffer
	code, handled := maybeRouteBdInfraSqliteMutation(cityPath, cfg, target,
		[]string{"update", created.ID, "--set-metadata", "review.verdict=pass"}, &out, &errb)
	if !handled {
		t.Fatalf("update on a sqlite infra scope was not handled in-process")
	}
	if code != 0 {
		t.Fatalf("update exit code = %d, stderr=%q", code, errb.String())
	}

	if got := getBdInfraSqliteBead(t, cityPath, target, created.ID); got.Metadata["review.verdict"] != "pass" {
		t.Fatalf("bead %s metadata review.verdict = %q, want pass", created.ID, got.Metadata["review.verdict"])
	}
	evts := readEmittedEvents(t, cityPath)
	if n := len(eventsOfType(evts, events.BeadUpdated)); n != 1 {
		t.Fatalf("bead.updated count = %d, want 1", n)
	}
}

// TestBdInfraSqliteRouteGatingByteIdentical proves the interception fires ONLY
// for the sqlite-backed infra scope and mutating subcommands: a non-infra scope,
// and a read subcommand on the infra scope, both fall through (handled=false) so
// the caller keeps its current exec behavior.
func TestBdInfraSqliteRouteGatingByteIdentical(t *testing.T) {
	cfg := &config.City{}

	t.Run("non-infra scope falls through", func(t *testing.T) {
		cityPath := t.TempDir()
		seedSQLiteInfraScopeMarkers(t, cityPath)
		cityTarget := execStoreTarget{ScopeRoot: cityPath, ScopeKind: "city"}
		var out, errb bytes.Buffer
		if _, handled := maybeRouteBdInfraSqliteMutation(cityPath, cfg, cityTarget, []string{"close", "gc-1"}, &out, &errb); handled {
			t.Fatalf("non-infra scope must not be handled in-process")
		}
	})

	t.Run("read subcommand on infra scope falls through", func(t *testing.T) {
		cityPath := t.TempDir()
		seedSQLiteInfraScopeMarkers(t, cityPath)
		target := bdInfraScopeTarget(cityPath)
		var out, errb bytes.Buffer
		if _, handled := maybeRouteBdInfraSqliteMutation(cityPath, cfg, target, []string{"show", "gcg-1"}, &out, &errb); handled {
			t.Fatalf("read subcommand must fall through to current behavior")
		}
	})

	t.Run("infra scope without sqlite backend falls through", func(t *testing.T) {
		cityPath := t.TempDir()
		// cityHasInfraStore true, but backend is dolt (not sqlite): exec bd path.
		dir := filepath.Join(infraScopeRoot(cityPath), ".beads")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("issue_prefix: gcg\n"), 0o644); err != nil {
			t.Fatalf("write config.yaml: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "metadata.json"), []byte(`{"backend":"dolt","dolt_database":"gcg"}`), 0o644); err != nil {
			t.Fatalf("write metadata.json: %v", err)
		}
		target := bdInfraScopeTarget(cityPath)
		var out, errb bytes.Buffer
		if _, handled := maybeRouteBdInfraSqliteMutation(cityPath, cfg, target, []string{"close", "gcg-1"}, &out, &errb); handled {
			t.Fatalf("a Dolt-backed infra scope must keep exec'ing bd (not handled in-process)")
		}
	})
}

func TestParseBdInfraUpdateArgs(t *testing.T) {
	t.Run("set-metadata and status", func(t *testing.T) {
		id, opts, err := parseBdInfraUpdateArgs([]string{"gcg-1", "--set-metadata", "review.verdict=pass", "--status", "closed"})
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if id != "gcg-1" {
			t.Fatalf("id = %q, want gcg-1", id)
		}
		if opts.Metadata["review.verdict"] != "pass" {
			t.Fatalf("metadata = %v", opts.Metadata)
		}
		if opts.Status == nil || *opts.Status != "closed" {
			t.Fatalf("status = %v", opts.Status)
		}
	})

	t.Run("equals form", func(t *testing.T) {
		id, opts, err := parseBdInfraUpdateArgs([]string{"gcg-2", "--set-metadata=k=v"})
		if err != nil || id != "gcg-2" || opts.Metadata["k"] != "v" {
			t.Fatalf("id=%q opts=%+v err=%v", id, opts, err)
		}
	})

	t.Run("unsupported flag rejected", func(t *testing.T) {
		if _, _, err := parseBdInfraUpdateArgs([]string{"gcg-3", "--design", "x"}); err == nil {
			t.Fatalf("expected an error for an unsupported update flag")
		}
	})

	t.Run("requires exactly one id", func(t *testing.T) {
		if _, _, err := parseBdInfraUpdateArgs([]string{"--set-metadata", "k=v"}); err == nil {
			t.Fatalf("expected an error for a missing id")
		}
	})
}

func TestParseBdInfraCloseArgs(t *testing.T) {
	ids, reason, err := parseBdInfraCloseArgs([]string{"gcg-1", "gcg-2", "--reason", "done"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(ids) != 2 || ids[0] != "gcg-1" || ids[1] != "gcg-2" {
		t.Fatalf("ids = %v", ids)
	}
	if reason != "done" {
		t.Fatalf("reason = %q, want done", reason)
	}
	if _, _, err := parseBdInfraCloseArgs([]string{"gcg-1", "--from-file", "x"}); err == nil {
		t.Fatalf("expected an error for an unsupported close flag")
	}
}
