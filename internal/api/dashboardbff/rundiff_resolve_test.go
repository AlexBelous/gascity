package dashboardbff

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/events"
)

// runRootWithCwd builds a graph.v2 run-root molecule whose gc.cwd metadata points
// at cwd, so the run-detail projection resolves executionPath = {known, cwd}. It
// is the seam the server-side run-diff resolution reads: the browser sends no
// path, and the BFF derives the git cwd from this bead exactly as the run-detail
// endpoint does.
func runRootWithCwd(cwd string) events.Event {
	return beadCreatedEvent(1, beads.Bead{
		ID:        "run1",
		Title:     "mol-adopt-pr-v2",
		Status:    "open",
		Type:      "molecule",
		Ref:       "mol-adopt-pr-v2",
		CreatedAt: time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
		Metadata: map[string]string{
			"gc.formula_contract": "graph.v2",
			"gc.kind":             "run",
			"gc.formula":          "mol-adopt-pr-v2",
			"gc.run_target":       "rig:demo",
			"gc.root_store_ref":   "rig:demo",
			"gc.scope_kind":       "rig",
			"gc.scope_ref":        "demo",
			"gc.cwd":              cwd,
		},
	})
}

// TestRunDiffResolvesExecutionPathFromRunID is the core server-side resolution: a
// run-id-only request (no executionPath in the body) resolves the git cwd from
// the run's own detail projection and returns a diff, so the browser never sends
// a filesystem path.
func TestRunDiffResolvesExecutionPathFromRunID(t *testing.T) {
	cityDir := initGitRepo(t, t.TempDir())
	writeEventLog(t, filepath.Join(cityDir, ".gc", "events.jsonl"), runRootWithCwd(cityDir))

	p := New(Deps{Resolver: fakeResolver{paths: map[string]string{"alpha": cityDir}}})
	p.Start(t.Context())
	defer p.Stop()

	rec := postRunDiff(t, p, "/api/city/alpha/runs/run1/diff", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s), want 200 for a run-id-only diff", rec.Code, rec.Body.String())
	}
	var got runDiffResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The server resolved a real in-city repo, so the root is known — not the
	// path_unknown a scrubbed/empty client path would have produced.
	if got.RootPath.Kind == "unavailable" && got.RootPath.Reason == "path_unknown" {
		t.Errorf("server-resolved in-city repo treated as path_unknown: %+v", got)
	}
}

// TestRunDiffResolvesExecutionPathFromEmptyBody proves a truly body-less run-diff
// POST (no JSON at all) is also treated as "resolve server-side", so the run-id
// alone is a complete request.
func TestRunDiffResolvesExecutionPathFromEmptyBody(t *testing.T) {
	cityDir := initGitRepo(t, t.TempDir())
	writeEventLog(t, filepath.Join(cityDir, ".gc", "events.jsonl"), runRootWithCwd(cityDir))

	p := New(Deps{Resolver: fakeResolver{paths: map[string]string{"alpha": cityDir}}})
	p.Start(t.Context())
	defer p.Stop()

	rec := postRunDiff(t, p, "/api/city/alpha/runs/run1/diff", ``)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s), want 200 for an empty-body diff", rec.Code, rec.Body.String())
	}
}

// TestRunDiffServerResolvedPathOutsideRootsRejected proves the SERVER-resolved
// path is not trusted: it flows through the same isValidRunCwd/allowedRoots gate
// as a client-supplied path, so a run executing outside the allowed roots is
// still refused (defense in depth — resolution does not bypass validation).
func TestRunDiffServerResolvedPathOutsideRootsRejected(t *testing.T) {
	cityDir := initGitRepo(t, t.TempDir())
	outside := initGitRepo(t, t.TempDir()) // a real repo, but outside the city + allowlist
	writeEventLog(t, filepath.Join(cityDir, ".gc", "events.jsonl"), runRootWithCwd(outside))

	p := New(Deps{Resolver: fakeResolver{paths: map[string]string{"alpha": cityDir}}})
	p.Start(t.Context())
	defer p.Stop()

	rec := postRunDiff(t, p, "/api/city/alpha/runs/run1/diff", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d (body %s), want 400 for a server-resolved path outside allowed roots", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid execution path") {
		t.Errorf("body = %s, want an invalid-execution-path error", rec.Body.String())
	}
}

// TestRunDiffServerResolvedOutsideRootsReadOnlyExplains proves the ReadOnly 403
// message behavior is preserved for a server-resolved path: on the public
// read-only floor a run executing outside the served roots is told the diff is
// unavailable here rather than "invalid execution path".
func TestRunDiffServerResolvedOutsideRootsReadOnlyExplains(t *testing.T) {
	cityDir := initGitRepo(t, t.TempDir())
	outside := initGitRepo(t, t.TempDir())
	writeEventLog(t, filepath.Join(cityDir, ".gc", "events.jsonl"), runRootWithCwd(outside))

	p := New(Deps{Resolver: fakeResolver{paths: map[string]string{"alpha": cityDir}}, ReadOnly: true})
	p.Start(t.Context())
	defer p.Stop()

	rec := postRunDiff(t, p, "/api/city/alpha/runs/run1/diff", `{}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d (body %s), want 403 on the read-only floor", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "read-only dashboard") {
		t.Errorf("body = %s, want a read-only-dashboard explanation", rec.Body.String())
	}
}

// TestRunDiffUnknownRunNoExecutionPath proves an unknown run (folded projection is
// warm but has never seen this run) yields a clear 4xx rather than a 500 or a
// silent path_unknown.
func TestRunDiffUnknownRunNoExecutionPath(t *testing.T) {
	cityDir := initGitRepo(t, t.TempDir())
	// run1 is known, so the projection is warm and has a run — gc-unknown genuinely
	// is not folded.
	writeEventLog(t, filepath.Join(cityDir, ".gc", "events.jsonl"), runRootWithCwd(cityDir))

	p := New(Deps{Resolver: fakeResolver{paths: map[string]string{"alpha": cityDir}}})
	p.Start(t.Context())
	defer p.Stop()

	rec := postRunDiff(t, p, "/api/city/alpha/runs/gc-unknown/diff", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d (body %s), want 400 for an unknown run", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "run has no known execution path") {
		t.Errorf("body = %s, want a no-known-execution-path error", rec.Body.String())
	}
}

// TestRunDiffServerResolvedUnavailablePath proves a KNOWN run whose beads carry no
// cwd/work_dir/rig_root (executionPath = unavailable) also yields the clear 4xx —
// the server-resolve path treats "kind != known" as no diffable folder, distinct
// from a client that explicitly asks for the path_unknown shape.
func TestRunDiffServerResolvedUnavailablePath(t *testing.T) {
	cityDir := initGitRepo(t, t.TempDir())
	// runDetailRootEvent (run1) carries no cwd/work_dir/rig_root metadata.
	writeEventLog(t, filepath.Join(cityDir, ".gc", "events.jsonl"), runDetailRootEvent())

	p := New(Deps{Resolver: fakeResolver{paths: map[string]string{"alpha": cityDir}}})
	p.Start(t.Context())
	defer p.Stop()

	rec := postRunDiff(t, p, "/api/city/alpha/runs/run1/diff", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d (body %s), want 400 when the run has no known execution path", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "run has no known execution path") {
		t.Errorf("body = %s, want a no-known-execution-path error", rec.Body.String())
	}
}
