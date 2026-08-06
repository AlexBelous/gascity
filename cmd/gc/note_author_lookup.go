package main

import (
	"encoding/json"
	"fmt"

	"github.com/gastownhall/gascity/internal/beads"
)

// bdHistoryEvent is the subset of `bd history <id> --events --json`'s event
// shape this lookup needs. old_value/new_value are JSON-encoded strings: on
// an "updated" event, old_value is typically the full prior issue object and
// new_value a partial patch containing only the changed fields.
type bdHistoryEvent struct {
	EventType string `json:"event_type"`
	Actor     string `json:"actor"`
	OldValue  string `json:"old_value"`
	NewValue  string `json:"new_value"`
}

// bdHistoryEventNotes decodes the notes field out of a bdHistoryEvent's
// JSON-encoded old_value/new_value string, if present. present is false when
// raw is empty, malformed, or has no notes key at all -- callers must
// distinguish "no notes key" from "notes key present but empty string".
func bdHistoryEventNotes(raw string) (notes string, present bool) {
	if raw == "" {
		return "", false
	}
	var decoded struct {
		Notes *string `json:"notes"`
	}
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil || decoded.Notes == nil {
		return "", false
	}
	return *decoded.Notes, true
}

// parseBDHistoryEvents decodes the JSON array `bd history --events --json`
// prints.
func parseBDHistoryEvents(out []byte) ([]bdHistoryEvent, error) {
	var events []bdHistoryEvent
	if err := json.Unmarshal(out, &events); err != nil {
		return nil, fmt.Errorf("parse bd history --events --json: %w", err)
	}
	return events, nil
}

// newestNoteAuthorFromHistoryEvents returns the actor who authored the
// newest "updated" event whose notes differ from the prior value, given
// events in bd's native newest-first order. Returns "" when no such event is
// found (e.g. the bead has never had its notes changed).
func newestNoteAuthorFromHistoryEvents(events []bdHistoryEvent) string {
	for _, ev := range events {
		if ev.EventType != "updated" {
			continue
		}
		newNotes, newPresent := bdHistoryEventNotes(ev.NewValue)
		if !newPresent {
			continue
		}
		oldNotes, _ := bdHistoryEventNotes(ev.OldValue)
		if newNotes != oldNotes {
			return ev.Actor
		}
	}
	return ""
}

// noteAuthorLookupWithRunner builds a noteAuthorLookup backed by `bd history
// <id> --events --json`, run through an injected beads.CommandRunner so the
// subprocess boundary is testable without a real bd/city.
func noteAuthorLookupWithRunner(runner beads.CommandRunner, cityPath string) noteAuthorLookup {
	return func(beadID string) (string, error) {
		out, err := runner(cityPath, "bd", "history", beadID, "--events", "--json")
		if err != nil {
			return "", fmt.Errorf("bd history %s --events --json: %w", beadID, err)
		}
		events, err := parseBDHistoryEvents(out)
		if err != nil {
			return "", err
		}
		return newestNoteAuthorFromHistoryEvents(events), nil
	}
}

// newBDHistoryNoteAuthorLookup is the production noteAuthorLookup, wired to
// the real city-scoped bd command runner.
func newBDHistoryNoteAuthorLookup(cityPath string) noteAuthorLookup {
	return noteAuthorLookupWithRunner(bdCommandRunnerForCity(cityPath), cityPath)
}
