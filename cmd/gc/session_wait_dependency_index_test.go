package main

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

func TestSessionWaitDependencyIndex_IndexesExactDependenciesDeterministically(t *testing.T) {
	index := newSessionWaitDependencyIndex()

	if err := index.Replace(sessionpkg.WaitInfo{
		ID:        "wait-a",
		SessionID: "session-a",
		Status:    "open",
		Kind:      "deps",
		State:     "pending",
		DepMode:   "all",
		DepIDs:    []string{"dep-x", "dep-y", "dep-y"},
	}); err != nil {
		t.Fatalf("Replace(wait-a): %v", err)
	}
	if err := index.Replace(sessionpkg.WaitInfo{
		ID:        "wait-b",
		SessionID: "session-b",
		Status:    "open",
		Kind:      "deps",
		State:     "pending",
		DepMode:   "any",
		DepIDs:    []string{"dep-y"},
	}); err != nil {
		t.Fatalf("Replace(wait-b): %v", err)
	}
	if err := index.Replace(sessionpkg.WaitInfo{
		ID:        "wait-c",
		SessionID: "session-a",
		Status:    "open",
		Kind:      "deps",
		State:     "pending",
		DepMode:   "all",
		DepIDs:    []string{"dep-y"},
	}); err != nil {
		t.Fatalf("Replace(wait-c): %v", err)
	}

	assertSessionWaitDependencyIndexSessions(t, index.SessionsForDependency("dep-x"), []string{"session-a"})
	assertSessionWaitDependencyIndexSessions(t, index.SessionsForDependency("dep-y"), []string{"session-a", "session-b"})
	assertSessionWaitDependencyIndexSessions(t, index.SessionsForDependency("dep-unrelated"), nil)

	returned := index.SessionsForDependency("dep-y")
	returned[0] = "mutated"
	assertSessionWaitDependencyIndexSessions(t, index.SessionsForDependency("dep-y"), []string{"session-a", "session-b"})
}

func TestSessionWaitDependencyIndex_ReplaceRemovesOnlyPriorWaitEdges(t *testing.T) {
	index := newSessionWaitDependencyIndex()
	for _, wait := range []sessionpkg.WaitInfo{
		{
			ID:        "wait-a",
			SessionID: "session-a",
			Status:    "open",
			Kind:      "deps",
			State:     "pending",
			DepMode:   "all",
			DepIDs:    []string{"dep-x", "dep-y"},
		},
		{
			ID:        "wait-b",
			SessionID: "session-b",
			Status:    "open",
			Kind:      "deps",
			State:     "pending",
			DepMode:   "any",
			DepIDs:    []string{"dep-y"},
		},
	} {
		if err := index.Replace(wait); err != nil {
			t.Fatalf("Replace(%s): %v", wait.ID, err)
		}
	}

	if err := index.Replace(sessionpkg.WaitInfo{
		ID:        "wait-a",
		SessionID: "session-a",
		Status:    "open",
		Kind:      "deps",
		State:     "pending",
		DepMode:   "all",
		DepIDs:    []string{"dep-z", "dep-z"},
	}); err != nil {
		t.Fatalf("Replace(replacement): %v", err)
	}

	assertSessionWaitDependencyIndexSessions(t, index.SessionsForDependency("dep-x"), nil)
	assertSessionWaitDependencyIndexSessions(t, index.SessionsForDependency("dep-y"), []string{"session-b"})
	assertSessionWaitDependencyIndexSessions(t, index.SessionsForDependency("dep-z"), []string{"session-a"})
}

func TestSessionWaitDependencyIndex_NonIndexableReplacementAndRemoveDiscardPriorRegistration(t *testing.T) {
	index := newSessionWaitDependencyIndex()
	valid := sessionpkg.WaitInfo{
		ID:        "wait-a",
		SessionID: "session-a",
		Status:    "open",
		Kind:      "deps",
		State:     "pending",
		DepMode:   "all",
		DepIDs:    []string{"dep-x"},
	}
	if err := index.Replace(valid); err != nil {
		t.Fatalf("Replace(valid): %v", err)
	}
	if err := index.Replace(sessionpkg.WaitInfo{
		ID:        "wait-b",
		SessionID: "session-b",
		Status:    "open",
		Kind:      "deps",
		State:     "pending",
		DepMode:   "all",
		DepIDs:    []string{"dep-x"},
	}); err != nil {
		t.Fatalf("Replace(wait-b): %v", err)
	}

	for _, replacement := range []sessionpkg.WaitInfo{
		{ID: "wait-a", Status: "open", Kind: "deps", State: waitStateReady},
		{ID: "wait-a", Status: "open", Kind: "deps", State: "canceled"},
		{ID: "wait-a", Status: "closed", Kind: "deps", State: waitStatePending},
		{ID: "wait-a", Status: "open", Kind: "probe", State: waitStatePending},
	} {
		if err := index.Replace(replacement); err != nil {
			t.Fatalf("Replace(non-indexable): %v", err)
		}
		assertSessionWaitDependencyIndexSessions(t, index.SessionsForDependency("dep-x"), []string{"session-b"})
		if err := index.Replace(valid); err != nil {
			t.Fatalf("Replace(valid again): %v", err)
		}
	}

	index.Remove("wait-a")
	assertSessionWaitDependencyIndexSessions(t, index.SessionsForDependency("dep-x"), []string{"session-b"})
}

