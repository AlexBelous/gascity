package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/api/apierr"
	"github.com/gastownhall/gascity/internal/events"
)

// blockingListProvider is a minimal events.Provider whose List blocks until
// ctx is done, then returns ctx.Err(). It implements neither
// [events.TailProvider] nor [events.InFlightProvider], so
// fetchEventPageAscending always falls through to the plain
// ep.List(ctx, filter) call -- this proves humaHandleEventList's ctx reaches
// the provider at all. TestHumaHandleEventListAbortsFallbackArchiveScan below
// proves the stronger, literal claim: a real multi-archive FileRecorder scan
// aborts partway rather than running to completion.
type blockingListProvider struct{}

func (blockingListProvider) Record(events.Event) {}

func (blockingListProvider) List(ctx context.Context, _ events.Filter) ([]events.Event, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(2 * time.Second):
		// Safety net: a GREEN wiring bug that never threads ctx through
		// must fail the test fast instead of hanging the suite forever.
		return nil, errors.New("blockingListProvider: List did not observe ctx cancellation within 2s")
	}
}

func (blockingListProvider) LatestSeq() (uint64, error) { return 0, nil }

func (blockingListProvider) Watch(_ context.Context, _ uint64) (events.Watcher, error) {
	return nil, errors.New("blockingListProvider: Watch not implemented")
}

func (blockingListProvider) Close() error { return nil }

func TestHumaHandleEventListAbortsOnContextCancel(t *testing.T) {
	fs := newFakeState(t)
	fs.eventProv = blockingListProvider{}
	s := &Server{state: fs}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled -- List must observe this, not block forever.

	_, err := s.humaHandleEventList(ctx, &EventListInput{})
	if err == nil {
		t.Fatal("expected an error from a canceled context, got nil")
	}
	em, ok := err.(*apierr.ErrorModel)
	if !ok {
		t.Fatalf("err = %T (%v), want *apierr.ErrorModel", err, err)
	}
	if !strings.Contains(em.Detail, "context canceled") {
		t.Errorf("detail = %q, want it to mention context cancellation", em.Detail)
	}
}

// countingCancelContext cancels its embedded context the Nth time Err is
// called, letting a test pin cancellation to a specific point in a
// multi-archive scan loop instead of racing a background timer. Duplicated
// from eventstest (internal/events/eventstest/conformance.go) because that
// helper is unexported and eventstest is a separate package.
type countingCancelContext struct {
	context.Context
	cancel   context.CancelFunc
	cancelAt int
	calls    int
}

func (c *countingCancelContext) Err() error {
	c.calls++
	if c.calls == c.cancelAt {
		c.cancel()
	}
	return c.Context.Err()
}

// newCancelAfter returns a context that becomes canceled on the Nth call to
// Err(), so a test can prove a scan loop checks ctx.Err() once per iteration
// rather than only once at entry.
func newCancelAfter(n int) *countingCancelContext {
	ctx, cancel := context.WithCancel(context.Background())
	return &countingCancelContext{Context: ctx, cancel: cancel, cancelAt: n}
}

// TestHumaHandleEventListAbortsFallbackArchiveScan is the literal end-to-end
// proof the exit contract asks for: a real *events.FileRecorder with 3
// rotated archives, reached through the actual Huma handler, aborts its
// archive scan when ctx is canceled partway rather than running to
// completion. fetchEventPageAscending and listWithInFlight are pure routing
// -- neither calls ctx.Err() itself -- so cancel-on-2nd-call lands at the
// same point in FileRecorder.List's scan loop as it does in eventstest's
// ListAbortsBetweenArchives, proving the two extra layers forward ctx rather
// than dropping it. The active log is left empty by the rotation loop below,
// so ListTail's fast path (active-file-only) comes up short and
// fetchEventPageAscending must fall through to the full archive scan.
func TestHumaHandleEventListAbortsFallbackArchiveScan(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	var stderr bytes.Buffer
	rec, err := events.NewFileRecorder(path, &stderr)
	if err != nil {
		t.Fatalf("NewFileRecorder: %v", err)
	}
	defer rec.Close() //nolint:errcheck // test cleanup

	const archives, perArchive = 3, 2
	for r := 0; r < archives; r++ {
		for i := 0; i < perArchive; i++ {
			rec.Record(events.Event{Type: events.BeadCreated, Actor: "human", Subject: fmt.Sprintf("r%d-%d", r, i)})
		}
		res, rotErr := rec.ForceRotate()
		if rotErr != nil {
			t.Fatalf("ForceRotate r=%d: %v", r, rotErr)
		}
		if !res.Rotated {
			t.Fatalf("ForceRotate r=%d did not rotate a non-empty log", r)
		}
		if res.Done != nil {
			<-res.Done
		}
	}

	fs := newFakeState(t)
	fs.eventProv = rec
	s := &Server{state: fs}

	// Err() call #1 fires before archive 1 is processed (not yet canceled).
	// Call #2 fires before archive 2 -- that's where this cancels.
	ctx := newCancelAfter(2)
	_, err = s.humaHandleEventList(ctx, &EventListInput{})
	if err == nil {
		t.Fatal("expected an error from a context canceled partway through the archive scan, got nil")
	}
	em, ok := err.(*apierr.ErrorModel)
	if !ok {
		t.Fatalf("err = %T (%v), want *apierr.ErrorModel", err, err)
	}
	if !strings.Contains(em.Detail, "context canceled") {
		t.Errorf("detail = %q, want it to mention context cancellation", em.Detail)
	}
}
