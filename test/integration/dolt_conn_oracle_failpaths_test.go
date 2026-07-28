//go:build integration && conn_oracle

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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
	// Synthetic oracle actors have no open session bead, so the normal assign
	// mutation rejects them. The atomic claim endpoint is the production path
	// that authoritatively binds such a hook actor and updates the controller
	// cache. Reopen with a status-only update for the assigned-ready tier.
	id := createOracleBead(t, city, title, "", "", nil)
	claimedBead, claimed, err := city.readyAPI.ClaimBead(context.Background(), id, assignee)
	if err != nil {
		t.Fatalf("claim assigned fixture %s as %s: %v", id, assignee, err)
	}
	if !claimed || claimedBead.ID != id || claimedBead.Assignee != assignee {
		t.Fatalf("claim assigned fixture %s as %s: claimed=%t bead=%+v", id, assignee, claimed, claimedBead)
	}
	if !inProgress {
		setOracleBeadStatus(t, city, id, "open")
	}
	waitAssignedOracleBeadCached(t, city, id, assignee, inProgress)
	return id
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

// TestManagedDoltConnOracle_APIUnavailableFailsClosed proves the managed-Dolt
// failure boundary: when a generated-default worker hook cannot use the
// controller before its request (GC_NO_API), it leaves routed demand queued and
// exits terminally. It must never open an independent BdStore working set,
// because separately opened Dolt working sets can overwrite another actor's
// claim even when hook processes are serialized.
func TestManagedDoltConnOracle_APIUnavailableFailsClosed(t *testing.T) {
	requireDoltIntegration(t)
	city := setupConnOracleCity(t, true)

	const n = 12
	ids := seedRoutedPoolDemand(t, city, n)
	actors := make([]string, n)
	for i := 0; i < n; i++ {
		actors[i] = fmt.Sprintf("fail-closed-actor-%02d", i)
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
		mu              sync.Mutex
		routeFailClosed int
		routeFallback   int
		routeAPI        int
		rejections      int
		unexpectedOK    int
		freshClaims     []hookClaim
		hookOutputs     = make(map[string]string, n)
	)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(actor string) {
			defer wg.Done()
			// GC_NO_API makes the controller unavailable to this hook before the
			// request. GC_DEBUG (from hookActorEnv) turns on route diagnostics.
			env := append(hookActorEnv(city, actor), "GC_NO_API=1")
			out, err := runGCWithEnv(env, city.dir, "hook", "--claim", "--json")
			id, asg := claimedBeadAndAssignee(out)
			mu.Lock()
			defer mu.Unlock()
			hookOutputs[actor] = out
			if strings.Contains(out, "route=fail-closed reason=controller-unavailable") {
				routeFailClosed++
			}
			if strings.Contains(out, "route=fallback") {
				routeFallback++
			}
			if strings.Contains(out, "route=api") {
				routeAPI++
			}
			if isConnectionRejection(out) {
				rejections++
			}
			if err == nil {
				unexpectedOK++
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
	t.Logf("API-unavailable fail-closed: freshClaims=%d route=fail-closed=%d route=fallback=%d route=api=%d unexpectedOK=%d rejections=%d peakConns=%d",
		len(freshClaims), routeFailClosed, routeFallback, routeAPI, unexpectedOK, rejections, peakConns)

	if routeFailClosed != n {
		for _, actor := range actors {
			t.Logf("fail-closed detail: actor=%s output=%q", actor, hookOutputs[actor])
		}
		t.Fatalf("only %d/%d hooks logged route=fail-closed; controller loss was not uniformly terminal", routeFailClosed, n)
	}
	if routeAPI != 0 {
		t.Fatalf("%d hook(s) took route=api despite the controller being unavailable", routeAPI)
	}
	if routeFallback != 0 {
		t.Fatalf("%d hook(s) opened the forbidden subprocess fallback", routeFallback)
	}
	if unexpectedOK != 0 {
		t.Fatalf("%d hook(s) exited successfully despite controller unavailability", unexpectedOK)
	}
	if len(freshClaims) != 0 {
		t.Fatalf("%d hook(s) reported a fresh claim while controller routing was disabled: %+v", len(freshClaims), freshClaims)
	}
	for _, id := range ids {
		read, err := city.readyAPI.GetBead(id)
		if err != nil {
			t.Fatalf("read fail-closed bead %s through controller: %v", id, err)
		}
		if read.Body.Assignee != "" || read.Body.Status != "open" {
			t.Fatalf("bead %s changed during fail-closed controller loss: status=%q assignee=%q", id, read.Body.Status, read.Body.Assignee)
		}
	}
	if rejections != 0 {
		t.Fatalf("%d fail-closed hook(s) saw a connection rejection", rejections)
	}
	if peakConns >= oracleMaxConnections {
		t.Fatalf("fail-closed controller outage peaked at %d connections, reaching the %d cap", peakConns, oracleMaxConnections)
	}
}

// TestManagedDoltConnOracle_KilledClientsReturnToBaseline kills a controlled
// subset of hook clients both before and after they submit their request, and
// asserts the managed server's connection count stays bounded during the load and
// returns to baseline after — the core cure property: a worker hook holds no
// managed-Dolt socket, so a SIGKILLed worker strands nothing on the server.
//
// Scope: this proves the CONNECTION property only (bounded peak + return to
// baseline under a mixed kill cohort). Killed clients are counted only when
// Process.Kill() actually succeeds, so a kill that raced the process to a natural
// exit does not inflate the count. The per-cohort commit-durability observable —
// that an after-submit killed client's claim commits and stays owned server-side
// — is proven deterministically in
// TestManagedDoltConnOracle_KilledAfterSubmitCommitIsDurable, not here: the ~40
// always-run i%3==2 clients in this load would leave routed beads claimed
// regardless of the killed cohort, so a claim-count assertion here would be
// vacuous.
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
	var killedBefore, killedAfter int64
	sem := make(chan struct{}, oracleMaxInFlight)
	for i := 0; i < procs; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			actor := fmt.Sprintf("killable-%03d", i)
			cmd := integrationTestCommand(gcBinary, "hook", "--claim", "--json")
			cmd.Dir = city.dir
			cmd.Env = hookActorEnv(city, actor)
			if err := cmd.Start(); err != nil {
				return
			}
			// Kill every third hook: half "before request" (immediately) and half
			// "after request" (a brief delay lets it reach/submit the claim). The
			// rest run to completion. A kill is counted only when Kill() reports no
			// error, so a process that already exited on its own is not miscounted
			// as a killed client.
			switch i % 3 {
			case 0: // before request submission
				if err := cmd.Process.Kill(); err == nil {
					atomic.AddInt64(&killedBefore, 1)
				}
				_, _ = cmd.Process.Wait()
				return
			case 1: // after request submission
				integrationTestPollPause(15 * time.Millisecond)
				if err := cmd.Process.Kill(); err == nil {
					atomic.AddInt64(&killedAfter, 1)
				}
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
	kBefore, kAfter := atomic.LoadInt64(&killedBefore), atomic.LoadInt64(&killedAfter)
	t.Logf("killed clients: before=%d after=%d peakConns=%d baseline=%d", kBefore, kAfter, peakConns, baseline)
	if kBefore == 0 || kAfter == 0 {
		t.Fatalf("kill cohorts not both exercised: before=%d after=%d", kBefore, kAfter)
	}
	if peakConns >= oracleMaxConnections {
		t.Fatalf("killed-client load peaked at %d connections, reaching the %d cap", peakConns, oracleMaxConnections)
	}
	if peakConns > oracleFastPathConnCap {
		t.Fatalf("killed-client load peaked at %d connections, above the bounded-pool cap %d; a killed worker leaked a server connection", peakConns, oracleFastPathConnCap)
	}
	// This test proves the CONNECTION property only: killed clients strand no
	// server socket, so the peak stays bounded and settles to baseline. The
	// per-cohort commit-durability observable — that an after-submit killed
	// client's claim commits server-side — is proven deterministically in
	// TestManagedDoltConnOracle_KilledAfterSubmitCommitIsDurable, not here (where
	// the ~40 always-run i%3==2 clients would guarantee a claim regardless of the
	// killed cohort, making a claim-count assertion vacuous).
	final := waitThreadsConnectedSettle(t, city.observer, baseline+2, 20*time.Second)
	if final > baseline+2 {
		t.Fatalf("connections did not return to baseline after killed clients: %d > baseline %d(+2)", final, baseline)
	}
}

// wrappedPathEnv returns base with prependDir placed at the front of PATH, so a
// subprocess launched with it resolves an intercepting binary there before the
// harness tool bin. It rewrites the existing PATH entry in place (rather than
// appending a duplicate, whose precedence is exec-implementation-defined).
func wrappedPathEnv(base []string, prependDir string) []string {
	out := make([]string, 0, len(base))
	replaced := false
	for _, kv := range base {
		if strings.HasPrefix(kv, "PATH=") {
			out = append(out, "PATH="+prependDir+string(os.PathListSeparator)+strings.TrimPrefix(kv, "PATH="))
			replaced = true
			continue
		}
		out = append(out, kv)
	}
	if !replaced {
		out = append(out, "PATH="+prependDir)
	}
	return out
}

// waitForFile blocks until path exists or timeout elapses, failing the test on
// timeout. It is the deterministic liveness barrier for the killed-after test:
// the wrapper touches the marker immediately before sleeping, so its appearance
// means the hook is blocked (hence alive) at the continuation-list read.
func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("marker %s did not appear within %s: the hook never reached the continuation-list read (the claim may not have taken the fast path, or the bead lacked root/group metadata)", path, timeout)
		}
		integrationTestPollPause(50 * time.Millisecond)
	}
}