func TestSessionWaitDependencyIndex_MalformedIndexableReplacementPreservesPriorRegistration(t *testing.T) {
	index := newSessionWaitDependencyIndex()
	valid := sessionpkg.WaitInfo{
		ID:        "wait-a",
		SessionID: "session-a",
		Status:    "open",
		Kind:      "deps",
		State:     "pending",
		DepMode:   "all",
		DepIDs:    []string{"dep-x"},
	}
	if err := index.Replace(valid); err != nil {
		t.Fatalf("Replace(valid): %v", err)
	}

	for _, malformed := range []struct {
		wait      sessionpkg.WaitInfo
		errorText string
	}{
		{
			wait: sessionpkg.WaitInfo{
				ID:        "wait-a",
				SessionID: "",
				Status:    "open",
				Kind:      "deps",
				State:     waitStatePending,
				DepMode:   "all",
				DepIDs:    []string{"dep-y"},
			},
			errorText: "session ID",
		},
		{
			wait: sessionpkg.WaitInfo{
				ID:        "wait-a",
				SessionID: "session-a",
				Status:    "open",
				Kind:      "deps",
				State:     waitStatePending,
				DepMode:   "invalid",
				DepIDs:    []string{"dep-y"},
			},
			errorText: "invalid dependency mode",
		},
		{
			wait: sessionpkg.WaitInfo{
				ID:        "wait-a",
				SessionID: "session-a",
				Status:    "open",
				Kind:      "deps",
				State:     "unknown-state",
				DepMode:   "all",
				DepIDs:    []string{"dep-y"},
			},
			errorText: "unknown-state",
		},
		{
			wait: sessionpkg.WaitInfo{
				ID:        "wait-a",
				SessionID: "session-a",
				Status:    "unknown-status",
				Kind:      "deps",
				State:     waitStatePending,
				DepMode:   "all",
				DepIDs:    []string{"dep-y"},
			},
			errorText: "unknown-status",
		},
	} {
		err := index.Replace(malformed.wait)
		if err == nil || !strings.Contains(err.Error(), malformed.errorText) {
			t.Fatalf("Replace(malformed) error = %v, want context containing %q", err, malformed.errorText)
		}
		assertSessionWaitDependencyIndexSessions(t, index.SessionsForDependency("dep-x"), []string{"session-a"})
		assertSessionWaitDependencyIndexSessions(t, index.SessionsForDependency("dep-y"), nil)
	}
}

func TestSessionWaitDependencyIndex_SameSessionRequiresEveryWaitRegistrationToLeave(t *testing.T) {
	index := newSessionWaitDependencyIndex()
	for _, waitID := range []string{"wait-a", "wait-b"} {
		if err := index.Replace(sessionpkg.WaitInfo{
			ID:        waitID,
			SessionID: "session-a",
			Status:    "open",
			Kind:      "deps",
			State:     waitStatePending,
			DepMode:   "all",
			DepIDs:    []string{"dep-x"},
		}); err != nil {
			t.Fatalf("Replace(%s): %v", waitID, err)
		}
	}

	if err := index.Replace(sessionpkg.WaitInfo{
		ID:     "wait-a",
		Status: "open",
		Kind:   "deps",
		State:  waitStateReady,
	}); err != nil {
		t.Fatalf("Replace(ready): %v", err)
	}
	assertSessionWaitDependencyIndexSessions(t, index.SessionsForDependency("dep-x"), []string{"session-a"})

	index.Remove("wait-b")
	assertSessionWaitDependencyIndexSessions(t, index.SessionsForDependency("dep-x"), nil)
}

func TestSessionWaitDependencyIndex_ConcurrentReplaceRemoveAndLookup(t *testing.T) {
	index := newSessionWaitDependencyIndex()
	const workers = 8
	const iterations = 40

	errs := make(chan error, workers)
	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		group.Add(1)
		go func(worker int) {
			defer group.Done()
			waitID := fmt.Sprintf("wait-%d", worker)
			sessionID := fmt.Sprintf("session-%d", worker)
			for iteration := 0; iteration < iterations; iteration++ {
				if err := index.Replace(sessionpkg.WaitInfo{
					ID:        waitID,
					SessionID: sessionID,
					Status:    "open",
					Kind:      "deps",
					State:     waitStatePending,
					DepMode:   "all",
					DepIDs:    []string{"dep-x"},
				}); err != nil {
					errs <- err
					return
				}
				if iteration%2 == 0 {
					index.Remove(waitID)
				}
				_ = index.SessionsForDependency("dep-x")
			}
		}(worker)
	}
	group.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Replace: %v", err)
	}
}

func assertSessionWaitDependencyIndexSessions(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sessions = %v, want %v", got, want)
	}
}
