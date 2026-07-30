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
	Sessions []struct {
		ID          string `json:"id"`
		Template    string `json:"template"`
		SessionName string `json:"session_name"`
		Closed      bool   `json:"closed"`
	} `json:"sessions"`
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
		Cause         string `json:"cause"`
		WaitOutcome   string `json:"wait_outcome"`
		StartOutcome  string `json:"start_outcome"`
		StartReason   string `json:"start_reason"`
		WaitID        string `json:"wait_id"`
		SessionID     string `json:"session_id"`
		Admission     string `json:"admission"`
		StatusOutcome string `json:"status_outcome"`
		StatusReason  string `json:"status_reason"`
		EffectApplied *bool  `json:"effect_applied"`
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
`, `patrol_interval = "1m"
tick_debounce = "30s"
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
	if durableWait.Wait.ID != waitID || durableWait.Wait.State != "pending" || durableWait.Wait.Status != "open" {
		t.Fatalf("durable wait after shadow witness = %+v, want id=%q state=pending status=open", durableWait.Wait, waitID)
	}

	afterIdentity := sessionWaitDependencyShadowJourneyTmuxIdentity(t, cityDir, session.SessionName)
	if afterIdentity != beforeIdentity {
		t.Fatalf("tmux identity changed across dependency wake: before=%q after=%q", beforeIdentity, afterIdentity)
	}
}

func sessionWaitDependencyShadowJourneySession(t *testing.T, cityDir string) struct {
	ID          string `json:"id"`
	Template    string `json:"template"`
	SessionName string `json:"session_name"`
	Closed      bool   `json:"closed"`
} {
	t.Helper()
	out, err := gc(cityDir, "session", "list", "--state", "all", "--template", "worker", "--json")
	if err != nil {
		t.Fatalf("list live worker session: %v\n%s", err, out)
	}
	var result sessionWaitDependencyShadowJourneySessionList
	if err := json.Unmarshal([]byte(strings.TrimSpace(extractJSONPayload(out))), &result); err != nil {
		t.Fatalf("decode worker session list: %v\n%s", err, out)
	}
	for _, session := range result.Sessions {
		if session.Template == "worker" && !session.Closed && session.ID != "" && session.SessionName != "" {
			return session
		}
	}
	t.Fatalf("live worker session absent from typed session list: %+v", result)
	return struct {
		ID          string `json:"id"`
		Template    string `json:"template"`
		SessionName string `json:"session_name"`
		Closed      bool   `json:"closed"`
	}{}
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
				return trace, time.Since(started), nil
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
				"waiting for dependency-commit shadow record for wait %s and session %s: %w; last error: %v; exact records: %+v",
				waitID,
				sessionID,
				deadline.Err(),
				lastErr,
				sessionWaitDependencyShadowJourneyExactRecords(lastTrace, waitID, sessionID),
			)
		case <-ticker.C:
		}
	}
}

func sessionWaitDependencyShadowJourneyDiagnostics(cityDir, waitID, dependencyID string) string {
	var sections []string

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
