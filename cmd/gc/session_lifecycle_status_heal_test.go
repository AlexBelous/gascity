package main

import (
	"errors"
	"maps"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/clock"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/session/sessiontest"
)

func TestApplySessionLifecycleStatusHealMatchesLegacyPatchAndFold(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name           string
		context        sessionLifecycleStatusHealContext
		startupTimeout time.Duration
		lastWokeAt     time.Duration
		wantPatch      sessionpkg.MetadataPatch
		wantClearLease bool
	}{
		{
			name: "orphan stale creating with partial store inventory preserves lease",
			context: sessionLifecycleStatusHealContext{
				RuntimeObserved:   true,
				RuntimeAlive:      false,
				RollbackAvailable: false,
			},
			wantPatch: sessionpkg.MetadataPatch{
				"state": string(sessionpkg.StateStartPending),
			},
		},
		{
			name: "desired stale creating with complete store inventory rolls back",
			context: sessionLifecycleStatusHealContext{
				RuntimeObserved:   true,
				RuntimeAlive:      false,
				RollbackAvailable: true,
			},
			wantClearLease: true,
			wantPatch: sessionpkg.MetadataPatch{
				"continuation_reset_pending": "true",
				"pending_create_claim":       "",
				"pending_create_started_at":  "",
				"primed_at":                  "",
				"priming_attempted_at":       "",
				"prompt_hash":                "",
				"session_key":                "",
				"sleep_reason":               string(sessionpkg.SleepReasonRuntimeMissing),
				"started_config_hash":        "",
				"state":                      string(sessionpkg.StateAsleep),
			},
		},
		{
			name: "desired configured startup timeout preserves in-flight lease",
			context: sessionLifecycleStatusHealContext{
				RuntimeObserved:   true,
				RuntimeAlive:      false,
				RollbackAvailable: true,
			},
			startupTimeout: 5 * time.Minute,
			lastWokeAt:     -90 * time.Second,
			wantPatch: sessionpkg.MetadataPatch{
				"state": string(sessionpkg.StateAsleep),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metadata := map[string]string{
				"state":                     string(sessionpkg.StateCreating),
				"pending_create_claim":      "true",
				"pending_create_started_at": now.Add(-20 * time.Minute).Format(time.RFC3339),
			}
			if tt.lastWokeAt != 0 {
				metadata["last_woke_at"] = now.Add(tt.lastWokeAt).Format(time.RFC3339)
			}
			info, front := statusHealFixture(t, "status-heal", now.Add(-20*time.Minute), metadata)
			tick := newReconcileTick([]sessionpkg.Info{info})
			clk := &clock.Fake{Time: now}

			got, err := applySessionLifecycleStatusHeal(tick, info.ID, tt.context, front, clk, tt.startupTimeout)
			if err != nil {
				t.Fatalf("apply status heal: %v", err)
			}
			if !maps.Equal(got, tt.wantPatch) {
				t.Fatalf("legacy patch = %#v, want %#v", got, tt.wantPatch)
			}
			for _, key := range []string{"pending_create_claim", "pending_create_started_at"} {
				value, exists := got[key]
				if tt.wantClearLease && (!exists || value != "") {
					t.Fatalf("legacy patch %q = %q (present %t), want explicit clear", key, value, exists)
				}
				if !tt.wantClearLease && exists {
					t.Fatalf("legacy patch unexpectedly contains %q: %#v", key, got)
				}
			}
			wantInfo := info.ApplyPatch(tt.wantPatch)
			if !reflect.DeepEqual(tick.infoByID[info.ID], wantInfo) {
				t.Fatalf("tick info = %#v, want input.ApplyPatch(patch) %#v", tick.infoByID[info.ID], wantInfo)
			}
			persisted, err := front.Get(info.ID)
			if err != nil {
				t.Fatalf("front.Get(%s): %v", info.ID, err)
			}
			if !reflect.DeepEqual(persisted, wantInfo) {
				t.Fatalf("persisted reread = %#v, want %#v", persisted, wantInfo)
			}
		})
	}
}

