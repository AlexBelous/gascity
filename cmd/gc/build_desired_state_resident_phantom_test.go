package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/runtime"
)

// TestControlDispatcherResidency_ReusesPhantomIdentityOwner is the production
// repro for the 96f1f2f11 live-gate failure (gc-0ychy). The preserved live
// dispatchers carried FULLY phantom (numbered "-1") pool identities from the
// prior collision churn — the supervisor logged "collapsing phantom pool
// identity for bead ... to <canonical>". canonicalSingletonResidency recognized
// owners only by exact-canonical identity (infoIdentifiesAsCanonical), so a
// fully-phantom bead was NOT recognized as the owner: residency skipped it, the
// min floor minted a colliding replacement, and the running phantom-named
// session was orphan-drained. The residency must recognize a phantom-slot owner
// (the same recognition the collapse uses) and REUSE the same bead.
func TestControlDispatcherResidency_ReusesPhantomIdentityOwner(t *testing.T) {
	store := beads.NewMemStore()
	cfg := residentControlDispatcherCfg(nil)
	applyControlDispatcherResidency(cfg) // floor -> min=1, resident
	phantom := residentControlDispatcherTemplate + "-1"
	created, err := store.Create(beads.Bead{
		Title:  phantom,
		Type:   sessionBeadType,
		Status: "open",
		Labels: []string{sessionBeadLabel, "agent:" + phantom},
		Metadata: map[string]string{
			"template":                residentControlDispatcherTemplate,
			"agent_name":              phantom, // fully phantom identity
			"alias":                   phantom,
			"canonical_instance_name": phantom,
			"state":                   "active",
			poolManagedMetadataKey:    boolMetadata(true),
			"pool_slot":               "1",
			"session_origin":          "ephemeral",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetMetadata(created.ID, "session_name", PoolSessionName(residentControlDispatcherTemplate, created.ID)); err != nil {
		t.Fatalf("set session_name: %v", err)
	}

	snap, err := loadSessionBeadSnapshot(store)
	if err != nil {
		t.Fatalf("snap: %v", err)
	}
	states := ComputePoolDesiredStates(cfg, nil, snap.OpenInfos(), map[string]int{residentControlDispatcherTemplate: 1})
	var newReqs, resumeReqs int
	var resumeBead string
	for _, s := range states {
		if s.Template != residentControlDispatcherTemplate {
			continue
		}
		for _, r := range s.Requests {
			switch r.Tier {
			case "new":
				newReqs++
			case "resume", "wake-known-identity":
				resumeReqs++
				resumeBead = r.SessionBeadID
			}
		}
	}
	if newReqs != 0 {
		t.Fatalf("phantom-identity active owner: %d new(mint) requests, want 0 — residency must recognize a phantom-slot owner and reuse it, not mint a colliding replacement that orphan-drains the running session (gc-0ychy)", newReqs)
	}
	if resumeReqs != 1 || resumeBead != created.ID {
		t.Fatalf("phantom-identity active owner: resume=%d bead=%q, want 1 resume of %q — the SAME phantom-identity bead must be reused in place (gc-0ychy)", resumeReqs, resumeBead, created.ID)
	}

	// End-to-end at the production boundary: the running phantom-named session
	// must be RETAINED in the desired state (not orphan-drained), and no colliding
	// replacement desired. The collapse may normalize the identity, but the
	// running session_name must survive.
	runningSessionName := PoolSessionName(residentControlDispatcherTemplate, created.ID)
	var stderr bytes.Buffer
	dsResult := buildDesiredState("test-city", t.TempDir(), time.Now().UTC(), cfg, runtime.NewFake(), store, &stderr)
	desiredNames := make([]string, 0, len(dsResult.State))
	for _, tp := range dsResult.State {
		desiredNames = append(desiredNames, tp.SessionName)
	}
	retained := false
	for _, n := range desiredNames {
		if n == runningSessionName {
			retained = true
		}
	}
	if !retained {
		t.Fatalf("running phantom-identity dispatcher %q NOT retained in desired state (desired=%v) — the resident occupant was orphaned/replaced (gc-0ychy)", runningSessionName, desiredNames)
	}
	if len(dsResult.State) != 1 {
		t.Fatalf("desired sessions = %d, want 1 (no colliding replacement) — %v", len(dsResult.State), desiredNames)
	}
}
