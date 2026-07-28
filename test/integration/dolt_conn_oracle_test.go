//go:build integration && conn_oracle

package integration

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// This is the AC-3 process-level oracle for the managed-Dolt connection cure
// (design: docs/design/managed-dolt-connection-boundary-2026-07-27.md,
// "Real-Dolt acceptance test"). The store-level oracle
// (internal/beads/native_dolt_store_pool_integration_test.go) proves invariant 6
// at a single bounded pool; this proves the END-TO-END property the incident was
// about: many concurrent generated-default `gc hook --claim` PROCESSES routed
// through a running controller against a real managed-Dolt sql-server
// (max_connections=32) do NOT accumulate server connections, claim atomically,
// and are never rejected — because the worker processes open no SQL socket at
// all; the controller's bounded pools are the only clients.
//
// It is heavy (a managed-Dolt city plus hundreds of `gc` subprocess forks), so
// it is opt-in behind the conn_oracle build tag, mirroring the chaos_dolt test.
// Run with: make test-conn-oracle
//   (go test -tags 'integration conn_oracle' -run TestManagedDoltConnOracle ...)

const (
	// oracleHookProcs is the concurrent generated-default hook process count the
	// design mandates ("at least 200").
	oracleHookProcs = 200
	// oracleMaxInFlight bounds how many `gc` forks run at once. The design mandates
	// at least 200 TRULY-concurrent hooks: the per-worker connection storm the cure
	// removes only materializes when all workers overlap, so a lower cap (an earlier
	// revision used 40) made the concurrency claim vacuous. It therefore matches
	// oracleHookProcs — every seeded hook runs at once. The protected runner
	// (scix-batch --mem-high 8G --mem-max 12G) is provisioned for the fork load.
	oracleMaxInFlight = oracleHookProcs
	// oracleSeedBeads is the routed-pool demand seeded for the claim race. Fewer
	// beads than hooks guarantees real contention: many hooks find no work, and
	// every claimed bead must have exactly one winner.
	oracleSeedBeads = 80
	// oracleMaxConnections is the managed-Dolt listener cap the design pins.
	oracleMaxConnections = 32
	// oracleFastPathConnCap is the upper bound on server connections the bounded
	// controller pools may reach under the full hook load. It sits well below the
	// 32-connection listener cap: the controller owns one connection per scoped
	// store (city + rigs) plus a small operational margin, and the worker hooks
	// add none. A peak at or above this cap means either the fast path silently
	// fell back to per-worker SQL clients or a pool is unbounded.
	oracleFastPathConnCap = 24
	// oraclePoolAgent is the generated-default pool worker whose route the seeded
	// demand targets. It carries no work_query, so hooks take the fast path.
	oraclePoolAgent = "polecat"
	// oracleCustomAgent carries an explicit work_query, so its hook must stay on
	// the subprocess adapter (invariant 3) and log route=custom-shell rather than
	// route=api. It exercises the custom-query lane's explicit observability.
	oracleCustomAgent = "custom-shell-worker"
)

// connOracleCity is a running managed-Dolt city wired for the oracle: a live
// controller over controller-owned NativeDoltStores, a managed-Dolt sql-server
// at max_connections=32, and a root observer connection for SHOW STATUS probes.
type connOracleCity struct {
	dir      string
	env      []string
	port     string
	observer *sql.DB
}

// TestManagedDoltConnOracle_Diagnostic is a fast (small-count) probe used to
// pin down why a hook does or does not take the controller fast path in the
// oracle harness: it dumps the effective [beads] config, whether the maintenance
// client resolves to the controller (route=api), and one hook's full output.
func TestManagedDoltConnOracle_Diagnostic(t *testing.T) {
	requireDoltIntegration(t)
	city := setupConnOracleCity(t, true)
	ids := seedRoutedPoolDemand(t, city, 3)
	t.Logf("diagnostic: seeded %v", ids)

	cityToml, _ := os.ReadFile(filepath.Join(city.dir, "city.toml"))
	t.Logf("diagnostic: city.toml:\n%s", string(cityToml))

	msOut, msErr := runGCWithEnv(city.env, city.dir, "maintenance", "status", "--json")
	t.Logf("diagnostic: `gc maintenance status --json` err=%v out=%s", msErr, msOut)

	hookOut, hookErr := runHookClaim(city, "polecat-diag")
	t.Logf("diagnostic: `gc hook --claim` err=%v\n--- output ---\n%s\n--- end ---", hookErr, hookOut)
}

