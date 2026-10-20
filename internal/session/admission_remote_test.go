package session

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/rollout/gate"
	"github.com/gastownhall/gascity/internal/runtime"
)

type remoteAdmissionTestProvider struct {
	*runtime.Fake
	binding runtime.RuntimeBinding
	bindErr error
	store   beads.Store
	starts  int
	start   func(context.Context) error
	lastSID string
}

func (p *remoteAdmissionTestProvider) BindRuntime(_ context.Context, id, _ string) (runtime.RuntimeBinding, error) {
	if p.store != nil {
		if p.lastSID == "" {
			p.lastSID = id
		}
		if err := NewStore(beads.SessionStore{Store: p.store}).PinRuntimeBinding(id, p.binding); err != nil {
			return runtime.RuntimeBinding{}, err
		}
	}
	return p.binding, p.bindErr
}

func (p *remoteAdmissionTestProvider) ResolveRuntime(string) (runtime.RuntimeBinding, error) {
	return p.binding, p.bindErr
}

func (p *remoteAdmissionTestProvider) Start(ctx context.Context, _ string, cfg runtime.Config) error {
	p.starts++
	if cfg.CommandScope != nil {
		return errors.New("remote start received local CommandScope")
	}
	if p.start != nil {
		return p.start(ctx)
	}
	return nil
}

type remoteAdmissionTestAuthority struct {
	profile                    RemoteAdmissionProfile
	prepared                   RemoteAdmissionReceipt
	prepares, begins, finishes int
	occupied                   bool
	phase                      RemoteAdmissionPhase
	finishErr                  error
	finishContextCanceled      bool
}

func newRemoteAdmissionTestAuthority(binding runtime.RuntimeBinding) *remoteAdmissionTestAuthority {
	a := &remoteAdmissionTestAuthority{phase: RemoteAdmissionEntered}
	a.profile = RemoteAdmissionProfile{UnitPrefix: "gc-remote", CgroupParent: "/system.slice", Binding: binding, SourceSHA: strings.Repeat("a", 40), ManifestSHA256: strings.Repeat("b", 64), ModelUID: 997, TransportUID: 998}
	a.profile.Prepare = func(_ context.Context, r RemoteAdmissionRequest) (RemoteAdmissionReceipt, error) {
		a.prepares++
		if a.occupied {
			return RemoteAdmissionReceipt{}, errors.New("VM attempt occupied")
		}
		a.occupied = true
		a.prepared = RemoteAdmissionReceipt{
			Request: r, AuthorityReceiptID: "vm-receipt", JournalRevision: 1, Phase: RemoteAdmissionPrepared,
			Identity: RemoteVMIdentity{ScopeIdentity: RemoteScopeIdentity{BootID: strings.Repeat("c", 32), CgroupPath: a.profile.CgroupParent, Device: 1, Inode: 9}, BootID: strings.Repeat("c", 32), SourceSHA: a.profile.SourceSHA, ManifestSHA256: a.profile.ManifestSHA256, ModelUID: 997, TransportUID: 998},
		}
		return a.prepared, nil
	}
	a.profile.Begin = func(_ context.Context, r RemoteAdmissionReceipt) (RemoteBeginReceipt, error) {
		a.begins++
		return RemoteBeginReceipt{Reservation: r, IntentID: "controller-intent"}, nil
	}
	a.profile.Finish = func(ctx context.Context, r RemoteBeginReceipt, _ error) (RemoteAdmissionReceipt, error) {
		a.finishes++
		a.finishContextCanceled = ctx.Err() != nil
		if _, ok := ctx.Deadline(); !ok {
			return RemoteAdmissionReceipt{}, errors.New("finalization has no bound")
		}
		receipt := r.Reservation
		receipt.Phase = a.phase
		receipt.JournalRevision++
		receipt.Identity.Unit = a.profile.UnitPrefix + "-" + r.Reservation.Request.Attempt + ".service"
		receipt.Identity.InvocationID = strings.Repeat("d", 32)
		receipt.Identity.CgroupPath = "/system.slice/" + receipt.Identity.Unit
		receipt.Identity.CgroupDevice = 1
		receipt.Identity.CgroupInode = 2
		return receipt, a.finishErr
	}
	return a
}

