package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/runtime"
)

func TestRemoteAuthorityStrictResponse(t *testing.T) {
	p := remoteOperatorProfile{Binding: runtime.RuntimeBinding{Selector: "ssh:worker@192.0.2.1", Endpoint: "worker@192.0.2.1", ResourceDomain: "fixture", PolicyDigest: strings.Repeat("a", 64)}, ControlUser: "control", ControlKeyPath: "/keys/control", KnownHostsPath: "/keys/hosts"}
	r := remoteWireRequest{Attempt: strings.Repeat("b", 32), Binding: p.Binding}
	for _, output := range []string{`{"schema_version":1,"schema_version":1,"receipt":{}}`, `{"schema_version":1,"receipt":{},"unknown":true}`} {
		c := remoteAuthorityClient{profile: p, execute: func(context.Context, []string, []byte) ([]byte, error) { return []byte(output), nil }}
		if _, err := c.call(context.Background(), "observe", r, nil, nil); err == nil {
			t.Fatal("accepted ambiguous response")
		}
	}
	receipt := remoteWireReceipt{Request: r, AuthorityReceiptID: "fixture", JournalRevision: 1, Phase: "prepared"}
	empty := false
	encoded, err := json.Marshal(remoteRPCResponse{SchemaVersion: 1, Receipt: receipt, ScopeEmpty: &empty, NeverEntered: &empty, ScopeIdentity: &remoteScopeIdentity{}})
	if err != nil {
		t.Fatal(err)
	}
	c := remoteAuthorityClient{profile: p, execute: func(_ context.Context, args []string, input []byte) ([]byte, error) {
		flat := strings.Join(args, " ")
		for _, want := range []string{"-F /dev/null", "StrictHostKeyChecking=yes", "IdentityAgent=none", "control@192.0.2.1 rpc"} {
			if !strings.Contains(flat, want) {
				t.Errorf("missing %s", want)
			}
		}
		var request remoteRPCRequest
		if err := strictRemoteJSON(input, &request); err != nil {
			t.Fatal(err)
		}
		if request.Operation != "prepare" || request.Request != r {
			t.Fatal("wrong request")
		}
		return encoded, nil
	}}
	if got, err := c.call(context.Background(), "prepare", r, nil, nil); err != nil || got != receipt {
		t.Fatalf("got %v %v", got, err)
	}
}

func TestRemoteAuthorityDeniesArbitraryOperationAndStaging(t *testing.T) {
	c := remoteAuthorityClient{}
	if _, err := c.call(context.Background(), "shell", remoteWireRequest{}, nil, nil); err == nil {
		t.Fatal("accepted shell")
	}
}

// TestRemoteAuthorityEmptyScopeRequiresExplicitProof rejects an exited string
// without the separately attested persistent-parent identity.
func TestRemoteAuthorityEmptyScopeRequiresExplicitProof(t *testing.T) {
	p := remoteOperatorProfile{Binding: runtime.RuntimeBinding{Selector: "ssh:worker@192.0.2.1", Endpoint: "worker@192.0.2.1", ResourceDomain: "fixture", PolicyDigest: strings.Repeat("a", 64)}, CgroupParent: "/fixture.slice", ControlUser: "control", ControlKeyPath: "/keys/control", KnownHostsPath: "/keys/hosts"}
	r := remoteWireRequest{Attempt: strings.Repeat("b", 32), Binding: p.Binding}
	falseValue := false
	receipt := remoteWireReceipt{Request: r, AuthorityReceiptID: "fixture", JournalRevision: 1, Phase: "exited", Identity: remoteWireIdentity{BootID: strings.Repeat("c", 32)}}
	for _, response := range []remoteRPCResponse{
		{SchemaVersion: 1, Receipt: receipt},
		{SchemaVersion: 1, Receipt: receipt, ScopeEmpty: &falseValue},
	} {
		encoded, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		client := remoteAuthorityClient{profile: p, execute: func(context.Context, []string, []byte) ([]byte, error) { return encoded, nil }}
		if _, err := client.call(context.Background(), "observe", r, nil, nil); err == nil {
			t.Fatal("accepted missing scope proof fields")
		}
	}
	trueValue := true
	bad := remoteRPCResponse{SchemaVersion: 1, Receipt: receipt, ScopeEmpty: &trueValue, NeverEntered: &falseValue, ScopeIdentity: &remoteScopeIdentity{BootID: strings.Repeat("d", 32), Device: 1, Inode: 2, CgroupPath: p.CgroupParent}}
	encoded, err := json.Marshal(bad)
	if err != nil {
		t.Fatal(err)
	}
	client := remoteAuthorityClient{profile: p, execute: func(context.Context, []string, []byte) ([]byte, error) { return encoded, nil }}
	if _, err := client.call(context.Background(), "observe", r, nil, nil); err == nil {
		t.Fatal("accepted different-boot empty proof")
	}
}