// TestManagedDoltConnOracle_FastPathBoundsConnectionsUnderHookLoad is the AC-3
// oracle. It runs the same 200-process generated-default hook load twice against
// real managed-Dolt sql-servers capped at 32 connections — once with the claim
// fast path ON, once OFF (the feature-flag rollback path) — and proves the ON
// run bounds server connections and claims atomically while the OFF run
// accumulates connections. The A/B makes the ON bound non-vacuous: it shows the
// load genuinely stresses connections and that the fast path, not a quiet load,
// is what keeps them bounded.
func TestManagedDoltConnOracle_FastPathBoundsConnectionsUnderHookLoad(t *testing.T) {
	requireDoltIntegration(t)

	// Fast path ON: the cure. Worker hooks route claims through the controller;
	// the managed server must never see a per-worker connection storm.
	on := setupConnOracleCity(t, true)
	onSeed := seedRoutedPoolDemand(t, on, oracleSeedBeads)
	onBaseline := sampleThreadsConnected(t, on.observer)
	t.Logf("fastpath ON: baseline Threads_connected=%d, seeded=%d beads", onBaseline, len(onSeed))

	// Drain the pool under repeated 200-concurrent bursts. One SIMULTANEOUS burst of
	// 200 single-shot hooks against 80 beads does not claim every bead: the
	// controller's bounded claim admission (a design invariant, invariant 5) sheds
	// excess concurrent claims as a retryable-degraded result, so a fraction of hooks
	// drain without claiming while beads still remain. That is correct behavior, not
	// a cure defect — the pool drains over successive hook ticks. So run the genuine
	// 200-concurrent burst (oracleMaxInFlight == oracleHookProcs) in rounds until
	// every seeded bead is claimed, accumulate the claims, and hold the per-burst
	// connection/route discipline on EVERY round.
	const onMaxRounds = 8
	var (
		onClaims []hookClaim
		onPeak   int
		claimed  = make(map[string]bool, oracleSeedBeads)
	)
	for round := 1; ; round++ {
		// A per-round actor prefix makes every round's 200 actors globally distinct,
		// so a bead's winner is unique across all rounds (round-2 actors are new
		// identities, never re-adopting a round-1 claim) and the distinct-winner
		// evidence below stays exact.
		res := runHookLoad(t, on, oracleHookProcs, fmt.Sprintf("polecat-oracle-r%d", round))
		if res.peakConns > onPeak {
			onPeak = res.peakConns
		}
		// Healthy-controller semantics hold on EVERY round: the controller serves
		// every hook over the API with zero fallback and zero rejection, no matter how
		// many hooks drain empty in a late round. A loose "apiRoutes>0" bar would pass
		// a run where 199 hooks silently shelled out and one claimed — the storm the
		// cure removes.
		if res.apiRoutes != oracleHookProcs {
			t.Fatalf("fastpath ON round %d: %d/%d hooks took route=api; a healthy controller must serve every hook", round, res.apiRoutes, oracleHookProcs)
		}
		if res.fallbackRoutes != 0 {
			t.Fatalf("fastpath ON round %d: %d hook(s) fell back to the subprocess path against a healthy controller, want 0", round, res.fallbackRoutes)
		}
		if res.rejections != 0 {
			t.Fatalf("fastpath ON round %d: %d hook(s) saw a connection rejection / max-waiting-connections error, want 0", round, res.rejections)
		}
		onClaims = append(onClaims, res.claims...)
		for _, c := range res.claims {
			claimed[c.beadID] = true
		}
		t.Logf("fastpath ON round %d: peakConns=%d roundClaims=%d cumulativeClaimed=%d/%d apiRoutes=%d fallbackRoutes=%d rejections=%d",
			round, res.peakConns, len(res.claims), len(claimed), oracleSeedBeads, res.apiRoutes, res.fallbackRoutes, res.rejections)
		if len(claimed) >= oracleSeedBeads {
			break
		}
		if round >= onMaxRounds {
			t.Fatalf("fastpath ON: only %d/%d beads claimed after %d rounds of %d-concurrent hooks; claimable pool work is being stranded",
				len(claimed), oracleSeedBeads, onMaxRounds, oracleHookProcs)
		}
	}

	// Invariant 1 (atomic claim), across every round: no bead was handed to two
	// DISTINCT actors, and every recorded claim is by its intended distinct actor.
	// Holding the orphan reconciler (max_active_sessions=0) means a bead claimed in
	// an early round stays claimed, so a later round cannot legitimately re-hand it
	// to a different actor — any such collision would be a real double-claim.
	assertUniqueClaims(t, onClaims)
	onBeads, onActors, onWorstBead, onWorstDistinct := claimAssigneeStats(onClaims)
	if onWorstDistinct > 1 {
		t.Logf("fastpath ON: worst bead %s won by %d DISTINCT assignees (invariant-1 violation)", onWorstBead, onWorstDistinct)
	}
	// Completeness + exact distinct-winner evidence: every seeded bead ends up
	// claimed exactly once (claims == distinct beads == seed count), and because each
	// round uses a globally-distinct actor set, every bead's winner is a distinct
	// actor too (distinct assignees == seed count). No claimable pool work is
	// stranded, and no bead was double-claimed (assertUniqueClaims above).
	if len(onClaims) != oracleSeedBeads || onBeads != oracleSeedBeads || onActors != oracleSeedBeads {
		t.Fatalf("fastpath ON: claims=%d distinctBeads=%d distinctAssignees=%d across all rounds, want all %d seeded beads won exactly once by %d globally-distinct actors",
			len(onClaims), onBeads, onActors, oracleSeedBeads, oracleSeedBeads)
	}

	// Invariant 6 / 7: the bounded controller pools cap server connections far below
	// the listener limit across every round, and 200 worker processes per round never
	// move it.
	if onPeak >= oracleMaxConnections {
		t.Fatalf("fastpath ON: peak Threads_connected=%d reached the %d-connection listener cap under load", onPeak, oracleMaxConnections)
	}
	if onPeak > oracleFastPathConnCap {
		t.Fatalf("fastpath ON: peak Threads_connected=%d exceeds the bounded-pool cap %d; a pool is unbounded or the fast path leaked worker connections",
			onPeak, oracleFastPathConnCap)
	}

	// Return to baseline: with no worker sockets to reap, connections settle back
	// to the controller's steady pool count promptly.
	final := waitThreadsConnectedSettle(t, on.observer, onBaseline+2, 20*time.Second)
	if final > onBaseline+2 {
		t.Fatalf("fastpath ON: Threads_connected=%d did not return to baseline %d(+2) after the load drained", final, onBaseline)
	}

	// Fast path OFF (feature-flag rollback): the legacy subprocess adapter. Each
	// worker hook opens its own SQL client, so the identical load accumulates
	// server connections. This control proves the ON bound is real, not an
	// artifact of a load that never touched connections.
	off := setupConnOracleCity(t, false)
	seedRoutedPoolDemand(t, off, oracleSeedBeads)
	offBaseline := sampleThreadsConnected(t, off.observer)
	offResult := runHookLoad(t, off, oracleHookProcs, "polecat-oracle")
	t.Logf("fastpath OFF: baseline=%d peakConns=%d claims=%d (control)", offBaseline, offResult.peakConns, len(offResult.claims))

	// The OFF control must accumulate MATERIALLY more connections than the ON run —
	// not a one-connection edge a jittery sampler could produce without real
	// per-worker clients. With the fast path off, each of the 200 overlapping hooks
	// opens its own managed-Dolt client, so the OFF peak must climb above
	// oracleFastPathConnCap, the bound the ON run is required to stay under.
	// Separating OFF (> cap) from ON (<= cap) proves the load genuinely stresses
	// connections and that the fast path, not a quiet load, is what keeps them
	// bounded (mirrors the bounded/unbounded control in the store-level oracle).
	if offResult.peakConns <= onPeak {
		t.Fatalf("fastpath OFF control peaked at %d connections, not above the fastpath-ON peak %d; the load did not overlap, so the ON bound is vacuous",
			offResult.peakConns, onPeak)
	}
	if offResult.peakConns <= oracleFastPathConnCap {
		t.Fatalf("fastpath OFF control peaked at only %d connections, not above the bounded-pool cap %d the ON run stays under; the per-worker-client pressure the cure removes was not material",
			offResult.peakConns, oracleFastPathConnCap)
	}

	// Atomic claim (invariant 1) must hold on the rollback path too: disabling the
	// fast path changes the transport (per-hook bd subprocess instead of the
	// controller), never the claim semantics. With the orphan reconciler held (so a
	// synthetic actor's claim is not legitimately released and re-claimed) the
	// upstream ClaimIssue conditional-UPDATE CAS is safe under concurrency — proven
	// directly, lock-free, in TestNativeDoltStoreConcurrentClaimHasExactlyOneWinner
	// — so the OFF load claims each bead for exactly one distinct actor, exactly
	// like the ON path. (This assertion is the reason feature-flag rollback is safe:
	// it does not reintroduce a double-claim.)
	offBeads, offAssignees, offWorstBead, offWorstDistinct := claimAssigneeStats(offResult.claims)
	t.Logf("fastpath OFF control: %d claims, distinctBeads=%d distinctAssignees=%d worstBead=%s worstDistinct=%d",
		len(offResult.claims), offBeads, offAssignees, offWorstBead, offWorstDistinct)
	assertUniqueClaims(t, offResult.claims)
}

