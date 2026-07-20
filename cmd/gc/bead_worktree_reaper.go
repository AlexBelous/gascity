package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/doctor"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/git"
	"github.com/gastownhall/gascity/internal/sling"
)

// reapClosedBeadWorktrees scans per-bead git worktrees under
// cityPath/.gc/worktrees/<rig>/ and removes any that are associated with a
// closed bead and pass all safety gates (no uncommitted work, no unpushed
// commits, no stashes). Named session home directories are never removed.
// Returns the number of worktrees successfully removed.
func reapClosedBeadWorktrees(
	cityPath string,
	cfg *config.City,
	rigBeadStores map[string]beads.Store,
	rec events.Recorder,
	stderr io.Writer,
) int {
	if stderr == nil {
		stderr = io.Discard
	}
	if rec == nil {
		rec = events.Discard
	}
	if cfg == nil || len(rigBeadStores) == 0 {
		return 0
	}

	// Build a guard set of session home names so agent template directories
	// are never touched.
	sessionHomes := make(map[string]bool, len(cfg.Agents))
	for i := range cfg.Agents {
		if name := cfg.Agents[i].BindingQualifiedName(); name != "" {
			sessionHomes[name] = true
		}
	}

	wtRoot := filepath.Join(cityPath, ".gc", "worktrees")
	reaped := 0

	for rigName, store := range rigBeadStores {
		if store == nil {
			continue
		}
		rigWorktreeDir := filepath.Join(wtRoot, rigName)
		entries, err := os.ReadDir(rigWorktreeDir)
		if err != nil {
			if !os.IsNotExist(err) {
				fmt.Fprintf(stderr, "reapClosedBeadWorktrees: reading %s: %v\n", rigWorktreeDir, err) //nolint:errcheck
			}
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			name := entry.Name()

			// Session home guard: never touch agent template directories.
			if sessionHomes[name] {
				continue
			}

			// Extract a bead ID candidate from the directory name.
			beadID := extractBeadIDFromWorktreeName(cfg, name)
			if beadID == "" {
				continue
			}

			// Confirm the bead exists and is closed in this rig's store.
			bead, err := store.Get(beadID)
			if err != nil || bead.Status != "closed" {
				// ErrNotFound, transient error, or bead not yet closed — skip.
				continue
			}

			worktreePath := filepath.Join(rigWorktreeDir, name)

			// Scope gate: only act on paths strictly under the worktree root.
			if !isStrictlyUnderDir(wtRoot, worktreePath) {
				continue
			}

			// Safety checks: run from the worktree directory so git status
			// and stash list apply to the worktree's branch.
			wg := git.New(worktreePath)
			hasUncommitted := wg.HasUncommittedWork()
			hasUnpushed, _ := wg.HasUnpushedCommitsResult()
			hasStashes, _ := wg.HasStashesResult()

			if hasUncommitted || hasUnpushed || hasStashes {
				reason := fmt.Sprintf("uncommitted=%v unpushed=%v stashes=%v", hasUncommitted, hasUnpushed, hasStashes)
				fmt.Fprintf(stderr, //nolint:errcheck
					"reapClosedBeadWorktrees: skipping %s (bead %s closed but unsafe: %s)\n",
					worktreePath, beadID, reason,
				)
				if raw, err := json.Marshal(events.BeadWorktreeReapSkippedPayload{
					BeadID: beadID,
					Path:   worktreePath,
					Rig:    rigName,
					Reason: reason,
				}); err == nil {
					rec.Record(events.Event{
						Type:    events.BeadWorktreeReapSkipped,
						Actor:   "gc",
						Subject: beadID,
						Payload: raw,
					})
				}
				continue
			}

			// Capture branch before removal — the worktree dir will be gone after.
			branch, _ := wg.CurrentBranch()

			// Containment guard: never remove a worktree that still has a
			// nested worktree living inside it. Nested reaping runs before
			// this phase within a tick, so anything still nested here was
			// deliberately left behind — removing worktreePath would
			// destroy that content as collateral damage.
			if blocked, reason := blockedByNestedWorktree(worktreePath); blocked {
				fmt.Fprintf(stderr, //nolint:errcheck
					"reapClosedBeadWorktrees: skipping %s (bead %s closed but %s)\n",
					worktreePath, beadID, reason,
				)
				if raw, err := json.Marshal(events.BeadWorktreeReapSkippedPayload{
					BeadID: beadID,
					Path:   worktreePath,
					Rig:    rigName,
					Reason: reason,
				}); err == nil {
					rec.Record(events.Event{
						Type:    events.BeadWorktreeReapSkipped,
						Actor:   "gc",
						Subject: beadID,
						Payload: raw,
					})
				}
				continue
			}

			// Remove the worktree. git worktree remove must be run from the
			// main repo root, not from within the worktree being removed.
			mainRepo := git.New(cityPath)
			if err := mainRepo.WorktreeRemove(worktreePath, false); err != nil {
				fmt.Fprintf(stderr, "reapClosedBeadWorktrees: removing %s: %v\n", worktreePath, err) //nolint:errcheck
				continue
			}
			fmt.Fprintf(stderr, //nolint:errcheck
				"reapClosedBeadWorktrees: removed worktree %s for closed bead %s\n",
				worktreePath, beadID,
			)
			if raw, err := json.Marshal(events.BeadWorktreeReapedPayload{
				BeadID: beadID,
				Path:   worktreePath,
				Rig:    rigName,
				Branch: branch,
			}); err == nil {
				rec.Record(events.Event{
					Type:    events.BeadWorktreeReaped,
					Actor:   "gc",
					Subject: beadID,
					Payload: raw,
				})
			}
			reaped++
		}
	}
	return reaped
}

