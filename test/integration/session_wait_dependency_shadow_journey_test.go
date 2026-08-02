//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sessionWaitDependencyShadowJourneyWitnessTimeout = 10 * time.Second

type sessionWaitDependencyShadowJourneySessionList struct {
	Sessions []sessionWaitDependencyShadowJourneySessionItem `json:"sessions"`
}

type sessionWaitDependencyShadowJourneySessionItem struct {
	ID          string `json:"id"`
	Template    string `json:"template"`
	SessionName string `json:"session_name"`
	Closed      bool   `json:"closed"`
}

type sessionWaitDependencyShadowJourneyBead struct {
	ID string `json:"id"`
}

type sessionWaitDependencyShadowJourneyWaitInspect struct {
	Wait struct {
		ID     string `json:"id"`
		State  string `json:"state"`
		Status string `json:"status"`
	} `json:"wait"`
}

type sessionWaitDependencyShadowJourneyEventEmit struct {
	HasPayload bool `json:"has_payload"`
	Submitted  bool `json:"submitted"`
}

type sessionWaitDependencyShadowJourneyTraceShow struct {
	Records []sessionWaitDependencyShadowJourneyTraceRecord `json:"records"`
}

type sessionWaitDependencyShadowJourneyTraceRecord struct {
	Seq                  uint64 `json:"seq"`
	RecordID             string `json:"record_id"`
	ControllerInstanceID string `json:"controller_instance_id"`
	RecordType           string `json:"record_type"`
	SiteCode             string `json:"site_code"`
	OutcomeCode          string `json:"outcome_code"`
	Fields               struct {
		Cause                         string `json:"cause"`
		WaitOutcome                   string `json:"wait_outcome"`
		StartOutcome                  string `json:"start_outcome"`
		StartReason                   string `json:"start_reason"`
		WaitID                        string `json:"wait_id"`
		SessionID                     string `json:"session_id"`
		Admission                     string `json:"admission"`
		StatusOutcome                 string `json:"status_outcome"`
		StatusReason                  string `json:"status_reason"`
		EffectApplied                 *bool  `json:"effect_applied"`
		WorkID                        string `json:"work_id"`
		PoolTarget                    string `json:"pool_target"`
		SourceActor                   string `json:"source_actor"`
		SourceStore                   string `json:"source_store"`
		ContributionPresent           bool   `json:"contribution_present"`
		EventTimestampValid           bool   `json:"event_timestamp_valid"`
		EventToShadowDecisionNS       int64  `json:"event_to_shadow_decision_ns"`
		ObservationToShadowDecisionNS int64  `json:"observation_to_shadow_decision_ns"`
		AllocationAction              string `json:"allocation_action"`
		AllocationReason              string `json:"allocation_reason"`
		AllocationStartCount          int    `json:"allocation_start_count"`
		AllocationSupported           bool   `json:"allocation_supported"`
	} `json:"fields"`
}