func remoteAdmissionFixture(t *testing.T) (string, *beads.MemStore, beads.Bead, *remoteAdmissionTestProvider, *remoteAdmissionTestAuthority) {
	t.Helper()
	city := t.TempDir()
	store := runtimeBindingMemStore(t)
	b := nativeTestSession(t, store)
	if err := store.SetMetadataBatch(b.ID, map[string]string{"generation": "7", "instance_token": strings.Repeat("e", 32)}); err != nil {
		t.Fatal(err)
	}
	b, _ = store.Get(b.ID)
	binding := runtime.RuntimeBinding{Selector: "ssh:executor@example", Endpoint: "executor@example", ResourceDomain: "remote-domain", PolicyDigest: strings.Repeat("f", 64)}
	p := &remoteAdmissionTestProvider{Fake: runtime.NewFake(), binding: binding, store: store}
	return city, store, b, p, newRemoteAdmissionTestAuthority(binding)
}

func remoteAdmissionConfig(b beads.Bead) runtime.Config {
	return runtime.Config{Command: "remote-command", Env: map[string]string{"GC_SESSION_ID": b.ID, "GC_TEMPLATE": "worker", "GC_SESSION_NAME": b.Metadata["session_name"], "GC_RUNTIME_EPOCH": b.Metadata["generation"], "GC_INSTANCE_TOKEN": b.Metadata["instance_token"]}}
}

func remoteAdmissionContext(t *testing.T, a *remoteAdmissionTestAuthority) context.Context {
	t.Helper()
	ctx, err := WithRemoteAdmissionProfiles(context.Background(), a.profile)
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

func TestRemoteAdmissionFullLocalCapDoesNotSpendLocalSeat(t *testing.T) {
	city, _, host, _, store := nativeTestFixture(t, 3)
	_, casErr := beads.OpenStoreAtForCity(context.Background(), beads.StoreOpenOptions{Provider: "file", ConditionalWrites: gate.Require, OpenFileStore: func() (beads.Store, error) { return store, nil }})
	if casErr != nil {
		t.Fatal(casErr)
	}
	_, _, b, p, a := remoteAdmissionFixture(t)
	p.store = store
	b.ID = ""
	b, err := store.Create(b)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SetMetadata(b.ID, "session_name", "s-"+b.ID); err != nil {
		t.Fatal(err)
	}
	b, _ = store.Get(b.ID)
	before, err := os.ReadFile(nativeCapacityPath(city))
	if err != nil {
		t.Fatal(err)
	}
	ctx, ok, reason := admitCapacityStartWithHost(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now(), p, store, host)
	if !ok {
		t.Fatalf("remote blocked by local cap3: %s", reason)
	}
	// Transfer only the opaque proof, not the registry; never Prepare twice.
	ctx = WithCapacityAdmission(context.Background(), ctx)
	m := NewManagerWithOptions(store, p, WithCityPath(city))
	if err = m.startRuntime(ctx, b.Metadata["session_name"], remoteAdmissionConfig(b)); err != nil {
		t.Fatal(err)
	}
	if a.prepares != 1 || a.begins != 1 || a.finishes != 1 || p.starts != 1 {
		t.Fatalf("attempt calls prepare=%d begin=%d finish=%d start=%d", a.prepares, a.begins, a.finishes, p.starts)
	}
	after, err := os.ReadFile(nativeCapacityPath(city))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("remote start changed local cap ledger")
	}
	if err = m.startRuntime(ctx, b.Metadata["session_name"], remoteAdmissionConfig(b)); err == nil {
		t.Fatal("one-shot proof reused")
	}
	if p.starts != 1 || a.prepares != 1 {
		t.Fatal("proof reuse started or prepared twice")
	}
}

func TestRemoteAdmissionMissingOrWrongProfileNeverFallsBack(t *testing.T) {
	for _, wrong := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "wrong-pin"}[wrong], func(t *testing.T) {
			city, store, b, p, a := remoteAdmissionFixture(t)
			ctx := context.Background()
			if wrong {
				a.profile.Binding.PolicyDigest = strings.Repeat("0", 64)
				ctx = remoteAdmissionContext(t, a)
			}
			_, ok, _ := AdmitCapacityStart(ctx, city, "worker", b.ID, time.Now(), p, store)
			if ok || a.prepares != 0 || p.starts != 0 {
				t.Fatal("remote silently fell back to unconfigured local admission")
			}
		})
	}
}

