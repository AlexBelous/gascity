//go:build linux

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/storebinding"
)

const (
	remoteWorkRequestMetadata  = "gc.remote_work_request"
	remoteWorkDigestMetadata   = "gc.remote_work_digest"
	remoteWorkResultMetadata   = "gc.remote_work_result"
	remoteWorkEvidenceMetadata = "gc.remote_work_evidence_sha"
)

type remoteWorkNative struct {
	policy       remoteWorkPolicy
	profile      remoteWorkProfile
	frame        remoteWorkFrame
	cfg          *config.City
	front        *sessionpkg.Store
	assignee     string
	lease        func(context.Context, string, func() error) error
	resolveStore func(string) (beads.Store, error)
	claim        func() ([]byte, int)
	drain        func() error
}

func newRemoteWorkNative(p remoteWorkPolicy, profile remoteWorkProfile, f remoteWorkFrame) (*remoteWorkNative, error) {
	cfg, e := loadCityConfig(profile.CityPath, io.Discard)
	if e != nil {
		return nil, errRemoteWorkDenied
	}
	resolveRigPaths(profile.CityPath, cfg.Rigs)
	work, e := openCityStoreAtWithConfig(profile.CityPath, cfg)
	if e != nil {
		return nil, errRemoteWorkDenied
	}
	n := &remoteWorkNative{policy: p, profile: profile, frame: f, cfg: cfg, front: cliSessionFrontDoor(work, cfg, profile.CityPath)}
	info, _, e := n.front.GetPersistedResponse(f.Identity.SID)
	if e != nil {
		return nil, errRemoteWorkDenied
	}
	n.assignee = sessionpkg.AssigneeIdentifier(info)
	n.lease = n.withEditorLease
	if n.Validate(context.Background()) != nil {
		return nil, errRemoteWorkDenied
	}
	return n, nil
}

func (n *remoteWorkNative) Validate(ctx context.Context) error {
	if ctx.Err() != nil || n.front == nil || n.assignee == "" {
		return errRemoteWorkDenied
	}
	if _, e := remoteWorkSelectProfile(n.policy, n.frame); e != nil {
		return e
	}
	i, p, e := n.front.GetPersistedResponse(n.frame.Identity.SID)
	id := n.frame.Identity
	if e != nil || i.ID != id.SID || i.Template != id.Template || i.SessionNameMetadata != id.Handle || i.Generation != id.Generation || i.InstanceToken != id.InstanceToken || i.Closed || p.Status == "closed" || i.ContinuationResetPending != "" {
		return errRemoteWorkDenied
	}
	switch i.MetadataState {
	case "active", "awake", "creating", "start-pending":
	default:
		return errRemoteWorkDenied
	}
	binding, found, e := n.front.RuntimeBinding(id.SID)
	if e != nil || !found || binding.Selector != n.profile.RuntimeProvider || binding.Endpoint != n.profile.RuntimeTarget || binding.ResourceDomain != n.profile.ResourceDomain || binding.PolicyDigest != n.profile.PolicyDigest {
		return errRemoteWorkDenied
	}
	if sessionpkg.AssigneeIdentifier(i) != n.assignee {
		return errRemoteWorkDenied
	}
	return nil
}

func (n *remoteWorkNative) storeFor(id string) (beads.Store, error) {
	if n.resolveStore != nil {
		return n.resolveStore(id)
	}
	target, e := resolveBdScopeTarget(n.cfg, n.profile.CityPath, "", []string{"show", id}, false, io.Discard)
	if e != nil {
		return nil, errRemoteWorkDenied
	}
	work, e := openStoreAtForCityWithConfig(target.ScopeRoot, n.profile.CityPath, n.cfg)
	if e != nil {
		return nil, errRemoteWorkDenied
	}
	return classRoutedStoreForID(n.profile.CityPath, id, work)
}

