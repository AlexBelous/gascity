package main

import (
	"fmt"
	"io"

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
