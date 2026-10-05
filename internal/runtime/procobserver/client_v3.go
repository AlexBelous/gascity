package procobserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/runtime/proctable"
)

// LoadPolicyV3 reads a separate, exact V3 selector. It never refreshes a caller
// binding, accepts a legacy selector, or enables a controller route.
func LoadPolicyV3(path, source string) (ReleasePolicyV3, error) {
	if err := trustedPath(path, false); err != nil {
		return ReleasePolicyV3{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return ReleasePolicyV3{}, fmt.Errorf("observer v3 policy unavailable")
	}
	defer f.Close() //nolint:errcheck // read-only policy
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(data) > 4096 {
		return ReleasePolicyV3{}, fmt.Errorf("observer v3 policy exceeds bound")
	}
	return decodePolicyV3(data, source)
}

func validatePolicyV3(p ReleasePolicyV3) error {
	if p.EvidenceSchema != ResponseSchemaV3 || validatePolicy(p.Policy) != nil {
		return fmt.Errorf("observer v3 policy contract invalid")
	}
	return nil
}

func decodePolicyV3(data []byte, source string) (ReleasePolicyV3, error) {
	var p ReleasePolicyV3
	if len(data) > 4096 || strictJSON(data, &p) != nil || validatePolicyV3(p) != nil || !isHex(source, 40) || p.HelperSourceRevision != source || p.CallerBinding.ControllerSourceRevision != source {
		return ReleasePolicyV3{}, fmt.Errorf("observer v3 policy source or contract invalid")
	}
	return p, nil
}

// ReadV3 requests one bounded V3 helper frame. Legacy Read remains unchanged.
func ReadV3(p ReleasePolicyV3) (ResponseV3, error) { return ReadContextV3(context.Background(), p) }

// ReadContextV3 uses the same exact caller, activation peer and per-message
// writer credentials as V2. No fallback or helper lifecycle operation exists.
func ReadContextV3(ctx context.Context, p ReleasePolicyV3) (ResponseV3, error) {
	if ctx.Err() != nil {
		return ResponseV3{}, fmt.Errorf("observer request canceled")
	}
	if err := validatePolicyV3(p); err != nil {
		return ResponseV3{}, err
	}
	if err := CheckCaller(p.Policy); err != nil {
		return ResponseV3{}, err
	}
	if err := trustedPath(p.SocketPath, true); err != nil {
		return ResponseV3{}, err
	}
	dialer := net.Dialer{Timeout: Timeout}
	conn, err := dialer.DialContext(ctx, "unix", p.SocketPath)
	if err != nil {
		return ResponseV3{}, fmt.Errorf("observer socket unavailable")
	}
	defer conn.Close() //nolint:errcheck // bounded socket cleanup
	c, ok := conn.(*net.UnixConn)
	if !ok {
		return ResponseV3{}, fmt.Errorf("observer transport unsupported")
	}
	if verifyActivationPeer(c) != nil {
		return ResponseV3{}, fmt.Errorf("observer root activation peer mismatch")
	}
	read, err := newWriterAuthenticatedReader(c, p.HelperUID)
	if err != nil {
		return ResponseV3{}, err
	}
	return exchangeContextV3WithReader(ctx, c, p, time.Now, read)
}

func exchangeContextV3WithReader(ctx context.Context, c *net.UnixConn, p ReleasePolicyV3, now func() time.Time, read func([]byte) (int, error)) (ResponseV3, error) {
	if err := validatePolicyV3(p); err != nil {
		return ResponseV3{}, err
	}
	if ctx.Err() != nil {
		return ResponseV3{}, fmt.Errorf("observer request canceled")
	}
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	deadline := time.Now().Add(Timeout)
	if bounded, ok := ctx.Deadline(); ok && bounded.Before(deadline) {
		deadline = bounded
	}
	if c.SetDeadline(deadline) != nil {
		return ResponseV3{}, fmt.Errorf("observer deadline unavailable")
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return ResponseV3{}, fmt.Errorf("observer nonce unavailable")
	}
	request := Request{Schema: RequestSchemaV3, RequestNonce: hex.EncodeToString(nonce)}
	data, err := json.Marshal(request)
	if err != nil || len(data) > MaxRequestBytes {
		return ResponseV3{}, fmt.Errorf("observer request unavailable")
	}
	if writeFrame(c, data) != nil {
		return ResponseV3{}, fmt.Errorf("observer request write failed")
	}
	if c.CloseWrite() != nil {
		return ResponseV3{}, fmt.Errorf("observer request boundary failed")
	}
	data, err = readFrameWithReader(read, MaxResponseBytes)
	if err != nil {
		return ResponseV3{}, err
	}
	var extra [1]byte
	if n, e := read(extra[:]); n != 0 || !errors.Is(e, io.EOF) {
		return ResponseV3{}, fmt.Errorf("observer response has extra data or no close")
	}
	return DecodeResponseV3(data, p, request.RequestNonce, now().UTC())
}

// DecodeResponseV3 enforces the separate typed envelope and fixed release pins.
// A structurally authenticated but incomplete census remains inspectable with
// an error; it never becomes provider authority or a legacy response.
func DecodeResponseV3(data []byte, p ReleasePolicyV3, nonce string, now time.Time) (ResponseV3, error) {
	var r ResponseV3
	if validatePolicyV3(p) != nil || len(data) > MaxResponseBytes || strictJSON(data, &r) != nil {
		return ResponseV3{}, fmt.Errorf("observer v3 response contract invalid")
	}
	if r.Schema != ResponseSchemaV3 || r.Scope != "host_procfs" || !isHex(nonce, 64) || r.RequestNonce != nonce ||
		r.HelperSourceRevision != p.HelperSourceRevision || r.HelperBinarySHA256 != p.HelperBinarySHA256 || r.PolicyDigest != p.PolicyDigest ||
		r.BootID != p.BootID || r.PIDNamespaceIdentity != p.PIDNamespaceIdentity || r.CallerBinding != p.CallerBinding || r.CertificateDisposition != "provisional" {
		return ResponseV3{}, fmt.Errorf("observer v3 response binding mismatch")
	}
	if r.StartedAt.IsZero() || r.FinishedAt.IsZero() || r.StartedAt.After(r.FinishedAt) || r.FinishedAt.After(now) || now.Sub(r.StartedAt) > 60*time.Second ||
		r.DurationMS < 0 || r.DurationMS >= Timeout.Milliseconds() || r.FinishedAt.Sub(r.StartedAt) >= Timeout {
		return ResponseV3{}, fmt.Errorf("observer v3 response interval invalid or stale")
	}
	if r.Roots == nil || r.Errors == nil || r.EnumeratedCountBefore < 1 || r.EnumeratedCountBefore > MaxProcesses || r.EnumeratedCountAfter < 1 || r.EnumeratedCountAfter > MaxProcesses ||
		!isHex(r.EnumerationDigestBefore, 64) || !isHex(r.EnumerationDigestAfter, 64) || len(r.Roots) > MaxProcesses || len(r.Errors) > 128 || r.ErrorsTotal < len(r.Errors) {
		return ResponseV3{}, fmt.Errorf("observer v3 coverage fields invalid")
	}
	last := 0
	for _, v := range r.Roots {
		if v.PID <= 1 || v.PID <= last || v.PID > 2147483647 || v.PPID < 0 || v.PPID > 2147483647 || v.PGID < 1 || v.PGID > 2147483647 ||
			!positiveNumber(v.StartTicks) || !safeIdentity(v.SessionID) || !safeIdentity(v.Template) || !filepath.IsAbs(v.City) || filepath.Clean(v.City) != v.City || !safeIdentity(v.City) ||
			v.Epoch < 1 || !isHex(v.InstanceTokenSHA256, 64) || len(v.Name) > 256 || len(v.ParentName) > 256 || v.ParentIsProviderInfrastructure != infrastructureName(v.ParentName) {
			return ResponseV3{}, fmt.Errorf("observer v3 root identity invalid")
		}
		last = v.PID
	}
	for _, e := range r.Errors {
		if len(e.Reason) < 1 || len(e.Reason) > 64 || len(e.Operation) < 1 || len(e.Operation) > 32 || e.PID < 0 || e.PID > 2147483647 || e.Errno < 0 || e.Errno > 4095 {
			return ResponseV3{}, fmt.Errorf("observer v3 diagnostic invalid")
		}
	}
	if !r.Complete || r.KernelProofProfile != KernelProofProfile || !strings.HasPrefix(r.KernelRelease, "6.8.") ||
		ValidateCensusV3(r.Census, r.Errors, r.ErrorsTotal, r.ErrorsTruncated, r.DurationMS, p.CallerBinding.PID) != nil {
		return r, fmt.Errorf("observer v3 process coverage incomplete")
	}
	initial := r.Census.Scans[0]
	closing := r.Census.Scans[r.Census.SelectedClosings[1]-1]
	if initial.EnumeratedCount != r.EnumeratedCountBefore || initial.EnumerationDigest != r.EnumerationDigestBefore ||
		closing.EnumeratedCount != r.EnumeratedCountAfter || closing.EnumerationDigest != r.EnumerationDigestAfter ||
		len(r.Roots) > r.Census.ReconciledCount || len(r.Roots) > r.Census.Seal.ClassifiedCount {
		return ResponseV3{}, fmt.Errorf("observer v3 census envelope binding mismatch")
	}
	seal := r.Census.Scans[len(r.Census.Scans)-1]
	declared := make(map[int]CensusIdentity)
	for _, v := range seal.Verified {
		if v.Classification == "managed" && v.DeclaredRoot {
			declared[v.PID] = v
		}
	}
	// The producer's fixed projection contains EVERY sealed declared root;
	// neither omission nor a non-root managed descendant may hide here.
	if len(r.Roots) != len(declared) {
		return ResponseV3{}, fmt.Errorf("observer v3 sealed root projection incomplete")
	}
	for _, root := range r.Roots {
		v, matched := declared[root.PID]
		if !matched || !censusIdentityMatchesRoot(v, root) {
			return ResponseV3{}, fmt.Errorf("observer v3 root lacks sealed declared identity")
		}
		for _, proof := range r.Census.Proofs {
			if proof.PID == root.PID && proof.StartTicks != nil && *proof.StartTicks == root.StartTicks {
				return ResponseV3{}, fmt.Errorf("observer v3 retired incarnation is still a root")
			}
		}
	}
	return r, nil
}

func censusIdentityMatchesRoot(v CensusIdentity, r Root) bool {
	return v.PID == r.PID && v.PPID == r.PPID && v.PGID == r.PGID && v.StartTicks == r.StartTicks && v.SessionID == r.SessionID && v.City == r.City && v.Template == r.Template && v.Epoch == r.Epoch && v.InstanceTokenSHA256 == r.InstanceTokenSHA256 && v.Name == r.Name
}

// ObservedRoots projects only redacted incarnation fields into the unchanged
// ordinary provider join. The ReadV3 error must accompany this projection.
func (r ResponseV3) ObservedRoots() []proctable.ObservedRoot {
	return (Response{Roots: r.Roots}).ObservedRoots()
}

// RetirementAnchors exposes the terminal declared root of each already valid
// process-only certificate. These anchors still require both provider joins.
func (r ResponseV3) RetirementAnchors() ([]proctable.ObservedRoot, error) {
	if !r.Complete || r.CertificateDisposition != "provisional" || ValidateCensusV3(r.Census, r.Errors, r.ErrorsTotal, r.ErrorsTruncated, r.DurationMS, r.CallerBinding.PID) != nil {
		return nil, fmt.Errorf("observer v3 retirement anchors unavailable")
	}
	out := make([]proctable.ObservedRoot, 0, len(r.Census.Certificates))
	for _, cert := range r.Census.Certificates {
		v := cert.Chain[len(cert.Chain)-1]
		out = append(out, proctable.ObservedRoot{
			Runtime:  runtime.LiveRuntime{PID: v.PID, PPID: v.PPID, SessionID: v.SessionID, City: v.City, Epoch: v.Epoch, Name: v.Name},
			Template: v.Template, StartIdentity: v.StartTicks, InstanceTokenSHA256: v.InstanceTokenSHA256,
		})
	}
	return out, nil
}