func (n *remoteWorkNative) owned(id string, allowClosed bool) (beads.Store, beads.Bead, error) {
	if !remoteWorkID.MatchString(id) {
		return nil, beads.Bead{}, errRemoteWorkDenied
	}
	current, e := n.front.CurrentClaimBeadID(n.frame.Identity.SID)
	if e != nil || current != id {
		return nil, beads.Bead{}, errRemoteWorkDenied
	}
	store, e := n.storeFor(id)
	if e != nil {
		return nil, beads.Bead{}, errRemoteWorkDenied
	}
	b, e := store.Get(id)
	if e != nil || b.ID != id || b.Assignee != n.assignee || (b.Status != "in_progress" && (!allowClosed || b.Status != "closed")) || b.Metadata["gc.routed_to"] != n.profile.Template || b.Metadata["gc.session_id"] != n.frame.Identity.SID {
		return nil, b, errRemoteWorkDenied
	}
	return store, b, nil
}

func (n *remoteWorkNative) Execute(ctx context.Context, r remoteWorkRequest) (remoteWorkResponse, error) {
	if validateRemoteWorkRequest(r) != nil {
		return remoteWorkResponse{}, errRemoteWorkDenied
	}
	if n.Validate(ctx) != nil {
		return remoteWorkResponse{}, errRemoteWorkDenied
	}
	out := remoteWorkResponse{SchemaVersion: 1, RequestID: r.RequestID}
	switch r.Op {
	case "claim":
		var stdout, stderr bytes.Buffer
		var raw []byte
		var code int
		if n.claim != nil {
			raw, code = n.claim()
		} else {
			code = cmdHookWithOptions(nil, hookCommandOptions{Claim: true, JSON: true}, &stdout, &stderr)
			raw = stdout.Bytes()
		}
		got, e := remoteWorkClaimResponse(raw, code)
		if e != nil {
			return out, e
		}
		got.RequestID = r.RequestID
		if got.State == "idle" {
			return got, nil
		}
		_, b, e := n.owned(got.BeadID, false)
		if e != nil {
			return out, e
		}
		got.Bead = remoteWorkPublicBead(b)
		return got, nil
	case "current":
		id, e := n.front.CurrentClaimBeadID(n.frame.Identity.SID)
		if e != nil || id == "" {
			return out, errRemoteWorkDenied
		}
		_, b, e := n.owned(id, false)
		if e != nil {
			return out, e
		}
		out.State = "claimed"
		out.BeadID = id
		out.Bead = remoteWorkPublicBead(b)
		return out, nil
	case "read":
		_, b, e := n.owned(r.BeadID, false)
		if e != nil {
			return out, e
		}
		out.State = "read"
		out.BeadID = b.ID
		out.Bead = remoteWorkPublicBead(b)
		return out, nil
	case "result", "close":
		e := n.lease(ctx, r.BeadID, func() error {
			if n.Validate(ctx) != nil {
				return errRemoteWorkDenied
			}
			s, b, e := n.owned(r.BeadID, false)
			if e != nil || b.Revision == 0 {
				return errRemoteWorkDenied
			}
			if r.Op == "close" && (b.Metadata[remoteWorkResultMetadata] == "" || b.Metadata[remoteWorkEvidenceMetadata] != r.EvidenceSHA) {
				return errRemoteWorkDenied
			}
			if r.Op == "close" && evaluateWorkRecordCloseGate([]string{"close", b.ID}, s, map[string]beads.Bead{b.ID: b}, classDoorRepoDirs(n.profile.CityPath), true, io.Discard) {
				return errRemoteWorkDenied
			}
			graph, e := storebinding.NewBeadsGraphStore(s)
			if e != nil {
				return errRemoteWorkDenied
			}
			meta := map[string]string{remoteWorkRequestMetadata: r.RequestID, remoteWorkDigestMetadata: remoteWorkRequestDigest(r), remoteWorkEvidenceMetadata: r.EvidenceSHA}
			if r.Op == "result" {
				meta[remoteWorkResultMetadata] = r.Text
				meta["gc.work_outcome"] = r.Outcome
			} else {
				meta["gc.remote_work_close_reason"] = r.Text
			}
			guard := &remoteWorkOwnedGraph{GraphStore: graph, store: s, revision: b.Revision, metadata: meta}
			var stdout, stderr bytes.Buffer
			var code int
			if r.Op == "result" {
				code = doBdByIDUpdate(guard, bdByIDOp{ID: b.ID, JSON: true, Update: beads.UpdateOpts{Metadata: meta}}, "verified remote work", &stdout, &stderr)
			} else {
				code = doBdByIDClose(guard, bdByIDOp{ID: b.ID, JSON: true}, "verified remote work", &stdout, &stderr)
			}
			if code != 0 {
				return errRemoteWorkDenied
			}
			actual, e := s.Get(b.ID)
			if e != nil || actual.Metadata[remoteWorkRequestMetadata] != r.RequestID || actual.Metadata[remoteWorkDigestMetadata] != remoteWorkRequestDigest(r) {
				return errRemoteWorkDenied
			}
			out.State = "recorded"
			if r.Op == "close" {
				if actual.Status != "closed" {
					return errRemoteWorkDenied
				}
				out.State = "closed"
			}
			out.BeadID = b.ID
			return nil
		})
		return out, e
	case "self_drain":
		// Native acknowledgement can release work. Never hand it an unresolved
		// in-progress action or infer safe completion from an empty SSH response.
		id, e := n.front.CurrentClaimBeadID(n.frame.Identity.SID)
		if e != nil {
			return out, errRemoteWorkDenied
		}
		if id != "" {
			_, b, e := n.owned(id, true)
			if e != nil || b.Status != "closed" {
				return out, errRemoteWorkDenied
			}
		}
		var stdout, stderr bytes.Buffer
		if n.drain != nil {
			if n.drain() != nil {
				return out, errRemoteWorkDenied
			}
		} else if code := cmdRuntimeDrainAck(nil, true, &stdout, &stderr); code != 0 {
			return out, errRemoteWorkDenied
		}
		out.State = "drain_acknowledged"
		return out, nil
	}
	return out, errRemoteWorkDenied
}

