//go:build integration

package dashport_test

import (
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/api/genclient"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/runproj"
	"github.com/gastownhall/gascity/test/dashport/emitseed"
)

// TestEmissionRawEventLog is Layer A part (a): it asserts against the RAW
// events.jsonl the emission pipeline wrote — no serving, no projection — so the
// pipeline's own output is proven before any consumer reads it. This is the
// honest core of the check: if these events are real, everything downstream is
// emission-driven.
func TestEmissionRawEventLog(t *testing.T) {
	h := newEmissionHarness(t)

	if !h.res.FailOnceFired {
		t.Fatal("the one-shot refresh fault never fired; the #4397 recovery path went untested")
	}

	all := readEmittedEvents(t, filepath.Join(h.cityPath, ".gc", "events.jsonl"))
	if len(all) == 0 {
		t.Fatal("emission produced no events")
	}

	// bead.closed edges for BOTH steps AND the root must be present.
	closed := map[string]bool{}
	for _, e := range all {
		if e.Type == "bead.closed" {
			closed[e.Subject] = true
		}
	}
	for _, id := range []string{emitseed.StepAID, emitseed.StepBID, emitseed.RunEmitID} {
		if !closed[id] {
			t.Errorf("raw event log missing bead.closed edge for %q", id)
		}
	}

	// The recovered event is the single bead.updated for the fail-once step.
	var recovered *events.Event
	stepAUpdates := 0
	for i := range all {
		if all[i].Subject == emitseed.StepAID && all[i].Type == "bead.updated" {
			stepAUpdates++
			recovered = &all[i]
		}
	}
	if stepAUpdates != 1 || recovered == nil {
		t.Fatalf("fail-once step bead.updated count = %d, want exactly 1 (the recovered event)", stepAUpdates)
	}

	// THE #4397 occurredAt proof: the recovered event carries a BACKDATED Ts, so
	// some strictly-earlier-seq event has a strictly-NEWER Ts. Assert the
	// non-monotonic Ts/seq pair explicitly.
	var older, newer *events.Event
	for i := range all {
		if all[i].Seq < recovered.Seq && all[i].Ts.After(recovered.Ts) {
			newer, older = &all[i], recovered
			break
		}
	}
	if older == nil {
		t.Errorf("no non-monotonic Ts/seq pair: recovered event (seq=%d ts=%s) was not backdated",
			recovered.Seq, recovered.Ts)
	} else {
		t.Logf("backdated proof: seq=%d ts=%s precedes recovered seq=%d ts=%s (older Ts at higher seq)",
			newer.Seq, newer.Ts.Format("15:04:05.000000000"),
			older.Seq, older.Ts.Format("15:04:05.000000000"))
	}

	// Every event's envelope correlation fields must be populated as production
	// stamps them: run id resolved from the metadata chain, cache-reconcile actor.
	for _, e := range all {
		if e.Actor != "cache-reconcile" {
			t.Errorf("event seq=%d actor = %q, want cache-reconcile", e.Seq, e.Actor)
		}
		if e.RunID != emitseed.RunEmitID {
			t.Errorf("event seq=%d (subject %q) RunID = %q, want %q", e.Seq, e.Subject, e.RunID, emitseed.RunEmitID)
		}
	}
	// The step events additionally carry a resolved step id and session id.
	for _, e := range all {
		if e.Subject == emitseed.StepAID || e.Subject == emitseed.StepBID {
			if e.StepID == "" {
				t.Errorf("step event seq=%d (subject %q) has empty StepID envelope", e.Seq, e.Subject)
			}
			if e.SessionID != emitseed.AgentName {
				t.Errorf("step event seq=%d SessionID = %q, want %q", e.Seq, e.SessionID, emitseed.AgentName)
			}
		}
	}
}

// TestEmissionRunProjection is Layer A part (b): the SERVED stack projects the
// emission-driven run as a completed run across every run view, and the home
// page's sources (sessions + summary) are populated.
func TestEmissionRunProjection(t *testing.T) {
	h := newEmissionHarness(t)

	t.Run("run census counts the emitted run completed", func(t *testing.T) {
		var census genclient.RunsCensusOutputBody
		h.getJSON(h.cityURL("/runs/census"), &census)
		if census.StatusCounts.Completed < 1 {
			t.Errorf("run census completed = %d, want >= 1 (the emission-driven run-emit)", census.StatusCounts.Completed)
		}
		if census.StatusCounts.Failed != 0 {
			t.Errorf("run census failed = %d, want 0 (run-emit carries no failing outcome)", census.StatusCounts.Failed)
		}
	})

	t.Run("run detail projects run-emit as terminal complete", func(t *testing.T) {
		var detail runproj.FormulaRunDetail
		h.getJSON(h.apiURL("/runs/"+emitseed.RunEmitID+"/detail"), &detail)
		if detail.RunID != emitseed.RunEmitID {
			t.Fatalf("runId = %q, want %q", detail.RunID, emitseed.RunEmitID)
		}
		if len(detail.Nodes) == 0 {
			t.Fatal("run detail has no nodes; emission-driven run projected empty")
		}
		if !detail.Progress.Terminal {
			t.Error("run detail progress.terminal = false, want true (root + both steps closed)")
		}
		if detail.Phase != "complete" {
			t.Errorf("run detail phase = %q, want \"complete\"", detail.Phase)
		}
	})

	t.Run("run summary places run-emit in a historical lane", func(t *testing.T) {
		var summary runproj.RunSummary
		h.getJSON(h.apiURL("/runs/summary"), &summary)
		if summary.TotalHistorical == 0 {
			t.Fatal("run summary TotalHistorical = 0; completed emission run absent from history")
		}
		found := false
		for _, lane := range summary.HistoricalLanes {
			if lane.ID == emitseed.RunEmitID {
				found = true
			}
		}
		if !found {
			t.Errorf("emission run %q not present in summary HistoricalLanes", emitseed.RunEmitID)
		}
	})

	t.Run("home page sources are populated: one active session", func(t *testing.T) {
		var sessions genclient.ListBodySessionResponse
		h.getJSON(h.cityURL("/sessions"), &sessions)
		if sessions.Items == nil || len(*sessions.Items) == 0 {
			t.Fatal("sessions list empty; the home dial active-session count would be zero")
		}
		found := false
		for i := range *sessions.Items {
			s := &(*sessions.Items)[i]
			if s.Alias != nil && *s.Alias == emitseed.AgentName {
				found = true
			}
		}
		if !found {
			t.Errorf("sessions list missing the seeded active session with alias %q", emitseed.AgentName)
		}
	})
}
