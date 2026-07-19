package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gascitypacks "github.com/gastownhall/gascity-packs"

	"github.com/gastownhall/gascity/internal/builtinpacks"
)

// fakeSetupScriptAt writes an executable fake worktree-setup.sh at
// scriptDir/worktree-setup.sh that records every argument it receives (one per
// line, in order) to recordFile, then exits with exitCode. Tests exercise
// doWorktreeHQ's contract with the script (argv shape, exit-code propagation)
// without invoking the real script.
func fakeSetupScriptAt(t *testing.T, scriptDir, recordFile string, exitCode int) {
	t.Helper()
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf("#!/bin/sh\nfor a in \"$@\"; do printf '%%s\\n' \"$a\" >> '%s'; done\nexit %d\n", recordFile, exitCode)
	scriptPath := filepath.Join(scriptDir, "worktree-setup.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// fakeSetupScript writes the fake worktree-setup.sh into the city's
// materialized scripts directory (cityPath/.gc/scripts), which is the first
// location resolveHQWorktreeSetupScript searches.
func fakeSetupScript(t *testing.T, cityPath, recordFile string, exitCode int) {
	t.Helper()
	fakeSetupScriptAt(t, filepath.Join(cityPath, ".gc", "scripts"), recordFile, exitCode)
}

func readRecordedArgs(t *testing.T, recordFile string) []string {
	t.Helper()
	data, err := os.ReadFile(recordFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	trimmed := strings.TrimSuffix(string(data), "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

func TestDoWorktreeHQInvokesSetupScriptWithSync(t *testing.T) {
	cityDir := t.TempDir()
	recordFile := filepath.Join(t.TempDir(), "argv.txt")
	fakeSetupScript(t, cityDir, recordFile, 0)
	t.Setenv("GC_TEMPLATE", "gascity/builder")
	t.Setenv("GC_AGENT", "")

	var stdout, stderr bytes.Buffer
	path, err := doWorktreeHQ(cityDir, nil, "ga-34q3ss", &stdout, &stderr)
	if err != nil {
		t.Fatalf("doWorktreeHQ() error = %v, stderr = %s", err, stderr.String())
	}

	wantPath := filepath.Join(cityDir, ".gc", "worktrees", "_hq", "builder-ga-34q3ss")
	if path != wantPath {
		t.Errorf("doWorktreeHQ() path = %q, want %q", path, wantPath)
	}

	got := readRecordedArgs(t, recordFile)
	want := []string{cityDir, wantPath, "builder", "--sync"}
	if len(got) != len(want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("argv[%d] = %q, want %q (full argv %q)", i, got[i], want[i], got)
		}
	}
	// The command must pass the script's real --sync mode, never a flag no
	// worktree-setup.sh honors. Regression guard for PR #4243 review: the
	// original code passed --freshen-commit, which every real script ignores.
	for _, forbidden := range got {
		if forbidden == "--freshen-commit" || forbidden == "--reset-main" {
			t.Fatalf("argv %q must never contain %q", got, forbidden)
		}
	}
}

func TestDoWorktreeHQWritesBeadsRedirect(t *testing.T) {
	cityDir := t.TempDir()
	recordFile := filepath.Join(t.TempDir(), "argv.txt")
	fakeSetupScript(t, cityDir, recordFile, 0)
	t.Setenv("GC_TEMPLATE", "gascity/builder")

	var stdout, stderr bytes.Buffer
	path, err := doWorktreeHQ(cityDir, nil, "ga-34q3ss", &stdout, &stderr)
	if err != nil {
		t.Fatalf("doWorktreeHQ() error = %v, stderr = %s", err, stderr.String())
	}

	redirectPath := filepath.Join(path, ".beads", "redirect")
	data, err := os.ReadFile(redirectPath)
	if err != nil {
		t.Fatalf("reading %s: %v", redirectPath, err)
	}
	got := strings.TrimSpace(string(data))
	want := filepath.Join(cityDir, ".beads")
	if got != want {
		t.Errorf(".beads/redirect content = %q, want %q", got, want)
	}
}

func TestDoWorktreeHQIsIdempotent(t *testing.T) {
	cityDir := t.TempDir()
	recordFile := filepath.Join(t.TempDir(), "argv.txt")
	fakeSetupScript(t, cityDir, recordFile, 0)
	t.Setenv("GC_TEMPLATE", "gascity/builder")

	var stdout, stderr bytes.Buffer
	path1, err := doWorktreeHQ(cityDir, nil, "ga-34q3ss", &stdout, &stderr)
	if err != nil {
		t.Fatalf("doWorktreeHQ() first call error = %v", err)
	}
	path2, err := doWorktreeHQ(cityDir, nil, "ga-34q3ss", &stdout, &stderr)
	if err != nil {
		t.Fatalf("doWorktreeHQ() second call error = %v", err)
	}
	if path1 != path2 {
		t.Errorf("doWorktreeHQ() paths differ across calls: %q vs %q", path1, path2)
	}

	got := readRecordedArgs(t, recordFile)
	// Two invocations of 4 args each, recorded back-to-back.
	if len(got) != 8 {
		t.Fatalf("expected script to be invoked twice (8 recorded args), got %d: %q", len(got), got)
	}
}

func TestDoWorktreeHQPropagatesScriptFailure(t *testing.T) {
	cityDir := t.TempDir()
	recordFile := filepath.Join(t.TempDir(), "argv.txt")
	fakeSetupScript(t, cityDir, recordFile, 1)
	t.Setenv("GC_TEMPLATE", "gascity/builder")

	var stdout, stderr bytes.Buffer
	_, err := doWorktreeHQ(cityDir, nil, "ga-34q3ss", &stdout, &stderr)
	if err == nil {
		t.Fatal("doWorktreeHQ() error = nil, want error on script failure")
	}

	worktreeDir := filepath.Join(cityDir, ".gc", "worktrees", "_hq", "builder-ga-34q3ss")
	if _, statErr := os.Stat(filepath.Join(worktreeDir, ".beads", "redirect")); !os.IsNotExist(statErr) {
		t.Errorf(".beads/redirect should not be written when the setup script fails")
	}
}

func TestDoWorktreeHQRejectsPathTraversalBeadID(t *testing.T) {
	cityDir := t.TempDir()
	recordFile := filepath.Join(t.TempDir(), "argv.txt")
	fakeSetupScript(t, cityDir, recordFile, 0)
	t.Setenv("GC_TEMPLATE", "gascity/builder")

	var stdout, stderr bytes.Buffer
	_, err := doWorktreeHQ(cityDir, nil, "../../../escape", &stdout, &stderr)
	if err == nil {
		t.Fatal("doWorktreeHQ() error = nil, want error for a path-traversal bead ID")
	}

	if got := readRecordedArgs(t, recordFile); got != nil {
		t.Errorf("setup script should not be invoked for a path-traversal bead ID, got argv %q", got)
	}

	escaped := filepath.Join(cityDir, ".gc", "worktrees", "escape")
	if _, statErr := os.Stat(escaped); !os.IsNotExist(statErr) {
		t.Errorf("path-traversal bead ID must not create anything outside the HQ bucket, found %s", escaped)
	}
}

func TestDoWorktreeHQMissingBeadID(t *testing.T) {
	cityDir := t.TempDir()
	t.Setenv("GC_TEMPLATE", "gascity/builder")

	var stdout, stderr bytes.Buffer
	if _, err := doWorktreeHQ(cityDir, nil, "   ", &stdout, &stderr); err == nil {
		t.Fatal("doWorktreeHQ() error = nil, want error for blank bead ID")
	}
}

func TestDoWorktreeHQMissingCallingRole(t *testing.T) {
	cityDir := t.TempDir()
	recordFile := filepath.Join(t.TempDir(), "argv.txt")
	fakeSetupScript(t, cityDir, recordFile, 0)
	t.Setenv("GC_TEMPLATE", "")
	t.Setenv("GC_AGENT", "")

	var stdout, stderr bytes.Buffer
	if _, err := doWorktreeHQ(cityDir, nil, "ga-34q3ss", &stdout, &stderr); err == nil {
		t.Fatal("doWorktreeHQ() error = nil, want error when no calling role can be resolved")
	}
	if got := readRecordedArgs(t, recordFile); got != nil {
		t.Errorf("setup script should not be invoked when calling role is unresolved, got argv %q", got)
	}
}

// TestDoWorktreeHQResolvesScriptFromPackAssetsDir proves the command finds
// worktree-setup.sh through a configured pack's assets/scripts directory — the
// same {{.ConfigDir}}/assets/scripts/worktree-setup.sh layout rig worktrees
// use — rather than a hardcoded packs/gastown/scripts path that does not exist
// in an imported-pack city.
func TestDoWorktreeHQResolvesScriptFromPackAssetsDir(t *testing.T) {
	cityDir := t.TempDir()
	packDir := t.TempDir()
	recordFile := filepath.Join(t.TempDir(), "argv.txt")
	fakeSetupScriptAt(t, filepath.Join(packDir, "assets", "scripts"), recordFile, 0)
	t.Setenv("GC_TEMPLATE", "gascity/builder")

	var stdout, stderr bytes.Buffer
	path, err := doWorktreeHQ(cityDir, []string{packDir}, "ga-34q3ss", &stdout, &stderr)
	if err != nil {
		t.Fatalf("doWorktreeHQ() error = %v, stderr = %s", err, stderr.String())
	}

	wantPath := filepath.Join(cityDir, ".gc", "worktrees", "_hq", "builder-ga-34q3ss")
	if path != wantPath {
		t.Errorf("doWorktreeHQ() path = %q, want %q", path, wantPath)
	}
	got := readRecordedArgs(t, recordFile)
	want := []string{cityDir, wantPath, "builder", "--sync"}
	if len(got) != len(want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("argv[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestResolveHQWorktreeSetupScript(t *testing.T) {
	t.Run("materialized .gc/scripts", func(t *testing.T) {
		cityDir := t.TempDir()
		gcScripts := filepath.Join(cityDir, ".gc", "scripts")
		fakeSetupScriptAt(t, gcScripts, filepath.Join(t.TempDir(), "rec.txt"), 0)

		got, err := resolveHQWorktreeSetupScript(cityDir, nil)
		if err != nil {
			t.Fatalf("resolveHQWorktreeSetupScript() error = %v", err)
		}
		if want := filepath.Join(gcScripts, "worktree-setup.sh"); got != want {
			t.Errorf("resolved = %q, want %q", got, want)
		}
	})

	t.Run("city scripts dir", func(t *testing.T) {
		cityDir := t.TempDir()
		scripts := filepath.Join(cityDir, "scripts")
		fakeSetupScriptAt(t, scripts, filepath.Join(t.TempDir(), "rec.txt"), 0)

		got, err := resolveHQWorktreeSetupScript(cityDir, nil)
		if err != nil {
			t.Fatalf("resolveHQWorktreeSetupScript() error = %v", err)
		}
		if want := filepath.Join(scripts, "worktree-setup.sh"); got != want {
			t.Errorf("resolved = %q, want %q", got, want)
		}
	})

	t.Run("pack assets/scripts dir", func(t *testing.T) {
		cityDir := t.TempDir()
		packDir := t.TempDir()
		assets := filepath.Join(packDir, "assets", "scripts")
		fakeSetupScriptAt(t, assets, filepath.Join(t.TempDir(), "rec.txt"), 0)

		got, err := resolveHQWorktreeSetupScript(cityDir, []string{packDir})
		if err != nil {
			t.Fatalf("resolveHQWorktreeSetupScript() error = %v", err)
		}
		if want := filepath.Join(assets, "worktree-setup.sh"); got != want {
			t.Errorf("resolved = %q, want %q", got, want)
		}
	})

	t.Run("prefers .gc/scripts over pack dir", func(t *testing.T) {
		cityDir := t.TempDir()
		gcScripts := filepath.Join(cityDir, ".gc", "scripts")
		fakeSetupScriptAt(t, gcScripts, filepath.Join(t.TempDir(), "rec.txt"), 0)
		packDir := t.TempDir()
		fakeSetupScriptAt(t, filepath.Join(packDir, "assets", "scripts"), filepath.Join(t.TempDir(), "rec.txt"), 0)

		got, err := resolveHQWorktreeSetupScript(cityDir, []string{packDir})
		if err != nil {
			t.Fatalf("resolveHQWorktreeSetupScript() error = %v", err)
		}
		if want := filepath.Join(gcScripts, "worktree-setup.sh"); got != want {
			t.Errorf("resolved = %q, want %q (should prefer materialized city scripts)", got, want)
		}
	})

	t.Run("not found names searched locations", func(t *testing.T) {
		cityDir := t.TempDir()
		packDir := t.TempDir()
		_, err := resolveHQWorktreeSetupScript(cityDir, []string{packDir})
		if err == nil {
			t.Fatal("resolveHQWorktreeSetupScript() error = nil, want not-found error")
		}
		msg := err.Error()
		for _, want := range []string{"worktree-setup.sh", filepath.Join(cityDir, ".gc", "scripts"), filepath.Join(packDir, "assets", "scripts")} {
			if !strings.Contains(msg, want) {
				t.Errorf("error %q does not name searched location %q", msg, want)
			}
		}
	})
}

// TestDoWorktreeHQWithRealEmbeddedScript exercises doWorktreeHQ against the
// real embedded gastown worktree-setup.sh (the script rigs actually run),
// materialized into the city's .gc/scripts, in a real git repo. It proves the
// resolved path and --sync mode provision an actual git worktree — the
// coverage the fake argv-recording tests cannot provide.
func TestDoWorktreeHQWithRealEmbeddedScript(t *testing.T) {
	cityPath := makeHQCityRepo(t)
	materializeEmbeddedWorktreeSetupScript(t, filepath.Join(cityPath, ".gc", "scripts"))
	t.Setenv("GC_TEMPLATE", "gascity/builder")

	var stdout, stderr bytes.Buffer
	path, err := doWorktreeHQ(cityPath, nil, "ga-34q3ss", &stdout, &stderr)
	if err != nil {
		t.Fatalf("doWorktreeHQ() error = %v, stderr = %s", err, stderr.String())
	}

	// A real git worktree must exist at the returned path.
	if _, statErr := os.Stat(filepath.Join(path, ".git")); statErr != nil {
		t.Fatalf("expected a real git worktree at %s: %v", path, statErr)
	}
	branch := strings.TrimSpace(gitOutputImport(t, path, "rev-parse", "--abbrev-ref", "HEAD"))
	if !strings.HasPrefix(branch, "gc-builder-") {
		t.Errorf("worktree branch = %q, want gc-builder-<hash> pattern", branch)
	}

	// .beads/redirect must point at the city's own beads store.
	data, err := os.ReadFile(filepath.Join(path, ".beads", "redirect"))
	if err != nil {
		t.Fatalf("reading .beads/redirect: %v", err)
	}
	if got, want := strings.TrimSpace(string(data)), filepath.Join(cityPath, ".beads"); got != want {
		t.Errorf(".beads/redirect = %q, want %q", got, want)
	}
}

// materializeEmbeddedWorktreeSetupScript writes the embedded gastown
// worktree-setup.sh (the source of truth for the script rigs run) into dstDir.
func materializeEmbeddedWorktreeSetupScript(t *testing.T, dstDir string) {
	t.Helper()
	const rel = "assets/scripts/worktree-setup.sh"
	data, err := fs.ReadFile(gascitypacks.Gastown(), rel)
	if err != nil {
		t.Fatalf("reading embedded gastown %s: %v", rel, err)
	}
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dstDir, "worktree-setup.sh")
	if err := os.WriteFile(dst, data, builtinpacks.MaterializedFileMode(rel)); err != nil {
		t.Fatalf("materializing %s: %v", rel, err)
	}
}

func TestResolveCallingRolePrefersTemplateOverAgent(t *testing.T) {
	t.Setenv("GC_TEMPLATE", "gascity/builder")
	t.Setenv("GC_AGENT", "gascity/reviewer")

	if got := resolveCallingRole(); got != "builder" {
		t.Errorf("resolveCallingRole() = %q, want %q", got, "builder")
	}
}

func TestResolveCallingRoleFallsBackToAgent(t *testing.T) {
	t.Setenv("GC_TEMPLATE", "")
	t.Setenv("GC_AGENT", "gascity/reviewer")

	if got := resolveCallingRole(); got != "reviewer" {
		t.Errorf("resolveCallingRole() = %q, want %q", got, "reviewer")
	}
}

func TestResolveCallingRoleUnqualifiedIdentity(t *testing.T) {
	t.Setenv("GC_TEMPLATE", "")
	t.Setenv("GC_AGENT", "mayor")

	if got := resolveCallingRole(); got != "mayor" {
		t.Errorf("resolveCallingRole() = %q, want %q", got, "mayor")
	}
}

func TestResolveCallingRoleEmptyWhenUnset(t *testing.T) {
	t.Setenv("GC_TEMPLATE", "")
	t.Setenv("GC_AGENT", "")

	if got := resolveCallingRole(); got != "" {
		t.Errorf("resolveCallingRole() = %q, want empty", got)
	}
}
