//go:build integration

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/runtime/observation"
	"github.com/gastownhall/gascity/internal/runtime/procobserver"
)

func TestControllerObservationSocketRelay(t *testing.T) {
	for _, mode := range []string{"exact", "partial", "wrong source", "wrong city", "missing scalar", "null scalar", "duplicate scalar", "extra frame", "oversized", "unsupported"} {
		t.Run(mode, func(t *testing.T) {
			city := t.TempDir()
			if err := os.MkdirAll(filepath.Join(city, ".gc"), 0o700); err != nil {
				t.Fatal(err)
			}
			lis, err := net.Listen("unix", controllerSocketPath(city))
			if err != nil {
				t.Fatal(err)
			}
			defer lis.Close() //nolint:errcheck // isolated fixture cleanup
			r := controllerObservationFixture(city)
			switch mode {
			case "partial":
				r.ProcessComplete = false
				r.UnknownReasons = []string{"fixture permission"}
			case "wrong source":
				r.SourceRevision = strings.Repeat("f", 40)
			case "wrong city":
				r.CityPath = "/other"
			}
			raw, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "missing scalar":
				raw = bytes.Replace(raw, []byte(`"provider_complete":true,`), nil, 1)
			case "null scalar":
				raw = bytes.Replace(raw, []byte(`"provider_complete":true`), []byte(`"provider_complete":null`), 1)
			case "duplicate scalar":
				raw = bytes.Replace(raw, []byte(`"provider_complete":true`), []byte(`"provider_complete":true,"provider_complete":true`), 1)
			case "extra frame":
				raw = append(raw, []byte("\n{}")...)
			case "oversized":
				raw = []byte(strings.Repeat(" ", controllerObservationLimit+1))
			case "unsupported":
				raw = []byte("unsupported\n")
			}
			raw = append(raw, '\n')
			done := make(chan error, 1)
			go func() {
				conn, e := lis.Accept()
				if e != nil {
					done <- e
					return
				}
				defer conn.Close() //nolint:errcheck // isolated fixture cleanup
				line, e := bufio.NewReader(conn).ReadString('\n')
				if e != nil || line != controllerObservationCommand+"\n" {
					done <- fmt.Errorf("wrong fixed request %q: %w", line, e)
					return
				}
				_, e = conn.Write(raw)
				if mode == "oversized" {
					e = nil
				}
				done <- e
			}()
			var out bytes.Buffer
			err = fixtureControllerRelay(context.Background(), city, strings.Repeat("a", 40), &out)
			if mode == "exact" {
				if err != nil || !bytes.Equal(out.Bytes(), raw) {
					t.Fatalf("provenance/bytes rewritten err=%v", err)
				}
			} else if err == nil {
				t.Fatal("invalid/incomplete became complete")
			}
			if mode == "partial" && !bytes.Equal(out.Bytes(), raw) {
				t.Fatal("partial daemon evidence rewritten")
			}
			if e := <-done; e != nil {
				t.Fatal(e)
			}
		})
	}
}

func TestControllerObservationForgedJSONCannotReplacePeerProof(t *testing.T) {
	city := t.TempDir()
	if err := os.MkdirAll(filepath.Join(city, ".gc"), 0o700); err != nil {
		t.Fatal(err)
	}
	lis, err := net.Listen("unix", controllerSocketPath(city))
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close() //nolint:errcheck // isolated fixture cleanup
	attempted := make(chan bool, 1)
	go func() {
		conn, e := lis.Accept()
		if e != nil {
			attempted <- false
			return
		}
		defer conn.Close() //nolint:errcheck // isolated fixture cleanup
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		var buf [1]byte
		n, _ := conn.Read(buf[:])
		attempted <- n > 0
	}()
	var out bytes.Buffer
	err = relayControllerObservationAuthenticated(context.Background(), city, strings.Repeat("a", 40), &out, func() (procobserver.Policy, error) { return procobserver.Policy{}, nil }, func(net.Conn, procobserver.CallerBinding) error { return fmt.Errorf("kernel PID mismatch") }, time.Now)
	if err == nil || <-attempted {
		t.Fatal("unverified listener received observation request")
	}
	var r controllerObservationReply
	if procobserver.DecodeStrict(out.Bytes(), &r) != nil || r.ProcessComplete || r.ControllerBinding == "verified_local_process" {
		t.Fatal("forged peer became source")
	}
}