func (n *remoteWorkNative) Reconcile(ctx context.Context, r remoteWorkRequest) (remoteWorkResponse, error) {
	if n.Validate(ctx) != nil {
		return remoteWorkResponse{}, errRemoteWorkDenied
	}
	out := remoteWorkResponse{SchemaVersion: 1, RequestID: r.RequestID, BeadID: r.BeadID}
	switch r.Op {
	case "read", "current":
		return n.Execute(ctx, r)
	case "result", "close":
		_, b, e := n.owned(r.BeadID, true)
		if e != nil || b.Metadata[remoteWorkRequestMetadata] != r.RequestID || b.Metadata[remoteWorkDigestMetadata] != remoteWorkRequestDigest(r) {
			return out, errRemoteWorkDenied
		}
		out.State = "recorded"
		if r.Op == "close" {
			if b.Status != "closed" {
				return out, errRemoteWorkDenied
			}
			out.State = "closed"
		}
		return out, nil
	// An uncertain claim cannot safely select another bead, and an uncertain
	// drain cannot be repeated. Root intake must resolve them against its own
	// durable binding/observation rather than infer ownership from stale ENV.
	default:
		return out, errRemoteWorkDenied
	}
}

// The existing by-ID handlers retain their write/readback behavior. This
// privately constructed wrapper makes their single write revision-fenced and
// makes close+receipt atomic; a backend lacking the capability refuses.
type remoteWorkOwnedGraph struct {
	storebinding.GraphStore
	store    beads.Store
	revision int64
	metadata map[string]string
}

func (g *remoteWorkOwnedGraph) Update(id string, o beads.UpdateOpts) error {
	w, ok := beads.ConditionalWriterFor(g.store)
	if !ok || g.revision == 0 {
		return errRemoteWorkDenied
	}
	return w.UpdateIfMatch(id, g.revision, o)
}