func TestSessionWaitDependencyShadowExactBinaryJourney(t *testing.T) {
	if usingSubprocess() {
		t.Skip("exact wait-dependency shadow journey requires tmux")
	}

	cityDir := setupReconcilerCityWithDaemon(t, `session_reconciler = "auto"

[[agent]]
name = "worker"
start_command = "sleep 3600"
`, `patrol_interval = "1h"
tick_debounce = "10m"
`, "")
	waitForExpectedTmuxSessions(t, cityDir, []string{"worker"})

	session := sessionWaitDependencyShadowJourneySession(t, cityDir)
	beforeIdentity := sessionWaitDependencyShadowJourneyTmuxIdentity(t, cityDir, session.SessionName)

	out, err := bd(cityDir, "create", "shadow dependency", "--json")
	if err != nil {
		t.Fatalf("create durable dependency: %v\n%s", err, out)
	}
	dependencyID := sessionWaitDependencyShadowJourneyBeadID(t, out)

	out, err = gc(cityDir, "session", "wait", session.ID,
		"--on-beads", dependencyID,
		"--note", "shadow dependency closed")
	if err != nil {
		t.Fatalf("register exact durable wait: %v\n%s", err, out)
	}
	waitID := sessionWaitDependencyShadowJourneyWaitID(t, out)

	// The file-store bd shim persists the wait but does not run the production
	// bd hook. Emit the typed hook event through the checkout-built gc surface.
	out, err = gc(cityDir, "event", "emit", "bead.created",
		"--subject", waitID,
		"--bead-payload", waitID,
		"--actor", "bd-hook",
		"--json")
	if err != nil {
		t.Fatalf("emit durable wait creation: %v\n%s", err, out)
	}
	var waitCreated sessionWaitDependencyShadowJourneyEventEmit
	if err := json.Unmarshal([]byte(strings.TrimSpace(extractJSONPayload(out))), &waitCreated); err != nil {
		t.Fatalf("decode durable wait creation event: %v\n%s", err, out)
	}
	if !waitCreated.HasPayload || !waitCreated.Submitted {
		t.Fatalf("durable wait creation event = %+v, want typed payload submitted", waitCreated)
	}

	pendingWait, err := sessionWaitDependencyShadowJourneyInspectWait(cityDir, waitID)
	if err != nil {
		t.Fatalf("inspect pending wait %s: %v", waitID, err)
	}
	if pendingWait.Wait.ID != waitID || pendingWait.Wait.State != "pending" || pendingWait.Wait.Status != "open" {
		t.Fatalf("wait while dependency is open = %+v, want id=%q state=pending status=open", pendingWait.Wait, waitID)
	}
	openTrace, err := sessionWaitDependencyShadowJourneyTrace(cityDir)
	if err != nil {
		t.Fatalf("read trace while dependency is open: %v", err)
	}
	for _, record := range sessionWaitDependencyShadowJourneyExactRecords(openTrace, waitID, session.ID) {
		if record.Fields.WaitOutcome == "ready" {
			t.Fatalf("ready exact shadow witness while dependency was open: %+v", record)
		}
	}

	out, err = bd(cityDir, "close", dependencyID)
	if err != nil {
		t.Fatalf("close durable dependency: %v\n%s", err, out)
	}
	// The integration file-store bd shim persists the close but does not run
	// the production bd hook. Invoke that hook's checkout-built gc surface so
	// the controller receives the same typed bead snapshot as a real bd close.
	out, err = gc(cityDir, "event", "emit", "bead.closed",
		"--subject", dependencyID,
		"--bead-payload", dependencyID,
		"--actor", "bd-hook",
		"--json")
	if err != nil {
		t.Fatalf("emit durable dependency close: %v\n%s", err, out)
	}
	var emitted sessionWaitDependencyShadowJourneyEventEmit
	if err := json.Unmarshal([]byte(strings.TrimSpace(extractJSONPayload(out))), &emitted); err != nil {
		t.Fatalf("decode durable dependency close event: %v\n%s", err, out)
	}
	if !emitted.HasPayload || !emitted.Submitted {
		t.Fatalf("durable dependency close event = %+v, want typed payload submitted", emitted)
	}

	finalTrace, observedLatency, err := sessionWaitDependencyShadowJourneyWaitForDependencyCommit(
		t.Context(),
		cityDir,
		waitID,
		session.ID,
		sessionWaitDependencyShadowJourneyWitnessTimeout,
	)
	if err != nil {
		t.Fatalf(
			"exact dependency-commit shadow witness did not converge: %v\n%s",
			err,
			sessionWaitDependencyShadowJourneyDiagnostics(cityDir, waitID, dependencyID),
		)
	}
	t.Logf("dependency-commit shadow witness observed after %s", observedLatency)

	exactRecords := sessionWaitDependencyShadowJourneyExactRecords(finalTrace, waitID, session.ID)
	for _, record := range exactRecords {
		if record.RecordType != "operation" || record.Fields.EffectApplied == nil || *record.Fields.EffectApplied {
			t.Fatalf("exact shadow record = %+v, want committed operation with no effect", record)
		}
	}
	dependencyRecords := sessionWaitDependencyShadowJourneyDependencyCommitRecords(finalTrace, waitID, session.ID)
	if len(dependencyRecords) != 1 {
		t.Fatalf("dependency-commit ready/noop/already-running shadow records = %d, want 1: %+v", len(dependencyRecords), dependencyRecords)
	}
	if record := dependencyRecords[0]; record.Seq == 0 || record.RecordID == "" {
		t.Fatalf("dependency-commit shadow record = %+v, want committed record identity", record)
	}
	durableWait, err := sessionWaitDependencyShadowJourneyInspectWait(cityDir, waitID)
	if err != nil {
		t.Fatalf("inspect durable wait after shadow witness: %v", err)
	}
	if durableWait.Wait.ID != waitID || durableWait.Wait.State != "ready" || durableWait.Wait.Status != "open" {
		t.Fatalf("durable wait after shadow witness = %+v, want id=%q state=ready status=open", durableWait.Wait, waitID)
	}

	afterIdentity := sessionWaitDependencyShadowJourneyTmuxIdentity(t, cityDir, session.SessionName)
	if afterIdentity != beforeIdentity {
		t.Fatalf("tmux identity changed across dependency wake: before=%q after=%q", beforeIdentity, afterIdentity)
	}
}

