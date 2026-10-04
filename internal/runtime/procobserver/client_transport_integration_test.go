//go:build integration

package procobserver

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestUnixExchangeUsesOneBoundedFrameAndHalfClose(t *testing.T) {
	for _, mode := range []string{"exact", "oversize", "extra", "partial frame", "ancillary FD", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			p, r, now := fixtureResponse()
			dir, err := os.MkdirTemp("/tmp", "gpo-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			path := filepath.Join(dir, "socket")
			addr := &net.UnixAddr{Name: path, Net: "unix"}
			lis, err := net.ListenUnix("unix", addr)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = lis.Close() }() //nolint:errcheck // isolated fixture cleanup
			done := make(chan error, 1)
			releaseTimeout := make(chan struct{})
			defer func() {
				select {
				case <-releaseTimeout:
				default:
					close(releaseTimeout)
				}
			}()
			go func() {
				c, err := lis.AcceptUnix()
				if err != nil {
					done <- err
					return
				}
				defer func() { _ = c.Close() }() //nolint:errcheck // isolated fixture cleanup
				_ = c.SetDeadline(time.Now().Add(time.Second))
				data, err := readFrame(c, MaxRequestBytes)
				if err != nil {
					done <- err
					return
				}
				var req Request
				err = json.Unmarshal(data, &req)
				if err != nil {
					done <- err
					return
				}
				r.RequestNonce = req.RequestNonce
				var extra [1]byte
				n, e := c.Read(extra[:])
				if n != 0 || e == nil {
					done <- e
					return
				}
				if mode == "timeout" {
					<-releaseTimeout
					done <- nil
					return
				}
				data, _ = json.Marshal(r)
				if mode == "ancillary FD" {
					f, e := os.Open(os.DevNull)
					if e != nil {
						done <- e
						return
					}
					defer func() { _ = f.Close() }() //nolint:errcheck // isolated fixture cleanup
					_, _, e = c.WriteMsgUnix([]byte{0, 0, 0, 1, '{'}, syscall.UnixRights(int(f.Fd())), nil)
					done <- e
					return
				}
				if mode == "oversize" {
					data = make([]byte, MaxResponseBytes+1)
				}
				if mode == "partial frame" {
					_, err = c.Write([]byte{0, 0, 0, 8, '{'})
					done <- err
					return
				}
				err = writeFrame(c, data)
				if mode == "extra" {
					_, err = c.Write([]byte{1})
				}
				done <- err
			}()
			c, err := net.DialUnix("unix", nil, addr)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = c.Close() }() //nolint:errcheck // isolated fixture cleanup
			_ = c.SetDeadline(time.Now().Add(time.Second))
			observedTimeout := false
			got, err := exchangeWithReader(c, p, func() time.Time { return now }, func(data []byte) (int, error) {
				n, e := readNoAncillary(c, data)
				var timeout net.Error
				if errors.As(e, &timeout) && timeout.Timeout() {
					observedTimeout = true
				}
				return n, e
			})
			if mode == "timeout" {
				close(releaseTimeout)
				if err == nil || !observedTimeout {
					t.Fatalf("real socket deadline not proved: %v", err)
				}
			}
			if mode == "exact" {
				if err != nil || len(got.Roots) != 0 {
					t.Fatalf("exchange %v", err)
				}
			} else if err == nil {
				t.Fatal("accepted malformed frame")
			}
			<-done
		})
	}
}

func TestExchangeContextCancellationClosesActualSocket(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "gccancel-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir) //nolint:errcheck // isolated fixture cleanup
	address := &net.UnixAddr{Name: filepath.Join(dir, "cancel.sock"), Net: "unix"}
	lis, err := net.ListenUnix("unix", address)
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close() //nolint:errcheck // isolated fixture cleanup
	c, err := net.DialUnix("unix", nil, address)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close() //nolint:errcheck // isolated fixture cleanup
	peer, err := lis.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close() //nolint:errcheck // isolated fixture cleanup
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	p, _, now := fixtureResponse()
	go func() { _, e := exchangeContext(ctx, c, p, func() time.Time { return now }); done <- e }()
	if _, err = readFrame(peer, MaxRequestBytes); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("canceled helper exchange accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("helper socket read survived cancellation")
	}
}
