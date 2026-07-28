package beads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fakeBdRunner is a programmable CommandRunner for the bd-leg fast tests. It
// dispatches on the bd subcommand so a single instance answers version, export,
// show, and config get in one test.
type fakeBdRunner struct {
	capabilities []string          // advertised in `bd version --json`
	export       string            // stdout for `bd export`
	show         string            // JSON array for `bd show --json`
	configGet    map[string]string // key -> value for `bd config get <key>` (absent = not set)
}

func (f *fakeBdRunner) run(_, name string, args ...string) ([]byte, error) {
	if name != "bd" || len(args) == 0 {
		return nil, fmt.Errorf("unexpected command %s %v", name, args)
	}
	switch args[0] {
	case "version":
		payload := map[string]any{"version": "1.1.0"}
		if f.capabilities != nil {
			payload["capabilities"] = f.capabilities
		}
		b, _ := json.Marshal(payload)
		return b, nil
	case "export":
		return []byte(f.export), nil
	case "show":
		return []byte(f.show), nil
	case "config":
		if len(args) >= 3 && args[1] == "get" {
			key := args[2]
			if v, ok := f.configGet[key]; ok {
				return []byte(v), nil
			}
			return []byte(key + " (not set in config.yaml)"), nil
		}
	}
	return nil, fmt.Errorf("unexpected bd subcommand %v", args)
}

type recordedImport struct {
	args  []string
	env   []string
	stdin string
}

func recordingImportRunner(calls *[]recordedImport, result string) BDImportRunnerFunc {
	return func(_ string, env []string, stdin []byte, args ...string) ([]byte, error) {
		*calls = append(*calls, recordedImport{
			args:  append([]string(nil), args...),
			env:   append([]string(nil), env...),
			stdin: string(stdin),
		})
		return []byte(result), nil
	}
}

func snapForID(t *testing.T, id string) Snapshot {
	t.Helper()
	return snapForIDAt(t, id, "2026-01-01T00:00:00Z")
}

func snapForIDAt(t *testing.T, id, updatedAt string) Snapshot {
	t.Helper()
	return decodeOne(t, exportLine(t, map[string]any{
		"id": id, "title": id, "status": "open", "updated_at": updatedAt,
	}))
}

func envContains(env []string, want string) bool {
	for _, kv := range env {
		if kv == want {
			return true
		}
	}
	return false
}

func envHasKey(env []string, key string) bool {
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return true
		}
	}
	return false
}

func TestBdStoreExportShellsBdExport(t *testing.T) {
	body := `{"_type":"issue","id":"gc-1","title":"a","updated_at":"2026-01-01T00:00:00Z"}
{"_type":"memory","key":"m","value":"x"}
{"_type":"issue","id":"gc-2","title":"b","updated_at":"2026-01-01T00:00:00Z"}
`
	f := &fakeBdRunner{export: body}
	s := NewBdStore("/city", f.run)
	snaps, err := s.ExportBeadSnapshots(context.Background(), ExportOptions{})
	if err != nil {
		t.Fatalf("ExportBeadSnapshots: %v", err)
	}
	if got := snapIDs(snaps); len(got) != 2 || got[0] != "gc-1" || got[1] != "gc-2" {
		t.Fatalf("exported %v, want [gc-1 gc-2] (memory skipped)", got)
	}
}

func TestBdStoreExportIncludeEphemeralUsesAllFlag(t *testing.T) {
	var calls [][]string
	runner := func(_, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		if len(args) > 0 && args[0] == "config" {
			return []byte("(not set in config.yaml)"), nil
		}
		return []byte(""), nil
	}
	s := NewBdStore("/city", runner)
	if _, err := s.ExportBeadSnapshots(context.Background(), ExportOptions{IncludeEphemeral: true}); err != nil {
		t.Fatalf("export: %v", err)
	}
	sawAll := false
	for _, c := range calls {
		if len(c) >= 2 && c[1] == "export" {
			for _, a := range c[2:] {
				if a == "--all" {
					sawAll = true
				}
			}
		}
	}
	if !sawAll {
		t.Fatalf("IncludeEphemeral export must pass --all; calls=%v", calls)
	}
}

