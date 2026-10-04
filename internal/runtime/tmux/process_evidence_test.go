package tmux

import (
	"errors"
	"testing"

	"github.com/gastownhall/gascity/internal/runtime"
)

func TestTrackProcessEvidenceRequiresLivePanePID(t *testing.T) {
	for _, mode := range []string{"exact", "wrong pid", "missing pid", "alias", "list failed"} {
		t.Run(mode, func(t *testing.T) {
			roots := []runtime.LiveRuntime{{PID: 42, PPID: 20, SessionID: "sid", City: "/city", Epoch: 2}, {PID: 43, PPID: 1, SessionID: "sid", City: "/city", Epoch: 1}}
			list := func(string) ([]string, error) {
				if mode == "list failed" {
					return nil, errors.New("unavailable")
				}
				if mode == "alias" {
					return []string{"live", "alias"}, nil
				}
				return []string{"live"}, nil
			}
			meta := func(string, string) (string, error) { return "sid", nil }
			pidRead := func(string) (string, error) {
				if mode == "wrong pid" {
					return "44", nil
				}
				if mode == "missing pid" {
					return "", errors.New("unavailable")
				}
				return "42", nil
			}
			got, err := trackProcessRoots(roots, list, meta, pidRead)
			if mode == "missing pid" || mode == "alias" || mode == "list failed" {
				if err == nil {
					t.Fatal("missing/alias pane proof accepted")
				}
				return
			}
			if err != nil || len(got) != 2 || got[0].IsTracked != (mode == "exact") || got[1].IsTracked {
				t.Fatalf("bad attribution %+v %v", got, err)
			}
		})
	}
}