// claimAssigneeStats summarizes a claim set: the number of distinct beads won,
// the number of distinct assignees seen, and the bead won by the most DISTINCT
// assignees (with that count). A worstDistinct > 1 is an atomic-claim violation.
func claimAssigneeStats(claims []hookClaim) (distinctBeads, distinctAssignees int, worstBead string, worstDistinct int) {
	byBead := make(map[string]map[string]struct{}, len(claims))
	allAssignees := make(map[string]struct{})
	for _, c := range claims {
		allAssignees[c.assignee] = struct{}{}
		if byBead[c.beadID] == nil {
			byBead[c.beadID] = make(map[string]struct{})
		}
		byBead[c.beadID][c.assignee] = struct{}{}
	}
	for id, assignees := range byBead {
		if len(assignees) > worstDistinct {
			worstDistinct = len(assignees)
			worstBead = id
		}
	}
	return len(byBead), len(allAssignees), worstBead, worstDistinct
}

// hookClaim is one successful claim observed from a hook process: which bead it
// won, the assignee the controller recorded, and the distinct synthetic actor
// identity the hook was launched with. A true atomic-claim violation is one bead
// won by more than one DISTINCT assignee; the same assignee winning a bead more
// than once is a legitimate idempotent same-actor re-claim (ClaimIssue returns
// success for the current owner), not a violation.
type hookClaim struct {
	beadID   string
	assignee string
	actor    string
}

