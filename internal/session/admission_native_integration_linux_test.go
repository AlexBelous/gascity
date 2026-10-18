//go:build linux

package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/runtime/tmux"
)

// This opt-in operational test uses one isolated tmux socket, a temporary
// MemStore/city and an owned sleep process. It never opens a real city DB,
// calls an LLM, touches the shared tmux server or runs the global observer.
func TestOwnedNativeScopeLinuxIntegration(t *testing.T) {
	if os.Getenv("GC_NATIVE_SCOPE_INTEGRATION") != "1" {
		t.Skip("requires explicit owned systemd-user scope integration")
	}
	city, c, _, _, store := nativeTestFixture(t, 0)
	host := newNativeCapacityHost()
	identity, err := host.Identity()
	if err != nil {
		t.Fatal(err)
	}
	ledger := nativeCapacityLedger{Schema: nativeCapacitySchema, CityPath: city, Identity: identity, Seats: map[string]nativeCapacitySeat{}}
	data, err := json.Marshal(ledger)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nativeCapacityPath(city), data, 0o600); err != nil {
		t.Fatal(err)
	}
	config := tmux.DefaultConfig()
	config.SocketName = fmt.Sprintf("gc142-owned-native-%d-%d", os.Getpid(), time.Now().UnixNano())
	sp := tmux.NewSeamBackedWithConfig(config)
	defer func() {
		if lifecycle, ok := sp.(runtime.ServerLifecycleProvider); ok {
			_ = lifecycle.TeardownServer()
		}
	}()
	mgr := NewManagerWithOptions(store, sp, WithCityPath(city))
	// The model stand-in exits after one second; its setsid child outlives
	// both the main command and tmux pane but remains inside the owned cgroup.
	info, err := mgr.CreateSession(context.Background(), CreateOptions{Template: "worker", Command: "sh -c '/usr/bin/setsid /usr/bin/sleep 4 >/dev/null 2>&1 </dev/null & /usr/bin/sleep 1'", WorkDir: city, Provider: "owned-sleep-test"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sp.Stop(info.SessionName) }()
	deadline := time.Now().Add(2 * time.Second)
	var seen nativeCapacitySeat
	for time.Now().Before(deadline) {
		if err := withNativeCapacityLedger(context.Background(), city, c, host, func(l *nativeCapacityLedger) error {
			refreshNativeSeats(l, host, time.Now())
			seen = l.Seats[info.ID]
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if seen.State == "active" && seen.CgroupPath != "" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if seen.State != "active" || !seen.LaunchFinalized || seen.CgroupPath == "" {
		t.Fatalf("actual scope never witnessed: state=%s finalized=%v", seen.State, seen.LaunchFinalized)
	}
	if observed, err := host.Observe(seen); err != nil || !observed.Populated {
		t.Fatalf("actual owned model not contained: populated=%v err=%v", observed.Populated, err)
	}
	deadline = time.Now().Add(2500 * time.Millisecond)
	for sp.IsRunning(info.SessionName) && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	if sp.IsRunning(info.SessionName) {
		t.Fatal("owned model stand-in did not exit on time")
	}
	if observed, err := host.Observe(seen); err != nil || !observed.Populated {
		t.Fatalf("detached child disappeared from owned accounting: populated=%v err=%v", observed.Populated, err)
	}
	// A lost provider row is not the release proof. Wait for the actual exact
	// cgroup to empty/remove; its durable seat is retired only by that proof.
	deadline = time.Now().Add(7 * time.Second)
	retired := false
	for time.Now().Before(deadline) {
		if err := withNativeCapacityLedger(context.Background(), city, c, host, func(l *nativeCapacityLedger) error {
			refreshNativeSeats(l, host, time.Now())
			_, present := l.Seats[info.ID]
			retired = !present
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if retired {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !retired {
		t.Fatal("completed actual model scope did not release seat")
	}
	final := readNativeTestLedger(t, city)
	if len(final.Seats) != 0 || len(final.Retired) != 1 || final.Retired[0].SessionID != info.ID {
		t.Fatal("actual scope retirement missing")
	}
	// Prove the fixture did not bind to an external store path.
	if filepath.Dir(nativeCapacityPath(city)) != filepath.Join(city, ".gc/runtime/pool-capacity-admission") {
		t.Fatal("unexpected runtime path")
	}
	if _, ok := any(store).(*beads.MemStore); !ok {
		t.Fatal("non-memory store")
	}
}
