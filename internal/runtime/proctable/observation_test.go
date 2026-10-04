package proctable

import (
	"testing"
)

func TestObservedEnvironmentRejectsAmbiguousIdentity(t *testing.T) {
	for _, data := range []string{"GC_SESSION_ID=a", "GC_SESSION_ID=a\x00GC_SESSION_ID=b\x00", "malformed\x00"} {
		if _, err := parseObservedEnvironment([]byte(data)); err == nil {
			t.Fatalf("accepted ambiguous identity %q", data)
		}
	}
	env, err := parseObservedEnvironment([]byte("GC_SESSION_ID=a\x00GC_CITY_PATH=/city with spaces\x00"))
	if err != nil || env["GC_CITY_PATH"] != "/city with spaces" {
		t.Fatalf("NUL parsing damaged exact identity: %v", err)
	}
}

func TestObservedRootsDoNotExposeCredentialOrDescendants(t *testing.T) {
	env := map[string]string{"GC_SESSION_ID": "sid", "GC_CITY_PATH": "/city", "GC_TEMPLATE": "worker", "GC_RUNTIME_EPOCH": "2", "GC_INSTANCE_TOKEN": "private-holder-credential"}
	rows := []observedProcess{
		{record: ProcessRecord{PID: 20, PPID: 1, StartTime: "start", Name: "agent"}, env: env},
		{record: ProcessRecord{PID: 21, PPID: 20, StartTime: "child", Name: "agent-child"}, env: env},
		{record: ProcessRecord{PID: 22, PPID: 1, StartTime: "server", Name: "tmux: server"}, env: env},
	}
	got, err := rootsFromObservedProcesses(rows)
	if err != nil || len(got) != 1 || got[0].Runtime.PID != 20 || len(got[0].InstanceTokenSHA256) != 64 {
		t.Fatalf("bad root evidence %+v, %v", got, err)
	}
}

func TestObservedRootRequiresExactRunAndParent(t *testing.T) {
	for _, key := range []string{"GC_CITY_PATH", "GC_TEMPLATE", "GC_RUNTIME_EPOCH", "GC_INSTANCE_TOKEN", "parent"} {
		t.Run(key, func(t *testing.T) {
			env := map[string]string{"GC_SESSION_ID": "sid", "GC_CITY_PATH": "/city", "GC_TEMPLATE": "worker", "GC_RUNTIME_EPOCH": "2", "GC_INSTANCE_TOKEN": "private-holder-credential"}
			p := observedProcess{record: ProcessRecord{PID: 20, PPID: 1, StartTime: "start", Name: "agent"}, env: env}
			if key == "parent" {
				p.record.PPID = 19
			} else {
				delete(env, key)
			}
			if _, err := rootsFromObservedProcesses([]observedProcess{p}); err == nil {
				t.Fatal("incomplete identity became coverage")
			}
		})
	}
}