func TestBdStoreExportRefusesOwnerExcludeConfig(t *testing.T) {
	f := &fakeBdRunner{
		export:    "",
		configGet: map[string]string{"export.exclude_owners": "bot@example.com"},
	}
	s := NewBdStore("/city", f.run)
	_, err := s.ExportBeadSnapshots(context.Background(), ExportOptions{})
	if !errors.Is(err, ErrExportOwnerExcludeConfigured) {
		t.Fatalf("export = %v, want ErrExportOwnerExcludeConfigured", err)
	}
}

func TestBdStoreImportPlainInheritsScopedEnvAndNeedsNoCapability(t *testing.T) {
	// A runner that FAILS on `bd version` proves the plain import never probes
	// capability.
	runner := func(_, _ string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "version" {
			t.Fatalf("plain import must not probe capability")
		}
		if len(args) > 0 && args[0] == "show" {
			return nil, fmt.Errorf("no issues found matching the provided IDs")
		}
		return nil, fmt.Errorf("unexpected %v", args)
	}
	s := NewBdStore("/city", runner, WithBdStoreEnv(map[string]string{"BEADS_DIR": "/city/.beads"}))
	var calls []recordedImport
	s.SetBDImportRunner(recordingImportRunner(&calls, `{"created":1,"ids":["gc-1"]}`))

	report, err := s.ImportBeadSnapshots(context.Background(), []Snapshot{snapForID(t, "gc-1")}, ImportOptions{})
	if err != nil {
		t.Fatalf("plain import: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 import call, got %d", len(calls))
	}
	if got := strings.Join(calls[0].args, " "); got != "import - --json" {
		t.Fatalf("guarded import args = %q", got)
	}
	// The scoped env must reach the import child, exactly like bd export/version.
	if !envContains(calls[0].env, "BEADS_DIR=/city/.beads") {
		t.Fatalf("import env missing scoped BEADS_DIR: %v", calls[0].env)
	}
	if !envContains(calls[0].env, bdAutoBackupOptOutEnvKey+"=false") {
		t.Fatalf("import env missing auto-backup opt-out: %v", calls[0].env)
	}
	if envHasKey(calls[0].env, "BD_JSON_ENVELOPE") {
		t.Fatalf("import env must strip BD_JSON_ENVELOPE: %v", calls[0].env)
	}
	if len(report.Inserted) != 1 || report.Inserted[0] != "gc-1" {
		t.Fatalf("report = %+v", report)
	}
}

func TestBdStoreImportIdenticalTieReclassifiedKeptLocal(t *testing.T) {
	f := &fakeBdRunner{
		show: `[{"id":"gc-1","title":"gc-1","status":"open","updated_at":"2026-01-01T00:00:00Z"}]`,
	}
	s := NewBdStore("/city", f.run)
	var calls []recordedImport
	s.SetBDImportRunner(recordingImportRunner(&calls, `{"created":0,"updated":0,"ids":["gc-1"]}`))

	report, err := s.ImportBeadSnapshots(context.Background(), []Snapshot{snapForID(t, "gc-1")}, ImportOptions{})
	if err != nil {
		t.Fatalf("ImportBeadSnapshots: %v", err)
	}
	if !reflect.DeepEqual(report.KeptLocal, []string{"gc-1"}) {
		t.Fatalf("KeptLocal = %v, want [gc-1]; report=%+v", report.KeptLocal, report)
	}
	if len(report.Inserted) != 0 {
		t.Fatalf("Inserted = %v, want none for existing identical tie", report.Inserted)
	}
}

