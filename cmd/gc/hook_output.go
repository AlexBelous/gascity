package main

import (
	"encoding/json"
	"io"
	"os"
	"strings"
)

const (
	hookOutputFormatAntigravity = "antigravity"
	hookOutputFormatCodex       = "codex"
	hookOutputFormatGemini      = "gemini"
)

func writeProviderHookContextForEvent(stdout io.Writer, format, eventName, content string) error {
	if content == "" {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(format)) {
	case hookOutputFormatAntigravity:
		return json.NewEncoder(stdout).Encode(antigravityHookAdditionalContext(content))
	case hookOutputFormatCodex:
		return json.NewEncoder(stdout).Encode(codexHookOutput(eventName, content))
	case hookOutputFormatGemini:
		return json.NewEncoder(stdout).Encode(geminiHookAdditionalContext(content))
	}
	_, err := io.WriteString(stdout, content)
	return err
}

func antigravityHookAdditionalContext(content string) map[string]any {
	return map[string]any{
		"injectSteps": []map[string]any{
			{"ephemeralMessage": strings.TrimRight(content, "\n")},
		},
	}
}

func codexHookOutput(eventName, content string) map[string]any {
	trimmedEvent := strings.TrimSpace(eventName)
	if strings.EqualFold(trimmedEvent, "Stop") {
		return map[string]any{
			"decision": "block",
			"reason":   strings.TrimRight(content, "\n"),
		}
	}
	if strings.EqualFold(trimmedEvent, "PreCompact") {
		return codexPreCompactOutput(content)
	}
	return codexHookAdditionalContext(eventName, content)
}

// codexPreCompactOutput builds a Codex PreCompact hook payload using only the
// universal output fields. Codex 0.146's PreCompactCommandOutputWire denies
// unknown fields and, unlike SessionStart/UserPromptSubmit, defines no
// hookSpecificOutput/additionalContext for PreCompact — emitting those traps
// the agent at compaction. The informational handoff reference travels in the
// allowed systemMessage field; the durable self-handoff itself is the
// auto-handoff mail bead that doHandoffAuto persists and SessionStart re-injects.
func codexPreCompactOutput(content string) map[string]any {
	msg := strings.TrimRight(content, "\n")
	if msg == "" {
		return map[string]any{}
	}
	return map[string]any{
		"systemMessage": msg,
	}
}

func codexHookAdditionalContext(eventName, content string) map[string]any {
	if strings.TrimSpace(eventName) == "" {
		eventName = strings.TrimSpace(os.Getenv("GC_HOOK_EVENT_NAME"))
	}
	if strings.TrimSpace(eventName) == "" {
		eventName = "SessionStart"
	}
	return map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     eventName,
			"additionalContext": strings.TrimRight(content, "\n"),
		},
	}
}

func geminiHookAdditionalContext(content string) map[string]any {
	return map[string]any{
		"hookSpecificOutput": map[string]any{
			"additionalContext": strings.TrimRight(content, "\n"),
		},
	}
}