// hookLoadResult is the aggregate outcome of running the concurrent hook load.
type hookLoadResult struct {
	peakConns      int
	claims         []hookClaim
	apiRoutes      int
	fallbackRoutes int
	rejections     int
	saturated      int
}

// setupConnOracleCity initializes and starts a managed-Dolt city with the claim
// fast path set to fastpath, a max_connections=32 listener, and a generated-
// default pool worker, then returns it ready for load with a root observer open.
func setupConnOracleCity(t *testing.T, fastpath bool) *connOracleCity {
	return setupConnOracleCityWithReadTimeout(t, fastpath, 0)
}

// setupConnOracleCityWithReadTimeout is setupConnOracleCity with an explicit
// managed-Dolt listener read_timeout_millis (0 = the managed default). A large
// value proves the connection cure does not depend on the server promptly
// reaping idle client sockets.
func setupConnOracleCityWithReadTimeout(t *testing.T, fastpath bool, readTimeoutMillis int) *connOracleCity {
	t.Helper()

	env := newIsolatedCommandEnv(t, true)
	root, err := os.MkdirTemp("/tmp", "conn-oracle-*")
	if err != nil {
		t.Fatalf("mktemp conn-oracle root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	cityDir := filepath.Join(root, "c")
	feDir := filepath.Join(root, "fe")
	beDir := filepath.Join(root, "be")
	for _, d := range []string{feDir, beDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir rig dir %s: %v", d, err)
		}
	}

	// The pool worker carries max_active_sessions = 0 so it does NOT support
	// generic ephemeral sessions. That holds the bead reconciler's orphaned-pool
	// release: releaseOrphanedPoolAssignments only reopens pool-routed work for an
	// agent that SupportsGenericEphemeralSessions(), and the oracle's synthetic
	// claimants have no session beads, so without this every claim would be
	// legitimately released (assignee has no open session) and re-claimed by
	// another synthetic actor, manufacturing a claimed->released->claimed sequence
	// that looks like a double-claim but is not a concurrency violation. Holding
	// the release lets the oracle isolate atomic-claim behavior from pool lifecycle
	// recovery. The fast-path tier-3 pool-demand claim matches on gc.routed_to and
	// is unaffected by the capacity cap.
	configPath := filepath.Join(root, "conn-oracle.toml")
	config := fmt.Sprintf(`[workspace]
name = "conn-oracle"

[beads]
provider = "bd"
hook_claim_fastpath = %t

[dolt]
max_connections = %d
read_timeout_millis = %d

[session]
provider = "subprocess"

[daemon]
patrol_interval = "250ms"

[[rigs]]
name = "frontend"
path = %q
prefix = "fe"

[[rigs]]
name = "backend"
path = %q
prefix = "be"

[[agent]]
name = %q
start_command = "sleep 3600"
max_active_sessions = 0

[[agent]]
name = %q
work_query = "bd ready --json --limit=0"
start_command = "sleep 3600"
max_active_sessions = 0
`, fastpath, oracleMaxConnections, readTimeoutMillis, feDir, beDir, oraclePoolAgent, oracleCustomAgent)
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		t.Fatalf("write conn-oracle config: %v", err)
	}

	out, err := runGCDoltWithEnv(env, "", "init", "--skip-provider-readiness", "--file", configPath, cityDir)
	if err != nil {
		t.Fatalf("gc init conn-oracle city: %v\noutput: %s", err, out)
	}
	registerCityCommandEnv(cityDir, env)
	t.Cleanup(func() {
		unregisterCityCommandEnv(cityDir)
		runGCDoltWithEnv(env, "", "stop", cityDir)                //nolint:errcheck // best-effort cleanup
		runGCDoltWithEnv(env, "", "supervisor", "stop", "--wait") //nolint:errcheck // best-effort cleanup
	})

	if _, err := waitForManagedDoltCityReady(env, cityDir, 30*time.Second); err != nil {
		t.Fatalf("managed Dolt city never became ready: %v", err)
	}
	waitForControllerReady(t, cityDir, 30*time.Second)

	port, ok := currentManagedDoltPortForTest(cityDir)
	if !ok {
		t.Fatalf("managed Dolt port not resolvable after city ready")
	}

	observer, err := sql.Open("mysql", fmt.Sprintf("root@tcp(127.0.0.1:%s)/", port))
	if err != nil {
		t.Fatalf("open managed Dolt observer: %v", err)
	}
	// The observer is a single probe connection; keep it to one so it does not
	// itself perturb the count it measures.
	observer.SetMaxOpenConns(1)
	observer.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = observer.Close() })

	return &connOracleCity{dir: cityDir, env: env, port: port, observer: observer}
}