func TestReconcileGuardedBDReportClassifiesMixedAmbiguousIDs(t *testing.T) {
	report := ImportReport{
		Inserted:     []string{"gc-new", "gc-stale", "gc-tie", "gc-newer"},
		Updated:      []string{"gc-known-updated"},
		KeptLocal:    []string{"gc-known-tie"},
		StaleSkipped: []string{"gc-known-stale"},
	}
	originalInserted := append([]string(nil), report.Inserted...)
	incoming := []Snapshot{
		snapForIDAt(t, "gc-new", "2026-01-02T00:00:00Z"),
		snapForIDAt(t, "gc-stale", "2026-01-01T00:00:00Z"),
		snapForIDAt(t, "gc-tie", "2026-01-02T00:00:00Z"),
		snapForIDAt(t, "gc-newer", "2026-01-03T00:00:00Z"),
	}
	destination := []Snapshot{
		snapForIDAt(t, "gc-stale", "2026-01-02T00:00:00Z"),
		snapForIDAt(t, "gc-tie", "2026-01-02T00:00:00Z"),
		snapForIDAt(t, "gc-newer", "2026-01-02T00:00:00Z"),
	}

	got := reconcileGuardedBDReport(report, incoming, destination)

	if !reflect.DeepEqual(got.Inserted, []string{"gc-new"}) {
		t.Fatalf("Inserted = %v, want [gc-new]", got.Inserted)
	}
	if !reflect.DeepEqual(got.Updated, []string{"gc-known-updated", "gc-newer"}) {
		t.Fatalf("Updated = %v", got.Updated)
	}
	if !reflect.DeepEqual(got.KeptLocal, []string{"gc-known-tie", "gc-tie"}) {
		t.Fatalf("KeptLocal = %v", got.KeptLocal)
	}
	if !reflect.DeepEqual(got.StaleSkipped, []string{"gc-known-stale", "gc-stale"}) {
		t.Fatalf("StaleSkipped = %v", got.StaleSkipped)
	}
	if !reflect.DeepEqual(report.Inserted, originalInserted) {
		t.Fatalf("input Inserted mutated to %v, want %v", report.Inserted, originalInserted)
	}
}

func TestBdStoreImportOptionsGateOnCapability(t *testing.T) {
	f := &fakeBdRunner{} // no capabilities advertised
	s := NewBdStore("/city", f.run)
	var calls []recordedImport
	s.SetBDImportRunner(recordingImportRunner(&calls, `{}`))
	snaps := []Snapshot{snapForID(t, "gc-1")}

	for _, opts := range []ImportOptions{
		{ConflictSkip: true},
		{AllowStaleIDs: []string{"gc-1"}},
	} {
		_, err := s.ImportBeadSnapshots(context.Background(), snaps, opts)
		if !errors.Is(err, ErrBdCapabilityMissing) {
			t.Fatalf("opts %+v: expected ErrBdCapabilityMissing, got %v", opts, err)
		}
	}
	if len(calls) != 0 {
		t.Fatalf("capability-gated import must not shell bd import, got %d calls", len(calls))
	}
}

func TestImportConflictSkipWithAllowStaleRejectedBothLegs(t *testing.T) {
	snaps := []Snapshot{snapForID(t, "gc-1")}
	opts := ImportOptions{ConflictSkip: true, AllowStaleIDs: []string{"gc-1"}}

	f := &fakeBdRunner{capabilities: []string{snapshotWorkspacePrefixMintCapability}}
	bd := NewBdStore("/city", f.run)
	if _, err := bd.ImportBeadSnapshots(context.Background(), snaps, opts); !errors.Is(err, ErrConflictSkipWithAllowStale) {
		t.Fatalf("bd leg = %v, want ErrConflictSkipWithAllowStale", err)
	}
	// Native leg goes through the same shared validate() (runGuardedUpsert).
	if _, err := runGuardedUpsert(context.Background(), snaps, opts, newMemImportBackend()); !errors.Is(err, ErrConflictSkipWithAllowStale) {
		t.Fatalf("shared leg = %v, want ErrConflictSkipWithAllowStale", err)
	}
}

