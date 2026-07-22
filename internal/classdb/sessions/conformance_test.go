package sessionsdb

// Both-backend conformance for the sessions class: the SAME behavioral
// assertions run over the bd-shape reference (beads.MemStore) and this
// store, exercised THROUGH the public session-domain surfaces
// (session.Store front door, the ListAll union family, the wait
// sub-surface) — the seam is the beads.Store interface, so the suite pins
// that both backends produce identical observable domain behavior: union
// trap rows, fingerprint-over-all-metadata, empty-string clears, the wait
// lifecycle, and the wake batch.

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/session"
)

func conformanceBackends(t *testing.T) map[string]func(t *testing.T) beads.Store {
	t.Helper()
	return map[string]func(t *testing.T) beads.Store{
		"memstore": func(_ *testing.T) beads.Store { return beads.NewMemStore() },
		"sessionsdb": func(t *testing.T) beads.Store {
			st, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
			if err != nil {
				t.Fatalf("open sessions store: %v", err)
			}
			t.Cleanup(func() { _ = st.CloseStore() })
			return st
		},
	}
}

func frontDoor(st beads.Store) *session.Store {
	return session.NewStore(beads.SessionStore{Store: st})
}

func mustCreateSession(t *testing.T, front *session.Store, agent string, meta map[string]string) session.Info {
	t.Helper()
	info, err := front.CreateSessionInfo(session.CreateSpec{
		Title:     agent,
		AgentName: agent,
		Metadata:  meta,
	})
	if err != nil {
		t.Fatalf("CreateSessionInfo: %v", err)
	}
	return info
}

