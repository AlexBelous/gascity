package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
)

func TestBindUnifiedWorkspacesConvergesRigAndWorktree(t *testing.T) {
	city := t.TempDir()
	cityBeads := filepath.Join(city, ".beads")
	rig := filepath.Join(city, "rigs", "frontend")
	worktree := filepath.Join(city, ".gc", "worktrees", "frontend", "polecat")
	for _, dir := range []string{cityBeads, filepath.Join(rig, ".beads"), filepath.Join(worktree, ".beads")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{rig, worktree} {
		if err := os.WriteFile(filepath.Join(dir, ".beads", "metadata.json"), []byte(`{"database":"dolt","dolt_database":"old","dolt_host":"old.example","custom":"keep"}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.City{Rigs: []config.Rig{{Name: "frontend", Path: rig, Prefix: "fr"}}}
	if err := bindUnifiedWorkspaces(city, cfg); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{rig, worktree} {
		redirect, err := os.ReadFile(filepath.Join(dir, ".beads", "redirect"))
		if err != nil {
			t.Fatal(err)
		}
		if string(redirect) != "# bd-redirect-mode: workspace\n"+cityBeads+"\n" {
			t.Fatalf("redirect=%q want %q", redirect, cityBeads)
		}
		metadata, err := os.ReadFile(filepath.Join(dir, ".beads", "metadata.json"))
		if err != nil {
			t.Fatal(err)
		}
		if string(metadata) != `{"database":"dolt","dolt_database":"old","dolt_host":"old.example","custom":"keep"}` {
			t.Fatalf("metadata changed during redirect cutover: %s", metadata)
		}
		configData, err := os.ReadFile(filepath.Join(dir, ".beads", "config.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(configData), "issue-prefix: fr") || strings.Contains(string(configData), "dolt.host") {
			t.Fatalf("prefix-only config not written: %s", configData)
		}
	}
	if err := bindUnifiedWorkspaces(city, cfg); err != nil {
		t.Fatalf("idempotent bind: %v", err)
	}
}
