package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
)

// TestCheckDeclinedBeadRewakeOnWake pins ga-um61n9's acceptance criterion 3:
// the call-site wiring that resolves an about-to-wake session's assigned
// work bead from this tick's assigned-work snapshot and runs the
// declined-bead re-wake detector (gm-6a5dt) against it. Exercised with a
// fake assigned-work-bead map and an injected fake lookup, mirroring what a
// real tick's assignedWorkBeadsByID index and noteAuthorLookup would look
// like -- no real bd subprocess or session_reconciler plumbing required.
// checkDeclinedBeadRewakeOnWake does not exist yet -- this file fails to
// compile until the GREEN step adds it.
func TestCheckDeclinedBeadRewakeOnWake(t *testing.T) {
	matchingBead := beads.Bead{
		ID:       "ga-assigned1",
		Metadata: beads.StringMap{beadmeta.RoutedToMetadataKey: "gascity/builder"},
	}

	tests := []struct {
		name         string
		assignedByID map[string]beads.Bead
		beadID       string
		lookupActor  string
		lookupErr    error
		wantLog      bool
	}{
		{
			name:         "resolved bead with matching note author logs the loop signature",
			assignedByID: map[string]beads.Bead{"ga-assigned1": matchingBead},
			beadID:       "ga-assigned1",
			lookupActor:  "gascity/builder",
			wantLog:      true,
		},
		{
			name:         "empty AssignedWorkBeadID is a no-op",
			assignedByID: map[string]beads.Bead{"ga-assigned1": matchingBead},
			beadID:       "",
			lookupActor:  "gascity/builder",
			wantLog:      false,
		},
		{
			name:         "AssignedWorkBeadID not present in this tick's snapshot is a no-op",
			assignedByID: map[string]beads.Bead{},
			beadID:       "ga-missing",
			lookupActor:  "gascity/builder",
			wantLog:      false,
		},
		{
			name:         "resolved bead with non-matching note author stays silent",
			assignedByID: map[string]beads.Bead{"ga-assigned1": matchingBead},
			beadID:       "ga-assigned1",
			lookupActor:  "gascity/reviewer",
			wantLog:      false,
		},
		{
			name:         "lookup error stays silent, never surfaced as a wake failure",
			assignedByID: map[string]beads.Bead{"ga-assigned1": matchingBead},
			beadID:       "ga-assigned1",
			lookupErr:    fmt.Errorf("bd history: subprocess failed"),
			wantLog:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logged []string
			lookup := func(_ string) (string, error) { return tt.lookupActor, tt.lookupErr }
			logf := func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }

			checkDeclinedBeadRewakeOnWake(tt.assignedByID, tt.beadID, lookup, logf)

			if tt.wantLog && len(logged) == 0 {
				t.Fatalf("expected a log line, got none")
			}
			if !tt.wantLog && len(logged) != 0 {
				t.Fatalf("expected no log line, got %v", logged)
			}
		})
	}
}

// TestDispatchDeclinedBeadRewakeCheckDoesNotBlock pins the round-1 review
// fix (ga-t7ux9m): the wake path must never wait on the bd-history
// subprocess behind noteAuthorLookup, since noteAuthorLookupWithRunner's
// call carries no bounded context of its own and falls through to the
// default 120s bdCommandTimeoutFor ceiling, inline in a sequential loop over
// every wake target (session_reconciler.go:3586). A blocked lookup must
// delay nothing but its own log line.
func TestDispatchDeclinedBeadRewakeCheckDoesNotBlock(t *testing.T) {
	release := make(chan struct{})
	logged := make(chan string, 1)

	lookup := func(_ string) (string, error) { //nolint:unparam // noteAuthorLookup's error return; this test only exercises the non-blocking path, error handling is covered by TestCheckDeclinedBeadRewakeOnWake
		<-release // stays blocked until the test explicitly releases it
		return "gascity/builder", nil
	}
	logf := func(format string, args ...any) {
		logged <- fmt.Sprintf(format, args...)
	}

	assignedByID := map[string]beads.Bead{
		"ga-assigned1": {ID: "ga-assigned1", Metadata: beads.StringMap{beadmeta.RoutedToMetadataKey: "gascity/builder"}},
	}

	returned := make(chan struct{})
	go func() {
		dispatchDeclinedBeadRewakeCheck(assignedByID, "ga-assigned1", lookup, logf)
		close(returned)
	}()

	select {
	case <-returned:
	case <-time.After(1 * time.Second):
		t.Fatal("dispatchDeclinedBeadRewakeCheck blocked on the lookup instead of returning immediately")
	}

	select {
	case <-logged:
		t.Fatal("log line fired before the lookup was released -- lookup should still be blocked")
	default:
	}

	close(release)

	select {
	case msg := <-logged:
		if !strings.Contains(msg, "ga-assigned1") {
			t.Fatalf("expected the deferred log line to reference the bead ID, got: %s", msg)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("expected the deferred log line after releasing the lookup, got none")
	}
}
