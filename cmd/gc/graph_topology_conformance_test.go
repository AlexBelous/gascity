package main

// The split-topology conformance invariants (feat/split-store-conformance's
// 11-invariant suite), re-expressed against this branch's graph-class seams
// instead of the work/infra-scope fixture the original harness targeted.
// Several invariants are pinned in depth by per-slice tests (creation
// residence: TestPolicyStoreCreateSideRoutesGraphClass; write residence:
// TestBdGraphSqliteMutationArm; read federation: TestBdShowFed*; residence
// sweep: TestEnsureGraphClassMigrated; warm-tick demand:
// TestAppendGraphScaleTargets; claim: TestGraphRoutedHookClaimOps). This
// suite adds the invariants that had no in-tree pin and keeps the whole set
// named in one place.

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
)

// TestGraphTopologyClaimRoutingWispIDs pins claim routing for the
// production-shaped wisp-tier ids (gcg-wisp-…): the id namespace bd's
// molecule roots actually carry must route to the graph store's CAS claim.
func TestGraphTopologyClaimRoutingWispIDs(t *testing.T) {
	cityPath := t.TempDir()
	writeGraphMigratedMarker(t, cityPath)
	st, err := graphClassStoreFor(cityPath)
	if err != nil {
		t.Fatal(err)
	}
	wisp, err := st.CreateWithForeignID(beads.Bead{ID: "gcg-wisp-topo1", Title: "wisp root", Type: "molecule", Ephemeral: true})
	if err != nil {
		t.Fatal(err)
	}
	ops := graphRoutedHookClaimOps(cityPath, sqliteGraphConfig())
	claimed, ok, err := ops.Claim(context.Background(), t.TempDir(), nil, wisp.ID, "worker-w")
	if err != nil || !ok || claimed.Assignee != "worker-w" {
		t.Fatalf("wisp-id claim = (%+v, %v, %v)", claimed, ok, err)
	}
}

