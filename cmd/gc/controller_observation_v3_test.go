package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/runtime/observation"
	"github.com/gastownhall/gascity/internal/runtime/procobserver"
	"github.com/gastownhall/gascity/internal/testutil"
)

func TestControllerV3RelayClosingCancellation(t *testing.T) {
	for _, mode := range []string{"before policy", "after decode", "after closing peer"} {
		t.Run(mode, func(t *testing.T) {
			p, frames, now := controllerV3Fixture()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var out bytes.Buffer
			loads := 0
			load := func() (procobserver.ReleasePolicyV3, error) { loads++; return p, nil }
			if mode == "before policy" {
				cancel()
				err := relayControllerObservationV3At(ctx, "/city", "/fixture-not-open", p.HelperSourceRevision, &out, load,
					func(net.Conn, procobserver.CallerBinding) error { t.Fatal("canceled relay verified peer"); return nil },
					writeControllerObservationRequestV3, func() time.Time { return now })
				if err == nil || loads != 0 {
					t.Fatalf("already canceled relay loaded policy or succeeded: loads=%d err=%v", loads, err)
				}
				return
			}
			calls := 0
			reply := collectControllerObservationV3(context.Background(), "/city", p, controllerV3Provider(t),
				func(context.Context, procobserver.ReleasePolicyV3) (procobserver.ResponseV3, error) {
					f := frames[calls]
					calls++
					return f, nil
				}, func() time.Time { return now })
			data, err := json.Marshal(reply)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "s")
			lis, err := net.Listen("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			defer lis.Close() //nolint:errcheck // owned fixture
			done := make(chan error, 1)
			go func() {
				c, e := lis.Accept()
				if e != nil {
					done <- e
					return
				}
				defer c.Close() //nolint:errcheck // owned fixture
				_ = c.SetDeadline(time.Now().Add(testutil.GoroutineRaceTimeout))
				if _, e = bufio.NewReader(c).ReadString('\n'); e == nil {
					_, e = c.Write(data)
				}
				done <- e
			}()
			checks := 0
			verify := func(net.Conn, procobserver.CallerBinding) error {
				checks++
				if mode == "after closing peer" && checks == 2 {
					cancel()
				}
				return nil
			}
			relayNow := func() time.Time {
				if mode == "after decode" {
					cancel()
				}
				return now
			}
			err = relayControllerObservationV3At(ctx, "/city", path, p.HelperSourceRevision, &out, load, verify, writeControllerObservationRequestV3, relayNow)
			if err == nil {
				t.Fatal("cancellation published a late COMPLETE")
			}
			var got controllerObservationReplyV3
			if procobserver.DecodeStrict(out.Bytes(), &got) != nil || (got.ProviderComplete && got.ProcessComplete) || got.CertificateDisposition != "provisional" {
				t.Fatal("cancellation was not typed UNKNOWN")
			}
			if e := waitControllerV3Service(t, done); e != nil {
				t.Fatal(e)
			}
		})
	}
}

