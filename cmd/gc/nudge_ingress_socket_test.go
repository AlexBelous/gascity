package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/nudgequeue"
	"github.com/gastownhall/gascity/internal/testutil"
)

func TestLocalNudgeBridgeDialFailureIsDefinitePreEntry(t *testing.T) {
	dials := 0
	result := admitNudgeThroughLocalControllerWithDeps(
		t.Context(),
		"/city/a",
		validControllerNudgeAdmissionWire().domainRequest(),
		func(string) (string, error) { return "token-a", nil },
		func(context.Context, string, string) (net.Conn, error) {
			dials++
			return nil, errors.New("socket absent")
		},
	)
	if result.Disposition != nudgeBridgePreEntryUnavailable {
		t.Fatalf("result = %#v, want definite pre-entry unavailable", result)
	}
	if dials != 1 {
		t.Fatalf("dial count = %d, want 1", dials)
	}
}

func TestLocalNudgeBridgePartialWriteNeverFallsBack(t *testing.T) {
	first := newScriptedNudgeConn(nil)
	first.writeLimit = 8
	first.writeErr = io.ErrClosedPipe
	dial := newScriptedNudgeDial(
		scriptedNudgeDialStep{conn: first},
		scriptedNudgeDialStep{err: errors.New("controller disappeared")},
	)

	result := admitNudgeThroughLocalControllerWithDeps(
		t.Context(), "/city/a", validControllerNudgeAdmissionWire().domainRequest(),
		func(string) (string, error) { return "token-a", nil }, dial.call,
	)
	if result.Disposition != nudgeBridgeMayHaveEntered {
		t.Fatalf("result = %#v, want fail-closed may-have-entered", result)
	}
	if first.written.Len() == 0 {
		t.Fatal("partial-write fixture did not enter the transport")
	}
	if dial.calls() != 2 {
		t.Fatalf("dial count = %d, want one idempotent retry", dial.calls())
	}
}

func TestLocalNudgeBridgeRetriesLostResponseWithSameRequestID(t *testing.T) {
	first := newScriptedNudgeConn(nil)
	first.readErr = io.ErrUnexpectedEOF
	second := newScriptedNudgeConn(controllerNudgeReplyBytes(t, controllerNudgeAdmissionReply{
		Outcome: controllerNudgeAdmissionAccepted, CommandID: "command-a", Status: nudgequeue.CommandStatePending, Created: true,
	}))
	dial := newScriptedNudgeDial(
		scriptedNudgeDialStep{conn: first},
		scriptedNudgeDialStep{conn: second},
	)

	request := validControllerNudgeAdmissionWire().domainRequest()
	result := admitNudgeThroughLocalControllerWithDeps(
		t.Context(), "/city/a", request,
		func(string) (string, error) { return "token-a", nil }, dial.call,
	)
	if result.Disposition != nudgeBridgeDurableAccepted || result.CommandID != "command-a" {
		t.Fatalf("result = %#v, want durable command-a acceptance", result)
	}
	firstWire := decodeWrittenNudgeRequest(t, first.written.Bytes())
	secondWire := decodeWrittenNudgeRequest(t, second.written.Bytes())
	if firstWire.RequestID != request.RequestID || secondWire.RequestID != request.RequestID || firstWire.RequestID != secondWire.RequestID {
		t.Fatalf("retry request IDs = %q/%q, want stable %q", firstWire.RequestID, secondWire.RequestID, request.RequestID)
	}
}

func TestLocalNudgeBridgeLegacyReplyAfterAmbiguousEntryStaysFailClosed(t *testing.T) {
	first := newScriptedNudgeConn(nil)
	first.readErr = io.ErrUnexpectedEOF
	second := newScriptedNudgeConn(controllerNudgeReplyBytes(t, controllerNudgeAdmissionReply{
		Outcome: controllerNudgeAdmissionLegacy,
	}))
	dial := newScriptedNudgeDial(
		scriptedNudgeDialStep{conn: first},
		scriptedNudgeDialStep{conn: second},
	)

	result := admitNudgeThroughLocalControllerWithDeps(
		t.Context(), "/city/a", validControllerNudgeAdmissionWire().domainRequest(),
		func(string) (string, error) { return "token-a", nil }, dial.call,
	)
	if result.Disposition != nudgeBridgeMayHaveEntered {
		t.Fatalf("result = %#v, want fail-closed ambiguity", result)
	}
}

func TestLocalNudgeBridgeInitialLegacyReplyPermitsLegacyOwner(t *testing.T) {
	conn := newScriptedNudgeConn(controllerNudgeReplyBytes(t, controllerNudgeAdmissionReply{
		Outcome: controllerNudgeAdmissionLegacy,
	}))
	dial := newScriptedNudgeDial(scriptedNudgeDialStep{conn: conn})

	result := admitNudgeThroughLocalControllerWithDeps(
		t.Context(), "/city/a", validControllerNudgeAdmissionWire().domainRequest(),
		func(string) (string, error) { return "token-a", nil }, dial.call,
	)
	if result.Disposition != nudgeBridgeLegacyOwnership {
		t.Fatalf("result = %#v, want explicit legacy ownership", result)
	}
}

