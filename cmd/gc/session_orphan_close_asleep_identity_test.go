package main

import (
	"errors"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// TestExactOrphanAsleepDeadRowFreesPoolIdentity is the win3 field scenario
// (2026-08-23), pinned end to end: a pool slot goes ASLEEP, its runtime dies,
// and the row keeps reserving the identity-derived session_name the allocator
// needs back. The allocator cannot mint a sibling name — the name is a function
// of the slot's identity — so it refuses the create with
// errPoolSessionNameUnavailable and the slot stalls
// (build_desired_state.go:2899-2905). The only way out is for the holder to be
// closed.
//
// The point of the test is WHICH family closes it. This row is asleep, so
// D-SLEEP, D-DEADLINE, D-STALL and D-ZOMBIE all decline it on detectorBeadAwake.
// D-ORPHAN deliberately has no awake rung: its condition is undesiredness plus
// proven runtime absence, and a stalled create publishes no desired entry for
// the name (build_desired_state.go:2909 skips the item), so the holder IS
// undesired and D-ORPHAN owns it. This test is the assertion that the awake-only
// gates on its siblings never grow onto this family.
//
// The second half is the half that actually mattered in the field: closing the
// row is not the same as freeing the identity. The release rung in
// names.go:381-385 fires only for a closed row that is BOTH pool_managed and
// session_origin=ephemeral, so a close that leaves either marker off retires the
// runtime and strands the name anyway.
func TestExactOrphanAsleepDeadRowFreesPoolIdentity(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents: []config.Agent{{
			Name: "worker", StartCommand: "true",
			MinActiveSessions: intPtr(0), MaxActiveSessions: intPtr(3),
		}},
	}
	provider := &deadRuntimeProvider{Fake: env.sp}

	// The holder: a pool slot that went to sleep for a reason the pool
	// clean-close arm's allowlist does not carry (isPoolSessionSlotFreeableInfo,
	// session_state_helpers.go:96-102), so nothing but undesiredness can reap it.
	holder := env.createSessionBead("worker-1", "worker")
	env.setSessionMetadata(&holder, map[string]string{
		"state":                "asleep",
		"sleep_reason":         "no-wake-reason",
		"session_origin":       "ephemeral",
		"pool_slot":            "1",
		poolManagedMetadataKey: boolMetadata(true),
	})

	// Symptom first: the allocator cannot have the identity back while the row
	// is open. This is the "session name already exists (skipping)" refusal the
	// operator saw 14,011 times.
	if err := sessionpkg.EnsureSessionNameAvailableWithConfigForOwner(
		env.store, env.cfg, "worker-1", "", "worker"); !errors.Is(err, sessionpkg.ErrSessionNameExists) {
		t.Fatalf("name availability with the asleep holder open = %v, want ErrSessionNameExists", err)
	}

	info, response, err := getAuthoritativeSessionStartPersistedRecord(env.store, holder.ID)
	if err != nil {
		t.Fatalf("authoritative read: %v", err)
	}
	if info.MetadataState != "asleep" {
		t.Fatalf("fixture state = %q, want asleep", info.MetadataState)
	}

	// D-ORPHAN must claim an asleep row. If a future awake rung lands on this
	// family's guard, this is the assertion that fails.
	if reason := exactSessionOrphanCloseCandidate(
		newExactOrphanCloseParams(env, provider, map[string]bool{}), info, response, env.clk,
	); reason != "orphaned" {
		t.Fatalf("D-ORPHAN candidacy for an undesired asleep dead row = %q, want \"orphaned\"", reason)
	}

	params := newExactOrphanCloseParams(env, provider, map[string]bool{})
	handled, owner, err := reconcileExactSessionDetectorFamily(
		t.Context(), orphanCloseAdmission(holder.ID), params, info, response, env.clk)
	if !handled {
		t.Fatal("no keyed family claimed the asleep dead identity holder")
	}
	if err != nil || owner != exactSessionStartKeyedOwner {
		t.Fatalf("handler returned owner=%v err=%v, want keyed ownership and no error", owner, err)
	}

	stored, err := env.store.Get(holder.ID)
	if err != nil {
		t.Fatalf("read closed row: %v", err)
	}
	if stored.Status != "closed" {
		t.Fatalf("holder status = %q, want closed", stored.Status)
	}

	// The payoff rung: the identity is actually reusable again.
	if err := sessionpkg.EnsureSessionNameAvailableWithConfigForOwner(
		env.store, env.cfg, "worker-1", "", "worker"); err != nil {
		t.Fatalf("name availability after the holder closed = %v, want the identity free", err)
	}
}

// TestExactOrphanAsleepDeadRowParksOnUnprovenAbsence is the other half of the
// win3 diagnosis: the family owns the row, but it will not act on an
// observation that did not complete. This is the ga-f7v2ft.194 / ga-lp5w6
// mechanism — a host whose /proc sweep could not be finished reports
// Complete=false, and every destructive arm fails closed, so the identity stays
// held for as long as the proof is poisoned. It is a REFUSAL, not a coverage
// gap: the condition is level-triggered and the close lands on the first sweep
// that can finish its proof.
func TestExactOrphanAsleepDeadRowParksOnUnprovenAbsence(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents:    []config.Agent{{Name: "worker", StartCommand: "true"}},
	}
	provider := &deadRuntimeProvider{Fake: env.sp, incomplete: true}

	holder := env.createSessionBead("worker-1", "worker")
	env.setSessionMetadata(&holder, map[string]string{
		"state":                "asleep",
		"session_origin":       "ephemeral",
		"pool_slot":            "1",
		poolManagedMetadataKey: boolMetadata(true),
	})

	info, response, err := getAuthoritativeSessionStartPersistedRecord(env.store, holder.ID)
	if err != nil {
		t.Fatalf("authoritative read: %v", err)
	}
	params := newExactOrphanCloseParams(env, provider, map[string]bool{})
	if _, _, err := reconcileExactSessionDetectorFamily(
		t.Context(), orphanCloseAdmission(holder.ID), params, info, response, env.clk); err == nil {
		t.Fatal("an incomplete liveness observation closed the row; absence was assumed, not proven")
	}
	stored, err := env.store.Get(holder.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status == "closed" {
		t.Fatal("the row was closed behind an unproven absence")
	}
	if err := sessionpkg.EnsureSessionNameAvailableWithConfigForOwner(
		env.store, env.cfg, "worker-1", "", "worker"); !errors.Is(err, sessionpkg.ErrSessionNameExists) {
		t.Fatalf("name availability while the proof is poisoned = %v, want the identity still held", err)
	}
}