func TestBdStoreImportConflictSkipParsesConflictSkippedIDs(t *testing.T) {
	f := &fakeBdRunner{capabilities: []string{snapshotWorkspacePrefixMintCapability}}
	s := NewBdStore("/city", f.run)
	var calls []recordedImport
	s.SetBDImportRunner(recordingImportRunner(&calls, `{"created":1,"ids":["gc-1"],"conflict_skipped_ids":["gc-2"]}`))
	snaps := []Snapshot{snapForID(t, "gc-1"), snapForID(t, "gc-2")}
	report, err := s.ImportBeadSnapshots(context.Background(), snaps, ImportOptions{ConflictSkip: true})
	if err != nil {
		t.Fatalf("conflict-skip import: %v", err)
	}
	if len(calls) != 1 || strings.Join(calls[0].args, " ") != "import - --json --conflict-skip" {
		t.Fatalf("conflict-skip args = %v", calls)
	}
	if !reflect.DeepEqual(report.ConflictSkipped, []string{"gc-2"}) {
		t.Fatalf("ConflictSkipped = %v, want [gc-2]", report.ConflictSkipped)
	}
	if !reflect.DeepEqual(report.Inserted, []string{"gc-1"}) {
		t.Fatalf("Inserted = %v, want [gc-1]", report.Inserted)
	}
}

func TestBdStoreImportAllowStaleConditionalForcedPass(t *testing.T) {
	// Destination: gc-1 is strictly newer than its source snapshot (must be
	// dropped/StaleSkipped); gc-2 ties (must be force-imported and reported
	// Updated).
	show := `[{"id":"gc-1","status":"open","updated_at":"2026-05-01T00:00:00Z"},
{"id":"gc-2","status":"open","updated_at":"2026-01-01T00:00:00Z"}]`
	f := &fakeBdRunner{capabilities: []string{snapshotWorkspacePrefixMintCapability}, show: show}
	s := NewBdStore("/city", f.run)
	var calls []recordedImport
	s.SetBDImportRunner(recordingImportRunner(&calls, `{"created":1,"ids":["gc-2"]}`))

	snaps := []Snapshot{
		snapForID(t, "gc-1"),
		snapForID(t, "gc-2"),
	}
	report, err := s.ImportBeadSnapshots(context.Background(), snaps, ImportOptions{AllowStaleIDs: []string{"gc-1", "gc-2"}})
	if err != nil {
		t.Fatalf("allow-stale import: %v", err)
	}
	// gc-1 dropped (dest newer), gc-2 force-imported.
	if !reflect.DeepEqual(report.StaleSkipped, []string{"gc-1"}) {
		t.Fatalf("StaleSkipped = %v, want [gc-1]", report.StaleSkipped)
	}
	if !reflect.DeepEqual(report.Updated, []string{"gc-2"}) {
		t.Fatalf("Updated = %v, want [gc-2]", report.Updated)
	}
	if len(calls) != 1 || !strings.Contains(strings.Join(calls[0].args, " "), "--allow-stale") {
		t.Fatalf("forced pass args = %v", calls)
	}
	if !strings.Contains(calls[0].stdin, `"gc-2"`) || strings.Contains(calls[0].stdin, `"gc-1"`) {
		t.Fatalf("forced pass stdin must contain only gc-2: %q", calls[0].stdin)
	}
}

func TestBdStoreGetBeadSnapshots(t *testing.T) {
	show := `[{"id":"gc-1","status":"closed","updated_at":"2026-01-01T00:00:00Z","closed_at":"2026-01-01T00:00:01Z"},
{"id":"gc-2","status":"open","updated_at":"2026-01-02T00:00:00Z"}]`
	f := &fakeBdRunner{show: show}
	s := NewBdStore("/city", f.run)
	snaps, err := s.GetBeadSnapshots(context.Background(), []string{"gc-1", "gc-2", "gc-absent"})
	if err != nil {
		t.Fatalf("GetBeadSnapshots: %v", err)
	}
	byID := map[string]Snapshot{}
	for _, sn := range snaps {
		byID[sn.ID()] = sn
	}
	if len(byID) != 2 {
		t.Fatalf("GetBeadSnapshots returned %d, want 2 (absent id omitted)", len(byID))
	}
	if byID["gc-1"].Status() != "closed" || byID["gc-1"].ClosedAt() == nil {
		t.Fatalf("gc-1 verify fields wrong: %+v", byID["gc-1"])
	}
}