// TestManagedDoltConnOracle_KilledAfterSubmitCommitIsDurable is the deterministic
// killed-after-submit observable the locked acceptance requires. The earlier form
// polled until the claim committed and then killed the client — but by then the
// hook had usually already exited on its own, so Kill() operated on a dead
// process and the "killed AFTER submit" property was never actually exercised
// (finding 4: a successful kill of an already-exited process proves nothing).
//
// This form makes the kill land on a provably LIVE client. The claimed bead
// carries continuation root+group metadata, so after the fast-path claim commits
// server-side the hook proceeds into the post-claim continuation-list read. A
// PATH-wrapped bd intercepts exactly that read (matched by the unique group
// sentinel) and PARKS there — touching a "reached" marker, then waiting for the
// test's "release" marker before touching "exited" and returning; every other bd
// call forwards to the harness bd untouched. When "reached" appears the hook is
// parked in that read — alive, with its claim already committed — so the kill is
// attributable to a live post-submit client and Kill() MUST return nil. The
// committed claim then outlives the killed worker because the worker held no
// managed-Dolt socket: a worker that owned its SQL client could have stranded or
// rolled back the mutation on SIGKILL. The release/exited handshake makes cleanup
// deterministic: killing the gc parent orphans the wrapper child, so the test
// releases it and waits for "exited" rather than leaving a timed sleep to linger.
func TestManagedDoltConnOracle_KilledAfterSubmitCommitIsDurable(t *testing.T) {
	requireDoltIntegration(t)
	city := setupConnOracleCity(t, true)

	// One routed pool bead carrying continuation root+group metadata: one bead and
	// one hook make the claim target unambiguous and attributable to this exact
	// actor, and the group metadata is what drives the post-claim continuation-list
	// read the wrapper parks on.
	const (
		actor    = "killed-after-actor"
		sentinel = "connoraclekillwrapgroup"
		root     = "connoraclekillwraproot"
	)
	want := seedRoutedBeadWithMeta(t, city, "killed-after-step", map[string]string{
		beadmetaRootKey:  root,
		beadmetaGroupKey: sentinel,
	})
	waitRoutedReadyCount(t, city, 1)

	// Continuation-list PATH wrapper: park ONLY on the continuation-list read for
	// our sentinel group (issued after the claim commits), forwarding every other bd
	// call to the harness bd so identity stamping and the claim path are unchanged.
	// The park waits for the release marker (bounded 60s backstop so a crashed test
	// cannot orphan this child) and writes the exited marker on the way out.
	wrapDir := t.TempDir()
	reached := filepath.Join(wrapDir, "continuation-reached")
	release := filepath.Join(wrapDir, "release")
	exited := filepath.Join(wrapDir, "wrapper-exited")
	shim := fmt.Sprintf(`#!/bin/sh
for a in "$@"; do
  case "$a" in
    *%s*)
      : > %q
      i=0
      while [ ! -f %q ] && [ "$i" -lt 600 ]; do
        sleep 0.1
        i=$((i + 1))
      done
      : > %q
      exit 0
      ;;
  esac
done
exec %q "$@"
`, sentinel, reached, release, exited, bdBinary)
	if err := os.WriteFile(filepath.Join(wrapDir, "bd"), []byte(shim), 0o755); err != nil {
		t.Fatalf("write continuation-list bd wrapper: %v", err)
	}

	cmd := integrationTestCommand(gcBinary, "hook", "--claim", "--json")
	cmd.Dir = city.dir
	cmd.Env = wrappedPathEnv(hookActorEnv(city, actor), wrapDir)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start hook client: %v", err)
	}
	// Safety net: if any assertion below fails before the explicit release, still
	// release a parked wrapper and wait for it to exit, so a failed run never leaves
	// the wrapper child lingering for its 60s backstop. Idempotent with the
	// normal-path release/wait below (writing release twice is harmless).
	t.Cleanup(func() {
		if _, err := os.Stat(reached); err != nil {
			return // wrapper never parked; nothing to release
		}
		_ = os.WriteFile(release, nil, 0o644)
		deadline := time.Now().Add(30 * time.Second)
		for {
			if _, err := os.Stat(exited); err == nil {
				return
			}
			if time.Now().After(deadline) {
				// A parked wrapper that never wrote its exited marker is a lingering
				// child — the no-orphan gate is not satisfied, so fail loudly rather
				// than let a silent timeout pass.
				t.Errorf("wrapper exited marker %s not written within 30s after release; the wrapper child may be lingering", exited)
				return
			}
			integrationTestPollPause(50 * time.Millisecond)
		}
	})

	// Barrier: the hook is parked in the wrapper at the continuation-list read, which
	// runs only after the claim commits — so the claim is durable server-side
	// already. Assert that before the kill so "committed" is proven.
	waitForFile(t, reached, 30*time.Second)
	if owner := beadAssignee(t, city, want); owner != actor {
		t.Fatalf("before kill, bead %s owned by %q, want %s: the claim did not commit before the continuation-list read", want, owner, actor)
	}

	// Kill the still-parked (hence provably live) client. Kill() MUST return nil: a
	// process parked in the wrapper cannot have already exited, so a non-nil error
	// would mean the barrier attributed the commit to a client that was no longer
	// running — the exact liveness gap finding 4 rejected.
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("killing the parked hook client returned %v, want nil: the after-submit kill was not attributable to a live client", err)
	}
	_, _ = cmd.Process.Wait()

	// Deterministic cleanup: killing the gc parent orphaned the wrapper child, so
	// release it and wait for its exited marker — the test leaves no lingering
	// wrapper process.
	if err := os.WriteFile(release, nil, 0o644); err != nil {
		t.Fatalf("write wrapper release marker: %v", err)
	}
	waitForFile(t, exited, 30*time.Second)

	// The committed claim outlives the killed client and is still owned by the exact
	// killed actor: the controller-owned commit does not depend on the worker.
	if owner := beadAssignee(t, city, want); owner != actor {
		t.Fatalf("after killing the submitting client, bead %s owned by %q, want %s: the committed claim was not durable", want, owner, actor)
	}
}

