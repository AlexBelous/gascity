//go:build integration

package execenv

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// This diagnostic uses only synthetic fixtures. It prints masks, never values,
// token hashes, inherited host ENV or process arguments.
func TestOwnedTupleBoundaryDiagnostic(t *testing.T) {
	fixture := []string{
		"GC_SESSION_ID=fixture-sid", "GC_TEMPLATE=fixture-route",
		"GC_RUNTIME_EPOCH=1", "GC_INSTANCE_TOKEN=fixture-private-token", "GC_TEST_OWNER_BOUNDARY_CHILD=1",
	}
	for _, tc := range []struct {
		name  string
		extra map[string]string
		want  int
	}{
		{"inherited credential filtered must not leave partial owner", nil, 0},
		{"explicit complete owned tuple remains complete", map[string]string{
			"GC_SESSION_ID": "fixture-sid", "GC_TEMPLATE": "fixture-route",
			"GC_RUNTIME_EPOCH": "1", "GC_INSTANCE_TOKEN": "fixture-private-token",
		}, 15},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			// Exercise the shell boundary used by MergeMap callers directly;
			// a test-binary bootstrap would add an unrelated layer to the probe.
			// Only fixed variable names reach argv.
			cmd := exec.CommandContext(ctx, "/bin/sh", "-c", `m=0; [ -n "$GC_SESSION_ID" ] && m=$((m|1)); [ -n "$GC_TEMPLATE" ] && m=$((m|2)); [ -n "$GC_RUNTIME_EPOCH" ] && m=$((m|4)); [ -n "$GC_INSTANCE_TOKEN" ] && m=$((m|8)); printf '{"nonempty_mask":%d}\n' "$m"`)
			cmd.Env = MergeMap(fixture, tc.extra)
			out, err := cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			var v struct {
				NonemptyMask int `json:"nonempty_mask"`
			}
			if err := json.Unmarshal(out, &v); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(out), "fixture-") {
				t.Fatal("diagnostic leaked fixture value")
			}
			if v.NonemptyMask != tc.want {
				t.Fatalf("owner nonempty mask=%02x, expected=%02x; token value was not observed", v.NonemptyMask, tc.want)
			}
		})
	}
}
