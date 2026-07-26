package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/fsys"
)

// TestDoltReadTimeoutRiskCheck_RiskLogic covers the ceiling logic and the
// skip/applicability branches with an injected DoltConfig so the risk decision
// is exercised without materializing a managed Dolt city.
func TestDoltReadTimeoutRiskCheck_RiskLogic(t *testing.T) {
	tests := []struct {
		name        string
		readTimeout int // 0 == managed default
		applicable  bool
		skip        bool
		wantStatus  CheckStatus
	}{
		{name: "managed default is safe", readTimeout: 0, applicable: true, wantStatus: StatusOK},
		{name: "at ceiling is safe", readTimeout: doltReadTimeoutRiskCeilingMillis, applicable: true, wantStatus: StatusOK},
		{name: "one over ceiling warns", readTimeout: doltReadTimeoutRiskCeilingMillis + 1, applicable: true, wantStatus: StatusWarning},
		{name: "incident 60s override warns", readTimeout: 60000, applicable: true, wantStatus: StatusWarning},
		{name: "5min override warns", readTimeout: 300000, applicable: true, wantStatus: StatusWarning},
		{name: "not applicable is skipped", readTimeout: 300000, applicable: false, wantStatus: StatusOK},
		{name: "skip flag short-circuits", readTimeout: 300000, applicable: true, skip: true, wantStatus: StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &DoltReadTimeoutRiskCheck{
				skip:            tt.skip,
				applicableKnown: true,
				applicable:      tt.applicable,
				doltConfig:      config.DoltConfig{ReadTimeoutMillis: tt.readTimeout},
			}
			r := c.Run(&CheckContext{})
			if r.Status != tt.wantStatus {
				t.Fatalf("Status = %d, want %d; msg = %s", r.Status, tt.wantStatus, r.Message)
			}
			if r.Status != StatusWarning {
				return
			}
			// A saturation-risk warning must never gate automation (dispatch,
			// gc start, exit codes): it is operator guidance about a supported
			// but risky override, not a broken state.
			if r.Severity != SeverityAdvisory {
				t.Fatalf("Severity = %d, want SeverityAdvisory", r.Severity)
			}
			if r.FixHint == "" {
				t.Fatal("a read_timeout risk warning must carry a FixHint")
			}
		})
	}
}

// TestDoltReadTimeoutRiskCheck_WarningNamesTheValues verifies the warning is
// actionable: it reports the offending effective value and the managed default
// so an operator knows both what is set and what to lower it toward.
func TestDoltReadTimeoutRiskCheck_WarningNamesTheValues(t *testing.T) {
	c := &DoltReadTimeoutRiskCheck{
		applicableKnown: true,
		applicable:      true,
		doltConfig:      config.DoltConfig{ReadTimeoutMillis: 60000},
	}
	r := c.Run(&CheckContext{})
	if r.Status != StatusWarning {
		t.Fatalf("Status = %d, want StatusWarning; msg = %s", r.Status, r.Message)
	}
	for _, want := range []string{"60000", "read_timeout_millis"} {
		if !strings.Contains(r.Message, want) {
			t.Fatalf("Message = %q, want it to contain %q", r.Message, want)
		}
	}
	if !strings.Contains(r.FixHint, "15000") {
		t.Fatalf("FixHint = %q, want it to name the managed default 15000", r.FixHint)
	}
}

// TestDoltReadTimeoutRiskCheck_ForConfig proves the registration constructor
// reads [dolt] read_timeout_millis from a loaded city config and computes
// applicability against a real managed-Dolt city (acceptance: overrides,
// including a stale long read timeout, are tested end to end).
func TestDoltReadTimeoutRiskCheck_ForConfig(t *testing.T) {
	dir := setupManagedDoltCity(t)
	if err := os.WriteFile(filepath.Join(dir, "city.toml"), []byte(`[workspace]
name = "demo"

[beads]
provider = "bd"

[dolt]
read_timeout_millis = 60000
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(fsys.OSFS{}, filepath.Join(dir, "city.toml"))
	if err != nil {
		t.Fatalf("Load city.toml: %v", err)
	}
	c := NewDoltReadTimeoutRiskCheckForConfig(dir, false, cfg, nil)
	r := c.Run(&CheckContext{})
	if r.Status != StatusWarning {
		t.Fatalf("Status = %d, want StatusWarning for a 60s managed read_timeout override; msg = %s", r.Status, r.Message)
	}
	if r.Severity != SeverityAdvisory {
		t.Fatalf("Severity = %d, want SeverityAdvisory", r.Severity)
	}
}

// TestDoltReadTimeoutRiskCheck_ForConfigDefaultOK confirms a city that leaves
// read_timeout at the managed default does not draw a warning.
func TestDoltReadTimeoutRiskCheck_ForConfigDefaultOK(t *testing.T) {
	dir := setupManagedDoltCity(t)
	if err := os.WriteFile(filepath.Join(dir, "city.toml"), []byte(`[workspace]
name = "demo"

[beads]
provider = "bd"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(fsys.OSFS{}, filepath.Join(dir, "city.toml"))
	if err != nil {
		t.Fatalf("Load city.toml: %v", err)
	}
	c := NewDoltReadTimeoutRiskCheckForConfig(dir, false, cfg, nil)
	r := c.Run(&CheckContext{})
	if r.Status != StatusOK {
		t.Fatalf("Status = %d, want StatusOK for the managed default read_timeout; msg = %s", r.Status, r.Message)
	}
}
