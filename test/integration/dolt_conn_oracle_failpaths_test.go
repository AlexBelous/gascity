//go:build integration && conn_oracle

package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// seedAssignedBead creates a city-scoped bead assigned to assignee, optionally
// moved to in_progress, and waits for it to be visible to the controller before
// returning its id. It is the setup for the assigned-in-progress (tier 1, crash
// recovery) and assigned-ready (tier 2) hook cases.
func seedAssignedBead(t *testing.T, city *connOracleCity, title, assignee string, inProgress bool) string {
	t.Helper()
	out, err := runGCWithEnv(city.env, city.dir, "bd", "create", "--json", title, "--assignee", assignee)
	if err != nil {
		t.Fatalf("create assigned bead %q: %v\n%s", title, err, out)
	}
	id := beadIDFromJSON(out)
	if id == "" {
		t.Fatalf("create assigned bead %q: no id in output:\n%s", title, out)
	}
	if inProgress {
		if o, err := runGCWithEnv(city.env, city.dir, "bd", "update", id, "--status", "in_progress", "--assignee", assignee); err != nil {
			t.Fatalf("move bead %s to in_progress: %v\n%s", id, err, o)
		}
	}
	waitBeadVisible(t, city, id)
	return id
}

// waitBeadVisible polls until the controller reports the bead (its write has
// propagated into the controller's cache), so a claim immediately afterward does
// not read a cold cache as no-work.
func waitBeadVisible(t *testing.T, city *connOracleCity, id string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		out, err := runGCWithEnv(city.env, city.dir, "bd", "show", id, "--json")
		if err == nil && strings.Contains(out, id) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("bead %s never became visible within 20s (last err=%v)", id, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// assertRouteAPI fails unless the hook output shows the controller fast path was
// taken (route=api), so a case that silently fell back to the subprocess path
// cannot masquerade as fast-path coverage.
func assertRouteAPI(t *testing.T, context, out string) {
	t.Helper()
	if !strings.Contains(out, "route=api") {
		t.Fatalf("%s: hook did not take the controller fast path (no route=api):\n%s", context, out)
	}
}

// TestManagedDoltConnOracle_AssignedTiersAndEmpty covers the generated-default
// query's assigned-in-progress (tier 1, crash recovery), assigned-ready (tier 2),
// and empty (clean no-work drain) cases end-to-end over the controller fast path.
func TestManagedDoltConnOracle_AssignedTiersAndEmpty(t *testing.T) {
	requireDoltIntegration(t)
	city := setupConnOracleCity(t, true)

	inProgID := seedAssignedBead(t, city, "assigned-in-progress", "actor-inprog", true)
	readyID := seedAssignedBead(t, city, "assigned-ready", "actor-ready", false)

	// Tier 1: the owner of an in_progress bead re-adopts it through the fast path.
	out1, _ := runHookClaim(city, "actor-inprog")
	assertRouteAPI(t, "assigned-in-progress", out1)
	if id, asg := heldBeadAndAssignee(out1); id != inProgID || asg != "actor-inprog" {
		t.Fatalf("assigned-in-progress: hook claimed (%s,%s), want (%s,actor-inprog)\n%s", id, asg, inProgID, out1)
	}

	// Tier 2: the owner of an assigned-ready bead claims it through the fast path.
	out2, _ := runHookClaim(city, "actor-ready")
	assertRouteAPI(t, "assigned-ready", out2)
	if id, asg := heldBeadAndAssignee(out2); id != readyID || asg != "actor-ready" {
		t.Fatalf("assigned-ready: hook claimed (%s,%s), want (%s,actor-ready)\n%s", id, asg, readyID, out2)
	}

	// Empty: an identity with no assigned work and no routed pool demand takes the
	// fast path and drains cleanly with no claim.
	out3, _ := runHookClaim(city, "actor-with-no-work")
	assertRouteAPI(t, "empty", out3)
	if id, _ := claimedBeadAndAssignee(out3); id != "" {
		t.Fatalf("empty: hook unexpectedly claimed %s\n%s", id, out3)
	}
	if !strings.Contains(out3, "\"action\":\"drain\"") {
		t.Fatalf("empty: hook did not drain cleanly\n%s", out3)
	}
}

// TestManagedDoltConnOracle_SameActorRetryIsIdempotent proves invariant 4's
// idempotence clause: the same actor re-claiming a bead it already owns succeeds
// with the same bead (never an error, never a different owner), while a different
// actor cannot acquire the now-owned bead.
func TestManagedDoltConnOracle_SameActorRetryIsIdempotent(t *testing.T) {
	requireDoltIntegration(t)
	city := setupConnOracleCity(t, true)
	want := seedRoutedPoolDemand(t, city, 1)[0]

	out1, _ := runHookClaim(city, "polecat-retry")
	assertRouteAPI(t, "first claim", out1)
	if id, asg := heldBeadAndAssignee(out1); id != want || asg != "polecat-retry" {
		t.Fatalf("first claim got (%s,%s), want (%s,polecat-retry)\n%s", id, asg, want, out1)
	}

	// Same actor retries — idempotent, same bead.
	out2, _ := runHookClaim(city, "polecat-retry")
	assertRouteAPI(t, "same-actor retry", out2)
	if id, asg := heldBeadAndAssignee(out2); id != want || asg != "polecat-retry" {
		t.Fatalf("same-actor retry got (%s,%s), want idempotent (%s,polecat-retry)\n%s", id, asg, want, out2)
	}

	// A different actor cannot acquire the owned bead.
	out3, _ := runHookClaim(city, "polecat-intruder")
	if id, _ := claimedBeadAndAssignee(out3); id == want {
		t.Fatalf("different actor acquired owned bead %s\n%s", want, out3)
	}
}

// TestManagedDoltConnOracle_APIUnavailableBoundedFallback proves the failure
// semantics: when the controller is unavailable to a worker hook before the
// request (GC_NO_API), the hook falls back to the bd subprocess adapter and still
// atomically CLAIMS unassigned routed pool demand — the mutation path, not merely
// re-adoption of already-owned work — exercised separately at BOUNDED concurrency
// so the fallback's per-worker SQL clients stay well under the listener cap. It
// asserts the explicit route=fallback marker (invariant 7), a fresh reason=claimed
// per hook, and that every intended actor owns exactly one DISTINCT bead.
func TestManagedDoltConnOracle_APIUnavailableBoundedFallback(t *testing.T) {
	requireDoltIntegration(t)
	city := setupConnOracleCity(t, true)

	const n = 12
	// Unassigned routed pool demand: the fallback must exercise the atomic claim
	// mutation, so each hook freshly claims one of these rather than adopting
	// pre-assigned work.
	seedRoutedPoolDemand(t, city, n)
	actors := make([]string, n)
	for i := 0; i < n; i++ {
		actors[i] = fmt.Sprintf("fallback-actor-%02d", i)
	}

	var peak int64
	stop := make(chan struct{})
	var sampler sync.WaitGroup
	sampler.Add(1)
	go func() {
		defer sampler.Done()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if c := int64(probeThreadsConnected(city.observer)); c > atomic.LoadInt64(&peak) {
					atomic.StoreInt64(&peak, c)
				}
			}
		}
	}()

	var (
		mu            sync.Mutex
		routeFallback int
		routeAPI      int
		rejections    int
		freshClaims   []hookClaim // reason=claimed only
	)
	sem := make(chan struct{}, 6) // bounded concurrency
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(actor string) {
			defer wg.Done()
			defer func() { <-sem }()
			// GC_NO_API makes the controller unavailable to this hook before the
			// request, forcing the bd subprocess (BdStore) fallback; GC_DEBUG (from
			// hookActorEnv) turns on the route diagnostics.
			env := append(hookActorEnv(city, actor), "GC_NO_API=1")
			out, _ := runGCWithEnv(env, city.dir, "hook", "--claim", "--json")
			id, asg := claimedBeadAndAssignee(out) // strict: reason=claimed (fresh mutation)
			mu.Lock()
			defer mu.Unlock()
			if strings.Contains(out, "route=fallback") {
				routeFallback++
			}
			if strings.Contains(out, "route=api") {
				routeAPI++
			}
			if isConnectionRejection(out) {
				rejections++
			}
			if id != "" {
				freshClaims = append(freshClaims, hookClaim{beadID: id, assignee: asg, actor: actor})
			}
		}(actors[i])
	}
	wg.Wait()
	close(stop)
	sampler.Wait()

	peakConns := int(atomic.LoadInt64(&peak))
	t.Logf("API-unavailable fallback: freshClaims=%d/%d route=fallback=%d route=api=%d rejections=%d peakConns=%d",
		len(freshClaims), n, routeFallback, routeAPI, rejections, peakConns)

	// Explicit fallback evidence (invariant 7): every hook logged route=fallback,
	// none took the fast path.
	if routeAPI != 0 {
		t.Fatalf("%d hook(s) took route=api despite the controller being unavailable", routeAPI)
	}
	if routeFallback != n {
		t.Fatalf("only %d/%d hooks logged route=fallback; the controller-unavailable fallback was not uniformly exercised", routeFallback, n)
	}
	// Fresh atomic claim by every intended actor, exact 1:1 ownership.
	if len(freshClaims) != n {
		t.Fatalf("fallback freshly claimed %d/%d beads (reason=claimed); the subprocess mutation path did not serve every actor", len(freshClaims), n)
	}
	assertUniqueClaims(t, freshClaims) // no bead won by two actors; assignee==actor exactly
	seenBead := map[string]struct{}{}
	for _, c := range freshClaims {
		if _, dup := seenBead[c.beadID]; dup {
			t.Fatalf("bead %s claimed by more than one fallback actor", c.beadID)
		}
		seenBead[c.beadID] = struct{}{}
	}
	if rejections != 0 {
		t.Fatalf("%d fallback hook(s) saw a connection rejection", rejections)
	}
	if peakConns >= oracleMaxConnections {
		t.Fatalf("bounded fallback peaked at %d connections, reaching the %d cap", peakConns, oracleMaxConnections)
	}
}

