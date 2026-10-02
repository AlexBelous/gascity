package beadstest

import "testing"

// TestPinnedBeadsModuleDirResolvesThisBuildsOwnCache is the live half of the
// resolver's fence: the subtests in pinned_module_test.go feed it temporary
// directories, and this one resolves the real host cache. Compiled metadata
// identifies the source when present; otherwise the repository manifest supplies
// its declared pin. Both paths must retain the replacement used by the checkout
// whose pinned-cursor drift check calls this helper.
//
// It lives in a file of its own because it is the one test in this package that
// cannot run hermetically. PinnedBeadsModuleDir walks up from os.Getwd() for
// go.mod and then reads GOMODCACHE/GOPATH/GOENV/HOME; under a bazel test sandbox
// the working directory is a runfiles tree with no go.mod above it and the
// environment is scrubbed, so the resolver fails — and it is meant to fail
// rather than skip (that is what pinnedBeadsModuleDirOrFatal's recorder pins).
// Separating it lets internal/beads/beadstest/BUILD.bazel defer this file alone
// from the hermetic Bazel target while every other test in the package, this
// one included, keeps running in the Go suite.
func TestPinnedBeadsModuleDirResolvesThisBuildsOwnCache(t *testing.T) {
	if dir := PinnedBeadsModuleDir(t); dir == "" {
		t.Fatal("PinnedBeadsModuleDir returned no directory")
	}
}
