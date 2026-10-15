package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/runtime/observationattempt"
	"github.com/gastownhall/gascity/internal/runtime/procobserver"
)

func controllerReacquireFrames(t *testing.T, original []procobserver.ResponseV3, now time.Time, partialIndex int) []procobserver.ResponseV3 {
	t.Helper()
	b, err := json.Marshal(append(append([]procobserver.ResponseV3{}, original...), original...))
	if err != nil {
		t.Fatal(err)
	}
	var frames []procobserver.ResponseV3
	if err = json.Unmarshal(b, &frames); err != nil {
		t.Fatal(err)
	}
	for i := range frames {
		f := &frames[i]
		f.RequestNonce = strings.Repeat(string(rune('a'+i)), 64)
		f.FinishedAt = now.Add(time.Duration(i-3) * 3 * time.Millisecond)
		f.StartedAt = f.FinishedAt.Add(-3 * time.Millisecond)
		f.Roots[0].ParentName = "tmux: server"
		f.Roots[0].ParentIsProviderInfrastructure = true
	}
	if partialIndex < 0 {
		return frames
	}
	f := &frames[partialIndex]
	f.Complete = false
	f.Roots = []procobserver.Root{}
	c := &f.Census
	c.Scans = c.Scans[:2]
	c.SelectedClosings = [2]int{}
	c.Seal = procobserver.CensusSeal{}
	c.ReconciledCount, c.ReconciledDigest = 0, ""
	c.Proofs = []procobserver.CensusProofV3{}
	c.Certificates = []procobserver.DescendantCertificate{}
	c.Resolutions = []procobserver.CensusResolution{}
	for i := range c.Scans {
		s := &c.Scans[i]
		s.Verified = s.Verified[:1]
		s.LiveCount = 1
		s.NumericPIDs = []int{20}
		if i == 1 {
			s.NumericPIDs = append(s.NumericPIDs, 99)
		}
		s.EnumeratedCount = len(s.NumericPIDs)
		h := sha256.New()
		field := func(v string) {
			var prefix [4]byte
			binary.BigEndian.PutUint32(prefix[:], uint32(len(v)))
			_, _ = h.Write(prefix[:])
			_, _ = h.Write([]byte(v))
		}
		field("bounded-process-census/v3/enumerated-pids/1")
		for _, pid := range s.NumericPIDs {
			field(strconv.Itoa(pid))
		}
		s.EnumerationDigest = hex.EncodeToString(h.Sum(nil))
	}
	f.EnumeratedCountBefore = c.Scans[0].EnumeratedCount
	f.EnumerationDigestBefore = c.Scans[0].EnumerationDigest
	f.EnumeratedCountAfter = c.Scans[1].EnumeratedCount
	f.EnumerationDigestAfter = c.Scans[1].EnumerationDigest
	yes, no, start := true, false, "999"
	f.Errors = []procobserver.EvidenceError{{Reason: "process_unavailable", Operation: "environ", Errno: 3, PID: 99, StartTicks: &start, ScanIndex: 2, CaptureDiagnostic: &procobserver.CaptureDiagnostic{FieldsPass: 1, BoundLive: &yes, FirstIdentityPresent: &no, ExitPoll: "zero", ExitWitness: &no}}}
	f.ErrorsTotal = 1
	return frames
}

func TestControllerV3ReacquireBindingFollowsReviewedConstant(t *testing.T) {
	s := newControllerObservationServiceV3(context.Background(), t.TempDir())
	if s.reacquireEnabled != controllerObservationV3WholeReacquisitionEnabled {
		t.Fatal("constructor does not consume separately reviewed binding")
	}
}