func TestBdStoreGetBeadSnapshotsDelimitsDashPrefixedIDs(t *testing.T) {
	var got []string
	runner := func(_, name string, args ...string) ([]byte, error) {
		if name != "bd" {
			return nil, fmt.Errorf("command = %q, want bd", name)
		}
		got = append([]string(nil), args...)
		return []byte(`[]`), nil
	}
	s := NewBdStore("/city", runner)
	if _, err := s.GetBeadSnapshots(context.Background(), []string{"-cyca"}); err != nil {
		t.Fatalf("GetBeadSnapshots: %v", err)
	}
	want := []string{"show", "--json", "--", "-cyca"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bd argv = %v, want %v", got, want)
	}
}

func TestBdStoreGetDelimitsDashPrefixedID(t *testing.T) {
	var got []string
	runner := func(_, name string, args ...string) ([]byte, error) {
		if name != "bd" {
			return nil, fmt.Errorf("command = %q, want bd", name)
		}
		got = append([]string(nil), args...)
		return []byte(`[{"id":"-cyca","title":"dash","status":"open","issue_type":"task","created_at":"2026-01-01T00:00:00Z"}]`), nil
	}
	s := NewBdStore("/city", runner)
	if _, err := s.Get("-cyca"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	want := []string{"show", "--json", "--", "-cyca"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bd argv = %v, want %v", got, want)
	}
}

func TestBdStoreGetBeadSnapshotsAllMissingReturnsEmpty(t *testing.T) {
	runner := func(_, _ string, _ ...string) ([]byte, error) {
		return nil, fmt.Errorf("no issues found matching the provided IDs")
	}
	s := NewBdStore("/city", runner)
	snaps, err := s.GetBeadSnapshots(context.Background(), []string{"nope-1"})
	if err != nil {
		t.Fatalf("all-missing GetBeadSnapshots should not error: %v", err)
	}
	if len(snaps) != 0 {
		t.Fatalf("expected empty, got %v", snapIDs(snaps))
	}
}

func TestImportTimeoutScalesWithBatch(t *testing.T) {
	small := nativeImportDeadline(1)
	large := nativeImportDeadline(10000)
	if large <= small {
		t.Fatalf("import deadline must grow with batch size: small=%v large=%v", small, large)
	}
	if small < bdCommandTimeout {
		t.Fatalf("import deadline must be at least the flat floor")
	}
}

func TestUnwrapBDJSONEnvelope(t *testing.T) {
	enveloped := []byte(`{"schema_version":1,"data":{"created":2,"ids":["gc-1","gc-2"]}}`)
	var result bdImportResult
	if err := json.Unmarshal(extractJSON(unwrapBDJSONEnvelope(enveloped)), &result); err != nil {
		t.Fatalf("unwrap+parse: %v", err)
	}
	if len(result.IDs) != 2 {
		t.Fatalf("enveloped import result did not unwrap: %+v", result)
	}
	// A bare (unenveloped) result passes through unchanged.
	bare := []byte(`{"created":1,"ids":["gc-1"]}`)
	if string(unwrapBDJSONEnvelope(bare)) != string(bare) {
		t.Fatalf("bare result must pass through unchanged")
	}
}

func TestBdImportChildEnvStripsEnvelope(t *testing.T) {
	env := bdImportChildEnv(map[string]string{"BEADS_DIR": "/x/.beads", "BD_JSON_ENVELOPE": "1"})
	if !envContains(env, "BEADS_DIR=/x/.beads") {
		t.Fatalf("scoped override missing: %v", env)
	}
	if envHasKey(env, "BD_JSON_ENVELOPE") {
		t.Fatalf("BD_JSON_ENVELOPE must be stripped: %v", env)
	}
}

func TestBdStoreDirectExecPathsUseAbsoluteBdBin(t *testing.T) {
	ambientDir := t.TempDir()
	ambientBd := filepath.Join(ambientDir, "bd")
	if err := os.WriteFile(ambientBd, []byte(`#!/bin/sh
case "$1" in
  list) printf '[]' ;;
  import) printf '{"created":1,"ids":["ambient"]}' ;;
  purge) printf '{"purged_count":1}' ;;
esac
`), 0o755); err != nil {
		t.Fatal(err)
	}
	ambientOverride := filepath.Join(t.TempDir(), "bd")
	if err := os.WriteFile(ambientOverride, []byte("#!/bin/sh\nexit 9\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	pinnedDir := t.TempDir()
	pinnedBd := filepath.Join(pinnedDir, "bd")
	if err := os.WriteFile(pinnedBd, []byte(`#!/bin/sh
case "$1" in
  list) printf '[]' ;;
  import) printf '{"created":1,"ids":["pinned"]}' ;;
  purge) printf '{"purged_count":2}' ;;
esac
`), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", ambientDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BD_BIN", ambientOverride)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	unscoped := NewBdStore(t.TempDir(), nil)
	if err := unscoped.CredentialPreflight(ctx); err != nil {
		t.Fatalf("ambient BD_BIN must be ignored in favor of logical bd: %v", err)
	}

	store := NewBdStore(t.TempDir(), func(_, _ string, _ ...string) ([]byte, error) {
		return nil, errors.New("runner must not be used by direct exec paths")
	}, WithBdStoreEnv(map[string]string{"BD_BIN": pinnedBd}))

	if err := store.CredentialPreflight(ctx); err != nil {
		t.Fatalf("CredentialPreflight: %v", err)
	}

	imported, err := store.runBDImport(ctx, nil, "import", "-", "--json")
	if err != nil {
		t.Fatalf("runBDImport: %v", err)
	}
	if got := imported.IDs; !reflect.DeepEqual(got, []string{"pinned"}) {
		t.Fatalf("import IDs = %v, want pinned bd", got)
	}

	purged, err := store.Purge(filepath.Join(store.Dir(), ".beads"), false)
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if purged.Purged != 2 {
		t.Fatalf("purged = %d, want pinned bd result 2", purged.Purged)
	}
}

func TestParseSkippedDependency(t *testing.T) {
	issueID, dependsOnID := parseSkippedDependency("gc-1 -> gc-2: target not found")
	if issueID != "gc-1" || dependsOnID != "gc-2" {
		t.Fatalf("parseSkippedDependency = (%q, %q)", issueID, dependsOnID)
	}
}

func TestSQLiteStoreSnapshotUnsupported(t *testing.T) {
	store, err := OpenSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}
	if _, err := ExportBeadSnapshotsFrom(context.Background(), store, ExportOptions{}); !errors.Is(err, ErrExportUnsupported) {
		t.Fatalf("ExportBeadSnapshotsFrom = %v, want ErrExportUnsupported", err)
	}
	snaps := []Snapshot{snapForID(t, "gc-1")}
	if _, err := ImportBeadSnapshotsTo(context.Background(), store, snaps, ImportOptions{}); !errors.Is(err, ErrImportUnsupported) {
		t.Fatalf("ImportBeadSnapshotsTo = %v, want ErrImportUnsupported", err)
	}
	if _, err := GetBeadSnapshotsFrom(context.Background(), store, []string{"gc-1"}); !errors.Is(err, ErrFetchUnsupported) {
		t.Fatalf("GetBeadSnapshotsFrom = %v, want ErrFetchUnsupported", err)
	}
}
