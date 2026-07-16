package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/nudgequeue"
)

func TestImmediateCLINudgeDurableAcceptanceDoesNotEnterProviderPath(t *testing.T) {
	target := nudgeTarget{
		cityPath:             "/path/that/does/not/exist",
		alias:                "worker",
		sessionID:            "session-a",
		sessionName:          "runtime-a",
		intentGeneration:     7,
		continuationIdentity: "continuation-a",
		launchIdentity:       "launch-a",
	}
	var admitted nudgequeue.NudgeIngressRequest
	var stdout, stderr bytes.Buffer
	code := deliverSessionNudgeWithIngress(
		target,
		"inspect the deploy",
		nudgeDeliveryImmediate,
		false,
		&stdout,
		&stderr,
		func(_ context.Context, cityPath string, request nudgequeue.NudgeIngressRequest) nudgeBridgeResult {
			if cityPath != target.cityPath {
				t.Fatalf("city path = %q, want %q", cityPath, target.cityPath)
			}
			admitted = request
			return nudgeBridgeResult{
				Disposition: nudgeBridgeDurableAccepted,
				CommandID:   "command-a",
				Status:      nudgequeue.CommandStatePending,
				Created:     true,
			}
		},
	)

	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("deliver code=%d stderr=%q, want success without provider/store access", code, stderr.String())
	}
	if admitted.RequestID == "" || admitted.Mode != nudgequeue.DeliveryModeImmediate || admitted.Source != nudgequeue.CommandSourceSession {
		t.Fatalf("admitted request = %#v, want identified immediate session command", admitted)
	}
	if admitted.Target.SessionID != "session-a" || admitted.Target.IntentGeneration != 7 ||
		admitted.Target.Policy != nudgequeue.TargetPolicyExactLaunch || admitted.Target.LaunchIdentity != "launch-a" ||
		admitted.Target.ContinuationIdentity != "" {
		t.Fatalf("admitted target = %#v, want canonical exact launch", admitted.Target)
	}
	if admitted.Message != "inspect the deploy" || !admitted.DeliverAfter.IsZero() || admitted.ExpiresAt.IsZero() {
		t.Fatalf("admitted delivery = message %q after=%s expires=%s", admitted.Message, admitted.DeliverAfter, admitted.ExpiresAt)
	}
	if got := stdout.String(); !strings.Contains(got, "Accepted nudge for worker") || !strings.Contains(got, "command-a") {
		t.Fatalf("stdout = %q, want accepted command identity", got)
	}
}

func TestImmediateCLINudgeAmbiguousAdmissionRefusesProviderFallback(t *testing.T) {
	target := nudgeTarget{
		cityPath:         "/path/that/does/not/exist",
		alias:            "worker",
		sessionID:        "session-a",
		intentGeneration: 7,
		launchIdentity:   "launch-a",
	}
	var stdout, stderr bytes.Buffer
	code := deliverSessionNudgeWithIngress(
		target,
		"inspect the deploy",
		nudgeDeliveryImmediate,
		false,
		&stdout,
		&stderr,
		func(context.Context, string, nudgequeue.NudgeIngressRequest) nudgeBridgeResult {
			return nudgeBridgeResult{Disposition: nudgeBridgeMayHaveEntered, Err: context.DeadlineExceeded}
		},
	)

	if code != 1 || stdout.Len() != 0 {
		t.Fatalf("deliver code=%d stdout=%q, want fail-closed error", code, stdout.String())
	}
	if got := stderr.String(); !strings.Contains(got, "refusing provider fallback") {
		t.Fatalf("stderr = %q, want explicit no-fallback diagnostic", got)
	}
}

func TestImmediateCLINudgeDefinitePreEntryRetainsLegacyPath(t *testing.T) {
	target := nudgeTarget{
		cityPath:         "/path/that/does/not/exist",
		alias:            "worker",
		sessionID:        "session-a",
		intentGeneration: 7,
		launchIdentity:   "launch-a",
	}
	var stdout, stderr bytes.Buffer
	code := deliverSessionNudgeWithIngress(
		target,
		"inspect the deploy",
		nudgeDeliveryImmediate,
		false,
		&stdout,
		&stderr,
		func(context.Context, string, nudgequeue.NudgeIngressRequest) nudgeBridgeResult {
			return nudgeBridgeResult{Disposition: nudgeBridgePreEntryUnavailable, Err: context.Canceled}
		},
	)

	if code != 1 || !strings.Contains(stderr.String(), "loading session") || strings.Contains(stderr.String(), "refusing provider fallback") {
		t.Fatalf("deliver code=%d stderr=%q, want legacy worker path after definite pre-entry", code, stderr.String())
	}
}