// reapNestedClosedBeadWorktrees scans the city for nested git worktrees
// (worktrees registered via `git worktree add` inside another worktree's own
// root, at any nesting depth) and removes any that are associated with a
// closed bead, pass all safety gates, and have no worktree still nested
// inside them. Must run before reapClosedBeadWorktrees within a tick: that
// ordering is what lets the containment guard below correctly defer an
// outer nested worktree to a later tick instead of destroying whatever is
// still living inside it. Returns the number of worktrees successfully
// removed.
func reapNestedClosedBeadWorktrees(
	cityPath string,
	cfg *config.City,
	rigBeadStores map[string]beads.Store,
	rec events.Recorder,
	stderr io.Writer,
) int {
	if stderr == nil {
		stderr = io.Discard
	}
	if rec == nil {
		rec = events.Discard
	}
	if cfg == nil || len(rigBeadStores) == 0 {
		return 0
	}

	findings, listingErrs, err := doctor.DiscoverNestedWorktrees(cityPath, newNestedWorktreeGitProbe)
	for _, e := range listingErrs {
		fmt.Fprintf(stderr, "reapNestedClosedBeadWorktrees: %s\n", e) //nolint:errcheck
	}
	if err != nil {
		fmt.Fprintf(stderr, "reapNestedClosedBeadWorktrees: discovering nested worktrees: %v\n", err) //nolint:errcheck
		return 0
	}
	if len(findings) == 0 {
		return 0
	}

	// Build a guard set of session home names so agent template directories
	// are never touched, even if one happens to sit at a nested path.
	sessionHomes := make(map[string]bool, len(cfg.Agents))
	for i := range cfg.Agents {
		if name := cfg.Agents[i].BindingQualifiedName(); name != "" {
			sessionHomes[name] = true
		}
	}

	wtRoot := filepath.Join(cityPath, ".gc", "worktrees")
	reaped := 0

	for _, f := range findings {
		rigName, ok := rigNameForWorktreePath(wtRoot, f.Path)
		if !ok {
			continue
		}
		store := rigBeadStores[rigName]
		if store == nil {
			continue
		}

		name := filepath.Base(f.Path)
		if sessionHomes[name] {
			continue
		}

		beadID := extractBeadIDFromWorktreeName(cfg, name)
		if beadID == "" {
			continue
		}

		// Confirm the bead exists and is closed in this rig's store.
		bead, err := store.Get(beadID)
		if err != nil || bead.Status != "closed" {
			// ErrNotFound, transient error, or bead not yet closed — skip.
			continue
		}

		if !f.SafeToRm {
			fmt.Fprintf(stderr, //nolint:errcheck
				"reapNestedClosedBeadWorktrees: skipping %s (bead %s closed but unsafe: %s)\n",
				f.Path, beadID, f.Reason,
			)
			if raw, err := json.Marshal(events.BeadWorktreeReapSkippedPayload{
				BeadID: beadID,
				Path:   f.Path,
				Rig:    rigName,
				Reason: f.Reason,
			}); err == nil {
				rec.Record(events.Event{
					Type:    events.BeadWorktreeReapSkipped,
					Actor:   "gc",
					Subject: beadID,
					Payload: raw,
				})
			}
			continue
		}

		// Containment guard: never remove a nested worktree that itself
		// still has a worktree nested inside it — a doubly-nested worktree
		// surfaces as its own separate finding (see DiscoverNestedWorktrees),
		// so without this check an outer finding could be removed as
		// collateral damage while something deeper was still deliberately
		// left behind by this same pass.
		if blocked, reason := blockedByNestedWorktree(f.Path); blocked {
			fmt.Fprintf(stderr, //nolint:errcheck
				"reapNestedClosedBeadWorktrees: skipping %s (bead %s closed but %s)\n",
				f.Path, beadID, reason,
			)
			if raw, err := json.Marshal(events.BeadWorktreeReapSkippedPayload{
				BeadID: beadID,
				Path:   f.Path,
				Rig:    rigName,
				Reason: reason,
			}); err == nil {
				rec.Record(events.Event{
					Type:    events.BeadWorktreeReapSkipped,
					Actor:   "gc",
					Subject: beadID,
					Payload: raw,
				})
			}
			continue
		}

		// Remove the worktree, rooted at its immediate parent (guaranteed to
		// share its git admin dir by construction of a NestedWorktreeFinding)
		// — mirrors NestedWorktreePruneCheck.Fix()'s established convention
		// for removing a nested finding.
		if err := newNestedWorktreeGitProbe(f.Parent).WorktreeRemove(f.Path, true); err != nil {
			fmt.Fprintf(stderr, "reapNestedClosedBeadWorktrees: removing %s: %v\n", f.Path, err) //nolint:errcheck
			continue
		}
		fmt.Fprintf(stderr, //nolint:errcheck
			"reapNestedClosedBeadWorktrees: removed nested worktree %s for closed bead %s\n",
			f.Path, beadID,
		)
		if raw, err := json.Marshal(events.BeadWorktreeReapedPayload{
			BeadID: beadID,
			Path:   f.Path,
			Rig:    rigName,
			Branch: f.Branch,
		}); err == nil {
			rec.Record(events.Event{
				Type:    events.BeadWorktreeReaped,
				Actor:   "gc",
				Subject: beadID,
				Payload: raw,
			})
		}
		reaped++
	}
	return reaped
}