// seedRigRoutedDemand creates one unassigned bead in the named RIG store routed
// to the pool worker and waits for it to become ready in that rig's scope, so the
// rig-scoped fast path's tier-3 read surfaces it. It returns the seeded bead ID.
func seedRigRoutedDemand(t *testing.T, city *connOracleCity, rig, title string) string {
	t.Helper()
	id := createOracleBead(t, city, title, rig, "",
		map[string]string{"gc.routed_to": oraclePoolAgent})
	waitRoutedPoolCache(t, city, rig, 1)
	return id
}

// rigScopedHookActorEnv builds the env for a RIG-SCOPED pool-worker hook: its
// GC_ALIAS carries the "<rig>/<name>" identity, which drives the fast path's
// store scope order to own-rig-first-then-city (hookFastPathScopeOrder). The
// claim assignee is still the distinct GC_SESSION_NAME, and GC_TEMPLATE resolves
// the generated-default pool agent so the hook takes the fast path.
func rigScopedHookActorEnv(city *connOracleCity, rig, actor string) []string {
	env := filterEnvMany(commandEnvForDir(city.dir, true),
		"GC_SESSION_ID", "GC_INSTANCE_TOKEN", "GC_SESSION_NAME", "GC_ALIAS", "GC_SESSION_ORIGIN", "GC_TEMPLATE", "GC_AGENT", "GC_DEBUG")
	return append(env,
		"GC_SESSION_NAME="+actor,
		"GC_ALIAS="+rig+"/"+actor,
		"GC_SESSION_ORIGIN=ephemeral",
		"GC_TEMPLATE="+oraclePoolAgent,
		"GC_DEBUG=1",
	)
}

