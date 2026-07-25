package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// dedupeState wraps the fake state with WorkStoreEndpointDeduper, modeling a
// unified/remote city where every rig store aliases ONE org DB: the deduper
// collapses all rig keys to a single representative. It pins that the aggregate
// BeadStores() fan-outs route through the collapse (the real endpoint-resolution
// logic is pinned separately by controllerState.DedupeWorkStoreKeys).
type dedupeState struct {
	*fakeState
}

func (dedupeState) DedupeWorkStoreKeys(keys []string) []string {
	if len(keys) < 2 {
		return keys
	}
	return keys[:1] // all stores alias one endpoint → one representative
}

// TestStatusWorkCountsCollapsesAliasedRigLegs pins finding 2 for the non-remote
// (unified-managed) branch: aliased rig stores must not inflate the stored
// Open/InProgress counts.
func TestStatusWorkCountsCollapsesAliasedRigLegs(t *testing.T) {
	fs := newFakeState(t)
	fs.cityBeadStore = orgDBWork()
	fs.stores = map[string]beads.Store{
		"alpha": orgDBWork(), // distinct instances aliasing one org DB
		"beta":  orgDBWork(),
	}
	state := dedupeState{fakeState: fs}

	h := newTestCityHandlerReadOnly(t, state)
	resp := getStatusFrom(t, h, fs)

	if resp.Work.Open != 1 || resp.Work.InProgress != 1 || resp.Work.Ready != 1 {
		t.Fatalf("status counts = %+v, want open=1 in_progress=1 ready=1 (aliased rig legs collapsed, not summed 2x)", resp.Work)
	}
	if resp.Partial {
		t.Fatalf("unexpected partial: %v", resp.PartialErrors)
	}
}

// TestBeadListCollapsesAliasedRigLegs pins finding 2 for GET /beads: aliased rig
// stores must list each shared-DB bead exactly once, not once per alias.
func TestBeadListCollapsesAliasedRigLegs(t *testing.T) {
	fs := newFakeState(t)
	fs.cityBeadStore = orgDBWork()
	// Both rig handles are the same org DB holding one bead gca-1.
	fs.stores = map[string]beads.Store{
		"alpha": storeWithIDs(beads.Bead{ID: "gca-1", Status: "open"}),
		"beta":  storeWithIDs(beads.Bead{ID: "gca-1", Status: "open"}),
	}
	state := dedupeState{fakeState: fs}

	h := newTestCityHandlerReadOnly(t, state)
	req := httptest.NewRequest("GET", cityURL(fs, "/beads"), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Items []beads.Bead `json:"items"`
		Total int          `json:"total"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 1 || len(resp.Items) != 1 {
		t.Fatalf("beads list = total %d / %d items, want exactly one (aliased legs collapsed)", resp.Total, len(resp.Items))
	}
}

// TestAggregateDedupDarkWithoutCapability confirms DARK: a state that does NOT
// implement WorkStoreEndpointDeduper keeps the full per-rig fan-out (the
// pre-topology behavior), so scoped cities are unaffected.
func TestAggregateDedupDarkWithoutCapability(t *testing.T) {
	fs := newFakeState(t)
	fs.cityBeadStore = orgDBWork()
	fs.stores = map[string]beads.Store{
		"alpha": storeWithIDs(beads.Bead{ID: "gca-1", Status: "open"}),
		"beta":  storeWithIDs(beads.Bead{ID: "gcb-1", Status: "open"}), // genuinely distinct
	}
	// fakeState has no DedupeWorkStoreKeys → endpointCollapsedRigNames is a no-op.
	h := newTestCityHandlerReadOnly(t, fs)
	req := httptest.NewRequest("GET", cityURL(fs, "/beads"), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var resp struct {
		Total int `json:"total"`
	}
	json.NewDecoder(rec.Body).Decode(&resp) //nolint:errcheck
	if resp.Total != 2 {
		t.Fatalf("DARK: distinct scoped rigs must both list, Total=%d want 2", resp.Total)
	}
}
