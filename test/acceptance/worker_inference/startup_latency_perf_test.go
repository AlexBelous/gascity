//go:build acceptance_c

package workerinference_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/config"
	workerpkg "github.com/gastownhall/gascity/internal/worker"
)

func TestAgentStartPerfParsesOnlyCorrelatedCommitTrace(t *testing.T) {
	payload := []byte(`{
		"schema_version":"1",
		"records":[
			{"record_type":"operation","site_code":"lifecycle.start.commit","session_bead_id":"other","outcome_code":"success","fields":{"session_id":"other","duration_ns":99,"effect_applied":true}},
			{"record_type":"operation","site_code":"lifecycle.start.commit","session_bead_id":"session-1","outcome_code":"success","fields":{"session_id":"session-1","duration_ns":12000000000,"start_call_ns":9000000000,"zombie_recycle_ns":100000000,"state_sync_recovery_ns":200000000,"post_start_observe_ns":2000000000,"commit_refresh_ns":300000000,"effect_applied":true}}
		]
	}`)

	got, err := parseAgentStartCommitTrace(payload, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	want := agentStartLatencyControllerTiming{
		SessionID:         "session-1",
		Total:             12 * time.Second,
		StartCall:         9 * time.Second,
		ZombieRecycle:     100 * time.Millisecond,
		StateSyncRecovery: 200 * time.Millisecond,
		PostStartObserve:  2 * time.Second,
		CommitRefresh:     300 * time.Millisecond,
	}
	if got != want {
		t.Fatalf("controller timing = %+v, want %+v", got, want)
	}
}

func TestAgentStartPerfObservesPromptFirstOutputAndIdleCompletion(t *testing.T) {
	promptAt := time.Date(2026, 8, 2, 12, 0, 1, 0, time.UTC)
	firstOutputAt := promptAt.Add(2 * time.Second)
	snapshot := &workerpkg.HistorySnapshot{
		Entries: []workerpkg.HistoryEntry{
			{Actor: workerpkg.ActorUser, Timestamp: &promptAt, Text: "Create the exact startup proof file."},
			{Actor: workerpkg.ActorAssistant, Timestamp: &firstOutputAt, Blocks: []workerpkg.HistoryBlock{{Kind: workerpkg.BlockKindToolUse, Name: "write_file"}}},
		},
		TailState: workerpkg.TailState{Activity: workerpkg.TailActivityIdle},
	}

	got := observeAgentStartTranscript(snapshot, "exact startup proof", firstOutputAt.Add(time.Second))
	if got.PromptAt == nil || !got.PromptAt.Equal(promptAt) {
		t.Fatalf("prompt timestamp = %v, want %v", got.PromptAt, promptAt)
	}
	if got.FirstAssistantOutputAt == nil || !got.FirstAssistantOutputAt.Equal(firstOutputAt) {
		t.Fatalf("first output timestamp = %v, want %v", got.FirstAssistantOutputAt, firstOutputAt)
	}
	if !got.AssistantAfterPrompt || !got.TranscriptIdle || !got.NoOpenToolUse || !got.NoPendingInteraction {
		t.Fatalf("terminal observation = %+v, want complete idle transcript proof", got)
	}
}

func TestAgentStartPerfRecognizesProviderDescendant(t *testing.T) {
	processes := []agentStartObservedProcess{
		{PID: 100, PPID: 1, Command: "zsh"},
		{PID: 200, PPID: 100, Command: "env"},
		{PID: 300, PPID: 200, Command: "node", Args: "node /opt/provider/claude"},
	}
	if !agentStartProcessTreeContains(100, []string{"claude", "node"}, processes) {
		t.Fatal("provider descendant was not recognized")
	}
	if agentStartProcessTreeContains(100, []string{"codex"}, processes) {
		t.Fatal("unrelated provider was recognized")
	}
}

func TestAgentStartPerfSetsRequiredReconcilerInExistingDaemonSection(t *testing.T) {
	input := "[workspace]\nname = \"perf\"\n\n[daemon]\nformula_v2 = true\nsession_reconciler = \"off\"\n\n[orders]\n"
	want := "[workspace]\nname = \"perf\"\n\n[daemon]\nformula_v2 = true\nsession_reconciler = \"require\"\n\n[orders]\n"
	got, err := withRequiredSessionReconciler(input)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("updated city config:\n%s\nwant:\n%s", got, want)
	}
	if gotAgain, err := withRequiredSessionReconciler(got); err != nil || gotAgain != want {
		t.Fatalf("idempotent update = %q, %v", gotAgain, err)
	}
}

