package session

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/runtime"
)

func TestNativeSeatRetirementRequiresFinalizedLaunchAndKernelEmptiness(t *testing.T) {
	for _, tc := range []struct {
		name        string
		seat        nativeCapacitySeat
		observation nativeScopeObservation
		want        bool
	}{
		{"legacy remains occupied", nativeCapacitySeat{Kind: "legacy", LaunchFinalized: true}, nativeScopeObservation{Empty: true}, false},
		{"inflight remains occupied", nativeCapacitySeat{Kind: "scope", CgroupPath: "/owned", LaunchFinalized: false}, nativeScopeObservation{Empty: true}, false},
		{"never witnessed missing unit remains occupied", nativeCapacitySeat{Kind: "scope", LaunchFinalized: true}, nativeScopeObservation{Empty: true}, false},
		{"owned empty group retires", nativeCapacitySeat{Kind: "scope", CgroupPath: "/owned", LaunchFinalized: true}, nativeScopeObservation{Empty: true}, true},
		{"owned descendant remains occupied", nativeCapacitySeat{Kind: "scope", CgroupPath: "/owned", LaunchFinalized: true}, nativeScopeObservation{Populated: true}, false},
		{"unknown remains occupied", nativeCapacitySeat{Kind: "scope", CgroupPath: "/owned", LaunchFinalized: true}, nativeScopeObservation{}, false},
		{"contradictory observation refuses", nativeCapacitySeat{Kind: "scope", CgroupPath: "/owned", LaunchFinalized: true}, nativeScopeObservation{Populated: true, Empty: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := nativeSeatRetirable(tc.seat, tc.observation); got != tc.want {
				t.Fatalf("retire=%v want %v", got, tc.want)
			}
		})
	}
}

type nativeTestHost struct {
	mu                                   sync.Mutex
	resourcesErr, commandErr, observeErr error
	observations                         map[string]nativeScopeObservation
	defaultPopulated                     bool
}

func (h *nativeTestHost) Identity() (nativeKernelIdentity, error) {
	return nativeKernelIdentity{"boot", "pid:[11]", "cgroup:[12]", 1, 2}, nil
}

func (h *nativeTestHost) Resources(nativeCapacityConfig) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.resourcesErr
}

func (h *nativeTestHost) Command(unit string) (runtime.CommandScope, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return runtime.CommandScope{Launcher: "/usr/bin/systemd-run", Unit: unit}, h.commandErr
}

func (h *nativeTestHost) Observe(seat nativeCapacitySeat) (nativeScopeObservation, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.observeErr != nil {
		return nativeScopeObservation{}, h.observeErr
	}
	if observed, ok := h.observations[seat.Unit]; ok {
		return observed, nil
	}
	if h.defaultPopulated {
		return nativeScopeObservation{Populated: true, CgroupPath: "/user.slice/" + seat.Unit, Device: 1, Inode: 10}, nil
	}
	return nativeScopeObservation{}, nil
}

type nativeTestProvider struct {
	*runtime.Fake
	startErr      error
	started       []runtime.Config
	registryErr   error
	registryReads int
}

func (p *nativeTestProvider) ListRunning(prefix string) ([]string, error) {
	p.registryReads++
	if p.registryErr != nil {
		return nil, p.registryErr
	}
	return p.Fake.ListRunning(prefix)
}

func (p *nativeTestProvider) SupportsCommandScope(string) bool { return true }
func (p *nativeTestProvider) Start(ctx context.Context, name string, cfg runtime.Config) error {
	p.started = append(p.started, cfg)
	if p.startErr != nil {
		return p.startErr
	}
	return p.Fake.Start(ctx, name, cfg)
}