// seedRoutedPoolDemand creates n unassigned city-scoped beads routed to the pool
// worker, so the generated-default fast path's tier-3 pool-demand read surfaces
// them for the claim race. It returns the seeded bead IDs.
func seedRoutedPoolDemand(t *testing.T, city *connOracleCity, n int) []string {
	t.Helper()
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		title := fmt.Sprintf("conn-oracle-demand-%03d", i)
		out, err := runGCWithEnv(city.env, city.dir, "bd", "create", "--json", title)
		if err != nil {
			t.Fatalf("seed bead %q: %v\n%s", title, err, out)
		}
		id := beadIDFromJSON(out)
		if id == "" {
			t.Fatalf("seed bead %q: no id in create output:\n%s", title, out)
		}
		if _, err := runGCWithEnv(city.env, city.dir, "bd", "update", id, "--set-metadata", "gc.routed_to="+oraclePoolAgent); err != nil {
			t.Fatalf("route seed bead %s: %v", id, err)
		}
		ids = append(ids, id)
	}
	// Wait for the controller's federated ready cache to surface the seeded
	// demand before the race, so a cold cache does not read as no-work.
	deadline := time.Now().Add(20 * time.Second)
	for {
		out, err := runGCWithEnv(city.env, city.dir, "bd", "ready", "--json", "--limit=0")
		if err == nil && countRoutedReady(out, oraclePoolAgent) >= n {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("seeded routed demand did not become ready within 20s (last err=%v)", err)
		}
		time.Sleep(250 * time.Millisecond)
	}
	return ids
}

