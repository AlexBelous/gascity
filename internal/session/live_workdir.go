package session

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gastownhall/gascity/internal/pathutil"
	"github.com/gastownhall/gascity/internal/runtime"
)

// ResolveLiveWorkDir reports the working directory the session's live
// process actually occupies. This is authoritative over stored metadata:
// pooled sessions can share a base WorkDir in their bead while each live
// process actually runs in a distinct per-instance directory, so callers
// that want to see real collisions must prefer this over info.WorkDir.
//
// It falls back to the trimmed info.WorkDir whenever the live value cannot
// be determined: sp does not support process-table scanning, info.ID is
// empty, the scan errors, no live runtime matches info.ID, the runtime has
// no resolvable PID, or that PID's cwd cannot be read (dead process,
// unreadable, or deleted out from under it). The session is never dropped —
// only the reported directory degrades to stored metadata.
func ResolveLiveWorkDir(sp runtime.Provider, info Info) string {
	fallback := strings.TrimSpace(info.WorkDir)

	id := strings.TrimSpace(info.ID)
	if id == "" {
		return fallback
	}

	scanner, ok := sp.(runtime.ProcessTableScanner)
	if !ok {
		return fallback
	}

	runtimes, err := scanner.FindRuntimesBySessionID(id)
	if err != nil {
		return fallback
	}

	for _, rt := range runtimes {
		if rt.SessionID != id || rt.PID <= 0 {
			continue
		}
		if cwd, ok := readProcessCwd(rt.PID); ok {
			return cwd
		}
	}
	return fallback
}

// readProcessCwd reads and canonicalizes the live cwd of pid via
// /proc/<pid>/cwd, matching the idiom in cmd/gc/bead_worktree_liveness.go's
// collectLiveWorktreeState. It reports ok=false for a dead/unreadable PID or
// a cwd whose directory has been unlinked ("... (deleted)").
func readProcessCwd(pid int) (string, bool) {
	link, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "cwd"))
	if err != nil || link == "" || strings.HasSuffix(link, " (deleted)") {
		return "", false
	}
	canon := pathutil.NormalizePathForCompare(link)
	if canon == "" {
		return "", false
	}
	return canon, true
}