func TestRemoteAdmissionBeginRejectsChangedLiveIncarnation(t *testing.T) {
	for _, key := range []string{"generation", "instance_token", "session_name", "template"} {
		t.Run(key, func(t *testing.T) {
			city, store, b, p, a := remoteAdmissionFixture(t)
			ctx, ok, reason := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now(), p, store)
			if !ok {
				t.Fatal(reason)
			}
			if err := store.SetMetadata(b.ID, key, "changed"); err != nil {
				t.Fatal(err)
			}
			m := NewManagerWithOptions(store, p, WithCityPath(city))
			err := m.startRuntime(ctx, b.Metadata["session_name"], remoteAdmissionConfig(b))
			if err == nil || p.starts != 0 || a.begins != 0 || !a.occupied {
				t.Fatalf("stale incarnation started, err=%v", err)
			}
		})
	}
}

func TestRemoteAdmissionCanceledStartStillObservesWithFreshBoundedContext(t *testing.T) {
	city, store, b, p, a := remoteAdmissionFixture(t)
	ctx, cancel := context.WithCancel(remoteAdmissionContext(t, a))
	defer cancel()
	p.start = func(context.Context) error { cancel(); return context.Canceled }
	a.phase = RemoteAdmissionUnknown
	admitted, ok, reason := AdmitCapacityStart(ctx, city, "worker", b.ID, time.Now(), p, store)
	if !ok {
		t.Fatal(reason)
	}
	m := NewManagerWithOptions(store, p, WithCityPath(city))
	err := m.startRuntime(admitted, b.Metadata["session_name"], remoteAdmissionConfig(b))
	if err == nil || !errors.Is(err, context.Canceled) || a.finishes != 1 || a.finishContextCanceled || !a.occupied {
		t.Fatalf("unknown canceled attempt lost: err=%v finish=%d canceled=%v", err, a.finishes, a.finishContextCanceled)
	}
	if _, ok, _ := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now(), p, store); ok {
		t.Fatal("unknown occupied attempt was released")
	}
}

func TestRemoteAdmissionFinishRequiresVMEnteredEvidence(t *testing.T) {
	for _, phase := range []RemoteAdmissionPhase{RemoteAdmissionPrepared, RemoteAdmissionUnknown, RemoteAdmissionPhase("unexpected")} {
		t.Run(string(phase), func(t *testing.T) {
			city, store, b, p, a := remoteAdmissionFixture(t)
			a.phase = phase
			admitted, ok, reason := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now(), p, store)
			if !ok {
				t.Fatal(reason)
			}
			m := NewManagerWithOptions(store, p, WithCityPath(city))
			if err := m.startRuntime(admitted, b.Metadata["session_name"], remoteAdmissionConfig(b)); err == nil {
				t.Fatal("provider exit0 treated as VM entry proof")
			}
			if p.starts != 1 || !a.occupied {
				t.Fatal("ambiguous attempt released")
			}
		})
	}
}

func TestRemoteAdmissionLocalNativeCapRemainsUnchanged(t *testing.T) {
	city, _, host, p, store := nativeTestFixture(t, 3)
	b := nativeTestSession(t, store)
	if _, ok, reason := admitCapacityStartWithHost(context.Background(), city, "worker", b.ID, time.Now(), p, store, host); ok || reason != "native capacity full" {
		t.Fatalf("local full cap bypassed: ok=%v reason=%s", ok, reason)
	}
}

func TestRemoteAdmissionIncompleteBackendCannotRegister(t *testing.T) {
	_, _, _, _, a := remoteAdmissionFixture(t)
	a.profile.Begin = nil
	if _, err := WithRemoteAdmissionProfiles(context.Background(), a.profile); err == nil {
		t.Fatal("incomplete authority registered")
	}
}

func TestRemoteAdmissionPreparedReceiptIdentityCannotDrift(t *testing.T) {
	city, store, b, p, a := remoteAdmissionFixture(t)
	original := a.profile.Prepare
	a.profile.Prepare = func(ctx context.Context, r RemoteAdmissionRequest) (RemoteAdmissionReceipt, error) {
		v, e := original(ctx, r)
		v.Request.Generation = "other"
		return v, e
	}
	if _, ok, _ := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now(), p, store); ok || p.starts != 0 {
		t.Fatal("wrong VM incarnation accepted")
	}
}

