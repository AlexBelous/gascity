package config

import (
	"strings"
	"testing"
)

// The assigned-in-progress self-adoption tier must not use the compound
// `ephemeral=true AND status=in_progress` predicate: bd v1.1.x returns zero
// rows for it even when in_progress wisps exist, which blinded respawned
// sessions to their own claimed molecule steps (2026-07-19, ga-oevup7).
func TestAssignedInProgressProbeAvoidsBrokenEphemeralCompound(t *testing.T) {
	script := ephemeralAssignedInProgressProbeScript("id", false)
	if strings.Contains(script, "ephemeral=true AND status=in_progress") {
		t.Fatalf("in_progress probe still uses the broken compound predicate: %s", script)
	}
	if !strings.Contains(script, "status=in_progress") {
		t.Fatalf("in_progress probe lost its status filter: %s", script)
	}
	// The open-status pool-demand probe keeps the compound (it works for open
	// and bounds row counts).
	openScript := bdQueryEphemeralStatusShell("open")
	if !strings.Contains(openScript, "ephemeral=true AND status=open") {
		t.Fatalf("open probe should keep the bounded compound: %s", openScript)
	}
}
