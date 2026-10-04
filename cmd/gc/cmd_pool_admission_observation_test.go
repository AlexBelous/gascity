package main

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/runtime/observation"
	"github.com/gastownhall/gascity/internal/runtime/proctable"
)

func TestPoolAdmissionPerSessionFlag(t *testing.T) {
	cmd := newPoolAdmissionProbeCmd(&bytes.Buffer{}, &bytes.Buffer{})
	flag := cmd.Flags().Lookup("per-session")
	if flag == nil || flag.DefValue != "false" {
		t.Fatal("per-session is missing or changed the default aggregate command")
	}
}

func TestPoolAdmissionObservationWireDeniesUnknown(t *testing.T) {
	var buf bytes.Buffer
	now := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	obs := observation.Observe("/city", runtime.NewFake(), func() ([]proctable.ObservedRoot, error) { return nil, errExit }, func() time.Time { return now })
	if err := writePoolSessionObservation(&buf, obs); err != nil {
		t.Fatal(err)
	}
	var got observation.Observation
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Schema != observation.Schema || got.ProcessComplete || len(got.UnknownReasons) == 0 || got.ProcessObservedAt.IsZero() {
		t.Fatalf("bad unknown wire: %s", buf.String())
	}
}

func TestControllerSourceRequiresPerSession(t *testing.T) {
	cmd := newPoolAdmissionProbeCmd(&bytes.Buffer{}, &bytes.Buffer{})
	cmd.SetArgs([]string{"--via-controller"})
	if err := cmd.Execute(); err == nil || err.Error() != "controller source requires --per-session" {
		t.Fatalf("aggregate accepted persistent controller mode: %v", err)
	}
}
