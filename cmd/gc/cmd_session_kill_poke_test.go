package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// newKillPokeSession stands up a city, store, and fake runtime for an awake
// canonical configured named session, returning the store and the session
// bead. The fake provider is wired through buildSessionProviderByName so
// cmdSessionKill resolves a real handle and reaches the asleep-sync + handoff
// tail.
func newKillPokeSession(t *testing.T, identity, mode string) (beads.Store, beads.Bead, string) {
	t.Helper()
	t.Setenv("GC_BEADS", "file")
	t.Setenv("GC_SESSION", "fake")

	cityDir := shortSocketTempDir(t, "gc-kill-poke-")
	t.Setenv("GC_CITY", cityDir)
	writeGenericNamedSessionCityTOML(t, cityDir)
	configPath := filepath.Join(cityDir, "city.toml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile(city.toml): %v", err)
	}
	data = append(data, []byte("mode = \""+mode+"\"\n")...)
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile(city.toml): %v", err)
	}
	cfg, err := loadCityConfig(cityDir, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("loadCityConfig: %v", err)
	}
	spec, ok := findNamedSessionSpec(cfg, cfg.EffectiveCityName(), identity)
	if !ok {
		t.Fatalf("findNamedSessionSpec(%q) = not found", identity)
	}
	if spec.Mode != mode {
		t.Fatalf("named session mode = %q, want %q", spec.Mode, mode)
	}
	sessionName := spec.SessionName

	fakeProvider := runtime.NewFake()
	oldBuild := buildSessionProviderByName
	buildSessionProviderByName = func(*config.City, string, config.SessionConfig, string, string) (runtime.Provider, error) {
		return fakeProvider, nil
	}
	t.Cleanup(func() { buildSessionProviderByName = oldBuild })

	store, err := openCityStoreAt(cityDir)
	if err != nil {
		t.Fatalf("openCityStoreAt: %v", err)
	}
	bead, err := store.Create(beads.Bead{
		Title:  "named session",
		Type:   sessionpkg.BeadType,
		Labels: []string{sessionpkg.LabelSession, "template:worker"},
		Metadata: map[string]string{
			"alias":                      identity,
			"template":                   spec.Agent.QualifiedName(),
			"agent_name":                 spec.Agent.QualifiedName(),
			"session_name":               sessionName,
			"state":                      "awake",
			"session_origin":             "named",
			namedSessionMetadataKey:      "true",
			namedSessionIdentityMetadata: identity,
			namedSessionModeMetadata:     mode,
		},
	})
	if err != nil {
		t.Fatalf("store.Create(session bead): %v", err)
	}
	if err := fakeProvider.Start(context.Background(), sessionName, runtime.Config{Command: "true"}); err != nil {
		t.Fatalf("fakeProvider.Start: %v", err)
	}
	if err := fakeProvider.SetMeta(sessionName, "GC_SESSION_ID", bead.ID); err != nil {
		t.Fatalf("SetMeta(GC_SESSION_ID): %v", err)
	}
	return store, bead, cityDir
}

