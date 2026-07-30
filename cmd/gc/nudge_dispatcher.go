package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/nudgequeue"
	"github.com/gastownhall/gascity/internal/nudgeshadow"
	"github.com/gastownhall/gascity/internal/runtime"
)

// pingNudgeWakeSocketDialTimeout bounds how long a producer waits to dial
// the supervisor wake socket. Producers must not block on a stale or
// missing socket — legacy-mode cities and pre-start producers expect the
// dial to fail fast.
const pingNudgeWakeSocketDialTimeout = 200 * time.Millisecond

const (
	nudgeDueTargetSelectionMatched       = "matched"
	nudgeDueTargetSelectionMismatch      = "mismatch"
	nudgeDueTargetSelectionCandidateOnly = "candidate_only"
	nudgeDueTargetSelectionLegacyOnly    = "legacy_only"

	nudgeDueTargetSelectionDigestDomain = "gascity.nudge.due-target-selection.v1"
)

// nudgeDueTargetSelectionObservation is a bounded summary of candidate and
// legacy due-target selection. It deliberately contains no raw session IDs.
type nudgeDueTargetSelectionObservation struct {
	Scope               string
	QueueItemCount      int
	CandidateCount      int
	CandidateDigest     string
	LegacyCount         int
	LegacyDigest        string
	ComparisonOutcome   string
	QueueDuration       time.Duration
	CandidateDuration   time.Duration
	LegacyDuration      time.Duration
	TotalDuration       time.Duration
	LegacyEffectOwner   bool
	ShadowEffectApplied bool
}

type nudgeDueTargetSelectionObserver func(nudgeDueTargetSelectionObservation)

// pingNudgeWakeSocket sends a best-effort wake signal to the supervisor's
// nudge dispatcher. Callers invoke this after enqueueing a queued nudge so
// the supervisor delivers within sub-second latency instead of waiting for
// the next patrol tick. Failures (no listener, dial timeout, write error)
// are intentionally silent: the patrol-tick fallback in supervisor mode
// and the per-session poller in legacy mode each guarantee eventual
// delivery without the wake.
func pingNudgeWakeSocket(cityPath string) {
	if cityPath == "" {
		return
	}
	path := nudgequeue.WakeSocketPath(cityPath)
	conn, err := net.DialTimeout("unix", path, pingNudgeWakeSocketDialTimeout)
	if err != nil {
		return
	}
	defer conn.Close() //nolint:errcheck // best-effort signaling
	_ = conn.SetWriteDeadline(time.Now().Add(pingNudgeWakeSocketDialTimeout))
	_, _ = conn.Write([]byte{1})
}

// startNudgeWakeListener opens the supervisor wake socket and spawns an
// accept loop that signals wakeCh on every connection. The returned
// listener is closed when ctx is canceled. Returns nil, nil when the
// socket cannot be opened (e.g. permission, path-too-long); callers fall
// back to patrol-interval dispatching.
func startNudgeWakeListener(ctx context.Context, cityPath string, wakeCh chan<- struct{}, stderr io.Writer, logPrefix string) (net.Listener, error) {
	path := nudgequeue.WakeSocketPath(cityPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("creating nudge wake dir: %w", err)
	}
	// A stale socket from a prior supervisor crash blocks Listen with
	// "address already in use". Removing it is safe because flock-based
	// queue access protects state; the socket carries no data of its own.
	_ = os.Remove(path)
	lis, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listening on nudge wake socket: %w", err)
	}
	// TOCTOU: there is a narrow window between Listen and Chmod where
	// the socket exists at the umask-default permissions and a co-local
	// user could connect. Worst case is a spurious dispatch tick — the
	// socket carries a single signal byte with no payload or auth — so
	// this is acceptable for now. A future hardening pass could set
	// umask before Listen, or use platform-specific abstract namespace
	// sockets where supported.
	if err := os.Chmod(path, 0o600); err != nil {
		_ = lis.Close()
		return nil, fmt.Errorf("chmod nudge wake socket: %w", err)
	}
	go func() {
		<-ctx.Done()
		_ = lis.Close()
	}()
	go func() {
		for {
			conn, err := lis.Accept()
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					return
				}
				if stderr != nil {
					fmt.Fprintf(stderr, "%s: nudge wake accept: %v\n", logPrefix, err) //nolint:errcheck
				}
				continue
			}
			// Drain whatever the producer sent (a single signal byte) and
			// close. The wake itself is the signal — payload is reserved
			// for future protocol extensions.
			_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			var buf [16]byte
			_, _ = conn.Read(buf[:])
			_ = conn.Close()
			select {
			case wakeCh <- struct{}{}:
			default:
				// Already-pending wake covers this enqueue; coalesced.
			}
		}
	}()
	return lis, nil
}