// extractBeadIDFromWorktreeName scans consecutive dash-separated segment pairs
// in name for one that LooksLikeConfiguredBeadID. Returns the first match, or
// "" if none. Handles names like "builder-ga-34q3ss-pr2738" → "ga-34q3ss" and
// bare "ga-06kfi6" → "ga-06kfi6".
func extractBeadIDFromWorktreeName(cfg *config.City, name string) string {
	if name == "" || cfg == nil {
		return ""
	}
	parts := strings.Split(name, "-")
	for i := 0; i+1 < len(parts); i++ {
		candidate := parts[i] + "-" + parts[i+1]
		if sling.LooksLikeConfiguredBeadID(cfg, candidate) {
			return candidate
		}
	}
	return ""
}

// isStrictlyUnderDir reports whether path is strictly contained within dir
// (i.e., it is not dir itself and has dir as a prefix component).
func isStrictlyUnderDir(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != "." && !strings.HasPrefix(rel, "..")
}

// rigNameForWorktreePath returns the rig name owning a worktree path under
// wtRoot (cityPath/.gc/worktrees) — the first path segment after wtRoot.
// Works for arbitrarily deep nested worktree paths since only that first
// segment is used. ok is false if path is not strictly under wtRoot.
func rigNameForWorktreePath(wtRoot, path string) (string, bool) {
	rel, err := filepath.Rel(wtRoot, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", false
	}
	parts := strings.SplitN(rel, string(filepath.Separator), 2)
	return parts[0], true
}