// TestGraphTopologyCrossStoreAttachLinkage pins landmine #4's replacement:
// an attach onto a graph-class root stamps metadata linkage on the work
// parent (bd cannot express a blocking cross-store edge), while a same-store
// attach keeps the real dep edge.
func TestGraphTopologyCrossStoreAttachLinkage(t *testing.T) {
	work := beads.NewMemStore()
	parent, err := work.Create(beads.Bead{Title: "source", Type: "task"})
	if err != nil {
		t.Fatal(err)
	}

	if err := ensureFormulaCookAttachDep(work, parent.ID, "gcg-wisp-root9"); err != nil {
		t.Fatalf("cross-store attach: %v", err)
	}
	got, err := work.Get(parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Metadata[beadmeta.AttachedWorkflowRootMetadataKey] != "gcg-wisp-root9" {
		t.Fatalf("attach linkage not stamped: %+v", got.Metadata)
	}
	if deps, _ := work.DepList(parent.ID, "down"); len(deps) != 0 {
		t.Fatalf("cross-store attach minted a dep edge: %+v", deps)
	}

	// Same-store attach keeps the real blocking edge.
	root, err := work.Create(beads.Bead{Title: "root", Type: "task"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureFormulaCookAttachDep(work, parent.ID, root.ID); err != nil {
		t.Fatal(err)
	}
	deps, _ := work.DepList(parent.ID, "down")
	if len(deps) != 1 || deps[0].DependsOnID != root.ID {
		t.Fatalf("same-store attach dep = %+v", deps)
	}
}

// TestGraphTopologyAttachBlockEnforcedAtReady pins the composite-ready
// enforcement: a work candidate marked with an OPEN graph root is withheld
// from the federated candidate list, released when the root closes, and a
// dangling marker fails LOUD (absence must never read as unblocked — the
// root-loss discipline).
func TestGraphTopologyAttachBlockEnforcedAtReady(t *testing.T) {
	cityPath := t.TempDir()
	writeGraphMigratedMarker(t, cityPath)
	st, err := graphClassStoreFor(cityPath)
	if err != nil {
		t.Fatal(err)
	}
	root, err := st.Create(beads.Bead{Title: "dag root", Type: "molecule"})
	if err != nil {
		t.Fatal(err)
	}

	shell := `[{"id":"gc-parent","title":"parent","status":"open","issue_type":"task","metadata":{"` +
		beadmeta.AttachedWorkflowRootMetadataKey + `":"` + root.ID + `"}}]`
	merged, err := mergeGraphReadyIntoWorkQueryOutput(shell, st)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(merged, "gc-parent") {
		t.Fatalf("attach-blocked parent surfaced while its root is open: %s", merged)
	}

	if err := st.Close(root.ID); err != nil {
		t.Fatal(err)
	}
	merged, err = mergeGraphReadyIntoWorkQueryOutput(shell, st)
	if err != nil || !strings.Contains(merged, "gc-parent") {
		t.Fatalf("parent not released after root close: (%q, %v)", merged, err)
	}

	dangling := strings.ReplaceAll(shell, root.ID, "gcg-999999")
	if _, err := mergeGraphReadyIntoWorkQueryOutput(dangling, st); err == nil {
		t.Fatal("dangling attach marker must fail loud, not read as unblocked")
	}
}

// TestGraphTopologyWakeOwnershipFastPath pins the dispatcher by-id fast
// path: findBeadAcrossStores resolves a gcg id from the graph store without
// scanning work/rig stores, and surfaces a hard miss as an error.
func TestGraphTopologyWakeOwnershipFastPath(t *testing.T) {
	cityPath := t.TempDir()
	writeGraphMigratedMarker(t, cityPath)
	// findBeadAcrossStores self-loads config; the flipped ratchet accepts the
	// sqlite graph backend in a parsed city.toml now.
	if err := os.WriteFile(filepath.Join(cityPath, "city.toml"), []byte("[workspace]\nname = \"topo\"\n\n[beads.classes.graph]\nbackend = \"sqlite\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := graphClassStoreFor(cityPath)
	if err != nil {
		t.Fatal(err)
	}
	control, err := st.Create(beads.Bead{Title: "control bead", Type: "task"})
	if err != nil {
		t.Fatal(err)
	}
	store, got, _, err := findBeadAcrossStores(cityPath, control.ID, io.Discard)
	if err != nil || got.ID != control.ID {
		t.Fatalf("fast path = (%+v, %v)", got, err)
	}
	if store != beads.Store(st) {
		t.Fatalf("fast path resolved store %T, want the graph store", store)
	}
}

// TestGraphTopologyReadPathConsistency pins the tier semantics the split
// suite called "read-path consistency": List(TierIssues) = durable rows
// only, Ready(TierBoth) = +wisps, TierWisps = ephemeral rows only.
func TestGraphTopologyReadPathConsistency(t *testing.T) {
	cityPath := t.TempDir()
	writeGraphMigratedMarker(t, cityPath)
	st, err := graphClassStoreFor(cityPath)
	if err != nil {
		t.Fatal(err)
	}
	durable, err := st.Create(beads.Bead{Title: "durable", Type: "task"})
	if err != nil {
		t.Fatal(err)
	}
	wisp, err := st.Create(beads.Bead{Title: "wispy", Type: "task", Ephemeral: true})
	if err != nil {
		t.Fatal(err)
	}

	issues, err := st.List(beads.ListQuery{AllowScan: true})
	if err != nil {
		t.Fatal(err)
	}
	ids := beadIDSet(issues)
	if !ids[durable.ID] || ids[wisp.ID] {
		t.Fatalf("List(TierIssues) = %v, want durable only", ids)
	}

	both, err := st.Ready(beads.ReadyQuery{TierMode: beads.TierBoth})
	if err != nil {
		t.Fatal(err)
	}
	ids = beadIDSet(both)
	if !ids[durable.ID] || !ids[wisp.ID] {
		t.Fatalf("Ready(TierBoth) = %v, want both tiers", ids)
	}

	wisps, err := st.List(beads.ListQuery{AllowScan: true, TierMode: beads.TierWisps})
	if err != nil {
		t.Fatal(err)
	}
	ids = beadIDSet(wisps)
	if ids[durable.ID] || !ids[wisp.ID] {
		t.Fatalf("List(TierWisps) = %v, want wisp only", ids)
	}
}

func beadIDSet(items []beads.Bead) map[string]bool {
	out := make(map[string]bool, len(items))
	for _, b := range items {
		out[b.ID] = true
	}
	return out
}
