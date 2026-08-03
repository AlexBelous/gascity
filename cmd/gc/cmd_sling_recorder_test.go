package main

import (
	"bytes"
	"io"
	"testing"

	"github.com/gastownhall/gascity/internal/events"
)

func TestCmdSlingClosesItsSingleRecorder(t *testing.T) {
	cityDir := setupCmdSlingBeadExistsFixture(t)
	previous := slingCityRecorder
	t.Cleanup(func() { slingCityRecorder = previous })

	recorder := &countingSlingRecorder{}
	opens := 0
	slingCityRecorder = func(path string, _ io.Writer) events.Recorder {
		if path != cityDir {
			t.Fatalf("recorder city path = %q, want %q", path, cityDir)
		}
		opens++
		return recorder
	}

	var stdout, stderr bytes.Buffer
	code := cmdSling([]string{"frontend/worker", "ship feature"}, false, false, true, "", nil, "", true, false, false, "", false, false, false, "", "", &stdout, &stderr)
	if code != 0 {
		t.Fatalf("cmdSling returned %d, want 0; stderr: %s", code, stderr.String())
	}
	if opens != 1 {
		t.Fatalf("recorder opens = %d, want 1", opens)
	}
	if recorder.closes != 1 {
		t.Fatalf("recorder closes = %d, want 1", recorder.closes)
	}
}

func TestCmdSlingDryRunDoesNotOpenRecorder(t *testing.T) {
	setupCmdSlingBeadExistsFixture(t)
	previous := slingCityRecorder
	t.Cleanup(func() { slingCityRecorder = previous })
	slingCityRecorder = func(string, io.Writer) events.Recorder {
		t.Fatal("dry-run opened a real recorder")
		return events.Discard
	}

	var stdout, stderr bytes.Buffer
	code := cmdSling([]string{"frontend/worker", "ship feature"}, false, false, true, "", nil, "", true, false, false, "", false, false, true, "", "", &stdout, &stderr)
	if code != 0 {
		t.Fatalf("cmdSling dry-run returned %d, want 0; stderr: %s", code, stderr.String())
	}
}

type countingSlingRecorder struct {
	closes   int
	closeErr error
}

func (*countingSlingRecorder) Record(events.Event) {}

func (r *countingSlingRecorder) Close() error {
	r.closes++
	return r.closeErr
}
