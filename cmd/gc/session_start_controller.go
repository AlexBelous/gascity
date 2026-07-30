package main

import (
	"context"
	"fmt"
	"io"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/util/workqueue"
)

const sessionStartAdmissionMaxIDBytes = 256

type sessionStartAdmissionSource string

const (
	sessionStartAdmissionPendingCreate sessionStartAdmissionSource = "pending_create"
	sessionStartAdmissionExplicitWake  sessionStartAdmissionSource = "explicit_wake"
	sessionStartAdmissionInProcess     sessionStartAdmissionSource = "in_process"
	sessionStartAdmissionSocket        sessionStartAdmissionSource = "socket"
	sessionStartAdmissionAntiEntropy   sessionStartAdmissionSource = "anti_entropy"
)

type sessionStartAdmission struct {
	SessionID        string
	Source           sessionStartAdmissionSource
	Version          uint64
	CensusGeneration uint64
	Culled           bool
	AdmittedAt       time.Time
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
	Admission  sessionStartAdmission
	Outcome    sessionStartReconcileOutcome
	StartedAt  time.Time
	FinishedAt time.Time
	Err        error
}

type sessionStartAuthoritativeSeedResult struct {
	SessionID string
	Complete  bool
	Err       error
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

	outcome, _, err := c.admit(id, source, false, 0)
	return outcome, err
}

func (c *sessionStartController) admitAuthoritative(id string, censusGeneration uint64) (sessionStartAdmissionOutcome, sessionStartAdmission, error) {
	return c.admit(id, sessionStartAdmissionAntiEntropy, true, censusGeneration)
}

func (c *sessionStartController) admit(id string, source sessionStartAdmissionSource, authoritative bool, censusGeneration uint64) (sessionStartAdmissionOutcome, sessionStartAdmission, error) {
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
	admission := sessionStartAdmission{
		SessionID:  id,
		Source:     source,
		Version:    c.nextVersion,
		AdmittedAt: admittedAt,
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
		}
		outcome, _, err := c.admitAuthoritative(pendingID, generation)
		if err != nil {
			c.failAuthoritativeSeed(generation)
			return
		}
		switch outcome {
		case sessionStartAdmissionAccepted, sessionStartAdmissionCoalesced:
			pendingID = ""
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
		sessionStartAdmissionAntiEntropy:
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
	finishedAt := c.now()
	result := sessionStartReconcileResult{
		Admission:  admission,
		StartedAt:  startedAt,
		FinishedAt: finishedAt,
		Err:        err,
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
	if c.queue.NumRequeues(key) < c.maxRetries {
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
