package session

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/clock"
	"github.com/gastownhall/gascity/internal/runtime"
)

func admissionTestCity(t *testing.T) string {
	t.Helper()
	city := t.TempDir()
	if err := os.MkdirAll(filepath.Join(city, "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(city, "config/capacity-scheduler-v2.toml"), []byte("[queue]\nmanaged_exact_routes=[\"worker\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return city
}

func TestManagedStartAdmissionCoversNormalAndRuntimeResume(t *testing.T) {
	for _, runtimeOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "runtime-only"}[runtimeOnly], func(t *testing.T) {
			mgr, sp, store, id, name := seedResumableACPSession(t, func(string) error { return nil })
			mgr.cityPath = admissionTestCity(t)
			if err := store.SetMetadataBatch(id, map[string]string{"template": "worker", "continuation_reset_pending": "true", "continuation_epoch": "7"}); err != nil {
				t.Fatal(err)
			}
			var err error
			if runtimeOnly {
				err = mgr.StartRuntimeOnly(context.Background(), id, capacityTestResumeCmd, runtime.Config{WorkDir: "/tmp"})
			} else {
				err = mgr.Start(context.Background(), id, capacityTestResumeCmd, runtime.Config{WorkDir: "/tmp"})
			}
			if err == nil || !runtime.IsProviderCapacity(err) {
				t.Fatalf("managed resume without evidence: %v", err)
			}
			if got := startCallCount(sp); got != 0 {
				t.Fatalf("provider started %d times without complete census", got)
			}
			b, e := store.Get(id)
			if e != nil {
				t.Fatal(e)
			}
			if b.Metadata["session_key"] != capacityTestSessionKey || b.Metadata["started_config_hash"] != capacityTestConfigHash || b.Metadata[PrimedAtMetadataKey] != capacityTestPrimedAt || b.Metadata["continuation_reset_pending"] != "true" || b.Metadata["continuation_epoch"] != "7" {
				t.Fatalf("refusal changed resume identity: %v", b.Metadata)
			}
			found := false
			for _, n := range sp.unrouted {
				if n == name {
					found = true
				}
			}
			if !found {
				t.Fatal("ACP route was not released")
			}
		})
	}
}

func TestManagedStartAdmissionCoversDirectCreate(t *testing.T) {
	store := beads.NewMemStore()
	sp := runtime.NewFake()
	mgr := NewManagerWithOptions(store, sp, WithCityPath(admissionTestCity(t)))
	_, err := mgr.CreateSession(context.Background(), CreateOptions{Template: "worker", Command: "fake-command", WorkDir: t.TempDir(), Provider: "fake"})
	if err == nil || !runtime.IsProviderCapacity(err) {
		t.Fatalf("managed create without evidence: %v", err)
	}
	for _, c := range sp.Calls {
		if c.Method == "Start" {
			t.Fatal("create bypassed admission")
		}
	}
}

