package main

import (
	"errors"
	"testing"

	"github.com/gastownhall/gascity/internal/api"
	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
)

// fakeFastPathReader is a controller-read stub honoring the same status/assignee
// filter the real ListBeads pushes down, so the tier reproduction is exercised
// against realistic responses without a live server.
type fakeFastPathReader struct {
	inProgress []beads.Bead
	ready      []beads.Bead
	readyCalls int
	listErr    error
	readyErr   error
}

func (f *fakeFastPathReader) ListBeads(opts api.ListBeadsOpts) (api.CachedRead[[]beads.Bead], error) {
	if f.listErr != nil {
		return api.CachedRead[[]beads.Bead]{}, f.listErr
	}
	var out []beads.Bead
	for _, b := range f.inProgress {
		if opts.Status != "" && b.Status != opts.Status {
			continue
		}
		if opts.Assignee != "" && b.Assignee != opts.Assignee {
			continue
		}
		out = append(out, b)
		if opts.Limit > 0 && len(out) >= opts.Limit {
			break
		}
	}
	return api.CachedRead[[]beads.Bead]{Body: out}, nil
}

func (f *fakeFastPathReader) BeadsReady() (api.CachedRead[[]beads.Bead], error) {
	f.readyCalls++
	if f.readyErr != nil {
		return api.CachedRead[[]beads.Bead]{}, f.readyErr
	}
	return api.CachedRead[[]beads.Bead]{Body: f.ready}, nil
}

func routed(id, target string) beads.Bead {
	return beads.Bead{ID: id, Type: "task", Metadata: beads.StringMap{beadmeta.RoutedToMetadataKey: target}}
}

// TestFastPathTier1AssignedInProgress proves an assigned in_progress bead
// (crash recovery) short-circuits ahead of the ready tiers and the ready read
// is never issued.
func TestFastPathTier1AssignedInProgress(t *testing.T) {
	r := &fakeFastPathReader{
		inProgress: []beads.Bead{{ID: "gc-crash", Status: "in_progress", Assignee: "sess-name"}},
		ready:      []beads.Bead{{ID: "gc-ready", Assignee: "sess-name"}},
	}
	got, err := fastPathClaimCandidates(r, []string{"", "sess-name", "alias-x"}, []string{"pool-x"}, "ephemeral")
	if err != nil {
		t.Fatalf("fastPathClaimCandidates: %v", err)
	}
	if len(got) != 1 || got[0].ID != "gc-crash" {
		t.Fatalf("got %+v, want [gc-crash]", got)
	}
	if r.readyCalls != 0 {
		t.Errorf("BeadsReady called %d times; tier 1 must short-circuit before the ready read", r.readyCalls)
	}
}

// TestFastPathTier2AssignedReady proves an assigned ready bead wins when no
// in_progress work exists, and identity order is honored.
func TestFastPathTier2AssignedReady(t *testing.T) {
	r := &fakeFastPathReader{
		ready: []beads.Bead{
			{ID: "gc-other", Assignee: "someone-else"},
			{ID: "gc-mine", Assignee: "alias-x"},
		},
	}
	got, err := fastPathClaimCandidates(r, []string{"sess-id", "sess-name", "alias-x"}, []string{"pool-x"}, "")
	if err != nil {
		t.Fatalf("fastPathClaimCandidates: %v", err)
	}
	if len(got) != 1 || got[0].ID != "gc-mine" {
		t.Fatalf("got %+v, want [gc-mine]", got)
	}
}

// TestFastPathTier3RoutedPool proves the pool tier returns unassigned, non-epic,
// route-matching ready beads and excludes assigned and epic rows.
func TestFastPathTier3RoutedPool(t *testing.T) {
	epic := routed("gc-epic", "pool-x")
	epic.Type = "epic"
	assigned := routed("gc-assigned", "pool-x")
	assigned.Assignee = "worker-9"
	wrongPool := routed("gc-wrong", "pool-y")
	match := routed("gc-pool", "pool-x")

	r := &fakeFastPathReader{ready: []beads.Bead{epic, assigned, wrongPool, match}}
	got, err := fastPathClaimCandidates(r, []string{"sess-name"}, []string{"pool-x"}, "ephemeral")
	if err != nil {
		t.Fatalf("fastPathClaimCandidates: %v", err)
	}
	if len(got) != 1 || got[0].ID != "gc-pool" {
		t.Fatalf("got %+v, want [gc-pool] (epic/assigned/wrong-pool excluded)", got)
	}
}

// TestFastPathTier3OriginGated proves a non-ephemeral, non-empty session origin
// disables the pool tier — matching the shell's GC_SESSION_ORIGIN gate — so a
// user-origin session never claims routed pool demand.
func TestFastPathTier3OriginGated(t *testing.T) {
	r := &fakeFastPathReader{ready: []beads.Bead{routed("gc-pool", "pool-x")}}
	got, err := fastPathClaimCandidates(r, []string{"sess-name"}, []string{"pool-x"}, "user")
	if err != nil {
		t.Fatalf("fastPathClaimCandidates: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %+v, want empty (pool tier gated off for non-ephemeral origin)", got)
	}
}

// TestFastPathReadErrorPropagates proves a read error is surfaced (not
// swallowed) so the caller can classify a connection failure and fall back.
func TestFastPathReadErrorPropagates(t *testing.T) {
	sentinel := errors.New("boom")
	r := &fakeFastPathReader{readyErr: sentinel}
	_, err := fastPathClaimCandidates(r, []string{"sess-name"}, []string{"pool-x"}, "ephemeral")
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the sentinel read error propagated", err)
	}
}