// runHookLoad launches procs concurrent generated-default `gc hook --claim`
// processes, each a distinct ephemeral actor (actorPrefix-NNN) racing for the
// routed pool demand, while sampling the managed server's peak Threads_connected
// throughout. actorPrefix namespaces the actors so a multi-round drain gives every
// round a globally-distinct actor set — a bead's winner is unique across all
// rounds, so distinct-winner evidence stays exact.
func runHookLoad(t *testing.T, city *connOracleCity, procs int, actorPrefix string) hookLoadResult {
	t.Helper()

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
		mu         sync.Mutex
		claims     []hookClaim
		apiRoutes  int
		fallbacks  int
		rejections int
		saturated  int
	)
	sem := make(chan struct{}, oracleMaxInFlight)
	var wg sync.WaitGroup
	for i := 0; i < procs; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()

			actor := fmt.Sprintf("%s-%03d", actorPrefix, i)
			out, _ := runHookClaim(city, actor)

			mu.Lock()
			defer mu.Unlock()
			if strings.Contains(out, "route=api") {
				apiRoutes++
			}
			if strings.Contains(out, "route=fallback") {
				fallbacks++
			}
			if isConnectionRejection(out) {
				rejections++
			}
			if strings.Contains(strings.ToLower(out), "admission saturated") {
				saturated++
			}
			if id, assignee := claimedBeadAndAssignee(out); id != "" {
				claims = append(claims, hookClaim{beadID: id, assignee: assignee, actor: actor})
			}
		}(i)
	}
	wg.Wait()
	close(stop)
	sampler.Wait()

	return hookLoadResult{
		peakConns:      int(atomic.LoadInt64(&peak)),
		claims:         claims,
		apiRoutes:      apiRoutes,
		fallbackRoutes: fallbacks,
		rejections:     rejections,
		saturated:      saturated,
	}
}

// hookActorEnv builds the environment for one generated-default pool-worker hook
// acting as a distinct ephemeral claimant. The hook is invoked with NO positional
// agent (see runHookClaim): identity comes from GC_TEMPLATE + GC_SESSION_NAME,
// which is what makes gc hook treat this as a session-template (pool-worker)
// invocation and honor the per-actor identity — a bare `gc hook --claim <agent>`
// ignores the env identity and claims as the canonical agent name, collapsing
// every actor into one.
//
// It sets NO GC_SESSION_ID and strips any inherited GC_INSTANCE_TOKEN so the
// stale-session identity fence (which drains a hook whose session bead does not
// exist) does not fire: these synthetic actors have no session bead. The distinct
// claim assignee comes from GC_SESSION_NAME; GC_SESSION_ORIGIN=ephemeral makes the
// actor eligible for routed pool demand (tier 3), GC_TEMPLATE names the pool route
// the seeded demand targets, and GC_DEBUG=1 turns on the route=api/route=fallback
// diagnostics the oracle asserts on to prove the fast path is actually taken.
func hookActorEnv(city *connOracleCity, actor string) []string {
	env := filterEnvMany(commandEnvForDir(city.dir, true),
		"GC_SESSION_ID", "GC_INSTANCE_TOKEN", "GC_SESSION_NAME", "GC_ALIAS", "GC_SESSION_ORIGIN", "GC_TEMPLATE", "GC_AGENT", "GC_DEBUG")
	return append(env,
		"GC_SESSION_NAME="+actor,
		"GC_SESSION_ORIGIN=ephemeral",
		"GC_TEMPLATE="+oraclePoolAgent,
		"GC_DEBUG=1",
	)
}

