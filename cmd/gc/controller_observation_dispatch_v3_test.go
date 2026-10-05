package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"reflect"
	"testing"

	"github.com/gastownhall/gascity/internal/runtime/procobserver"
	"github.com/gastownhall/gascity/internal/testutil"
)

func controllerV3RouteFixture(t *testing.T, city string) (*controllerObservationServiceV3, procobserver.ReleasePolicyV3, []procobserver.ResponseV3) {
	t.Helper()
	s, cs, p, frames := controllerV3ServiceFixture(t)
	s.city, cs.cityPath = city, city
	for i := range frames {
		for j := range frames[i].Roots {
			frames[i].Roots[j].City = city
		}
		for j := range frames[i].Census.Scans {
			for k := range frames[i].Census.Scans[j].Verified {
				frames[i].Census.Scans[j].Verified[k].City = city
			}
		}
		for j := range frames[i].Census.Certificates {
			for k := range frames[i].Census.Certificates[j].Chain {
				frames[i].Census.Certificates[j].Chain[k].City = city
			}
		}
	}
	return s, p, frames
}

func assertControllerV3SupportedFullRPCRoute(t *testing.T, city string, setObserver func(func(context.Context) controllerObservationReplyV3)) {
	t.Helper()
	for _, mode := range []string{"complete", "partial helper", "provider lost", "v2 policy"} {
		t.Run(mode, func(t *testing.T) {
			s, p, frames := controllerV3RouteFixture(t, city)
			reads := 0
			read := s.readEvidence
			s.readEvidence = func(ctx context.Context, p procobserver.ReleasePolicyV3) (procobserver.ResponseV3, error) {
				reads++
				return read(ctx, p)
			}
			if mode == "partial helper" {
				frames[1].Complete = false
			}
			if mode == "provider lost" {
				s.state.sp.(*controllerRetryProvider).trackingErr = fmt.Errorf("fixture provider root lost")
			}
			if mode == "v2 policy" {
				bad := p
				bad.EvidenceSchema = procobserver.Schema
				s.loadPolicy = func() (procobserver.ReleasePolicyV3, error) { return bad, nil }
			}
			setObserver(s.observe)
			ctx, cancel := context.WithTimeout(context.Background(), testutil.GoroutineRaceTimeout)
			defer cancel()
			var out bytes.Buffer
			err := relayControllerObservationV3At(ctx, city, controllerSocketPath(city), p.HelperSourceRevision, &out,
				func() (procobserver.ReleasePolicyV3, error) { return p, nil },
				func(net.Conn, procobserver.CallerBinding) error { return nil },
				writeControllerObservationRequestV3, s.now)
			if (err == nil) != (mode == "complete") {
				t.Fatalf("mode %s route outcome: %v", mode, err)
			}
			var got controllerObservationReplyV3
			if err := procobserver.DecodeStrict(out.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if mode == "complete" || mode == "partial helper" {
				if reads != 2 || !reflect.DeepEqual(got.ProcessDiagnostics, frames) {
					t.Fatal("FULL route changed either original provisional helper frame")
				}
			}
			for _, frame := range got.ProcessDiagnostics {
				if frame.CertificateDisposition != "provisional" {
					t.Fatal("route promoted inner certificate")
				}
			}
			if mode == "complete" && got.CertificateDisposition != "provider_verified" {
				t.Fatal("ordinary BOTH provider join did not establish outer authority")
			}
			if mode != "complete" && ((got.ProviderComplete && got.ProcessComplete) || got.CertificateDisposition != "provisional") {
				t.Fatal("UNKNOWN route promoted coverage")
			}
			controllerV3ServiceIdle(t)
		})
	}
}

func TestControllerV3SupportedRouteRejectsExtraRequest(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close() //nolint:errcheck // owned fixture
	done := make(chan struct{})
	called := false
	go func() {
		handleControllerConn(server, "/city", controllerHostingStandalone, func() {}, nil, nil, nil, nil, nil, nil,
			controllerSocketOptions{observeV3: func(context.Context) controllerObservationReplyV3 {
				called = true
				return controllerObservationReplyV3{}
			}})
		close(done)
	}()
	if _, err := io.WriteString(client, controllerObservationCommand+"\nextra\n"); err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(client)
	if err != nil {
		t.Fatal(err)
	}
	waitControllerV3Service(t, done)
	var got controllerObservationReplyV3
	if procobserver.DecodeStrict(raw, &got) != nil || called || got.ProcessComplete || got.CertificateDisposition != "provisional" {
		t.Fatal("extra request reached service or was not typed UNKNOWN")
	}
}

// Legacy services remain covered by isolated unit fixtures, without registering
// a V2 fallback on the production controller socket.

// A legacy-only option cannot supply the production V3 dispatcher.
func TestControllerV3LegacyOnlyOptionNeverFallsBack(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close() //nolint:errcheck // owned fixture
	done := make(chan struct{})
	legacyCalls := 0
	go func() {
		handleControllerConn(server, "/city", controllerHostingStandalone, func() {}, nil, nil, nil, nil, nil, nil,
			controllerSocketOptions{observe: func(context.Context) controllerObservationReply {
				legacyCalls++
				return controllerObservationReply{}
			}})
		close(done)
	}()
	if _, err := io.WriteString(client, controllerObservationCommand+"\n"); err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(client)
	if err != nil {
		t.Fatal(err)
	}
	waitControllerV3Service(t, done)
	var got controllerObservationReplyV3
	if procobserver.DecodeStrict(raw, &got) != nil || legacyCalls != 0 || got.ProviderComplete || got.ProcessComplete || got.CertificateDisposition != "provisional" {
		t.Fatal("legacy option promoted V3 or invoked V2 fallback")
	}
}
