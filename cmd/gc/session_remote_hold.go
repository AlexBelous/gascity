package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/gastownhall/gascity/internal/runtime"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// Snapshot absence is not authority to destroy an occupied source. Always
// reread the controller store before lifecycle cleanup. Nil fronts are legacy
// test/unbacked paths; a real remote admission requires a concrete source store.
func remoteAdmissionSourceHeld(id string, front *sessionpkg.Store, stderr io.Writer) bool {
	if id == "" || front == nil {
		return false
	}
	held, err := front.RemoteAdmissionHeld(id)
	if held || err != nil {
		fmt.Fprintf(stderr, "session reconciler: holding source %s for VM authority reconciliation\n", id) //nolint:errcheck
		return true
	}
	return false
}

// reconcileRemoteAdmissionSourceHold is the level-triggered controller consumer
// of fresh VM exit evidence. Only the SDK's exact authority/identity/CAS checks
// may clear a reservation; elapsed time and transport absence never do.
func reconcileRemoteAdmissionSourceHold(ctx context.Context, id string, front *sessionpkg.Store, sp runtime.Provider, stderr io.Writer) bool {
	if id == "" || front == nil {
		return false
	}
	if stderr == nil {
		stderr = io.Discard
	}
	held, err := front.RemoteAdmissionHeld(id)
	if err != nil || !held {
		return remoteAdmissionSourceHeld(id, front, stderr)
	}
	request, active, err := front.HeldRemoteAdmissionRequest(id)
	if err != nil || !active || request.Attempt == "" {
		return remoteAdmissionSourceHeld(id, front, stderr)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	authority, err := sessionpkg.ContextWithProviderRemoteAdmission(bounded, sp)
	if err == nil {
		err = front.ReconcileRemoteAdmissionHold(authority, id)
	}
	if err != nil {
		fmt.Fprintf(stderr, "session reconciler: remote authority for %s remains unconfirmed\n", id) //nolint:errcheck
	}
	return remoteAdmissionSourceHeld(id, front, stderr)
}
