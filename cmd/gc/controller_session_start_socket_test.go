package main

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/testutil"
)

func TestPokeSessionStartControllerUsesExactKey(t *testing.T) {
	var commands []string
	fallbackCalled := false
	err := pokeSessionStartControllerWith(
		"/city",
		"ga-session-1",
		func(cityPath, command string) ([]byte, error) {
			if cityPath != "/city" {
				t.Fatalf("city path = %q, want /city", cityPath)
			}
			commands = append(commands, command)
			return []byte(sessionStartSocketReplyOK), nil
		},
		func(string) error {
			fallbackCalled = true
			return nil
		},
	)
	if err != nil {
		t.Fatalf("pokeSessionStartControllerWith: %v", err)
	}
	if fallbackCalled {
		t.Fatal("generic fallback called after exact admission succeeded")
	}
	want := []string{sessionStartCommandPrefix + "ga-session-1"}
	if len(commands) != len(want) || commands[0] != want[0] {
		t.Fatalf("commands = %v, want %v", commands, want)
	}
}

func TestPokeSessionStartControllerFallsBackWhenExactIngressIsUnavailable(t *testing.T) {
	fallbackCalls := 0
	err := pokeSessionStartControllerWith(
		"/city",
		"ga-session-1",
		func(string, string) ([]byte, error) {
			return []byte(sessionStartSocketReplyFallback), nil
		},
		func(cityPath string) error {
			if cityPath != "/city" {
				t.Fatalf("fallback city path = %q, want /city", cityPath)
			}
			fallbackCalls++
			return nil
		},
	)
	if err != nil {
		t.Fatalf("pokeSessionStartControllerWith: %v", err)
	}
	if fallbackCalls != 1 {
		t.Fatalf("fallback calls = %d, want 1", fallbackCalls)
	}
}

func TestHandleControllerConnRoutesValidatedSessionStartKey(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close() //nolint:errcheck

	admitted := make(chan string, 1)
	go handleControllerConn(
		server,
		"/city",
		func() {},
		nil,
		&atomic.Bool{},
		nil,
		make(chan convergenceRequest),
		make(chan struct{}, 1),
		make(chan struct{}, 1),
		func(sessionID string) sessionStartSocketReply {
			admitted <- sessionID
			return sessionStartSocketReplyOK
		},
	)

	if _, err := fmt.Fprintln(client, sessionStartCommandPrefix+"ga-session-1"); err != nil {
		t.Fatalf("write command: %v", err)
	}
	response, err := bufio.NewReader(client).ReadString('\n')
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if got := strings.TrimSpace(response); got != string(sessionStartSocketReplyOK) {
		t.Fatalf("response = %q, want %q", got, sessionStartSocketReplyOK)
	}
	select {
	case sessionID := <-admitted:
		if sessionID != "ga-session-1" {
			t.Fatalf("session ID = %q, want ga-session-1", sessionID)
		}
	case <-time.After(testutil.GoroutineRaceTimeout):
		t.Fatal("timed out waiting for exact session-start request")
	}
}

func TestHandleControllerConnRejectsInvalidSessionStartKey(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close() //nolint:errcheck

	admitCalled := false
	go handleControllerConn(
		server,
		"/city",
		func() {},
		nil,
		&atomic.Bool{},
		nil,
		make(chan convergenceRequest),
		make(chan struct{}, 1),
		make(chan struct{}, 1),
		func(string) sessionStartSocketReply {
			admitCalled = true
			return sessionStartSocketReplyOK
		},
	)

	if _, err := fmt.Fprintln(client, sessionStartCommandPrefix+" bad key "); err != nil {
		t.Fatalf("write command: %v", err)
	}
	response, err := bufio.NewReader(client).ReadString('\n')
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if got := strings.TrimSpace(response); got != string(sessionStartSocketReplyInvalid) {
		t.Fatalf("response = %q, want %q", got, sessionStartSocketReplyInvalid)
	}
	if admitCalled {
		t.Fatal("invalid key reached session-start admission")
	}
}

func TestCityRuntimeAdmitsSessionStartSocketKey(t *testing.T) {
	reconciled := make(chan sessionStartAdmission, 1)
	controller, err := newSessionStartController(sessionStartControllerOptions{
		Workers:     1,
		MaxDistinct: 8,
		MaxRetries:  0,
		Reconcile: func(_ context.Context, admission sessionStartAdmission) error {
			reconciled <- admission
			return nil
		},
	})
	if err != nil {
		t.Fatalf("newSessionStartController: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := controller.Start(ctx); err != nil {
		cancel()
		t.Fatalf("controller.Start: %v", err)
	}
	defer controller.Stop()

	cr := &CityRuntime{
		sessionStartController: controller,
		sessionStartOwnership:  sessionStartOwnershipKeyed,
	}
	if reply := cr.admitSessionStartSocketKey("ga-session-1"); reply != sessionStartSocketReplyOK {
		t.Fatalf("reply = %q, want %q", reply, sessionStartSocketReplyOK)
	}
	select {
	case admission := <-reconciled:
		if admission.SessionID != "ga-session-1" || admission.Source != sessionStartAdmissionSocket {
			t.Fatalf("admission = %+v, want exact socket key", admission)
		}
	case <-time.After(testutil.GoroutineRaceTimeout):
		t.Fatal("timed out waiting for keyed reconciliation")
	}
	cancel()
}

func TestCityRuntimeSessionStartSocketIngressFallsBackToLegacyOwner(t *testing.T) {
	cr := &CityRuntime{sessionStartOwnership: sessionStartOwnershipLegacy}
	if got := cr.admitSessionStartSocketKey("ga-session-1"); got != sessionStartSocketReplyFallback {
		t.Fatalf("reply = %q, want %q", got, sessionStartSocketReplyFallback)
	}
}
