package main

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/orders"
)

type reservedDispatchExecRecorder struct {
	mu       sync.Mutex
	calls    []string
	started  chan struct{}
	startOne sync.Once
	release  <-chan struct{}
}

func (r *reservedDispatchExecRecorder) run(ctx context.Context, command, _ string, _ []string) ([]byte, error) {
	r.mu.Lock()
	r.calls = append(r.calls, command)
	r.mu.Unlock()
	if r.started != nil {
		r.startOne.Do(func() { close(r.started) })
	}
	if r.release != nil {
		select {
		case <-r.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, nil
}

func (r *reservedDispatchExecRecorder) counts() map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	counts := make(map[string]int, len(r.calls))
	for _, command := range r.calls {
		counts[command]++
	}
	return counts
}

func reservedExecOrder(t *testing.T, name string, noWorkGate bool) orders.Order {
	t.Helper()
	definition := `[order]
exec = "placeholder"
trigger = "cooldown"
interval = "1h"
reserved_dispatch = true
`
	if noWorkGate {
		definition += "no_work_gate = true\n"
	}
	order, err := orders.Parse([]byte(definition))
	if err != nil {
		t.Fatalf("Parse reserved order %q: %v", name, err)
	}
	order.Name = name
	order.Exec = name
	return order
}

func ordinaryExecOrder(name string) orders.Order {
	return orders.Order{
		Name:     name,
		Exec:     name,
		Trigger:  "cooldown",
		Interval: "1h",
	}
}

func TestOrderDispatchReservedOrdersRemainSubjectToSuspension(t *testing.T) {
	tests := []struct {
		name  string
		order orders.Order
		cfg   *config.City
	}{
		{
			name:  "city",
			order: reservedExecOrder(t, "reserved-city", false),
			cfg:   &config.City{Workspace: config.Workspace{SuspendedOnStart: true}},
		},
		{
			name: "rig",
			order: func() orders.Order {
				order := reservedExecOrder(t, "reserved-rig", false)
				order.Rig = "frozen"
				return order
			}(),
			cfg: &config.City{Rigs: []config.Rig{{Name: "frozen", Path: "/frozen", SuspendedOnStart: true}}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := beads.NewMemStore()
			recorder := &reservedDispatchExecRecorder{}
			m := buildOrderDispatcherFromListExec([]orders.Order{tt.order}, store, nil, recorder.run, nil).(*memoryOrderDispatcher)
			m.cfg = tt.cfg
			cityPath := t.TempDir()
			m.cityPath = cityPath

			m.dispatch(context.Background(), cityPath, time.Date(2031, 1, 2, 3, 4, 5, 0, time.UTC))
			drainOrderDispatch(t, m)

			if got := len(recorder.counts()); got != 0 {
				t.Fatalf("reserved order dispatches while %s is suspended = %d, want 0", tt.name, got)
			}
		})
	}
}

func TestOrderDispatchReservedOrdersPreserveOpenWorkPolicy(t *testing.T) {
	tests := []struct {
		name       string
		noWorkGate bool
		wantCalls  int
	}{
		{name: "default gate blocks", wantCalls: 0},
		{name: "explicit no-work-gate opt-out still fires", noWorkGate: true, wantCalls: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const orderName = "reserved-open-work"
			store := beads.NewMemStore()
			openWork, err := store.Create(beads.Bead{
				Title:    "mol-do-work",
				Labels:   []string{"order-run:" + orderName},
				Metadata: map[string]string{beadmeta.KindMetadataKey: beadmeta.KindWisp},
			})
			if err != nil {
				t.Fatalf("Create open work: %v", err)
			}
			recorder := &reservedDispatchExecRecorder{}
			order := reservedExecOrder(t, orderName, tt.noWorkGate)
			m := buildOrderDispatcherFromListExec([]orders.Order{order}, store, nil, recorder.run, nil).(*memoryOrderDispatcher)

			m.dispatch(context.Background(), t.TempDir(), openWork.CreatedAt.Add(2*time.Hour))
			drainOrderDispatch(t, m)

			if got := recorder.counts()[orderName]; got != tt.wantCalls {
				t.Fatalf("reserved order dispatches = %d, want %d", got, tt.wantCalls)
			}
		})
	}
}

