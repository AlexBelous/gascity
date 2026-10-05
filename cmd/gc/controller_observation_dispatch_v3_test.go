package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

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

func TestControllerV3SupportedFullRPCRoute(t *testing.T) {
	for _, mode := range []string{"complete", "partial helper", "provider lost", "v2 policy", "legacy-only option"} {
		t.Run(mode, func(t *testing.T) {
			city := t.TempDir()
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
			legacyCalls := 0
			option := controllerSocketOptions{observeV3: s.observe, observe: func(context.Context) controllerObservationReply {
				legacyCalls++
				return controllerObservationReply{}
			}}
			if mode == "legacy-only option" {
				option.observeV3 = nil
			}
			lis, err := startControllerSocket(city, controllerHostingStandalone, func() {}, nil, nil, nil, nil, nil, nil, option)
			if err != nil {
				t.Fatal(err)
			}
			defer lis.Close() //nolint:errcheck // owned fixture
			ctx, cancel := context.WithTimeout(context.Background(), testutil.GoroutineRaceTimeout)
			defer cancel()
			var out bytes.Buffer
			err = relayControllerObservationV3At(ctx, city, controllerSocketPath(city), p.HelperSourceRevision, &out,
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
			if legacyCalls != 0 {
				t.Fatal("supported FULL route fell back to V2")
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
func startLegacyObservationTestSocket(city string, options ...controllerSocketOptions) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(controllerSocketPath(city)), 0o700); err != nil {
		return nil, err
	}
	lis, err := net.Listen("unix", controllerSocketPath(city))
	if err != nil {
		return nil, err
	}
	go func() {
		for {
			c, err := lis.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close() //nolint:errcheck // owned fixture
				_ = c.SetDeadline(time.Now().Add(testutil.GoroutineRaceTimeout))
				reader := bufio.NewReader(c)
				line, err := readControllerCommandLine(reader)
				if err == nil && string(line) == controllerObservationCommand {
					handleControllerObservation(c, reader, city, options)
				}
			}()
		}
	}()
	return lis, nil
}
