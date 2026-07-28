package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/gastownhall/gascity/internal/beads/contract"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/fsys"
)

// bindUnifiedWorkspaces makes the city workspace the sole bd workspace after
// the durable unified marker exists. Source config remains only as local GC
// routing context (not a storage endpoint); each redirect is one level.
func bindUnifiedWorkspaces(cityPath string, cfg *config.City) error {
	if cfg == nil {
		return nil
	}
	cityBeads := filepath.Join(cityPath, ".beads")
	if info, err := os.Stat(cityBeads); err != nil || !info.IsDir() {
		return fmt.Errorf("unified workspace binding: city .beads is unavailable: %w", err)
	}
	resolveRigPaths(cityPath, cfg.Rigs)
	for _, rig := range cfg.Rigs {
		if strings.TrimSpace(rig.Path) == "" {
			continue
		}
		if err := bindUnifiedBeadsDir(filepath.Join(rig.Path, ".beads"), cityBeads, rig.EffectivePrefix()); err != nil {
			return fmt.Errorf("binding rig %q: %w", rig.Name, err)
		}
		worktrees := filepath.Join(cityPath, ".gc", "worktrees", rig.Name)
		if err := filepath.WalkDir(worktrees, func(path string, d fs.DirEntry, err error) error {
			if err != nil || !d.IsDir() || filepath.Base(path) != ".beads" {
				return err
			}
			return bindUnifiedBeadsDir(path, cityBeads, rig.EffectivePrefix())
		}); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("walking rig %q worktrees: %w", rig.Name, err)
		}
	}
	return nil
}

func bindUnifiedBeadsDir(beadsDir, cityBeads, prefix string) error {
	if _, err := os.Stat(beadsDir); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	// Retain only the scope prefix in the local config. The redirect is the
	// atomic storage cutover; metadata remains an untouched cold/residue witness.
	if _, err := contract.EnsureCanonicalConfig(fsys.OSFS{}, filepath.Join(beadsDir, "config.yaml"), contract.ConfigState{
		IssuePrefix: prefix, EndpointOrigin: contract.EndpointOriginInheritedCity,
	}); err != nil {
		return err
	}
	return fsys.WriteFileAtomic(fsys.OSFS{}, filepath.Join(beadsDir, "redirect"), []byte("# bd-redirect-mode: workspace\n"+cityBeads+"\n"), 0o644)
}
