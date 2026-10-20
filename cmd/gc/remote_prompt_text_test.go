package main

import (
	"encoding/json"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/session/sessiontest"
)

func TestRemoteLiteralPromptTracksDeliveryDecisions(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		resume     bool
		message    string
	}{{"arg", "arg", false, ""}, {"flag", "flag", false, ""}, {"none", "none", false, ""}, {"arg-message", "arg", false, "$(literal)\n' ` ;"}, {"none-message", "none", false, "$(literal)\n' ` ;"}, {"resume-no-replay", "arg", true, "ignore initial"}} {
		t.Run(tc.name, func(t *testing.T) {
			store := beads.NewMemStore()
			meta := map[string]string{"template": "worker", "session_name": "worker", "state": "creating", "generation": "1", "instance_token": "fixture-token"}
			if tc.resume {
				meta["started_config_hash"] = "previous"
				meta["session_key"] = "fixture-key"
				meta["state"] = "asleep"
			}
			overrides, _ := json.Marshal(map[string]string{"initial_message": tc.message})
			meta["template_overrides"] = string(overrides)
			row, err := store.Create(beads.Bead{Title: "worker", Type: sessionBeadType, Labels: []string{sessionBeadLabel}, Metadata: meta})
			if err != nil {
				t.Fatal(err)
			}
			prompt := "literal-role\n$(do-not-evaluate) ' ` ;"
			item, _, err := buildPreparedStart(startCandidate{info: sessiontest.SeedBead(t, row), tp: TemplateParams{TemplateName: "worker", SessionName: "worker", Command: "fixture-command", Prompt: prompt, ResolvedProvider: &config.ResolvedProvider{Name: "fixture", PromptMode: tc.mode, PromptFlag: "--prompt", ResumeFlag: "--resume", ResumeCommand: "fixture-command --resume {{.SessionKey}}"}}}, &config.City{}, store)
			if err != nil {
				t.Fatal(err)
			}
			want := prompt
			if tc.message != "" && !tc.resume {
				want += "\n\n---\n\nUser message:\n" + tc.message
			}
			if item.cfg.PromptText.Literal() != want {
				t.Fatalf("literal delivery mismatch: got %q want %q", item.cfg.PromptText.Literal(), want)
			}
			if tc.resume && item.cfg.PromptSuffix != "" {
				t.Fatal("resume restored an argv prompt")
			}
		})
	}
}