// TestManagedDoltConnOracle_KilledClientsReturnToBaseline kills a controlled
// subset of hook clients both before and after they submit their request, and
// asserts the managed server's connection count stays bounded during the load and
// returns to baseline after — the core cure property: a worker hook holds no
// managed-Dolt socket, so a SIGKILLed worker strands nothing on the server.
func TestManagedDoltConnOracle_KilledClientsReturnToBaseline(t *testing.T) {
	requireDoltIntegration(t)
	city := setupConnOracleCity(t, true)
	seedRoutedPoolDemand(t, city, oracleSeedBeads)
	baseline := sampleThreadsConnected(t, city.observer)

	const procs = 120
	var peak int64
	stop := make(chan struct{})
	var sampler sync.WaitGroup
	sampler.Add(1)
	go func() {
		defer sampler.Done()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if c := int64(probeThreadsConnected(city.observer)); c > atomic.LoadInt64(&peak) {
					atomic.StoreInt64(&peak, c)
				}
			}
		}
	}()

	var wg sync.WaitGroup
	var killed int64
	sem := make(chan struct{}, oracleMaxInFlight)
	for i := 0; i < procs; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			actor := fmt.Sprintf("killable-%03d", i)
			cmd := exec.Command(gcBinary, "hook", "--claim", "--json")
			cmd.Dir = city.dir
			cmd.Env = hookActorEnv(city, actor)
			if err := cmd.Start(); err != nil {
				return
			}
			// Kill every third hook: half "before request" (immediately) and half
			// "after request" (a brief delay lets it reach/submit the claim). The
			// rest run to completion.
			switch i % 3 {
			case 0: // before request submission
				_ = cmd.Process.Kill()
				atomic.AddInt64(&killed, 1)
				_, _ = cmd.Process.Wait()
				return
			case 1: // after request submission
				time.Sleep(15 * time.Millisecond)
				_ = cmd.Process.Kill()
				atomic.AddInt64(&killed, 1)
				_, _ = cmd.Process.Wait()
				return
			default:
				_ = cmd.Wait()
			}
		}(i)
	}
	wg.Wait()
	close(stop)
	sampler.Wait()

	peakConns := int(atomic.LoadInt64(&peak))
	t.Logf("killed clients: killed=%d/%d peakConns=%d baseline=%d", atomic.LoadInt64(&killed), procs, peakConns, baseline)
	if atomic.LoadInt64(&killed) == 0 {
		t.Fatalf("no hook was killed; the case was not exercised")
	}
	if peakConns >= oracleMaxConnections {
		t.Fatalf("killed-client load peaked at %d connections, reaching the %d cap", peakConns, oracleMaxConnections)
	}
	if peakConns > oracleFastPathConnCap {
		t.Fatalf("killed-client load peaked at %d connections, above the bounded-pool cap %d; a killed worker leaked a server connection", peakConns, oracleFastPathConnCap)
	}
	final := waitThreadsConnectedSettle(t, city.observer, baseline+2, 20*time.Second)
	if final > baseline+2 {
		t.Fatalf("connections did not return to baseline after killed clients: %d > baseline %d(+2)", final, baseline)
	}
}

