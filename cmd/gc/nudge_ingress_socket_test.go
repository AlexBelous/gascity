package main

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/nudgequeue"
)

func TestControllerNudgeAdmissionSocketRejectsInvalidTokenBeforeAuthority(t *testing.T) {
	authority := &socketRecordingNudgeAuthority{
		tenant: "tenant-a", city: "city-a", credential: "credential-a",
	}
	wire := validControllerNudgeAdmissionWire()
	wire.Token = "wrong-token"

	reply := serveControllerNudgeAdmissionPayload(t, wire, controllerSocketConfig{
		nudgeToken:     "right-token",
		nudgeAuthority: authority,
	})

	if reply.Outcome != controllerNudgeAdmissionRejected || reply.Code != controllerNudgeAdmissionCodeUnauthorized {
		t.Fatalf("reply = %#v, want unauthorized rejection", reply)
	}
	if got := authority.admissionCount(); got != 0 {
		t.Fatalf("authority admissions = %d, want 0", got)
	}
}

func TestControllerNudgeAdmissionSocketReturnsDurableAcceptance(t *testing.T) {
	authority := &socketRecordingNudgeAuthority{
		tenant: "tenant-a", city: "city-a", credential: "credential-a",
		result: nudgequeue.NudgeIngressResult{
			Created: true,
			Entry: nudgequeue.CommandIndexEntry{Command: &nudgequeue.Command{
				ID:    "command-a",
				State: nudgequeue.CommandStatePending,
			}},
		},
	}
	wire := validControllerNudgeAdmissionWire()

	reply := serveControllerNudgeAdmissionPayload(t, wire, controllerSocketConfig{
		nudgeToken:     wire.Token,
		nudgeAuthority: authority,
	})

	if reply.Outcome != controllerNudgeAdmissionAccepted || reply.CommandID != "command-a" ||
		reply.Status != nudgequeue.CommandStatePending || !reply.Created {
		t.Fatalf("reply = %#v, want durable command-a acceptance", reply)
	}
	if got := authority.admissionCount(); got != 1 {
		t.Fatalf("authority admissions = %d, want 1", got)
	}
	if got := authority.lastRequest(); !reflect.DeepEqual(got, wire.domainRequest()) {
		t.Fatalf("authority request = %#v, want %#v", got, wire.domainRequest())
	}
}

func TestControllerNudgeAdmissionSocketWithoutCapabilityDeclaresLegacyOwnership(t *testing.T) {
	reply := serveControllerNudgeAdmissionPayload(t, validControllerNudgeAdmissionWire(), controllerSocketConfig{})
	if reply.Outcome != controllerNudgeAdmissionLegacy {
		t.Fatalf("reply = %#v, want explicit legacy ownership", reply)
	}
}

func TestControllerNudgeAdmissionSocketRejectsUnknownRequesterFields(t *testing.T) {
	wire := validControllerNudgeAdmissionWire()
	payload, err := json.Marshal(wire)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	payload = bytes.Replace(payload, []byte(`"request_id"`), []byte(`"principal_id":"forged","request_id"`), 1)
	var out bytes.Buffer
	handleControllerNudgeAdmissionPayload(t.Context(), &out, string(payload), controllerSocketConfig{
		nudgeToken:     wire.Token,
		nudgeAuthority: &socketRecordingNudgeAuthority{},
	})

	var reply controllerNudgeAdmissionReply
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &reply); err != nil {
		t.Fatalf("Unmarshal reply: %v; raw=%q", err, out.String())
	}
	if reply.Outcome != controllerNudgeAdmissionRejected || reply.Code != controllerNudgeAdmissionCodeInvalid {
		t.Fatalf("reply = %#v, want strict invalid-request rejection", reply)
	}
}

func TestControllerNudgeAdmissionWireHasNoRequesterAuthorityFields(t *testing.T) {
	typeOfWire := reflect.TypeOf(controllerNudgeAdmissionRequest{})
	for i := 0; i < typeOfWire.NumField(); i++ {
		name := strings.Split(typeOfWire.Field(i).Tag.Get("json"), ",")[0]
		switch name {
		case "principal_id", "tenant_scope", "city_scope", "credential_class", "evidence_id", "policy_version", "policy_decision_id":
			t.Fatalf("socket request exposes caller-owned authority field %q", name)
		}
	}
}

func serveControllerNudgeAdmissionPayload(t *testing.T, wire controllerNudgeAdmissionRequest, config controllerSocketConfig) controllerNudgeAdmissionReply {
	t.Helper()
	payload, err := json.Marshal(wire)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var out bytes.Buffer
	handleControllerNudgeAdmissionPayload(t.Context(), &out, string(payload), config)
	var reply controllerNudgeAdmissionReply
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &reply); err != nil {
		t.Fatalf("Unmarshal reply: %v; raw=%q", err, out.String())
	}
	return reply
}

func validControllerNudgeAdmissionWire() controllerNudgeAdmissionRequest {
	return controllerNudgeAdmissionRequest{
		Token:            "right-token",
		RequestID:        "request-a",
		Mode:             nudgequeue.DeliveryModeImmediate,
		SessionID:        "session-a",
		IntentGeneration: 7,
		LaunchIdentity:   "launch-a",
		TargetPolicy:     nudgequeue.TargetPolicyExactLaunch,
		Source:           nudgequeue.CommandSourceSession,
		Message:          "hello",
		DeliverAfter:     nil,
		ExpiresAt:        time.Now().UTC().Add(time.Hour),
	}
}

type socketRecordingNudgeAuthority struct {
	mu         sync.Mutex
	tenant     string
	city       string
	credential string
	request    nudgequeue.NudgeIngressRequest
	result     nudgequeue.NudgeIngressResult
	err        error
	admissions int
}

func (a *socketRecordingNudgeAuthority) RequesterScope() (string, string, string) {
	return a.tenant, a.city, a.credential
}

func (a *socketRecordingNudgeAuthority) Admit(_ context.Context, request nudgequeue.NudgeIngressRequest) (nudgequeue.NudgeIngressResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.admissions++
	a.request = request
	return a.result, a.err
}

func (a *socketRecordingNudgeAuthority) admissionCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.admissions
}

func (a *socketRecordingNudgeAuthority) lastRequest() nudgequeue.NudgeIngressRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.request
}
