package procobserver

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func fixtureResponse() (Policy, Response, time.Time) {
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	p := Policy{SocketPath: "/run/test.sock", HelperUID: 42, HelperSourceRevision: strings.Repeat("a", 40), HelperBinarySHA256: strings.Repeat("b", 64), PolicyDigest: strings.Repeat("c", 64), BootID: "01234567-0123-0123-0123-0123456789ab", PIDNamespaceIdentity: "pid:[123]", CallerBinding: CallerBinding{PID: 123, UID: 1000, StartTicks: "456", BootID: "01234567-0123-0123-0123-0123456789ab", ControllerSourceRevision: strings.Repeat("a", 40), ControllerBinarySHA256: strings.Repeat("d", 64)}}
	r := Response{Schema: Schema, Scope: "host_procfs", RequestNonce: strings.Repeat("e", 64), HelperSourceRevision: p.HelperSourceRevision, HelperBinarySHA256: p.HelperBinarySHA256, PolicyDigest: p.PolicyDigest, BootID: p.BootID, PIDNamespaceIdentity: p.PIDNamespaceIdentity, StartedAt: now, FinishedAt: now, Complete: true, EnumeratedCountBefore: 12, EnumeratedCountAfter: 12, EnumerationDigestBefore: strings.Repeat("f", 64), EnumerationDigestAfter: strings.Repeat("f", 64), Roots: []Root{}, Errors: []EvidenceError{}, CallerBinding: p.CallerBinding}
	return p, r, now
}

func TestValidateEvidenceCannotRefreshReplayOrPartial(t *testing.T) {
	for _, mode := range []string{"exact", "nonce", "source", "binary", "policy", "boot", "namespace", "caller", "stale", "future", "reverse", "duration", "partial", "errors", "truncated", "count", "digest", "nil roots", "nil errors", "bad root", "secret field", "duplicate key"} {
		t.Run(mode, func(t *testing.T) {
			p, r, now := fixtureResponse()
			nonce := r.RequestNonce
			switch mode {
			case "nonce":
				r.RequestNonce = strings.Repeat("1", 64)
			case "source":
				r.HelperSourceRevision = strings.Repeat("0", 40)
			case "binary":
				r.HelperBinarySHA256 = strings.Repeat("0", 64)
			case "policy":
				r.PolicyDigest = strings.Repeat("0", 64)
			case "boot":
				r.BootID = "other"
			case "namespace":
				r.PIDNamespaceIdentity = "pid:[456]"
			case "caller":
				r.CallerBinding.PID++
			case "stale":
				r.StartedAt = now.Add(-61 * time.Second)
			case "future":
				r.FinishedAt = now.Add(time.Second)
			case "reverse":
				r.StartedAt = now.Add(time.Second)
			case "duration":
				r.DurationMS = 10001
			case "partial":
				r.Complete = false
			case "errors":
				r.Errors = []EvidenceError{{Reason: "permission", Operation: "environ", PID: 42, Errno: 13}}
				r.ErrorsTotal = 1
			case "truncated":
				r.ErrorsTruncated = true
			case "count":
				r.EnumeratedCountAfter++
			case "digest":
				r.EnumerationDigestAfter = strings.Repeat("0", 64)
			case "nil roots":
				r.Roots = nil
			case "nil errors":
				r.Errors = nil
			case "bad root":
				r.Roots = []Root{{PID: 42}}
			}
			data, _ := json.Marshal(r)
			if mode == "secret field" {
				data = append(data[:len(data)-1], []byte(",\"raw_environment\":\"secret\"}")...)
			}
			if mode == "duplicate key" {
				data = append(data[:len(data)-1], []byte(",\"complete\":true}")...)
			}
			got, err := decodeResponse(data, p, nonce, now)
			if (err == nil) != (mode == "exact") {
				t.Fatalf("accepted %s roots=%+v error=%v", mode, got, err)
			}
			if err != nil && strings.Contains(err.Error(), "secret") {
				t.Fatal("error leaked input")
			}
		})
	}
}

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
					time.Sleep(1200 * time.Millisecond)
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
			got, err := exchange(c, p, func() time.Time { return now })
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