func TestSessionsClassConformance(t *testing.T) {
	for name, newStore := range conformanceBackends(t) {
		t.Run(name, func(t *testing.T) {
			t.Run("CreateGetRoundTrip", func(t *testing.T) {
				st := newStore(t)
				front := frontDoor(st)
				created := mustCreateSession(t, front, "polly", map[string]string{
					"state":        "awake",
					"session_name": "gc-polly",
					"template":     "worker",
					"generation":   "3",
				})
				got, err := front.Get(created.ID)
				if err != nil {
					t.Fatalf("Get: %v", err)
				}
				if !reflect.DeepEqual(created, got) {
					t.Fatalf("create echo != Get projection:\n echo %+v\n got  %+v", created, got)
				}
				if got.MetadataState != "awake" || got.SessionName != "gc-polly" || got.Generation != "3" {
					t.Fatalf("hot fields lost: %+v", got)
				}
			})

			t.Run("ApplyPatchAtomicAndEmptyStringClears", func(t *testing.T) {
				st := newStore(t)
				front := frontDoor(st)
				info := mustCreateSession(t, front, "a", map[string]string{
					"state": "awake", "sleep_reason": "idle", "last_woke_at": "x",
				})
				if err := front.ApplyPatch(info.ID, session.MetadataPatch{
					"state":        "asleep",
					"sleep_reason": "",
					"held_until":   "2027-01-01T00:00:00Z",
				}); err != nil {
					t.Fatalf("ApplyPatch: %v", err)
				}
				got, err := front.Get(info.ID)
				if err != nil {
					t.Fatalf("Get: %v", err)
				}
				if got.State != "asleep" || got.SleepReason != "" || got.HeldUntil != "2027-01-01T00:00:00Z" {
					t.Fatalf("patch not applied atomically: %+v", got)
				}
			})

			t.Run("UnionTrapsAndListFilters", func(t *testing.T) {
				st := newStore(t)
				front := frontDoor(st)
				normal := mustCreateSession(t, front, "n", map[string]string{"state": "awake"})
				labelLost := mustCreateSession(t, front, "l", map[string]string{"state": "awake"})
				if err := st.Update(labelLost.ID, beads.UpdateOpts{RemoveLabels: []string{session.LabelSession}}); err != nil {
					t.Fatalf("strip label: %v", err)
				}
				emptyType := mustCreateSession(t, front, "e", map[string]string{"state": "awake"})
				empty := ""
				if err := st.Update(emptyType.ID, beads.UpdateOpts{Type: &empty}); err != nil {
					t.Fatalf("blank type: %v", err)
				}
				closedSess := mustCreateSession(t, front, "c", map[string]string{"state": "awake"})
				if _, err := front.Close(closedSess.ID, "done", time.Now()); err != nil {
					t.Fatalf("Close: %v", err)
				}
				damaged := mustCreateSession(t, front, "d", map[string]string{"state": "awake"})
				chore := "chore"
				if err := st.Update(damaged.ID, beads.UpdateOpts{Type: &chore}); err != nil {
					t.Fatalf("damage type: %v", err)
				}
				wait := mustWait(t, front, normal.ID, "note")

				openIDs := listIDs(t, front, session.ListAllOptions{Sort: beads.SortCreatedDesc})
				wantOpen := map[string]bool{normal.ID: true, labelLost.ID: true, emptyType.ID: true}
				if !sameIDSet(openIDs, wantOpen) {
					t.Fatalf("open union = %v, want exactly %v (damaged/wait/closed excluded)", openIDs, wantOpen)
				}
				allIDs := listIDs(t, front, session.ListAllOptions{IncludeClosed: true, Sort: beads.SortCreatedDesc})
				wantAll := map[string]bool{normal.ID: true, labelLost.ID: true, emptyType.ID: true, closedSess.ID: true}
				if !sameIDSet(allIDs, wantAll) {
					t.Fatalf("include-closed union = %v, want exactly %v", allIDs, wantAll)
				}
				if wait.ID == "" {
					t.Fatal("wait id empty")
				}

				// The label-only unfiltered lister surfaces the damaged-type row.
				unfiltered, err := front.ListLabeledSessionInfosUnfiltered()
				if err != nil {
					t.Fatalf("ListLabeledSessionInfosUnfiltered: %v", err)
				}
				found := false
				for _, info := range unfiltered {
					if info.ID == damaged.ID {
						found = true
					}
					if info.ID == labelLost.ID {
						t.Fatalf("label-lost bead %s surfaced by label-only lister", labelLost.ID)
					}
				}
				if !found {
					t.Fatalf("damaged-type row %s missing from label-only lister", damaged.ID)
				}
			})

			t.Run("FingerprintReflectsAllMetadataKeys", func(t *testing.T) {
				st := newStore(t)
				front := frontDoor(st)
				info := mustCreateSession(t, front, "f", map[string]string{"state": "awake"})
				opts := session.ListAllOptions{Sort: beads.SortCreatedDesc}
				_, fp1, err := front.ListAllForReconcileWithFingerprint(opts)
				if err != nil {
					t.Fatalf("fingerprint list: %v", err)
				}
				// A NON-codec key (not projected onto Info) must still move the
				// fingerprint — the all-metadata contract.
				if err := front.ApplyPatch(info.ID, session.MetadataPatch{"session_circuit_state": "open"}); err != nil {
					t.Fatalf("patch circuit key: %v", err)
				}
				rows, fp2, err := front.ListAllForReconcileWithFingerprint(opts)
				if err != nil {
					t.Fatalf("fingerprint relist: %v", err)
				}
				if fp1 == fp2 {
					t.Fatal("fingerprint unchanged after non-codec metadata write")
				}
				if rows[0].Circuit.State != "open" {
					t.Fatalf("circuit projection lost: %+v", rows[0].Circuit)
				}
				_, fp3, err := front.ListAllForReconcileWithFingerprint(opts)
				if err != nil {
					t.Fatalf("fingerprint stable relist: %v", err)
				}
				if fp2 != fp3 {
					t.Fatal("fingerprint unstable across idempotent relists")
				}
			})

			t.Run("WaitLifecycle", func(t *testing.T) {
				st := newStore(t)
				front := frontDoor(st)
				sess := mustCreateSession(t, front, "w", map[string]string{
					"state": "awake", "session_name": "gc-w", "continuation_epoch": "e1",
				})
				w := mustWait(t, front, sess.ID, "remind me")
				if w.State != "pending" || w.SessionID != sess.ID || w.RegisteredEpoch != "e1" {
					t.Fatalf("created wait shape: %+v", w)
				}
				got, err := front.GetWait(w.ID)
				if err != nil || got.ID != w.ID || got.Note != "remind me" {
					t.Fatalf("GetWait: %+v, %v", got, err)
				}
				forSession, err := front.WaitsForSession(sess.ID)
				if err != nil || len(forSession) != 1 {
					t.Fatalf("WaitsForSession: %v, %v", forSession, err)
				}
				if err := front.MarkWaitReady(w.ID, time.Now()); err != nil {
					t.Fatalf("MarkWaitReady: %v", err)
				}
				if err := front.SetWaitNudgeID(w.ID, "wait-n-1"); err != nil {
					t.Fatalf("SetWaitNudgeID: %v", err)
				}
				ids, err := front.WaitNudgeIDs(sess.ID)
				if err != nil || !reflect.DeepEqual(ids, []string{"wait-n-1"}) {
					t.Fatalf("WaitNudgeIDs: %v, %v", ids, err)
				}
				if err := front.CloseWaitFromNudge(w.ID, time.Now(), "wait-n-1", "boundary"); err != nil {
					t.Fatalf("CloseWaitFromNudge: %v", err)
				}
				closed, err := front.GetWait(w.ID)
				if err != nil || closed.State != "closed" || closed.Status != "closed" || closed.NudgeID != "wait-n-1" {
					t.Fatalf("terminal wait: %+v, %v", closed, err)
				}

				// Retry clone: fresh ready wait, bookkeeping cleared, attempt bumped.
				retried, err := front.RetryClosedWait(w.ID, "2", time.Now())
				if err != nil {
					t.Fatalf("RetryClosedWait: %v", err)
				}
				if retried.ID == w.ID || retried.State != "ready" || retried.DeliveryAttempt != "2" || retried.NudgeID != "" {
					t.Fatalf("retried wait: %+v", retried)
				}

				// Reassign to a new session id.
				sess2 := mustCreateSession(t, front, "w2", map[string]string{"state": "awake"})
				if err := front.ReassignWaits(sess.ID, sess2.ID); err != nil {
					t.Fatalf("ReassignWaits: %v", err)
				}
				moved, err := front.WaitsForSession(sess2.ID)
				if err != nil || len(moved) != 1 || moved[0].ID != retried.ID {
					t.Fatalf("reassigned waits: %+v, %v", moved, err)
				}
				if err := front.SetWaitNudgeID(retried.ID, "wait-n-2"); err != nil {
					t.Fatalf("SetWaitNudgeID retried: %v", err)
				}
				nudgeIDs, capped, err := front.CancelWaits(sess2.ID, time.Now())
				if err != nil || capped {
					t.Fatalf("CancelWaits: capped=%v err=%v", capped, err)
				}
				if !reflect.DeepEqual(nudgeIDs, []string{"wait-n-2"}) {
					t.Fatalf("collected nudge ids: %v", nudgeIDs)
				}
				after, err := front.WaitsForSession(sess2.ID)
				if err != nil || len(after) != 0 {
					t.Fatalf("open waits after cancel: %+v, %v", after, err)
				}
			})

			t.Run("WakeSessionClearsBlockersAndCancelsWaits", func(t *testing.T) {
				st := newStore(t)
				front := frontDoor(st)
				sess := mustCreateSession(t, front, "wake", map[string]string{
					"state": "asleep", "sleep_reason": "idle",
					"wait_hold": "true", "quarantined_until": "2099-01-01T00:00:00Z",
					"session_name": "gc-wake",
				})
				w := mustWait(t, front, sess.ID, "n")
				if err := front.SetWaitNudgeID(w.ID, "wait-n-9"); err != nil {
					t.Fatalf("SetWaitNudgeID: %v", err)
				}
				res, err := front.WakeSession(sess.ID, time.Now(), session.WakeOpts{})
				if err != nil {
					t.Fatalf("WakeSession: %v", err)
				}
				if !reflect.DeepEqual(res.NudgeIDs, []string{"wait-n-9"}) {
					t.Fatalf("wake nudge ids: %v", res.NudgeIDs)
				}
				got, err := front.Get(sess.ID)
				if err != nil {
					t.Fatalf("Get: %v", err)
				}
				if got.WaitHold != "" || got.QuarantinedUntil != "" {
					t.Fatalf("wake blockers not cleared: %+v", got)
				}
				// Closed sessions conflict when RejectClosed is set.
				if _, err := front.Close(sess.ID, "done", time.Now()); err != nil {
					t.Fatalf("Close: %v", err)
				}
				_, err = front.WakeSession(sess.ID, time.Now(), session.WakeOpts{RejectClosed: true})
				var conflict *session.WakeConflictError
				if !errors.As(err, &conflict) {
					t.Fatalf("want WakeConflictError on closed session, got %v", err)
				}
			})

			t.Run("HasOpenSessionNamedProbe", func(t *testing.T) {
				st := newStore(t)
				front := frontDoor(st)
				sess := mustCreateSession(t, front, "p", map[string]string{"session_name": "gc-probe"})
				ok, err := front.HasOpenSessionNamed("gc-probe")
				if err != nil || !ok {
					t.Fatalf("probe open: %v %v", ok, err)
				}
				if _, err := front.Close(sess.ID, "done", time.Now()); err != nil {
					t.Fatalf("Close: %v", err)
				}
				ok, err = front.HasOpenSessionNamed("gc-probe")
				if err != nil || ok {
					t.Fatalf("probe after close: %v %v", ok, err)
				}
			})

			t.Run("CloseStampsAndReopen", func(t *testing.T) {
				st := newStore(t)
				front := frontDoor(st)
				sess := mustCreateSession(t, front, "c", map[string]string{"state": "awake"})
				closed, err := front.Close(sess.ID, "manual", time.Now())
				if err != nil || !closed {
					t.Fatalf("Close: %v %v", closed, err)
				}
				again, err := front.Close(sess.ID, "manual", time.Now())
				if err != nil || again {
					t.Fatalf("idempotent Close: %v %v", again, err)
				}
				_, isClosed, err := front.GetState(sess.ID)
				if err != nil || !isClosed {
					t.Fatalf("GetState after close: closed=%v err=%v", isClosed, err)
				}
				if err := front.SetStatusOpen(sess.ID); err != nil {
					t.Fatalf("SetStatusOpen: %v", err)
				}
				_, isClosed, err = front.GetState(sess.ID)
				if err != nil || isClosed {
					t.Fatalf("GetState after reopen: closed=%v err=%v", isClosed, err)
				}
			})

			t.Run("ManagerShapeCreateProjects", func(t *testing.T) {
				st := newStore(t)
				front := frontDoor(st)
				created, err := st.Create(beads.Bead{
					Title:  "mgr",
					Type:   session.BeadType,
					Labels: []string{session.LabelSession, "template:worker"},
					Metadata: map[string]string{
						"state": "creating", "template": "worker",
						"pending_create_claim": "true",
					},
				})
				if err != nil {
					t.Fatalf("raw create: %v", err)
				}
				if err := st.SetMetadata(created.ID, "session_name", "gc-mgr"); err != nil {
					t.Fatalf("SetMetadata: %v", err)
				}
				info, err := front.Get(created.ID)
				if err != nil {
					t.Fatalf("Get: %v", err)
				}
				if !info.PendingCreateClaim || info.SessionName != "gc-mgr" || info.Template != "worker" {
					t.Fatalf("manager-shape projection: %+v", info)
				}
				markers, err := front.PersistedMarkers(created.ID)
				if err != nil || markers.SessionName != "gc-mgr" || markers.Title != "mgr" {
					t.Fatalf("PersistedMarkers: %+v, %v", markers, err)
				}
			})
		})
	}
}

func mustWait(t *testing.T, front *session.Store, sessionID, note string) session.WaitInfo {
	t.Helper()
	w, err := front.CreateWait(session.WaitSpec{
		SessionID: sessionID,
		Kind:      "deps",
		DepIDs:    []string{"gc-100"},
		DepMode:   "all",
		Note:      note,
		Now:       time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("CreateWait: %v", err)
	}
	return w
}

func listIDs(t *testing.T, front *session.Store, opts session.ListAllOptions) []string {
	t.Helper()
	infos, err := front.ListAll(opts)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	ids := make([]string, 0, len(infos))
	for _, info := range infos {
		ids = append(ids, info.ID)
	}
	return ids
}

func sameIDSet(ids []string, want map[string]bool) bool {
	if len(ids) != len(want) {
		return false
	}
	for _, id := range ids {
		if !want[id] {
			return false
		}
	}
	return true
}