func TestRemoteAdmissionEnteredWithoutKernelProofRemainsHeld(t *testing.T) {
	city, store, b, p, a := remoteAdmissionFixture(t)
	original := a.profile.Finish
	a.profile.Finish = func(ctx context.Context, r RemoteBeginReceipt, e error) (RemoteAdmissionReceipt, error) {
		v, err := original(ctx, r, e)
		v.Identity.CgroupInode = 0
		return v, err
	}
	admitted, ok, reason := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now(), p, store)
	if !ok {
		t.Fatal(reason)
	}
	m := NewManagerWithOptions(store, p, WithCityPath(city))
	if err := m.startRuntime(admitted, b.Metadata["session_name"], remoteAdmissionConfig(b)); err == nil || !a.occupied {
		t.Fatal("incomplete VM kernel proof accepted")
	}
}

func TestRemoteAdmissionMissingOrNoncanonicalIncarnationNeverPrepares(t *testing.T) {
	for _, tc := range []struct{ key, value string }{{"generation", ""}, {"generation", "01"}, {"generation", "0"}, {"instance_token", ""}, {"instance_token", "malformed"}} {
		t.Run(tc.key+"-"+tc.value, func(t *testing.T) {
			city, store, b, p, a := remoteAdmissionFixture(t)
			if err := store.SetMetadata(b.ID, tc.key, tc.value); err != nil {
				t.Fatal(err)
			}
			if _, ok, _ := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now(), p, store); ok || a.prepares != 0 {
				t.Fatal("invalid durable incarnation prepared remote attempt")
			}
		})
	}
}

func TestRemoteAdmissionPreparedProofRejectsChangedBinding(t *testing.T) {
	city, store, b, p, a := remoteAdmissionFixture(t)
	admitted, ok, reason := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now(), p, store)
	if !ok {
		t.Fatal(reason)
	}
	p.binding.PolicyDigest = strings.Repeat("0", 64)
	m := NewManagerWithOptions(store, p, WithCityPath(city))
	if err := m.startRuntime(admitted, b.Metadata["session_name"], remoteAdmissionConfig(b)); err == nil || p.starts != 0 || a.begins != 0 || !a.occupied {
		t.Fatal("prepared proof accepted changed immutable runtime pin")
	}
}

func TestRemoteAdmissionBeginReceiptCannotReplaceReservation(t *testing.T) {
	city, store, b, p, a := remoteAdmissionFixture(t)
	original := a.profile.Begin
	a.profile.Begin = func(ctx context.Context, r RemoteAdmissionReceipt) (RemoteBeginReceipt, error) {
		v, e := original(ctx, r)
		v.Reservation.AuthorityReceiptID = "other"
		return v, e
	}
	admitted, ok, reason := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now(), p, store)
	if !ok {
		t.Fatal(reason)
	}
	m := NewManagerWithOptions(store, p, WithCityPath(city))
	if err := m.startRuntime(admitted, b.Metadata["session_name"], remoteAdmissionConfig(b)); err == nil || p.starts != 0 || !a.occupied {
		t.Fatal("Begin replaced VM reservation")
	}
}