func TestOrderDispatchReservedOrdersRemainSubjectToTriggerEligibility(t *testing.T) {
	const orderName = "reserved-not-due"
	store := beads.NewMemStore()
	recent, err := store.Create(beads.Bead{
		Title:  "recent completed run",
		Status: "closed",
		Labels: []string{"order-run:" + orderName},
	})
	if err != nil {
		t.Fatalf("Create recent run: %v", err)
	}
	now := recent.CreatedAt.Add(time.Minute)

	recorder := &reservedDispatchExecRecorder{}
	m := buildOrderDispatcherFromListExec([]orders.Order{
		reservedExecOrder(t, orderName, false),
		ordinaryExecOrder("ordinary-due"),
	}, store, nil, recorder.run, nil).(*memoryOrderDispatcher)
	m.maxDispatchesPerTick = 1

	m.dispatch(context.Background(), t.TempDir(), now)
	drainOrderDispatch(t, m)

	counts := recorder.counts()
	if got := counts[orderName]; got != 0 {
		t.Errorf("not-due reserved order dispatches = %d, want 0", got)
	}
	if got := counts["ordinary-due"]; got != 1 {
		t.Errorf("ordinary due order dispatches = %d, want 1", got)
	}
}

func TestOrderDispatchReservedOrderDoesNotDoubleDispatchOnRepeatTick(t *testing.T) {
	t.Run("in-flight tracking bead", func(t *testing.T) {
		const orderName = "reserved-single-flight"
		store := beads.NewMemStore()
		started := make(chan struct{})
		release := make(chan struct{})
		recorder := &reservedDispatchExecRecorder{started: started, release: release}
		m := buildOrderDispatcherFromListExec([]orders.Order{reservedExecOrder(t, orderName, false)}, store, nil, recorder.run, nil).(*memoryOrderDispatcher)
		cityPath := t.TempDir()
		var releaseOne sync.Once
		releaseRun := func() { releaseOne.Do(func() { close(release) }) }
		t.Cleanup(func() {
			releaseRun()
			drainOrderDispatch(t, m)
		})

		now := time.Date(2031, 1, 2, 3, 4, 5, 0, time.UTC)
		m.dispatch(context.Background(), cityPath, now)
		awaitClose(t, started, "reserved order exec start")
		m.dispatch(context.Background(), cityPath, now.Add(time.Second))

		if got := len(trackingBeads(t, store, "order-run:"+orderName)); got != 1 {
			t.Fatalf("tracking runs across an immediate in-flight repeat tick = %d, want 1", got)
		}
		releaseRun()
		drainOrderDispatch(t, m)
		if got := recorder.counts()[orderName]; got != 1 {
			t.Fatalf("exec calls across an immediate in-flight repeat tick = %d, want 1", got)
		}
	})

	t.Run("completed cooldown", func(t *testing.T) {
		const orderName = "reserved-cooldown"
		store := beads.NewMemStore()
		recorder := &reservedDispatchExecRecorder{}
		m := buildOrderDispatcherFromListExec([]orders.Order{reservedExecOrder(t, orderName, false)}, store, nil, recorder.run, nil).(*memoryOrderDispatcher)
		cityPath := t.TempDir()

		now := time.Now()
		m.dispatch(context.Background(), cityPath, now)
		drainOrderDispatch(t, m)
		firstRuns := trackingBeads(t, store, "order-run:"+orderName)
		if len(firstRuns) != 1 {
			t.Fatalf("tracking runs after first tick = %d, want 1", len(firstRuns))
		}
		m.dispatch(context.Background(), cityPath, firstRuns[0].CreatedAt.Add(time.Second))
		drainOrderDispatch(t, m)

		if got := recorder.counts()[orderName]; got != 1 {
			t.Fatalf("exec calls across an immediate completed repeat tick = %d, want 1", got)
		}
		if got := len(trackingBeads(t, store, "order-run:"+orderName)); got != 1 {
			t.Fatalf("tracking runs across an immediate completed repeat tick = %d, want 1", got)
		}
	})
}