// TestManagedDoltConnOracle_RigScopePrecedenceOwnRigBeatsCity is the end-to-end
// invariant-2 proof over real managed Dolt across multiple rig stores: a
// rig-scoped agent with an assigned-ready bead waiting in the CITY store and
// routed pool demand waiting in its OWN rig store claims the rig work first. The
// legacy firstStoreWithWork reads the rig store before the city store, so the
// rig's tier-3 routed work outranks the city's tier-2 assigned work; a federated
// (tier-outermost) read would invert this and hand the agent city work instead.
func TestManagedDoltConnOracle_RigScopePrecedenceOwnRigBeatsCity(t *testing.T) {
	requireDoltIntegration(t)
	city := setupConnOracleCity(t, true)

	const actor = "fe-precedence-actor"
	// City store: an assigned-ready bead the agent owns (tier 2, city scope).
	cityID := seedAssignedBead(t, city, "city-assigned-ready", actor, false)
	// Own rig store (frontend): routed pool demand (tier 3, rig scope).
	rigID := seedRigRoutedDemand(t, city, "frontend", "frontend-routed-demand")
	// A second rig (backend) also holds routed demand, proving >=2 rigs are seeded
	// and that the rig-scoped agent does not reach into a rig that is not its own.
	backendID := seedRigRoutedDemand(t, city, "backend", "backend-routed-demand")

	out, _ := runGCWithEnv(rigScopedHookActorEnv(city, "frontend", actor), city.dir, "hook", "--claim", "--json")
	assertRouteAPI(t, "rig-scope precedence", out)
	claimedID, asg := heldBeadAndAssignee(out)
	if claimedID != rigID || asg != actor {
		t.Fatalf("rig-scope precedence: claimed (%s,%s), want own-rig routed (%s,%s); city assigned %s must not win, backend %s must not be reached\n%s",
			claimedID, asg, rigID, actor, cityID, backendID, out)
	}
}