func TestHandleControllerConnRoutesAuthenticatedNudgeAdmission(t *testing.T) {
	authority := &socketRecordingNudgeAuthority{
		tenant: "tenant-a", city: "city-a", credential: "credential-a",
		result: nudgequeue.NudgeIngressResult{Entry: nudgequeue.CommandIndexEntry{
			Command: &nudgequeue.Command{ID: "command-a", State: nudgequeue.CommandStatePending},
		}},
	}
	wire := validControllerNudgeAdmissionWire()
	payload, err := json.Marshal(wire)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	server, client := net.Pipe()
	t.Cleanup(func() {
		_ = server.Close()
		_ = client.Close()
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		handleControllerConn(
			server,
			"/city/a",
			func() {},
			nil,
			nil,
			nil,
			nil,
			make(chan struct{}, 1),
			make(chan struct{}, 1),
			withControllerNudgeAdmission(wire.Token, authority),
		)
	}()
	deadline := time.Now().Add(testutil.GoroutineRaceTimeout)
	if err := client.SetDeadline(deadline); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}
	if _, err := fmt.Fprintf(client, "%s%s\n", controllerNudgeAdmissionCommandPrefix, payload); err != nil {
		t.Fatalf("write request: %v", err)
	}
	line, err := bufio.NewReader(client).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read reply: %v", err)
	}
	var reply controllerNudgeAdmissionReply
	if err := json.Unmarshal(bytes.TrimSpace(line), &reply); err != nil {
		t.Fatalf("Unmarshal reply: %v", err)
	}
	if reply.Outcome != controllerNudgeAdmissionAccepted || reply.CommandID != "command-a" {
		t.Fatalf("reply = %#v, want accepted command-a", reply)
	}
	select {
	case <-done:
	case <-time.After(testutil.GoroutineRaceTimeout):
		t.Fatal("controller connection handler did not exit")
	}
}

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

type scriptedNudgeConn struct {
	written    bytes.Buffer
	read       *bytes.Reader
	readErr    error
	writeLimit int
	writeErr   error
}

func newScriptedNudgeConn(reply []byte) *scriptedNudgeConn {
	return &scriptedNudgeConn{read: bytes.NewReader(reply), writeLimit: -1}
}

func (c *scriptedNudgeConn) Read(p []byte) (int, error) {
	if c.read != nil && c.read.Len() > 0 {
		return c.read.Read(p)
	}
	if c.readErr != nil {
		err := c.readErr
		c.readErr = nil
		return 0, err
	}
	return 0, io.EOF
}

func (c *scriptedNudgeConn) Write(p []byte) (int, error) {
	limit := len(p)
	if c.writeLimit >= 0 && c.writeLimit < limit {
		limit = c.writeLimit
	}
	if limit > 0 {
		_, _ = c.written.Write(p[:limit])
	}
	if c.writeErr != nil {
		err := c.writeErr
		c.writeErr = nil
		return limit, err
	}
	return limit, nil
}

func (*scriptedNudgeConn) Close() error                     { return nil }
func (*scriptedNudgeConn) LocalAddr() net.Addr              { return scriptedNudgeAddr("local") }
func (*scriptedNudgeConn) RemoteAddr() net.Addr             { return scriptedNudgeAddr("remote") }
func (*scriptedNudgeConn) SetDeadline(time.Time) error      { return nil }
func (*scriptedNudgeConn) SetReadDeadline(time.Time) error  { return nil }
func (*scriptedNudgeConn) SetWriteDeadline(time.Time) error { return nil }

type scriptedNudgeAddr string

func (a scriptedNudgeAddr) Network() string { return "unix" }
func (a scriptedNudgeAddr) String() string  { return string(a) }

type scriptedNudgeDialStep struct {
	conn net.Conn
	err  error
}

type scriptedNudgeDial struct {
	mu    sync.Mutex
	steps []scriptedNudgeDialStep
	count int
}

func newScriptedNudgeDial(steps ...scriptedNudgeDialStep) *scriptedNudgeDial {
	return &scriptedNudgeDial{steps: steps}
}

func (d *scriptedNudgeDial) call(context.Context, string, string) (net.Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.count++
	if len(d.steps) == 0 {
		return nil, errors.New("unexpected dial")
	}
	step := d.steps[0]
	d.steps = d.steps[1:]
	return step.conn, step.err
}

func (d *scriptedNudgeDial) calls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.count
}

func controllerNudgeReplyBytes(t *testing.T, reply controllerNudgeAdmissionReply) []byte {
	t.Helper()
	data, err := json.Marshal(reply)
	if err != nil {
		t.Fatalf("Marshal reply: %v", err)
	}
	return append(data, '\n')
}

func decodeWrittenNudgeRequest(t *testing.T, written []byte) controllerNudgeAdmissionRequest {
	t.Helper()
	line := strings.TrimSpace(string(written))
	if !strings.HasPrefix(line, controllerNudgeAdmissionCommandPrefix) {
		t.Fatalf("written command = %q, missing nudge prefix", line)
	}
	var wire controllerNudgeAdmissionRequest
	if err := json.Unmarshal([]byte(strings.TrimPrefix(line, controllerNudgeAdmissionCommandPrefix)), &wire); err != nil {
		t.Fatalf("Unmarshal written request: %v", err)
	}
	return wire
}
