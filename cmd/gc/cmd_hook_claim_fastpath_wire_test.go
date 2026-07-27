package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/api"
	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/rollout"
)

// fakeFastPathClient satisfies the full read+mutate surface the fast-path
// orchestrator needs, so the success wiring is exercised without a live
// controller. Connection-failure and server-verdict paths instead use a real
// api.Client against an httptest server, since only the api package can mint the
// *connError that IsConnError recognizes.
type fakeFastPathClient struct {
	fakeFastPathReader
	claimBead func(id, actor string) (beads.Bead, bool, error)
	updates   []string
}

func (f *fakeFastPathClient) ClaimBead(_ context.Context, id, actor string) (beads.Bead, bool, error) {
	return f.claimBead(id, actor)
}

func (f *fakeFastPathClient) GetBead(id string) (api.CachedRead[beads.Bead], error) {
	return api.CachedRead[beads.Bead]{Body: beads.Bead{ID: id}}, nil
}

func (f *fakeFastPathClient) UpdateBeadMetadata(_ context.Context, id string, _ map[string]string) error {
	f.updates = append(f.updates, id)
	return nil
}

func TestHookFastPathEligible(t *testing.T) {
	on := rollout.ForTest(rollout.WithHookClaimFastPath(true))
	off := rollout.ForTest(rollout.WithHookClaimFastPath(false))
	tests := []struct {
		name        string
		flags       rollout.Flags
		workQuery   string
		agentName   string
		routeTarget string
		want        bool
	}{
		{"flag off", off, "", "polecat", "polecat", false},
		{"standard eligible", on, "", "polecat", "polecat", true},
		{"custom work_query stays shell", on, "bd ready --json", "polecat", "polecat", false},
		{"legacy control-dispatcher agent", on, "", "gastown/control-dispatcher", "gastown/control-dispatcher", false},
		{"legacy control-dispatcher target", on, "", "polecat", "gastown/control-dispatcher", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := hookFastPathEligible(tc.flags, tc.workQuery, tc.agentName, tc.routeTarget); got != tc.want {
				t.Errorf("hookFastPathEligible = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestClaimHookWorkFastPathFallsBackOnConnError proves a pre-request connection
// failure returns handled=false so the caller runs the subprocess path.
func TestClaimHookWorkFastPathFallsBackOnConnError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := ts.URL
	ts.Close() // nothing listening → real *connError from the client reads.

	client := api.NewCityScopedClient(url, "alpha")
	var stdout, stderr bytes.Buffer
	opts := hookClaimOptions{Assignee: "worker-1", RouteTargets: []string{"pool-x"}}
	code, handled := claimHookWorkFastPath(client, []string{"worker-1"}, opts.RouteTargets, "ephemeral", nil, "/wt", opts, &stdout, &stderr)
	if handled {
		t.Fatal("handled = true on conn error, want false (fall back to shell)")
	}
	if code != 0 {
		t.Errorf("code = %d on fallback, want 0", code)
	}
}

// TestClaimHookWorkFastPathFailsFastOnServerVerdict proves a real controller
// verdict (a 500, not a connection failure) is handled terminally and never
// shelled out — shelling would multiply the connection pressure the cure removes.
func TestClaimHookWorkFastPathFailsFastOnServerVerdict(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	client := api.NewCityScopedClient(ts.URL, "alpha")
	var stdout, stderr bytes.Buffer
	opts := hookClaimOptions{Assignee: "worker-1", RouteTargets: []string{"pool-x"}}
	code, handled := claimHookWorkFastPath(client, []string{"worker-1"}, opts.RouteTargets, "ephemeral", nil, "/wt", opts, &stdout, &stderr)
	if !handled {
		t.Fatal("handled = false on a server verdict, want true (fail fast, do not shell out)")
	}
	if code == 0 {
		t.Error("code = 0 on controller error, want a non-zero terminal failure")
	}
}

// TestClaimHookWorkFastPathClaimsRoutedBead proves the end-to-end fast path: a
// routed unassigned ready bead is discovered via controller reads and claimed
// via the controller claim op.
func TestClaimHookWorkFastPathClaimsRoutedBead(t *testing.T) {
	pool := beads.Bead{ID: "gc-pool", Type: "task", Metadata: beads.StringMap{beadmeta.RoutedToMetadataKey: "pool-x"}}
	var claimedID string
	client := &fakeFastPathClient{
		fakeFastPathReader: fakeFastPathReader{ready: []beads.Bead{pool}},
		claimBead: func(id, actor string) (beads.Bead, bool, error) {
			claimedID = id
			return beads.Bead{
				ID: id, Assignee: actor, Status: "in_progress",
				Metadata: beads.StringMap{beadmeta.RoutedToMetadataKey: "pool-x"},
			}, true, nil
		},
	}
	var stdout, stderr bytes.Buffer
	opts := hookClaimOptions{Assignee: "worker-1", RouteTargets: []string{"pool-x"}}
	code, handled := claimHookWorkFastPath(client, []string{"worker-1"}, opts.RouteTargets, "ephemeral", nil, "/wt", opts, &stdout, &stderr)
	if !handled {
		t.Fatal("handled = false, want true")
	}
	if code != 0 {
		t.Errorf("code = %d, want 0 on successful claim; stderr=%s", code, stderr.String())
	}
	if claimedID != "gc-pool" {
		t.Errorf("claimed %q, want gc-pool", claimedID)
	}
	if !strings.Contains(stdout.String(), "gc-pool") {
		t.Errorf("stdout = %q, want it to report the claimed bead gc-pool", stdout.String())
	}
}
