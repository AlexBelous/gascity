package procobserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/testutil"
)

func clientV3Fixture() (ReleasePolicyV3, ResponseV3, time.Time) {
	p, _, now := fixtureResponse()
	c, errors := v3Fixture()
	v := c.Certificates[0].Chain[1]
	r := ResponseV3{
		Schema: ResponseSchemaV3, Scope: "host_procfs", RequestNonce: strings.Repeat("e", 64),
		HelperSourceRevision: p.HelperSourceRevision, HelperBinarySHA256: p.HelperBinarySHA256, PolicyDigest: p.PolicyDigest,
		BootID: p.BootID, PIDNamespaceIdentity: p.PIDNamespaceIdentity, CallerBinding: p.CallerBinding,
		StartedAt: now.Add(-3 * time.Millisecond), FinishedAt: now, DurationMS: 3, Complete: true,
		EnumeratedCountBefore: c.Scans[0].EnumeratedCount, EnumeratedCountAfter: c.Scans[2].EnumeratedCount,
		EnumerationDigestBefore: c.Scans[0].EnumerationDigest, EnumerationDigestAfter: c.Scans[2].EnumerationDigest,
		Roots: []Root{{
			PID: v.PID, PPID: v.PPID, PGID: v.PGID, StartTicks: v.StartTicks, SessionID: v.SessionID,
			City: v.City, Template: v.Template, Epoch: v.Epoch, InstanceTokenSHA256: v.InstanceTokenSHA256, Name: v.Name,
		}},
		Errors: errors, ErrorsTotal: len(errors), KernelRelease: "6.8.fixture", KernelProofProfile: KernelProofProfile,
		Census: c, CertificateDisposition: "provisional",
	}
	return ReleasePolicyV3{Policy: p, EvidenceSchema: ResponseSchemaV3}, r, now
}

