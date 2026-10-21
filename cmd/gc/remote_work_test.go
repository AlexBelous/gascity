//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/storebinding"
)

func TestRemoteWorkStrictFrames(t *testing.T) {
	for _, raw := range []string{`{"schema_version":1,"unexpected":true}`, `{"schema_version":1,"schema_version":1}`, `{}`, `{} {}`, strings.Repeat("x", remoteWorkFrameLimit+1)} {
		if _, err := decodeRemoteWorkFrame(strings.NewReader(raw)); err == nil {
			t.Fatalf("accepted malformed frame: %.80s", raw)
		}
	}
}

func TestRemoteWorkProofRequiresActualRootParentAndPrivatePipe(t *testing.T) {
	good := remoteWorkParent{PID: 31, Start: "41", UID: 0, Argv: []string{"/usr/bin/python3", "/opt/receiver.py", "intake"}, HelperSHA: strings.Repeat("a", 64), HelperSafe: true, PipeRoot: true, PipeAnonymous: true, PipeReadOnly: true, ChildUID: 1001}
	policy := remoteWorkPolicy{CityUID: 1001, ParentArgv: good.Argv, HelperSHA: good.HelperSHA}
	if err := verifyRemoteWorkParent(policy, good); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*remoteWorkParent){func(p *remoteWorkParent) { p.UID = 1001 }, func(p *remoteWorkParent) { p.ChildUID = 0 }, func(p *remoteWorkParent) { p.PipeRoot = false }, func(p *remoteWorkParent) { p.PipeAnonymous = false }, func(p *remoteWorkParent) { p.PipeReadOnly = false }, func(p *remoteWorkParent) { p.HelperSafe = false }, func(p *remoteWorkParent) { p.HelperSHA = "other" }, func(p *remoteWorkParent) { p.Argv = []string{"env", "verified=1"} }, func(p *remoteWorkParent) { p.PID = 1 }} {
		p := good
		mutate(&p)
		if err := verifyRemoteWorkParent(policy, p); err == nil {
			t.Fatalf("accepted forged parent %+v", p)
		}
	}
}

func TestRemoteWorkNoWorkIsNotClaimError(t *testing.T) {
	for _, reason := range []string{"claims_errored", "stale_session", "missing_session_registration", "non_turn_context", "drain_pending", "unknown"} {
		if _, err := remoteWorkClaimResponse([]byte(`{"schema_version":"1","ok":true,"command":"hook","action":"drain","reason":"`+reason+`"}`), 1); err == nil {
			t.Fatalf("%s became idle", reason)
		}
	}
	out, err := remoteWorkClaimResponse([]byte(`{"schema_version":"1","ok":true,"command":"hook","action":"drain","reason":"no_work"}`), 1)
	if err != nil || out.State != "idle" {
		t.Fatalf("%+v %v", out, err)
	}
	if _, err = remoteWorkClaimResponse([]byte(`{"schema_version":"1","ok":true,"command":"hook","action":"work","reason":"claimed","bead_id":"fixture-1","assignee":"s1"}`), 1); err == nil {
		t.Fatal("nonzero work accepted")
	}
}

type remoteWorkFake struct {
	calls       int
	validateErr error
	effects     int
	reconcile   *remoteWorkResponse
}

func (f *remoteWorkFake) Validate(context.Context) error { return f.validateErr }
func (f *remoteWorkFake) Execute(_ context.Context, r remoteWorkRequest) (remoteWorkResponse, error) {
	f.calls++
	f.effects++
	state := "recorded"
	if r.Op == "read" {
		state = "read"
	}
	return remoteWorkResponse{SchemaVersion: 1, RequestID: r.RequestID, State: state, BeadID: r.BeadID}, nil
}

func (f *remoteWorkFake) Reconcile(_ context.Context, _ remoteWorkRequest) (remoteWorkResponse, error) {
	if f.reconcile == nil {
		return remoteWorkResponse{}, errors.New("unknown")
	}
	return *f.reconcile, nil
}