// This file adds the AC-3 failure-path subtests that layer onto the core
// process-level oracle in dolt_conn_oracle_test.go: they reuse the same managed-
// Dolt harness (with the orphan-reconciler held via max_active_sessions=0) to
// prove the remaining design assertions — admission saturation fails fast without
// invoking managed-Dolt recovery, and the connection bound does not depend on a
// short server read_timeout.

// managedDoltStateForTest reads the managed Dolt server PID and started_at from
// the city's dolt runtime state, so a test can prove the server was NOT restarted
// (recovery was not invoked) by comparing both before and after a load: a
// recovery restart mints a new PID AND a new started_at, so an unchanged pair is
// stronger evidence than PID alone.
func managedDoltStateForTest(cityDir string) (pid int, startedAt string, ok bool) {
	data, err := os.ReadFile(filepath.Join(cityDir, ".gc", "runtime", "packs", "dolt", "dolt-state.json"))
	if err != nil {
		return 0, "", false
	}
	var state struct {
		Running   bool   `json:"running"`
		PID       int    `json:"pid"`
		StartedAt string `json:"started_at"`
	}
	if err := json.Unmarshal(data, &state); err != nil || !state.Running || state.PID <= 0 {
		return 0, "", false
	}
	return state.PID, state.StartedAt, true
}

