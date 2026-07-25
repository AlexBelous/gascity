package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// TestWorkQueryMetadataScopingAudit is deliverable F's implementation-audit
// guard (engdocs/design/beads-work-topology.md, "Routing is metadata, not
// residence"): post-unify the store boundary no longer scopes anything, so every
// SDK-issued rig-scoped work query must scope by bead routing metadata/labels
// (gc.routed_to / gc.run_target / rig-qualified hook labels) and dedup by bead
// ID across stores — never by "this store contains only this rig's beads".
//
// A fully general grep guard for "relies on store-boundary scoping" is not
// expressible (the reliance is a semantic property of a query's intent), so this
// follows the spec's fallback: the SDK-issued rig-scoped work-query sites were
// audited by hand and are pinned structurally here so a regression that drops
// the metadata-scoping mechanism fails the build.
//
// Audit result (2026-07, this slice):
//   - Controller demand pass (build_desired_state.go, defaultScaleCheckCountsAndDemand):
//     filters ready beads by controllerDemandRouteTarget (gc.routed_to /
//     gc.run_target), NOT by which store returned them, and dedups counted
//     beads by ID across store groups (countedBeads) so an aliased/shared store
//     never double-counts. Metadata-scoped: OK.
//   - Work-unify quarantine seam (graph_hook_ready.go / work_unify_quarantine.go /
//     cmd_hook_claim.go): excludes gc.topology_migrating rows by LABEL,
//     unconditionally — a label filter, not a store boundary. Metadata-scoped: OK.
//   - The pack-supplied work_query command is issued by the AGENT, not the SDK;
//     pack-spec.md now documents that unified cities require a rig-scoping filter
//     on it.
//
// No SDK-issued rig-scoped work query was found relying on store-boundary
// scoping. The structural pins below are name-level only (they would pass
// against a same-name semantic regression); the BEHAVIORAL guarantee — that the
// demand pass counts a shared bead id once across endpoint-identical store
// groups rather than by store residence — is pinned by
// TestControllerDemandDedupsSharedBeadIDAcrossEndpointIdenticalStores below.
func TestWorkQueryMetadataScopingAudit(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(currentFile)

	mustContain := func(file string, needles ...string) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			t.Fatalf("ReadFile(%q): %v", file, err)
		}
		content := string(data)
		for _, needle := range needles {
			if !strings.Contains(content, needle) {
				t.Errorf("%s no longer contains %q — a rig-scoped work query may now rely on the store boundary "+
					"instead of routing metadata; re-audit the metadata-scoping (deliverable F)", file, needle)
			}
		}
	}

	// The demand pass must route by metadata and dedup by ID, not by store.
	mustContain("build_desired_state.go",
		"controllerDemandRouteTarget(",
		"countedBeads",
	)
	// The quarantine seam must exclude by the topology-migrating LABEL.
	mustContain("work_unify_quarantine.go", "workTopologyMigratingLabel")
}

// TestControllerDemandDedupsSharedBeadIDAcrossEndpointIdenticalStores is the
// behavioral pin for deliverable F / the runtime aftermath: post-unify the
// controller demand pass must count a work bead by its (globally-unique) ID, not
// by which endpoint-identical store surfaced it. Two DISTINCT store handles onto
// aliased scopes both returning the SAME ready bead id must contribute demand
// exactly ONCE (the countedBeads ID-dedup), not once per aliased leg.
func TestControllerDemandDedupsSharedBeadIDAcrossEndpointIdenticalStores(t *testing.T) {
	const template = "gascity/workflows.claude-min"
	shared := beads.Bead{
		ID:     "gca-1",
		Type:   "task",
		Status: "open",
		// Unassigned + routed by metadata, not by store residence.
		Metadata: map[string]string{"gc.routed_to": template},
	}

	counts, _, errs := defaultScaleCheckCounts([]defaultScaleCheckTarget{
		{template: template, storeKey: "rig:a", store: &readyStaticStore{ready: []beads.Bead{shared}}},
		{template: template, storeKey: "rig:b", store: &readyStaticStore{ready: []beads.Bead{shared}}},
	})
	if len(errs) != 0 {
		t.Fatalf("defaultScaleCheckCounts errs = %v", errs)
	}
	if got := counts[template]; got != 1 {
		t.Fatalf("counts[%q] = %d, want 1 (shared bead id deduped across endpoint-identical store groups, not counted per aliased leg)", template, got)
	}
}
