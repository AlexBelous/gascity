package tmux

import (
	"context"
	"os/exec"
	"time"

	"github.com/gastownhall/gascity/internal/runtime"
)

// ReadSessionAuthoritySnapshot uses the actual tmux socket and preserves raw
// whitespace/line framing. Legacy GetMeta/GetAllEnvironment/executeCtx remain
// unchanged; their error suppression/TrimSpace cannot grant this authority.
func (p *Provider) ReadSessionAuthoritySnapshot(ctx context.Context) ([]runtime.SessionAuthorityHandle, error) {
	if ctx == nil || p == nil || p.tm == nil {
		return nil, errSessionAuthoritySnapshot
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return readAuthoritySnapshot(ctx, p.authorityCommand)
}

// ReadSessionAuthorityTarget uses the persisted handle and SID to select one
// live tmux session without depending on unrelated sessions' private ENV.
func (p *Provider) ReadSessionAuthorityTarget(ctx context.Context, handle, sid string) (runtime.SessionAuthorityHandle, error) {
	if ctx == nil || p == nil || p.tm == nil {
		return runtime.SessionAuthorityHandle{}, errSessionAuthoritySnapshot
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return readAuthorityTarget(ctx, handle, sid, p.authorityCommand)
}

func (p *Provider) authorityCommand(ctx context.Context, args ...string) ([]byte, error) {
	all := []string{"-u"}
	if p.tm.cfg.SocketName != "" {
		all = append(all, "-L", p.tm.cfg.SocketName)
	}
	all = append(all, args...)
	cmd := exec.CommandContext(ctx, "tmux", all...)
	// No ENV values or stderr enter diagnostics, trace, argv, or artifacts.
	stdout := &authorityBoundedBuffer{limit: 65536}
	stderr := &authorityBoundedBuffer{limit: 1024}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if err != nil || ctx.Err() != nil || stdout.oversize {
		return nil, errSessionAuthoritySnapshot
	}
	return append([]byte(nil), stdout.data...), nil
}

type authorityBoundedBuffer struct {
	data     []byte
	limit    int
	oversize bool
}

func (b *authorityBoundedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	room := b.limit - len(b.data)
	if n > room {
		b.oversize = true
		b.data = append(b.data, p[:room]...)
	} else {
		b.data = append(b.data, p...)
	}
	return n, nil
}