func TestRemoteWorkDurableDuplicateAndUncertainIntent(t *testing.T) {
	f := &remoteWorkFake{}
	journal := remoteWorkTestJournal(t)
	req := remoteWorkRequest{RequestID: "req-1", Op: "result", BeadID: "fixture-1", Text: "checked", Outcome: "no-op", EvidenceSHA: strings.Repeat("b", 64)}
	if _, err := runRemoteWork(context.Background(), "scope", req, journal, f); err != nil {
		t.Fatal(err)
	}
	if _, err := runRemoteWork(context.Background(), "scope", req, journal, f); err != nil {
		t.Fatal(err)
	}
	if f.effects != 1 {
		t.Fatal("duplicate effect", f.effects)
	}
	changed := req
	changed.Text = "different"
	if _, err := runRemoteWork(context.Background(), "scope", changed, journal, f); err == nil {
		t.Fatal("request reuse accepted")
	}
	req.RequestID = "req-2"
	if err := journal.Intent("scope", req); err != nil {
		t.Fatal(err)
	}
	if _, err := runRemoteWork(context.Background(), "scope", req, journal, f); err == nil {
		t.Fatal("unresolved intent replayed")
	}
	if f.effects != 1 {
		t.Fatal("uncertain effect repeated")
	}
	f.reconcile = &remoteWorkResponse{SchemaVersion: 1, RequestID: req.RequestID, State: "recorded", BeadID: req.BeadID}
	if _, err := runRemoteWork(context.Background(), "scope", req, journal, f); err != nil {
		t.Fatal(err)
	}
	if f.effects != 1 {
		t.Fatal("reconciled intent repeated")
	}
}

func TestRemoteWorkStaleAuthorityBlocksEvenCachedReceipt(t *testing.T) {
	f := &remoteWorkFake{}
	j := remoteWorkTestJournal(t)
	r := remoteWorkRequest{RequestID: "r1", Op: "read", BeadID: "fixture-1"}
	if _, err := runRemoteWork(context.Background(), "scope", r, j, f); err != nil {
		t.Fatal(err)
	}
	f.validateErr = errors.New("stale")
	if _, err := runRemoteWork(context.Background(), "scope", r, j, f); err == nil {
		t.Fatal("cached receipt bypassed stale authority")
	}
}

func remoteWorkTestJournal(t *testing.T) *remoteWorkJournal {
	t.Helper()
	dir := t.TempDir()
	if e := os.Chmod(dir, 0o700); e != nil {
		t.Fatal(e)
	}
	return newRemoteWorkJournal(dir)
}

func TestRemoteWorkJournalConcurrentScopeAndSymlinkDenied(t *testing.T) {
	j := remoteWorkTestJournal(t)
	r := remoteWorkRequest{RequestID: "r1", Op: "claim"}
	unlock, e := j.Lock("scope", r)
	if e != nil {
		t.Fatal(e)
	}
	defer unlock()
	r.RequestID = "r2"
	if unlock, e := j.Lock("scope", r); e == nil {
		unlock()
		t.Fatal("parallel session request accepted")
	}
	file := j.dir + "/" + j.key("other", r) + ".intent"
	if e := os.Symlink("/dev/null", file); e != nil {
		t.Fatal(e)
	}
	if _, _, e := j.Load("other", r); e == nil {
		t.Fatal("symlink intent accepted")
	}
}

