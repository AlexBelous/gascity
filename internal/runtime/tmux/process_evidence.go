package tmux

import (
	"fmt"
	"strconv"

	"github.com/gastownhall/gascity/internal/runtime"
)

// TrackProcessRoots reads the live pane PID separately from process evidence.
// Matching metadata without the owning pane PID never marks a root tracked.
func (p *Provider) TrackProcessRoots(roots []runtime.LiveRuntime) ([]runtime.LiveRuntime, error) {
	return trackProcessRoots(roots, p.ListRunning, p.GetMeta, p.tm.GetPanePID)
}

func trackProcessRoots(roots []runtime.LiveRuntime, list func(string) ([]string, error), meta func(string, string) (string, error), pidRead func(string) (string, error)) ([]runtime.LiveRuntime, error) {
	names, err := list("")
	if err != nil {
		return nil, fmt.Errorf("tmux live process list unavailable")
	}
	handles := make([]runtime.ProcessHandle, 0, len(names))
	for _, name := range names {
		sid, err := meta(name, "GC_SESSION_ID")
		if err != nil || sid == "" {
			return nil, fmt.Errorf("tmux process SID unavailable")
		}
		raw, err := pidRead(name)
		if err != nil {
			return nil, fmt.Errorf("tmux pane PID unavailable")
		}
		pid, err := strconv.Atoi(raw)
		if err != nil || pid <= 1 {
			return nil, fmt.Errorf("tmux pane PID invalid")
		}
		handles = append(handles, runtime.ProcessHandle{Name: name, SessionID: sid, PID: pid})
	}
	return runtime.BindProcessEvidence(roots, handles)
}
