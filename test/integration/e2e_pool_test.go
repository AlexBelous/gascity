//go:build integration

package integration

import (
	"strings"
	"testing"
)

// TestE2E_Pool_InstanceNaming verifies that pool agents with max>1 get
// numbered instance names (worker-1, worker-2, etc.).
func TestE2E_Pool_InstanceNaming(t *testing.T) {
	city := e2eCity{
		Agents: []e2eAgent{
			{
				Name:         "worker",
				StartCommand: e2eReportScript(),
				WorkDir:      ".gc/agents/{{.AgentBase}}",
				Pool:         &e2ePool{Min: 2, Max: 2, Check: "echo 2"},
			},
		},
	}
	cityDir := setupE2ECity(t, nil, city)

	r1 := waitForReport(t, cityDir, "worker-1", e2eDefaultTimeout())
	r2 := waitForReport(t, cityDir, "worker-2", e2eDefaultTimeout())

	if !r1.has("GC_AGENT", "worker-1") {
		t.Errorf("worker-1 GC_AGENT: got %v, want [worker-1]", r1.getAll("GC_AGENT"))
	}
	if !r2.has("GC_AGENT", "worker-2") {
		t.Errorf("worker-2 GC_AGENT: got %v, want [worker-2]", r2.getAll("GC_AGENT"))
	}
}

// TestE2E_Pool_MaxOneUsesCanonicalIdentity verifies that max=1 pool configs
// use the canonical singleton identity rather than concrete pool instance
// naming.
func TestE2E_Pool_MaxOneUsesCanonicalIdentity(t *testing.T) {
	city := e2eCity{
		Agents: []e2eAgent{
			{
				Name:         "singleton",
				StartCommand: e2eReportScript(),
				Pool:         &e2ePool{Min: 1, Max: 1, Check: "echo 1"},
			},
		},
	}
	cityDir := setupE2ECity(t, nil, city)

	report := waitForReport(t, cityDir, "singleton", e2eDefaultTimeout())
	if !report.has("GC_AGENT", "singleton") {
		t.Errorf("singleton GC_AGENT: got %v, want [singleton]", report.getAll("GC_AGENT"))
	}
}

// TestE2E_Pool_WithDir verifies that a pool agent with a dir but no
// per-instance work_dir template is rejected at init time: without a
// template, dirpool-1 and dirpool-2 would resolve to the identical working
// directory, which the pool work_dir isolation guard
// (internal/workdir/pool_isolation.go ValidatePoolWorkDirIsolation) fails
// closed on rather than silently letting instances collide.
func TestE2E_Pool_WithDir(t *testing.T) {
	city := e2eCity{
		Agents: []e2eAgent{
			{
				Name:         "dirpool",
				StartCommand: e2eReportScript(),
				Dir:          "workdir",
				Pool:         &e2ePool{Min: 2, Max: 2, Check: "echo 2"},
			},
		},
	}

	out := setupE2ECityExpectInitFailure(t, city)

	for _, want := range []string{"dirpool-1", "dirpool-2", "work_dir does not vary per instance", "{{.AgentBase}}"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in gc init rejection output:\n%s", want, out)
		}
	}
}

// TestE2E_Pool_SharedDir verifies that a pool agent with no dir and no
// per-instance work_dir template is rejected at init time: without a
// template, shared-1 and shared-2 would resolve to the identical working
// directory, which the pool work_dir isolation guard fails closed on.
func TestE2E_Pool_SharedDir(t *testing.T) {
	city := e2eCity{
		Agents: []e2eAgent{
			{
				Name:         "shared",
				StartCommand: e2eReportScript(),
				Pool:         &e2ePool{Min: 2, Max: 2, Check: "echo 2"},
			},
		},
	}

	out := setupE2ECityExpectInitFailure(t, city)

	for _, want := range []string{"shared-1", "shared-2", "work_dir does not vary per instance", "{{.AgentBase}}"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in gc init rejection output:\n%s", want, out)
		}
	}
}

// TestE2E_Pool_EnvPerInstance verifies that each pool instance gets its own
// GC_AGENT env var with the correct instance name.
func TestE2E_Pool_EnvPerInstance(t *testing.T) {
	city := e2eCity{
		Agents: []e2eAgent{
			{
				Name:         "envpool",
				StartCommand: e2eReportScript(),
				WorkDir:      ".gc/agents/{{.AgentBase}}",
				Env:          map[string]string{"CUSTOM_SHARED": "yes"},
				Pool:         &e2ePool{Min: 2, Max: 2, Check: "echo 2"},
			},
		},
	}
	cityDir := setupE2ECity(t, nil, city)

	r1 := waitForReport(t, cityDir, "envpool-1", e2eDefaultTimeout())
	r2 := waitForReport(t, cityDir, "envpool-2", e2eDefaultTimeout())

	// Each instance gets unique GC_AGENT.
	if !r1.has("GC_AGENT", "envpool-1") {
		t.Errorf("envpool-1 GC_AGENT: got %v", r1.getAll("GC_AGENT"))
	}
	if !r2.has("GC_AGENT", "envpool-2") {
		t.Errorf("envpool-2 GC_AGENT: got %v", r2.getAll("GC_AGENT"))
	}

	// Both share custom env.
	if !r1.has("CUSTOM_SHARED", "yes") {
		t.Error("envpool-1 missing CUSTOM_SHARED")
	}
	if !r2.has("CUSTOM_SHARED", "yes") {
		t.Error("envpool-2 missing CUSTOM_SHARED")
	}
}
