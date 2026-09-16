package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
)

func TestWarnFederationBlindOverridesFailsOnlyBlindWorkQuery(t *testing.T) {
	topo := config.QueryTopology{FederatedReady: true}
	for _, tt := range []struct {
		name       string
		agent      *config.Agent
		wantRefuse bool
		wantKey    string
	}{
		{
			name:       "unacknowledged custom work query",
			agent:      &config.Agent{Name: "worker", WorkQuery: "bd ready --json"},
			wantRefuse: true,
			wantKey:    "work_query",
		},
		{
			name:       "declared federated custom work query",
			agent:      &config.Agent{Name: "worker", WorkQuery: "gc ready --json", WorkQueryFederated: true},
			wantRefuse: false,
		},
		{
			name:       "scale check warning does not block hook",
			agent:      &config.Agent{Name: "worker", ScaleCheck: "echo 1"},
			wantRefuse: false,
			wantKey:    "scale_check",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			if got := warnFederationBlindOverrides(&stderr, tt.agent, topo); got != tt.wantRefuse {
				t.Fatalf("warnFederationBlindOverrides refuse = %v, want %v; stderr=%q", got, tt.wantRefuse, stderr.String())
			}
			if tt.wantKey == "" && stderr.Len() != 0 {
				t.Fatalf("stderr = %q, want no warning", stderr.String())
			}
			if tt.wantKey != "" && !strings.Contains(stderr.String(), "custom "+tt.wantKey) {
				t.Fatalf("stderr = %q, want key %q", stderr.String(), tt.wantKey)
			}
		})
	}
}