// A complete reservation must admit one actual provider start and retain its
// concrete SID claim for every canonical manager entry point.
func TestManagedStartAdmissionSpendsGrantAtActualBoundary(t *testing.T) {
	for _, mode := range []string{"create", "normal", "runtime-only"} {
		t.Run(mode, func(t *testing.T) {
			city := admissionTestCity(t)
			now := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
			ledgerPath := writeAdmissionGrant(t, city, now)
			store := beads.NewMemStore()
			sp := runtime.NewFake()
			mgr := NewManagerWithOptions(store, sp, WithCityPath(city), WithClock(&clock.Fake{Time: now}))
			var id string
			if mode == "create" {
				info, err := mgr.CreateSession(context.Background(), CreateOptions{Template: "worker", Command: "fake-command", WorkDir: t.TempDir(), Provider: "fake"})
				if err != nil {
					t.Fatal(err)
				}
				id = info.ID
			} else {
				b, err := store.Create(beads.Bead{Type: BeadType, Labels: []string{LabelSession}, Metadata: map[string]string{"state": string(StateSuspended), "template": "worker", "provider": "fake", "work_dir": t.TempDir(), "command": "fake-command"}})
				if err != nil {
					t.Fatal(err)
				}
				id = b.ID
				if mode == "normal" {
					err = mgr.Start(context.Background(), id, "fake-command", runtime.Config{})
				} else {
					err = mgr.StartRuntimeOnly(context.Background(), id, "fake-command", runtime.Config{})
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			starts := 0
			for _, c := range sp.Calls {
				if c.Method == "Start" {
					starts++
				}
			}
			if starts != 1 {
				t.Fatalf("provider starts=%d", starts)
			}
			data, err := os.ReadFile(ledgerPath)
			if err != nil {
				t.Fatal(err)
			}
			var claimed struct {
				Reservations map[string]struct {
					StartClaims map[string]string `json:"start_claims"`
				} `json:"reservations"`
			}
			if err = json.Unmarshal(data, &claimed); err != nil {
				t.Fatal(err)
			}
			if len(claimed.Reservations["worker"].StartClaims) != 1 || claimed.Reservations["worker"].StartClaims[id] != now.Format(time.RFC3339) {
				t.Fatalf("missing actual SID grant: %s", data)
			}
		})
	}
}

func TestManagedAdmissionRefusalPrecedesOrphanCleanup(t *testing.T) {
	for _, mode := range []string{"create", "normal", "runtime-only"} {
		t.Run(mode, func(t *testing.T) {
			store := beads.NewMemStore()
			sp := &orphanScanProvider{Fake: runtime.NewFake(), results: []runtime.LiveRuntime{{PID: 4321, City: admissionTestCity(t)}}}
			mgr := NewManagerWithOptions(store, sp, WithCityPath(admissionTestCity(t)))
			var err error
			if mode == "create" {
				_, err = mgr.CreateSession(context.Background(), CreateOptions{Template: "worker", Command: "fake-command", WorkDir: t.TempDir(), Provider: "fake"})
			} else {
				b, e := store.Create(beads.Bead{Type: BeadType, Labels: []string{LabelSession}, Metadata: map[string]string{"state": string(StateSuspended), "template": "worker", "provider": "fake", "work_dir": t.TempDir(), "command": "fake-command", "continuation_reset_pending": "true", "continuation_epoch": "7", "session_key": "prior-key", "started_config_hash": "prior-hash"}})
				if e != nil {
					t.Fatal(e)
				}
				if mode == "normal" {
					err = mgr.Start(context.Background(), b.ID, "fake-command", runtime.Config{})
				} else {
					err = mgr.StartRuntimeOnly(context.Background(), b.ID, "fake-command", runtime.Config{})
				}
				after, e := store.Get(b.ID)
				if e != nil {
					t.Fatal(e)
				}
				for _, key := range []string{"continuation_reset_pending", "continuation_epoch", "session_key", "started_config_hash"} {
					if after.Metadata[key] != b.Metadata[key] {
						t.Fatalf("denial mutated %s: %v", key, after.Metadata)
					}
				}
			}
			if !runtime.IsProviderCapacity(err) {
				t.Fatalf("denial=%v", err)
			}
			if len(sp.events) != 0 {
				t.Fatalf("denied start touched orphan/provider lifecycle: %v", sp.events)
			}
		})
	}
}

func writeAdmissionGrant(t *testing.T, city string, now time.Time) string {
	t.Helper()
	capacity := map[string]any{"managed_active_count": 0, "managed_worker_cap": 2, "over_cap_by": 0, "active": []any{}}
	snapshot := map[string]any{"schema_version": 5, "generated_at": now.Format(time.RFC3339), "zone": "green", "hysteresis": map[string]any{"green_streak": 3, "cooldown_until": now.Add(-time.Hour).Format(time.RFC3339)}, "capacity": capacity, "live_census": map[string]any{"contract": "managed-live-census/v1", "complete": true, "count": 0, "observed_at": now.Format(time.RFC3339), "evidence_at": now.Format(time.RFC3339), "active": []any{}}}
	ledger := map[string]any{"schema": 1, "reservations": map[string]any{"worker": map[string]any{"slots": 1, "active_at_reservation": 0, "created_at": now.Format(time.RFC3339)}}}
	ledgerPath := filepath.Join(city, ".gc/runtime/pool-capacity-admission/reservations.json")
	for path, value := range map[string]any{filepath.Join(city, ".gc/runtime/capacity-scheduler-v2/snapshot.json"): snapshot, ledgerPath: ledger} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	return ledgerPath
}

func TestCapacityAdmissionProofIsBoundedAndOneShot(t *testing.T) {
	for _, mode := range []string{"concurrent-duplicate", "different-SID", "expired-evidence"} {
		t.Run(mode, func(t *testing.T) {
			city := admissionTestCity(t)
			now := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
			ledgerPath := writeAdmissionGrant(t, city, now)
			store := beads.NewMemStore()
			sp := runtime.NewFake()
			clk := &clock.Fake{Time: now}
			mgr := NewManagerWithOptions(store, sp, WithCityPath(city), WithClock(clk))
			ctx, allowed, reason := AdmitCapacityStart(context.Background(), city, "worker", "SID-a", now, sp, store)
			if !allowed {
				t.Fatalf("initial admission denied: %s", reason)
			}
			before, err := os.ReadFile(ledgerPath)
			if err != nil {
				t.Fatal(err)
			}
			cfg := runtime.Config{Command: "fake-command", Env: map[string]string{"GC_SESSION_ID": "SID-a", "GC_TEMPLATE": "worker"}}
			startsWant := 0
			if mode == "concurrent-duplicate" {
				var wg sync.WaitGroup
				results := make(chan error, 2)
				for i := 0; i < 2; i++ {
					wg.Add(1)
					go func() { defer wg.Done(); results <- mgr.startRuntime(ctx, "runtime-a", cfg) }()
				}
				wg.Wait()
				close(results)
				admitted := 0
				denied := 0
				for e := range results {
					switch {
					case e == nil:
						admitted++
					case runtime.IsProviderCapacity(e):
						denied++
					default:
						t.Fatal(e)
					}
				}
				if admitted != 1 || denied != 1 {
					t.Fatalf("same proof admitted=%d denied=%d", admitted, denied)
				}
				startsWant = 1
			} else {
				if mode == "different-SID" {
					cfg.Env["GC_SESSION_ID"] = "SID-b"
				} else {
					clk.Time = now.Add(61 * time.Second)
				}
				if e := mgr.startRuntime(ctx, "runtime-a", cfg); !runtime.IsProviderCapacity(e) {
					t.Fatalf("invalid proof allowed: %v", e)
				}
			}
			starts := 0
			for _, c := range sp.Calls {
				if c.Method == "Start" {
					starts++
				}
			}
			if starts != startsWant {
				t.Fatalf("provider starts=%d want%d", starts, startsWant)
			}
			after, err := os.ReadFile(ledgerPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatal("proof reuse rewrote or spent a second ledger claim")
			}
		})
	}
}
