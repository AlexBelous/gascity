package main

import (
	"errors"
	"fmt"

	"github.com/gastownhall/gascity/internal/doctor"
	"github.com/gastownhall/gascity/internal/tmuxorphan"
)

// tmuxOrphanCheck reports orphaned tmux server processes: servers whose
// named (-L/-S) socket is provably absent from disk, so nothing can ever
// reach them again (ga-026hrg: 647 such processes found leaked in the
// field, 2.08 GiB and 3910 fds, with only 3 of their sockets reachable).
//
// Advisory + opt-in remediation: a leaking tmux server is never a gating
// failure, and Run never mutates anything -- only `gc doctor --fix` (Fix)
// terminates confirmed orphans, via tmuxorphan.Reap.
type tmuxOrphanCheck struct {
	listServers  func() ([]tmuxorphan.ServerProcess, error)
	socketExists func(string) (bool, error)
	terminate    func(int) error
}

func newTmuxOrphanCheck() *tmuxOrphanCheck {
	return &tmuxOrphanCheck{
		listServers:  tmuxorphan.ListServers,
		socketExists: tmuxorphan.SocketExists,
		terminate:    tmuxorphan.Terminate,
	}
}

func (c *tmuxOrphanCheck) Name() string         { return "tmux-orphan" }
func (c *tmuxOrphanCheck) CanFix() bool         { return true }
func (c *tmuxOrphanCheck) WarmupEligible() bool { return false }

func (c *tmuxOrphanCheck) scanConfig() tmuxorphan.ScanConfig {
	return tmuxorphan.ScanConfig{
		ListServers:  c.listServers,
		SocketExists: c.socketExists,
	}
}

// Run classifies but never mutates: it reports the orphan count as an
// advisory warning and leaves remediation to Fix.
func (c *tmuxOrphanCheck) Run(_ *doctor.CheckContext) *doctor.CheckResult {
	res := &doctor.CheckResult{Name: c.Name(), Severity: doctor.SeverityAdvisory}

	scan := tmuxorphan.Scan(c.scanConfig())

	if len(scan.Orphaned) == 0 {
		res.Status = doctor.StatusOK
		if len(scan.Errors) > 0 {
			res.Message = fmt.Sprintf("tmux-orphan: scan incomplete (%d error(s)) -- skipped", len(scan.Errors))
		} else {
			res.Message = "no orphaned tmux server processes found"
		}
		return res
	}

	res.Status = doctor.StatusWarning
	res.Message = fmt.Sprintf("%d orphaned tmux server process(es) found (socket gone, process still running)", len(scan.Orphaned))
	res.FixHint = `run "gc doctor --fix" to terminate them`
	for _, proc := range scan.Orphaned {
		res.Details = append(res.Details, fmt.Sprintf("pid=%d socket=%s", proc.PID, proc.SocketPath))
	}
	return res
}

// Fix terminates every confirmed orphan via tmuxorphan.Reap. Processes
// Scan would skip (no named socket, socket still present, unverifiable)
// are never touched.
func (c *tmuxOrphanCheck) Fix(_ *doctor.CheckContext) error {
	result := tmuxorphan.Reap(tmuxorphan.ReapConfig{
		ScanConfig: c.scanConfig(),
		Terminate:  c.terminate,
	})
	return errors.Join(result.Errors...)
}