// TestManagedDoltConnOracle_ContinuationGroupPreassigned proves the fast-path
// claim still pre-assigns continuation-group siblings: claiming a routed bead
// that carries gc.root_bead_id + gc.continuation_group assigns its open,
// route-matching sibling in the same group to the same actor, so a multi-step
// continuation stays with one worker. The fast path leaves continuation
// list/assign on the bd defaults by design, so this exercises that sub-step end
// to end through a fast-path claim.
func TestManagedDoltConnOracle_ContinuationGroupPreassigned(t *testing.T) {
	requireDoltIntegration(t)
	city := setupConnOracleCity(t, true)

	const root = "gc-cont-root"
	const group = "conn-oracle-cont"
	a := seedRoutedBeadWithMeta(t, city, "continuation-step-a", map[string]string{
		beadmetaRootKey:  root,
		beadmetaGroupKey: group,
	})
	b := seedRoutedBeadWithMeta(t, city, "continuation-step-b", map[string]string{
		beadmetaRootKey:  root,
		beadmetaGroupKey: group,
	})
	waitRoutedReadyCount(t, city, 2)

	out, _ := runGCWithEnv(hookActorEnv(city, "continuation-actor"), city.dir, "hook", "--claim", "--json")
	assertRouteAPI(t, "continuation", out)

	claimedID, asg := heldBeadAndAssignee(out)
	if asg != "continuation-actor" || (claimedID != a && claimedID != b) {
		t.Fatalf("continuation: claimed (%s,%s), want one of (%s,%s) by continuation-actor\n%s", claimedID, asg, a, b, out)
	}
	// The sibling not claimed must have been pre-assigned to the same actor.
	sibling := a
	if claimedID == a {
		sibling = b
	}
	if !continuationAssignedContains(out, sibling) {
		t.Fatalf("continuation: sibling %s not in continuation_assigned after claiming %s\n%s", sibling, claimedID, out)
	}
	if owner := beadAssignee(t, city, sibling); owner != "continuation-actor" {
		t.Fatalf("continuation: sibling %s owned by %q, want continuation-actor", sibling, owner)
	}
}