// runHookClaim runs one generated-default pool-worker hook claim as actor,
// returning the combined output. The agent is resolved from GC_TEMPLATE in the
// environment (no positional argument) so gc hook takes the session-template
// identity path.
func runHookClaim(city *connOracleCity, actor string) (string, error) {
	return runGCWithEnv(hookActorEnv(city, actor), city.dir, "hook", "--claim", "--json")
}

// assertUniqueClaims fails if any bead was won by more than one DISTINCT assignee
// — the direct expression of invariant 1 (two different workers cannot both
// acquire one bead). The same assignee winning a bead more than once is a
// legitimate idempotent same-actor re-claim and is NOT a violation. It also
// cross-checks that each hook's recorded assignee matches the distinct synthetic
// actor it was launched as, so a harness that silently collapsed identities
// (making every "claim" the same actor) cannot masquerade as a passing uniqueness
// proof.
func assertUniqueClaims(t *testing.T, claims []hookClaim) {
	t.Helper()
	byBead := make(map[string]map[string]struct{}, len(claims))
	identityMismatch := 0
	for _, c := range claims {
		if c.assignee != c.actor {
			identityMismatch++
		}
		if byBead[c.beadID] == nil {
			byBead[c.beadID] = make(map[string]struct{})
		}
		byBead[c.beadID][c.assignee] = struct{}{}
	}
	for id, assignees := range byBead {
		if len(assignees) > 1 {
			names := make([]string, 0, len(assignees))
			for a := range assignees {
				names = append(names, a)
			}
			t.Fatalf("bead %s was won by %d DISTINCT assignees %v; atomic claim broken", id, len(assignees), names)
		}
	}
	// Exact identity: every accepted reason=claimed result must record the
	// assignee that hook was launched as. A single mismatch means a loser or a
	// current-owner response was counted as a fresh win, which could mask a real
	// collision — so fail on ANY mismatch, not a majority.
	if identityMismatch != 0 {
		var tuples []string
		for _, c := range claims {
			if c.assignee != c.actor {
				tuples = append(tuples, fmt.Sprintf("{bead:%s assignee:%s actor:%s}", c.beadID, c.assignee, c.actor))
			}
		}
		t.Fatalf("%d/%d claims had assignee != intended actor: %v; a mismatched claim would hide a collision, so the uniqueness proof is not trustworthy",
			identityMismatch, len(claims), tuples)
	}
}

// sampleThreadsConnected reads SHOW STATUS LIKE 'Threads_connected' and fails the
// test on error; use it for baseline reads where a failure is fatal.
func sampleThreadsConnected(t *testing.T, db *sql.DB) int {
	t.Helper()
	var name string
	var value int
	row := db.QueryRow("SHOW STATUS LIKE 'Threads_connected'")
	if err := row.Scan(&name, &value); err != nil {
		t.Fatalf("SHOW STATUS Threads_connected: %v", err)
	}
	return value
}

// probeThreadsConnected is the best-effort variant used by the peak sampler; a
// transient probe error yields 0 rather than failing the concurrent load.
func probeThreadsConnected(db *sql.DB) int {
	var name string
	var value int
	if err := db.QueryRow("SHOW STATUS LIKE 'Threads_connected'").Scan(&name, &value); err != nil {
		return 0
	}
	return value
}

