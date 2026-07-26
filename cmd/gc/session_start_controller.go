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
	SessionID  string
	Source     sessionStartAdmissionSource
	Version    uint64
	AdmittedAt time.Time
}

type sessionStartAdmissionOutcome string

const (
	sessionStartAdmissionAccepted  sessionStartAdmissionOutcome = "accepted"
	sessionStartAdmissionCoalesced sessionStartAdmissionOutcome = "coalesced"
	sessionStartAdmissionOverflow  sessionStartAdmissionOutcome = "overflow"
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
	queue        workqueue.TypedRateLimitingInterface[string]
	workers      int
	maxDistinct  int
	maxRetries   int
	reconcile    func(context.Context, sessionStartAdmission) error
	observer     func(sessionStartReconcileResult)
	now          func() time.Time
	stderr       io.Writer
	admissions   map[string]sessionStartAdmission
	nextVersion  uint64
	auditPending bool

	mu        sync.Mutex
	started   bool
	accepting bool
	stopped   bool
	ctx       context.Context
	cancel    context.CancelFunc
	workerWG  sync.WaitGroup
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
		queue:       workqueue.NewTypedRateLimitingQueue(rateLimiter),
		workers:     opts.Workers,
		maxDistinct: opts.MaxDistinct,
		maxRetries:  opts.MaxRetries,
		reconcile:   opts.Reconcile,
		observer:    opts.Observer,
		now:         now,
		stderr:      stderr,
		admissions:  make(map[string]sessionStartAdmission, opts.MaxDistinct),
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

	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.accepting || c.stopped {
		return "", fmt.Errorf("admitting session start %q: controller is stopped", id)
	}
	_, existed := c.admissions[id]
	if !existed && len(c.admissions) >= c.maxDistinct {
		c.auditPending = true
		return sessionStartAdmissionOverflow, nil
	}
	c.nextVersion++
	if c.nextVersion == 0 {
		c.auditPending = true
		return "", fmt.Errorf("admitting session start %q: admission version exhausted", id)
	}
	c.admissions[id] = sessionStartAdmission{
		SessionID:  id,
		Source:     source,
		Version:    c.nextVersion,
		AdmittedAt: c.now(),
	}
	c.queue.Add(id)
	if existed {
		return sessionStartAdmissionCoalesced, nil
	}
	return sessionStartAdmissionAccepted, nil
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
		if started {
			c.queue.ShutDownWithDrain()
			c.workerWG.Wait()
		} else {
			c.queue.ShutDown()
		}

		c.mu.Lock()
		clear(c.admissions)
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

func (c *sessionStartController) deleteAdmissionIfVersion(key string, version uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if current, ok := c.admissions[key]; ok && current.Version == version {
		delete(c.admissions, key)
	}
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