// TestManagedDoltConnOracle_CustomWorkQueryRoutesCustomShell proves invariant 3's
// observability: an agent with an explicit work_query never takes the fast path
// and logs route=custom-shell, so the custom-query lane is visible rather than
// silently indistinguishable from a controller outage fallback.
func TestManagedDoltConnOracle_CustomWorkQueryRoutesCustomShell(t *testing.T) {
	requireDoltIntegration(t)
	city := setupConnOracleCity(t, true)

	env := filterEnvMany(commandEnvForDir(city.dir, true),
		"GC_SESSION_ID", "GC_INSTANCE_TOKEN", "GC_SESSION_NAME", "GC_ALIAS", "GC_SESSION_ORIGIN", "GC_TEMPLATE", "GC_AGENT", "GC_DEBUG")
	env = append(env,
		"GC_SESSION_NAME=custom-actor",
		"GC_SESSION_ORIGIN=ephemeral",
		"GC_TEMPLATE="+oracleCustomAgent,
		"GC_DEBUG=1",
	)
	out, _ := runGCWithEnv(env, city.dir, "hook", "--claim", "--json")
	if strings.Contains(out, "route=api") {
		t.Fatalf("custom work_query took the fast path (route=api); invariant 3 violated:\n%s", out)
	}
	if !strings.Contains(out, "route=custom-shell") {
		t.Fatalf("custom work_query hook did not log route=custom-shell:\n%s", out)
	}
}

// metadata keys duplicated as test constants so the oracle does not import the
// production beadmeta package (the integration test binary stays decoupled from
// internal packages); their values are pinned by beadmeta.keys.go.
const (
	beadmetaRootKey  = "gc.root_bead_id"
	beadmetaGroupKey = "gc.continuation_group"
)

// seedRoutedBeadWithMeta creates one city-scoped bead routed to the pool worker
// with additional metadata (e.g. continuation root/group) and returns its ID.
func seedRoutedBeadWithMeta(t *testing.T, city *connOracleCity, title string, meta map[string]string) string {
	t.Helper()
	allMeta := map[string]string{"gc.routed_to": oraclePoolAgent}
	for k, v := range meta {
		allMeta[k] = v
	}
	return createOracleBead(t, city, title, "", "", allMeta)
}

// waitRoutedReadyCount polls until at least n routed pool beads are ready to the
// controller, so a race started immediately after does not read a cold cache.
func waitRoutedReadyCount(t *testing.T, city *connOracleCity, n int) {
	t.Helper()
	waitRoutedPoolCache(t, city, city.cityName, n)
}

// continuationAssignedContains reports whether the hook JSON's
// continuation_assigned list includes id.
func continuationAssignedContains(out, id string) bool {
	var res struct {
		ContinuationAssigned []string `json:"continuation_assigned"`
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		if err := json.Unmarshal([]byte(line), &res); err != nil {
			continue
		}
		for _, a := range res.ContinuationAssigned {
			if a == id {
				return true
			}
		}
	}
	return false
}

