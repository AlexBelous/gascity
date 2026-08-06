package main

import (
	"errors"
	"strings"
	"testing"
)

// TestNoteAuthorLookupWithRunner expresses ga-um61n9's acceptance criterion 4:
// the real bd-history-backed noteAuthorLookup, exercised through an injected
// fake beads.CommandRunner so no real bd subprocess or city is needed.
// noteAuthorLookupWithRunner does not exist yet -- this file fails to
// compile until the GREEN step adds it.
func TestNoteAuthorLookupWithRunner(t *testing.T) {
	tests := []struct {
		name      string
		runnerOut []byte
		runnerErr error
		wantActor string
		wantErr   bool
	}{
		{
			name: "matching actor found returns the notes-changing event's actor",
			runnerOut: []byte(`[
				{"event_type":"updated","actor":"reviewer-abc","old_value":"{\"notes\":\"old\"}","new_value":"{\"notes\":\"new\"}"},
				{"event_type":"claimed","actor":"reviewer-abc","old_value":"{}","new_value":"{}"}
			]`),
			wantActor: "reviewer-abc",
		},
		{
			name: "no notes-changing event yet returns empty actor and no error",
			runnerOut: []byte(`[
				{"event_type":"created","actor":"builder-abc","old_value":"","new_value":""},
				{"event_type":"label_added","actor":"builder-abc","old_value":"","new_value":""}
			]`),
			wantActor: "",
		},
		{
			name: "updated event whose notes did not change is not a match",
			runnerOut: []byte(`[
				{"event_type":"updated","actor":"controller","old_value":"{\"notes\":\"same\",\"status\":\"open\"}","new_value":"{\"status\":\"in_progress\"}"}
			]`),
			wantActor: "",
		},
		{
			name:      "malformed event JSON is a lookup error, not a match",
			runnerOut: []byte(`not valid json`),
			wantErr:   true,
		},
		{
			name:      "subprocess error is a lookup error, not a match",
			runnerErr: errors.New("bd: exit status 1"),
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := func(_, _ string, _ ...string) ([]byte, error) {
				return tt.runnerOut, tt.runnerErr
			}
			lookup := noteAuthorLookupWithRunner(runner, "/fake/city")

			actor, err := lookup("ga-test")

			if tt.wantErr && err == nil {
				t.Fatalf("expected an error, got nil (actor=%q)", actor)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if actor != tt.wantActor {
				t.Fatalf("actor = %q, want %q", actor, tt.wantActor)
			}
		})
	}
}

// TestNoteAuthorLookupWithRunnerPassesBDHistoryArgs pins the exact
// subprocess invocation shape the bead description specifies: "history",
// beadID, "--events", "--json" -- via bdCommandRunnerForCity's own
// established (dir, name, args...) call shape (mirrors
// preflightBDContextReader in beads_preflight_checker.go).
func TestNoteAuthorLookupWithRunnerPassesBDHistoryArgs(t *testing.T) {
	var gotDir, gotName string
	var gotArgs []string
	runner := func(dir, name string, args ...string) ([]byte, error) {
		gotDir, gotName, gotArgs = dir, name, args
		return []byte(`[]`), nil
	}
	lookup := noteAuthorLookupWithRunner(runner, "/fake/city")

	if _, err := lookup("ga-abc123"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotDir != "/fake/city" {
		t.Errorf("dir = %q, want %q", gotDir, "/fake/city")
	}
	if gotName != "bd" {
		t.Errorf("name = %q, want %q", gotName, "bd")
	}
	wantArgs := "history ga-abc123 --events --json"
	if strings.Join(gotArgs, " ") != wantArgs {
		t.Errorf("args = %v, want %q", gotArgs, wantArgs)
	}
}
