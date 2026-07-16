package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/nudgequeue"
	"github.com/gastownhall/gascity/internal/rollout"
)

func TestInspectLegacyNudgeBacklogIsReadOnlyAndRequiresPendingAndInFlightToDrain(t *testing.T) {
	for _, test := range []struct {
		name         string
		state        string
		wantDrained  bool
		wantPending  int
		wantInFlight int
		wantErr      string
	}{
		{name: "missing state is empty", wantDrained: true},
		{name: "dead only is drained", state: `{"dead":[{"id":"dead-1"}]}`, wantDrained: true},
		{name: "pending blocks", state: `{"pending":[{"id":"pending-1"}]}`, wantPending: 1},
		{name: "in flight blocks", state: `{"in_flight":[{"id":"claim-1"}]}`, wantInFlight: 1},
		{name: "both block", state: `{"pending":[{"id":"pending-1"}],"in_flight":[{"id":"claim-1"}]}`, wantPending: 1, wantInFlight: 1},
		{name: "unreadable blocks", state: `{"pending":`, wantErr: "parse nudge queue"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cityPath := t.TempDir()
			statePath := nudgequeue.StatePath(cityPath)
			if test.state != "" {
				if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
					t.Fatalf("MkdirAll: %v", err)
				}
				if err := os.WriteFile(statePath, []byte(test.state), 0o600); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
			}

			before, beforeErr := os.ReadFile(statePath)
			evidence := inspectLegacyNudgeBacklog(cityPath)
			after, afterErr := os.ReadFile(statePath)

			if evidence.Drained != test.wantDrained || evidence.Pending != test.wantPending || evidence.InFlight != test.wantInFlight {
				t.Fatalf("evidence = %+v, want drained=%t pending=%d in_flight=%d", evidence, test.wantDrained, test.wantPending, test.wantInFlight)
			}
			if test.wantErr == "" {
				if evidence.Err != nil {
					t.Fatalf("evidence error = %v", evidence.Err)
				}
			} else if evidence.Err == nil || !strings.Contains(evidence.Err.Error(), test.wantErr) {
				t.Fatalf("evidence error = %v, want %q", evidence.Err, test.wantErr)
			}
			if string(after) != string(before) || !sameNotExist(beforeErr, afterErr) {
				t.Fatalf("legacy backlog inspection mutated state: before=%q err=%v after=%q err=%v", before, beforeErr, after, afterErr)
			}
			if _, err := os.Stat(nudgequeue.LockPath(cityPath)); !os.IsNotExist(err) {
				t.Fatalf("read-only inventory created legacy lock: %v", err)
			}
		})
	}
}

func TestNudgeEffectStartupCapabilitiesRequireLegacyBacklogDrainedSeparately(t *testing.T) {
	capabilities := completeNudgeEffectStartupCapabilities()
	capabilities.LegacyBacklogDrained = false
	capabilities.LegacyBacklogDiagnostic = "1 pending and 2 in-flight legacy nudges"

	ok, reason := capabilities.complete()
	if ok || !strings.Contains(reason, "legacy nudge backlog") || !strings.Contains(reason, capabilities.LegacyBacklogDiagnostic) {
		t.Fatalf("complete = %t, %q; want precise independent backlog prerequisite", ok, reason)
	}
	if !capabilities.CommandProducersCovered {
		t.Fatal("legacy backlog prerequisite was overloaded onto producer coverage")
	}
}

func TestLegacyNudgeBacklogGateKeepsAutoLegacyAndMakesRequireRefuse(t *testing.T) {
	for _, mode := range []rollout.Mode{rollout.Auto, rollout.Require} {
		t.Run(string(mode), func(t *testing.T) {
			flags := rollout.ForTest(rollout.WithNudgeEffectOwner(mode))
			capabilities := completeNudgeEffectStartupCapabilities()
			capabilities.LegacyBacklogDrained = false
			capabilities.LegacyBacklogDiagnostic = "1 pending and 1 in-flight entries"

			selection, err := selectNudgeEffectOwnership(t.Context(), flags, string(nudgequeue.CommandSecurityProfileStoreWriterIsController), func(context.Context) nudgeEffectStartupCapabilities {
				return capabilities
			})
			if mode == rollout.Auto {
				if err != nil {
					t.Fatalf("auto selection: %v", err)
				}
				if selection.Ownership != nudgeEffectOwnershipLegacy {
					t.Fatalf("auto ownership = %d, want legacy", selection.Ownership)
				}
				if notice := strings.ToLower(selection.Notice); !strings.Contains(notice, "retaining legacy") || !strings.Contains(notice, "legacy nudge backlog") {
					t.Fatalf("auto notice = %q, want precise legacy-backlog degradation", selection.Notice)
				}
				return
			}
			if selection.Ownership != nudgeEffectOwnershipLegacy {
				t.Fatalf("require ownership = %d, want inert legacy zero value", selection.Ownership)
			}
			if !errors.Is(err, errNudgeEffectStartupRefused) || !strings.Contains(strings.ToLower(err.Error()), "legacy nudge backlog") {
				t.Fatalf("require error = %v, want startup refusal naming legacy backlog", err)
			}
		})
	}
}

func sameNotExist(before, after error) bool {
	return (before == nil && after == nil) || (os.IsNotExist(before) && os.IsNotExist(after))
}