func controllerV3Fixture() (procobserver.ReleasePolicyV3, []procobserver.ResponseV3, time.Time) {
	now := time.Now().UTC()
	source := strings.Repeat("a", 40)
	digest := strings.Repeat("a", 64)
	p := procobserver.ReleasePolicyV3{Policy: procobserver.Policy{
		SocketPath: "/run/fixture.sock", HelperUID: 42, HelperSourceRevision: source, HelperBinarySHA256: strings.Repeat("b", 64), PolicyDigest: strings.Repeat("c", 64), BootID: "01234567-0123-0123-0123-0123456789ab", PIDNamespaceIdentity: "pid:[123]",
		CallerBinding: procobserver.CallerBinding{PID: 123, UID: 1000, StartTicks: "456", BootID: "01234567-0123-0123-0123-0123456789ab", ControllerSourceRevision: source, ControllerBinarySHA256: strings.Repeat("d", 64)},
	}, EvidenceSchema: procobserver.ResponseSchemaV3}
	root := procobserver.CensusIdentity{Classification: "managed", EnvironmentRevalidated: true, PID: 20, PPID: 2, PGID: 20, UIDs: [4]uint32{1000, 1000, 1000, 1000}, StartTicks: "200", SessionID: "sid", City: "/city", Template: "fixture-template", Epoch: 1, InstanceTokenSHA256: observation.TokenDigest("fixture-token"), Name: "gc", DeclaredRoot: true, StatRevalidated: true, PIDFDBound: true, UIDsRevalidated: true}
	child := root
	child.PID = 30
	child.PPID = 20
	child.PGID = 30
	child.StartTicks = "300"
	child.Name = "child"
	child.DeclaredRoot = false
	childStart := "300"
	c := procobserver.CensusV3{
		Schema: procobserver.CensusV3Schema, SelectedClosings: [2]int{2, 3}, Seal: procobserver.CensusSeal{ScanIndex: 4, EnumeratedCount: 1, PIDDigest: digest, ClassifiedCount: 1, ClassifiedDigest: digest}, ReconciledCount: 1, ReconciledDigest: digest, PeakFD: 32, RetainedBytes: 4096,
		Scans: []procobserver.CensusScanV3{
			{CensusClosing: procobserver.CensusClosing{ScanIndex: 1, EnumeratedCount: 2, EnumerationDigest: digest, LiveCount: 2, LiveDigest: strings.Repeat("e", 64)}, Kind: "initial", Verified: []procobserver.CensusIdentity{root, child}},
			{CensusClosing: procobserver.CensusClosing{ScanIndex: 2, EnumeratedCount: 2, EnumerationDigest: digest, LiveCount: 2, LiveDigest: strings.Repeat("e", 64)}, Round: 1, Kind: "closing", OffsetMS: 1, Verified: []procobserver.CensusIdentity{root, child}},
			{CensusClosing: procobserver.CensusClosing{ScanIndex: 3, EnumeratedCount: 2, EnumerationDigest: digest, LiveCount: 1, LiveDigest: digest}, Round: 1, Kind: "closing", OffsetMS: 2, Verified: []procobserver.CensusIdentity{root}},
			{CensusClosing: procobserver.CensusClosing{ScanIndex: 4, EnumeratedCount: 1, EnumerationDigest: digest, LiveCount: 1, LiveDigest: digest}, Round: 1, Kind: "seal", OffsetMS: 3, Verified: []procobserver.CensusIdentity{root}},
		},
		Proofs: []procobserver.CensusProofV3{
			{CensusProof: procobserver.CensusProof{Kind: "enumerated_pid_absent", Method: "pidfd_no_pid", PID: 30, ScanIndex: 3, OffsetMS: 2}},
			{CensusProof: procobserver.CensusProof{Kind: "incarnation_retired", Method: "pidfd_no_pid", PID: 30, StartTicks: &childStart, ScanIndex: 3, OffsetMS: 2, ProtectedIdentity: true}, DescendantCertificateID: 1},
		},
		Certificates: []procobserver.DescendantCertificate{{ID: 1, PriorScan: 2, Chain: []procobserver.CensusIdentity{child, root}, AbsenceProof: 1, RetirementProof: 2}},
		Resolutions:  []procobserver.CensusResolution{{ErrorIndex: 1, Kind: "kernel_absence", ProofIndex: 1}},
	}
	frames := []procobserver.ResponseV3{}
	for i := 0; i < 2; i++ {
		finish := now.Add(time.Duration(i-1) * 3 * time.Millisecond)
		frame := procobserver.ResponseV3{
			Schema: procobserver.ResponseSchemaV3, Scope: "host_procfs", RequestNonce: strings.Repeat([]string{"e", "f"}[i], 64), HelperSourceRevision: p.HelperSourceRevision, HelperBinarySHA256: p.HelperBinarySHA256, PolicyDigest: p.PolicyDigest, BootID: p.BootID, PIDNamespaceIdentity: p.PIDNamespaceIdentity, CallerBinding: p.CallerBinding,
			StartedAt: finish.Add(-3 * time.Millisecond), FinishedAt: finish, DurationMS: 3, Complete: true, EnumeratedCountBefore: 2, EnumeratedCountAfter: 2, EnumerationDigestBefore: digest, EnumerationDigestAfter: digest,
			Roots:  []procobserver.Root{{PID: root.PID, PPID: root.PPID, PGID: root.PGID, StartTicks: root.StartTicks, SessionID: root.SessionID, City: root.City, Template: root.Template, Epoch: root.Epoch, InstanceTokenSHA256: root.InstanceTokenSHA256, Name: root.Name}},
			Errors: []procobserver.EvidenceError{{Reason: "process_unavailable", PID: 30, Operation: "stat", Errno: 2, ScanIndex: 3}}, ErrorsTotal: 1, KernelRelease: "6.8.fixture", KernelProofProfile: procobserver.KernelProofProfile, Census: c, CertificateDisposition: "provisional",
		}
		data, _ := json.Marshal(frame)
		var owned procobserver.ResponseV3
		_ = json.Unmarshal(data, &owned)
		frames = append(frames, owned)
	}
	return p, frames, now
}

