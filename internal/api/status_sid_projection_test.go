package api

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/api/genclient"
)

func TestStatusSIDLegacyLookupNeverChoosesFirstSibling(t *testing.T) {
	s := statusSessionSnapshot{bySessionName: map[string]statusSessionInfo{}, byTemplate: map[string][]statusSessionInfo{}}
	for _, handle := range []string{"asleep-new", "asleep-old", "live-manual"} {
		s.byTemplate["postman-out"] = append(s.byTemplate["postman-out"], statusSessionInfo{agentName: "postman-out", template: "postman-out", sessionName: handle})
	}
	canonical := agentSessionName("city", "postman-out", "")
	if got := statusRuntimeSessionName("city", "", "postman-out", "", false, s); got != canonical {
		t.Fatalf("ambiguous template selected sibling %q", got)
	}
	s.byTemplate["postman-out"] = s.byTemplate["postman-out"][2:]
	if got := statusRuntimeSessionName("city", "", "postman-out", "", false, s); got != canonical {
		t.Fatalf("manual session relabeled configured identity: %q", got)
	}
	s.byTemplate["postman-out"][0].origin = "manual"
	if got := statusRuntimeSessionName("city", "", "postman-out", "postman-out", true, s); got != canonical {
		t.Fatalf("manual session relabeled singleton pool identity: %q", got)
	}
	s.byTemplate["postman-out"][0].configuredNamedIdentity = "postman-out"
	if got := statusRuntimeSessionName("city", "", "postman-out", "", false, s); got != "live-manual" {
		t.Fatalf("explicit configured named identity ignored: %q", got)
	}
}

func TestStatusSIDGeneratedViewPreservesProjection(t *testing.T) {
	now := time.Date(2026, 10, 7, 14, 50, 0, 0, time.UTC)
	rows := []genclient.RuntimeSessionStatus{{Id: "manual", Template: "tenders/gastown.polecat", AgentName: "adhoc", RuntimeName: "actual-handle", Provider: "*tmux.seamBackedProvider", Running: true}, {Id: "never", Template: "worker", Provider: "*tmux.seamBackedProvider"}}
	body := &genclient.StatusBody{RuntimeSessions: &genclient.RuntimeSessions{Schema: "gascity.runtime-sessions/v1", ObservedAt: now, ProviderComplete: true, Sessions: &rows}}
	v := statusViewFromGen(body)
	if v.RuntimeSessions == nil || !v.RuntimeSessions.ObservedAt.Equal(now) || len(v.RuntimeSessions.Sessions) != 2 || v.RuntimeSessions.Sessions[1].RuntimeName != "" {
		t.Fatal("projection changed through generated view")
	}
	data, err := json.Marshal(v.RuntimeSessions)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	if json.Unmarshal(data, &obj) != nil || len(obj) != 4 {
		t.Fatal("projection schema changed")
	}
	if len(obj["sessions"].([]any)[0].(map[string]any)) != 6 {
		t.Fatal("row keys changed")
	}
	if v.Partial || len(v.Agents) != 0 {
		t.Fatal("projection changed legacy status")
	}
}