func TestControllerV3ReacquireServiceWholeTransactions(t *testing.T) {
	for _, mode := range []string{"complete-first", "partial-first", "partial-second", "both-partial", "both-managed-retired", "both-second-epoch-drift", "both-second-token-drift", "durable-readback", "retention-failed", "record-failed", "permission", "tracker-failed", "provider-changed", "root-replaced", "caller-changed", "policy-changed", "generation-changed", "nonce-reused", "second-incomplete", "default-single"} {
		t.Run(mode, func(t *testing.T) {
			s, cs, p, original := controllerV3ServiceFixture(t)
			clock := s.now()
			current := clock.Add(-6 * time.Millisecond)
			s.now = func() time.Time { return current }
			index := 0
			switch mode {
			case "complete-first":
				index = -1
			case "partial-second":
				index = 1
			}
			frames := controllerReacquireFrames(t, original, clock, index)
			if mode == "both-partial" || mode == "both-managed-retired" || mode == "both-second-epoch-drift" || mode == "both-second-token-drift" {
				other := controllerReacquireFrames(t, original, clock, 1)
				frames[1] = other[1]
				// The successful fresh transaction has the same managed set in every
				// scan. A separate negative covers retirement/change of protected rows.
				for i := 2; mode != "both-managed-retired" && i < 4; i++ {
					f := &frames[i]
					f.Errors = []procobserver.EvidenceError{}
					f.ErrorsTotal = 0
					f.Census.Proofs = []procobserver.CensusProofV3{}
					f.Census.Certificates = []procobserver.DescendantCertificate{}
					f.Census.Resolutions = []procobserver.CensusResolution{}
					for j := range f.Census.Scans {
						scan := &f.Census.Scans[j]
						scan.Verified = scan.Verified[:1]
						scan.LiveCount = 1
						scan.LiveDigest = f.Census.ReconciledDigest
					}
				}
			}
			if mode == "permission" {
				frames[0].Errors[0].Errno = 13
			}
			if mode == "nonce-reused" {
				frames[2].RequestNonce = frames[0].RequestNonce
			}
			if mode == "root-replaced" {
				for i := 2; i < 4; i++ {
					f := &frames[i]
					f.Roots[0].PID, f.Roots[0].PGID, f.Roots[0].StartTicks = 21, 21, "201"
					mutate := func(v *procobserver.CensusIdentity) {
						if v.PID == 20 {
							v.PID, v.PGID, v.StartTicks = 21, 21, "201"
						}
						if v.PPID == 20 {
							v.PPID = 21
						}
					}
					for j := range f.Census.Scans {
						for k := range f.Census.Scans[j].Verified {
							mutate(&f.Census.Scans[j].Verified[k])
						}
					}
					for j := range f.Census.Certificates {
						for k := range f.Census.Certificates[j].Chain {
							mutate(&f.Census.Certificates[j].Chain[k])
						}
					}
				}
				reads := 2
				independent := collectControllerObservationV3(context.Background(), "/city", p, cs.sp, func(context.Context, procobserver.ReleasePolicyV3) (procobserver.ResponseV3, error) {
					f := frames[reads]
					reads++
					return f, nil
				}, func() time.Time { return clock })
				if !independent.ProcessComplete || validateControllerObservationReplyV3(independent, p, "/city", p.HelperSourceRevision, clock) != nil {
					t.Fatal("root replacement fixture is not independently complete")
				}
			}
			if mode == "second-incomplete" {
				other := controllerReacquireFrames(t, original, clock, 1)
				frames[3] = other[1]
				frames[3].RequestNonce = strings.Repeat("d", 64)
				frames[3].StartedAt, frames[3].FinishedAt = clock.Add(-3*time.Millisecond), clock
			}
			if mode == "tracker-failed" {
				cs.sp.(*controllerRetryProvider).trackingErr = errors.New("fixture tracker denied")
			}
			s.reacquireEnabled = mode != "default-single"
			calls, retained, recorded, transactions := 0, 0, 0, 0
			store, err := observationattempt.NewStore(filepath.Join(t.TempDir(), "evidence"), controllerObservationV3Limit)
			if err != nil {
				t.Fatal(err)
			}
			var retainedReceipt observationattempt.Receipt
			s.readEvidence = func(context.Context, procobserver.ReleasePolicyV3) (procobserver.ResponseV3, error) {
				if calls >= len(frames) {
					t.Fatal("unexpected extra helper exchange")
				}
				f := frames[calls]
				calls++
				if calls == 4 && mode == "both-second-epoch-drift" {
					_ = cs.sp.SetMeta("sid", "GC_RUNTIME_EPOCH", "2")
				}
				if calls == 4 && mode == "both-second-token-drift" {
					_ = cs.sp.SetMeta("sid", "GC_INSTANCE_TOKEN", "new-provider-incarnation")
				}
				return f, nil
			}
			collect := s.collect
			s.collect = func(ctx context.Context, p procobserver.ReleasePolicyV3, sp runtime.Provider) controllerObservationReplyV3 {
				transactions++
				if transactions == 2 {
					current = clock
				}
				if transactions == 2 && mode == "provider-changed" {
					if err := sp.SetMeta("sid", "GC_INSTANCE_TOKEN", "successor-token"); err != nil {
						t.Fatal(err)
					}
				}
				return collect(ctx, p, sp)
			}
			s.retainAttempt = func(ctx context.Context, r controllerObservationReplyV3) (observationattempt.Receipt, error) {
				retained++
				if mode == "durable-readback" {
					retainedReceipt, err = store.Save(ctx, r)
					return retainedReceipt, err
				}
				if mode == "retention-failed" {
					return observationattempt.Receipt{}, errors.New("fixture private store full")
				}
				if mode == "caller-changed" {
					s.checkCaller = func(procobserver.Policy) error { return errors.New("fixture caller changed") }
				}
				if mode == "policy-changed" {
					s.loadPolicy = func() (procobserver.ReleasePolicyV3, error) {
						q := p
						q.PolicyDigest = strings.Repeat("0", 64)
						return q, nil
					}
				}
				if mode == "generation-changed" {
					cs.mu.Lock()
					cs.observationGeneration++
					cs.mu.Unlock()
				}
				return observationattempt.Receipt{Path: "/private/fixture", SHA256: strings.Repeat("c", 64), Bytes: 42, Complete: true}, nil
			}
			s.recordAttempt = func(ctx context.Context, failed observationattempt.Receipt, first, selected observationattempt.Identity, r controllerObservationReplyV3) (observationattempt.Receipt, error) {
				recorded++
				if mode == "durable-readback" {
					return store.SaveCandidate(ctx, failed, first, selected, wholeAttemptDigest(r))
				}
				if mode == "record-failed" {
					return observationattempt.Receipt{}, errors.New("fixture record unavailable")
				}
				if failed.Path != "/private/fixture" || first.RootFingerprint != selected.RootFingerprint || len(r.ProcessDiagnostics) != 2 || !r.ProcessComplete {
					t.Fatal("selection lost its failed receipt or exact final transaction")
				}
				return observationattempt.Receipt{Path: "/private/fixture.result", SHA256: strings.Repeat("f", 64), Bytes: 42, Complete: true}, nil
			}
			got := s.observe(context.Background())
			controllerV3ServiceIdle(t)
			wantComplete := mode == "complete-first" || mode == "partial-first" || mode == "partial-second" || mode == "both-partial" || mode == "durable-readback"
			if (got.ProviderComplete && got.ProcessComplete) != wantComplete {
				t.Fatalf("mode=%s transactions=%d reads=%d retained=%d causes=%v reasons=%v", mode, transactions, calls, retained, got.FailureKinds, got.UnknownReasons)
			}
			wantTransactions := 1
			if mode == "partial-first" || mode == "partial-second" || mode == "both-partial" || mode == "both-managed-retired" || mode == "both-second-epoch-drift" || mode == "both-second-token-drift" || mode == "durable-readback" || mode == "provider-changed" || mode == "root-replaced" || mode == "record-failed" || mode == "nonce-reused" || mode == "second-incomplete" {
				wantTransactions = 2
			}
			if transactions != wantTransactions || calls != wantTransactions*2 {
				t.Fatalf("transactions=%d reads=%d want=%d whole transactions", transactions, calls, wantTransactions)
			}
			if wantComplete && mode != "complete-first" {
				if retained != 1 || recorded != 1 || !reflect.DeepEqual(got.ProcessDiagnostics, frames[2:]) {
					t.Fatal("selected result mixed attempts or failed to retain first")
				}
			}
			if !wantComplete && got.CertificateDisposition != "provisional" {
				t.Fatal("partial attempt promoted")
			}
			if mode == "root-replaced" && !strings.Contains(strings.Join(got.UnknownReasons, ";"), "attempt_identity_changed") {
				t.Fatal("root replacement did not reach the cross-attempt fence", got.UnknownReasons)
			}
			if mode == "durable-readback" {
				b, err := os.ReadFile(retainedReceipt.Path + ".candidate.json")
				if err != nil {
					t.Fatal(err)
				}
				var readback observationattempt.CandidateRecord
				if err = json.Unmarshal(b, &readback); err != nil || readback.CandidateAttempt != 2 || readback.FailedAttempt != retainedReceipt || readback.CandidateTypedEnvelopeSHA256 != wholeAttemptDigest(got) {
					t.Fatal("real service success lost durable failed/selected-envelope link")
				}
			}
		})
	}
}

