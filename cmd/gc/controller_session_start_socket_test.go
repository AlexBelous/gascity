package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/rollout"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/session"
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
	if err == nil || !strings.Contains(err.Error(), "exact session-start hint for \"ga-session-1\" in city \"/city\" was not admitted") || !strings.Contains(err.Error(), "controller returned \"fallback\"") || !strings.Contains(err.Error(), "generic fallback requested") {
		t.Fatalf("pokeSessionStartControllerWith = %v, want exact-ingress fallback diagnostic", err)
	}
	if fallbackCalls != 1 {
		t.Fatalf("fallback calls = %d, want 1", fallbackCalls)
	}
}

func TestPokeSessionStartControllerDoesNotFallBackWhenRequireRefusesClosed(t *testing.T) {
	fallbackCalls := 0
	err := pokeSessionStartControllerWith(
		"/city",
		"ga-session-1",
		func(string, string) ([]byte, error) {
			return []byte(sessionStartSocketReplyBlocked), nil
		},
		func(string) error {
			fallbackCalls++
			return nil
		},
	)
	if !errors.Is(err, errSessionStartControllerBlocked) {
		t.Fatalf("require refusal error = %v, want errors.Is(errSessionStartControllerBlocked)", err)
	}
	if fallbackCalls != 0 {
		t.Fatalf("fallback calls = %d, want 0", fallbackCalls)
	}
}

func TestHandleControllerConnRoutesValidatedSessionStartKey(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close() //nolint:errcheck

	admitted := make(chan string, 1)
	go handleControllerConn(
		server,
		"/city",
		controllerHostingStandalone,
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
		controllerHostingStandalone,
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
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker", StartCommand: "true"}}}
	bead := env.createSessionBead("worker", "worker")
	if err := env.store.SetMetadataBatch(bead.ID, session.RequestExplicitWakePatch(string(session.WakeCauseExplicit), env.clk.Now())); err != nil {
		t.Fatalf("request explicit wake: %v", err)
	}
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
		cfg:                    env.cfg,
		sp:                     env.sp,
		cs:                     coherentSessionStartControllerStateForTest(env.cfg, env.sp, env.store, rollout.Auto),
		sessionStartController: controller,
		sessionStartOwnership:  sessionStartOwnershipKeyed,
	}
	if reply := cr.admitSessionStartSocketKey(bead.ID); reply != sessionStartSocketReplyOK {
		t.Fatalf("reply = %q, want %q", reply, sessionStartSocketReplyOK)
	}
	select {
	case admission := <-reconciled:
		if admission.SessionID != bead.ID || admission.Source != sessionStartAdmissionSocket {
			t.Fatalf("admission = %+v, want exact socket key", admission)
		}
	case <-time.After(testutil.GoroutineRaceTimeout):
		t.Fatal("timed out waiting for keyed reconciliation")
	}
	cancel()
}

func TestCityRuntimeSessionStartSocketIngressFallsBackToLegacyOwner(t *testing.T) {
	var stderr bytes.Buffer
	cr := &CityRuntime{stderr: &stderr, sessionStartOwnership: sessionStartOwnershipLegacy}
	if got := cr.admitSessionStartSocketKey("ga-session-1"); got != sessionStartSocketReplyFallback {
		t.Fatalf("reply = %q, want %q", got, sessionStartSocketReplyFallback)
	}
	if !strings.Contains(stderr.String(), "exact session-start socket fallback for ga-session-1: controller unavailable or not keyed") {
		t.Fatalf("fallback diagnostic = %q, want unavailable-owner reason", stderr.String())
	}
}

func TestCityRuntimeSessionStartSocketIngressFallsBackForLegacyOwnedKey(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{
		{Name: "database", StartCommand: "true"},
		{Name: "worker", StartCommand: "true", DependsOn: []string{"database"}},
	}}
	bead := env.createSessionBead("worker", "worker")
	if err := env.store.SetMetadataBatch(bead.ID, session.RequestExplicitWakePatch(string(session.WakeCauseExplicit), env.clk.Now())); err != nil {
		t.Fatalf("request explicit wake: %v", err)
	}

	controller, err := newSessionStartController(sessionStartControllerOptions{
		Workers:     1,
		MaxDistinct: 8,
		MaxRetries:  0,
		Reconcile: func(context.Context, sessionStartAdmission) error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("newSessionStartController: %v", err)
	}
	if err := controller.Start(context.Background()); err != nil {
		t.Fatalf("controller.Start: %v", err)
	}
	t.Cleanup(controller.Stop)

	var stderr bytes.Buffer
	cr := &CityRuntime{
		cfg:                    env.cfg,
		sp:                     runtime.NewFake(),
		cs:                     coherentSessionStartControllerStateForTest(env.cfg, env.sp, env.store, rollout.Auto),
		sessionStartController: controller,
		sessionStartOwnership:  sessionStartOwnershipKeyed,
		stderr:                 &stderr,
	}
	if got := cr.admitSessionStartSocketKey(bead.ID); got != sessionStartSocketReplyFallback {
		t.Fatalf("reply = %q, want %q for a dependency-bearing legacy-owned start", got, sessionStartSocketReplyFallback)
	}
	if !strings.Contains(stderr.String(), "exact session-start socket fallback for "+bead.ID+": clean legacy ownership classification") {
		t.Fatalf("fallback diagnostic = %q, want clean legacy-owner reason", stderr.String())
	}
}

func TestCityRuntimeSessionStartSocketIngressClassifiesFromAuthoritativeRow(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker", StartCommand: "true"}}}
	backing := env.store
	bead := env.createSessionBead("worker", "worker")
	env.setSessionMetadata(&bead, map[string]string{"session_origin": "manual"})
	if err := backing.SetMetadataBatch(bead.ID, session.RequestExplicitWakePatch(string(session.WakeCauseExplicit), env.clk.Now())); err != nil {
		t.Fatalf("request explicit wake: %v", err)
	}
	cache := beads.NewCachingStore(backing, nil)
	if err := cache.PrimeActive(); err != nil {
		t.Fatalf("prime controller cache: %v", err)
	}
	if err := backing.SetMetadata(bead.ID, poolManagedMetadataKey, "true"); err != nil {
		t.Fatalf("mark backing row pool-managed: %v", err)
	}

	controller, err := newSessionStartController(sessionStartControllerOptions{
		Workers:     1,
		MaxDistinct: 8,
		MaxRetries:  0,
		Reconcile: func(context.Context, sessionStartAdmission) error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("newSessionStartController: %v", err)
	}
	if err := controller.Start(context.Background()); err != nil {
		t.Fatalf("controller.Start: %v", err)
	}
	t.Cleanup(controller.Stop)

	cr := &CityRuntime{
		cfg:                    env.cfg,
		sp:                     env.sp,
		cs:                     coherentSessionStartControllerStateForTest(env.cfg, env.sp, cache, rollout.Auto),
		sessionStartController: controller,
		sessionStartOwnership:  sessionStartOwnershipKeyed,
	}
	if got := cr.admitSessionStartSocketKey(bead.ID); got != sessionStartSocketReplyFallback {
		t.Fatalf("reply = %q, want %q for live pool row hidden by stale cache", got, sessionStartSocketReplyFallback)
	}
}