func TestRemoteWorkCanonicalByIDWritesUseRevisionFenceAndAtomicClose(t *testing.T) {
	store := beads.NewAtomicCloseMemStore()
	b, e := store.Create(beads.Bead{ID: "fixture-1", Status: "in_progress", Assignee: "session-1", Metadata: map[string]string{"owner": "fixture"}})
	if e != nil {
		t.Fatal(e)
	}
	graph, e := storebinding.NewBeadsGraphStore(store)
	if e != nil {
		t.Fatal(e)
	}
	guard := &remoteWorkOwnedGraph{GraphStore: graph, store: store, revision: b.Revision, metadata: map[string]string{beadmeta.RemoteWorkRequestMetadataKey: "close1"}}
	if code := doBdByIDUpdate(guard, bdByIDOp{ID: b.ID, JSON: true, Update: beads.UpdateOpts{Metadata: map[string]string{"result": "checked"}}}, "fixture", io.Discard, io.Discard); code != 0 {
		t.Fatal("canonical update failed")
	}
	if code := doBdByIDClose(guard, bdByIDOp{ID: b.ID, JSON: true}, "fixture", io.Discard, io.Discard); code == 0 {
		t.Fatal("stale revision closed bead")
	}
	actual, _ := store.Get(b.ID)
	if actual.Status == "closed" || actual.Metadata[beadmeta.RemoteWorkRequestMetadataKey] != "" {
		t.Fatal("partial stale close", actual)
	}
	guard.revision = actual.Revision
	if code := doBdByIDClose(guard, bdByIDOp{ID: b.ID, JSON: true}, "fixture", io.Discard, io.Discard); code != 0 {
		t.Fatal("atomic close failed")
	}
	actual, _ = store.Get(b.ID)
	if actual.Status != "closed" || actual.Metadata[beadmeta.RemoteWorkRequestMetadataKey] != "close1" {
		t.Fatal("close and receipt separated")
	}
}

func TestRemoteWorkInvalidOpsNeverReachPort(t *testing.T) {
	for _, r := range []remoteWorkRequest{{RequestID: "r", Op: "exec"}, {RequestID: "r", Op: "claim", BeadID: "other"}, {RequestID: "r", Op: "read", BeadID: "../x"}, {RequestID: "r", Op: "close", BeadID: "fixture-1", Text: "missing evidence"}} {
		f := &remoteWorkFake{}
		if _, e := runRemoteWork(context.Background(), "scope", r, remoteWorkTestJournal(t), f); e == nil {
			t.Fatalf("accepted %+v", r)
		}
		if f.effects != 0 {
			t.Fatal("effect on invalid request")
		}
	}
}

func TestRemoteWorkUncertainClaimAndDrainNeverReexecute(t *testing.T) {
	for _, op := range []string{"claim", "self_drain"} {
		j := remoteWorkTestJournal(t)
		r := remoteWorkRequest{RequestID: "r", Op: op}
		if e := j.Intent("scope", r); e != nil {
			t.Fatal(e)
		}
		f := &remoteWorkFake{}
		if _, e := runRemoteWork(context.Background(), "scope", r, j, f); e == nil {
			t.Fatal("uncertain action reported success")
		}
		if f.effects != 0 {
			t.Fatal("uncertain action replay")
		}
	}
}

func TestRemoteWorkOrdinaryCLIDeniedWithoutRootPrivateIntake(t *testing.T) {
	var out bytes.Buffer
	if e := receiveRemoteWork(-1, &out); e == nil {
		t.Fatal("ordinary caller admitted")
	}
	if out.Len() != 0 {
		t.Fatal("proof refusal emitted private data")
	}
}

func TestRemoteWorkCloseRequiresAtomicBackendCapability(t *testing.T) {
	s := beads.NewMemStore()
	b, e := s.Create(beads.Bead{ID: "fixture-1", Status: "in_progress"})
	if e != nil {
		t.Fatal(e)
	}
	g, _ := storebinding.NewBeadsGraphStore(s)
	guard := &remoteWorkOwnedGraph{GraphStore: g, store: s, revision: b.Revision, metadata: map[string]string{"marker": "must-not-write"}}
	if code := doBdByIDClose(guard, bdByIDOp{ID: b.ID}, "fixture", io.Discard, io.Discard); code == 0 {
		t.Fatal("non-atomic backend accepted")
	}
	now, _ := s.Get(b.ID)
	if now.Status == "closed" || now.Metadata["marker"] != "" {
		t.Fatal("partial close on unsupported capability")
	}
}

