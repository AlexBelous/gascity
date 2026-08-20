package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/doctor"
)

func poolIdleWorkSessionBead(template, state, triggerBeadID string) beads.Bead {
	const id = "SESS-1"
	meta := map[string]string{
		"template":     template,
		"state":        state,
		"session_name": id,
	}
	if triggerBeadID != "" {
		meta["gc.trigger_bead_id"] = triggerBeadID
	}
	return beads.Bead{ID: id, Status: "open", Type: "session", Labels: []string{"gc:session"}, Metadata: meta}
}

func poolIdleWorkRoutedBead(routedTo, assignee string) beads.Bead {
	return beads.Bead{
		ID:       "GA-1",
		Title:    "routed work",
		Type:     "task",
		Status:   "open",
		Assignee: assignee,
		Metadata: map[string]string{"gc.routed_to": routedTo},
	}
}

func TestPoolIdleRoutedWorkCheckWarnsOnIdleInstanceWithUnclaimedRoutedWork(t *testing.T) {
	cityDir := t.TempDir()
	cfg := &config.City{
		Agents: []config.Agent{{Name: "builder", Dir: "gascity"}},
	}
	store := beads.NewMemStoreFrom(0, []beads.Bead{
		poolIdleWorkSessionBead("gascity/builder", "active", ""),
		poolIdleWorkRoutedBead("gascity/builder", ""),
	}, nil)

	result := newPoolIdleRoutedWorkCheck(cfg, cityDir, func(path string) (beads.Store, error) {
		if path != cityDir {
			return nil, fmt.Errorf("unexpected store path %q", path)
		}
		return store, nil
	}).Run(&doctor.CheckContext{})

	if result.Status != doctor.StatusWarning {
		t.Fatalf("status = %v, want warning: %#v", result.Status, result)
	}
	details := strings.Join(result.Details, "\n")
	for _, want := range []string{"gascity/builder", "GA-1", "SESS-1"} {
		if !strings.Contains(details, want) {
			t.Fatalf("details missing %q:\n%s", want, details)
		}
	}
}

func TestPoolIdleRoutedWorkCheckOKWhenNoUnclaimedRoutedWork(t *testing.T) {
	cityDir := t.TempDir()
	cfg := &config.City{
		Agents: []config.Agent{{Name: "builder", Dir: "gascity"}},
	}
	store := beads.NewMemStoreFrom(0, []beads.Bead{
		poolIdleWorkSessionBead("gascity/builder", "active", ""),
	}, nil)

	result := newPoolIdleRoutedWorkCheck(cfg, cityDir, func(_ string) (beads.Store, error) {
		return store, nil
	}).Run(&doctor.CheckContext{})

	if result.Status != doctor.StatusOK {
		t.Fatalf("status = %v, want ok (idle alone is legitimate min-floor capacity): %#v", result.Status, result)
	}
}

func TestPoolIdleRoutedWorkCheckOKWhenNoIdleInstance(t *testing.T) {
	cityDir := t.TempDir()
	cfg := &config.City{
		Agents: []config.Agent{{Name: "builder", Dir: "gascity"}},
	}
	store := beads.NewMemStoreFrom(0, []beads.Bead{
		poolIdleWorkSessionBead("gascity/builder", "active", "GA-9"),
		poolIdleWorkRoutedBead("gascity/builder", ""),
	}, nil)

	result := newPoolIdleRoutedWorkCheck(cfg, cityDir, func(_ string) (beads.Store, error) {
		return store, nil
	}).Run(&doctor.CheckContext{})

	if result.Status != doctor.StatusOK {
		t.Fatalf("status = %v, want ok (every instance already busy): %#v", result.Status, result)
	}
}

func TestPoolIdleRoutedWorkCheckOKWhenOnlyInstanceIsAsleep(t *testing.T) {
	cityDir := t.TempDir()
	cfg := &config.City{
		Agents: []config.Agent{{Name: "builder", Dir: "gascity"}},
	}
	store := beads.NewMemStoreFrom(0, []beads.Bead{
		poolIdleWorkSessionBead("gascity/builder", "asleep", ""),
		poolIdleWorkRoutedBead("gascity/builder", ""),
	}, nil)

	result := newPoolIdleRoutedWorkCheck(cfg, cityDir, func(_ string) (beads.Store, error) {
		return store, nil
	}).Run(&doctor.CheckContext{})

	if result.Status != doctor.StatusOK {
		t.Fatalf("status = %v, want ok (asleep instance is not live): %#v", result.Status, result)
	}
}