// dispatchAllQueuedNudges runs one supervisor-side dispatcher pass: scan
// the queue for pending agents, resolve each to a nudgeTarget via
// sessionBeads, and try delivery. Returns the number of targets that
// successfully delivered at least one item.
//
// This is a no-op when the dispatcher is configured for "legacy" mode —
// the per-session `gc nudge poll` processes own delivery in that case.
func dispatchAllQueuedNudges(cityPath string, cfg *config.City, store, sessStore beads.Store, sp runtime.Provider, sessionBeads *sessionBeadSnapshot) (int, error) {
	return dispatchAllQueuedNudgesExcept(cityPath, cfg, store, sessStore, sp, sessionBeads, nil)
}

// dispatchAllQueuedNudgesObserved runs the same legacy dispatch while
// observing its pre-provider due-target selection.
func dispatchAllQueuedNudgesObserved(cityPath string, cfg *config.City, store, sessStore beads.Store, sp runtime.Provider, sessionBeads *sessionBeadSnapshot, observer nudgeDueTargetSelectionObserver) (int, error) {
	return dispatchAllQueuedNudgesExceptObserved(cityPath, cfg, store, sessStore, sp, sessionBeads, nil, observer)
}

// dispatchAllQueuedNudgesExcept preserves legacy delivery for every target
// except exact session IDs currently scheduled by the keyed controller. Queue
// claiming remains the physical cross-path delivery fence.
func dispatchAllQueuedNudgesExcept(cityPath string, cfg *config.City, store, sessStore beads.Store, sp runtime.Provider, sessionBeads *sessionBeadSnapshot, excludedSessionIDs map[string]struct{}) (int, error) {
	return dispatchAllQueuedNudgesExceptObserved(cityPath, cfg, store, sessStore, sp, sessionBeads, excludedSessionIDs, nil)
}

func dispatchAllQueuedNudgesExceptObserved(cityPath string, cfg *config.City, store, sessStore beads.Store, sp runtime.Provider, sessionBeads *sessionBeadSnapshot, excludedSessionIDs map[string]struct{}, observer nudgeDueTargetSelectionObserver) (int, error) {
	if cfg == nil || sessionBeads == nil || cityPath == "" {
		return 0, nil
	}
	if !nudgeDispatcherIsSupervisor(cfg) {
		return 0, nil
	}
	var queueStarted time.Time
	if observer != nil {
		queueStarted = time.Now()
	}
	state, err := nudgequeue.LoadState(cityPath)
	if err != nil {
		return 0, fmt.Errorf("loading nudge queue: %w", err)
	}
	if observer != nil {
		return dispatchAllQueuedNudgesFromStateObserved(
			cityPath,
			cfg,
			store,
			sessStore,
			sp,
			sessionBeads,
			excludedSessionIDs,
			state,
			time.Since(queueStarted),
			observer,
		)
	}
	return dispatchAllQueuedNudgesFromState(cityPath, cfg, store, sessStore, sp, sessionBeads, excludedSessionIDs, state)
}

// dispatchAllQueuedNudgesFromState uses the state snapshot already loaded by
// keyed admission, avoiding a second queue read on the mixed legacy path.
func dispatchAllQueuedNudgesFromState(cityPath string, cfg *config.City, store, sessStore beads.Store, sp runtime.Provider, sessionBeads *sessionBeadSnapshot, excludedSessionIDs map[string]struct{}, state nudgequeue.State) (int, error) {
	targets := selectLegacyQueuedNudgeTargets(cityPath, cfg, sessionBeads, state, time.Now())
	return dispatchSelectedQueuedNudgeTargets(targets, store, sessStore, sp, excludedSessionIDs)
}

// dispatchAllQueuedNudgesFromStateObserved compares the pure exact-session
// candidate selector with the immutable legacy pre-provider selection, then
// makes the legacy dispatcher consume precisely the selection it observed.
func dispatchAllQueuedNudgesFromStateObserved(
	cityPath string,
	cfg *config.City,
	store, sessStore beads.Store,
	sp runtime.Provider,
	sessionBeads *sessionBeadSnapshot,
	excludedSessionIDs map[string]struct{},
	state nudgequeue.State,
	queueDuration time.Duration,
	observer nudgeDueTargetSelectionObserver,
) (int, error) {
	selectionStarted := time.Now()
	now := time.Now()

	candidateStarted := time.Now()
	candidateIDs := discoverDueExactNudgeSessionIDs(state, now)
	candidateDuration := time.Since(candidateStarted)

	legacyStarted := time.Now()
	targets := selectLegacyQueuedNudgeTargets(cityPath, cfg, sessionBeads, state, now)
	legacyDuration := time.Since(legacyStarted)
	legacyIDs := make([]string, 0, len(targets))
	for _, target := range targets {
		legacyIDs = append(legacyIDs, target.sessionID)
	}

	observation := newNudgeDueTargetSelectionObservation(
		state,
		candidateIDs,
		legacyIDs,
		queueDuration,
		candidateDuration,
		legacyDuration,
		queueDuration+time.Since(selectionStarted),
	)
	delivered, err := dispatchSelectedQueuedNudgeTargets(targets, store, sessStore, sp, excludedSessionIDs)
	if observer != nil {
		observer(observation)
	}
	return delivered, err
}