func TestRemoteWorkResultMustCarryCanonicalWorkOutcome(t *testing.T) {
	r := remoteWorkRequest{RequestID: "r1", Op: "result", BeadID: "fixture-1", Text: "checked", EvidenceSHA: strings.Repeat("e", 64)}
	if validateRemoteWorkRequest(r) == nil {
		t.Fatal("missing outcome accepted")
	}
	for _, v := range []string{"shipped", "no-op", "blocked", "abandoned"} {
		r.Outcome = v
		if e := validateRemoteWorkRequest(r); e != nil {
			t.Fatalf("canonical outcome %s refused", v)
		}
	}
	r.Outcome = "success"
	if validateRemoteWorkRequest(r) == nil {
		t.Fatal("invented outcome accepted")
	}
}

func TestRemoteWorkDifferentRequestCannotBypassUncertainScope(t *testing.T) {
	j := remoteWorkTestJournal(t)
	pending := remoteWorkRequest{RequestID: "first", Op: "claim"}
	if e := j.Intent("scope", pending); e != nil {
		t.Fatal(e)
	}
	f := &remoteWorkFake{}
	newRequest := remoteWorkRequest{RequestID: "second", Op: "read", BeadID: "fixture-1"}
	if _, e := runRemoteWork(context.Background(), "scope", newRequest, j, f); e == nil {
		t.Fatal("new request escaped scope hold")
	}
	if f.effects != 0 {
		t.Fatal("effect after uncertain scope")
	}
}

func TestRemoteWorkMalformedResponseCannotCommitOrReleaseScopeHold(t *testing.T) {
	j := remoteWorkTestJournal(t)
	r := remoteWorkRequest{RequestID: "first", Op: "claim"}
	if e := j.Intent("scope", r); e != nil {
		t.Fatal(e)
	}
	if e := j.Finish("scope", r, remoteWorkResponse{SchemaVersion: 1, RequestID: r.RequestID, State: "fake_success"}); e == nil {
		t.Fatal("invalid response committed")
	}
	if _, _, e := j.Load("scope", remoteWorkRequest{RequestID: "second", Op: "claim"}); e == nil {
		t.Fatal("invalid receipt released hold")
	}
}

func TestRemoteWorkForgedJournalKindAndMalformedCompletedReceiptHoldScope(t *testing.T) {
	for _, kind := range []string{"intent-response", "bad-receipt"} {
		t.Run(kind, func(t *testing.T) {
			j := remoteWorkTestJournal(t)
			r := remoteWorkRequest{RequestID: "first", Op: "claim"}
			if e := j.Intent("scope", r); e != nil {
				t.Fatal(e)
			}
			rec := remoteWorkJournalRecord{Digest: remoteWorkRequestDigest(r), RequestID: r.RequestID, Op: r.Op, Response: &remoteWorkResponse{SchemaVersion: 1, RequestID: r.RequestID, State: "fake_success"}}
			raw, _ := json.Marshal(rec)
			suffix := ".receipt"
			if kind == "intent-response" {
				suffix = ".intent"
			}
			if e := os.WriteFile(filepath.Join(j.dir, j.key("scope", r)+suffix), raw, 0o600); e != nil {
				t.Fatal(e)
			}
			if _, _, e := j.Load("scope", r); e == nil {
				t.Fatal("forged record accepted as receipt")
			}
			if _, _, e := j.Load("scope", remoteWorkRequest{RequestID: "next", Op: "claim"}); e == nil {
				t.Fatal("malformed completion released scope")
			}
		})
	}
}

type remoteWorkNativeHarness struct {
	n              *remoteWorkNative
	sessions, work beads.Store
	sid, bid       string
}

