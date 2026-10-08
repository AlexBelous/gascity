package proctable

import (
	"context"
	"fmt"
	"reflect"
	"time"
)

// CallerUIDs contains only UIDs actually measured by the OS.
type CallerUIDs struct {
	Real, Effective, Saved, FileSystem uint32
	HasFileSystem                      bool // Darwin has no measured Linux fsuid: do not synthesize it.
}

// CallerIncarnation is per-process evidence, not session authority.
type CallerIncarnation struct {
	PID, PPID, PGID         int
	Start, Domain, Platform string
	UIDs                    CallerUIDs
	Environment             CallerEnvironment
}

// Format hides the private process evidence in every diagnostic representation.
func (CallerIncarnation) Format(s fmt.State, _ rune) {
	_, _ = s.Write([]byte("[private caller incarnation]"))
}

// CallerAnchor pins a separately validated provider incarnation and OS domain.
type CallerAnchor struct {
	PID                     int
	Start, Domain, Platform string
	UIDs                    CallerUIDs
}

type callerPIDReader func(context.Context, int) (CallerIncarnation, error)

func callerSameIdentity(a, b CallerIncarnation) bool {
	return a.PID == b.PID && a.PPID == b.PPID && a.PGID == b.PGID && a.Start == b.Start &&
		a.Domain == b.Domain && a.Platform == b.Platform && a.UIDs == b.UIDs
}

// ReadCallerIncarnation acquires one OS process with a dedicated complete ENV
// region. No unsupported/restricted Darwin ENV is promoted to a Linux fact.
func ReadCallerIncarnation(ctx context.Context, pid int) (CallerIncarnation, error) {
	if ctx == nil || ctx.Err() != nil {
		return CallerIncarnation{}, errCallerUnknown
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	p, err := readCallerPID(ctx, pid)
	if err != nil {
		return CallerIncarnation{}, err
	}
	if ctx.Err() != nil {
		return CallerIncarnation{}, callerFailure("deadline", nil)
	}
	if !p.Environment.Complete() {
		return CallerIncarnation{}, callerFailure("environment-completeness", nil)
	}
	return p, nil
}

// ReadCallerIncarnations is preparatory acquisition, NOT runtime authority.
// The anchor must eventually come from a separately validated provider, not
// ambient ENV or the requesting process's claims. It never scans a host table.
func ReadCallerIncarnations(ctx context.Context, pid int, anchor CallerAnchor) ([]CallerIncarnation, error) {
	return readCallerIncarnations(ctx, pid, anchor, readCallerPID)
}

func readCallerIncarnations(ctx context.Context, pid int, anchor CallerAnchor, read callerPIDReader) ([]CallerIncarnation, error) {
	if ctx == nil || read == nil || pid <= 1 || anchor.PID <= 1 || anchor.Start == "" || anchor.Domain == "" || anchor.Platform == "" {
		return nil, errCallerUnknown
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out := []CallerIncarnation{}
	seen := map[int]bool{}
	total := 0
	for len(out) < 128 {
		if ctx.Err() != nil || seen[pid] {
			return nil, errCallerUnknown
		}
		seen[pid] = true
		p, err := read(ctx, pid)
		if err != nil || p.PID != pid || p.Start == "" || p.PGID <= 0 || p.Platform != anchor.Platform || p.Domain != anchor.Domain || p.UIDs != anchor.UIDs {
			return nil, errCallerUnknown
		}
		p.Environment = CallerEnvironment{entries: p.Environment.Entries(), complete: p.Environment.complete}
		if len(p.Environment.entries) == 0 || !p.Environment.complete {
			return nil, errCallerUnknown
		}
		for _, e := range p.Environment.entries {
			total += len(e) + 1
		}
		if total > callerMaxBytes {
			return nil, errCallerUnknown
		}
		out = append(out, p)
		if pid == anchor.PID {
			if p.Start != anchor.Start {
				return nil, errCallerUnknown
			}
			// All edges/identities/environments must survive a fresh per-PID capture.
			// No initial chain is returned after a missing, changed or reused ancestor.
			for _, prior := range out {
				if ctx.Err() != nil {
					return nil, errCallerUnknown
				}
				fresh, err := read(ctx, prior.PID)
				if err != nil || ctx.Err() != nil || !reflect.DeepEqual(prior, fresh) {
					return nil, errCallerUnknown
				}
			}
			if ctx.Err() != nil {
				return nil, errCallerUnknown
			}
			return out, nil
		}
		if p.PPID <= 1 {
			return nil, errCallerUnknown
		}
		pid = p.PPID
	}
	return nil, errCallerUnknown
}
