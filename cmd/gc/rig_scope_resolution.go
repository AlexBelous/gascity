package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gastownhall/gascity/internal/config"
)

func rigForDir(cfg *config.City, cityPath, dir string) (config.Rig, bool) {
	rig, ok, _ := resolveRigForDir(cfg, cityPath, dir)
	return rig, ok
}

func resolveRigForDir(cfg *config.City, cityPath, dir string) (config.Rig, bool, error) {
	dir = normalizePathForCompare(dir)
	resolveRigPaths(cityPath, cfg.Rigs)
	for _, rig := range cfg.Rigs {
		if strings.TrimSpace(rig.Path) == "" {
			continue
		}
		rigPath := normalizePathForCompare(resolveStoreScopeRoot(cityPath, rig.Path))
		if pathWithinScope(dir, rigPath) {
			return rig, true, nil
		}
	}
	return rigFromRedirectedBeadsDir(cfg, cityPath, dir)
}

func rigFromRedirectedBeadsDir(cfg *config.City, cityPath, dir string) (config.Rig, bool, error) {
	// Redirect resolution is meaningful only when cwd lies inside cityPath.
	// When tests or commands run with a cwd outside the declared city tree
	// (e.g., a polecat worktree under a different gc city), walking up the
	// cwd chain would pick up unrelated .beads/redirect files and either
	// mis-route the command or hard-error against the test's fake cfg.
	cityScope := normalizePathForCompare(cityPath)
	cityBeadsDir := normalizePathForCompare(filepath.Join(cityPath, ".beads"))
	if !pathWithinScope(normalizePathForCompare(dir), cityScope) {
		return config.Rig{}, false, nil
	}
	for current := dir; current != "" && current != filepath.Dir(current); current = filepath.Dir(current) {
		if !pathWithinScope(normalizePathForCompare(current), cityScope) {
			break
		}
		redirectPath := filepath.Join(current, ".beads", "redirect")
		redirectTarget, err := os.ReadFile(redirectPath)
		if err != nil {
			continue
		}
		targetBeadsDir := normalizePathForCompare(strings.TrimSpace(string(redirectTarget)))
		if targetBeadsDir == "" {
			continue
		}
		if targetBeadsDir == cityBeadsDir {
			// Unified worktrees bind directly to the city workspace. Their rig
			// identity remains in the canonical .gc/worktrees/<rig>/ path so
			// actor routing and hooks still carry the rig prefix.
			if rig, ok := rigForUnifiedWorktree(cfg, cityPath, dir); ok {
				return rig, true, nil
			}
			return config.Rig{}, false, nil
		}
		for _, rig := range cfg.Rigs {
			if strings.TrimSpace(rig.Path) == "" {
				continue
			}
			rigBeadsDir := normalizePathForCompare(filepath.Join(resolveStoreScopeRoot(cityPath, rig.Path), ".beads"))
			if targetBeadsDir == rigBeadsDir {
				return rig, true, nil
			}
		}
		return config.Rig{}, false, fmt.Errorf("cwd redirect %s points outside declared city rigs", redirectPath)
	}
	return config.Rig{}, false, nil
}

func rigForUnifiedWorktree(cfg *config.City, cityPath, dir string) (config.Rig, bool) {
	rel, err := filepath.Rel(cityPath, dir)
	if err != nil {
		return config.Rig{}, false
	}
	parts := strings.Split(filepath.Clean(rel), string(filepath.Separator))
	if len(parts) < 3 || parts[0] != ".gc" || parts[1] != "worktrees" {
		return config.Rig{}, false
	}
	for _, rig := range cfg.Rigs {
		if rig.Name == parts[2] {
			return rig, true
		}
	}
	return config.Rig{}, false
}

func pathWithinScope(path, scopeRoot string) bool {
	if scopeRoot == "" {
		return false
	}
	if path == scopeRoot {
		return true
	}
	return len(path) > len(scopeRoot) && strings.HasPrefix(path, scopeRoot) && path[len(scopeRoot)] == '/'
}