func remoteWorkNativeFixture(t *testing.T) remoteWorkNativeHarness {
	t.Helper()
	sessions := beads.NewAtomicCloseMemStore()
	work := beads.NewAtomicCloseMemStore()
	binding := `{"selector":"ssh","endpoint":"fixture-host","resource_domain":"fixture-domain","policy_digest":"fixture-policy"}`
	// Create returns canonical IDs; a pinned-ID test fixture must not pretend the
	// store honored an ID supplied to Create when its provider mints its own.
	_, _ = sessions.Create(beads.Bead{Type: "task", Title: "unrelated fixture"})
	sb, e := sessions.Create(beads.Bead{Type: "session", Status: "open", Labels: []string{"gc:session"}, Metadata: map[string]string{"template": "worker", "session_name": "fixture-runtime", "generation": "1", "instance_token": strings.Repeat("a", 32), "state": "active", "session_origin": "ephemeral", "runtime_binding_v1": binding}})
	if e != nil {
		t.Fatal(e)
	}
	wb, e := work.Create(beads.Bead{Type: "task", Status: "in_progress", Assignee: sb.ID, Metadata: map[string]string{beadmeta.RoutedToMetadataKey: "worker", beadmeta.SessionIDMetadataKey: sb.ID}})
	if e != nil {
		t.Fatal(e)
	}
	status := "in_progress"
	if e = work.Update(wb.ID, beads.UpdateOpts{Status: &status}); e != nil {
		t.Fatal(e)
	}
	front := sessionpkg.NewStore(beads.SessionStore{Store: sessions})
	_, e = front.SetCurrentClaim(sb.ID, wb.ID)
	if e != nil {
		t.Fatal(e)
	}
	profile := remoteWorkProfile{ID: "fixture", CityPath: t.TempDir(), Template: "worker", ModelUID: 1201, RuntimeProvider: "ssh", RuntimeTarget: "fixture-host", ResourceDomain: "fixture-domain", PolicyDigest: "fixture-policy", SourceSHA: strings.Repeat("d", 40), ManifestSHA: strings.Repeat("e", 64)}
	frame := remoteWorkFrame{SchemaVersion: 1, ProfileID: profile.ID, Identity: remoteWorkIdentity{SID: sb.ID, Template: "worker", Handle: "fixture-runtime", Generation: "1", InstanceToken: strings.Repeat("a", 32), Attempt: strings.Repeat("b", 32)}, Observe: remoteWorkObservation{ReceiptID: "receipt", Revision: "4", BootID: "boot", Unit: "fixture.service", Cgroup: "/fixture", InvocationID: "invocation", ModelUID: 1201, PID: 44, Start: "60", Phase: "entered", SourceSHA: profile.SourceSHA, ManifestSHA: profile.ManifestSHA, ObservedAt: time.Now().UTC()}}
	n := &remoteWorkNative{profile: profile, policy: remoteWorkPolicy{Profiles: []remoteWorkProfile{profile}}, frame: frame, front: front, assignee: sb.ID, resolveStore: func(string) (beads.Store, error) { return work, nil }, lease: func(_ context.Context, _ string, write func() error) error { return write() }}
	if e = n.Validate(context.Background()); e != nil {
		t.Fatal("invalid native fixture", e)
	}
	return remoteWorkNativeHarness{n, sessions, work, sb.ID, wb.ID}
}

func TestRemoteWorkNativeIdentityChangesDenyBeforeClaimOrWrite(t *testing.T) {
	for _, key := range []string{"template", "session_name", "generation", "instance_token", "state", "runtime_binding_v1", "continuation_reset_pending"} {
		t.Run(key, func(t *testing.T) {
			h := remoteWorkNativeFixture(t)
			n := h.n
			if e := h.sessions.SetMetadata(h.sid, key, "changed"); e != nil {
				t.Fatal(e)
			}
			called := false
			n.claim = func() ([]byte, int) { called = true; return nil, 0 }
			if _, e := n.Execute(context.Background(), remoteWorkRequest{RequestID: "r", Op: "claim"}); e == nil {
				t.Fatal("stale native identity claimed")
			}
			if called {
				t.Fatal("hook reached stale identity")
			}
			r := remoteWorkRequest{RequestID: "r2", Op: "result", BeadID: h.bid, Text: "checked", Outcome: "no-op", EvidenceSHA: strings.Repeat("e", 64)}
			if _, e := n.Execute(context.Background(), r); e == nil {
				t.Fatal("stale identity wrote result")
			}
			b, _ := h.work.Get(h.bid)
			if b.Metadata[beadmeta.RemoteWorkResultMetadataKey] != "" {
				t.Fatal("stale write persisted")
			}
		})
	}
}