// TestReadyRoutedWorkPriorityStartsLegacySessionBeforeDebounce proves that a
// schema-59-style routed-work update with no dependency field accelerates the
// existing legacy tick. The event admission itself does not create a session;
// the typed session projection remains the user-visible legacy-owned result.
func TestReadyRoutedWorkPriorityStartsLegacySessionBeforeDebounce(t *testing.T) {
	if usingSubprocess() {
		t.Skip("exact ready routed-work journey requires tmux")
	}

	cityDir := setupReconcilerCityWithDaemon(t, `session_reconciler = "auto"

[[agent]]
name = "worker"
start_command = "sleep 3600"
min_active_sessions = 0
max_active_sessions = -1
`, `patrol_interval = "1h"
tick_debounce = "10m"
`, "")
	if out, err := gc("", "stop", cityDir); err != nil {
		t.Fatalf("stop empty city before priming routed work: %v\n%s", err, out)
	}
	if err := sessionWaitDependencyShadowJourneyWaitForControllerStop(t.Context(), cityDir, sessionWaitDependencyShadowJourneyWitnessTimeout); err != nil {
		t.Fatalf("wait for empty city controller to stop: %v", err)
	}

	out, err := bd(cityDir, "create", "ready routed-work priority journey", "--json")
	if err != nil {
		t.Fatalf("create ready routed work while city is stopped: %v\n%s", err, out)
	}
	workID := sessionWaitDependencyShadowJourneyBeadID(t, out)
	if out, err = gc("", "start", cityDir); err != nil {
		t.Fatalf("restart city with unrouted work in its initial cache prime: %v\n%s", err, out)
	}

	initial, err := sessionWaitDependencyShadowJourneyListSessions(cityDir)
	if err != nil {
		t.Fatalf("list initial worker sessions: %v", err)
	}
	for _, session := range initial.Sessions {
		if session.Template == "worker" && !session.Closed {
			t.Fatalf("initial worker session = %+v, want no unclosed worker session", session)
		}
	}

	out, err = runCommand(cityDir, replaceEnv(commandEnvForDir(cityDir, false), "GC_BEADS", "file"), integrationBDCommandTimeout,
		bdBinary, "update", workID, "--set-metadata", "gc.routed_to=worker")
	if err != nil {
		t.Fatalf("route ready work to worker: %v\n%s", err, out)
	}

	started := time.Now()
	out, err = gc(cityDir, "event", "emit", "bead.updated",
		"--subject", workID,
		"--bead-payload", workID,
		"--actor", "bd-hook",
		"--json")
	if err != nil {
		t.Fatalf("emit routed-work update: %v\n%s", err, out)
	}
	var emitted sessionWaitDependencyShadowJourneyEventEmit
	if err := json.Unmarshal([]byte(strings.TrimSpace(extractJSONPayload(out))), &emitted); err != nil {
		t.Fatalf("decode routed-work update event: %v\n%s", err, out)
	}
	if !emitted.HasPayload || !emitted.Submitted {
		t.Fatalf("routed-work update event = %+v, want typed payload submitted", emitted)
	}
	if err := sessionWaitDependencyShadowJourneyRequireOmittedDependenciesEvent(cityDir, workID); err != nil {
		t.Fatalf("routed-work update did not retain schema-59 omitted-dependencies shape: %v", err)
	}

	session, latency, err := sessionWaitDependencyShadowJourneyWaitForWorkerSession(
		t.Context(),
		cityDir,
		started,
		sessionWaitDependencyShadowJourneyWitnessTimeout,
	)
	if err != nil {
		t.Fatalf(
			"ready routed-work session did not materialize before the ten-minute debounce: %v\n%s",
			err,
			sessionWaitDependencyShadowJourneyDiagnostics(cityDir, workID, workID),
		)
	}
	trace, err := sessionWaitDependencyShadowJourneyTrace(cityDir)
	if err != nil {
		t.Fatalf("read routed-work demand shadow trace: %v", err)
	}
	shadowRecords := sessionWaitDependencyShadowJourneyRoutedWorkDemandRecords(trace, workID)
	if len(shadowRecords) != 1 {
		t.Fatalf("routed-work demand shadow records = %d, want 1: %+v\n%s", len(shadowRecords), shadowRecords, sessionWaitDependencyShadowJourneyDiagnostics(cityDir, workID, workID))
	}
	shadow := shadowRecords[0]
	if shadow.Seq == 0 || shadow.RecordID == "" ||
		shadow.RecordType != "operation" ||
		shadow.OutcomeCode != "accepted" ||
		shadow.Fields.PoolTarget != "worker" ||
		shadow.Fields.SourceActor != "bd-hook" ||
		shadow.Fields.SourceStore == "" ||
		!shadow.Fields.ContributionPresent ||
		!shadow.Fields.EventTimestampValid ||
		shadow.Fields.EventToShadowDecisionNS <= 0 ||
		shadow.Fields.ObservationToShadowDecisionNS < 0 ||
		shadow.Fields.AllocationAction != "start_one" ||
		shadow.Fields.AllocationReason != "cold_from_zero" ||
		shadow.Fields.AllocationStartCount != 1 ||
		!shadow.Fields.AllocationSupported ||
		shadow.Fields.EffectApplied == nil || *shadow.Fields.EffectApplied {
		t.Fatalf("routed-work demand shadow record = %+v, want committed exact no-effect cold allocation", shadow)
	}
	t.Logf(
		"ready routed-work event produced start-one shadow allocation in %s and materialized legacy-owned session %s in %s",
		time.Duration(shadow.Fields.EventToShadowDecisionNS),
		session.ID,
		latency,
	)
}

