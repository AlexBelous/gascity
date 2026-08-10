package main

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// revisionOrderingComparison matches an identifier ending in Revision compared
// against a numeric literal with an ORDERING operator — the shape every fence
// site had drifted into.
var revisionOrderingComparison = regexp.MustCompile(`[A-Za-z_.]*[Rr]evision\s*(<=|>=|<|>)\s*-?[0-9]`)

// TestRevisionConsumersNeverOrderARevision is the standing guard for
// ga-f7v2ft.140 and .141. The revision contract on beads.ConditionalWriter says
// a revision is an opaque token callers may test only for EQUALITY, with zero as
// the "unavailable" sentinel; ordering is undefined. Two independent fence sites
// nevertheless drifted to `> 0` / `<= 0`, and because bd hands out SIGNED
// revisions each one silently misclassified the negative half of every city's
// rows — one failing closed (the advisory status heal, the trigger rebind, the
// recovery lease) and one failing open (the pre-wake incarnation commit, whose
// CAS simply never ran). Both defects were invisible to every existing test
// because the native Mem/File stores mint small positive counters.
//
// Revision CONSUMERS are scanned; the stores that MINT revisions
// (internal/beads) legitimately order their own tokens. Use beads.RevisionKnown
// for the known/unavailable question and plain equality for everything else.
//
// Scope: the SIGN family only — a revision compared against a numeric literal.
// Revision ARITHMETIC (`revision + 1` and the ordering guard around it) is the
// same contract violation from the other direction and is tracked separately;
// see ga-f7v2ft.144.
func TestRevisionConsumersNeverOrderARevision(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	cmdGC := filepath.Dir(currentFile)
	repoRoot := filepath.Dir(filepath.Dir(cmdGC))
	for _, dir := range []string{cmdGC, filepath.Join(repoRoot, "internal", "session")} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("ReadDir(%q): %v", dir, err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(dir, name)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile(%q): %v", path, err)
			}
			for i, line := range strings.Split(string(data), "\n") {
				if match := revisionOrderingComparison.FindString(line); match != "" {
					t.Errorf("%s:%d orders a revision (%q): revisions are opaque and signed — test beads.RevisionKnown for known/unavailable and equality for everything else",
						path, i+1, match)
				}
			}
		}
	}
}
