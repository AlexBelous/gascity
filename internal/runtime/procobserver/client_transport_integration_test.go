//go:build integration

package procobserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/testutil"
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

// Unauthenticated readers are fixture-only; production uses the authenticated
// writer reader. Keep their identical transport behavior in the integration owner.
func exchangeContext(ctx context.Context, c *net.UnixConn, p Policy, now func() time.Time) (Response, error) {
	return exchangeContextWithReader(ctx, c, p, now, func(data []byte) (int, error) { return readNoAncillary(c, data) })
}

func exchange(c *net.UnixConn, p Policy, now func() time.Time) (Response, error) {
	return exchangeWithReader(c, p, now, func(data []byte) (int, error) { return readNoAncillary(c, data) })
}

func TestClientV3FixtureFrameBoundIsHonored(t *testing.T) {
	for _, limit := range []int{8, MaxRequestBytes} {
		t.Run(fmt.Sprintf("limit_%d", limit), func(t *testing.T) {
			addr := &net.UnixAddr{Name: filepath.Join(testutil.ShortTempDir(t, "gpo-v3-"), "s"), Net: "unix"}
			listener, err := net.ListenUnix("unix", addr)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close() //nolint:errcheck // owned fixture
			writer, err := net.DialUnix("unix", nil, addr)
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close() //nolint:errcheck // owned fixture
			reader, err := listener.AcceptUnix()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close() //nolint:errcheck // owned fixture
			_ = writer.SetDeadline(time.Now().Add(testutil.GoroutineRaceTimeout))
			_ = reader.SetDeadline(time.Now().Add(testutil.GoroutineRaceTimeout))
			payload := []byte("123456789")
			if err := writeFrame(writer, payload); err != nil {
				t.Fatal(err)
			}
			got, err := readFrame(reader, limit)
			if limit < len(payload) {
				if err == nil {
					t.Fatal("frame exceeded actual reader bound")
				}
			} else if err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("bounded valid frame changed: %v %q", err, got)
			}
		})
	}
}

func TestClientV3UnixBoundedRequest(t *testing.T) {
	for _, mode := range []string{"exact", "v2 schema", "extra frame", "partial frame", "oversize", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			p, r, now := clientV3Fixture()
			path := filepath.Join(testutil.ShortTempDir(t, "gpo-v3-"), "s")
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close() //nolint:errcheck // isolated fixture
			done := make(chan error, 1)
			ctx, cancel := context.WithTimeout(context.Background(), testutil.GoroutineRaceTimeout)
			defer cancel()
			go func() {
				c, e := listener.AcceptUnix()
				if e != nil {
					done <- e
					return
				}
				defer c.Close() //nolint:errcheck // isolated fixture
				_ = c.SetDeadline(time.Now().Add(testutil.GoroutineRaceTimeout))
				data, e := readFrame(c, MaxRequestBytes)
				if e != nil {
					done <- e
					return
				}
				var req Request
				if e = strictJSON(data, &req); e != nil {
					done <- e
					return
				}
				if req.Schema != RequestSchemaV3 || !isHex(req.RequestNonce, 64) {
					done <- io.ErrUnexpectedEOF
					return
				}
				var extra [1]byte
				if n, e := c.Read(extra[:]); n != 0 || !errors.Is(e, io.EOF) {
					done <- io.ErrUnexpectedEOF
					return
				}
				r.RequestNonce = req.RequestNonce
				if mode == "cancel" {
					cancel()
					done <- nil
					return
				}
				if mode == "v2 schema" {
					r.Schema = Schema
				}
				data, e = json.Marshal(r)
				if e != nil {
					done <- e
					return
				}
				if mode == "partial frame" {
					_, e = c.Write([]byte{0, 0, 0, 8, '{'})
					done <- e
					return
				}
				if mode == "oversize" {
					_, e = c.Write([]byte{1, 0, 0, 1})
					done <- e
					return
				}
				e = writeFrame(c, data)
				if e == nil && mode == "extra frame" {
					_, e = c.Write([]byte{0})
				}
				done <- e
			}()
			c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close() //nolint:errcheck // isolated fixture
			_, err = exchangeContextV3WithReader(ctx, c, p, func() time.Time { return now }, func(data []byte) (int, error) { return readNoAncillary(c, data) })
			if (err == nil) != (mode == "exact") {
				t.Fatalf("mode %s: %v", mode, err)
			}
			select {
			case e := <-done:
				if e != nil {
					t.Fatal(e)
				}
			case <-ctx.Done():
				if mode != "cancel" {
					t.Fatal("fixture server did not return")
				}
			}
		})
	}
}

func readNoAncillary(c *net.UnixConn, data []byte) (int, error) {
	oob := make([]byte, 32)
	n, on, flags, _, err := c.ReadMsgUnix(data, oob)
	if on != 0 {
		messages, _ := syscall.ParseSocketControlMessage(oob[:on])
		for _, message := range messages {
			fds, err := syscall.ParseUnixRights(&message)
			if err == nil {
				for _, fd := range fds {
					_ = syscall.Close(fd)
				}
			}
		}
	}
	if on != 0 || flags&(syscall.MSG_CTRUNC|syscall.MSG_TRUNC) != 0 {
		return 0, fmt.Errorf("observer ancillary data rejected")
	}
	return n, err
}

func readFrame(c *net.UnixConn, byteLimit int) ([]byte, error) {
	return readFrameWithReader(func(data []byte) (int, error) { return readNoAncillary(c, data) }, byteLimit)
}
