package tmux

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/gastownhall/gascity/internal/runtime"
)

// recordingWriteCloser captures the keystrokes gc injects into a hidden attach
// client so a test can confirm the hidden-injection branch actually ran.
type recordingWriteCloser struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (w *recordingWriteCloser) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *recordingWriteCloser) Close() error { return nil }

func (w *recordingWriteCloser) written() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// TestNudgeNowNeverBypassesTheBoundFenceThroughHiddenAttach ensures the generic
// nudge API never writes directly to a hidden client. That write cannot be
// atomically conditioned on tmux's attachment/copy-mode state, so it must park
// just like a human-attached session.
func TestNudgeNowNeverBypassesTheBoundFenceThroughHiddenAttach(t *testing.T) {
	const sess = "hidden-attach-nudge"
	fe := &fakeExecutor{outs: []string{
		"$1\t" + sess + "\t@1\t%1\t0\t0\t0",
		"$1\t" + sess + "\t@1\t%1\tsh\t123",
		"$1\t" + sess + "\t@1\t%1",
		"",
		"GC_PROVIDER=codex",
		"123",
		boundInputFenceMarker,
	}}
	tm := NewTmux()
	tm.exec = fe

	sink := &recordingWriteCloser{}
	tm.hiddenAttachMu.Lock()
	tm.hiddenAttachClients = map[string]*hiddenAttachClient{
		sess: {stdin: sink},
	}
	tm.hiddenAttachMu.Unlock()

	p := &Provider{tm: tm}
	if err := p.NudgeNow(sess, runtime.TextContent("/rewind")); !errors.Is(err, runtime.ErrInputFenced) {
		t.Fatalf("NudgeNow = %v, want ErrInputFenced", err)
	}
	if got := sink.written(); got != "" {
		t.Fatalf("hidden client received %q, want zero input while fenced", got)
	}
	tm.pokeMu.Lock()
	_, ok := tm.pokes[sess]
	tm.pokeMu.Unlock()
	if ok {
		t.Fatal("fenced nudge recorded a poke without delivering input")
	}
}

func TestHiddenAttachedRewindUsesProviderNativeInput(t *testing.T) {
	const sess = "hidden-attach-rewind"
	fe := &fakeExecutor{outs: []string{"123"}}
	tm := NewTmux()
	tm.exec = fe
	tm.cfg.DebounceMs = 0
	sink := &recordingWriteCloser{}
	tm.hiddenAttachMu.Lock()
	tm.hiddenAttachClients = map[string]*hiddenAttachClient{sess: {stdin: sink}}
	tm.hiddenAttachMu.Unlock()

	if err := tm.sendHiddenAttachedRewind(sess); err != nil {
		t.Fatalf("sendHiddenAttachedRewind: %v", err)
	}
	if got := sink.written(); got != "/rewind\r" {
		t.Fatalf("hidden client received %q, want one native rewind submission", got)
	}
	if got := len(fe.calls); got != 1 {
		t.Fatalf("tmux calls = %#v, want only the activity observation and no tmux send-keys", fe.calls)
	}
}