func TestRemoteAdmissionPublicCreatePreservesOccupiedSource(t *testing.T) {
	for _, fault := range []string{"prepare", "begin", "finish-unknown", "finish-error", "entered-lost-start"} {
		t.Run(fault, func(t *testing.T) {
			city, _, _, p, a := remoteAdmissionFixture(t)
			store := runtimeBindingMemStore(t)
			p.store = store
			originalPrepare := a.profile.Prepare
			if fault == "prepare" {
				a.profile.Prepare = func(ctx context.Context, r RemoteAdmissionRequest) (RemoteAdmissionReceipt, error) {
					_, _ = originalPrepare(ctx, r)
					return RemoteAdmissionReceipt{}, errors.New("lost Prepare response")
				}
			}
			if fault == "begin" {
				a.profile.Begin = func(context.Context, RemoteAdmissionReceipt) (RemoteBeginReceipt, error) {
					a.begins++
					return RemoteBeginReceipt{}, errors.New("lost intent response")
				}
			}
			if fault == "finish-unknown" {
				a.phase = RemoteAdmissionUnknown
			}
			if fault == "finish-error" {
				a.finishErr = errors.New("lost Finish response")
			}
			if fault == "entered-lost-start" {
				p.start = func(context.Context) error { return errors.New("lost Start response") }
			}
			m := NewManagerWithOptions(store, p, WithCityPath(city))
			opts := CreateOptions{Template: "worker", ExplicitName: "remote-canary", Command: "fake-command", WorkDir: t.TempDir(), Provider: "fake"}
			_, err := m.CreateSession(remoteAdmissionContext(t, a), opts)
			if err == nil {
				t.Fatal("ambiguous operation returned success")
			}
			r := a.prepared.Request
			b, getErr := store.Get(r.SessionID)
			if getErr != nil || b.Status == "closed" || b.Metadata["session_name"] != r.RuntimeHandle || b.Metadata["template"] != r.Template || b.Metadata["generation"] != r.Generation || b.Metadata["instance_token"] != r.InstanceToken || !a.occupied {
				t.Fatalf("occupied source identity rolled back: status=%s err=%v", b.Status, getErr)
			}
			pinned, present, e := NewStore(beads.SessionStore{Store: store}).RuntimeBinding(b.ID)
			if e != nil || !present || pinned != r.Binding {
				t.Fatal("occupied runtime pin lost")
			}
			beforePrepares, beforeStarts := a.prepares, p.starts
			if _, e = m.CreateSession(remoteAdmissionContext(t, a), opts); e == nil {
				t.Fatal("same explicit source retried")
			}
			if a.prepares != beforePrepares || p.starts != beforeStarts {
				t.Fatal("source identity retry created another attempt")
			}
		})
	}
}

func TestRemoteAdmissionKillFenceWithinAuthorityWindow(t *testing.T) {
	for _, window := range []string{"before-prepare", "prepare", "begin"} {
		t.Run(window, func(t *testing.T) {
			city, store, b, p, a := remoteAdmissionFixture(t)
			kill := func() {
				patch := KillPendingPatch(time.Now().UTC())
				meta := map[string]string{}
				for k, v := range patch {
					meta[k] = v
				}
				if err := store.SetMetadataBatch(b.ID, meta); err != nil {
					t.Fatal(err)
				}
			}
			if window == "before-prepare" {
				kill()
			}
			if window == "prepare" {
				original := a.profile.Prepare
				a.profile.Prepare = func(ctx context.Context, r RemoteAdmissionRequest) (RemoteAdmissionReceipt, error) {
					v, e := original(ctx, r)
					kill()
					return v, e
				}
			}
			if window == "begin" {
				original := a.profile.Begin
				a.profile.Begin = func(ctx context.Context, r RemoteAdmissionReceipt) (RemoteBeginReceipt, error) {
					v, e := original(ctx, r)
					kill()
					return v, e
				}
			}
			ctx, ok, _ := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now(), p, store)
			if ok {
				m := NewManagerWithOptions(store, p, WithCityPath(city))
				if e := m.startRuntime(ctx, b.Metadata["session_name"], remoteAdmissionConfig(b)); e == nil {
					t.Fatal("kill fence was ignored")
				}
			}
			if p.starts != 0 {
				t.Fatal("kill fenced provider started")
			}
			if window == "before-prepare" && a.prepares != 0 {
				t.Fatal("kill fence reserved new attempt")
			}
			if window != "before-prepare" && !a.occupied {
				t.Fatal("kill window released occupied attempt")
			}
		})
	}
}

func TestRemoteAdmissionChangedStartFenceCannotBegin(t *testing.T) {
	for _, key := range []string{"state", "pending_create_claim", "sleep_intent", "held_until"} {
		t.Run(key, func(t *testing.T) {
			city, store, b, p, a := remoteAdmissionFixture(t)
			ctx, ok, reason := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now(), p, store)
			if !ok {
				t.Fatal(reason)
			}
			if e := store.SetMetadata(b.ID, key, "revoked"); e != nil {
				t.Fatal(e)
			}
			m := NewManagerWithOptions(store, p, WithCityPath(city))
			if e := m.startRuntime(ctx, b.Metadata["session_name"], remoteAdmissionConfig(b)); e == nil || p.starts != 0 || !a.occupied {
				t.Fatal("changed lifecycle fence accepted")
			}
		})
	}
}

