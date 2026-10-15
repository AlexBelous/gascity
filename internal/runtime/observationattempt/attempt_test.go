package observationattempt

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

type fixtureValue struct {
	Decision Decision
	ID       Identity
}

func TestWholeAttemptCancellationDuringDurableCandidateDoesNotClaimSelection(t *testing.T) {
	h, _ := fixtureHooks()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := NewStore(filepath.Join(t.TempDir(), "evidence"), 4096)
	if err != nil {
		t.Fatal(err)
	}
	h.Retain = func(ctx context.Context, v fixtureValue) (Receipt, error) { return store.Save(ctx, v) }
	var audit Receipt
	h.Record = func(ctx context.Context, failed Receipt, first, candidate Identity, _ fixtureValue) (Receipt, error) {
		var err error
		audit, err = store.SaveCandidate(ctx, failed, first, candidate, strings.Repeat("f", 64))
		cancel() // the real file is durable; the final fence must still refuse
		return audit, err
	}
	result := Run(ctx, h)
	if result.Selected != 0 || result.Reason != "canceled_or_invariant_changed" || !audit.Complete {
		t.Fatal("candidate became an accepted outcome", result.Selected, result.Reason)
	}
	b, err := os.ReadFile(audit.Path)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err = json.Unmarshal(b, &wire); err != nil {
		t.Fatal(err)
	}
	if _, exists := wire["selected_attempt"]; exists || string(wire["candidate_attempt"]) != "2" || string(wire["schema"]) != `"whole-observation-candidate/v1"` {
		t.Fatal("durable audit falsely claims final selection")
	}
}

func fixtureHooks() (Hooks[fixtureValue], *int) {
	clock := time.Now()
	calls := 0
	h := Hooks[fixtureValue]{
		Now: func() time.Time { return clock },
		Collect: func(context.Context) fixtureValue {
			calls++
			clock = clock.Add(time.Second)
			d := TransientCoverage
			if calls == 2 {
				d = Complete
			}
			return fixtureValue{Decision: d, ID: Identity{BindingFingerprint: strings.Repeat("a", 64), ProviderFingerprint: strings.Repeat("b", 64), RootFingerprint: strings.Repeat("e", 64), WholeStartedAt: clock.Add(-time.Second), WholeFinishedAt: clock, Frames: [2]Frame{
				{Nonce: strings.Repeat(string(rune('0'+calls*2-1)), 64), StartedAt: clock.Add(-time.Second), FinishedAt: clock.Add(-time.Second / 2)},
				{Nonce: strings.Repeat(string(rune('0'+calls*2)), 64), StartedAt: clock.Add(-time.Second / 2), FinishedAt: clock},
			}}}
		},
		Validate:  func(v fixtureValue) Decision { return v.Decision },
		Identity:  func(v fixtureValue) Identity { return v.ID },
		Invariant: func(context.Context) error { return nil },
		Retain: func(context.Context, fixtureValue) (Receipt, error) {
			return Receipt{Path: "/private/fixture", SHA256: strings.Repeat("c", 64), Bytes: 10, Complete: true}, nil
		},
		Record: func(context.Context, Receipt, Identity, Identity, fixtureValue) (Receipt, error) {
			return Receipt{Path: "/private/fixture.result", SHA256: strings.Repeat("f", 64), Bytes: 100, Complete: true}, nil
		},
	}
	return h, &calls
}

func TestWholeAttemptProviderTransactionsCannotOverlap(t *testing.T) {
	h, calls := fixtureHooks()
	collect := h.Collect
	h.Now = func() time.Time { return time.Now().Add(time.Minute) }
	h.Collect = func(ctx context.Context) fixtureValue {
		v := collect(ctx)
		if *calls == 1 {
			// The provider's after-read completes later than its second helper.
			// Fresh nonces and disjoint helper pairs alone are insufficient.
			v.ID.WholeFinishedAt = v.ID.WholeFinishedAt.Add(time.Millisecond)
		}
		return v
	}
	if got := Run(context.Background(), h); got.Selected != 0 || got.Reason != "attempt_identity_changed" {
		t.Fatal("overlapping provider transaction selected", got.Selected, got.Reason)
	}
}

