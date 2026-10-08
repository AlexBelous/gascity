//go:build integration

package procobserver

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/runtime/observation"
)

func TestSerializedHelperConsumerObservations(t *testing.T) {
	data, err := os.ReadFile("../observation/testdata/consumer-input.json")
	if err != nil {
		t.Fatal(err)
	}
	var input struct {
		Templates []string `json:"templates"`
	}
	if err = json.Unmarshal(data, &input); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"complete", "strict-unknown", "tracking-partial", "missing-process", "alias-conflict", "wrong-run", "missing-tracker", "stale", "wrong-helper-pin"} {
		t.Run(mode, func(t *testing.T) {
			policy, response, _ := fixtureResponse()
			now := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
			response.StartedAt = now
			response.FinishedAt = now
			p := &joinedProvider{Fake: runtime.NewFake(), names: []string{"live-a", "live-b", "protected"}}
			for i, name := range p.names {
				sid := []string{"sid-a", "sid-b", "sid-p"}[i]
				token := "test-token-" + sid
				template := input.Templates[i]
				for key, value := range map[string]string{"GC_SESSION_ID": sid, "GC_TEMPLATE": template, "GC_RUNTIME_EPOCH": "2", "GC_INSTANCE_TOKEN": token} {
					if e := p.SetMeta(name, key, value); e != nil {
						t.Fatal(e)
					}
				}
				p.handles = append(p.handles, runtime.ProcessHandle{Name: name, SessionID: sid, PID: 100 + i})
				response.Roots = append(response.Roots, Root{PID: 100 + i, PPID: 1, PGID: 100 + i, StartTicks: "789", SessionID: sid, City: "/city", Template: template, Epoch: 2, InstanceTokenSHA256: observation.TokenDigest(token), Name: "agent"})
			}
			var provider runtime.Provider = p
			switch mode {
			case "strict-unknown":
				response.Complete = false
				response.ErrorsTotal = 1
				response.Errors = []EvidenceError{{Reason: "process_unavailable", Operation: "environ", PID: 10, Errno: 13}}
			case "tracking-partial":
				p.trackingErr = errors.New("owner PID unavailable")
			case "missing-process":
				response.Roots = response.Roots[1:]
				p.handles = p.handles[1:]
			case "alias-conflict":
				_ = p.SetMeta("live-b", "GC_SESSION_ID", "sid-a")
			case "wrong-run":
				response.Roots[0].InstanceTokenSHA256 = observation.TokenDigest("old-incarnation")
			case "missing-tracker":
				provider = struct{ runtime.Provider }{p}
			case "stale":
				response.StartedAt = now.Add(-61 * time.Second)
			case "wrong-helper-pin":
				response.HelperBinarySHA256 = observation.TokenDigest("unreviewed executable")
			}
			dir, err := os.MkdirTemp("/tmp", "gcjoin-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir) //nolint:errcheck
			address := &net.UnixAddr{Name: filepath.Join(dir, "socket"), Net: "unix"}
			lis, err := net.ListenUnix("unix", address)
			if err != nil {
				t.Fatal(err)
			}
			defer lis.Close() //nolint:errcheck
			done := make(chan error, 1)
			go func() {
				for i := 0; i < 2; i++ {
					c, e := lis.AcceptUnix()
					if e != nil {
						done <- e
						return
					}
					_ = c.SetDeadline(time.Now().Add(time.Second))
					raw, e := readFrame(c, MaxRequestBytes)
					if e != nil {
						_ = c.Close()
						done <- e
						return
					}
					var req Request
					e = json.Unmarshal(raw, &req)
					if e != nil {
						_ = c.Close()
						done <- e
						return
					}
					var extra [1]byte
					_, _ = c.Read(extra[:])
					response.RequestNonce = req.RequestNonce
					raw, e = json.Marshal(response)
					if e == nil {
						e = writeFrame(c, raw)
					}
					_ = c.Close()
					if e != nil {
						done <- e
						return
					}
				}
				done <- nil
			}()
			read := func() observation.ProcessEvidence {
				c, e := net.DialUnix("unix", nil, address)
				if e != nil {
					return observation.ProcessEvidence{Err: e}
				}
				defer c.Close() //nolint:errcheck
				_ = c.SetDeadline(time.Now().Add(time.Second))
				r, e := exchange(c, policy, func() time.Time { return now })
				return observation.ProcessEvidence{Roots: r.ObservedRoots(), StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, Err: e}
			}
			got := observation.ObserveProcessEvidence("/city", provider, read, func() time.Time { return now })
			if e := <-done; e != nil {
				t.Fatal(e)
			}
			if (got.ProviderComplete && got.ProcessComplete) != (mode == "complete") {
				t.Fatalf("wrong completeness %+v", got)
			}
			for _, call := range p.SnapshotCalls() {
				if call.Method != "SetMeta" && call.Method != "GetMeta" {
					t.Fatalf("lifecycle effect %s", call.Method)
				}
			}
			encoded, e := json.Marshal(struct {
				Name        string                  `json:"name"`
				Observation observation.Observation `json:"observation"`
			}{mode, got})
			if e != nil {
				t.Fatal(e)
			}
			t.Logf("NATIVE_HELPER_CONSUMER_FIXTURE=%s", encoded)
		})
	}
}