func TestRemoteAdmissionStrictFinishMutationMatrix(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*RemoteAdmissionReceipt)
	}{
		{"city", func(r *RemoteAdmissionReceipt) { r.Request.City += "x" }},
		{"sid", func(r *RemoteAdmissionReceipt) { r.Request.SessionID += "x" }},
		{"template", func(r *RemoteAdmissionReceipt) { r.Request.Template += "x" }},
		{"handle", func(r *RemoteAdmissionReceipt) { r.Request.RuntimeHandle += "x" }},
		{"generation", func(r *RemoteAdmissionReceipt) { r.Request.Generation = "99" }},
		{"token", func(r *RemoteAdmissionReceipt) { r.Request.InstanceToken = strings.Repeat("0", 32) }},
		{"attempt", func(r *RemoteAdmissionReceipt) { r.Request.Attempt = strings.Repeat("0", 32) }},
		{"binding", func(r *RemoteAdmissionReceipt) { r.Request.Binding.Endpoint += "x" }},
		{"receipt-id", func(r *RemoteAdmissionReceipt) { r.AuthorityReceiptID += "x" }},
		{"revision-equal", func(r *RemoteAdmissionReceipt) { r.JournalRevision = 1 }},
		{"revision-zero", func(r *RemoteAdmissionReceipt) { r.JournalRevision = 0 }},
		{"boot", func(r *RemoteAdmissionReceipt) { r.Identity.BootID = strings.Repeat("0", 32) }},
		{"source", func(r *RemoteAdmissionReceipt) { r.Identity.SourceSHA = strings.Repeat("0", 40) }},
		{"manifest", func(r *RemoteAdmissionReceipt) { r.Identity.ManifestSHA256 = strings.Repeat("0", 64) }},
		{"model-uid", func(r *RemoteAdmissionReceipt) { r.Identity.ModelUID = 0 }},
		{"transport-uid", func(r *RemoteAdmissionReceipt) { r.Identity.TransportUID = 0 }},
		{"unit", func(r *RemoteAdmissionReceipt) { r.Identity.Unit = "other.service" }},
		{"unit-path", func(r *RemoteAdmissionReceipt) { r.Identity.Unit = "bad/attempt.service" }},
		{"invocation", func(r *RemoteAdmissionReceipt) { r.Identity.InvocationID = "bad" }},
		{"cgroup-unrelated", func(r *RemoteAdmissionReceipt) { r.Identity.CgroupPath = "/system.slice/unrelated.service" }},
		{"cgroup-subtree", func(r *RemoteAdmissionReceipt) { r.Identity.CgroupPath = "/unexpected/" + r.Identity.Unit }},
		{"cgroup-relative", func(r *RemoteAdmissionReceipt) { r.Identity.CgroupPath = "relative" }},
		{"cgroup-dirty", func(r *RemoteAdmissionReceipt) { r.Identity.CgroupPath = "/system.slice/../" + r.Identity.Unit }},
		{"cgroup-device", func(r *RemoteAdmissionReceipt) { r.Identity.CgroupDevice = 0 }},
		{"cgroup-inode", func(r *RemoteAdmissionReceipt) { r.Identity.CgroupInode = 0 }},
		{"never-entered-is-not-started", func(r *RemoteAdmissionReceipt) { r.Identity.NeverEntered = true }},
		{"parent-inode-replaced", func(r *RemoteAdmissionReceipt) { r.Identity.ScopeIdentity.Inode++ }},
		{"parent-device-replaced", func(r *RemoteAdmissionReceipt) { r.Identity.ScopeIdentity.Device++ }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			city, store, b, p, a := remoteAdmissionFixture(t)
			original := a.profile.Finish
			a.profile.Finish = func(ctx context.Context, r RemoteBeginReceipt, e error) (RemoteAdmissionReceipt, error) {
				v, err := original(ctx, r, e)
				tc.mutate(&v)
				return v, err
			}
			ctx, ok, reason := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now(), p, store)
			if !ok {
				t.Fatal(reason)
			}
			m := NewManagerWithOptions(store, p, WithCityPath(city))
			if e := m.startRuntime(ctx, b.Metadata["session_name"], remoteAdmissionConfig(b)); e == nil {
				t.Fatal("malformed VM receipt confirmed entry")
			}
			if p.starts != 1 || a.finishes != 1 || !a.occupied {
				t.Fatal("malformed receipt lost attempt")
			}
			if _, e := os.Stat(nativeCapacityPath(city)); !os.IsNotExist(e) {
				t.Fatal("remote mutated local ledger")
			}
		})
	}
}