// assertNoManagedDoltRecoveryLog is best-effort supervisor-log evidence that the
// managed-Dolt recovery path was not entered during the load (recovery logs to
// the controller's stderr, which the isolated supervisor tees to supervisor.log).
// It fails only when a recovery marker is actually present; a missing log is not
// a failure, since the PID/started_at invariant already proves no restart.
func assertNoManagedDoltRecoveryLog(t *testing.T, city *connOracleCity) {
	t.Helper()
	gcHome := ""
	for _, kv := range city.env {
		if strings.HasPrefix(kv, "GC_HOME=") {
			gcHome = strings.TrimPrefix(kv, "GC_HOME=")
			break
		}
	}
	if gcHome == "" {
		return
	}
	data, err := os.ReadFile(filepath.Join(gcHome, "supervisor.log"))
	if err != nil {
		return
	}
	log := strings.ToLower(string(data))
	for _, marker := range []string{"circuit breaker is open", "recover-managed", "recovering managed dolt", "managed dolt recovery"} {
		if strings.Contains(log, marker) {
			t.Fatalf("supervisor log shows managed-Dolt recovery was invoked (marker %q) during admission saturation", marker)
		}
	}
}

// TestManagedDoltConnOracle_AdmissionSaturationFailsFastWithoutRecovery proves
// AC-3's admission-saturation invariant (invariant 5): when the controller's
// bounded claim admission is saturated, the claim fails fast with a retryable
// degraded result and NEVER invokes managed-Dolt recovery. It pins the admission
// ceiling to a single slot (GC_CLAIM_ADMISSION_SLOTS=1, inherited by the
// controller process) and drives the full 200-hook load, then asserts that some
// hooks were admission-saturated, the managed Dolt server was not restarted (same
// PID), and every hook that DID win got a distinct bead.
func TestManagedDoltConnOracle_AdmissionSaturationFailsFastWithoutRecovery(t *testing.T) {
	requireDoltIntegration(t)
	// Pin the controller's claim admission to one slot. integrationEnvFor forwards
	// unfiltered GC_* env into the supervisor/controller, and the process-wide
	// admitter reads this at start, so the controller admits one claim at a time.
	t.Setenv("GC_CLAIM_ADMISSION_SLOTS", "1")

	city := setupConnOracleCity(t, true)
	seedRoutedPoolDemand(t, city, oracleSeedBeads)

	pidBefore, startedBefore, ok := managedDoltStateForTest(city.dir)
	if !ok {
		t.Fatalf("could not read managed Dolt state before load")
	}

	start := time.Now()
	result := runHookLoad(t, city, oracleHookProcs)
	elapsed := time.Since(start)
	t.Logf("admission saturation: saturated=%d claims=%d apiRoutes=%d rejections=%d peakConns=%d elapsed=%s",
		result.saturated, len(result.claims), result.apiRoutes, result.rejections, result.peakConns, elapsed)

	// The fast path must be exercised, and the one-slot admitter must actually
	// saturate under 200 concurrent claimants (otherwise the invariant is untested).
	if result.apiRoutes == 0 {
		t.Fatalf("no hook took the controller route; admission was never exercised")
	}
	if result.saturated == 0 {
		t.Fatalf("no hook was admission-saturated with a single admission slot under %d concurrent claimants", oracleHookProcs)
	}

	// Fail-fast within the hook budget: a saturated claim returns a retryable
	// degraded result immediately rather than blocking, so the whole load completes
	// well within budget. A saturated claim that blocked on the single slot would
	// serialize all 200 hooks behind slow work and balloon this far past the bound.
	const saturationLoadBudget = 90 * time.Second
	if elapsed > saturationLoadBudget {
		t.Fatalf("admission-saturated load took %s > %s budget; a saturated claim blocked instead of failing fast", elapsed, saturationLoadBudget)
	}

	// No managed-Dolt recovery: admission saturation is a bounded degraded result,
	// never a recovery trigger. Prove it three ways beyond a single PID sample: the
	// server PID and started_at are both unchanged (no restart), and the supervisor
	// log shows no recovery marker (recovery was not entered).
	pidAfter, startedAfter, ok := managedDoltStateForTest(city.dir)
	if !ok {
		t.Fatalf("could not read managed Dolt state after load")
	}
	if pidAfter != pidBefore || startedAfter != startedBefore {
		t.Fatalf("managed Dolt restarted (pid %d->%d, started_at %q->%q): admission saturation invoked recovery",
			pidBefore, pidAfter, startedBefore, startedAfter)
	}
	assertNoManagedDoltRecoveryLog(t, city)

	// A connection rejection is distinct from admission saturation: the cure must
	// never let a worker open its own SQL client, so even under saturation the
	// managed server sees no rejected client.
	if result.rejections != 0 {
		t.Fatalf("%d hook(s) saw a managed-Dolt connection rejection under admission saturation, want 0", result.rejections)
	}

	// Every hook that won still won a distinct bead (saturated hooks simply did
	// not claim); admission bounding does not corrupt claim atomicity.
	assertUniqueClaims(t, result.claims)
}

