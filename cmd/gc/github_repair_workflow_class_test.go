package main

// Where the GitHub PR repair workflow's molecule lands on a split city.
//
// ensureGitHubPRRepairBead opens ONE store — the scope store at
// resolveStoreScopeRoot(cityPath, rig.Path) — and instantiates the monitor's
// repair workflow on it. Nothing on that path asks the class seam anything. Under
// the normal topology the scope is a rig, rig stores are never relocated, and the
// answer happens to be right; but "happens to be right" is not the rule, and the
// rule has an edge. Nothing forbids registering a rig AT the city root
// (cmd_convoy_dispatch.go:637-651), and there the scope store IS the city work
// ledger — so a graph-class repair workflow mints its beads into the ledger a
// converged city moved graph off, which is the stranded write `gc storage status`
// counts and every later command refuses on.
//
// Two rows, because the class question and the scope question are different
// questions and the fix must answer both: a city-root scope routes to the
// binding, a rig scope stays co-resident in its own ledger.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/coordclass"
	"github.com/gastownhall/gascity/internal/githubmonitor"
)

// githubRepairSplitCity builds a converged split city whose PR monitor is bound
// to a rig, with a graph.v2 repair workflow on disk. rigSubdir names the rig's
// root relative to the city; "" registers the rig AT the city root, which is the
// edge the city-scope row is about.
//
// The scope store is stubbed rather than opened from disk because what is under
// test is which of two stores the workflow is instantiated in, and a stub makes
// "the scope store" unambiguous: whatever ensureGitHubPRRepairBead was handed.
func githubRepairSplitCity(t *testing.T, rigSubdir string) (cityPath string, cfg *config.City, scope, graph beads.Store) {
	t.Helper()
	cityPath = t.TempDir()
	rigPath := cityPath
	if rigSubdir != "" {
		rigPath = filepath.Join(cityPath, rigSubdir)
	}
	if err := os.MkdirAll(rigPath, 0o755); err != nil {
		t.Fatalf("mkdir rig %s: %v", rigPath, err)
	}

	formulaDir := filepath.Join(cityPath, "formulas")
	if err := os.MkdirAll(formulaDir, 0o755); err != nil {
		t.Fatalf("mkdir formulas: %v", err)
	}
	// A graph.v2 repair workflow: the compiler stamps gc.kind=workflow on the
	// root, so every bead instantiating it produces is graph class. That is the
	// shape whose placement this bead is about — a v1 poured molecule classifies
	// as work and correctly stays on the work ledger in either scope.
	if err := os.WriteFile(filepath.Join(formulaDir, "pr-repair.formula.toml"), []byte(`
formula = "pr-repair"
version = 2
contract = "graph.v2"

[[steps]]
id = "repair"
title = "Repair the PR"
`), 0o644); err != nil {
		t.Fatalf("write repair formula: %v", err)
	}
	enableFormulaV2ForOneShotTest(t)

	cfg = &config.City{
		Workspace: config.Workspace{Name: "repair-city"},
		Rigs:      []config.Rig{{Name: "wf", Path: rigPath}},
		FormulaLayers: config.FormulaLayers{
			City: []string{formulaDir},
			Rigs: map[string][]string{"wf": {formulaDir}},
		},
	}

	graph = beads.NewMemStore()
	seedCLIStorageRoutes(t, cityPath, messagingSplitRoutes(graph))

	scope = beads.NewMemStore()
	prev := openGitHubPRRepairStore
	openGitHubPRRepairStore = func(string, string) (beads.Store, error) { return scope, nil }
	t.Cleanup(func() { openGitHubPRRepairStore = prev })
	return cityPath, cfg, scope, graph
}

// githubRepairMonitor is the monitor + result pair the two rows share: one
// actionable failing PR routed to the rig under test.
func githubRepairMonitor() (config.GitHubPRMonitor, githubmonitor.Result) {
	monitor := config.GitHubPRMonitor{
		Name:           "partcl-main",
		Owner:          "partcleda",
		Repo:           "partcl",
		Rig:            "wf",
		RepairWorkflow: "pr-repair",
	}
	result := githubmonitor.Result{
		Monitor:     "partcl-main",
		Owner:       "partcleda",
		Repo:        "partcl",
		Number:      2560,
		HeadSHA:     "abc123",
		HeadRefName: "fix",
		State:       githubmonitor.StateFailed,
		FailureKind: "checks_failed",
		Actionable:  true,
		Rig:         "wf",
		RepairRoute: "wf/polecat",
	}
	return monitor, result
}