// waitThreadsConnectedSettle polls until Threads_connected drops to at most
// target or the timeout elapses, returning the last observed value.
func waitThreadsConnectedSettle(t *testing.T, db *sql.DB, target int, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := probeThreadsConnected(db)
	for time.Now().Before(deadline) {
		last = probeThreadsConnected(db)
		if last <= target {
			return last
		}
		time.Sleep(250 * time.Millisecond)
	}
	return last
}

// isConnectionRejection reports whether hook output shows a managed-Dolt
// connection admission failure — the accumulation symptom the cure removes.
func isConnectionRejection(out string) bool {
	msg := strings.ToLower(out)
	for _, marker := range []string{
		"max waiting connections",
		"too many connections",
		"max_connections",
		"connection limit",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// claimedBeadAndAssignee returns the bead id a `gc hook --claim` process FRESHLY
// claimed (reason "claimed") and the assignee the controller recorded, or ("","")
// for a no-work drain, a lost race, an already-owned re-adoption, or an error. It
// is deliberately strict on the fresh-claim reason so the fresh-claim uniqueness
// oracle cannot be satisfied by an idempotent re-adoption; tier/idempotency
// assertions that expect a re-adoption use heldBeadAndAssignee instead.
func claimedBeadAndAssignee(out string) (string, string) {
	return hookResultBead(out, false)
}

// heldBeadAndAssignee returns the bead a hook holds after running, counting a
// fresh claim (reason "claimed"), the claim of already-assigned-but-open work
// (reason "ready_assignment", tier 2), and re-adoption of already-owned
// in_progress work (reason "existing_assignment", the crash-recovery /
// idempotent-retry tier 1). Use it for tier/idempotency assertions where the win
// is not a fresh pool claim; never for the fresh-claim uniqueness oracle.
func heldBeadAndAssignee(out string) (string, string) {
	return hookResultBead(out, true)
}

// hookResultBead parses a hook claim result. When acceptAssigned is false only a
// fresh reason "claimed" counts; when true, the assigned-work wins
// "ready_assignment" and "existing_assignment" also count.
func hookResultBead(out string, acceptAssigned bool) (string, string) {
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] != '{' {
			continue
		}
		var res struct {
			OK       bool   `json:"ok"`
			Action   string `json:"action"`
			Reason   string `json:"reason"`
			BeadID   string `json:"bead_id"`
			Assignee string `json:"assignee"`
		}
		if err := json.Unmarshal([]byte(line), &res); err != nil {
			continue
		}
		if !res.OK || res.BeadID == "" {
			continue
		}
		if res.Reason == "claimed" {
			return res.BeadID, res.Assignee
		}
		if acceptAssigned && (res.Reason == "ready_assignment" || res.Reason == "existing_assignment") {
			return res.BeadID, res.Assignee
		}
	}
	return "", ""
}

// beadIDFromJSON extracts the first bead "id" from gc/bd JSON output (a single
// object or an array), returning "" when the output carries no bead.
func beadIDFromJSON(out string) string {
	out = strings.TrimSpace(out)
	if out == "" {
		return ""
	}
	// Isolate the JSON payload: gc may interleave route= log lines on the same
	// stream, so scan lines for the first that parses as a bead object or array.
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || (line[0] != '{' && line[0] != '[') {
			continue
		}
		if id := parseBeadID(line); id != "" {
			return id
		}
	}
	return parseBeadID(out)
}

func parseBeadID(s string) string {
	var obj struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(s), &obj); err == nil && obj.ID != "" {
		return obj.ID
	}
	var arr []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(s), &arr); err == nil {
		for _, o := range arr {
			if o.ID != "" {
				return o.ID
			}
		}
	}
	return ""
}

// countRoutedReady counts beads in `bd ready --json` output routed to target.
func countRoutedReady(out, target string) int {
	out = strings.TrimSpace(out)
	start := strings.IndexAny(out, "[{")
	if start < 0 {
		return 0
	}
	var arr []struct {
		Metadata map[string]string `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(out[start:]), &arr); err != nil {
		return 0
	}
	n := 0
	for _, b := range arr {
		if strings.TrimSpace(b.Metadata["gc.routed_to"]) == target {
			n++
		}
	}
	return n
}