func nativeTestFixture(t *testing.T, legacy int) (string, nativeCapacityConfig, *nativeTestHost, *nativeTestProvider, *beads.MemStore) {
	t.Helper()
	city := t.TempDir()
	if err := os.MkdirAll(filepath.Join(city, "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := `[admission]
mode="native-managed-v1"
[queue]
managed_exact_routes=["worker"]
[limits]
max_managed_workers=3
[thresholds]
mem_green_gib=6
memory_psi_green=1
io_psi_green=10
`
	if err := os.WriteFile(filepath.Join(city, "config/capacity-scheduler-v2.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	c, enabled, err := readNativeCapacityConfig(city)
	if err != nil || !enabled {
		t.Fatalf("config enabled=%v err=%v", enabled, err)
	}
	host := &nativeTestHost{observations: map[string]nativeScopeObservation{}, defaultPopulated: true}
	identity, _ := host.Identity()
	ledger := nativeCapacityLedger{Schema: nativeCapacitySchema, CityPath: city, Identity: identity, Seats: map[string]nativeCapacitySeat{}}
	store := beads.NewMemStore()
	for i := 0; i < legacy; i++ {
		b := nativeTestSession(t, store)
		ledger.Seats[b.ID] = nativeCapacitySeat{Route: "worker", Handle: b.Metadata["session_name"], Kind: "legacy", Attempt: "cutover-proof", State: "legacy", LaunchFinalized: true, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	}
	if err := os.MkdirAll(filepath.Dir(nativeCapacityPath(city)), 0o700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(ledger)
	if err := os.WriteFile(nativeCapacityPath(city), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return city, c, host, &nativeTestProvider{Fake: runtime.NewFake()}, store
}

func nativeTestSession(t *testing.T, store *beads.MemStore) beads.Bead {
	t.Helper()
	b, err := store.Create(beads.Bead{Type: BeadType, Labels: []string{LabelSession}, Metadata: map[string]string{"template": "worker", "state": "suspended", "provider": "fake", "command": "fake-command", "work_dir": t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetMetadata(b.ID, "session_name", "s-"+b.ID); err != nil {
		t.Fatal(err)
	}
	b, err = store.Get(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func readNativeTestLedger(t *testing.T, city string) nativeCapacityLedger {
	t.Helper()
	data, err := os.ReadFile(nativeCapacityPath(city))
	if err != nil {
		t.Fatal(err)
	}
	var ledger nativeCapacityLedger
	if err := json.Unmarshal(data, &ledger); err != nil {
		t.Fatal(err)
	}
	return ledger
}

func TestNativeConcurrentLastSeatIsAtomic(t *testing.T) {
	city, c, host, sp, store := nativeTestFixture(t, 2)
	sids := []string{}
	for i := 0; i < 10; i++ {
		sids = append(sids, nativeTestSession(t, store).ID)
	}
	var wg sync.WaitGroup
	results := make(chan error, len(sids))
	for _, sid := range sids {
		wg.Add(1)
		go func(sid string) {
			defer wg.Done()
			_, err := claimNativeCapacityStart(context.Background(), city, "worker", sid, time.Now(), c, host, sp, store)
			results <- err
		}(sid)
	}
	wg.Wait()
	close(results)
	allowed := 0
	for err := range results {
		if err == nil {
			allowed++
		}
	}
	if allowed != 1 || len(readNativeTestLedger(t, city).Seats) != 3 {
		t.Fatalf("allowed=%d seats=%d", allowed, len(readNativeTestLedger(t, city).Seats))
	}
}

func TestNativeLedgerAdmissionDoesNotDependOnProviderRegistry(t *testing.T) {
	city, c, host, sp, store := nativeTestFixture(t, 2)
	// A first tmux start has no server to enumerate yet. That diagnostic
	// failure cannot override the authoritative durable seats or erase them.
	sp.registryErr = errors.New("isolated tmux server does not exist yet")
	b := nativeTestSession(t, store)
	ctx, allowed, reason := admitCapacityStartWithHost(context.Background(), city, "worker", b.ID, time.Now(), sp, store, host)
	if !allowed {
		t.Fatalf("registry diagnostic blocked the free durable seat: %s", reason)
	}
	mgr := NewManagerWithOptions(store, sp, WithCityPath(city))
	if err := mgr.startRuntime(ctx, b.Metadata["session_name"], runtime.Config{Command: "fake-command", Env: map[string]string{"GC_SESSION_ID": b.ID, "GC_TEMPLATE": "worker"}}); err != nil {
		t.Fatal(err)
	}
	seat := readNativeTestLedger(t, city).Seats[b.ID]
	if len(sp.started) != 1 || sp.started[0].CommandScope == nil || seat.State != "active" {
		t.Fatal("free durable seat did not produce one accounted scoped start")
	}
	other := nativeTestSession(t, store)
	if _, err := claimNativeCapacityStart(context.Background(), city, "worker", other.ID, time.Now(), c, host, sp, store); err == nil {
		t.Fatal("registry failure disabled the shared cap")
	}
	if sp.registryReads != 0 || len(readNativeTestLedger(t, city).Seats) != 3 {
		t.Fatalf("registry reads=%d; durable seats=%d", sp.registryReads, len(readNativeTestLedger(t, city).Seats))
	}
}

func TestNativeLedgerAdmissionStillValidatesTargetIdentity(t *testing.T) {
	for _, invalid := range []string{"missing", "closed", "wrong-template"} {
		t.Run(invalid, func(t *testing.T) {
			city, c, host, sp, store := nativeTestFixture(t, 2)
			b := nativeTestSession(t, store)
			var err error
			sid := b.ID
			switch invalid {
			case "missing":
				sid = "gc-missing-session"
			case "closed":
				err = store.Close(b.ID)
			case "wrong-template":
				err = store.SetMetadata(b.ID, "template", "another-route")
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := claimNativeCapacityStart(context.Background(), city, "worker", sid, time.Now(), c, host, sp, store); err == nil {
				t.Fatal("invalid target identity admitted")
			}
			if len(readNativeTestLedger(t, city).Seats) != 2 || len(sp.started) != 0 || sp.registryReads != 0 {
				t.Fatal("invalid target mutated admission or consulted global registry")
			}
		})
	}
}

func TestNativeProofStartsExactlyOnceWithoutSnapshotOrScanner(t *testing.T) {
	for _, mode := range []string{"boundary", "normal", "runtime-only"} {
		t.Run(mode, func(t *testing.T) {
			city, _, host, sp, store := nativeTestFixture(t, 2)
			b := nativeTestSession(t, store)
			ctx, allowed, reason := admitCapacityStartWithHost(context.Background(), city, "worker", b.ID, time.Now(), sp, store, host)
			if !allowed {
				t.Fatalf("admit: %s", reason)
			}
			mgr := NewManagerWithOptions(store, sp, WithCityPath(city))
			var err error
			switch mode {
			case "normal":
				err = mgr.Start(ctx, b.ID, "fake-command", runtime.Config{})
			case "runtime-only":
				err = mgr.StartRuntimeOnly(ctx, b.ID, "fake-command", runtime.Config{})
			default:
				err = mgr.startRuntime(ctx, b.Metadata["session_name"], runtime.Config{Command: "fake-command", Env: map[string]string{"GC_SESSION_ID": b.ID, "GC_TEMPLATE": "worker"}})
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(sp.started) != 1 || sp.started[0].Command != "fake-command" || sp.started[0].CommandScope == nil {
				t.Fatalf("not one scoped model start: %+v", sp.started)
			}
			seat := readNativeTestLedger(t, city).Seats[b.ID]
			if seat.State != "active" || !seat.LaunchFinalized || seat.CgroupPath == "" {
				t.Fatalf("seat not witnessed active: %+v", seat)
			}
			_, allowed, _ = admitCapacityStartWithHost(context.Background(), city, "worker", b.ID, time.Now(), sp, store, host)
			if allowed {
				t.Fatal("same SID admitted a second process")
			}
			if err := mgr.startRuntime(ctx, b.Metadata["session_name"], runtime.Config{Command: "fake-command", Env: map[string]string{"GC_SESSION_ID": b.ID, "GC_TEMPLATE": "worker"}}); err == nil {
				t.Fatal("one-shot proof reused")
			}
		})
	}
}

func TestNativeFailedOrNeverWitnessedStartRetainsSeatAcrossRestart(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "fast-exit-before-witness", true: "failed-and-late-launch"}[failed], func(t *testing.T) {
			city, c, host, sp, store := nativeTestFixture(t, 2)
			b := nativeTestSession(t, store)
			ctx, allowed, reason := admitCapacityStartWithHost(context.Background(), city, "worker", b.ID, time.Now(), sp, store, host)
			if !allowed {
				t.Fatal(reason)
			}
			host.defaultPopulated = false
			if failed {
				sp.startErr = errors.New("ambiguous timeout")
			}
			mgr := NewManagerWithOptions(store, sp, WithCityPath(city))
			_ = mgr.startRuntime(ctx, b.Metadata["session_name"], runtime.Config{Command: "fake-command", Env: map[string]string{"GC_SESSION_ID": b.ID, "GC_TEMPLATE": "worker"}})
			seat := readNativeTestLedger(t, city).Seats[b.ID]
			if seat.State != "ambiguous" || (failed && seat.LaunchFinalized) {
				t.Fatalf("uncertain start freed or finalized: %+v", seat)
			}
			if failed {
				host.defaultPopulated = true
				if err := withNativeCapacityLedger(context.Background(), city, c, host, func(l *nativeCapacityLedger) error { refreshNativeSeats(l, host, time.Now()); return nil }); err != nil {
					t.Fatal(err)
				}
				seat = readNativeTestLedger(t, city).Seats[b.ID]
				host.observations[seat.Unit] = nativeScopeObservation{Empty: true}
			}
			// A new Manager/claim has no invocation-local proof from the old process.
			other := nativeTestSession(t, store)
			_, err := claimNativeCapacityStart(context.Background(), city, "worker", other.ID, time.Now().Add(24*time.Hour), c, host, sp, store)
			if err == nil || len(readNativeTestLedger(t, city).Seats) != 3 {
				t.Fatal("unknown seat expired into free capacity")
			}
		})
	}
}

func TestNativeKnownEmptyScopeReleasesButDescendantAndUnknownDoNot(t *testing.T) {
	city, c, host, sp, store := nativeTestFixture(t, 2)
	b := nativeTestSession(t, store)
	ctx, allowed, reason := admitCapacityStartWithHost(context.Background(), city, "worker", b.ID, time.Now(), sp, store, host)
	if !allowed {
		t.Fatal(reason)
	}
	mgr := NewManagerWithOptions(store, sp, WithCityPath(city))
	if err := mgr.startRuntime(ctx, b.Metadata["session_name"], runtime.Config{Command: "fake-command", Env: map[string]string{"GC_SESSION_ID": b.ID, "GC_TEMPLATE": "worker"}}); err != nil {
		t.Fatal(err)
	}
	// Missing tmux/provider handle is NOT what frees the scope: a surviving
	// detached descendant keeps it populated, and diagnostic failure keeps it occupied.
	if err := sp.Stop(b.Metadata["session_name"]); err != nil {
		t.Fatal(err)
	}
	other := nativeTestSession(t, store)
	if _, err := claimNativeCapacityStart(context.Background(), city, "worker", other.ID, time.Now(), c, host, sp, store); err == nil {
		t.Fatal("lost provider freed live descendant")
	}
	host.observeErr = errors.New("manager unavailable")
	if _, err := claimNativeCapacityStart(context.Background(), city, "worker", other.ID, time.Now(), c, host, sp, store); err == nil {
		t.Fatal("unknown scope freed capacity")
	}
	host.observeErr = nil
	seat := readNativeTestLedger(t, city).Seats[b.ID]
	host.observations[seat.Unit] = nativeScopeObservation{Empty: true}
	if _, err := claimNativeCapacityStart(context.Background(), city, "worker", other.ID, time.Now(), c, host, sp, store); err != nil {
		t.Fatalf("positive kernel emptiness did not free seat: %v", err)
	}
	ledger := readNativeTestLedger(t, city)
	if len(ledger.Seats) != 3 || len(ledger.Retired) != 1 || ledger.Retired[0].SessionID != b.ID {
		t.Fatal("retirement/new reservation not one atomic ledger")
	}
}

func TestNativeMissingCorruptForeignOrUnsafeBaselineNeverInitializesEmpty(t *testing.T) {
	for _, mutation := range []string{"missing", "corrupt", "foreign", "symlink", "permissions"} {
		t.Run(mutation, func(t *testing.T) {
			city, c, host, sp, store := nativeTestFixture(t, 2)
			path := nativeCapacityPath(city)
			before, _ := os.ReadFile(path)
			switch mutation {
			case "missing":
				if err := os.Rename(path, path+".saved"); err != nil {
					t.Fatal(err)
				}
			case "corrupt":
				if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "foreign":
				if err := os.WriteFile(path, []byte(strings.Replace(string(before), "boot", "foreign-boot", 1)), 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(path, path+".saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+".saved", path); err != nil {
					t.Fatal(err)
				}
			case "permissions":
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			b := nativeTestSession(t, store)
			if _, err := claimNativeCapacityStart(context.Background(), city, "worker", b.ID, time.Now(), c, host, sp, store); err == nil {
				t.Fatal("unsafe baseline admitted")
			}
		})
	}
}

func TestNativeResourceOrScopeUnknownRefusesWithoutSeatAndChecksAgainAtLaunch(t *testing.T) {
	city, c, host, sp, store := nativeTestFixture(t, 2)
	b := nativeTestSession(t, store)
	host.resourcesErr = errors.New("memory unknown")
	if _, err := claimNativeCapacityStart(context.Background(), city, "worker", b.ID, time.Now(), c, host, sp, store); err == nil {
		t.Fatal("unknown resource admitted")
	}
	if len(readNativeTestLedger(t, city).Seats) != 2 {
		t.Fatal("resource refusal mutated occupied seats")
	}
	host.resourcesErr = nil
	host.commandErr = errors.New("no user manager")
	if _, err := claimNativeCapacityStart(context.Background(), city, "worker", b.ID, time.Now(), c, host, sp, store); err == nil {
		t.Fatal("scope unavailable admitted")
	}
	host.commandErr = nil
	ctx, allowed, reason := admitCapacityStartWithHost(context.Background(), city, "worker", b.ID, time.Now(), sp, store, host)
	if !allowed {
		t.Fatal(reason)
	}
	host.resourcesErr = errors.New("headroom lost after preparation")
	mgr := NewManagerWithOptions(store, sp, WithCityPath(city))
	if err := mgr.startRuntime(ctx, b.Metadata["session_name"], runtime.Config{Command: "fake-command", Env: map[string]string{"GC_SESSION_ID": b.ID, "GC_TEMPLATE": "worker"}}); !runtime.IsProviderCapacity(err) {
		t.Fatalf("actual boundary failed to reject: %v", err)
	}
	if len(sp.started) != 0 {
		t.Fatal("provider called after resources changed")
	}
	if len(readNativeTestLedger(t, city).Seats) != 2 {
		t.Fatal("provably unlaunched refusal leaked a permanent seat")
	}
}

func TestNativeAbandonOnlyCancelsUnlaunchedPrivateProof(t *testing.T) {
	city, _, host, sp, store := nativeTestFixture(t, 2)
	b := nativeTestSession(t, store)
	ctx, allowed, reason := admitCapacityStartWithHost(context.Background(), city, "worker", b.ID, time.Now(), sp, store, host)
	if !allowed {
		t.Fatal(reason)
	}
	if err := AbandonCapacityStart(ctx); err != nil {
		t.Fatal(err)
	}
	if len(readNativeTestLedger(t, city).Seats) != 2 {
		t.Fatal("never-started seat not released")
	}
	mgr := NewManagerWithOptions(store, sp, WithCityPath(city))
	if err := mgr.startRuntime(ctx, b.Metadata["session_name"], runtime.Config{Command: "fake-command", Env: map[string]string{"GC_SESSION_ID": b.ID, "GC_TEMPLATE": "worker"}}); err == nil {
		t.Fatal("canceled proof later launched")
	}
	ctx, allowed, reason = admitCapacityStartWithHost(context.Background(), city, "worker", b.ID, time.Now(), sp, store, host)
	if !allowed {
		t.Fatal(reason)
	}
	if err := mgr.startRuntime(ctx, b.Metadata["session_name"], runtime.Config{Command: "fake-command", Env: map[string]string{"GC_SESSION_ID": b.ID, "GC_TEMPLATE": "worker"}}); err != nil {
		t.Fatal(err)
	}
	if err := AbandonCapacityStart(ctx); err != nil {
		t.Fatal(err)
	}
	if len(readNativeTestLedger(t, city).Seats) != 3 {
		t.Fatal("abandon released actual live scope")
	}
}

func TestNativeUnknownOwnedSeatConsumesOneSeatWithoutVetoingOtherHeadroom(t *testing.T) {
	city, c, host, sp, store := nativeTestFixture(t, 1)
	b := nativeTestSession(t, store)
	ctx, allowed, reason := admitCapacityStartWithHost(context.Background(), city, "worker", b.ID, time.Now(), sp, store, host)
	if !allowed {
		t.Fatal(reason)
	}
	sp.startErr = errors.New("unconfirmed owned attempt")
	mgr := NewManagerWithOptions(store, sp, WithCityPath(city))
	_ = mgr.startRuntime(ctx, b.Metadata["session_name"], runtime.Config{Command: "fake-command", Env: map[string]string{"GC_SESSION_ID": b.ID, "GC_TEMPLATE": "worker"}})
	host.observeErr = errors.New("diagnostic unavailable for prior owned scope")
	other := nativeTestSession(t, store)
	if _, err := claimNativeCapacityStart(context.Background(), city, "worker", other.ID, time.Now(), c, host, sp, store); err != nil {
		t.Fatalf("one unknown seat vetoed spare independent capacity: %v", err)
	}
	if len(readNativeTestLedger(t, city).Seats) != 3 {
		t.Fatal("unknown was not counted")
	}
}

func TestNativeOpaqueProviderCannotRunOutsideScope(t *testing.T) {
	city, c, host, _, store := nativeTestFixture(t, 2)
	b := nativeTestSession(t, store)
	sp := runtime.NewFake() // No explicit local-shell containment capability.
	if _, err := claimNativeCapacityStart(context.Background(), city, "worker", b.ID, time.Now(), c, host, sp, store); err == nil {
		t.Fatal("opaque provider admitted")
	}
	if len(readNativeTestLedger(t, city).Seats) != 2 {
		t.Fatal("unsupported provider spent seat")
	}
}

func TestNativeModePreservesUnusedLegacyConfigurationSemantics(t *testing.T) {
	for _, header := range []string{"", "[admission]\nmode=\"legacy-census-v1\"\n"} {
		city := admissionTestCity(t)
		path := filepath.Join(city, "config/capacity-scheduler-v2.toml")
		// These fields were never decoded by the old start fence. Its route
		// scope still permits an unmanaged protected target without runtime IO.
		config := header + "[queue]\nmanaged_exact_routes=[\"worker\"]\n[limits]\nmax_managed_workers=\"unused\"\n[thresholds]\nmem_green_gib=\"unused\"\n"
		if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
		if mode, err := CapacityAdmissionMode(city); err != nil || mode != "legacy-census-v1" {
			t.Fatalf("legacy mode changed: %q %v", mode, err)
		}
		if allowed, reason := ClaimCapacityStart(city, "protected", "sid", time.Now(), nil, nil); !allowed || reason != "capacity_route_unmanaged" {
			t.Fatalf("legacy route behavior changed: %v %s", allowed, reason)
		}
	}
}