// graphClassBeads returns the graph-class beads a store holds, which is the
// only population either row is about: the repair TASK bead itself is work class
// and belongs in the scope store in both rows.
func graphClassBeads(t *testing.T, store beads.Store) []beads.Bead {
	t.Helper()
	var got []beads.Bead
	for _, b := range allBeads(t, store) {
		if coordclass.Classify(b) == coordclass.ClassGraph {
			got = append(got, b)
		}
	}
	return got
}

// TestGitHubPRRepairWorkflowTakesTheBindingWhenItsScopeIsTheCity is the defect.
//
// Red-before, on the current tree:
//
//	the graph binding holds 0 bead(s) from the repair workflow; a graph-class
//	molecule instantiated in a CITY scope belongs in the binding
//	the city work ledger holds 2 graph-class bead(s) ([gc-2 gc-3]) — stranded
//	writes in the ledger this city moved graph off
func TestGitHubPRRepairWorkflowTakesTheBindingWhenItsScopeIsTheCity(t *testing.T) {
	cityPath, cfg, scope, graph := githubRepairSplitCity(t, "")
	monitor, result := githubRepairMonitor()

	outcome, err := ensureGitHubPRRepairBead(cityPath, cfg, monitor, result)
	if err != nil {
		t.Fatalf("ensureGitHubPRRepairBead: %v", err)
	}
	if outcome.dispatchErr != nil {
		t.Fatalf("attaching the repair workflow: %v", outcome.dispatchErr)
	}
	if !outcome.dispatched {
		t.Fatal("the repair workflow was not dispatched; the row cannot say where its beads landed")
	}

	if got := graphClassBeads(t, graph); len(got) == 0 {
		t.Errorf("the graph binding holds 0 bead(s) from the repair workflow; a graph-class molecule instantiated in a CITY scope belongs in the binding")
	}
	if got := graphClassBeads(t, scope); len(got) != 0 {
		t.Errorf("the city work ledger holds %d graph-class bead(s) (%v) — stranded writes in the ledger this city moved graph off", len(got), beadIDs(got))
	}
	// The repair bead itself is work class and stays where the monitor's scope
	// store is, in both rows. Routing the molecule must not move it.
	if _, err := scope.Get(outcome.bead.ID); err != nil {
		t.Errorf("the repair bead %s is not in the scope store: %v", outcome.bead.ID, err)
	}
}

// TestGitHubPRRepairWorkflowStaysInTheRigLedgerInARigScope is the co-residence
// rule, and it is green today — it is here so the fix cannot buy the row above
// by routing every repair workflow to the city binding.
//
// A relocation is a CITY-scope event: `gc storage migrate` copies only the city
// work store and resolveClassStore holds one city-level store per class with no
// per-rig binding to route to (convergence_tick.go:82-88). A rig's ledger holds
// both the repair bead and its workflow, the city's containment check never
// reads it, and nothing written there can be stranded.
func TestGitHubPRRepairWorkflowStaysInTheRigLedgerInARigScope(t *testing.T) {
	cityPath, cfg, scope, graph := githubRepairSplitCity(t, "wf")
	monitor, result := githubRepairMonitor()

	outcome, err := ensureGitHubPRRepairBead(cityPath, cfg, monitor, result)
	if err != nil {
		t.Fatalf("ensureGitHubPRRepairBead: %v", err)
	}
	if outcome.dispatchErr != nil {
		t.Fatalf("attaching the repair workflow: %v", outcome.dispatchErr)
	}

	if got := graphClassBeads(t, scope); len(got) == 0 {
		t.Errorf("the rig ledger holds no graph-class bead from the repair workflow; a rig scope keeps both ends of the molecule in its own store")
	}
	if got := allBeads(t, graph); len(got) != 0 {
		t.Errorf("the graph binding holds %d bead(s) from a RIG-scoped repair workflow (%v); the binding is city-keyed and never serves a rig", len(got), beadIDs(got))
	}
}
