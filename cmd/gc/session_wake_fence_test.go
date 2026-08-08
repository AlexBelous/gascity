package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/clock"
	"github.com/gastownhall/gascity/internal/rollout/gate"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// newPreWakeFenceStore opens a memstore stamped with the given conditional-writes
// mode through the real factory, so ResolveConditionalWriter sees the same shape
// production sees.
func newPreWakeFenceStore(t *testing.T, mode gate.Mode) beads.Store {
	t.Helper()
	result, err := beads.OpenStoreAtForCity(context.Background(), beads.StoreOpenOptions{
		ScopeRoot:         t.TempDir(),
		Provider:          "file",
		ConditionalWrites: mode,
		OpenFileStore:     func() (beads.Store, error) { return beads.NewMemStore(), nil },
	})
	if err != nil {
		t.Fatalf("OpenStoreAtForCity: %v", err)
	}
	return result.Store
}

// createPreWakeFenceSession persists one asleep manual session bead carrying a
// pre-wake incarnation token and returns its id.
func createPreWakeFenceSession(t *testing.T, store beads.Store, preWakeToken string) string {
	t.Helper()
	id, err := sessionFrontDoor(store).CreateSession(sessionpkg.CreateSpec{
		Title:     "worker",
		AgentName: "worker",
		Metadata: map[string]string{
			"template":       "worker",
			"session_name":   "worker-adhoc-fence",
			"state":          string(sessionpkg.StateAsleep),
			"generation":     "1",
			"instance_token": preWakeToken,
			"session_origin": "manual",
		},
	})
	if err != nil {
		t.Fatalf("create session bead: %v", err)
	}
	return id
}

// TestPreWakeCommitFenceRefusesSupersededRotation is the ga-l1j53 P1 red. Two
// writers converge on the SHARED start wave for one row — a controller tick and
// a second `gc start` process, which the in-process per-session mutation lock
// cannot serialize. Both complete their freshness re-read before either commits,
// so both hold the same pre-wake revision. Unfenced, the second re-rotates on top
// of the first: the durable row ends on a THIRD token that names neither the
// pre-wake incarnation nor the incarnation whose provider start actually ran, and
// every later token-fenced operation is aimed at a dead incarnation.
func TestPreWakeCommitFenceRefusesSupersededRotation(t *testing.T) {
	store := newPreWakeFenceStore(t, gate.Require)
	const preWakeToken = "90d0051f7d88b0786e2edc2cab2e19da"
	id := createPreWakeFenceSession(t, store, preWakeToken)
	sessFront := sessionFrontDoor(store)
	clk := clock.Real{}

	// Both entrants re-read before either commits (the cross-process ordering).
	winnerInfo, winnerRead, err := sessFront.GetPersistedResponse(id)
	if err != nil {
		t.Fatalf("winner re-read: %v", err)
	}
	loserInfo, loserRead, err := sessFront.GetPersistedResponse(id)
	if err != nil {
		t.Fatalf("loser re-read: %v", err)
	}
	if winnerRead.Revision <= 0 || winnerRead.Revision != loserRead.Revision {
		t.Fatalf("test premise: revisions = %d/%d, want one shared positive revision", winnerRead.Revision, loserRead.Revision)
	}

	_, winnerToken, _, err := preWakeCommit(winnerInfo, winnerRead.Revision, sessFront, clk)
	if err != nil {
		t.Fatalf("winning pre-wake commit: %v", err)
	}

	_, loserToken, _, err := preWakeCommit(loserInfo, loserRead.Revision, sessFront, clk)
	if !errors.Is(err, errPreWakeSuperseded) {
		t.Fatalf("superseded pre-wake commit = token %q err %v, want errPreWakeSuperseded", loserToken, err)
	}

	latest, err := sessFront.Get(id)
	if err != nil {
		t.Fatalf("read row after both commits: %v", err)
	}
	durable := strings.TrimSpace(latest.InstanceToken)
	if durable != winnerToken {
		t.Fatalf("durable instance token = %q, want the winning rotation %q (pre_wake=%q loser=%q): the row names an incarnation with no runtime behind it",
			durable, winnerToken, preWakeToken, loserToken)
	}
}

// TestPreWakeCommitFenceKeepsSingleWriterUnchanged pins that the fence is
// invisible to the ordinary single-writer start: a commit on the revision its own
// re-read carried lands and rotates the row.
func TestPreWakeCommitFenceKeepsSingleWriterUnchanged(t *testing.T) {
	store := newPreWakeFenceStore(t, gate.Require)
	const preWakeToken = "3f6dc558afe524273fe39f0a596c1f99"
	id := createPreWakeFenceSession(t, store, preWakeToken)
	sessFront := sessionFrontDoor(store)

	info, read, err := sessFront.GetPersistedResponse(id)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	gen, token, fold, err := preWakeCommit(info, read.Revision, sessFront, clock.Real{})
	if err != nil {
		t.Fatalf("single-writer pre-wake commit: %v", err)
	}
	if gen != 2 || token == "" || token == preWakeToken || len(fold) == 0 {
		t.Fatalf("single-writer pre-wake commit = gen %d token %q fold %d, want a fresh rotation", gen, token, len(fold))
	}
	latest, err := sessFront.Get(id)
	if err != nil {
		t.Fatalf("read row after commit: %v", err)
	}
	if strings.TrimSpace(latest.InstanceToken) != token || latest.MetadataState != string(sessionpkg.StateCreating) {
		t.Fatalf("durable row = token %q state %q, want %q/creating", latest.InstanceToken, latest.MetadataState, token)
	}
}

// TestPreWakeCommitFenceDegradesWithConditionalWritesOff keeps the ga-797vy F1
// precedent: a deployment without a revision contract has nothing to fence on, so
// the unconditional write remains — a start must never be blocked there.
func TestPreWakeCommitFenceDegradesWithConditionalWritesOff(t *testing.T) {
	store := newPreWakeFenceStore(t, gate.Off)
	const preWakeToken = "8417bd5a2bc47195501b75190c7bd3b4"
	id := createPreWakeFenceSession(t, store, preWakeToken)
	sessFront := sessionFrontDoor(store)

	info, read, err := sessFront.GetPersistedResponse(id)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	// A deliberately stale revision: with conditional writes off it must not be
	// consulted at all.
	if _, token, _, err := preWakeCommit(info, read.Revision-1, sessFront, clock.Real{}); err != nil || token == "" {
		t.Fatalf("conditional-writes-off pre-wake commit = token %q err %v, want the unconditional write", token, err)
	}
}