func selectLegacyQueuedNudgeTargets(cityPath string, cfg *config.City, sessionBeads *sessionBeadSnapshot, state nudgequeue.State, now time.Time) []nudgeTarget {
	if len(state.Pending) == 0 && len(state.InFlight) == 0 {
		return nil
	}
	pendingAgents := make(map[string]bool, len(state.Pending))
	for _, item := range state.Pending {
		if item.Agent == "" {
			continue
		}
		if !item.DeliverAfter.IsZero() && item.DeliverAfter.After(now) {
			continue
		}
		pendingAgents[item.Agent] = true
	}
	// In-flight items with expired leases are recoverable on the next
	// claim attempt. Including their agents lets us retry without waiting
	// for the patrol tick to discover them.
	for _, item := range state.InFlight {
		if item.Agent == "" {
			continue
		}
		if item.LeaseUntil.IsZero() || !item.LeaseUntil.Before(now) {
			continue
		}
		pendingAgents[item.Agent] = true
	}
	if len(pendingAgents) == 0 {
		return nil
	}

	targets := make([]nudgeTarget, 0, len(pendingAgents))
	for _, info := range sessionBeads.OpenInfos() {
		target := resolveNudgeTargetFromSessionInfo(cityPath, cfg, info)
		if target.sessionName == "" {
			continue
		}
		// ACP sessions also flow through this dispatcher. The inject-on-hook
		// drain path still catches deliveries when the agent receives external
		// prompts, but a warm-idle ACP session never fires its hook on its
		// own — queued patrol wisps would otherwise pile up forever. The
		// atomic queue claim in claimDueQueuedNudgesForTarget guarantees a
		// nudge is delivered exactly once across the dispatcher + drain paths.
		matched := false
		for _, key := range target.queueKeys() {
			if pendingAgents[key] {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		targets = append(targets, target)
	}
	return targets
}

func dispatchSelectedQueuedNudgeTargets(targets []nudgeTarget, store, sessStore beads.Store, sp runtime.Provider, excludedSessionIDs map[string]struct{}) (int, error) {
	// The dispatcher receives the nudges-class store (store) PLUS the
	// session-class store (sessStore). Session observation and delivery route
	// through sessStore; queue claim/dead-letter state remains on store.
	delivered := 0
	var firstErr error
	for _, target := range targets {
		obs, err := workerObserveNudgeTarget(target, sessStore, sp)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if !obs.Running {
			continue
		}
		ok, err := tryDeliverQueuedNudgesByPollerMatching(target, store, sessStore, sp, defaultNudgePollQuiescence, obs, func(item queuedNudge) bool {
			_, excluded := excludedSessionIDs[item.SessionID]
			return !excluded
		})
		if err != nil && firstErr == nil {
			firstErr = err
		}
		if ok {
			delivered++
		}
	}
	return delivered, firstErr
}

func newNudgeDueTargetSelectionObservation(
	state nudgequeue.State,
	candidateIDs, legacyIDs []string,
	queueDuration, candidateDuration, legacyDuration, totalDuration time.Duration,
) nudgeDueTargetSelectionObservation {
	return nudgeDueTargetSelectionObservation{
		Scope:               nudgeshadow.ScopeQueuedExactDueTargetSelection,
		QueueItemCount:      len(state.Pending) + len(state.InFlight),
		CandidateCount:      len(candidateIDs),
		CandidateDigest:     digestNudgeDueTargetSelection(candidateIDs),
		LegacyCount:         len(legacyIDs),
		LegacyDigest:        digestNudgeDueTargetSelection(legacyIDs),
		ComparisonOutcome:   compareNudgeDueTargetSelections(candidateIDs, legacyIDs),
		QueueDuration:       queueDuration,
		CandidateDuration:   candidateDuration,
		LegacyDuration:      legacyDuration,
		TotalDuration:       totalDuration,
		LegacyEffectOwner:   true,
		ShadowEffectApplied: false,
	}
}

func compareNudgeDueTargetSelections(candidateIDs, legacyIDs []string) string {
	if equalNudgeDueTargetSelections(candidateIDs, legacyIDs) {
		return nudgeDueTargetSelectionMatched
	}
	switch {
	case len(candidateIDs) > 0 && len(legacyIDs) == 0:
		return nudgeDueTargetSelectionCandidateOnly
	case len(candidateIDs) == 0 && len(legacyIDs) > 0:
		return nudgeDueTargetSelectionLegacyOnly
	default:
		return nudgeDueTargetSelectionMismatch
	}
}

func equalNudgeDueTargetSelections(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	left = append([]string(nil), left...)
	right = append([]string(nil), right...)
	sort.Strings(left)
	sort.Strings(right)
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func digestNudgeDueTargetSelection(ids []string) string {
	ordered := append([]string(nil), ids...)
	sort.Strings(ordered)
	digest := sha256.New()
	_, _ = io.WriteString(digest, nudgeDueTargetSelectionDigestDomain)
	for _, id := range ordered {
		_, _ = io.WriteString(digest, "\x00"+strconv.Itoa(len(id))+"\x00"+id)
	}
	return hex.EncodeToString(digest.Sum(nil))
}