// A declared health reservation must receive a bounded opportunity even when
// ordinary clock work ahead of it spends the entire ordinary dispatch budget.
func TestOrderDispatchReservedCapacitySurvivesOrdinaryContention(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := beads.NewMemStore()
		recorder := &reservedDispatchExecRecorder{}
		aa := []orders.Order{ordinaryExecOrder("ordinary-a"), ordinaryExecOrder("ordinary-b")}
		for _, name := range []string{"reserved-a", "reserved-b", "reserved-c", "reserved-d", "reserved-e"} {
			aa = append(aa, reservedExecOrder(t, name, false))
		}
		m := buildOrderDispatcherFromListExec(aa, store, nil, recorder.run, nil).(*memoryOrderDispatcher)
		m.maxDispatchesPerTick = 1
		cityPath := t.TempDir()
		now := time.Now()
		m.dispatch(context.Background(), cityPath, now)
		drainOrderDispatch(t, m)
		counts := recorder.counts()
		for _, name := range []string{"ordinary-a", "reserved-a", "reserved-b", "reserved-c"} {
			if got := counts[name]; got != 1 {
				t.Errorf("first-tick %s dispatches = %d, want 1", name, got)
			}
		}
		for _, name := range []string{"ordinary-b", "reserved-d", "reserved-e"} {
			if got := counts[name]; got != 0 {
				t.Errorf("first-tick %s dispatches = %d, want 0: both lanes must remain capped", name, got)
			}
		}
		m.dispatch(context.Background(), cityPath, now.Add(time.Second))
		drainOrderDispatch(t, m)
		counts = recorder.counts()
		for _, order := range aa {
			if got := counts[order.Name]; got != 1 {
				t.Errorf("after two ticks %s dispatches = %d, want 1: independent rotation must progress without duplicates", order.Name, got)
			}
		}
	})
}

func TestOrderDispatchUnusedReservationDoesNotEnlargeOrdinaryBudget(t *testing.T) {
	store := beads.NewMemStore()
	recorder := &reservedDispatchExecRecorder{}
	aa := []orders.Order{reservedExecOrder(t, "reserved-only", false), ordinaryExecOrder("ordinary-a"), ordinaryExecOrder("ordinary-b")}
	m := buildOrderDispatcherFromListExec(aa, store, nil, recorder.run, nil).(*memoryOrderDispatcher)
	m.maxDispatchesPerTick = 1
	m.dispatch(context.Background(), t.TempDir(), time.Date(2031, 1, 2, 3, 4, 5, 0, time.UTC))
	drainOrderDispatch(t, m)
	counts := recorder.counts()
	for _, name := range []string{"reserved-only", "ordinary-a"} {
		if got := counts[name]; got != 1 {
			t.Errorf("%s dispatches = %d, want 1", name, got)
		}
	}
	if got := counts["ordinary-b"]; got != 0 {
		t.Errorf("ordinary-b dispatches = %d, want 0: unused reserved capacity cannot be borrowed", got)
	}
}

// All reservations remain due on every tick; cooldown suppression must not
// disguise a cursor that repeatedly favors the first three definitions.
func TestOrderDispatchReservedOverflowRotatesWithAllOrdersDue(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := beads.NewMemStore()
		recorder := &reservedDispatchExecRecorder{}
		var aa []orders.Order
		for _, name := range []string{"reserved-a", "reserved-b", "reserved-c", "reserved-d", "reserved-e"} {
			aa = append(aa, reservedExecOrder(t, name, false))
		}
		for _, name := range []string{"ordinary-a", "ordinary-b", "ordinary-c", "ordinary-d", "ordinary-e", "ordinary-f"} {
			aa = append(aa, ordinaryExecOrder(name))
		}
		m := buildOrderDispatcherFromListExec(aa, store, nil, recorder.run, nil).(*memoryOrderDispatcher)
		m.maxDispatchesPerTick = 4
		cityPath := t.TempDir()
		now := time.Now()
		for tick := 0; tick < 5; tick++ {
			before := recorder.counts()
			m.dispatch(context.Background(), cityPath, now.Add(time.Duration(tick)*2*time.Hour))
			drainOrderDispatch(t, m)
			after := recorder.counts()
			reserved, ordinary := 0, 0
			for _, order := range aa {
				n := after[order.Name] - before[order.Name]
				if order.ReservedDispatch {
					reserved += n
				} else {
					ordinary += n
				}
			}
			if reserved != 3 || ordinary != 4 {
				t.Fatalf("tick %d launches reserved=%d ordinary=%d, want 3 and 4 independently", tick, reserved, ordinary)
			}
		}
		counts := recorder.counts()
		for _, order := range aa[:5] {
			if got := counts[order.Name]; got != 3 {
				t.Errorf("%s dispatches after five saturated ticks = %d, want 3", order.Name, got)
			}
		}
	})
}

