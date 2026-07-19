package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/spf13/cobra"
)

// hqWorktreeSetupScriptName is the basename of the worktree-setup.sh script
// that provisions HQ worktrees. It is resolved at runtime from the city's
// materialized scripts or a configured pack's assets/scripts dir (see
// resolveHQWorktreeSetupScript) — the same script and layout rig worktrees
// use — rather than a single hardcoded path, so the command works in both
// local-pack and imported-pack cities.
const hqWorktreeSetupScriptName = "worktree-setup.sh"

func newWorktreeCmd(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "worktree",
		Short: "Manage per-bead git worktrees",
		Args:  cobra.ArbitraryArgs,
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 {
				fmt.Fprintln(stderr, "gc worktree: missing subcommand (hq)") //nolint:errcheck
			} else {
				fmt.Fprintf(stderr, "gc worktree: unknown subcommand %q\n", args[0]) //nolint:errcheck
			}
			return errExit
		},
	}
	cmd.AddCommand(newWorktreeHQCmd(stdout, stderr))
	return cmd
}

func newWorktreeHQCmd(stdout, stderr io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "hq <bead-id>",
		Short: "Provision (or reuse) a dedicated worktree for HQ-targeting bead work",
		Long: `Provision an isolated git worktree of the city repo itself, for bead work
that targets HQ (the city) rather than a rig.

Idempotent: reuses an existing worktree for the calling role and bead ID if
one already exists; otherwise creates one via the city's worktree-setup.sh.
On reuse it invokes the script's --sync mode to freshen the worktree against
its upstream; it never discards local work. The worktree's .beads/redirect is
unconditionally rewritten to point at the city's own beads store.

The setup script is resolved from the city's scripts (.gc/scripts or scripts/)
or a configured pack's assets/scripts directory — the same script and layout
rig worktrees use.

The calling role is resolved from $GC_TEMPLATE (preferred) or $GC_AGENT.`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			cityPath, err := resolveCity()
			if err != nil {
				fmt.Fprintf(stderr, "gc worktree hq: %v\n", err) //nolint:errcheck
				return errExit
			}
			// Pack asset dirs are one source for locating worktree-setup.sh; a
			// city that materializes it under .gc/scripts resolves without
			// config, so a config-load failure here is not fatal.
			var packDirs []string
			if cfg, cfgErr := loadCityConfig(cityPath, io.Discard); cfgErr == nil && cfg != nil {
				packDirs = cfg.PackDirs
			}
			path, err := doWorktreeHQ(cityPath, packDirs, args[0], stdout, stderr)
			if err != nil {
				fmt.Fprintf(stderr, "gc worktree hq: %v\n", err) //nolint:errcheck
				return errExit
			}
			fmt.Fprintln(stdout, path) //nolint:errcheck
			return nil
		},
	}
}

// resolveCallingRole resolves the bare (non-rig-qualified) role name of the
// calling agent from $GC_TEMPLATE (preferred) or $GC_AGENT, e.g.
// "gascity/builder" -> "builder". Returns "" if neither is set.
func resolveCallingRole() string {
	identity := strings.TrimSpace(os.Getenv("GC_TEMPLATE"))
	if identity == "" {
		identity = strings.TrimSpace(os.Getenv("GC_AGENT"))
	}
	if identity == "" {
		return ""
	}
	_, name := config.ParseQualifiedName(identity)
	return name
}

// resolveHQWorktreeSetupScript returns the path to the worktree-setup.sh the
// HQ command should invoke. It searches, in order: the city's materialized
// scripts (.gc/scripts, then scripts/), then each configured pack dir's
// assets/scripts and scripts subdirs — the same layout rigs resolve
// {{.ConfigDir}}/assets/scripts/worktree-setup.sh from. It returns an error
// naming every searched location when no script exists.
func resolveHQWorktreeSetupScript(cityPath string, packDirs []string) (string, error) {
	candidates := []string{
		filepath.Join(cityPath, ".gc", "scripts", hqWorktreeSetupScriptName),
		filepath.Join(cityPath, "scripts", hqWorktreeSetupScriptName),
	}
	for _, dir := range packDirs {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		candidates = append(candidates,
			filepath.Join(dir, "assets", "scripts", hqWorktreeSetupScriptName),
			filepath.Join(dir, "scripts", hqWorktreeSetupScriptName),
		)
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s not found in city scripts or configured pack asset dirs (searched: %s)",
		hqWorktreeSetupScriptName, strings.Join(candidates, ", "))
}

// doWorktreeHQ provisions (or reuses) an HQ-targeting bead worktree at
// cityPath/.gc/worktrees/_hq/<role>-<beadID> via the resolved worktree-setup.sh
// in --sync mode, and unconditionally rewrites the worktree's .beads/redirect
// to point at cityPath/.beads. packDirs are the city's configured pack
// directories, used to locate the setup script. Returns the worktree's
// absolute path.
func doWorktreeHQ(cityPath string, packDirs []string, beadID string, stdout, stderr io.Writer) (string, error) {
	beadID = strings.TrimSpace(beadID)
	if beadID == "" {
		return "", fmt.Errorf("missing bead ID")
	}
	role := resolveCallingRole()
	if role == "" {
		return "", fmt.Errorf("cannot resolve calling role: neither GC_TEMPLATE nor GC_AGENT is set")
	}

	bucketDir := filepath.Join(cityPath, ".gc", "worktrees", hqBeadWorktreeBucket)
	worktreeDir := filepath.Join(bucketDir, role+"-"+beadID)
	// Scope gate: reject bead IDs whose resolved path escapes the HQ bucket.
	if !isStrictlyUnderDir(bucketDir, worktreeDir) {
		return "", fmt.Errorf("bead ID %q resolves outside the HQ worktree bucket", beadID)
	}

	scriptPath, err := resolveHQWorktreeSetupScript(cityPath, packDirs)
	if err != nil {
		return "", err
	}

	// #nosec G204 -- scriptPath is resolved only from the city's own scripts or
	// a configured pack's assets/scripts dir (never from user argv); the bead
	// ID is path-traversal-gated above and the role is env-derived.
	cmd := exec.Command(scriptPath, cityPath, worktreeDir, role, "--sync")
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("worktree-setup.sh: %w", err)
	}

	if err := writeHQBeadsRedirect(worktreeDir, cityPath); err != nil {
		return "", err
	}
	return worktreeDir, nil
}

// writeHQBeadsRedirect unconditionally (re)writes worktreeDir/.beads/redirect
// to point at cityPath/.beads, so bd commands run from inside the worktree
// resolve to the city's own beads store.
func writeHQBeadsRedirect(worktreeDir, cityPath string) error {
	beadsDir := filepath.Join(worktreeDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", beadsDir, err)
	}
	target := filepath.Join(cityPath, ".beads") + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "redirect"), []byte(target), 0o644); err != nil {
		return fmt.Errorf("writing .beads/redirect: %w", err)
	}
	return nil
}