// PID ownership is supplied independently of helper identities. Unlike the
// older branch-mechanics fake, matching SID metadata never certifies a PID.
type controllerPartialOwnershipProvider struct {
	*controllerRetryProvider
	handles        []runtime.ProcessHandle
	nonempty       int
	mode           string
	afterFinalList func()
}

func (p *controllerPartialOwnershipProvider) TrackProcessRoots(in []runtime.LiveRuntime) ([]runtime.LiveRuntime, error) {
	if len(in) > 0 {
		p.nonempty++
		if p.mode == "tracker-error" {
			return nil, errors.New("fixture tracker failed")
		}
		if p.mode == "second-pid-drift" && p.nonempty == 2 {
			p.handles[0].PID++
		}
		if p.mode == "metadata-drift" && p.nonempty == 2 {
			_ = p.SetMeta("sid", "GC_RUNTIME_EPOCH", "2")
		}
		if p.mode == "input-mutation" {
			in[0].PPID++
		}
	}
	return runtime.BindProcessEvidence(in, p.handles)
}

func (p *controllerPartialOwnershipProvider) ListRunning(prefix string) ([]string, error) {
	names, err := p.controllerRetryProvider.ListRunning(prefix)
	if p.nonempty == 2 && p.afterFinalList != nil {
		p.afterFinalList()
	}
	if p.mode == "extra-handle" && p.nonempty >= 2 {
		return append(names, "new-unmatched-provider-handle"), err
	}
	return names, err
}

