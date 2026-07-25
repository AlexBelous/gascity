package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// writeTopologyCity writes a minimal city.toml at dir and returns dir. body is
// the [beads]/[beads.work]/[[rigs]] TOML appended after the workspace stanza.
func writeTopologyCity(t *testing.T, dir, prefix, body string) string {
	t.Helper()
	toml := "[workspace]\nname = \"topo\"\nprefix = \"" + prefix + "\"\n" + body
	if err := os.WriteFile(filepath.Join(dir, "city.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func runBdTopology(t *testing.T, cityPath string, args ...string) (string, string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := doBdTopology(cityPath, args, &stdout, &stderr)
	return stdout.String(), stderr.String(), code
}

func TestBdTopologyShowScopedDefaults(t *testing.T) {
	city := writeTopologyCity(t, t.TempDir(), "ga", "")
	out, errOut, code := runBdTopology(t, city)
	if code != 0 {
		t.Fatalf("show exit=%d stderr=%s", code, errOut)
	}
	for _, want := range []string{"infra:  bd", "scope:  scoped", "target: managed", "hq", "ga"} {
		if !strings.Contains(out, want) {
			t.Fatalf("show output missing %q:\n%s", want, out)
		}
	}
}

func TestBdTopologyShowJSON(t *testing.T) {
	city := writeTopologyCity(t, t.TempDir(), "ga", "")
	out, errOut, code := runBdTopology(t, city, "--json")
	if code != 0 {
		t.Fatalf("show --json exit=%d stderr=%s", code, errOut)
	}
	var rep bdTopologyReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("decoding json %q: %v", out, err)
	}
	if !rep.OK || rep.Infra != "bd" || rep.Scope != "scoped" || rep.Target != "managed" {
		t.Fatalf("unexpected report: %+v", rep)
	}
	if len(rep.Classes) != 5 {
		t.Fatalf("want 5 class states, got %d", len(rep.Classes))
	}
	if len(rep.Prefixes) == 0 || rep.Prefixes[0].Scope != "hq" || rep.Prefixes[0].Prefix != "ga" {
		t.Fatalf("prefix inventory = %+v", rep.Prefixes)
	}
}

func TestBdTopologyShowUnifiedMarkerState(t *testing.T) {
	city := writeTopologyCity(t, t.TempDir(), "ga", "\n[beads.work]\nscope = \"unified\"\n")
	writeUnifiedMarker(t, city)
	out, _, code := runBdTopology(t, city, "--json")
	if code != 0 {
		t.Fatalf("exit=%d", code)
	}
	var rep bdTopologyReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Infra != "local" || rep.Scope != "unified" {
		t.Fatalf("unified city effective axes = %+v", rep)
	}
	if !rep.Unified.MarkerPresent {
		t.Fatalf("unified marker must be reported present: %+v", rep.Unified)
	}
}

func TestBdTopologySetScopeUnified(t *testing.T) {
	city := writeTopologyCity(t, t.TempDir(), "ga", "")
	out, errOut, code := runBdTopology(t, city, "--scope", "unified")
	if code != 0 {
		t.Fatalf("set exit=%d stderr=%s", code, errOut)
	}
	if !strings.Contains(out, "scope=unified") || !strings.Contains(out, "NEXT STEP") {
		t.Fatalf("set output missing summary/next-step:\n%s", out)
	}
	// The write must round-trip: reload and confirm scope=unified.
	data, err := os.ReadFile(filepath.Join(city, "city.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "unified") {
		t.Fatalf("city.toml not updated:\n%s", data)
	}
	// Idempotent re-show reflects the new state.
	showOut, _, _ := runBdTopology(t, city, "--json")
	var rep bdTopologyReport
	if err := json.Unmarshal([]byte(showOut), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Scope != "unified" || rep.Infra != "local" {
		t.Fatalf("post-set show = %+v", rep)
	}
}

func TestBdTopologySetInfraLocal(t *testing.T) {
	city := writeTopologyCity(t, t.TempDir(), "ga", "")
	_, errOut, code := runBdTopology(t, city, "--infra", "local")
	if code != 0 {
		t.Fatalf("set infra exit=%d stderr=%s", code, errOut)
	}
	data, _ := os.ReadFile(filepath.Join(city, "city.toml"))
	if !strings.Contains(string(data), "infra") || !strings.Contains(string(data), "local") {
		t.Fatalf("infra=local not written:\n%s", data)
	}
}

// TestBdTopologyValidationTable pins deliverable D: only valid forward
// combinations are accepted; each rejection names the rule violated.
func TestBdTopologyValidationTable(t *testing.T) {
	tests := []struct {
		name       string
		prefix     string
		body       string // extra city.toml stanzas
		markers    func(city string, t *testing.T)
		args       []string
		wantSubstr string
	}{
		{
			name:       "remote target requires unified",
			prefix:     "ga",
			args:       []string{"--target", "dolt://db:3306/org"},
			wantSubstr: "requires beads.work.scope",
		},
		{
			name:       "malformed remote target",
			prefix:     "ga",
			args:       []string{"--scope", "unified", "--target", "dolt://db/org"},
			wantSubstr: "invalid value",
		},
		{
			name:       "unknown infra value",
			prefix:     "ga",
			args:       []string{"--infra", "bd"},
			wantSubstr: "unknown value",
		},
		{
			name:       "unified one-way (marker present, config scoped)",
			prefix:     "ga",
			body:       "\n[beads.work]\nscope = \"unified\"\n",
			markers:    func(city string, t *testing.T) { writeUnifiedMarker(t, city) },
			args:       []string{"--scope", "scoped"},
			wantSubstr: "one-way",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			city := writeTopologyCity(t, t.TempDir(), tc.prefix, tc.body)
			if tc.markers != nil {
				tc.markers(city, t)
			}
			_, errOut, code := runBdTopology(t, city, tc.args...)
			if code == 0 {
				t.Fatalf("expected rejection, got exit 0")
			}
			if !strings.Contains(errOut, tc.wantSubstr) {
				t.Fatalf("error %q missing %q", errOut, tc.wantSubstr)
			}
		})
	}
}

// TestBdTopologySetUnifiedPrefixCollision pins the prefix-distinctness rejection
// under a unified flip (HQ prefix == a rig prefix).
func TestBdTopologySetUnifiedPrefixCollision(t *testing.T) {
	city := writeTopologyCity(t, t.TempDir(), "dup",
		"\n[[rigs]]\nname = \"dup\"\nprefix = \"dup\"\npath = \"dup\"\n")
	_, errOut, code := runBdTopology(t, city, "--scope", "unified")
	if code == 0 {
		t.Fatalf("expected prefix collision rejection, got exit 0")
	}
	if !strings.Contains(errOut, "prefix") || !strings.Contains(errOut, "dup") {
		t.Fatalf("collision error missing pair/prefix: %q", errOut)
	}
}

// TestBdTopologySetRejectionLeavesConfigUnchanged pins the all-or-nothing set:
// a combo rejected by pre-write validation must leave city.toml byte-identical.
func TestBdTopologySetRejectionLeavesConfigUnchanged(t *testing.T) {
	city := writeTopologyCity(t, t.TempDir(), "ga", "")
	before, err := os.ReadFile(filepath.Join(city, "city.toml"))
	if err != nil {
		t.Fatal(err)
	}
	// remote target without unified is rejected before any write.
	_, _, code := runBdTopology(t, city, "--target", "dolt://db:3306/org")
	if code == 0 {
		t.Fatal("invalid combo must be rejected")
	}
	after, err := os.ReadFile(filepath.Join(city, "city.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("rejected set must not modify city.toml:\nbefore=%q\nafter=%q", before, after)
	}
}

// TestBdTopologyShowSurfacesClassMarkerStatFault pins finding 4: a non-ENOENT
// class-marker stat fault is surfaced in the report (never a silent "unknown").
func TestBdTopologyShowSurfacesClassMarkerStatFault(t *testing.T) {
	city := writeTopologyCity(t, t.TempDir(), "ga", "")
	// Make the class-store dir a FILE so class-marker stats fail ENOTDIR — a
	// non-ENOENT fault classMigrationStates records as statErr (routing=unknown),
	// while the work markers (which fold ENOTDIR to absent) still read cleanly.
	storeDir := filepath.Dir(graphMigratedMarkerPath(city))
	if err := os.MkdirAll(filepath.Dir(storeDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(storeDir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, errOut, code := runBdTopology(t, city, "--json")
	if code != 0 {
		t.Fatalf("show must still render (exit 0) and surface the fault, got %d stderr=%s", code, errOut)
	}
	var rep bdTopologyReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range rep.Classes {
		if c.StatError != "" {
			found = true
			if c.Routing != "unknown" {
				t.Fatalf("class %s with a stat fault must route unknown, got %q", c.Class, c.Routing)
			}
		}
	}
	if !found {
		t.Fatalf("a class-marker stat fault must be surfaced, not silently dropped: %+v", rep.Classes)
	}
}

func TestBdTopologyShowPositionalWithSetterRejected(t *testing.T) {
	city := writeTopologyCity(t, t.TempDir(), "ga", "")
	_, errOut, code := runBdTopology(t, city, "show", "--scope", "unified")
	if code == 0 || !strings.Contains(errOut, "read-only") {
		t.Fatalf("show+setter must be rejected: exit=%d err=%q", code, errOut)
	}
}

func TestBdTopologyUnknownSubcommand(t *testing.T) {
	city := writeTopologyCity(t, t.TempDir(), "ga", "")
	_, errOut, code := runBdTopology(t, city, "bogus")
	if code == 0 || !strings.Contains(errOut, "unknown subcommand") {
		t.Fatalf("unknown subcommand must be rejected: exit=%d err=%q", code, errOut)
	}
}

// TestBdTopologyDryRunUnifyPlan pins deliverable C: the unify rung shows each
// rig would-merge with a ClassWork count and the union prefix set, computed
// read-only over injected fake stores.
func TestBdTopologyDryRunUnifyPlan(t *testing.T) {
	city := writeTopologyCity(t, t.TempDir(), "ga",
		"\n[[rigs]]\nname = \"fe\"\nprefix = \"fe\"\npath = \"fe\"\n"+
			"[[rigs]]\nname = \"be\"\nprefix = \"be\"\npath = \"be\"\n")

	// Distinct identities so the rigs are trigger scopes (differ from city).
	origResolve := workUnifyResolveIdentity
	t.Cleanup(func() { workUnifyResolveIdentity = origResolve })
	workUnifyResolveIdentity = func(cityPath, scopeRoot string) (workUnifyScope, error) {
		db := "hq"
		if !samePath(scopeRoot, cityPath) {
			db = filepath.Base(scopeRoot)
		}
		return workUnifyScope{root: scopeRoot, database: db}, nil
	}

	feStore := newFakeWorkStore()
	feStore.seed(&fakeWorkRec{id: "fe-1", status: "open", issueType: "task"})
	feStore.seed(&fakeWorkRec{id: "fe-2", status: "closed", issueType: "task"})
	beStore := newFakeWorkStore()
	beStore.seed(&fakeWorkRec{id: "be-1", status: "open", issueType: "task"})

	origOpen := openWorkUnifyScopeStore
	t.Cleanup(func() { openWorkUnifyScopeStore = origOpen })
	openWorkUnifyScopeStore = func(_, scopeRoot string) (beads.Store, func(), error) {
		switch filepath.Base(scopeRoot) {
		case "fe":
			return feStore, func() {}, nil
		case "be":
			return beStore, func() {}, nil
		default:
			return newFakeWorkStore(), func() {}, nil
		}
	}

	out, errOut, code := runBdTopology(t, city, "--scope", "unified", "--dry-run", "--json")
	if code != 0 {
		t.Fatalf("dry-run exit=%d stderr=%s", code, errOut)
	}
	var plan bdTopologyPlan
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatalf("decode plan %q: %v", out, err)
	}
	if plan.UnifyRung.Status != "would-run" {
		t.Fatalf("unify rung status = %q, want would-run", plan.UnifyRung.Status)
	}
	if len(plan.UnifyRung.Rigs) != 2 {
		t.Fatalf("want 2 rig plans, got %+v", plan.UnifyRung.Rigs)
	}
	counts := map[string]int{}
	for _, r := range plan.UnifyRung.Rigs {
		if !r.Countable {
			t.Fatalf("rig %s not countable: %+v", r.Rig, r)
		}
		counts[r.Rig] = r.WorkBeads
	}
	if counts["fe"] != 2 || counts["be"] != 1 {
		t.Fatalf("work-bead counts = %+v", counts)
	}
	if got := strings.Join(plan.UnifyRung.UnionPrefixes, ","); got != "be,fe,ga" {
		t.Fatalf("union prefixes = %q, want be,fe,ga", got)
	}
	// Dry-run must write NOTHING: no marker, config unchanged.
	if _, err := os.Stat(workUnifiedMarkerPath(city)); !os.IsNotExist(err) {
		t.Fatalf("dry-run must not write the unified marker (stat err=%v)", err)
	}
	data, _ := os.ReadFile(filepath.Join(city, "city.toml"))
	if strings.Contains(string(data), "unified") {
		t.Fatalf("dry-run must not modify city.toml:\n%s", data)
	}
}

// TestBdTopologyDryRunRemotePlan pins the remote rung: endpoint, allowed_prefixes,
// copy count, and credential note.
func TestBdTopologyDryRunRemotePlan(t *testing.T) {
	city := writeTopologyCity(t, t.TempDir(), "ga", "")

	cityStore := newFakeWorkStore()
	cityStore.seed(&fakeWorkRec{id: "ga-1", status: "open", issueType: "task"})
	cityStore.seed(&fakeWorkRec{id: "ga-2", status: "open", issueType: "task"})
	cityStore.seed(&fakeWorkRec{id: "ga-3", status: "closed", issueType: "task"})
	origOpen := openWorkUnifyScopeStore
	t.Cleanup(func() { openWorkUnifyScopeStore = origOpen })
	openWorkUnifyScopeStore = func(string, string) (beads.Store, func(), error) {
		return cityStore, func() {}, nil
	}

	out, errOut, code := runBdTopology(t, city,
		"--scope", "unified", "--target", "dolt://org.db:3306/shared", "--dry-run", "--json")
	if code != 0 {
		t.Fatalf("remote dry-run exit=%d stderr=%s", code, errOut)
	}
	var plan bdTopologyPlan
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.RemoteRung.Status != "would-run" {
		t.Fatalf("remote rung status = %q", plan.RemoteRung.Status)
	}
	if plan.RemoteRung.Endpoint != "dolt://org.db:3306/shared" {
		t.Fatalf("endpoint = %q", plan.RemoteRung.Endpoint)
	}
	if !plan.RemoteRung.Countable || plan.RemoteRung.WorkBeadCopy != 3 {
		t.Fatalf("copy count = %d countable=%v", plan.RemoteRung.WorkBeadCopy, plan.RemoteRung.Countable)
	}
	if plan.RemoteRung.CredentialNote == "" {
		t.Fatalf("remote rung must carry a credential note")
	}
	if got := strings.Join(plan.RemoteRung.AllowedPrefixes, ","); got != "ga" {
		t.Fatalf("allowed_prefixes = %q", got)
	}
	// Remote implies unified — the unify rung is present too.
	if plan.UnifyRung.Status != "would-run" {
		t.Fatalf("remote implies unify would-run, got %q", plan.UnifyRung.Status)
	}
}

// TestBdTopologyDryRunInfraCensus pins the infra-rung .gc/infra G1 census: an
// orphan work-class bead (absent from the work store) is reported as blocking.
func TestBdTopologyDryRunInfraCensus(t *testing.T) {
	city := writeTopologyCity(t, t.TempDir(), "ga", "\n[beads]\ninfra = \"local\"\n")

	scope := newFakeWorkStore()
	scope.seed(&fakeWorkRec{id: "gcg-1", status: "open", issueType: "task"}) // classifies ClassWork
	origScope := openInfraCombinedScopeSource
	t.Cleanup(func() { openInfraCombinedScopeSource = origScope })
	openInfraCombinedScopeSource = func(string) (beads.Store, func(), bool, error) {
		return scope, func() {}, true, nil
	}
	// Force infraScopeMigrationSource to report present by planting a beads.sqlite.
	infraDir := infraCombinedScopeDir(city)
	if err := os.MkdirAll(infraDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(infraDir, "beads.sqlite"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Work store lacks gcg-1 → orphan.
	work := newFakeWorkStore()
	origOpen := openWorkUnifyScopeStore
	t.Cleanup(func() { openWorkUnifyScopeStore = origOpen })
	openWorkUnifyScopeStore = func(string, string) (beads.Store, func(), error) {
		return work, func() {}, nil
	}

	out, errOut, code := runBdTopology(t, city, "--dry-run", "--json")
	if code != 0 {
		t.Fatalf("infra dry-run exit=%d stderr=%s", code, errOut)
	}
	var plan bdTopologyPlan
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatal(err)
	}
	c := plan.InfraRung.InfraScopeCensus
	if c == nil {
		t.Fatalf("expected an infra-scope census in the plan: %+v", plan.InfraRung)
	}
	if c.WorkClass != 1 || c.Orphans != 1 {
		t.Fatalf("census = %+v, want work_class=1 orphans=1", c)
	}
}