func TestApplySessionLifecycleStatusHealRejectsInvalidTickIdentity(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	bead := statusHealBead("status-heal-valid", now, map[string]string{
		"state": string(sessionpkg.StateAsleep),
	})
	mem := beads.NewMemStoreFrom(1, []beads.Bead{bead}, nil)
	writer := &statusHealAttemptStore{Store: mem}
	front := sessionpkg.NewStore(beads.SessionStore{Store: writer})
	info, err := front.Get(bead.ID)
	if err != nil {
		t.Fatalf("front.Get(%s): %v", bead.ID, err)
	}

	tests := []struct {
		name            string
		tick            *reconcileTick
		request         string
		wantErrContains string
	}{
		{
			name:            "missing tick key",
			tick:            newReconcileTick(nil),
			request:         info.ID,
			wantErrContains: `session "status-heal-valid" missing from reconcile tick`,
		},
		{
			name: "mismatched info ID",
			tick: &reconcileTick{infoByID: map[string]sessionpkg.Info{info.ID: func() sessionpkg.Info {
				mismatched := info
				mismatched.ID = "status-heal-actual"
				return mismatched
			}()}},
			request:         info.ID,
			wantErrContains: `requested session ID "status-heal-valid", tick info ID "status-heal-actual"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			beforeInfo, beforeExists := tt.tick.infoByID[tt.request]
			beforeLen := len(tt.tick.infoByID)

			patch, err := applySessionLifecycleStatusHeal(tt.tick, tt.request, sessionLifecycleStatusHealContext{
				RuntimeObserved:   true,
				RuntimeAlive:      true,
				RollbackAvailable: true,
			}, front, &clock.Fake{Time: now}, 0)
			if err == nil || !strings.Contains(err.Error(), tt.wantErrContains) {
				t.Fatalf("error = %v, want substring %q", err, tt.wantErrContains)
			}
			if patch != nil {
				t.Fatalf("patch = %#v, want nil", patch)
			}
			if writer.attempts != 0 {
				t.Fatalf("writer attempts = %d, want 0", writer.attempts)
			}
			afterInfo, afterExists := tt.tick.infoByID[tt.request]
			if len(tt.tick.infoByID) != beforeLen || afterExists != beforeExists || !reflect.DeepEqual(afterInfo, beforeInfo) {
				t.Fatalf("tick map changed: len=%d entry=(%#v,%t), want len=%d entry=(%#v,%t)", len(tt.tick.infoByID), afterInfo, afterExists, beforeLen, beforeInfo, beforeExists)
			}
		})
	}
}

type statusHealAttemptStore struct {
	beads.Store
	attempts int
}

func (s *statusHealAttemptStore) SetMetadataBatch(id string, patch map[string]string) error {
	s.attempts++
	return s.Store.SetMetadataBatch(id, patch)
}

func TestApplySessionLifecycleStatusHealUnknownRuntimeKeepsLegacyWriter(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	info, front := statusHealFixture(t, "status-heal-unknown", now, map[string]string{
		"state": string(sessionpkg.StateAsleep),
	})
	tick := newReconcileTick([]sessionpkg.Info{info})

	patch, err := applySessionLifecycleStatusHeal(tick, info.ID, sessionLifecycleStatusHealContext{
		RuntimeObserved:   false,
		RuntimeAlive:      true,
		RollbackAvailable: false,
	}, front, &clock.Fake{Time: now}, 0)
	if err != nil {
		t.Fatalf("apply unknown-runtime status heal: %v", err)
	}
	if patch["state"] != string(sessionpkg.StateAwake) {
		t.Fatalf("legacy patch = %#v, want state=awake", patch)
	}
	if tick.infoByID[info.ID].MetadataState != string(sessionpkg.StateAwake) {
		t.Fatalf("tick state = %q, want awake legacy patch folded", tick.infoByID[info.ID].MetadataState)
	}
	persisted, err := front.Get(info.ID)
	if err != nil {
		t.Fatalf("front.Get(%s): %v", info.ID, err)
	}
	if !reflect.DeepEqual(persisted, tick.infoByID[info.ID]) {
		t.Fatalf("persisted reread = %#v, want folded legacy awake state %#v", persisted, tick.infoByID[info.ID])
	}
}

func TestApplySessionLifecycleStatusHealWriteFailureDoesNotFold(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	bead := statusHealBead("status-heal-write-failure", now, map[string]string{
		"state": string(sessionpkg.StateAsleep),
	})
	mem := beads.NewMemStoreFrom(1, []beads.Bead{bead}, nil)
	writeErr := errors.New("apply then error")
	front := sessionpkg.NewStore(beads.SessionStore{Store: &applyThenErrorStatusHealStore{Store: mem, err: writeErr}})
	info, err := front.Get(bead.ID)
	if err != nil {
		t.Fatalf("front.Get(%s): %v", bead.ID, err)
	}
	tick := newReconcileTick([]sessionpkg.Info{info})
	before := tick.infoByID[info.ID]

	patch, err := applySessionLifecycleStatusHeal(tick, info.ID, sessionLifecycleStatusHealContext{
		RuntimeObserved:   true,
		RuntimeAlive:      true,
		RollbackAvailable: true,
	}, front, &clock.Fake{Time: now}, 0)
	if err == nil {
		t.Fatal("apply status heal error = nil, want apply-then-error failure")
	}
	if patch != nil {
		t.Fatalf("patch = %#v, want nil after failed write", patch)
	}
	if !errors.Is(err, writeErr) {
		t.Fatalf("error = %v, want %v", err, writeErr)
	}
	if !reflect.DeepEqual(tick.infoByID[info.ID], before) {
		t.Fatalf("tick projection advanced after failed write: got %#v, want %#v", tick.infoByID[info.ID], before)
	}
	persisted, getErr := front.Get(info.ID)
	if getErr != nil {
		t.Fatalf("front.Get(%s): %v", info.ID, getErr)
	}
	wantPersisted := before.ApplyPatch(sessionpkg.MetadataPatch{"state": string(sessionpkg.StateAwake)})
	if !reflect.DeepEqual(persisted, wantPersisted) {
		t.Fatalf("durable row = %#v, want committed legacy patch %#v", persisted, wantPersisted)
	}
}

func statusHealFixture(t *testing.T, id string, createdAt time.Time, metadata map[string]string) (sessionpkg.Info, *sessionpkg.Store) {
	t.Helper()
	front, _ := sessiontest.Store(t, statusHealBead(id, createdAt, metadata))
	info, err := front.Get(id)
	if err != nil {
		t.Fatalf("front.Get(%s): %v", id, err)
	}
	return info, front
}

func statusHealBead(id string, createdAt time.Time, metadata map[string]string) beads.Bead {
	clonedMetadata := make(map[string]string, len(metadata))
	for key, value := range metadata {
		clonedMetadata[key] = value
	}
	return beads.Bead{
		ID:        id,
		Type:      sessionpkg.BeadType,
		Labels:    []string{sessionpkg.LabelSession},
		CreatedAt: createdAt,
		Metadata:  clonedMetadata,
	}
}

type applyThenErrorStatusHealStore struct {
	beads.Store
	err error
}

func (s *applyThenErrorStatusHealStore) SetMetadataBatch(id string, patch map[string]string) error {
	if err := s.Store.SetMetadataBatch(id, patch); err != nil {
		return err
	}
	return s.err
}
