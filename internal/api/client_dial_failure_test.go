package api

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"syscall"
	"testing"

	"github.com/gastownhall/gascity/internal/api/apierr"
)

// TestIsDialFailure pins the pre-request classifier: only a PROVABLE dial-phase
// failure (the controller was never reached) is a dial failure. A response or
// overall timeout, a mid-response EOF, or a real API verdict is
// ambiguous/terminal. Errors are also tested wrapped in *connError, the exact
// shape the client returns.
func TestIsDialFailure(t *testing.T) {
	wrap := func(err error) error { return &connError{err: fmt.Errorf("request failed: %w", err)} }

	dialRefused := &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}}
	dialMissingSocket := &net.OpError{Op: "dial", Net: "unix", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ENOENT}}
	dialTimeout := &net.OpError{Op: "dial", Net: "tcp", Err: os.ErrDeadlineExceeded}
	readTimeout := &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}

	tests := []struct {
		name string
		err  error
		want bool
	}{
		// Pre-request dial failures.
		{"dial refused", dialRefused, true},
		{"dial refused wrapped", wrap(dialRefused), true},
		{"dial missing unix socket", dialMissingSocket, true},
		{"dial missing socket wrapped", wrap(dialMissingSocket), true},
		{"dial connect timeout is still pre-request", dialTimeout, true},

		// Ambiguous / terminal. A bare errno without the dial-phase net.OpError
		// wrapper carries no phase evidence, so it is not provably pre-request.
		{"bare ECONNREFUSED lacks dial-phase proof", syscall.ECONNREFUSED, false},
		{"bare ENOENT lacks dial-phase proof", syscall.ENOENT, false},
		{"overall/response deadline", context.DeadlineExceeded, false},
		{"overall deadline wrapped", wrap(context.DeadlineExceeded), false},
		{"os deadline exceeded", os.ErrDeadlineExceeded, false},
		{"read-phase timeout (post-dial)", readTimeout, false},
		{"read-phase timeout wrapped", wrap(readTimeout), false},
		{"mid-response EOF", io.EOF, false},
		{"mid-response unexpected EOF", io.ErrUnexpectedEOF, false},
		{"EOF wrapped", wrap(io.EOF), false},
		{"real API verdict (503)", apierr.ServiceUnavailable.Msg("busy"), false},
		{"nil", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsDialFailure(tc.err); got != tc.want {
				t.Fatalf("IsDialFailure(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