func TestControllerV3BothPartialRequiresActualPIDOwnership(t *testing.T) {
	for _, mode := range []string{"exact", "sid-only", "missing-handle", "duplicate", "second-pid-drift", "tracker-error", "metadata-drift", "extra-handle", "input-mutation", "wire-only"} {
		t.Run(mode, func(t *testing.T) {
			policy, original, now := controllerV3Fixture()
			f := controllerReacquireFrames(t, original, now, 0)
			other := controllerReacquireFrames(t, original, now, 1)
			f[1] = other[1]
			p := &controllerPartialOwnershipProvider{controllerRetryProvider: controllerV3Provider(t), handles: []runtime.ProcessHandle{{Name: "sid", SessionID: "sid", PID: 20}}, mode: mode}
			switch mode {
			case "sid-only":
				p.handles[0].PID = 21
			case "missing-handle":
				p.handles = nil
			case "duplicate":
				p.handles = append(p.handles, p.handles[0])
			}
			calls := 0
			got := collectControllerObservationV3ForReacquisition(context.Background(), "/city", policy, p, func(context.Context, procobserver.ReleasePolicyV3) (procobserver.ResponseV3, error) {
				v := f[calls]
				calls++
				return v, nil
			}, func() time.Time { return now }, true)
			if mode == "wire-only" {
				got.reacquisitionJoins = nil
			}
			if controllerV3TransientWhole(got, policy, func() time.Time { return now }) != (mode == "exact") {
				t.Fatal("incorrect partial ownership eligibility", got.UnknownReasons)
			}
			if got.ProcessComplete || got.CertificateDisposition != "provisional" || len(got.Processes) != 0 || !reflect.DeepEqual(got.ProcessDiagnostics, f[:2]) {
				t.Fatal("provisional proof altered original wire/projection")
			}
			b, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			var wire controllerObservationReplyV3
			if err = json.Unmarshal(b, &wire); err != nil || len(wire.reacquisitionJoins) != 0 {
				t.Fatal("internal proof leaked to wire")
			}
		})
	}
}

