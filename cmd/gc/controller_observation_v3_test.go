package main

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/runtime/observation"
	"github.com/gastownhall/gascity/internal/runtime/procobserver"
)

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