func TestWholeAttemptCoordinator(t *testing.T) {
	for _, mode := range []string{"complete-first", "complete-second", "both-partial", "fatal-first", "retain-fails", "provider-changed", "binding-changed", "roots-changed", "record-fails", "replay", "invariant-changed"} {
		t.Run(mode, func(t *testing.T) {
			h, calls := fixtureHooks()
			collect := h.Collect
			h.Collect = func(ctx context.Context) fixtureValue {
				v := collect(ctx)
				switch mode {
				case "complete-first":
					v.Decision = Complete
				case "both-partial":
					v.Decision = TransientCoverage
				case "fatal-first":
					v.Decision = Fatal
				case "provider-changed":
					if *calls == 2 {
						v.ID.ProviderFingerprint = strings.Repeat("d", 64)
					}
				case "binding-changed":
					if *calls == 2 {
						v.ID.BindingFingerprint = strings.Repeat("d", 64)
					}
				case "roots-changed":
					if *calls == 2 {
						v.ID.RootFingerprint = strings.Repeat("d", 64)
					}
				case "replay":
					if *calls == 2 {
						v.ID.Frames[0].Nonce = strings.Repeat("1", 64)
					}
				}
				return v
			}
			if mode == "retain-fails" {
				h.Retain = func(context.Context, fixtureValue) (Receipt, error) { return Receipt{}, errors.New("not logged") }
			}
			if mode == "record-fails" {
				h.Record = func(context.Context, Receipt, Identity, Identity, fixtureValue) (Receipt, error) {
					return Receipt{}, errors.New("fixture record refused")
				}
			}
			if mode == "invariant-changed" {
				h.Invariant = func(context.Context) error {
					if *calls > 0 {
						return errors.New("not logged")
					}
					return nil
				}
			}
			got := Run(context.Background(), h)
			want := 0
			if mode == "complete-first" {
				want = 1
			}
			if mode == "complete-second" {
				want = 2
			}
			if got.Selected != want || *calls > 2 {
				t.Fatalf("mode=%s calls=%d result=%+v", mode, *calls, got)
			}
			if (mode == "complete-first" || mode == "fatal-first" || mode == "retain-fails" || mode == "invariant-changed") && *calls != 1 {
				t.Fatal("unexpected second attempt")
			}
		})
	}
}

func TestWholeAttemptDoesNotPromoteAfterValidatorCancellation(t *testing.T) {
	h, _ := fixtureHooks()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.Validate = func(fixtureValue) Decision { cancel(); return Complete }
	if got := Run(ctx, h); got.Selected != 0 {
		t.Fatal("cancellation during validation promoted", got.Selected)
	}
}

func TestWholeAttemptOneDeadlineAndNoReset(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, calls := fixtureHooks()
		h.Now = time.Now
		collect := h.Collect
		var deadline time.Time
		h.Collect = func(ctx context.Context) fixtureValue {
			d, ok := ctx.Deadline()
			if !ok {
				t.Fatal("no deadline")
			}
			if deadline.IsZero() {
				deadline = d
			} else if deadline != d {
				t.Fatal("deadline reset")
			}
			<-time.After(13 * time.Second)
			v := collect(ctx)
			v.ID.WholeStartedAt = time.Now().Add(-time.Second)
			v.ID.WholeFinishedAt = time.Now()
			v.ID.Frames[0].StartedAt = time.Now().Add(-time.Second)
			v.ID.Frames[0].FinishedAt = time.Now().Add(-time.Second / 2)
			v.ID.Frames[1].StartedAt = time.Now().Add(-time.Second / 2)
			v.ID.Frames[1].FinishedAt = time.Now()
			return v
		}
		got := Run(context.Background(), h)
		if got.Selected != 0 || *calls != 2 {
			t.Fatalf("deadline result %+v calls%d", got, *calls)
		}
	})
}
