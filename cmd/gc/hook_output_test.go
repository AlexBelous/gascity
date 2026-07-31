package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestWriteProviderHookContextGemini(t *testing.T) {
	var out bytes.Buffer
	err := writeProviderHookContextForEvent(&out, "gemini", "", "<system-reminder>\nhello\n</system-reminder>\n")
	if err != nil {
		t.Fatalf("writeProviderHookContextForEvent: %v", err)
	}

	var payload struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal output: %v\n%s", err, out.String())
	}
	if got, want := payload.HookSpecificOutput.AdditionalContext, "<system-reminder>\nhello\n</system-reminder>"; got != want {
		t.Fatalf("additionalContext = %q, want %q", got, want)
	}
}

func TestWriteProviderHookContextAntigravity(t *testing.T) {
	var out bytes.Buffer
	err := writeProviderHookContextForEvent(&out, "antigravity", "", "<system-reminder>\nhello\n</system-reminder>\n")
	if err != nil {
		t.Fatalf("writeProviderHookContextForEvent: %v", err)
	}

	var payload struct {
		InjectSteps []struct {
			EphemeralMessage string `json:"ephemeralMessage"`
		} `json:"injectSteps"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal output: %v\n%s", err, out.String())
	}
	if got, want := len(payload.InjectSteps), 1; got != want {
		t.Fatalf("len(injectSteps) = %d, want %d", got, want)
	}
	if got, want := payload.InjectSteps[0].EphemeralMessage, "<system-reminder>\nhello\n</system-reminder>"; got != want {
		t.Fatalf("ephemeralMessage = %q, want %q", got, want)
	}
}

func TestWriteProviderHookContextCodex(t *testing.T) {
	var out bytes.Buffer
	err := writeProviderHookContextForEvent(&out, "codex", "Stop", "<system-reminder>\nhello\n</system-reminder>\n")
	if err != nil {
		t.Fatalf("writeProviderHookContextForEvent: %v", err)
	}

	var payload struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal output: %v\n%s", err, out.String())
	}
	if got, want := payload.Decision, "block"; got != want {
		t.Fatalf("decision = %q, want %q", got, want)
	}
	if got, want := payload.Reason, "<system-reminder>\nhello\n</system-reminder>"; got != want {
		t.Fatalf("reason = %q, want %q", got, want)
	}
}

func TestWriteProviderHookContextCodexAdditionalContext(t *testing.T) {
	var out bytes.Buffer
	err := writeProviderHookContextForEvent(&out, "codex", "UserPromptSubmit", "<system-reminder>\nhello\n</system-reminder>\n")
	if err != nil {
		t.Fatalf("writeProviderHookContextForEvent: %v", err)
	}

	var payload struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal output: %v\n%s", err, out.String())
	}
	if got, want := payload.HookSpecificOutput.HookEventName, "UserPromptSubmit"; got != want {
		t.Fatalf("hookEventName = %q, want %q", got, want)
	}
	if got, want := payload.HookSpecificOutput.AdditionalContext, "<system-reminder>\nhello\n</system-reminder>"; got != want {
		t.Fatalf("additionalContext = %q, want %q", got, want)
	}
}

func TestWriteProviderHookContextCodexDefaultsSessionStartFromEnv(t *testing.T) {
	t.Setenv("GC_HOOK_EVENT_NAME", "SessionStart")

	var out bytes.Buffer
	err := writeProviderHookContextForEvent(&out, "codex", "", "<system-reminder>\nhello\n</system-reminder>\n")
	if err != nil {
		t.Fatalf("writeProviderHookContextForEvent: %v", err)
	}

	var payload struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal output: %v\n%s", err, out.String())
	}
	if got, want := payload.HookSpecificOutput.HookEventName, "SessionStart"; got != want {
		t.Fatalf("hookEventName = %q, want %q", got, want)
	}
	if got, want := payload.HookSpecificOutput.AdditionalContext, "<system-reminder>\nhello\n</system-reminder>"; got != want {
		t.Fatalf("additionalContext = %q, want %q", got, want)
	}
}

// codexPreCompactCommandOutputWire mirrors Codex 0.146's
// PreCompactCommandOutputWire: the only universal output fields it accepts, with
// serde `deny_unknown_fields`. Decoding gc's PreCompact hook output into this
// struct with DisallowUnknownFields reproduces Codex's rejection of any extra
// top-level key (notably hookSpecificOutput/additionalContext).
type codexPreCompactCommandOutputWire struct {
	Continue       *bool   `json:"continue"`
	StopReason     *string `json:"stopReason"`
	SuppressOutput *bool   `json:"suppressOutput"`
	SystemMessage  *string `json:"systemMessage"`
}

func TestWriteProviderHookContextCodexPreCompactUniversalOnly(t *testing.T) {
	var out bytes.Buffer
	err := writeProviderHookContextForEvent(&out, "codex", "PreCompact", "Handoff: sent auto mail gc-abc12 (restart skipped).\n")
	if err != nil {
		t.Fatalf("writeProviderHookContextForEvent: %v", err)
	}

	// Codex denies unknown fields on PreCompact output; DisallowUnknownFields
	// reproduces that so hookSpecificOutput/additionalContext output fails here
	// exactly as Codex 0.146 rejects it at compaction time.
	dec := json.NewDecoder(bytes.NewReader(out.Bytes()))
	dec.DisallowUnknownFields()
	var payload codexPreCompactCommandOutputWire
	if err := dec.Decode(&payload); err != nil {
		t.Fatalf("PreCompact output rejected by Codex PreCompactCommandOutputWire: %v\n%s", err, out.String())
	}

	// The handoff reference must survive through the one allowed string field.
	if payload.SystemMessage == nil {
		t.Fatalf("systemMessage missing; handoff reference not preserved:\n%s", out.String())
	}
	if got, want := *payload.SystemMessage, "Handoff: sent auto mail gc-abc12 (restart skipped)."; got != want {
		t.Fatalf("systemMessage = %q, want %q", got, want)
	}

	// PreCompact must not carry compaction-blocking signals.
	if payload.Continue != nil && !*payload.Continue {
		t.Fatalf("PreCompact output set continue=false, which would block compaction:\n%s", out.String())
	}
	if payload.StopReason != nil {
		t.Fatalf("PreCompact output set stopReason, which pairs with a stop:\n%s", out.String())
	}
}

func TestWriteProviderHookContextPlain(t *testing.T) {
	var out bytes.Buffer
	err := writeProviderHookContextForEvent(&out, "", "", "<system-reminder>\nhello\n</system-reminder>\n")
	if err != nil {
		t.Fatalf("writeProviderHookContextForEvent: %v", err)
	}
	if got, want := out.String(), "<system-reminder>\nhello\n</system-reminder>\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}