func sessionWaitDependencyShadowJourneyWaitForControllerStop(ctx context.Context, cityDir string, timeout time.Duration) error {
	deadline, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if controllerAlive(cityDir) == 0 {
			return nil
		}
		select {
		case <-deadline.Done():
			return fmt.Errorf("controller still answered after stop: %w", deadline.Err())
		case <-ticker.C:
		}
	}
}

func sessionWaitDependencyShadowJourneyRequireOmittedDependenciesEvent(cityDir, workID string) error {
	data, err := os.ReadFile(filepath.Join(cityDir, ".gc", "events.jsonl"))
	if err != nil {
		return fmt.Errorf("read event log: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		var event struct {
			Type    string          `json:"type"`
			Actor   string          `json:"actor"`
			Subject string          `json:"subject"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal([]byte(lines[i]), &event); err != nil ||
			event.Type != "bead.updated" || event.Actor != "bd-hook" || event.Subject != workID {
			continue
		}
		var envelope struct {
			Bead map[string]json.RawMessage `json:"bead"`
		}
		if err := json.Unmarshal(event.Payload, &envelope); err != nil {
			return fmt.Errorf("decode event payload: %w", err)
		}
		if _, ok := envelope.Bead["dependencies"]; ok {
			return fmt.Errorf("payload unexpectedly contains dependencies")
		}
		if _, ok := envelope.Bead["needs"]; ok {
			return fmt.Errorf("payload unexpectedly contains needs")
		}
		return nil
	}
	return fmt.Errorf("typed bead.updated event for %s not found", workID)
}

func sessionWaitDependencyShadowJourneyListSessions(cityDir string) (sessionWaitDependencyShadowJourneySessionList, error) {
	out, err := gc(cityDir, "session", "list", "--state", "all", "--template", "worker", "--json")
	if err != nil {
		return sessionWaitDependencyShadowJourneySessionList{}, fmt.Errorf("gc session list: %w: %s", err, out)
	}
	var result sessionWaitDependencyShadowJourneySessionList
	if err := json.Unmarshal([]byte(strings.TrimSpace(extractJSONPayload(out))), &result); err != nil {
		return sessionWaitDependencyShadowJourneySessionList{}, fmt.Errorf("decode gc session list: %w: %s", err, out)
	}
	return result, nil
}

func sessionWaitDependencyShadowJourneyWaitForWorkerSession(
	ctx context.Context,
	cityDir string,
	started time.Time,
	timeout time.Duration,
) (sessionWaitDependencyShadowJourneySessionItem, time.Duration, error) {
	deadline, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	var last sessionWaitDependencyShadowJourneySessionList
	var lastErr error
	for {
		current, err := sessionWaitDependencyShadowJourneyListSessions(cityDir)
		if err != nil {
			lastErr = err
		} else {
			last = current
			lastErr = nil
			for _, session := range current.Sessions {
				if session.Template == "worker" && !session.Closed && session.ID != "" {
					return session, time.Since(started), nil
				}
			}
		}

		select {
		case <-deadline.Done():
			return sessionWaitDependencyShadowJourneySessionItem{}, time.Since(started), fmt.Errorf(
				"waiting for an unclosed worker session: %w; last error: %v; last sessions: %+v",
				deadline.Err(),
				lastErr,
				last.Sessions,
			)
		case <-ticker.C:
		}
	}
}

func sessionWaitDependencyShadowJourneySession(t *testing.T, cityDir string) sessionWaitDependencyShadowJourneySessionItem {
	t.Helper()
	result, err := sessionWaitDependencyShadowJourneyListSessions(cityDir)
	if err != nil {
		t.Fatalf("list live worker session: %v", err)
	}
	for _, session := range result.Sessions {
		if session.Template == "worker" && !session.Closed && session.ID != "" && session.SessionName != "" {
			return session
		}
	}
	t.Fatalf("live worker session absent from typed session list: %+v", result)
	return sessionWaitDependencyShadowJourneySessionItem{}
}

func sessionWaitDependencyShadowJourneyTmuxIdentity(t *testing.T, cityDir, sessionName string) string {
	t.Helper()
	out, err := runCommand("", commandEnvForDir(cityDir, false), integrationGCCommandTimeout,
		"tmux", "-L", filepath.Base(cityDir), "display-message", "-p", "-t", "="+sessionName,
		"#{session_id}|#{session_name}|#{socket_path}")
	if err != nil {
		t.Fatalf("read tmux identity for %s: %v\n%s", sessionName, err, out)
	}
	identity := strings.TrimSpace(out)
	if identity == "" {
		t.Fatalf("tmux identity for %s is empty", sessionName)
	}
	return identity
}

func sessionWaitDependencyShadowJourneyBeadID(t *testing.T, output string) string {
	t.Helper()
	const createdPrefix = "Created bead:"
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, createdPrefix) {
			continue
		}
		if id := strings.TrimSpace(strings.TrimPrefix(line, createdPrefix)); id != "" {
			return id
		}
	}
	payload := []byte(strings.TrimSpace(extractJSONPayload(output)))
	var bead sessionWaitDependencyShadowJourneyBead
	if err := json.Unmarshal(payload, &bead); err == nil && bead.ID != "" {
		return bead.ID
	}
	var beads []sessionWaitDependencyShadowJourneyBead
	if err := json.Unmarshal(payload, &beads); err == nil && len(beads) == 1 && beads[0].ID != "" {
		return beads[0].ID
	}
	t.Fatalf("decode created dependency ID from %q", output)
	return ""
}

func sessionWaitDependencyShadowJourneyWaitID(t *testing.T, output string) string {
	t.Helper()
	const prefix = "Registered wait "
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, prefix))
		if len(fields) > 0 && fields[0] != "" {
			return fields[0]
		}
	}
	t.Fatalf("decode registered wait ID from %q", output)
	return ""
}

func sessionWaitDependencyShadowJourneyInspectWait(
	cityDir string,
	waitID string,
) (sessionWaitDependencyShadowJourneyWaitInspect, error) {
	out, err := gc(cityDir, "wait", "inspect", waitID, "--json")
	if err != nil {
		return sessionWaitDependencyShadowJourneyWaitInspect{}, fmt.Errorf("gc wait inspect %s: %w: %s", waitID, err, out)
	}
	var result sessionWaitDependencyShadowJourneyWaitInspect
	if err := json.Unmarshal([]byte(strings.TrimSpace(extractJSONPayload(out))), &result); err != nil {
		return sessionWaitDependencyShadowJourneyWaitInspect{}, fmt.Errorf("decode gc wait inspect %s: %w: %s", waitID, err, out)
	}
	return result, nil
}

func sessionWaitDependencyShadowJourneyWaitForDependencyCommit(
	ctx context.Context,
	cityDir string,
	waitID string,
	sessionID string,
	timeout time.Duration,
) (sessionWaitDependencyShadowJourneyTraceShow, time.Duration, error) {
	started := time.Now()
	deadline, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	var lastTrace sessionWaitDependencyShadowJourneyTraceShow
	var lastWait sessionWaitDependencyShadowJourneyWaitInspect
	var lastErr error
	for {
		trace, traceErr := sessionWaitDependencyShadowJourneyTrace(cityDir)
		if traceErr != nil {
			lastErr = traceErr
		} else {
			lastTrace = trace
			lastErr = nil
			matches := sessionWaitDependencyShadowJourneyDependencyCommitRecords(trace, waitID, sessionID)
			switch len(matches) {
			case 1:
				wait, waitErr := sessionWaitDependencyShadowJourneyInspectWait(cityDir, waitID)
				if waitErr != nil {
					lastErr = waitErr
					break
				}
				lastWait = wait
				if wait.Wait.ID == waitID && wait.Wait.State == "ready" && wait.Wait.Status == "open" {
					return trace, time.Since(started), nil
				}
				lastErr = fmt.Errorf("durable wait = %+v, want id=%q state=ready status=open", wait.Wait, waitID)
			case 0:
			default:
				return trace, time.Since(started), fmt.Errorf(
					"dependency-commit shadow records for wait %s and session %s = %d, want exactly 1: %+v",
					waitID,
					sessionID,
					len(matches),
					matches,
				)
			}
		}

		select {
		case <-deadline.Done():
			return lastTrace, time.Since(started), fmt.Errorf(
				"waiting for dependency-commit shadow record and durable ready wait for wait %s and session %s: %w; last error: %v; last wait: %+v; exact records: %+v",
				waitID,
				sessionID,
				deadline.Err(),
				lastErr,
				lastWait.Wait,
				sessionWaitDependencyShadowJourneyExactRecords(lastTrace, waitID, sessionID),
			)
		case <-ticker.C:
		}
	}
}

func sessionWaitDependencyShadowJourneyDiagnostics(cityDir, waitID, dependencyID string) string {
	var sections []string
	if out, err := gc(cityDir, "session", "list", "--state", "all", "--json"); err != nil {
		sections = append(sections, fmt.Sprintf("session list: %v: %s", err, out))
	} else {
		sections = append(sections, "session list:\n"+tailText(out, 100))
	}

	eventsPath := filepath.Join(cityDir, ".gc", "events.jsonl")
	if data, err := os.ReadFile(eventsPath); err != nil {
		sections = append(sections, fmt.Sprintf("relevant events: read %s: %v", eventsPath, err))
	} else {
		var relevant []string
		for _, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, waitID) || strings.Contains(line, dependencyID) {
				relevant = append(relevant, line)
			}
		}
		sections = append(sections, "relevant event tail:\n"+tailText(strings.Join(relevant, "\n"), 30))
	}

	env := parseEnvList(commandEnvForDir(cityDir, false))
	logPath := filepath.Join(env["GC_HOME"], "supervisor.log")
	if data, err := os.ReadFile(logPath); err != nil {
		sections = append(sections, fmt.Sprintf("supervisor log: read %s: %v", logPath, err))
	} else {
		sections = append(sections, "supervisor log tail:\n"+tailText(string(data), 100))
	}

	if out, err := gc(cityDir, "trace", "status", "--json"); err != nil {
		sections = append(sections, fmt.Sprintf("trace status: %v: %s", err, out))
	} else {
		sections = append(sections, "trace status:\n"+tailText(out, 20))
	}
	if out, err := gc(cityDir, "trace", "show", "--since", "2m"); err != nil {
		sections = append(sections, fmt.Sprintf("recent trace: %v: %s", err, out))
	} else {
		sections = append(sections, "recent trace tail:\n"+tailText(out, 100))
	}

	return strings.Join(sections, "\n\n")
}

func sessionWaitDependencyShadowJourneyTrace(cityDir string) (sessionWaitDependencyShadowJourneyTraceShow, error) {
	out, err := gc(cityDir, "trace", "show", "--json")
	if err != nil {
		return sessionWaitDependencyShadowJourneyTraceShow{}, fmt.Errorf("gc trace show: %w: %s", err, out)
	}
	var result sessionWaitDependencyShadowJourneyTraceShow
	if err := json.Unmarshal([]byte(strings.TrimSpace(extractJSONPayload(out))), &result); err != nil {
		return sessionWaitDependencyShadowJourneyTraceShow{}, fmt.Errorf("decode gc trace show: %w: %s", err, out)
	}
	return result, nil
}

func sessionWaitDependencyShadowJourneyExactRecords(
	trace sessionWaitDependencyShadowJourneyTraceShow,
	waitID string,
	sessionID string,
) []sessionWaitDependencyShadowJourneyTraceRecord {
	var matches []sessionWaitDependencyShadowJourneyTraceRecord
	for _, record := range trace.Records {
		if record.SiteCode == "lifecycle.wait_dependency.shadow" &&
			record.Fields.WaitID == waitID &&
			record.Fields.SessionID == sessionID {
			matches = append(matches, record)
		}
	}
	return matches
}

func sessionWaitDependencyShadowJourneyRoutedWorkDemandRecords(
	trace sessionWaitDependencyShadowJourneyTraceShow,
	workID string,
) []sessionWaitDependencyShadowJourneyTraceRecord {
	var matches []sessionWaitDependencyShadowJourneyTraceRecord
	for _, record := range trace.Records {
		if record.SiteCode == "pool_demand.contribution.shadow" && record.Fields.WorkID == workID {
			matches = append(matches, record)
		}
	}
	return matches
}

func sessionWaitDependencyShadowJourneyDependencyCommitRecords(
	trace sessionWaitDependencyShadowJourneyTraceShow,
	waitID string,
	sessionID string,
) []sessionWaitDependencyShadowJourneyTraceRecord {
	var matches []sessionWaitDependencyShadowJourneyTraceRecord
	for _, record := range sessionWaitDependencyShadowJourneyExactRecords(trace, waitID, sessionID) {
		if record.RecordType == "operation" &&
			record.Fields.Cause == "dependency_commit" &&
			record.Fields.WaitOutcome == "ready" &&
			record.Fields.StartOutcome == "noop" &&
			record.Fields.StartReason == "already_running" &&
			record.Fields.EffectApplied != nil &&
			!*record.Fields.EffectApplied {
			matches = append(matches, record)
		}
	}
	return matches
}
