package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	sessionpkg "github.com/gastownhall/gascity/internal/session"
	"k8s.io/client-go/util/workqueue"
)

const sessionStartAdmissionMaxIDBytes = 256

var errSessionStartLegacyFallbackRequired = errors.New("session start requires legacy fallback")

var errSessionStartPoolDrainAckPending = errors.New("pool drain acknowledgement stop remains pending")

type sessionStartAdmissionSource string

const (
	sessionStartAdmissionPendingCreate  sessionStartAdmissionSource = "pending_create"
	sessionStartAdmissionExplicitWake   sessionStartAdmissionSource = "explicit_wake"
	sessionStartAdmissionInProcess      sessionStartAdmissionSource = "in_process"
	sessionStartAdmissionSocket         sessionStartAdmissionSource = "socket"
	sessionStartAdmissionAntiEntropy    sessionStartAdmissionSource = "anti_entropy"
	sessionStartAdmissionWaitDependency sessionStartAdmissionSource = "wait_dependency"
)

type sessionStartAdmission struct {
	SessionID      string
	Source         sessionStartAdmissionSource
	Version        uint64
	PoolAllocation *routedWorkPoolStartLease
	PoolDrainAck   *routedWorkPoolDrainAckLease
	WaitDependency *sessionWaitDependencyStartLease
	// PoolDrainAckUncertain retains a durable stop-pending row when an
	// anti-entropy seed cannot reconstruct its agent acknowledgement lease.
	// It is a retry fence, never destructive-stop authority.
	PoolDrainAckUncertain bool
	PoolStartEntered      bool
	CensusGeneration      uint64
	Culled                bool
	AdmittedAt            time.Time
}

type sessionStartAdmissionOutcome string

const (
	sessionStartAdmissionAccepted  sessionStartAdmissionOutcome = "accepted"
	sessionStartAdmissionCoalesced sessionStartAdmissionOutcome = "coalesced"
	sessionStartAdmissionOverflow  sessionStartAdmissionOutcome = "overflow"
	sessionStartAdmissionStale     sessionStartAdmissionOutcome = "stale"
)

type sessionStartReconcileOutcome string

const (
	sessionStartReconcileSucceeded  sessionStartReconcileOutcome = "succeeded"
	sessionStartReconcileSuperseded sessionStartReconcileOutcome = "superseded"
	sessionStartReconcileRetrying   sessionStartReconcileOutcome = "retrying"
	sessionStartReconcileExhausted  sessionStartReconcileOutcome = "exhausted"
	sessionStartReconcileCanceled   sessionStartReconcileOutcome = "canceled"
)

type sessionStartReconcileResult struct {
	Admission      sessionStartAdmission
	Outcome        sessionStartReconcileOutcome
	StartedAt      time.Time
	FinishedAt     time.Time
	LegacyFallback bool
	Err            error
}

type sessionStartAuthoritativeSeedResult struct {
	SessionID             string
	PoolDrainAck          *routedWorkPoolDrainAckLease
	PoolDrainAckUncertain bool
	Complete              bool
	Err                   error
}

type sessionStartControllerOptions struct {
	Workers     int
	MaxDistinct int
	MaxRetries  int
	Reconcile   func(context.Context, sessionStartAdmission) error
	Observer    func(sessionStartReconcileResult)
	RateLimiter workqueue.TypedRateLimiter[string]
	Now         func() time.Time
	Stderr      io.Writer
}

// sessionStartController is a bounded, keyed workqueue for session-start
// reconciliation. The durable store remains authoritative: admissions are only
// hints naming which exact key to reread.
type sessionStartController struct {
	queue                     workqueue.TypedRateLimitingInterface[string]
	workers                   int
	maxDistinct               int
	maxRetries                int
	reconcile                 func(context.Context, sessionStartAdmission) error
	observer                  func(sessionStartReconcileResult)
	now                       func() time.Time
	stderr                    io.Writer
	admissions                map[string]sessionStartAdmission
	nextVersion               uint64
	auditPending              bool
	seedOutstanding           map[string]struct{}
	inFlight                  map[string]uint64
	seedGeneration            uint64
	seedActive                bool
	seedCapacity              chan struct{}
	beforeMarkInFlightForTest func()

	mu        sync.Mutex
	started   bool
	accepting bool
	stopped   bool
	ctx       context.Context
	cancel    context.CancelFunc
	workerWG  sync.WaitGroup
	seedWG    sync.WaitGroup
	stopOnce  sync.Once
	stderrMu  sync.Mutex
}