type agentStartTraceShow struct {
	Records []agentStartTraceRecord `json:"records"`
}

type agentStartTraceRecord struct {
	RecordType    string                     `json:"record_type"`
	SiteCode      string                     `json:"site_code"`
	SessionBeadID string                     `json:"session_bead_id"`
	OutcomeCode   string                     `json:"outcome_code"`
	Fields        map[string]json.RawMessage `json:"fields"`
}

func parseAgentStartCommitTrace(output []byte, sessionID string) (agentStartLatencyControllerTiming, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return agentStartLatencyControllerTiming{}, fmt.Errorf("session id is empty")
	}
	payload := output
	if payloads := jsonPayloads(string(output)); len(payloads) > 0 {
		payload = payloads[len(payloads)-1]
	}
	var shown agentStartTraceShow
	if err := json.Unmarshal(payload, &shown); err != nil {
		return agentStartLatencyControllerTiming{}, fmt.Errorf("decode trace show: %w", err)
	}

	var matches []agentStartLatencyControllerTiming
	for _, record := range shown.Records {
		if record.RecordType != "operation" || record.SiteCode != "lifecycle.start.commit" ||
			strings.TrimSpace(record.SessionBeadID) != sessionID || record.OutcomeCode != "success" {
			continue
		}
		fieldSessionID, err := agentStartTraceString(record.Fields, "session_id")
		if err != nil || fieldSessionID != sessionID {
			continue
		}
		effectApplied, err := agentStartTraceBool(record.Fields, "effect_applied")
		if err != nil || !effectApplied {
			continue
		}
		timing := agentStartLatencyControllerTiming{SessionID: sessionID}
		for key, dst := range map[string]*time.Duration{
			"duration_ns":            &timing.Total,
			"start_call_ns":          &timing.StartCall,
			"zombie_recycle_ns":      &timing.ZombieRecycle,
			"state_sync_recovery_ns": &timing.StateSyncRecovery,
			"post_start_observe_ns":  &timing.PostStartObserve,
			"commit_refresh_ns":      &timing.CommitRefresh,
		} {
			ns, parseErr := agentStartTraceInt64(record.Fields, key)
			if parseErr != nil {
				return agentStartLatencyControllerTiming{}, parseErr
			}
			*dst = time.Duration(ns)
		}
		if timing.Total <= 0 || timing.StartCall < 0 || timing.ZombieRecycle < 0 || timing.StateSyncRecovery < 0 || timing.PostStartObserve < 0 || timing.CommitRefresh < 0 {
			return agentStartLatencyControllerTiming{}, fmt.Errorf("trace for session %q has invalid durations: %+v", sessionID, timing)
		}
		matches = append(matches, timing)
	}
	if len(matches) != 1 {
		return agentStartLatencyControllerTiming{}, fmt.Errorf("correlated lifecycle.start.commit traces for session %q = %d, want 1", sessionID, len(matches))
	}
	return matches[0], nil
}

func agentStartTraceString(fields map[string]json.RawMessage, key string) (string, error) {
	raw, ok := fields[key]
	if !ok {
		return "", fmt.Errorf("trace field %q is missing", key)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("decode trace field %q: %w", key, err)
	}
	return strings.TrimSpace(value), nil
}

func agentStartTraceBool(fields map[string]json.RawMessage, key string) (bool, error) {
	raw, ok := fields[key]
	if !ok {
		return false, fmt.Errorf("trace field %q is missing", key)
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, fmt.Errorf("decode trace field %q: %w", key, err)
	}
	return value, nil
}

