package main

import (
	"bytes"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

type containmentProjectionSource struct {
	beads.Store
	rows       []beads.ClassificationRow
	err        error
	reads      int
	fullReads  int
	refuseFull bool
}

func (s *containmentProjectionSource) ReadClassification() ([]beads.ClassificationRow, error) {
	s.reads++
	return s.rows, s.err
}

func (s *containmentProjectionSource) DepMetadata(id, parent string) (string, bool, error) {
	return s.Store.(beads.DepMetadataReader).DepMetadata(id, parent)
}

func (s *containmentProjectionSource) List(q beads.ListQuery) ([]beads.Bead, error) {
	s.fullReads++
	if s.refuseFull {
		return nil, errors.New("containment attempted full source hydration")
	}
	return s.Store.List(q)
}

// A new stranded row must be noticed on the next call through either policy wrapper.
func TestInfraContainmentUsesLiveClassificationThroughPolicy(t *testing.T) {
	for _, graph := range []bool{false, true} {
		t.Run(map[bool]string{false: "policy", true: "graph policy"}[graph], func(t *testing.T) {
			city, _, _, target := convergedInfraCity(t)
			source := &containmentProjectionSource{Store: beads.NewMemStore(), refuseFull: true}
			policy := &beadPolicyStore{Store: source}
			var wrapped beads.Store = policy
			if graph {
				wrapped = &beadPolicyGraphStore{beadPolicyStore: policy}
			}
			failInfraMigrationSourceWith(t, func(string) (beads.Store, error) { return wrapped, nil })
			gap, err := classifyInfraContainmentGap(city, target, nil)
			if err != nil || len(gap.Stranded) != 0 {
				t.Fatalf("empty census: %+v %v", gap, err)
			}
			source.rows = []beads.ClassificationRow{
				{ID: "stranded", Type: "task", Metadata: map[string]string{"gc.root_bead_id": "root"}},
				{ID: "queue", Type: "task", Labels: []string{"gc:nudge-queue"}},
				{ID: "synthetic", Type: "convoy", Metadata: map[string]string{"gc.root_bead_id": "root", "gc.synthetic": "true"}},
				{ID: "work", Type: "task"},
			}
			gap, err = classifyInfraContainmentGap(city, target, nil)
			if err != nil || !reflect.DeepEqual(gap.Stranded, []string{"queue", "stranded"}) {
				t.Fatalf("fresh classification: %+v %v", gap, err)
			}
			if source.reads != 2 || source.fullReads != 0 {
				t.Fatalf("reads projection=%d full=%d", source.reads, source.fullReads)
			}
			source.err = errors.New("classification read broke")
			if _, err = classifyInfraContainmentGap(city, target, nil); !errors.Is(err, source.err) {
				t.Fatalf("read error accepted or lost: %v", err)
			}
		})
	}
}

// A projection-capable source must still provide complete rows to the actual copy.
func TestInfraMigrationDoesNotUseClassificationPayload(t *testing.T) {
	city := t.TempDir()
	base := stubInfraMigrationSource(t)
	row := mustCreateInfraBead(t, base, beads.Bead{Title: "full migration row", Type: "session", Description: "body must survive", Labels: []string{"gc:session"}, Metadata: map[string]string{"opaque": "preserve"}})
	source := &containmentProjectionSource{Store: base, err: errors.New("projection must not run in migration")}
	failInfraMigrationSourceWith(t, func(string) (beads.Store, error) { return source, nil })
	cfg := infraSplitConfig(filepath.Join(city, ".gc", "store"))
	var log bytes.Buffer
	if got := migrateInfraClasses(t, city, cfg, &log); got.Outcome != infraMigrationConverged {
		t.Fatalf("migration=%+v log=%s", got, log.String())
	}
	dest := openMigratedDestination(t, mustResolveInfraTarget(t, city, cfg))
	got, err := dest.Get(row.ID)
	if err != nil || got.Description != row.Description || got.Title != row.Title || got.Metadata["opaque"] != "preserve" {
		t.Fatalf("payload lost: %#v %v", got, err)
	}
	if source.reads != 0 || source.fullReads == 0 {
		t.Fatalf("migration reads projection=%d full=%d", source.reads, source.fullReads)
	}
}

// An unsupported optional reader retains the original complete-read path.
func TestInfraContainmentClassificationUnsupportedFallsBack(t *testing.T) {
	city, _, base, target := convergedInfraCity(t)
	row := mustCreateInfraBead(t, base, beads.Bead{Title: "stranded fallback", Type: "session"})
	source := &containmentProjectionSource{Store: base, err: beads.ErrClassificationUnsupported}
	wrapped := wrapStoreWithBeadPolicies(source, &config.City{})
	failInfraMigrationSourceWith(t, func(string) (beads.Store, error) { return wrapped, nil })
	gap, err := classifyInfraContainmentGap(city, target, nil)
	if err != nil || !reflect.DeepEqual(gap.Stranded, []string{row.ID}) {
		t.Fatalf("fallback=%+v %v", gap, err)
	}
}

type containmentBorrowedSource struct {
	*containmentProjectionSource
	closes   int
	closeErr error
}

func (s *containmentBorrowedSource) CloseStore() error { s.closes++; return s.closeErr }

// Borrowing is synchronous and never owns the source, including error exits.
func TestInfraContainmentBorrowedSourceStaysLiveAndOwnedByCaller(t *testing.T) {
	source := &containmentBorrowedSource{containmentProjectionSource: &containmentProjectionSource{Store: beads.NewMemStore(), refuseFull: true}}
	if _, err := classifyInfraContainmentGapFromSource(source, infraBindingTarget{}, nil); err != nil {
		t.Fatal(err)
	}
	source.err = errors.New("fresh read failed")
	if _, err := classifyInfraContainmentGapFromSource(source, infraBindingTarget{}, nil); !errors.Is(err, source.err) {
		t.Fatalf("fresh read error lost: %v", err)
	}
	if source.reads != 2 || source.closes != 0 || source.fullReads != 0 {
		t.Fatalf("reads=%d closes=%d full=%d", source.reads, source.closes, source.fullReads)
	}
}