// Couples exact serialized helper-codec/domain output to the actual bounded
// controller relay. The fixture peer is injected; Linux peer proof is separate.

func TestControllerObservationSerializedConsumerRelay(t *testing.T) {
	path := os.Getenv("TEST_OBSERVER_SERIALIZED_HELPER_LOG")
	if path == "" {
		t.Skip("dedicated producer/relay/consumer receipt supplies the exact producer log")
	}
	input, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, line := range strings.Split(string(input), "\n") {
		const marker = "NATIVE_HELPER_CONSUMER_FIXTURE="
		index := strings.Index(line, marker)
		if index < 0 {
			continue
		}
		var fixture struct {
			Name        string                  `json:"name"`
			Observation observation.Observation `json:"observation"`
		}
		if err = json.Unmarshal([]byte(line[index+len(marker):]), &fixture); err != nil {
			t.Fatal(err)
		}
		t.Run(fixture.Name, func(t *testing.T) {
			count++
			r := controllerObservationFixture("/city")
			r.Observation = fixture.Observation
			r.ControllerPID = os.Getpid()
			if !r.ProcessComplete || !r.ProviderComplete {
				r.ControllerBinding = "not_observed"
			}
			raw, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			raw = append(raw, '\n')
			city := t.TempDir()
			if err = os.MkdirAll(filepath.Join(city, ".gc"), 0o700); err != nil {
				t.Fatal(err)
			}
			// The fixture socket location is independent of the source city whose
			// immutable identity is checked in the reply. No actual city is opened.
			lis, err := net.Listen("unix", controllerSocketPath(city))
			if err != nil {
				t.Fatal(err)
			}
			defer lis.Close() //nolint:errcheck
			done := make(chan error, 1)
			go func() {
				conn, e := lis.Accept()
				if e != nil {
					done <- e
					return
				}
				defer conn.Close() //nolint:errcheck
				_, e = bufio.NewReader(conn).ReadString('\n')
				if e == nil {
					_, e = conn.Write(raw)
				}
				done <- e
			}()
			// Dial the temporary socket while retaining the producer city in the
			// canonical relay validation through a temporary path-only seam.
			var out bytes.Buffer
			policy := procobserver.Policy{CallerBinding: procobserver.CallerBinding{PID: r.ControllerPID, StartTicks: r.ControllerStartIdentity, ControllerBinarySHA256: r.ControllerBinarySHA256, BootID: r.ControllerBootID}, HelperBinarySHA256: r.HelperBinarySHA256, PolicyDigest: r.HelperPolicyDigest}
			err = relayControllerObservationAt(context.Background(), "/city", controllerSocketPath(city), r.SourceRevision, &out, func() (procobserver.Policy, error) { return policy, nil }, func(net.Conn, procobserver.CallerBinding) error { return nil }, func() time.Time { return time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC) })
			if e := <-done; e != nil {
				t.Fatal(e)
			}
			complete := fixture.Name == "complete"
			validInterval := fixture.Name != "stale" && fixture.Name != "wrong-helper-pin"
			if (err == nil) != complete || (validInterval && !bytes.Equal(out.Bytes(), raw)) {
				t.Fatalf("relay changed producer bytes/provenance: err=%v complete=%v", err, complete)
			}
			encoded, e := json.Marshal(struct {
				Name        string          `json:"name"`
				Observation json.RawMessage `json:"observation"`
			}{fixture.Name, json.RawMessage(out.Bytes())})
			if e != nil {
				t.Fatal(e)
			}
			t.Logf("NATIVE_CONTROLLER_CONSUMER_FIXTURE=%s", encoded)
		})
	}
	if count != 9 {
		t.Fatalf("expected9 producer cases, got%d", count)
	}
}