// TestCmdSessionKill_AlwaysNamedPersistsWakeBeforeExactHandoff proves an
// always-named kill durably records both the killed sleep transition and an
// explicit wake before handing the exact canonical bead ID to the keyed start
// controller.
func TestCmdSessionKill_AlwaysNamedPersistsWakeBeforeExactHandoff(t *testing.T) {
	const identity = "session-a"
	store, bead, cityDir := newKillPokeSession(t, identity, "always")

	calls := 0
	var gotCityPath, gotSessionID string
	var metadataAtHandoff map[string]string
	old := sessionKillExactStartController
	sessionKillExactStartController = func(cityPath, sessionID string) error {
		calls++
		gotCityPath = cityPath
		gotSessionID = sessionID
		if b, getErr := store.Get(sessionID); getErr == nil {
			metadataAtHandoff = b.Metadata
		}
		return nil
	}
	t.Cleanup(func() { sessionKillExactStartController = old })

	var stdout, stderr bytes.Buffer
	if code := cmdSessionKill([]string{identity}, &stdout, &stderr); code != 0 {
		t.Fatalf("cmdSessionKill = %d, want 0; stderr=%s", code, stderr.String())
	}

	if calls != 1 {
		t.Fatalf("poke called %d times, want exactly 1", calls)
	}
	if gotCityPath != cityDir {
		t.Errorf("handoff cityPath = %q, want %q", gotCityPath, cityDir)
	}
	if gotSessionID != bead.ID {
		t.Errorf("handoff session ID = %q, want canonical bead %q", gotSessionID, bead.ID)
	}
	if got := metadataAtHandoff["state"]; got != string(sessionpkg.StateAsleep) {
		t.Errorf("state at handoff = %q, want %q", got, sessionpkg.StateAsleep)
	}
	if got := metadataAtHandoff["sleep_reason"]; got != "killed" {
		t.Errorf("sleep_reason at handoff = %q, want killed", got)
	}
	if got := metadataAtHandoff["wake_request"]; got != string(sessionpkg.WakeCauseExplicit) {
		t.Errorf("wake_request at handoff = %q, want %q", got, sessionpkg.WakeCauseExplicit)
	}
	if metadataAtHandoff["wake_requested_at"] == "" {
		t.Error("wake_requested_at at handoff is empty")
	}
	if metadataAtHandoff["synced_at"] == "" {
		t.Error("synced_at at handoff is empty")
	}
}

// TestCmdSessionKill_OnDemandNamedStaysAsleepWithoutExactHandoff proves an
// on-demand configured session retains the killed sleep transition without a
// durable wake request or exact-key start hint.
func TestCmdSessionKill_OnDemandNamedStaysAsleepWithoutExactHandoff(t *testing.T) {
	const identity = "session-a"
	store, bead, _ := newKillPokeSession(t, identity, "on_demand")

	exactCalls := 0
	oldExact := sessionKillExactStartController
	sessionKillExactStartController = func(string, string) error {
		exactCalls++
		return nil
	}
	t.Cleanup(func() { sessionKillExactStartController = oldExact })
	genericCalls := 0
	oldGeneric := sessionKillPokeController
	sessionKillPokeController = func(string) error {
		genericCalls++
		return nil
	}
	t.Cleanup(func() { sessionKillPokeController = oldGeneric })

	var stdout, stderr bytes.Buffer
	if code := cmdSessionKill([]string{identity}, &stdout, &stderr); code != 0 {
		t.Fatalf("cmdSessionKill = %d, want 0; stderr=%s", code, stderr.String())
	}
	if exactCalls != 0 {
		t.Fatalf("exact handoff called %d times, want 0", exactCalls)
	}
	if genericCalls != 1 {
		t.Fatalf("generic poke called %d times, want 1", genericCalls)
	}
	updated, err := store.Get(bead.ID)
	if err != nil {
		t.Fatalf("store.Get(%q): %v", bead.ID, err)
	}
	if got := updated.Metadata["state"]; got != string(sessionpkg.StateAsleep) {
		t.Errorf("state = %q, want %q", got, sessionpkg.StateAsleep)
	}
	if got := updated.Metadata["sleep_reason"]; got != "killed" {
		t.Errorf("sleep_reason = %q, want killed", got)
	}
	if got := updated.Metadata["wake_request"]; got != "" {
		t.Errorf("wake_request = %q, want empty", got)
	}
	if got := updated.Metadata["wake_requested_at"]; got != "" {
		t.Errorf("wake_requested_at = %q, want empty", got)
	}
}

// TestCmdSessionKill_ExactHandoffFailureIsNonFatal pins the best-effort
// contract: an exact start-handoff failure must not fail the kill after the
// durable sleep and wake intent have been persisted.
func TestCmdSessionKill_ExactHandoffFailureIsNonFatal(t *testing.T) {
	const identity = "session-a"
	_, _, _ = newKillPokeSession(t, identity, "always")

	old := sessionKillExactStartController
	sessionKillExactStartController = func(string, string) error { return errors.New("dial failed") }
	t.Cleanup(func() { sessionKillExactStartController = old })

	var stdout, stderr bytes.Buffer
	if code := cmdSessionKill([]string{identity}, &stdout, &stderr); code != 0 {
		t.Fatalf("cmdSessionKill = %d, want 0 (handoff failure is best-effort); stderr=%s", code, stderr.String())
	}
}
