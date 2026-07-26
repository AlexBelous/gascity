package events

import (
	"encoding/json"
	"testing"
)

func TestHandoffRestartNoEffectPayloadJSON(t *testing.T) {
	p := HandoffRestartNoEffectPayload{
		SessionID:            "s1",
		SessionName:          "mayor",
		Mode:                 "self",
		BeforeGeneration:     "5",
		BeforeAwakeStartedAt: "2026-07-20T00:00:00Z",
		AfterGeneration:      "5",
		AfterAwakeStartedAt:  "2026-07-20T00:00:00Z",
		RestartMarkerState:   "pending",
		Reason:               "handoff_restart_no_effect",
		ElapsedSeconds:       75,
	}
	raw := HandoffRestartNoEffectPayloadJSON(p)

	var got HandoffRestartNoEffectPayload
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if got != p {
		t.Errorf("round-tripped payload = %+v, want %+v", got, p)
	}

	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("json.Unmarshal into map: %v", err)
	}
	for _, key := range []string{
		"session_id", "session_name", "mode",
		"before_generation", "before_awake_started_at",
		"after_generation", "after_awake_started_at",
		"restart_marker_state", "reason", "elapsed_s",
	} {
		if _, ok := m[key]; !ok {
			t.Errorf("payload JSON missing key %q: %s", key, raw)
		}
	}
}

func TestHandoffRestartNoEffectPayloadIsEventPayload(t *testing.T) {
	var _ Payload = HandoffRestartNoEffectPayload{}
}