// TestManagedDoltConnOracle_LongReadTimeoutStillBounds proves AC-3's final
// requirement: the connection cure does not depend on the managed Dolt server
// promptly reaping idle client sockets. It runs the same 200-hook fast-path load
// against a server configured with a very long read_timeout and asserts the same
// bounded, rejection-free, unique-claim outcome as the default-timeout oracle.
func TestManagedDoltConnOracle_LongReadTimeoutStillBounds(t *testing.T) {
	requireDoltIntegration(t)
	const tenMinutesMillis = 600000

	city := setupConnOracleCityWithReadTimeout(t, true, tenMinutesMillis)
	seedRoutedPoolDemand(t, city, oracleSeedBeads)
	baseline := sampleThreadsConnected(t, city.observer)

	result := runHookLoad(t, city, oracleHookProcs)
	t.Logf("long read_timeout: peakConns=%d claims=%d apiRoutes=%d rejections=%d (baseline=%d)",
		result.peakConns, len(result.claims), result.apiRoutes, result.rejections, baseline)

	if result.apiRoutes == 0 {
		t.Fatalf("no hook took the controller route; the cure was not exercised")
	}
	assertUniqueClaims(t, result.claims)
	if len(result.claims) == 0 {
		t.Fatalf("no bead was claimed under load")
	}
	if result.rejections != 0 {
		t.Fatalf("%d hook(s) saw a connection rejection with a long read_timeout, want 0", result.rejections)
	}
	if result.peakConns >= oracleMaxConnections {
		t.Fatalf("peak Threads_connected=%d reached the %d-connection cap despite the controller pool bound", result.peakConns, oracleMaxConnections)
	}
	if result.peakConns > oracleFastPathConnCap {
		t.Fatalf("peak Threads_connected=%d exceeds the bounded-pool cap %d with a long read_timeout; the bound depended on fast reaping", result.peakConns, oracleFastPathConnCap)
	}
}
