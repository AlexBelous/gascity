// Package prwatchdog evaluates whether a pull request has produced complete,
// passing CI evidence for its current head commit.
package prwatchdog

import "time"

// Status mirrors a GitHub check run's status field.
type Status string

// Status values a GitHub check run can report.
const (
	StatusQueued     Status = "queued"
	StatusInProgress Status = "in_progress"
	StatusCompleted  Status = "completed"
)

// Conclusion mirrors a GitHub check run's conclusion field, valid once
// Status is StatusCompleted.
type Conclusion string

// Conclusion values a GitHub check run can report once completed.
const (
	ConclusionSuccess        Conclusion = "success"
	ConclusionFailure        Conclusion = "failure"
	ConclusionNeutral        Conclusion = "neutral"
	ConclusionCancelled      Conclusion = "canceled"
	ConclusionSkipped        Conclusion = "skipped"
	ConclusionTimedOut       Conclusion = "timed_out"
	ConclusionActionRequired Conclusion = "action_required"
	ConclusionStale          Conclusion = "stale"
)

const (
	// CheckName is the preflight check run that must succeed before
	// CIRequiredName is considered meaningful.
	CheckName = "Check"
	// CIRequiredName is the comprehensive required CI check run.
	CIRequiredName = "CI / required"
	// MacCheckName is the opt-in macOS regression check run.
	MacCheckName = "Mac regression summary"
	// ReviewFormulasCheckName is the opt-in formula-review check run.
	ReviewFormulasCheckName = "Integration / review-formulas"
	// RequiredCheckName is the name this watchdog publishes as its own
	// required check run.
	RequiredCheckName = "Evidence / critical-path suites"
	// ObservationDeadline bounds how long the watchdog observes a head
	// commit before failing closed.
	ObservationDeadline = 25 * time.Minute
)

// CheckRun is a single GitHub check run observation for a head commit.
type CheckRun struct {
	Name       string
	HeadSHA    string
	Status     Status
	Conclusion Conclusion
	StartedAt  time.Time
	ID         int64
}

// Input is the observed state for one evaluation of a pull request head.
type Input struct {
	HeadSHA                  string
	CheckRuns                []CheckRun
	Elapsed                  time.Duration
	Deadline                 time.Duration
	NeedsMacLabel            bool
	NeedsReviewFormulasLabel bool
	FetchError               error
}

// Summary is a human-readable rendering of each tracked check's state.
type Summary struct {
	Check          string
	CIRequired     string
	Mac            string
	ReviewFormulas string
}

// Evaluation is the result of evaluating an Input.
type Evaluation struct {
	Pass     bool
	Terminal bool
	Reason   string
	Summary  Summary
}

// Evaluate inspects the observed check runs for in.HeadSHA and decides
// whether the watchdog should keep observing, or stop with a pass/fail
// verdict.
func Evaluate(_ Input) Evaluation {
	return Evaluation{}
}