func TestOrderDispatchUnusedOrdinarySlotsDoNotEnlargeReservedCapacity(t *testing.T) {
	for _, budget := range []int{1, 4, 0} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			recorder := &reservedDispatchExecRecorder{}
			var aa []orders.Order
			for _, name := range []string{"reserved-a", "reserved-b", "reserved-c", "reserved-d"} {
				aa = append(aa, reservedExecOrder(t, name, false))
			}
			m := buildOrderDispatcherFromListExec(aa, beads.NewMemStore(), nil, recorder.run, nil).(*memoryOrderDispatcher)
			m.maxDispatchesPerTick = budget
			m.dispatch(context.Background(), t.TempDir(), time.Date(2031, 1, 2, 3, 4, 5, 0, time.UTC))
			drainOrderDispatch(t, m)
			want := 3
			if budget == 0 {
				want = 4
			}
			if got := len(recorder.counts()); got != want {
				t.Fatalf("reserved dispatches=%d, want %d: no borrowing; unlimited sentinel preserved", got, want)
			}
		})
	}
}

func TestOrderDispatchReservedRotationSurvivesGateMembershipChanges(t *testing.T) {
	for _, trackingGate := range []bool{false, true} {
		t.Run(fmt.Sprintf("tracking-gate=%t", trackingGate), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				store := beads.NewMemStore()
				recorder := &reservedDispatchExecRecorder{}
				names := []string{"reserved-a", "reserved-b", "reserved-c", "reserved-d", "reserved-e"}
				var aa []orders.Order
				for _, name := range names {
					aa = append(aa, reservedExecOrder(t, name, false))
				}
				aa = append(aa, ordinaryExecOrder("ordinary"))
				var blocked []string
				for _, name := range []string{"reserved-a", "reserved-c"} {
					var id string
					if trackingGate {
						run, err := orders.NewStore(beads.OrdersStore{Store: store}).CreateRun(name, orders.RunOpts{})
						if err != nil {
							t.Fatal(err)
						}
						id = run.ID
					} else {
						b, err := store.Create(beads.Bead{Title: "open work", Labels: []string{orders.RunLabel(name)}, Metadata: map[string]string{beadmeta.KindMetadataKey: beadmeta.KindWisp}})
						if err != nil {
							t.Fatal(err)
						}
						id = b.ID
					}
					blocked = append(blocked, id)
				}
				m := buildOrderDispatcherFromListExec(aa, store, nil, recorder.run, nil).(*memoryOrderDispatcher)
				m.maxDispatchesPerTick = 1
				cityPath := t.TempDir()
				now := time.Now()
				m.dispatch(context.Background(), cityPath, now)
				drainOrderDispatch(t, m)
				counts := recorder.counts()
				for _, name := range []string{"reserved-b", "reserved-d", "reserved-e", "ordinary"} {
					if counts[name] != 1 {
						t.Errorf("first-tick %s=%d, want1", name, counts[name])
					}
				}
				for _, name := range []string{"reserved-a", "reserved-c"} {
					if counts[name] != 0 {
						t.Errorf("gated %s fired", name)
					}
				}
				closed := "closed"
				for _, id := range blocked {
					if err := store.Update(id, beads.UpdateOpts{Status: &closed}); err != nil {
						t.Fatal(err)
					}
				}
				m.dispatch(context.Background(), cityPath, now.Add(2*time.Hour))
				drainOrderDispatch(t, m)
				counts = recorder.counts()
				for i, name := range names {
					want := 1
					if i == 1 {
						want = 2
					}
					if counts[name] != want {
						t.Errorf("after gate change %s=%d, want%d: cursor must index the stable order list", name, counts[name], want)
					}
				}
			})
		})
	}
}
