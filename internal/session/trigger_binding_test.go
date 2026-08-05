package session

import (
	"errors"
	"reflect"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/rollout/gate"
)

func TestRebindTriggerIfMatchCommitsCompleteProvenanceUnderOneRevisionFence(t *testing.T) {
	front, store, id := conditionalTriggerBindingStore(t)
	pre, persisted, err := front.GetPersistedResponse(id)
	if err != nil {
		t.Fatalf("read preimage: %v", err)
	}
	binding := TriggerBinding{
		WorkID:         "ga-next",
		StoreRef:       "city:test-city",
		BrainParentSID: "sid-next",
		Pack:           "review-pack",
		Workspace:      "workspace-b",
		WorkDir:        "/city/worker-root/review-pack/workspace-b",
	}

	got, err := front.RebindTriggerIfMatch(pre, persisted, binding)
	if err != nil {
		t.Fatalf("rebind trigger: %v", err)
	}
	after, err := store.Get(id)
	if err != nil {
		t.Fatalf("read rebound row: %v", err)
	}
	if after.Revision != persisted.Revision+1 {
		t.Fatalf("rebound revision = %d, want %d", after.Revision, persisted.Revision+1)
	}
	want := map[string]string{
		beadmeta.TriggerBeadIDMetadataKey:       binding.WorkID,
		beadmeta.TriggerBeadStoreRefMetadataKey: binding.StoreRef,
		beadmeta.BrainParentSIDMetadataKey:      binding.BrainParentSID,
		beadmeta.PackMetadataKey:                binding.Pack,
		beadmeta.PackWorkspaceMetadataKey:       binding.Workspace,
		beadmeta.WorkDirMetadataKey:             binding.WorkDir,
		beadmeta.LegacyWorkDirMetadataKey:       binding.WorkDir,
	}
	for key, value := range want {
		if after.Metadata[key] != value {
			t.Errorf("rebound metadata[%q] = %q, want %q", key, after.Metadata[key], value)
		}
	}
	if !binding.Matches(got) {
		t.Fatalf("returned Info does not match binding: %+v", got)
	}
}

func TestRebindTriggerIfMatchFailsClosedOnStaleRevision(t *testing.T) {
	front, store, id := conditionalTriggerBindingStore(t)
	pre, persisted, err := front.GetPersistedResponse(id)
	if err != nil {
		t.Fatalf("read preimage: %v", err)
	}
	if err := store.SetMetadata(id, "unrelated", "newer"); err != nil {
		t.Fatalf("advance row revision: %v", err)
	}

	got, err := front.RebindTriggerIfMatch(pre, persisted, TriggerBinding{
		WorkID:   "ga-next",
		StoreRef: "city:test-city",
		WorkDir:  "/city/worker",
	})
	if !beads.IsPreconditionFailed(err) {
		t.Fatalf("stale rebind error = %v, want precondition failure", err)
	}
	if !reflect.DeepEqual(got, pre) {
		t.Fatalf("stale rebind returned changed Info\n got=%+v\nwant=%+v", got, pre)
	}
	after, err := store.Get(id)
	if err != nil {
		t.Fatalf("read row after stale rebind: %v", err)
	}
	if after.Metadata[beadmeta.TriggerBeadIDMetadataKey] != "ga-old" {
		t.Fatalf("stale rebind changed durable trigger to %q", after.Metadata[beadmeta.TriggerBeadIDMetadataKey])
	}
}

func TestRebindTriggerIfMatchRefusesStoreWithoutResolvedConditionalWrites(t *testing.T) {
	store := beads.NewMemStore()
	created, err := store.Create(triggerBindingSessionBead())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	front := NewStore(beads.SessionStore{Store: store})
	pre, persisted, err := front.GetPersistedResponse(created.ID)
	if err != nil {
		t.Fatalf("read preimage: %v", err)
	}

	got, err := front.RebindTriggerIfMatch(pre, persisted, TriggerBinding{
		WorkID:   "ga-next",
		StoreRef: "city:test-city",
		WorkDir:  "/city/worker",
	})
	if !errors.Is(err, beads.ErrConditionalWriteUnsupported) {
		t.Fatalf("unresolved conditional-write error = %v, want ErrConditionalWriteUnsupported", err)
	}
	if !reflect.DeepEqual(got, pre) {
		t.Fatalf("refused rebind returned changed Info\n got=%+v\nwant=%+v", got, pre)
	}
}

func TestRebindTriggerIfMatchExactReplayIsNoOp(t *testing.T) {
	store := beads.NewMemStore()
	bead := triggerBindingSessionBead()
	bead.Metadata[beadmeta.TriggerBeadIDMetadataKey] = "ga-next"
	bead.Metadata[beadmeta.TriggerBeadStoreRefMetadataKey] = "city:test-city"
	bead.Metadata[beadmeta.BrainParentSIDMetadataKey] = ""
	bead.Metadata[beadmeta.PackMetadataKey] = ""
	bead.Metadata[beadmeta.PackWorkspaceMetadataKey] = ""
	bead.Metadata[beadmeta.WorkDirMetadataKey] = "/city/worker"
	bead.Metadata[beadmeta.LegacyWorkDirMetadataKey] = "/city/worker"
	created, err := store.Create(bead)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	front := NewStore(beads.SessionStore{Store: store})
	pre, persisted, err := front.GetPersistedResponse(created.ID)
	if err != nil {
		t.Fatalf("read preimage: %v", err)
	}

	got, err := front.RebindTriggerIfMatch(pre, persisted, TriggerBinding{
		WorkID:   "ga-next",
		StoreRef: "city:test-city",
		WorkDir:  "/city/worker",
	})
	if err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	if !reflect.DeepEqual(got, pre) {
		t.Fatalf("exact replay changed Info\n got=%+v\nwant=%+v", got, pre)
	}
	after, err := store.Get(created.ID)
	if err != nil {
		t.Fatalf("read replayed row: %v", err)
	}
	if after.Revision != persisted.Revision {
		t.Fatalf("exact replay revision = %d, want unchanged %d", after.Revision, persisted.Revision)
	}
}

func conditionalTriggerBindingStore(t *testing.T) (*Store, beads.Store, string) {
	t.Helper()
	opened, err := beads.OpenStoreAtForCity(t.Context(), beads.StoreOpenOptions{
		Provider:          "file",
		OpenFileStore:     func() (beads.Store, error) { return beads.NewMemStore(), nil },
		ConditionalWrites: gate.Auto,
	})
	if err != nil {
		t.Fatalf("open conditional-write store: %v", err)
	}
	created, err := opened.Store.Create(triggerBindingSessionBead())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return NewStore(beads.SessionStore{Store: opened.Store}), opened.Store, created.ID
}

func triggerBindingSessionBead() beads.Bead {
	return beads.Bead{
		Title:  "worker",
		Type:   BeadType,
		Labels: []string{LabelSession},
		Metadata: map[string]string{
			"state":                                 string(StateActive),
			beadmeta.TriggerBeadIDMetadataKey:       "ga-old",
			beadmeta.TriggerBeadStoreRefMetadataKey: "city:test-city",
			beadmeta.BrainParentSIDMetadataKey:      "sid-old",
			beadmeta.PackMetadataKey:                "old-pack",
			beadmeta.PackWorkspaceMetadataKey:       "old-workspace",
			beadmeta.WorkDirMetadataKey:             "/city/old",
			beadmeta.LegacyWorkDirMetadataKey:       "/city/old",
		},
	}
}
