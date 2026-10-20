package ssh

import (
	"strings"
	"testing"
)

func TestStrictEndpointIgnoresAmbientAuthority(t *testing.T) {
	args := strings.Join(sshArgs(Endpoint{Host: "192.0.2.1", User: "worker", KeyPath: "/keys/id", KnownHostsPath: "/keys/hosts", Strict: true}, []string{"true"}), " ")
	for _, want := range []string{"-F /dev/null", "StrictHostKeyChecking=yes", "IdentitiesOnly=yes", "IdentityAgent=none", "GlobalKnownHostsFile=/dev/null", "ControlMaster=no", "ControlPath=none", "ClearAllForwardings=yes", "PermitLocalCommand=no"} {
		if !strings.Contains(args, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(args, "accept-new") {
		t.Fatal("TOFU enabled")
	}
}

func TestStrictAttachIgnoresAmbientAuthority(t *testing.T) {
	args := strings.Join(attachArgs(Endpoint{Host: "192.0.2.1", User: "worker", KeyPath: "/keys/id", KnownHostsPath: "/keys/hosts", Strict: true}, "session"), " ")
	for _, want := range []string{"-F /dev/null", "StrictHostKeyChecking=yes", "IdentityAgent=none", "ControlPath=none", "PermitLocalCommand=no"} {
		if !strings.Contains(args, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(args, "accept-new") {
		t.Fatal("TOFU enabled")
	}
}