func TestRemoteAdmissionAmbiguousCallbacksDoNotBecomeFreshGrant(t *testing.T) {
	for _, fault := range []string{"prepare", "begin", "start", "finish", "finish-deadline"} {
		t.Run(fault, func(t *testing.T) {
			city, store, b, p, a := remoteAdmissionFixture(t)
			lost := errors.New("transport response lost")
			if fault == "prepare" {
				original := a.profile.Prepare
				a.profile.Prepare = func(ctx context.Context, r RemoteAdmissionRequest) (RemoteAdmissionReceipt, error) {
					_, _ = original(ctx, r)
					return RemoteAdmissionReceipt{}, lost
				}
			}
			if fault == "begin" {
				a.profile.Begin = func(context.Context, RemoteAdmissionReceipt) (RemoteBeginReceipt, error) {
					a.begins++
					return RemoteBeginReceipt{}, lost
				}
			}
			if fault == "start" {
				p.start = func(context.Context) error { return lost }
				original := a.profile.Finish
				a.profile.Finish = func(ctx context.Context, r RemoteBeginReceipt, e error) (RemoteAdmissionReceipt, error) {
					if !errors.Is(e, lost) {
						t.Fatal("Start result not passed to observer")
					}
					v, err := original(ctx, r, e)
					v.Phase = RemoteAdmissionUnknown
					return v, err
				}
			}
			if fault == "finish" {
				a.finishErr = lost
			}
			if fault == "finish-deadline" {
				a.finishErr = context.DeadlineExceeded
			}
			ctx, ok, _ := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now(), p, store)
			if ok {
				m := NewManagerWithOptions(store, p, WithCityPath(city))
				if e := m.startRuntime(ctx, b.Metadata["session_name"], remoteAdmissionConfig(b)); e == nil {
					t.Fatal("ambiguous callback reported success")
				}
			}
			oldAttempt := a.prepared.Request.Attempt
			beforeStarts := p.starts
			if _, allow, _ := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now().Add(365*24*time.Hour), p, store); allow {
				t.Fatal("TTL released unknown VM attempt")
			}
			if !a.occupied || a.prepared.Request.Attempt != oldAttempt || p.starts != beforeStarts {
				t.Fatal("ambiguous attempt replaced")
			}
		})
	}
}

func TestRemoteAdmissionOnePreparedProofConcurrentConsumption(t *testing.T) {
	city, store, b, p, a := remoteAdmissionFixture(t)
	ctx, ok, reason := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now(), p, store)
	if !ok {
		t.Fatal(reason)
	}
	if _, ok, _ = AdmitCapacityStart(ctx, city, "worker", b.ID, time.Now().Add(365*24*time.Hour), p, store); !ok || a.prepares != 1 {
		t.Fatal("prepared proof re-prepared or inferred TTL")
	}
	for _, identity := range []struct{ city, route, id string }{{city + "other", "worker", b.ID}, {city, "other", b.ID}, {city, "worker", "other"}} {
		if _, ok, _ = AdmitCapacityStart(ctx, identity.city, identity.route, identity.id, time.Now(), p, store); ok {
			t.Fatal("wrong proof tuple accepted")
		}
	}
	m := NewManagerWithOptions(store, p, WithCityPath(city))
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- m.startRuntime(ctx, b.Metadata["session_name"], remoteAdmissionConfig(b))
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	for e := range results {
		if e == nil {
			success++
		}
	}
	if success != 1 || a.prepares != 1 || a.begins != 1 || a.finishes != 1 || p.starts != 1 {
		t.Fatalf("one proof spent more than once: success=%d starts=%d", success, p.starts)
	}
}

