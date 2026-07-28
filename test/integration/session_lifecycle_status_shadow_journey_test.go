//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

const sessionLifecycleStatusShadowJourneyWitnessTimeout = 10 * time.Second

type sessionLifecycleStatusShadowJourneyNew struct {
	SessionID   string `json:"session_id"`
	SessionName string `json:"session_name"`
}

type sessionLifecycleStatusShadowJourneyBead struct {
	Metadata map[string]string `json:"metadata"`
}

// TestSessionLifecycleStatusShadowExactBinaryJourney proves that a typed
// session-bead update independently produces the no-effect status witness;
// the one-minute patrol cadence cannot explain the result.
func TestSessionLifecycleStatusShadowExactBinaryJourney(t *testing.T) {
	if usingSubprocess() {
		t.Skip("exact lifecycle-status shadow journey requires tmux")
	}
	cityDir := setupReconcilerCityWithDaemon(t, `session_reconciler = "auto"

[[agent]]
name = "worker"
start_command = "sleep 3600"
`, `patrol_interval = "1m"
tick_debounce = "30s"
	`)
	waitForExpectedTmuxSessions(t, cityDir, []string{"worker"})
	out, err := gc(cityDir, "session", "new", "worker", "--no-attach", "--json")
	if err != nil {
		t.Fatalf("create manual session: %v\n%s", err, out)
	}
	var created sessionLifecycleStatusShadowJourneyNew
	if err := json.Unmarshal([]byte(strings.TrimSpace(extractJSONPayload(out))), &created); err != nil {
		t.Fatalf("decode manual session creation: %v\n%s", err, out)
	}
	if created.SessionID == "" || created.SessionName == "" {
		t.Fatalf("manual session creation = %+v, want ID and tmux name", created)
	}
	waitForExpectedTmuxSessions(t, cityDir, []string{"worker", created.SessionName})
	waitForAgentRunning(t, cityDir, created.SessionName, 30*time.Second)
	beforeIdentity := sessionWaitDependencyShadowJourneyTmuxIdentity(t, cityDir, created.SessionName)
	// The bd shim persists this ownership precondition without a typed hook
	// payload. The checkout-built gc event below is the only tested trigger.
	// Use the integration file-bd shim directly: the generic bd helper's
	// legacy fallback deliberately supports only assignee updates.
	out, err = runCommand(cityDir, replaceEnv(commandEnvForDir(cityDir, false), "GC_BEADS", "file"), integrationBDCommandTimeout,
		bdBinary, "update", created.SessionID, "--set-metadata", "state=awake", "--set-metadata", "wake_request=explicit")
	if err != nil {
		t.Fatalf("stage exact start ownership: %v\n%s", err, out)
	}
	beforeBead := sessionLifecycleStatusShadowJourneyReadBead(t, cityDir, created.SessionID)
	state := beforeBead.Metadata["state"]
	if beforeBead.Metadata["session_origin"] != "manual" || beforeBead.Metadata["pool_managed"] != "" || beforeBead.Metadata["pool_slot"] != "" || beforeBead.Metadata["wake_request"] != "explicit" ||
		state != "awake" || beforeBead.Metadata["pending_create_claim"] != "" || beforeBead.Metadata["pending_create_started_at"] != "" {
		t.Fatalf("manual exact-start bead metadata = %+v, want settled awake manual non-pool explicit-wake session", beforeBead.Metadata)
	}
	beforeTrace, err := sessionWaitDependencyShadowJourneyTrace(cityDir)
	if err != nil {
		t.Fatalf("read operation trace before event: %v", err)
	}
	out, err = gc(cityDir, "event", "emit", "bead.updated", "--subject", created.SessionID,
		"--bead-payload", created.SessionID, "--actor", "bd-hook", "--json")
	if err != nil {
		t.Fatalf("emit typed session update: %v\n%s", err, out)
	}
	var emitted sessionWaitDependencyShadowJourneyEventEmit
	if err := json.Unmarshal([]byte(strings.TrimSpace(extractJSONPayload(out))), &emitted); err != nil {
		t.Fatalf("decode typed session update: %v\n%s", err, out)
	}
	if !emitted.HasPayload || !emitted.Submitted {
		t.Fatalf("typed session update = %+v, want submitted payload", emitted)
	}
	trace, latency, err := sessionLifecycleStatusShadowJourneyWaitForStatusWitness(t.Context(), cityDir, created.SessionID, sessionLifecycleStatusShadowJourneyLastSeq(beforeTrace), sessionLifecycleStatusShadowJourneyWitnessTimeout)
	if err != nil {
		t.Fatalf(
			"lifecycle status shadow witness did not converge: %v\n%s",
			err,
			sessionWaitDependencyShadowJourneyDiagnostics(cityDir, created.SessionID, created.SessionID),
		)
	}
	t.Logf("lifecycle-status shadow witness observed after %s", latency)
	witnesses := sessionLifecycleStatusShadowJourneyStatusWitnesses(trace, created.SessionID, sessionLifecycleStatusShadowJourneyLastSeq(beforeTrace))
	if len(witnesses) != 1 {
		t.Fatalf("lifecycle status shadow witnesses = %d, want exactly 1: %+v", len(witnesses), witnesses)
	}
	witness := witnesses[0]
	if witness.RecordType != "operation" || witness.Fields.Admission != "in_process" || witness.Fields.StatusOutcome != "noop" || witness.Fields.StatusReason != "converged" || witness.Fields.EffectApplied == nil || *witness.Fields.EffectApplied {
		t.Fatalf("lifecycle status shadow witness = %+v, want in-process candidate/noop/converged with no effect", witness)
	}
	afterBead := sessionLifecycleStatusShadowJourneyReadBead(t, cityDir, created.SessionID)
	if !reflect.DeepEqual(afterBead, beforeBead) {
		t.Fatalf("durable session bead changed across pure shadow event: before=%+v after=%+v", beforeBead, afterBead)
	}
	if afterIdentity := sessionWaitDependencyShadowJourneyTmuxIdentity(t, cityDir, created.SessionName); afterIdentity != beforeIdentity {
		t.Fatalf("tmux identity changed across pure shadow event: before=%q after=%q", beforeIdentity, afterIdentity)
	}
}

