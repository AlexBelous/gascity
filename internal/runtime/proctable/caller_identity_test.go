package proctable

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func callerChainFixture() (CallerAnchor, map[int]CallerIncarnation) {
	env, _ := callerStrictNUL([]byte("GC_SESSION_ID=fixture\x00GC_INSTANCE_TOKEN=fixture-only\x00BEADS_HOLDER_TOKEN=fixture-only\x00"))
	env.complete = true // injected dedicated-region fixture; raw syntax alone cannot grant this.
	uid := CallerUIDs{Real: 1000, Effective: 1000, Saved: 1000}
	anchor := CallerAnchor{PID: 10, Start: "100", Domain: "fixture-domain", Platform: "fixture-platform", UIDs: uid}
	rows := map[int]CallerIncarnation{
		11: {PID: 11, PPID: 10, PGID: 10, Start: "110", Domain: anchor.Domain, Platform: anchor.Platform, UIDs: uid, Environment: env},
		10: {PID: 10, PPID: 1, PGID: 10, Start: "100", Domain: anchor.Domain, Platform: anchor.Platform, UIDs: uid, Environment: env},
	}
	return anchor, rows
}

func TestCallerChainBoundedInjected(t *testing.T) {
	anchor, rows := callerChainFixture()
	read := func(_ context.Context, pid int) (CallerIncarnation, error) {
		p, ok := rows[pid]
		if !ok {
			return CallerIncarnation{}, errors.New("fixture error")
		}
		return p, nil
	}
	chain, err := readCallerIncarnations(context.Background(), 11, anchor, read)
	if err != nil || len(chain) != 2 {
		t.Fatal("known injected chain refused")
	}
	if chain[1].UIDs.HasFileSystem {
		t.Fatal("unmeasured fsuid invented")
	}
	if got := fmt.Sprintf("%#v", chain[0]); got == "" {
		t.Fatal("empty diagnostic")
	}
	cases := []string{"missing", "cycle", "uid", "domain", "anchor-start", "reread-start", "reread-env", "canceled"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			a, m := callerChainFixture()
			calls := 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch name {
			case "missing":
				delete(m, 10)
			case "cycle":
				p := m[11]
				p.PPID = 11
				m[11] = p
			case "uid":
				p := m[11]
				p.UIDs.Real++
				m[11] = p
			case "domain":
				p := m[11]
				p.Domain = "other"
				m[11] = p
			case "anchor-start":
				a.Start = "200"
			case "canceled":
				cancel()
			}
			r := func(_ context.Context, pid int) (CallerIncarnation, error) {
				calls++
				p, ok := m[pid]
				if !ok {
					return CallerIncarnation{}, errCallerUnknown
				}
				if calls == 3 && name == "reread-start" {
					p.Start = "different"
				}
				if calls == 3 && name == "reread-env" {
					p.Environment, _ = callerStrictNUL([]byte("GC_SESSION_ID=other\x00"))
				}
				return p, nil
			}
			if _, err := readCallerIncarnations(ctx, 11, a, r); err == nil {
				t.Fatal("unavailable/mutated chain accepted")
			}
		})
	}
}

func TestCallerSyntaxAloneNeverCompleteness(t *testing.T) {
	a, rows := callerChainFixture()
	raw, err := callerStrictNUL([]byte("GC_SESSION_ID=fixture\x00GC_INSTANCE_TOKEN=fixture-only\x00BEADS_HOLDER_TOKEN=fixture-only\x00"))
	if err != nil {
		t.Fatal("fixture syntax invalid")
	}
	p := rows[11]
	p.Environment = raw
	rows[11] = p
	if _, err := readCallerIncarnations(context.Background(), 11, a, func(_ context.Context, pid int) (CallerIncarnation, error) { return rows[pid], nil }); err == nil {
		t.Fatal("syntax-only ENV became completeness proof")
	}
}

func TestCallerFinalReadCancellationDeniesSuccess(t *testing.T) {
	a, rows := callerChainFixture()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	read := func(_ context.Context, pid int) (CallerIncarnation, error) {
		calls++
		if calls == 4 {
			cancel()
		}
		return rows[pid], nil
	}
	if _, err := readCallerIncarnations(ctx, 11, a, read); err == nil {
		t.Fatal("last-read cancellation returned success")
	}
}