func newSessionStartController(opts sessionStartControllerOptions) (*sessionStartController, error) {
	switch {
	case opts.Workers <= 0:
		return nil, fmt.Errorf("creating session-start controller: workers must be positive")
	case opts.MaxDistinct <= 0:
		return nil, fmt.Errorf("creating session-start controller: max distinct admissions must be positive")
	case opts.MaxRetries < 0:
		return nil, fmt.Errorf("creating session-start controller: max retries must not be negative")
	case opts.Reconcile == nil:
		return nil, fmt.Errorf("creating session-start controller: reconcile function is nil")
	}
	rateLimiter := opts.RateLimiter
	if rateLimiter == nil {
		rateLimiter = workqueue.DefaultTypedControllerRateLimiter[string]()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	stderr := opts.Stderr
	if stderr == nil {
		stderr = io.Discard
	}
	return &sessionStartController{
		queue:           workqueue.NewTypedRateLimitingQueue(rateLimiter),
		workers:         opts.Workers,
		maxDistinct:     opts.MaxDistinct,
		maxRetries:      opts.MaxRetries,
		reconcile:       opts.Reconcile,
		observer:        opts.Observer,
		now:             now,
		stderr:          stderr,
		admissions:      make(map[string]sessionStartAdmission, opts.MaxDistinct),
		seedOutstanding: make(map[string]struct{}),
		inFlight:        make(map[string]uint64, opts.MaxDistinct),
		seedCapacity:    make(chan struct{}, 1),
	}, nil
}

func (c *sessionStartController) Start(ctx context.Context) error {
	if c == nil {
		return fmt.Errorf("starting session-start controller: controller is nil")
	}
	if ctx == nil {
		return fmt.Errorf("starting session-start controller: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("starting session-start controller: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started || c.stopped {
		return fmt.Errorf("starting session-start controller: controller is single-start")
	}
	c.ctx, c.cancel = context.WithCancel(ctx)
	c.started = true
	c.accepting = true
	c.workerWG.Add(c.workers)
	for range c.workers {
		go c.runWorker()
	}
	return nil
}

func (c *sessionStartController) Admit(id string, source sessionStartAdmissionSource) (sessionStartAdmissionOutcome, error) {
	if c == nil {
		return "", fmt.Errorf("admitting session start: controller is nil")
	}
	if err := validateSessionStartAdmission(id, source); err != nil {
		return "", err
	}

	outcome, _, err := c.admit(id, source, false, 0, nil, nil, false, nil)
	return outcome, err
}

func (c *sessionStartController) AdmitPoolAllocation(lease routedWorkPoolStartLease) (sessionStartAdmissionOutcome, error) {
	if c == nil {
		return "", fmt.Errorf("admitting pool allocation: controller is nil")
	}
	if err := validateRoutedWorkPoolStartLease(lease); err != nil {
		return "", err
	}
	outcome, _, err := c.admit(lease.SessionID, sessionStartAdmissionInProcess, false, 0, &lease, nil, false, nil)
	return outcome, err
}

// AdmitPoolDrainAck retains the narrow ownership proof for one agent-sourced
// pool drain acknowledgement until exact reconciliation has reread it.
func (c *sessionStartController) AdmitPoolDrainAck(lease routedWorkPoolDrainAckLease) (sessionStartAdmissionOutcome, error) {
	if c == nil {
		return "", fmt.Errorf("admitting pool drain acknowledgement: controller is nil")
	}
	if err := validateRoutedWorkPoolDrainAckLease(lease); err != nil {
		return "", err
	}
	outcome, _, err := c.admit(lease.SessionID, sessionStartAdmissionInProcess, false, 0, nil, &lease, false, nil)
	return outcome, err
}

// AdmitWaitDependency retains a certified durable wait/session pair until the
// keyed worker has either committed its ready/start handoff or observed that
// the pair no longer matches. The queue remains keyed by the session because a
// provider start is the exclusive effect boundary.
func (c *sessionStartController) AdmitWaitDependency(lease sessionWaitDependencyStartLease) (sessionStartAdmissionOutcome, error) {
	if c == nil {
		return "", fmt.Errorf("admitting dependency wait start: controller is nil")
	}
	if err := validateSessionWaitDependencyStartLease(lease); err != nil {
		return "", err
	}
	outcome, _, err := c.admit(lease.SessionID, sessionStartAdmissionWaitDependency, false, 0, nil, nil, false, &lease)
	return outcome, err
}

// poolDrainAckSupersedesPoolStart permits the one safe start-to-stop handoff:
// the same active pool incarnation acknowledges completion of the exact work
// that caused its start. A newer membership observation is allowed, but no
// identity, work, source, generation, or requester proof may change.
func poolDrainAckSupersedesPoolStart(start routedWorkPoolStartLease, drain routedWorkPoolDrainAckLease) bool {
	return start.SessionID == drain.SessionID &&
		start.InstanceToken == drain.InstanceToken &&
		start.ControllerGeneration == drain.ControllerGeneration &&
		start.PoolTarget == drain.PoolTarget &&
		start.WorkID == drain.WorkID &&
		start.SourceStore == drain.SourceStore &&
		start.MembershipRevision <= drain.MembershipRevision &&
		drain.RequesterSessionID == start.SessionID &&
		drain.RequesterInstanceToken == start.InstanceToken
}

func (c *sessionStartController) admitAuthoritative(id string, censusGeneration uint64, poolDrainAck *routedWorkPoolDrainAckLease, poolDrainAckUncertain bool) (sessionStartAdmissionOutcome, sessionStartAdmission, error) {
	return c.admit(id, sessionStartAdmissionAntiEntropy, true, censusGeneration, nil, poolDrainAck, poolDrainAckUncertain, nil)
}

func (c *sessionStartController) admit(id string, source sessionStartAdmissionSource, authoritative bool, censusGeneration uint64, poolAllocation *routedWorkPoolStartLease, poolDrainAck *routedWorkPoolDrainAckLease, poolDrainAckUncertain bool, waitDependency *sessionWaitDependencyStartLease) (sessionStartAdmissionOutcome, sessionStartAdmission, error) {
	if err := validateSessionStartAdmission(id, source); err != nil {
		return "", sessionStartAdmission{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.accepting || c.stopped {
		return "", sessionStartAdmission{}, fmt.Errorf("admitting session start %q: controller is stopped", id)
	}
	if authoritative && (c.seedGeneration != censusGeneration || c.ctx.Err() != nil) {
		return sessionStartAdmissionStale, sessionStartAdmission{}, nil
	}
	previous, existed := c.admissions[id]
	if authoritative && !existed && len(c.seedOutstanding) >= c.authoritativeCapacity() {
		return sessionStartAdmissionOverflow, sessionStartAdmission{}, nil
	}
	if !existed && len(c.admissions) >= c.maxDistinct {
		if !authoritative {
			c.auditPending = true
		}
		return sessionStartAdmissionOverflow, sessionStartAdmission{}, nil
	}
	c.nextVersion++
	if c.nextVersion == 0 {
		c.auditPending = true
		return "", sessionStartAdmission{}, fmt.Errorf("admitting session start %q: admission version exhausted", id)
	}
	admittedAt := c.now()
	if existed && (source == sessionStartAdmissionAntiEntropy ||
		(previous.Source == sessionStartAdmissionInProcess && source != sessionStartAdmissionInProcess)) {
		source = previous.Source
		admittedAt = previous.AdmittedAt
	}
	if (poolAllocation != nil && poolDrainAck != nil) || (waitDependency != nil && (poolAllocation != nil || poolDrainAck != nil)) {
		return "", sessionStartAdmission{}, fmt.Errorf("admitting session start %q: conflicting exact-start leases", id)
	}
	supersedesPoolStart := false
	if existed && poolAllocation != nil && previous.PoolDrainAck != nil {
		return "", sessionStartAdmission{}, fmt.Errorf("admitting session start %q: retained pool lease conflicts with new admission", id)
	}
	if existed && waitDependency != nil && (previous.PoolAllocation != nil || previous.PoolDrainAck != nil) {
		return "", sessionStartAdmission{}, fmt.Errorf("admitting session start %q: retained pool lease conflicts with dependency wait", id)
	}
	if existed && (poolAllocation != nil || poolDrainAck != nil) && previous.WaitDependency != nil {
		return "", sessionStartAdmission{}, fmt.Errorf("admitting session start %q: retained dependency wait conflicts with pool lease", id)
	}
	if existed && poolDrainAck != nil && previous.PoolAllocation != nil {
		if !previous.PoolStartEntered || !poolDrainAckSupersedesPoolStart(*previous.PoolAllocation, *poolDrainAck) {
			return "", sessionStartAdmission{}, fmt.Errorf("admitting session start %q: retained pool lease conflicts with new admission", id)
		}
		supersedesPoolStart = true
	}
	poolStartEntered := false
	if poolAllocation == nil && existed && !supersedesPoolStart {
		poolAllocation = previous.PoolAllocation
		poolStartEntered = previous.PoolStartEntered
	} else if poolAllocation != nil && existed && previous.PoolAllocation != nil &&
		previous.PoolAllocation.SessionID == poolAllocation.SessionID &&
		previous.PoolAllocation.InstanceToken == poolAllocation.InstanceToken {
		poolStartEntered = previous.PoolStartEntered
	}
	if poolAllocation != nil {
		copied := *poolAllocation
		poolAllocation = &copied
	}
	if poolDrainAck == nil && existed {
		poolDrainAck = previous.PoolDrainAck
		poolDrainAckUncertain = previous.PoolDrainAckUncertain
	}
	if poolDrainAck != nil {
		copied := *poolDrainAck
		poolDrainAck = &copied
	}
	if waitDependency == nil && existed {
		waitDependency = previous.WaitDependency
	}
	if waitDependency != nil && existed && previous.WaitDependency != nil && sameWaitDependencyCertificate(*previous.WaitDependency, *waitDependency) {
		// Duplicate hints for the same durable observation keep the first minted
		// operation. A changed revision is a new durable observation and replaces
		// the parked lease so relevant events can make progress.
		waitDependency = previous.WaitDependency
	}
	if waitDependency != nil {
		copied := *waitDependency
		copied.DepIDs = append([]string(nil), waitDependency.DepIDs...)
		waitDependency = &copied
	}
	admission := sessionStartAdmission{
		SessionID:             id,
		Source:                source,
		Version:               c.nextVersion,
		PoolAllocation:        poolAllocation,
		PoolDrainAck:          poolDrainAck,
		PoolDrainAckUncertain: poolDrainAckUncertain,
		WaitDependency:        waitDependency,
		PoolStartEntered:      poolStartEntered,
		AdmittedAt:            admittedAt,
	}
	if authoritative && admission.Source == sessionStartAdmissionAntiEntropy {
		admission.CensusGeneration = censusGeneration
	}
	c.admissions[id] = admission
	if authoritative && !existed && admission.Source == sessionStartAdmissionAntiEntropy {
		c.seedOutstanding[id] = struct{}{}
	}
	c.queue.Add(id)
	if existed {
		return sessionStartAdmissionCoalesced, admission, nil
	}
	return sessionStartAdmissionAccepted, admission, nil
}

func sameWaitDependencyCertificate(a, b sessionWaitDependencyStartLease) bool {
	return a.WaitID == b.WaitID && a.SessionID == b.SessionID && a.DepMode == b.DepMode &&
		a.RegisteredEpoch == b.RegisteredEpoch && a.WaitRevision == b.WaitRevision && a.SessionRevision == b.SessionRevision &&
		a.IndexGeneration == b.IndexGeneration && a.ControllerGeneration == b.ControllerGeneration && slices.Equal(a.DepIDs, b.DepIDs)
}

// StartAuthoritativeSeed starts at most one bounded producer. next distinguishes
// normal snapshot exhaustion from an incomplete or failed producer result. It is
// called without the controller lock.
func (c *sessionStartController) StartAuthoritativeSeed(next func(context.Context) sessionStartAuthoritativeSeedResult) error {
	if c == nil {
		return fmt.Errorf("starting authoritative session-start seed: controller is nil")
	}
	if next == nil {
		return fmt.Errorf("starting authoritative session-start seed: next is nil")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.accepting || c.stopped {
		return fmt.Errorf("starting authoritative session-start seed: controller is stopped")
	}
	if c.seedActive {
		c.seedGeneration++
		c.auditPending = true
		c.signalSeedCapacityLocked()
		return nil
	}
	c.seedGeneration++
	if c.seedGeneration == 0 {
		c.auditPending = true
		return fmt.Errorf("starting authoritative session-start seed: generation exhausted")
	}
	generation := c.seedGeneration
	c.auditPending = false
	c.seedActive = true
	c.seedWG.Add(1)
	go c.runAuthoritativeSeed(generation, next)
	return nil
}

func (c *sessionStartController) runAuthoritativeSeed(generation uint64, next func(context.Context) sessionStartAuthoritativeSeedResult) {
	defer c.seedWG.Done()
	defer func() {
		c.mu.Lock()
		c.seedActive = false
		c.mu.Unlock()
	}()

	pendingID := ""
	var pendingDrainAck *routedWorkPoolDrainAckLease
	pendingDrainAckUncertain := false
	for {
		if err := c.ctx.Err(); err != nil || !c.seedGenerationCurrent(generation) {
			return
		}
		if pendingID == "" {
			result := next(c.ctx)
			if result.Err != nil {
				c.failAuthoritativeSeed(generation)
				return
			}
			if result.Complete {
				if result.SessionID != "" {
					c.failAuthoritativeSeed(generation)
					return
				}
				c.publishCompleteAuthoritativeCensus(generation)
				return
			}
			if result.SessionID == "" {
				c.failAuthoritativeSeed(generation)
				return
			}
			pendingID = result.SessionID
			pendingDrainAck = result.PoolDrainAck
			pendingDrainAckUncertain = result.PoolDrainAckUncertain
		}
		outcome, _, err := c.admitAuthoritative(pendingID, generation, pendingDrainAck, pendingDrainAckUncertain)
		if err != nil {
			c.failAuthoritativeSeed(generation)
			return
		}
		switch outcome {
		case sessionStartAdmissionAccepted, sessionStartAdmissionCoalesced:
			pendingID = ""
			pendingDrainAck = nil
			pendingDrainAckUncertain = false
		case sessionStartAdmissionOverflow:
			if !c.waitForSeedCapacity() {
				return
			}
		case sessionStartAdmissionStale:
			return
		}
	}
}

func (c *sessionStartController) failAuthoritativeSeed(generation uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seedGeneration != generation || !c.accepting || c.stopped || c.ctx.Err() != nil {
		return
	}
	c.auditPending = true
}

// publishCompleteAuthoritativeCensus scans only the controller's bounded
// admission set. A key absent from this generation is marked culled only when
// no worker is already executing that exact admission; its slot remains until
// the workqueue delivers the retained key.
func (c *sessionStartController) publishCompleteAuthoritativeCensus(generation uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seedGeneration != generation || !c.accepting || c.stopped || c.ctx.Err() != nil {
		return
	}
	for id, admission := range c.admissions {
		if admission.Source != sessionStartAdmissionAntiEntropy || admission.CensusGeneration == 0 || admission.CensusGeneration == generation ||
			c.inFlight[id] == admission.Version {
			continue
		}
		admission.Culled = true
		c.admissions[id] = admission
	}
}

func (c *sessionStartController) seedGenerationCurrent(generation uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seedGeneration == generation && c.accepting && !c.stopped
}

func (c *sessionStartController) authoritativeCapacity() int {
	capacity := min(c.workers, c.maxDistinct-1)
	if capacity < 1 {
		return 1
	}
	return capacity
}

func (c *sessionStartController) waitForSeedCapacity() bool {
	select {
	case <-c.ctx.Done():
		return false
	case <-c.seedCapacity:
		return true
	}
}

func (c *sessionStartController) signalSeedCapacityLocked() {
	select {
	case c.seedCapacity <- struct{}{}:
	default:
	}
}

func validateSessionStartAdmission(id string, source sessionStartAdmissionSource) error {
	if id == "" || strings.TrimSpace(id) != id {
		return fmt.Errorf("admitting session start: session id %q is not canonical", id)
	}
	if len(id) > sessionStartAdmissionMaxIDBytes {
		return fmt.Errorf("admitting session start: session id is %d bytes; maximum is %d", len(id), sessionStartAdmissionMaxIDBytes)
	}
	if !strings.ContainsRune(id, '-') {
		return fmt.Errorf("admitting session start: session id %q has no store prefix", id)
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '-' || r == '_' || r == '.' {
			continue
		}
		return fmt.Errorf("admitting session start: session id %q contains an invalid character", id)
	}
	switch source {
	case sessionStartAdmissionPendingCreate,
		sessionStartAdmissionExplicitWake,
		sessionStartAdmissionInProcess,
		sessionStartAdmissionSocket,
		sessionStartAdmissionAntiEntropy,
		sessionStartAdmissionWaitDependency:
		return nil
	default:
		return fmt.Errorf("admitting session start %q: unknown source %q", id, source)
	}
}

// RequestAudit records a level-triggered request for an authoritative census.
func (c *sessionStartController) RequestAudit() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.auditPending = true
	c.mu.Unlock()
}

// TakeAuditRequest returns and clears the current authoritative-audit request.
func (c *sessionStartController) TakeAuditRequest() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	requested := c.auditPending
	c.auditPending = false
	return requested
}

// Pending returns the number of distinct admitted keys, including keys
// currently being processed.
func (c *sessionStartController) Pending() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.admissions)
}

// ownsPoolAllocationStart requires the durable token to match before the first
// attempt enters. Once entered, pre-wake may rotate that token, so the retained
// admission remains the exclusion authority through retries until it terminates.
func (c *sessionStartController) ownsPoolAllocationStart(sessionID, instanceToken string) bool {
	instanceToken = strings.TrimSpace(instanceToken)
	if c == nil || sessionID == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	admission, ok := c.admissions[sessionID]
	if !ok || admission.PoolAllocation == nil {
		return false
	}
	lease := admission.PoolAllocation
	return lease.SessionID == sessionID &&
		(admission.PoolStartEntered || (instanceToken != "" && lease.InstanceToken == instanceToken))
}

// ownsWaitDependencyStart reports whether a retained exact wait lease owns the
// session's start boundary. It intentionally ignores a caller-supplied token:
// the durable wait operation, not a legacy session projection, is the proof.
func (c *sessionStartController) ownsWaitDependencyStart(sessionID string) bool {
	if c == nil || sessionID == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	admission, ok := c.admissions[sessionID]
	return ok && admission.WaitDependency != nil
}

// ownsWaitDependencyWait verifies the full durable wait identity retained by
// a keyed admission. A matching session alone is insufficient because a later
// wait registration must not inherit an earlier operation's exclusion.
func (c *sessionStartController) ownsWaitDependencyWait(wait sessionpkg.WaitInfo) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	admission, ok := c.admissions[wait.SessionID]
	if !ok || admission.WaitDependency == nil {
		return false
	}
	lease := admission.WaitDependency
	return wait.ID == lease.WaitID && wait.SessionID == lease.SessionID && wait.Kind == "deps" && wait.DepMode == lease.DepMode &&
		slices.Equal(wait.DepIDs, lease.DepIDs) && wait.RegisteredEpoch == lease.RegisteredEpoch &&
		(wait.State == waitStatePending || wait.State == waitStateReady && wait.ReadyOwner == string(sessionpkg.WaitReadyOwnerDependency) && wait.ReadyOperation == lease.Operation)
}

// ownsPoolDrainAckStop keeps legacy from entering a stop only while the exact
// drain acknowledgement it names is retained. Unlike a start lease, the
// runtime incarnation never rotates before the destructive stop effect.
func (c *sessionStartController) ownsPoolDrainAckStop(sessionID, instanceToken string) bool {
	instanceToken = strings.TrimSpace(instanceToken)
	if c == nil || sessionID == "" || instanceToken == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	admission, ok := c.admissions[sessionID]
	if !ok || admission.PoolDrainAck == nil {
		return false
	}
	lease := admission.PoolDrainAck
	return lease.SessionID == sessionID && lease.InstanceToken == instanceToken
}

// YieldPoolDrainAck releases a retained agent drain acknowledgement only when
// the same durable lease still owns the key. An async pre-stop rollback must
// not erase a newer admission for a replacement runtime incarnation.
func (c *sessionStartController) YieldPoolDrainAck(lease routedWorkPoolDrainAckLease) bool {
	if c == nil || validateRoutedWorkPoolDrainAckLease(lease) != nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	admission, ok := c.admissions[lease.SessionID]
	if !ok || admission.PoolDrainAck == nil || *admission.PoolDrainAck != lease {
		return false
	}
	delete(c.admissions, lease.SessionID)
	c.releaseAuthoritativeSlotLocked(lease.SessionID)
	c.queue.Forget(lease.SessionID)
	return true
}

func (c *sessionStartController) Stop() {
	if c == nil {
		return
	}
	c.stopOnce.Do(func() {
		c.mu.Lock()
		started := c.started
		c.accepting = false
		c.stopped = true
		cancel := c.cancel
		c.mu.Unlock()

		if cancel != nil {
			cancel()
		}
		c.seedWG.Wait()
		if started {
			c.queue.ShutDownWithDrain()
			c.workerWG.Wait()
		} else {
			c.queue.ShutDown()
		}

		c.mu.Lock()
		clear(c.admissions)
		clear(c.seedOutstanding)
		clear(c.inFlight)
		c.mu.Unlock()
	})
}

func (c *sessionStartController) runWorker() {
	defer c.workerWG.Done()
	for {
		key, shutdown := c.queue.Get()
		if shutdown {
			return
		}
		func() {
			defer c.queue.Done(key)
			c.reconcileKey(key)
		}()
	}
}

func (c *sessionStartController) reconcileKey(key string) {
	admission, ok := c.readAdmission(key)
	if !ok {
		c.queue.Forget(key)
		return
	}
	if admission.Culled {
		c.queue.Forget(key)
		c.deleteAdmissionIfVersion(key, admission.Version)
		return
	}
	if err := c.ctx.Err(); err != nil {
		finishedAt := c.now()
		c.queue.Forget(key)
		c.deleteAdmissionIfVersion(key, admission.Version)
		c.observe(sessionStartReconcileResult{
			Admission:  admission,
			Outcome:    sessionStartReconcileCanceled,
			StartedAt:  finishedAt,
			FinishedAt: finishedAt,
			Err:        err,
		})
		return
	}
	if c.beforeMarkInFlightForTest != nil {
		c.beforeMarkInFlightForTest()
	}
	if !c.markInFlightIfVersion(key, admission.Version) {
		c.queue.Forget(key)
		c.deleteAdmissionIfVersion(key, admission.Version)
		return
	}
	defer c.clearInFlightIfVersion(key, admission.Version)
	startedAt := c.now()
	err := c.callReconcile(admission)
	legacyFallback := errors.Is(err, errSessionStartLegacyFallbackRequired)
	legacyFallbackErr := error(nil)
	if legacyFallback {
		if err.Error() != errSessionStartLegacyFallbackRequired.Error() {
			legacyFallbackErr = err
		}
		err = nil
	}
	finishedAt := c.now()
	result := sessionStartReconcileResult{
		Admission:      admission,
		StartedAt:      startedAt,
		FinishedAt:     finishedAt,
		LegacyFallback: legacyFallback,
		Err:            errors.Join(err, legacyFallbackErr),
	}

	if c.ctx.Err() != nil {
		c.queue.Forget(key)
		c.deleteAdmissionIfVersion(key, admission.Version)
		result.Outcome = sessionStartReconcileCanceled
		result.Err = c.ctx.Err()
		c.observe(result)
		return
	}
	if !c.admissionVersionCurrent(key, admission.Version) {
		c.queue.Forget(key)
		result.Outcome = sessionStartReconcileSuperseded
		c.observe(result)
		return
	}
	if err == nil {
		c.queue.Forget(key)
		c.deleteAdmissionIfVersion(key, admission.Version)
		result.Outcome = sessionStartReconcileSucceeded
		c.observe(result)
		return
	}
	if admission.WaitDependency != nil {
		// A durable wait handoff is never exhausted. Forget this queued attempt
		// while retaining the lease; the next exact event or audit admission
		// redrives it without deleting its only ownership proof.
		c.queue.Forget(key)
		result.Outcome = sessionStartReconcileRetrying
		c.observe(result)
		return
	}
	if admission.PoolDrainAck != nil || admission.PoolDrainAckUncertain || errors.Is(err, errSessionStartPoolDrainAckPending) || c.queue.NumRequeues(key) < c.maxRetries {
		c.queue.AddRateLimited(key)
		result.Outcome = sessionStartReconcileRetrying
		c.observe(result)
		return
	}

	c.queue.Forget(key)
	c.mu.Lock()
	if current, exists := c.admissions[key]; exists && current.Version == admission.Version {
		delete(c.admissions, key)
		c.releaseAuthoritativeSlotLocked(key)
		c.auditPending = true
	}
	c.mu.Unlock()
	result.Outcome = sessionStartReconcileExhausted
	c.observe(result)
}

func (c *sessionStartController) readAdmission(key string) (sessionStartAdmission, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	admission, ok := c.admissions[key]
	return admission, ok
}

func (c *sessionStartController) admissionVersionCurrent(key string, version uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	current, ok := c.admissions[key]
	return ok && current.Version == version
}

func (c *sessionStartController) markInFlightIfVersion(key string, version uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	current, ok := c.admissions[key]
	if !ok || current.Version != version || current.Culled {
		return false
	}
	if current.PoolAllocation != nil && !current.PoolStartEntered {
		current.PoolStartEntered = true
		c.admissions[key] = current
	}
	c.inFlight[key] = version
	return true
}

func (c *sessionStartController) clearInFlightIfVersion(key string, version uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inFlight[key] == version {
		delete(c.inFlight, key)
	}
}

func (c *sessionStartController) deleteAdmissionIfVersion(key string, version uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if current, ok := c.admissions[key]; ok && current.Version == version {
		delete(c.admissions, key)
		c.releaseAuthoritativeSlotLocked(key)
	}
}

func (c *sessionStartController) releaseAuthoritativeSlotLocked(key string) {
	delete(c.seedOutstanding, key)
	c.signalSeedCapacityLocked()
}

func (c *sessionStartController) callReconcile(admission sessionStartAdmission) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("session-start reconcile panicked for %s: %v", admission.SessionID, recovered)
			c.writeDiagnostic("%v\n%s\n", err, debug.Stack())
		}
	}()
	return c.reconcile(c.ctx, admission)
}

func (c *sessionStartController) observe(result sessionStartReconcileResult) {
	if c.observer == nil {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			c.writeDiagnostic("session-start result observer panicked for %s: %v\n%s\n", result.Admission.SessionID, recovered, debug.Stack())
		}
	}()
	c.observer(result)
}

func (c *sessionStartController) writeDiagnostic(format string, args ...any) {
	c.stderrMu.Lock()
	defer c.stderrMu.Unlock()
	fmt.Fprintf(c.stderr, format, args...) //nolint:errcheck // controller diagnostics must not kill reconciliation
}