func controllerV3Provider(t *testing.T) *controllerRetryProvider {
	t.Helper()
	p := &controllerRetryProvider{Fake: runtime.NewFake(), name: "sid"}
	for key, value := range map[string]string{"GC_SESSION_ID": "sid", "GC_TEMPLATE": "fixture-template", "GC_RUNTIME_EPOCH": "1", "GC_INSTANCE_TOKEN": "fixture-token"} {
		if err := p.SetMeta("sid", key, value); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func TestControllerV3CollectionRetainsBothProvisionalFrames(t *testing.T) {
	for _, mode := range []string{"exact", "provider lost", "second root changed", "partial helper", "pins changed", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			p, frames, now := controllerV3Fixture()
			provider := controllerV3Provider(t)
			calls := 0
			if mode == "provider lost" {
				provider.trackingErr = fmt.Errorf("fixture owner unavailable")
			}
			if mode == "second root changed" {
				frames[1].Roots[0].StartTicks = "201"
			}
			if mode == "partial helper" {
				frames[1].Complete = false
			}
			if mode == "pins changed" {
				frames[1].HelperBinarySHA256 = strings.Repeat("0", 64)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "canceled" {
				cancel()
			}
			read := func(context.Context, procobserver.ReleasePolicyV3) (procobserver.ResponseV3, error) {
				r := frames[calls]
				calls++
				return r, nil
			}
			got := collectControllerObservationV3(ctx, "/city", p, provider, read, func() time.Time { return now })
			if (got.ProviderComplete && got.ProcessComplete) != (mode == "exact") {
				t.Fatalf("mode %s: %+v", mode, got)
			}
			if got.Schema != observation.SchemaV3 {
				t.Fatal("V3 downgraded")
			}
			if (got.CertificateDisposition == "provider_verified") != (mode == "exact") {
				t.Fatal("false authority")
			}
			if mode == "exact" || mode == "provider lost" || mode == "partial helper" {
				if calls != 2 || len(got.ProcessDiagnostics) != 2 {
					t.Fatalf("frames lost: %d %+v", calls, got)
				}
				for i, frame := range got.ProcessDiagnostics {
					if !reflect.DeepEqual(frame, frames[i]) || frame.CertificateDisposition != "provisional" {
						t.Fatal("original helper frame altered")
					}
				}
			}
			if mode == "exact" {
				if err := validateControllerObservationReplyV3(got, p, "/city", p.HelperSourceRevision, now); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestControllerV3ValidationCannotPromoteOrDropAFrame(t *testing.T) {
	for _, mode := range []string{"exact", "one frame", "replayed nonce", "overlapping frames", "v2 frame", "promoted helper", "source", "outer pins", "outer interval", "anchor missing process", "anchor missing second root", "false partial promotion", "missing session", "duplicate process", "duplicate session", "session changed"} {
		t.Run(mode, func(t *testing.T) {
			p, frames, now := controllerV3Fixture()
			calls := 0
			got := collectControllerObservationV3(context.Background(), "/city", p, controllerV3Provider(t), func(context.Context, procobserver.ReleasePolicyV3) (procobserver.ResponseV3, error) {
				r := frames[calls]
				calls++
				return r, nil
			}, func() time.Time { return now })
			switch mode {
			case "one frame":
				got.ProcessDiagnostics = got.ProcessDiagnostics[:1]
			case "replayed nonce":
				got.ProcessDiagnostics[1].RequestNonce = got.ProcessDiagnostics[0].RequestNonce
			case "overlapping frames":
				got.ProcessDiagnostics[1].StartedAt = got.ProcessDiagnostics[0].StartedAt
			case "v2 frame":
				got.ProcessDiagnostics[1].Schema = procobserver.Schema
			case "promoted helper":
				got.ProcessDiagnostics[1].CertificateDisposition = "provider_verified"
			case "source":
				got.SourceRevision = strings.Repeat("0", 40)
			case "outer pins":
				got.HelperPolicyDigest = strings.Repeat("0", 64)
			case "outer interval":
				got.FinishedAt = now.Add(time.Second)
			case "anchor missing process":
				got.Processes = []observation.Process{}
			case "anchor missing second root":
				got.ProcessDiagnostics[1].Roots = []procobserver.Root{}
			case "false partial promotion":
				got.ProcessDiagnostics[1].Complete = false
			case "missing session":
				got.Sessions = []observation.Session{}
			case "duplicate process":
				got.Processes = append(got.Processes, got.Processes[0])
			case "duplicate session":
				got.Sessions = append(got.Sessions, got.Sessions[0])
			case "session changed":
				got.Sessions[0].InstanceTokenSHA256 = strings.Repeat("0", 64)
			}
			if err := validateControllerObservationReplyV3(got, p, "/city", p.HelperSourceRevision, now); (err == nil) != (mode == "exact") {
				t.Fatalf("mode %s: %v", mode, err)
			}
			if mode == "exact" {
				data, _ := json.Marshal(got)
				var legacy controllerObservationReply
				if procobserver.DecodeStrict(data, &legacy) == nil {
					t.Fatal("legacy relay accepted V3")
				}
			}
		})
	}
}

func TestControllerV3RelayPreservesBytesAndPeerFence(t *testing.T) {
	for _, mode := range []string{"exact", "bad JSON", "extra JSON", "peer before", "peer after", "request failure", "partial"} {
		t.Run(mode, func(t *testing.T) {
			p, frames, now := controllerV3Fixture()
			calls := 0
			r := collectControllerObservationV3(context.Background(), "/city", p, controllerV3Provider(t), func(context.Context, procobserver.ReleasePolicyV3) (procobserver.ResponseV3, error) {
				f := frames[calls]
				calls++
				return f, nil
			}, func() time.Time { return now })
			if mode == "partial" {
				r.ProviderComplete = false
				r.CertificateDisposition = "provisional"
				r.UnknownReasons = []string{"fixture provider unavailable"}
				r.ControllerBinding = "not_observed"
			}
			data, _ := json.MarshalIndent(r, "", " ")
			data = append(data, '\n')
			if mode == "bad JSON" {
				data = []byte("{}\n")
			}
			if mode == "extra JSON" {
				data = append(data, []byte("{}\n")...)
			}
			path := filepath.Join(t.TempDir(), "s")
			lis, e := net.Listen("unix", path)
			if e != nil {
				t.Fatal(e)
			}
			defer lis.Close() //nolint:errcheck // isolated relay fixture
			done := make(chan error, 1)
			go func() {
				c, e := lis.Accept()
				if e != nil {
					done <- e
					return
				}
				defer c.Close() //nolint:errcheck // isolated relay fixture
				_ = c.SetDeadline(time.Now().Add(testutil.GoroutineRaceTimeout))
				_, e = bufio.NewReader(c).ReadString('\n')
				if e != nil {
					done <- nil
					return
				}
				_, e = c.Write(data)
				done <- e
			}()
			checks := 0
			verify := func(net.Conn, procobserver.CallerBinding) error {
				checks++
				if mode == "peer before" || mode == "peer after" && checks == 2 {
					return fmt.Errorf("fixture mismatch")
				}
				return nil
			}
			request := func(c net.Conn) error {
				if mode == "request failure" {
					return fmt.Errorf("fixture request failed")
				}
				_, err := io.WriteString(c, "test-only request\n")
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), testutil.GoroutineRaceTimeout)
			defer cancel()
			var out bytes.Buffer
			err := relayControllerObservationV3At(ctx, "/city", path, p.HelperSourceRevision, &out, func() (procobserver.ReleasePolicyV3, error) { return p, nil }, verify, request, func() time.Time { return now })
			if (err == nil) != (mode == "exact") {
				t.Fatalf("mode %s: %v", mode, err)
			}
			if mode == "exact" && (!bytes.Equal(out.Bytes(), data) || checks != 2) {
				t.Fatal("relay refreshed, trimmed or failed to fence evidence")
			}
			if mode == "partial" && (!bytes.Equal(out.Bytes(), data) || checks != 2) {
				t.Fatal("partial raw frames altered")
			}
			if mode != "exact" && mode != "partial" {
				var unknown controllerObservationReplyV3
				if procobserver.DecodeStrict(out.Bytes(), &unknown) != nil || unknown.ProcessComplete || unknown.ProviderComplete || unknown.CertificateDisposition != "provisional" {
					t.Fatal("invalid relay reply promoted")
				}
			}
			select {
			case e := <-done:
				if e != nil {
					t.Fatal(e)
				}
			case <-ctx.Done():
				t.Fatal("fixture relay server did not return")
			}
		})
	}
}