func TestPoolIdleRoutedWorkCheckIgnoresClaimedRoutedWork(t *testing.T) {
	cityDir := t.TempDir()
	cfg := &config.City{
		Agents: []config.Agent{{Name: "builder", Dir: "gascity"}},
	}
	store := beads.NewMemStoreFrom(0, []beads.Bead{
		poolIdleWorkSessionBead("gascity/builder", "active", ""),
		poolIdleWorkRoutedBead("gascity/builder", "someone-else"),
	}, nil)

	result := newPoolIdleRoutedWorkCheck(cfg, cityDir, func(_ string) (beads.Store, error) {
		return store, nil
	}).Run(&doctor.CheckContext{})

	if result.Status != doctor.StatusOK {
		t.Fatalf("status = %v, want ok (routed work is already claimed): %#v", result.Status, result)
	}
}

func TestPoolIdleRoutedWorkCheckScansRigScopes(t *testing.T) {
	cityDir := t.TempDir()
	rigDir := t.TempDir()
	cfg := &config.City{
		Agents: []config.Agent{{Name: "builder", Dir: "repo"}},
		Rigs:   []config.Rig{{Name: "repo", Path: rigDir}},
	}
	cityStore := beads.NewMemStoreFrom(0, nil, nil)
	rigStore := beads.NewMemStoreFrom(0, []beads.Bead{
		poolIdleWorkSessionBead("repo/builder", "active", ""),
		poolIdleWorkRoutedBead("repo/builder", ""),
	}, nil)
	stores := map[string]beads.Store{cityDir: cityStore, rigDir: rigStore}

	result := newPoolIdleRoutedWorkCheck(cfg, cityDir, func(path string) (beads.Store, error) {
		store, ok := stores[path]
		if !ok {
			return nil, fmt.Errorf("unexpected store path %q", path)
		}
		return store, nil
	}).Run(&doctor.CheckContext{})

	if result.Status != doctor.StatusWarning {
		t.Fatalf("status = %v, want warning: %#v", result.Status, result)
	}
	details := strings.Join(result.Details, "\n")
	if !strings.Contains(details, "rig repo") {
		t.Fatalf("details missing rig scope label:\n%s", details)
	}
}

func TestPoolIdleRoutedWorkCheckWarnsOnSkippedStoreScopes(t *testing.T) {
	cityDir := t.TempDir()
	rigDir := t.TempDir()
	cfg := &config.City{
		Agents: []config.Agent{{Name: "builder", Dir: "gascity"}},
		Rigs:   []config.Rig{{Name: "repo", Path: rigDir}},
	}

	result := newPoolIdleRoutedWorkCheck(cfg, cityDir, func(path string) (beads.Store, error) {
		switch path {
		case cityDir:
			return nil, errors.New("city offline")
		case rigDir:
			return beads.NewMemStoreFrom(0, nil, nil), nil
		default:
			return nil, fmt.Errorf("unexpected store path %q", path)
		}
	}).Run(&doctor.CheckContext{})

	if result.Status != doctor.StatusWarning {
		t.Fatalf("status = %v, want warning: %#v", result.Status, result)
	}
	details := strings.Join(result.Details, "\n")
	if !strings.Contains(details, "city skipped: opening bead store: city offline") {
		t.Fatalf("details missing skipped-scope note:\n%s", details)
	}
}

func TestPoolIdleRoutedWorkCheckCanFix(t *testing.T) {
	check := newPoolIdleRoutedWorkCheck(&config.City{}, t.TempDir(), nil)
	if check.CanFix() {
		t.Fatal("expected CanFix to return false; this check is detection-only")
	}
}

func TestPoolIdleRoutedWorkCheckFixIsNoop(t *testing.T) {
	cityDir := t.TempDir()
	cfg := &config.City{
		Agents: []config.Agent{{Name: "builder", Dir: "gascity"}},
	}
	store := beads.NewMemStoreFrom(0, []beads.Bead{
		poolIdleWorkSessionBead("gascity/builder", "active", ""),
		poolIdleWorkRoutedBead("gascity/builder", ""),
	}, nil)

	check := newPoolIdleRoutedWorkCheck(cfg, cityDir, func(_ string) (beads.Store, error) {
		return store, nil
	})
	if err := check.Fix(&doctor.CheckContext{}); err != nil {
		t.Fatalf("Fix returned error: %v", err)
	}

	b, err := store.Get("GA-1")
	if err != nil {
		t.Fatalf("loading GA-1: %v", err)
	}
	if b.Assignee != "" {
		t.Fatalf("Fix must not mutate beads; GA-1 assignee = %q", b.Assignee)
	}

	result := check.Run(&doctor.CheckContext{})
	if result.Status != doctor.StatusWarning {
		t.Fatalf("status after no-op Fix = %v, want still warning: %#v", result.Status, result)
	}
}