func TestClientV3FixtureFrameBoundIsHonored(t *testing.T) {
	for _, limit := range []int{8, MaxRequestBytes} {
		t.Run(fmt.Sprintf("limit_%d", limit), func(t *testing.T) {
			addr := &net.UnixAddr{Name: filepath.Join(t.TempDir(), "s"), Net: "unix"}
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

func TestClientV3PolicySelectorIsExact(t *testing.T) {
	for _, mode := range []string{"exact", "v2 selector", "missing selector", "null selector", "duplicate selector", "unknown field", "wrong source", "short source", "missing caller", "null UID"} {
		t.Run(mode, func(t *testing.T) {
			p, _, _ := clientV3Fixture()
			source := p.HelperSourceRevision
			if mode == "v2 selector" {
				p.EvidenceSchema = Schema
			}
			if mode == "wrong source" {
				source = strings.Repeat("0", 40)
			}
			if mode == "short source" {
				source = source[:8]
			}
			data, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "missing selector":
				data = bytes.Replace(data, []byte(`,"evidence_schema":"host-process-evidence/v3"`), nil, 1)
			case "null selector":
				data = bytes.Replace(data, []byte(`"evidence_schema":"host-process-evidence/v3"`), []byte(`"evidence_schema":null`), 1)
			case "duplicate selector":
				data = append(data[:len(data)-1], []byte(`,"evidence_schema":"host-process-evidence/v3"}`)...)
			case "unknown field":
				data = append(data[:len(data)-1], []byte(`,"refresh_binding":true}`)...)
			case "missing caller":
				data = bytes.Replace(data, []byte(`"caller_binding":`), []byte(`"other_binding":`), 1)
			case "null UID":
				data = bytes.Replace(data, []byte(`"helper_uid":42`), []byte(`"helper_uid":null`), 1)
			}
			_, err = decodePolicyV3(data, source)
			if (err == nil) != (mode == "exact") {
				t.Fatalf("mode %s: %v", mode, err)
			}
			if mode == "exact" {
				var old Policy
				if strictJSON(data, &old) == nil {
					t.Fatal("V2 silently consumed V3 selector")
				}
			}
		})
	}
}

func TestClientV3EnvelopePinsAndGrammar(t *testing.T) {
	for _, mode := range []string{"exact", "v2 wire", "v2 policy", "nonce", "helper source", "helper binary", "policy digest", "boot", "namespace", "caller", "future", "stale", "duration", "promoted helper", "partial", "wrong kernel", "status grammar pending", "UID shape", "unknown key", "duplicate key", "missing key", "null key", "root seal mismatch", "retired root", "selected closing binding", "missing sealed root"} {
		t.Run(mode, func(t *testing.T) {
			p, r, now := clientV3Fixture()
			nonce := r.RequestNonce
			switch mode {
			case "v2 wire":
				r.Schema = Schema
			case "v2 policy":
				p.EvidenceSchema = Schema
			case "nonce":
				r.RequestNonce = strings.Repeat("0", 64)
			case "helper source":
				r.HelperSourceRevision = strings.Repeat("0", 40)
			case "helper binary":
				r.HelperBinarySHA256 = strings.Repeat("0", 64)
			case "policy digest":
				r.PolicyDigest = strings.Repeat("0", 64)
			case "boot":
				r.BootID = "other"
			case "namespace":
				r.PIDNamespaceIdentity = "pid:[456]"
			case "caller":
				r.CallerBinding.PID++
			case "future":
				r.FinishedAt = now.Add(time.Second)
			case "stale":
				r.StartedAt = now.Add(-61 * time.Second)
			case "duration":
				r.DurationMS = 10000
			case "promoted helper":
				r.CertificateDisposition = "provider_verified"
			case "partial":
				r.Complete = false
			case "wrong kernel":
				r.KernelProofProfile = "fixture-only"
			case "status grammar pending":
				r.Errors[0].Operation = "status"
				r.Errors[0].Errno = 3
			case "root seal mismatch":
				r.Roots[0].StartTicks = "201"
			case "retired root":
				r.Roots[0].PID = 30
				r.Roots[0].StartTicks = "300"
			case "selected closing binding":
				r.EnumeratedCountAfter++
			case "missing sealed root":
				r.Roots = []Root{}
			}
			data, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "UID shape":
				data = bytes.ReplaceAll(data, []byte(`"uids":[1000,1000,1000,1000]`), []byte(`"uids":[1000,1000,1000]`))
			case "unknown key":
				data = append(data[:len(data)-1], []byte(`,"secret_environment":"hidden"}`)...)
			case "duplicate key":
				data = append(data[:len(data)-1], []byte(`,"complete":true}`)...)
			case "missing key":
				data = bytes.Replace(data, []byte(`"complete":true,`), nil, 1)
			case "null key":
				data = bytes.Replace(data, []byte(`"complete":true`), []byte(`"complete":null`), 1)
			}
			got, err := DecodeResponseV3(data, p, nonce, now)
			if (err == nil) != (mode == "exact") {
				t.Fatalf("mode %s: %v", mode, err)
			}
			if mode == "partial" && (got.Schema != ResponseSchemaV3 || got.CertificateDisposition != "provisional" || len(got.Census.Certificates) != 1) {
				t.Fatal("trusted partial history lost")
			}
			if mode == "exact" {
				if len(got.ObservedRoots()) != 1 {
					t.Fatal("root lost")
				}
				anchors, e := got.RetirementAnchors()
				if e != nil || len(anchors) != 1 || anchors[0].Runtime.PID != 20 {
					t.Fatalf("anchors %v %v", anchors, e)
				}
			}
			if err != nil && strings.Contains(err.Error(), "hidden") {
				t.Fatal("input leaked in error")
			}
		})
	}
}

func TestClientV3UnixBoundedRequest(t *testing.T) {
	for _, mode := range []string{"exact", "v2 schema", "extra frame", "partial frame", "oversize", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			p, r, now := clientV3Fixture()
			path := filepath.Join(t.TempDir(), "s")
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

func TestClientV3CanceledReadDoesNotOpenTransport(t *testing.T) {
	p, _, _ := clientV3Fixture()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReadContextV3(ctx, p); err == nil {
		t.Fatal("canceled request accepted")
	}
}

func TestClientV3RootProjectionRequiresDeclaredIdentity(t *testing.T) {
	p, r, now := clientV3Fixture()
	for i := range r.Census.Scans {
		root := r.Census.Scans[i].Verified[0]
		root.DeclaredRoot = false
		r.Census.Scans[i].Verified = []CensusIdentity{root}
		r.Census.Scans[i].EnumeratedCount = 1
		r.Census.Scans[i].LiveCount = 1
		r.Census.Scans[i].EnumerationDigest = r.EnumerationDigestBefore
		r.Census.Scans[i].LiveDigest = r.Census.ReconciledDigest
	}
	r.Census.Proofs = []CensusProofV3{}
	r.Census.Certificates = []DescendantCertificate{}
	r.Census.Resolutions = []CensusResolution{}
	r.Errors = []EvidenceError{}
	r.ErrorsTotal = 0
	r.EnumeratedCountBefore = 1
	r.EnumeratedCountAfter = 1
	if err := ValidateCensusV3(r.Census, r.Errors, r.ErrorsTotal, r.ErrorsTruncated, r.DurationMS, p.CallerBinding.PID); err != nil {
		t.Fatalf("synthetic census invalid before projection check: %v", err)
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeResponseV3(data, p, r.RequestNonce, now); err == nil {
		t.Fatal("non-root managed identity was accepted as a root")
	}
	r.Roots = []Root{}
	data, err = json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeResponseV3(data, p, r.RequestNonce, now)
	if err != nil {
		t.Fatal(err)
	}
	anchors, err := got.RetirementAnchors()
	if err != nil || anchors == nil || len(anchors) != 0 {
		t.Fatal("empty valid certificate set lost its explicit non-nil contract")
	}
}