func (g *remoteWorkOwnedGraph) Close(id string) error {
	w, ok := beads.AtomicConditionalCloserFor(g.store)
	if !ok || g.revision == 0 {
		return errRemoteWorkDenied
	}
	_, e := w.CloseWithMetadataIfMatch(id, g.revision, g.metadata)
	return e
}

func (n *remoteWorkNative) withEditorLease(ctx context.Context, id string, write func() error) error {
	code, e := remoteWorkRootFile(n.policy.LeaseCommand, 2<<20)
	if e != nil {
		return errRemoteWorkDenied
	}
	h := sha256.Sum256(code)
	if hex.EncodeToString(h[:]) != n.policy.LeaseSHA {
		return errRemoteWorkDenied
	}
	// This identity is derived solely from the verified private root proof. The
	// remote model cannot set the legacy diagnostic override or release another
	// session's leases.
	principal := "remote/" + n.frame.Identity.SID + "/" + n.frame.Identity.Generation + "/" + n.frame.Identity.Attempt
	run := func(args ...string) ([]byte, error) {
		c := exec.CommandContext(ctx, n.policy.LeaseCommand, append([]string{"--session-id", principal}, args...)...)
		c.Env = os.Environ()
		var out, errout bytes.Buffer
		c.Stdout = &remoteWorkBoundedWriter{buf: &out, limit: remoteWorkFrameLimit}
		c.Stderr = &remoteWorkBoundedWriter{buf: &errout, limit: 4096}
		if e := c.Run(); e != nil {
			return nil, errRemoteWorkDenied
		}
		return out.Bytes(), nil
	}
	raw, e := run("claim", id, "--reason", "verified remote work mutation", "--hold", "60s", "--nowait")
	if e != nil {
		return e
	}
	// Claim's holder_token is private and deliberately never emitted or logged.
	var claim struct {
		Bd        string    `json:"bd"`
		LeaseID   string    `json:"lease_id"`
		HoldUntil time.Time `json:"hold_until"`
	}
	if json.Unmarshal(raw, &claim) != nil || claim.Bd != id || claim.LeaseID == "" || time.Until(claim.HoldUntil) < 10*time.Second {
		return errRemoteWorkDenied
	}
	released := false
	defer func() {
		if !released {
			_, _ = run("release", id)
		}
	}()
	raw, e = run("status")
	if e != nil {
		return e
	}
	var status struct {
		Leases []struct {
			Bd      string `json:"bd"`
			Session string `json:"session_id"`
			LeaseID string `json:"lease_id"`
			Stale   bool   `json:"stale"`
		} `json:"leases"`
	}
	if json.Unmarshal(raw, &status) != nil {
		return errRemoteWorkDenied
	}
	found := false
	for _, l := range status.Leases {
		if l.Bd == id {
			if found || l.Session != principal || l.LeaseID != claim.LeaseID || l.Stale {
				return errRemoteWorkDenied
			}
			found = true
		}
	}
	if !found {
		return errRemoteWorkDenied
	}
	writeErr := write()
	raw, releaseErr := run("release", id)
	if releaseErr != nil {
		return errRemoteWorkDenied
	}
	var releasedReceipt struct {
		OK bool `json:"ok"`
	}
	if json.Unmarshal(raw, &releasedReceipt) != nil || !releasedReceipt.OK {
		return errRemoteWorkDenied
	}
	released = true
	if writeErr != nil {
		return writeErr
	}
	return nil
}

type remoteWorkBoundedWriter struct {
	buf   *bytes.Buffer
	limit int
}

func (w *remoteWorkBoundedWriter) Write(p []byte) (int, error) {
	if len(p) > w.limit-w.buf.Len() {
		return 0, errRemoteWorkDenied
	}
	return w.buf.Write(p)
}