func TestRemoteWorkNativeOwnedScopeMatrix(t *testing.T) {
	for _, key := range []string{"assignee", beadmeta.RoutedToMetadataKey, beadmeta.SessionIDMetadataKey, "status", "current_claim"} {
		t.Run(key, func(t *testing.T) {
			h := remoteWorkNativeFixture(t)
			n := h.n
			switch key {
			case "assignee":
				v := "foreign"
				_ = h.work.Update(h.bid, beads.UpdateOpts{Assignee: &v})
			case "status":
				_ = h.work.Close(h.bid)
			case "current_claim":
				_, _ = n.front.SetCurrentClaim(h.sid, "other")
			default:
				_ = h.work.SetMetadata(h.bid, key, "foreign")
			}
			if _, e := n.Execute(context.Background(), remoteWorkRequest{RequestID: "r", Op: "read", BeadID: h.bid}); e == nil {
				t.Fatal("foreign native scope read")
			}
		})
	}
}

func TestRemoteWorkNativeResultCloseAndPostLeaseFence(t *testing.T) {
	h := remoteWorkNativeFixture(t)
	n := h.n
	r := remoteWorkRequest{RequestID: "result", Op: "result", BeadID: h.bid, Text: "before to after; control and natural cycle passed", Outcome: "no-op", EvidenceSHA: strings.Repeat("f", 64)}
	if _, e := n.Execute(context.Background(), r); e != nil {
		t.Fatal("native result", e)
	}
	closeRequest := remoteWorkRequest{RequestID: "close", Op: "close", BeadID: r.BeadID, Text: "verified", EvidenceSHA: r.EvidenceSHA}
	if _, e := n.Execute(context.Background(), closeRequest); e != nil {
		t.Fatal("native close", e)
	}
	b, _ := h.work.Get(h.bid)
	if b.Status != "closed" || b.Metadata[beadmeta.WorkOutcomeMetadataKey] != "no-op" {
		t.Fatal("canonical result/close missing")
	}
	h = remoteWorkNativeFixture(t)
	n = h.n
	r.BeadID = h.bid
	n.lease = func(_ context.Context, _ string, write func() error) error {
		_ = h.sessions.SetMetadata(h.sid, "instance_token", strings.Repeat("c", 32))
		return write()
	}
	if _, e := n.Execute(context.Background(), r); e == nil {
		t.Fatal("post-lease incarnation drift wrote")
	}
	b, _ = h.work.Get(h.bid)
	if b.Metadata[beadmeta.RemoteWorkResultMetadataKey] != "" {
		t.Fatal("post-lease effect")
	}
}

func TestRemoteWorkNativeCloseWithoutResultAndInvalidOutcomeDeny(t *testing.T) {
	h := remoteWorkNativeFixture(t)
	n := h.n
	r := remoteWorkRequest{RequestID: "close", Op: "close", BeadID: h.bid, Text: "must not close", EvidenceSHA: strings.Repeat("f", 64)}
	if _, e := n.Execute(context.Background(), r); e == nil {
		t.Fatal("closed without result")
	}
	_ = h.work.SetMetadata(h.bid, beadmeta.RemoteWorkResultMetadataKey, "unverified")
	_ = h.work.SetMetadata(h.bid, beadmeta.RemoteWorkEvidenceSHAMetadataKey, r.EvidenceSHA)
	if _, e := n.Execute(context.Background(), r); e == nil {
		t.Fatal("closed without canonical work outcome")
	}
	b, _ := h.work.Get(h.bid)
	if b.Status == "closed" {
		t.Fatal("close gate bypass")
	}
}

