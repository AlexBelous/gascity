package sessionsdb

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/session"
)

func newShadowPair(t *testing.T) (beads.Store, *Store) {
	t.Helper()
	class := openTestStore(t)
	return NewShadow(beads.NewMemStore(), class), class
}

func sessionBead(title string) beads.Bead {
	return beads.Bead{
		Title:    title,
		Type:     session.BeadType,
		Labels:   []string{session.LabelSession, "agent:" + title},
		Metadata: map[string]string{"state": "awake", "session_name": "gc-" + title},
	}
}

func TestShadowCreateTeesVerbatimEcho(t *testing.T) {
	sh, class := newShadowPair(t)
	created, err := sh.Create(sessionBead("a"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := class.Get(created.ID)
	if err != nil {
		t.Fatalf("shadow row missing after teed create: %v", err)
	}
	if got.ID != created.ID || got.Metadata["session_name"] != "gc-a" || got.Status != "open" {
		t.Fatalf("teed row not verbatim: %+v", got)
	}
	if !got.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("teed clock diverged: %v != %v", got.CreatedAt, created.CreatedAt)
	}
}

func TestShadowNonSessionCreateNotTeed(t *testing.T) {
	sh, class := newShadowPair(t)
	created, err := sh.Create(beads.Bead{Title: "work", Type: "task"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := class.Get(created.ID); err == nil {
		t.Fatal("work-class create teed into the sessions shadow")
	}
}

func TestShadowWritesReplayOntoClassStore(t *testing.T) {
	sh, class := newShadowPair(t)
	created, err := sh.Create(sessionBead("b"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sh.SetMetadataBatch(created.ID, map[string]string{"state": "asleep", "sleep_reason": "idle"}); err != nil {
		t.Fatal(err)
	}
	if err := sh.Close(created.ID); err != nil {
		t.Fatal(err)
	}
	got, err := class.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Metadata["state"] != "asleep" || got.Metadata["sleep_reason"] != "idle" || got.Status != "closed" {
		t.Fatalf("replayed writes lost: %+v", got)
	}
}

func TestShadowOnMissImportConverges(t *testing.T) {
	// A row created before the shadow was enabled (directly on the primary)
	// converges on its first teed write.
	class := openTestStore(t)
	primary := beads.NewMemStore()
	pre, err := primary.Create(sessionBead("pre"))
	if err != nil {
		t.Fatal(err)
	}
	sh := NewShadow(primary, class)
	if err := sh.SetMetadata(pre.ID, "state", "awake"); err != nil {
		t.Fatal(err)
	}
	got, err := class.Get(pre.ID)
	if err != nil {
		t.Fatalf("miss import did not converge: %v", err)
	}
	if got.Metadata["state"] != "awake" {
		t.Fatalf("miss import stale: %+v", got)
	}
}

func TestShadowFailureNeverFailsPrimary(t *testing.T) {
	class := openTestStore(t)
	primary := beads.NewMemStore()
	sh := NewShadow(primary, class)
	created, err := sh.Create(sessionBead("c"))
	if err != nil {
		t.Fatal(err)
	}
	// Kill the shadow store; primary ops must keep succeeding.
	if err := class.CloseStore(); err != nil {
		t.Fatal(err)
	}
	if err := sh.SetMetadata(created.ID, "state", "asleep"); err != nil {
		t.Fatalf("primary write failed because the shadow store is down: %v", err)
	}
	b, err := primary.Get(created.ID)
	if err != nil || b.Metadata["state"] != "asleep" {
		t.Fatalf("primary state lost: %+v, %v", b, err)
	}
}

func TestShadowReadsComeFromPrimary(t *testing.T) {
	sh, class := newShadowPair(t)
	created, err := sh.Create(sessionBead("d"))
	if err != nil {
		t.Fatal(err)
	}
	// Diverge the shadow deliberately; reads must not see it.
	if err := class.SetMetadata(created.ID, "state", "diverged"); err != nil {
		t.Fatal(err)
	}
	got, err := sh.Get(created.ID)
	if err != nil || got.Metadata["state"] != "awake" {
		t.Fatalf("read did not come from primary: %+v, %v", got, err)
	}
}

type cachedListerStub struct {
	beads.Store
	hits int
}

func (c *cachedListerStub) CachedList(beads.ListQuery) ([]beads.Bead, bool) {
	c.hits++
	return nil, true
}

func TestShadowForwardsCachedListCapability(t *testing.T) {
	class := openTestStore(t)
	stub := &cachedListerStub{Store: beads.NewMemStore()}
	sh := NewShadow(stub, class)
	lister, ok := sh.(interface {
		CachedList(beads.ListQuery) ([]beads.Bead, bool)
	})
	if !ok {
		t.Fatal("shadow over a cache-capable primary must forward CachedList")
	}
	if _, ok := lister.CachedList(beads.ListQuery{}); !ok || stub.hits != 1 {
		t.Fatalf("CachedList not forwarded (hits=%d)", stub.hits)
	}
	// A primary WITHOUT the capability must not grow one.
	plain := NewShadow(beads.NewMemStore(), class)
	if _, ok := plain.(interface {
		CachedList(beads.ListQuery) ([]beads.Bead, bool)
	}); ok {
		t.Fatal("shadow over a plain primary must not fabricate CachedList")
	}
}

func TestSeedFromPrimaryAndDiff(t *testing.T) {
	class := openTestStore(t)
	primary := beads.NewMemStore()
	front := session.NewStore(beads.SessionStore{Store: primary})
	open, err := front.CreateSessionInfo(session.CreateSpec{
		Title: "s1", AgentName: "s1",
		Metadata: map[string]string{"state": "awake"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := front.CreateWait(session.WaitSpec{
		SessionID: open.ID, Kind: "deps",
		DepIDs: []string{"gc-9"}, DepMode: "all", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	closedInfo, err := front.CreateSessionInfo(session.CreateSpec{Title: "s2", AgentName: "s2", Metadata: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := front.Close(closedInfo.ID, "done", time.Now()); err != nil {
		t.Fatal(err)
	}
	// A work bead must never cross.
	if _, err := primary.Create(beads.Bead{Title: "job", Type: "task"}); err != nil {
		t.Fatal(err)
	}

	imported, err := class.SeedFromPrimary(primary)
	if err != nil {
		t.Fatal(err)
	}
	if imported != 3 { // open session + wait + closed session
		t.Fatalf("seed imported %d rows, want 3", imported)
	}
	diff, err := class.DiffAgainstPrimary(primary)
	if err != nil {
		t.Fatal(err)
	}
	if !diff.Clean() {
		t.Fatalf("post-seed diff not clean: %+v", diff)
	}

	// Diverge the shadow: metadata drift + a missing row.
	if err := class.SetMetadata(open.ID, "state", "diverged"); err != nil {
		t.Fatal(err)
	}
	extra, err := primary.Create(sessionBead("late"))
	if err != nil {
		t.Fatal(err)
	}
	diff, err = class.DiffAgainstPrimary(primary)
	if err != nil {
		t.Fatal(err)
	}
	if diff.Clean() || len(diff.Mismatched) != 1 || len(diff.Missing) != 1 {
		t.Fatalf("diff blind to divergence: %+v", diff)
	}
	if diff.Missing[0] != extra.ID || diff.Mismatched[0].ID != open.ID {
		t.Fatalf("diff misattributed: %+v", diff)
	}
}

func TestSharedStoreForCachesHandle(t *testing.T) {
	city := t.TempDir()
	st1, err := SharedStoreFor(city)
	if err != nil {
		t.Fatal(err)
	}
	st2, err := SharedStoreFor(city)
	if err != nil {
		t.Fatal(err)
	}
	if st1 != st2 {
		t.Fatal("SharedStoreFor returned distinct handles for one city")
	}
	if st1.Path() != filepath.Join(city, ".gc", "store", "sessions.db") {
		t.Fatalf("store path %q", st1.Path())
	}
}