func TestControllerV3BothPartialIntervalIncludesFinalProviderFence(t *testing.T) {
	policy, original, now := controllerV3Fixture()
	f := controllerReacquireFrames(t, original, now, 0)
	other := controllerReacquireFrames(t, original, now, 1)
	f[1] = other[1]
	current := now
	p := &controllerPartialOwnershipProvider{controllerRetryProvider: controllerV3Provider(t), handles: []runtime.ProcessHandle{{Name: "sid", SessionID: "sid", PID: 20}}}
	calls := 0
	p.afterFinalList = func() { current = now.Add(time.Millisecond) }
	got := collectControllerObservationV3ForReacquisition(context.Background(), "/city", policy, p, func(context.Context, procobserver.ReleasePolicyV3) (procobserver.ResponseV3, error) {
		v := f[calls]
		calls++
		return v, nil
	}, func() time.Time { return current }, true)
	if !got.FinishedAt.Equal(current) || got.FinishedAt.Before(now.Add(time.Millisecond)) {
		t.Fatal("whole interval omits final ownership fence")
	}
	if !reflect.DeepEqual(got.ProcessDiagnostics, f[:2]) {
		t.Fatal("helper intervals altered")
	}
}

func TestControllerV3BothPartialRetentionIncludesEligibilityAudit(t *testing.T) {
	policy, original, now := controllerV3Fixture()
	frames := controllerReacquireFrames(t, original, now, 0)
	other := controllerReacquireFrames(t, original, now, 1)
	frames[1] = other[1]
	provider := &controllerPartialOwnershipProvider{controllerRetryProvider: controllerV3Provider(t), handles: []runtime.ProcessHandle{{Name: "sid", SessionID: "sid", PID: 20}}}
	calls := 0
	r := collectControllerObservationV3ForReacquisition(context.Background(), "/city", policy, provider, func(context.Context, procobserver.ReleasePolicyV3) (procobserver.ResponseV3, error) {
		f := frames[calls]
		calls++
		return f, nil
	}, func() time.Time { return now }, true)
	if !controllerV3TransientWhole(r, policy, func() time.Time { return now }) {
		t.Fatal("audit fixture is not eligible")
	}
	service := newControllerObservationServiceV3(context.Background(), t.TempDir())
	if err := os.MkdirAll(filepath.Join(service.city, ".gc", "runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	receipt, err := service.retainAttempt(context.Background(), r)
	if err != nil || !receipt.Complete {
		t.Fatal("failed retention", err)
	}
	b, err := os.ReadFile(receipt.Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "ParentIsProviderInfrastructure") {
		t.Fatal("audit guessed absent parent metadata")
	}
	var bundle struct {
		Observation json.RawMessage                `json:"collected_typed_envelope"`
		Joins       []controllerV3CaptureJoinAudit `json:"eligibility_provider_joins"`
	}
	if err = json.Unmarshal(b, &bundle); err != nil {
		t.Fatal(err)
	}
	originalWire, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(bundle.Observation) != string(originalWire) || len(bundle.Joins) != 2 {
		t.Fatal("bundle altered original failed wire or lost eligibility proof")
	}
	for i, j := range bundle.Joins {
		if j.Classification != "eligibility_only_not_complete" || j.RequestNonce != frames[i].RequestNonce || len(j.ManagedIdentities) != 1 || len(j.TrackedRoots) != 1 || j.TrackedRoots[0].PID != 20 || j.TrackedRoots[0].ProviderName != "sid" {
			t.Fatal("incorrect audit provenance")
		}
	}
}