func TestRemoteAdmissionExitedReceiptConfirmsPriorEntry(t *testing.T) {
	city, store, b, p, a := remoteAdmissionFixture(t)
	a.phase = RemoteAdmissionExited
	ctx, ok, reason := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now(), p, store)
	if !ok {
		t.Fatal(reason)
	}
	m := NewManagerWithOptions(store, p, WithCityPath(city))
	if e := m.startRuntime(ctx, b.Metadata["session_name"], remoteAdmissionConfig(b)); e != nil {
		t.Fatal(e)
	}
}

func TestRemoteAdmissionPublicCreateAndWakeRemainValid(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		city, _, _, p, a := remoteAdmissionFixture(t)
		store := runtimeBindingMemStore(t)
		p.store = store
		m := NewManagerWithOptions(store, p, WithCityPath(city))
		info, e := m.CreateSession(remoteAdmissionContext(t, a), CreateOptions{Template: "worker", ExplicitName: "remote-positive", Command: "fake-command", WorkDir: t.TempDir(), Provider: "fake"})
		if e != nil || info.ID == "" || info.Closed || p.starts != 1 {
			t.Fatalf("valid remote create refused: %v", e)
		}
	})
	t.Run("wake", func(t *testing.T) {
		city, store, b, p, a := remoteAdmissionFixture(t)
		m := NewManagerWithOptions(store, p, WithCityPath(city))
		if e := m.Start(remoteAdmissionContext(t, a), b.ID, "fake-command", runtime.Config{WorkDir: t.TempDir()}); e != nil || p.starts != 1 {
			t.Fatalf("valid remote wake refused: %v", e)
		}
	})
}

func TestRemoteAdmissionPublicCreatePreEffectDenialStillRollsBack(t *testing.T) {
	city, _, _, p, _ := remoteAdmissionFixture(t)
	store := runtimeBindingMemStore(t)
	p.store = store
	m := NewManagerWithOptions(store, p, WithCityPath(city))
	if _, e := m.CreateSession(context.Background(), CreateOptions{Template: "worker", ExplicitName: "pre-effect", Command: "fake-command", WorkDir: t.TempDir(), Provider: "fake"}); e == nil {
		t.Fatal("unregistered remote admitted")
	}
	b, e := store.Get(p.lastSID)
	if e != nil {
		t.Fatal(e)
	}
	if b.Status != "closed" || b.Metadata["session_name"] != "" || p.starts != 0 {
		t.Fatal("pre-effect denial no longer rolls back")
	}
}

func TestRemoteAdmissionLostStartIsTypedControllerHold(t *testing.T) {
	city, store, b, p, a := remoteAdmissionFixture(t)
	lost := errors.New("lost Start response")
	p.start = func(context.Context) error { return lost }
	ctx, ok, reason := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now(), p, store)
	if !ok {
		t.Fatal(reason)
	}
	m := NewManagerWithOptions(store, p, WithCityPath(city))
	e := m.startRuntime(ctx, b.Metadata["session_name"], remoteAdmissionConfig(b))
	if !IsRemoteAdmissionHeld(e) || !errors.Is(e, ErrCapacityAdmission) || !runtime.IsProviderCapacity(e) || !errors.Is(e, lost) {
		t.Fatal("controller cannot distinguish occupied remote result")
	}
}

func TestRemoteAdmissionPrepareUnknownMarkerSurvivesContextTransfer(t *testing.T) {
	city, store, b, p, a := remoteAdmissionFixture(t)
	original := a.profile.Prepare
	a.profile.Prepare = func(ctx context.Context, r RemoteAdmissionRequest) (RemoteAdmissionReceipt, error) {
		_, _ = original(ctx, r)
		return RemoteAdmissionReceipt{}, errors.New("lost Prepare response")
	}
	admitted, ok, _ := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", b.ID, time.Now(), p, store)
	if ok {
		t.Fatal("unknown prepare admitted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	transferred := WithCapacityAdmission(ctx, admitted)
	cancel()
	if !RemoteAdmissionMayHaveEffect(transferred) || !errors.Is(transferred.Err(), context.Canceled) {
		t.Fatal("source effect or recipient cancellation lost during transfer")
	}
}
