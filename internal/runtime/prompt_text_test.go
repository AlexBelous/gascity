package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestPromptTextLiteralIsPrivateAndFingerprintNeutral(t *testing.T) {
	literal := "literal-secret-prompt\n$(not-executed) ' ` ; \""
	prompt := PromptTextFromLiteral(literal)
	if !prompt.IsSet() || !PromptTextFromLiteral("").IsSet() || (PromptText{}).IsSet() {
		t.Fatal("literal presence lost")
	}
	if prompt.Literal() != literal {
		t.Fatal("literal changed")
	}
	cfg := Config{Command: "fixture", PromptText: prompt}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	slog.New(slog.NewTextHandler(&logs, nil)).Info("fixture", "cfg", cfg, "prompt", prompt)
	for _, printed := range []string{string(encoded), fmt.Sprintf("%v %+v %#v", cfg, cfg, cfg), logs.String()} {
		if strings.Contains(printed, "literal-secret-prompt") {
			t.Fatal("literal leaked through serialization/logging")
		}
	}
	other := cfg
	other.PromptText = PromptTextFromLiteral("different")
	for _, f := range []func(Config) string{ConfigFingerprint, CoreFingerprint, ProvisionFingerprint, LaunchFingerprint, LiveFingerprint} {
		if f(cfg) != f(other) {
			t.Fatal("literal affected desired fingerprint")
		}
	}
}