func TestRemoteWorkNativeWrongSIDAndBeadIdentityDeny(t *testing.T) {
	h := remoteWorkNativeFixture(t)
	h.n.frame.Identity.SID = "missing-session"
	if h.n.Validate(context.Background()) == nil {
		t.Fatal("wrong SID admitted")
	}
	h = remoteWorkNativeFixture(t)
	if _, e := h.n.Execute(context.Background(), remoteWorkRequest{RequestID: "r", Op: "read", BeadID: "foreign-1"}); e == nil {
		t.Fatal("foreign bead read")
	}
}

// This exercises the real native receiver wrapper with fake runtime ports;
// it is deliberately not evidence of an actual SSH write or readback.
func TestRemoteWorkNativeDrainRequiresRuntimeAckReadback(t *testing.T) {
	for _, failure := range []string{"none", "missing", "ack", "source", "token", "read-error", "identity-drift"} {
		t.Run(failure, func(t *testing.T) {
			h := remoteWorkNativeFixture(t)
			n := h.n
			if e := h.work.Close(h.bid); e != nil {
				t.Fatal(e)
			}
			calls := 0
			reads := 0
			n.drain = func() error { calls++; return nil }
			expected := map[string]string{"GC_DRAIN_ACK": "1", reconcilerDrainAckSourceKey: drainAckSourceAgentValue, drainAckRequesterInstanceTokenKey: drainAckInstanceTokenDigest(n.frame.Identity.InstanceToken)}
			n.drainMeta = func(handle, key string) (string, error) {
				reads++
				if calls != 1 || handle != n.frame.Identity.Handle {
					t.Fatal("readback did not follow exact-handle canonical handler")
				}
				value, ok := expected[key]
				if !ok {
					t.Fatal("unexpected metadata read", key)
				}
				switch failure {
				case "missing":
					return "", nil
				case "ack":
					if key == "GC_DRAIN_ACK" {
						return "0", nil
					}
				case "source":
					if key == reconcilerDrainAckSourceKey {
						return "reconciler", nil
					}
				case "token":
					if key == drainAckRequesterInstanceTokenKey {
						return strings.Repeat("f", 64), nil
					}
				case "read-error":
					return "", errors.New("fake unreadable runtime")
				case "identity-drift":
					if reads == 3 {
						if e := h.sessions.SetMetadata(h.sid, "generation", "2"); e != nil {
							t.Fatal(e)
						}
					}
				}
				return value, nil
			}
			j := remoteWorkTestJournal(t)
			r := remoteWorkRequest{RequestID: "drain", Op: "self_drain"}
			out, e := runRemoteWork(context.Background(), "drain-scope", r, j, n)
			if failure == "none" {
				if e != nil || out.State != "drain_acknowledged" || reads != 3 {
					t.Fatalf("verified drain: state=%s reads=%d error=%v", out.State, reads, e)
				}
				return
			}
			if e == nil {
				t.Fatal("unverified runtime drain acknowledged")
			}
			if calls != 1 {
				t.Fatal("canonical handler repeated")
			}
			if failure != "identity-drift" {
				if _, e = runRemoteWork(context.Background(), "drain-scope", r, j, n); e == nil {
					t.Fatal("uncertain drain reconciled without proof")
				}
				if _, e = runRemoteWork(context.Background(), "drain-scope", remoteWorkRequest{RequestID: "new-drain", Op: "self_drain"}, j, n); e == nil {
					t.Fatal("new ID escaped uncertain drain scope")
				}
			}
			if calls != 1 {
				t.Fatal("uncertain drain retried")
			}
			record, exists, e := j.Load("drain-scope", r)
			if e != nil || !exists || record.Response != nil {
				t.Fatal("uncertain drain lost intent or acquired completion")
			}
		})
	}
}
