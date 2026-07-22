package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	messagingdb "github.com/gastownhall/gascity/internal/classdb/messaging"
	"github.com/gastownhall/gascity/internal/extmsg"
)

// reapStaleExtmsgBindings reconciles external-message conversation bindings
// against live session identity on each reconciler tick. A binding stores the
// session bead ID it was created against; when that session crashes and
// respawns under the same name it gets a fresh bead ID, leaving the binding
// pointing at a dead session so inbound triage silently drops and a fresh bind
// is rejected as a conflict. The reaper re-points bindings at the respawned
// session and clears bindings whose session is gone.
//
// It runs after session beads have been synced for the tick so a respawned
// session's replacement bead is already visible. Errors are logged and
// swallowed so a binding-store hiccup never stalls the reconciler loop.
//
// The two typed handles carry the reaper's two-class nature: binding records
// are MESSAGING-class beads (msgStore, mutated), while session liveness is
// resolved from SESSION-class beads (sessionStore, read-only). Both collapse
// to the work store on a single-store city.
func reapStaleExtmsgBindings(ctx context.Context, class *messagingdb.Store, msgStore beads.MailStore, sessionStore beads.SessionStore, now time.Time, stderr io.Writer) {
	if stderr == nil {
		stderr = io.Discard
	}
	var stats extmsg.BindingReapStats
	var err error
	if class != nil {
		stats, err = extmsg.ReapStaleBindingsWithBackend(ctx, class, sessionStore.Store, now)
	} else {
		if msgStore.Store == nil {
			return
		}
		stats, err = extmsg.ReapStaleBindings(ctx, msgStore.Store, sessionStore.Store, now)
	}
	if err != nil {
		fmt.Fprintf(stderr, "session reconciler: reaping stale extmsg bindings: %v\n", err) //nolint:errcheck
		return
	}
	if stats.Reassigned > 0 || stats.Cleared > 0 {
		fmt.Fprintf(stderr, "session reconciler: extmsg bindings reaped (reassigned=%d cleared=%d scanned=%d)\n", //nolint:errcheck
			stats.Reassigned, stats.Cleared, stats.Scanned)
	}
}

// reapStaleExtmsgParticipants reconciles external-message group participants
// against live session identity on each reconciler tick — the participant-side
// companion to reapStaleExtmsgBindings. Group-participant routing self-heals at
// read time, but the group-owned transcript membership (keyed by session ID)
// does not, and a binding-less group participant whose session respawns is
// reached by no other backstop, so without this sweep its membership would stay
// stranded on the retired session bead. It runs on the same tick and after
// session beads have been synced. Errors are logged and swallowed so a
// participant-store hiccup never stalls the reconciler loop.
//
// Same two-class handle split as reapStaleExtmsgBindings: participant records
// are MESSAGING-class (msgStore, mutated), liveness reads are SESSION-class
// (sessionStore).
func reapStaleExtmsgParticipants(ctx context.Context, class *messagingdb.Store, msgStore beads.MailStore, sessionStore beads.SessionStore, stderr io.Writer) {
	if stderr == nil {
		stderr = io.Discard
	}
	var stats extmsg.ParticipantReapStats
	var err error
	if class != nil {
		stats, err = extmsg.ReapStaleParticipantsWithBackend(ctx, class, sessionStore.Store)
	} else {
		if msgStore.Store == nil {
			return
		}
		stats, err = extmsg.ReapStaleParticipants(ctx, msgStore.Store, sessionStore.Store)
	}
	if err != nil {
		fmt.Fprintf(stderr, "session reconciler: reaping stale extmsg participants: %v\n", err) //nolint:errcheck
		return
	}
	if stats.Reassigned > 0 {
		fmt.Fprintf(stderr, "session reconciler: extmsg participants reaped (reassigned=%d scanned=%d)\n", //nolint:errcheck
			stats.Reassigned, stats.Scanned)
	}
}