func agentStartTraceInt64(fields map[string]json.RawMessage, key string) (int64, error) {
	raw, ok := fields[key]
	if !ok {
		return 0, fmt.Errorf("trace field %q is missing", key)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value json.Number
	if err := dec.Decode(&value); err != nil {
		return 0, fmt.Errorf("decode trace field %q: %w", key, err)
	}
	ns, err := value.Int64()
	if err != nil {
		return 0, fmt.Errorf("decode trace field %q: %w", key, err)
	}
	return ns, nil
}

type agentStartTranscriptObservation struct {
	PromptAt               *time.Time
	FirstAssistantOutputAt *time.Time
	AssistantAfterPrompt   bool
	TranscriptIdle         bool
	NoOpenToolUse          bool
	NoPendingInteraction   bool
}

func observeAgentStartTranscript(snapshot *workerpkg.HistorySnapshot, prompt string, observedAt time.Time) agentStartTranscriptObservation {
	observation := agentStartTranscriptObservation{}
	if snapshot == nil {
		return observation
	}
	promptIndex := findEntryTextIndex(snapshot.Entries, 0, strings.TrimSpace(prompt))
	if promptIndex >= 0 {
		observation.PromptAt = agentStartEntryTimestamp(snapshot.Entries[promptIndex], observedAt)
		for _, entry := range snapshot.Entries[promptIndex+1:] {
			if entry.Actor != workerpkg.ActorAssistant || (strings.TrimSpace(entry.Text) == "" && len(entry.Blocks) == 0) {
				continue
			}
			observation.AssistantAfterPrompt = true
			observation.FirstAssistantOutputAt = agentStartEntryTimestamp(entry, observedAt)
			break
		}
	}
	observation.TranscriptIdle = snapshot.TailState.Activity == workerpkg.TailActivityIdle
	observation.NoOpenToolUse = len(snapshot.TailState.OpenToolUseIDs) == 0
	observation.NoPendingInteraction = len(snapshot.TailState.PendingInteractionIDs) == 0
	return observation
}

func agentStartEntryTimestamp(entry workerpkg.HistoryEntry, fallback time.Time) *time.Time {
	if entry.Timestamp != nil && !entry.Timestamp.IsZero() {
		at := entry.Timestamp.UTC()
		return &at
	}
	at := fallback.UTC()
	return &at
}

type agentStartObservedProcess struct {
	PID     int
	PPID    int
	Command string
	Args    string
}

func agentStartProcessTreeContains(rootPID int, names []string, processes []agentStartObservedProcess) bool {
	wanted := make(map[string]struct{}, len(names))
	for _, name := range names {
		if normalized := agentStartProcessName(name); normalized != "" {
			wanted[normalized] = struct{}{}
		}
	}
	if rootPID <= 0 || len(wanted) == 0 {
		return false
	}
	byPID := make(map[int]agentStartObservedProcess, len(processes))
	children := make(map[int][]int)
	for _, process := range processes {
		byPID[process.PID] = process
		children[process.PPID] = append(children[process.PPID], process.PID)
	}
	queue := []int{rootPID}
	seen := make(map[int]struct{}, len(processes))
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if _, ok := seen[pid]; ok {
			continue
		}
		seen[pid] = struct{}{}
		if process, ok := byPID[pid]; ok && agentStartProcessMatches(process, wanted) {
			return true
		}
		queue = append(queue, children[pid]...)
	}
	return false
}

func agentStartProcessMatches(process agentStartObservedProcess, wanted map[string]struct{}) bool {
	if _, ok := wanted[agentStartProcessName(process.Command)]; ok {
		return true
	}
	for _, arg := range strings.Fields(process.Args) {
		if _, ok := wanted[agentStartProcessName(strings.Trim(arg, "'\""))]; ok {
			return true
		}
	}
	return false
}

func agentStartProcessName(value string) string {
	return strings.ToLower(strings.TrimSpace(filepath.Base(value)))
}

func withRequiredSessionReconciler(input string) (string, error) {
	if _, err := config.Parse([]byte(input)); err != nil {
		return "", fmt.Errorf("parse city config before setting required reconciler: %w", err)
	}
	lines := strings.Split(input, "\n")
	daemonStart, daemonEnd := -1, len(lines)
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "[daemon]" {
			daemonStart = i
			continue
		}
		if daemonStart >= 0 && i > daemonStart && strings.HasPrefix(trimmed, "[") {
			daemonEnd = i
			break
		}
	}
	if daemonStart < 0 {
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		lines = append(lines, "", "[daemon]", `session_reconciler = "require"`, "")
	} else {
		updated := false
		for i := daemonStart + 1; i < daemonEnd; i++ {
			left, _, found := strings.Cut(lines[i], "=")
			if !found || strings.TrimSpace(left) != "session_reconciler" {
				continue
			}
			indent := lines[i][:len(lines[i])-len(strings.TrimLeft(lines[i], " \t"))]
			lines[i] = indent + `session_reconciler = "require"`
			updated = true
			break
		}
		if !updated {
			lines = append(lines[:daemonEnd], append([]string{`session_reconciler = "require"`}, lines[daemonEnd:]...)...)
		}
	}
	output := strings.Join(lines, "\n")
	cfg, err := config.Parse([]byte(output))
	if err != nil {
		return "", fmt.Errorf("parse city config after setting required reconciler: %w", err)
	}
	if cfg.Daemon.SessionReconciler != "require" {
		return "", fmt.Errorf("session reconciler mode = %q, want require", cfg.Daemon.SessionReconciler)
	}
	return output, nil
}