// beadAssignee returns the current assignee of a bead. bd show --json emits a
// single-bead array, which may be pretty-printed across many lines, so this
// decodes the whole JSON value starting at the first bracket (a json.Decoder
// stops after one value, tolerating trailing log lines) rather than scanning
// line by line.
func beadAssignee(t *testing.T, city *connOracleCity, id string) string {
	t.Helper()
	out, err := runGCWithEnv(city.env, city.dir, "bd", "show", id, "--json")
	if err != nil {
		t.Fatalf("show bead %s: %v\n%s", id, err, out)
	}
	start := strings.IndexAny(out, "[{")
	if start < 0 {
		t.Fatalf("show bead %s: no JSON in output:\n%s", id, out)
	}
	payload := out[start:]
	var arr []struct {
		Assignee string `json:"assignee"`
	}
	if err := json.NewDecoder(strings.NewReader(payload)).Decode(&arr); err == nil && len(arr) > 0 {
		return strings.TrimSpace(arr[0].Assignee)
	}
	var one struct {
		Assignee string `json:"assignee"`
	}
	if err := json.NewDecoder(strings.NewReader(payload)).Decode(&one); err == nil {
		return strings.TrimSpace(one.Assignee)
	}
	return ""
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
	result := runHookLoad(t, city, oracleHookProcs, "polecat-oracle")
	elapsed := time.Since(start)
	t.Logf("admission saturation: saturated=%d claims=%d api=%d fallback=%d(reasons=%v) noRoute=%d procErr=%d rejections=%d peakConns=%d elapsed=%s",
		result.saturated, len(result.claims), result.apiRoutes, result.fallbackRoutes, result.fallbackReasons, result.noRoute, result.processErrors, result.rejections, result.peakConns, elapsed)
	for _, s := range result.samples {
		t.Logf("admission saturation exceptional sample: %s", s)
	}

	// Discovery is not admission-bounded (only the claim mutation is), so a healthy
	// controller still serves EVERY hook over the API with zero fallback — saturation
	// shows up as a retryable claim result, never as a per-worker subprocess fallback.
	if result.apiRoutes != oracleHookProcs {
		t.Fatalf("admission saturation: %d/%d hooks took route=api (fallback=%d procErr=%d noRoute=%d); a healthy controller must serve every hook",
			result.apiRoutes, oracleHookProcs, result.fallbackRoutes, result.processErrors, result.noRoute)
	}
	if result.fallbackRoutes != 0 {
		t.Fatalf("admission saturation: %d hook(s) fell back (reasons=%v); saturation must not become a subprocess-fallback storm", result.fallbackRoutes, result.fallbackReasons)
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

	result := runHookLoad(t, city, oracleHookProcs, "polecat-oracle")
	t.Logf("long read_timeout: peakConns=%d claims=%d api=%d fallback=%d(reasons=%v) noRoute=%d procErr=%d rejections=%d (baseline=%d)",
		result.peakConns, len(result.claims), result.apiRoutes, result.fallbackRoutes, result.fallbackReasons, result.noRoute, result.processErrors, result.rejections, baseline)
	for _, s := range result.samples {
		t.Logf("long read_timeout exceptional sample: %s", s)
	}

	// A long server read_timeout must not change the healthy-controller contract:
	// every hook takes the API route with zero fallback. A loose "apiRoutes>0" bar
	// would let the cure silently degrade to the subprocess path here.
	if result.apiRoutes != oracleHookProcs {
		t.Fatalf("long read_timeout: %d/%d hooks took route=api (fallback=%d procErr=%d noRoute=%d); a healthy controller must serve every hook",
			result.apiRoutes, oracleHookProcs, result.fallbackRoutes, result.processErrors, result.noRoute)
	}
	if result.fallbackRoutes != 0 {
		t.Fatalf("long read_timeout: %d hook(s) fell back to the subprocess path, want 0 (reasons=%v)", result.fallbackRoutes, result.fallbackReasons)
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

	// Return to baseline under the long read_timeout. The bounded ON peak is only
	// half the contract; the count must also settle back once the load drains,
	// proving the workers left no server socket for the server to reap. Because the
	// controller's pool caps idle lifetime BELOW the server wait_timeout, the
	// settle is driven by client-side idle-close, not the 10-minute server reaping —
	// so a return to baseline here is exactly the finding-6 evidence that the cure
	// does not depend on the server promptly reaping idle sockets. The earlier
	// revision recorded baseline and peak but never asserted the count came back
	// down under a long timeout.
	final := waitThreadsConnectedSettle(t, city.observer, baseline+2, 30*time.Second)
	if final > baseline+2 {
		t.Fatalf("long read_timeout: Threads_connected=%d did not return to baseline %d(+2) after the load drained; the connection bound depended on server-side reaping", final, baseline)
	}
}
