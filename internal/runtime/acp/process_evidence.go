package acp

import (
	"bufio"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/gastownhall/gascity/internal/runtime"
)

// TrackProcessRoots reads each live control socket's owner PID, without reading
// the process environment again or trusting inherited ACP socket markers.
func (p *Provider) TrackProcessRoots(roots []runtime.LiveRuntime) ([]runtime.LiveRuntime, error) {
	names, err := p.ListRunning("")
	if err != nil {
		return nil, fmt.Errorf("ACP live process list unavailable")
	}
	handles := make([]runtime.ProcessHandle, 0, len(names))
	for _, name := range names {
		sid, err := p.GetMeta(name, "GC_SESSION_ID")
		if err != nil || sid == "" {
			return nil, fmt.Errorf("ACP process SID unavailable")
		}
		pid, err := p.observedOwnerPID(name)
		if err != nil {
			return nil, err
		}
		handles = append(handles, runtime.ProcessHandle{Name: name, SessionID: sid, PID: pid})
	}
	return runtime.BindProcessEvidence(roots, handles)
}

func (p *Provider) observedOwnerPID(name string) (int, error) {
	for _, path := range []string{p.sockPath(name), p.legacySockPath(name)} {
		conn, err := net.DialTimeout("unix", path, ownerDialTimeout)
		if err != nil {
			continue
		}
		if err = conn.SetDeadline(time.Now().Add(ownerDialTimeout)); err != nil {
			_ = conn.Close()
			continue
		}
		_, err = conn.Write([]byte("pid\n"))
		if err != nil {
			_ = conn.Close()
			continue
		}
		scanner := bufio.NewScanner(conn)
		scanner.Buffer(make([]byte, 32), 64)
		ok := scanner.Scan()
		raw := scanner.Text()
		_ = conn.Close()
		if !ok {
			continue
		}
		pid, err := strconv.Atoi(raw)
		if err == nil && pid > 1 {
			return pid, nil
		}
	}
	return 0, fmt.Errorf("ACP live owner PID unavailable")
}
