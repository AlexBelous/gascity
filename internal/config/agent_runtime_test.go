package config

import "testing"

func TestAgentRuntimeIndependentOfTransport(t *testing.T) {
	a := Agent{Name: "worker", Session: "tmux"}
	r := "ssh:worker@example"
	applyAgentMutation(&a, &AgentPatch{Runtime: &r}, "")
	if a.Runtime != r || a.Session != "tmux" {
		t.Fatalf("runtime/transport conflated: %+v", a)
	}
	ov := AgentOverride{Runtime: &r}
	p := ov.toAgentPatch()
	if p.Runtime == nil || *p.Runtime != r {
		t.Fatal("runtime override lost")
	}
}