func sessionLifecycleStatusShadowJourneyReadBead(t *testing.T, cityDir, sessionID string) sessionLifecycleStatusShadowJourneyBead {
	t.Helper()
	out, err := runCommand(cityDir, replaceEnv(commandEnvForDir(cityDir, false), "GC_BEADS", "file"), integrationBDCommandTimeout,
		bdBinary, "show", sessionID, "--json")
	if err != nil {
		t.Fatalf("read durable session bead %s: %v\n%s", sessionID, err, out)
	}
	var bead sessionLifecycleStatusShadowJourneyBead
	if err := json.Unmarshal([]byte(strings.TrimSpace(extractJSONPayload(out))), &bead); err != nil {
		t.Fatalf("decode durable session bead %s: %v\n%s", sessionID, err, out)
	}
	return bead
}

func sessionLifecycleStatusShadowJourneyLastSeq(trace sessionWaitDependencyShadowJourneyTraceShow) uint64 {
	var max uint64
	for _, record := range trace.Records {
		if record.Seq > max {
			max = record.Seq
		}
	}
	return max
}

func sessionLifecycleStatusShadowJourneyWaitForStatusWitness(ctx context.Context, cityDir, sessionID string, afterSeq uint64, timeout time.Duration) (sessionWaitDependencyShadowJourneyTraceShow, time.Duration, error) {
	started := time.Now()
	deadline, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	var lastTrace sessionWaitDependencyShadowJourneyTraceShow
	var lastErr error
	for {
		trace, err := sessionWaitDependencyShadowJourneyTrace(cityDir)
		if err != nil {
			lastErr = err
		} else {
			lastTrace = trace
			lastErr = nil
			matches := sessionLifecycleStatusShadowJourneyStatusWitnesses(trace, sessionID, afterSeq)
			if len(matches) == 1 {
				return trace, time.Since(started), nil
			}
			if len(matches) > 1 {
				return trace, time.Since(started), fmt.Errorf("lifecycle status shadow witnesses = %d, want exactly 1: %+v", len(matches), matches)
			}
		}
		select {
		case <-deadline.Done():
			return lastTrace, time.Since(started), fmt.Errorf(
				"waiting for lifecycle status shadow witness for session %s: %w; last error: %v; status records: %+v",
				sessionID,
				deadline.Err(),
				lastErr,
				sessionLifecycleStatusShadowJourneyStatusWitnesses(lastTrace, sessionID, afterSeq),
			)
		case <-ticker.C:
		}
	}
}

func sessionLifecycleStatusShadowJourneyStatusWitnesses(trace sessionWaitDependencyShadowJourneyTraceShow, sessionID string, afterSeq uint64) []sessionWaitDependencyShadowJourneyTraceRecord {
	var matches []sessionWaitDependencyShadowJourneyTraceRecord
	for _, record := range trace.Records {
		if record.Seq > afterSeq && record.SiteCode == "lifecycle.status.shadow" && record.Fields.SessionID == sessionID {
			matches = append(matches, record)
		}
	}
	return matches
}