func TestEvidenceRequiresEveryScalarWithExactType(t *testing.T) {
	p, r, now := fixtureResponse()
	data, _ := json.Marshal(r)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"duration_ms", "errors_total", "errors_truncated", "complete", "enumerated_count_before", "schema", "scope", "caller_binding"} {
		for _, mode := range []string{"missing", "null", "wrong type"} {
			t.Run(key+"/"+mode, func(t *testing.T) {
				modified := map[string]json.RawMessage{}
				for k, v := range fields {
					modified[k] = v
				}
				switch mode {
				case "missing":
					delete(modified, key)
				case "null":
					modified[key] = json.RawMessage("null")
				case "wrong type":
					modified[key] = json.RawMessage("[]")
				}
				bad, _ := json.Marshal(modified)
				if _, err := decodeResponse(bad, p, r.RequestNonce, now); err == nil {
					t.Fatalf("missing/type key %s accepted", key)
				}
			})
		}
	}
}

func TestExactRootConversionAndNestedTypes(t *testing.T) {
	p, r, now := fixtureResponse()
	r.Roots = []Root{{PID: 42, PPID: 1, PGID: 42, StartTicks: "789", SessionID: "sid", City: "/city", Template: "worker", Epoch: 2, InstanceTokenSHA256: strings.Repeat("f", 64), Name: "agent", ParentName: "", ParentIsProviderInfrastructure: false}}
	data, _ := json.Marshal(r)
	got, err := decodeResponse(data, p, r.RequestNonce, now)
	if err != nil || len(got.ObservedRoots()) != 1 || got.ObservedRoots()[0].Runtime.IsTracked || got.ObservedRoots()[0].Runtime.ProviderName != "" || got.ObservedRoots()[0].StartIdentity != "789" {
		t.Fatalf("untrusted conversion %+v %v", got, err)
	}
	var object map[string]json.RawMessage
	_ = json.Unmarshal(data, &object)
	var rows []map[string]json.RawMessage
	_ = json.Unmarshal(object["roots"], &rows)
	for key := range rows[0] {
		t.Run(key, func(t *testing.T) {
			saved := rows[0][key]
			rows[0][key] = json.RawMessage("null")
			object["roots"], _ = json.Marshal(rows)
			bad, _ := json.Marshal(object)
			if _, e := decodeResponse(bad, p, r.RequestNonce, now); e == nil {
				t.Fatalf("null root field %s accepted", key)
			}
			rows[0][key] = saved
		})
	}
	var binding map[string]json.RawMessage
	_ = json.Unmarshal(object["caller_binding"], &binding)
	for key := range binding {
		t.Run("caller/"+key, func(t *testing.T) {
			saved := binding[key]
			delete(binding, key)
			object["caller_binding"], _ = json.Marshal(binding)
			bad, _ := json.Marshal(object)
			if _, e := decodeResponse(bad, p, r.RequestNonce, now); e == nil {
				t.Fatalf("missing caller field %s accepted", key)
			}
			binding[key] = saved
		})
	}
}

func TestCoverageCountsCannotUnderstateRoots(t *testing.T) {
	p, r, now := fixtureResponse()
	r.Roots = []Root{{PID: 42, PPID: 1, PGID: 42, StartTicks: "17", SessionID: "sid", City: "/city", Template: "worker", Epoch: 1, InstanceTokenSHA256: strings.Repeat("a", 64), Name: "worker", ParentName: "init"}}
	r.EnumeratedCountBefore = 1
	r.EnumeratedCountAfter = 1
	r.Roots = append(r.Roots, r.Roots[0])
	r.Roots[1].PID = 43
	r.Roots[1].SessionID = "sid2"
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = decodeResponse(b, p, r.RequestNonce, now); err == nil {
		t.Fatal("roots exceeded full census counts")
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
