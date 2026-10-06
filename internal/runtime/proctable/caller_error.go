package proctable

import (
	"errors"
	"fmt"
	"syscall"
)

type callerAcquisitionError struct {
	stage string
	errno int
}

func (e *callerAcquisitionError) Error() string {
	return fmt.Sprintf("caller acquisition UNKNOWN stage=%s errno=%d", e.stage, e.errno)
}
func (e *callerAcquisitionError) Unwrap() error { return errCallerUnknown }
func callerFailure(stage string, err error) error {
	var existing *callerAcquisitionError
	if errors.As(err, &existing) {
		return existing
	}
	var errno syscall.Errno
	_ = errors.As(err, &errno)
	return &callerAcquisitionError{stage: stage, errno: int(errno)}
}

// CallerDiagnostic exposes only a static source stage and errno. It never
// includes ENV, identity values, paths, raw input, or underlying error text.
func CallerDiagnostic(err error) (string, int) {
	var e *callerAcquisitionError
	if errors.As(err, &e) {
		return e.stage, e.errno
	}
	return "unknown", 0
}
