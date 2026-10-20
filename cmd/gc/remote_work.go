//go:build linux

package main

// remote-work is an explicit, narrow delegated control-plane projection. A
// private root intake proves the remote process before dropping to the city UID.
// Ordinary gc bd and its local ancestry contract are deliberately unchanged.
import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/spf13/cobra"
)

const (
	remoteWorkFrameLimit = 64 << 10
	remoteWorkPolicyPath = "/etc/gascity/remote-work-policy.json"
)

var (
	errRemoteWorkDenied = errors.New("remote work authority denied")
	remoteWorkID        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,159}$`)
)

type remoteWorkRequest struct {
	RequestID   string `json:"request_id"`
	Op          string `json:"op"`
	BeadID      string `json:"bead_id,omitempty"`
	Text        string `json:"text,omitempty"`
	EvidenceSHA string `json:"evidence_sha,omitempty"`
	Outcome     string `json:"outcome,omitempty"`
}
type remoteWorkIdentity struct {
	SID           string `json:"sid"`
	Template      string `json:"template"`
	Handle        string `json:"runtime_handle"`
	Generation    string `json:"generation"`
	InstanceToken string `json:"instance_token"`
	Attempt       string `json:"attempt"`
}

// Format keeps private incarnation material out of diagnostics.
func (remoteWorkIdentity) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[private remote identity]")
}

type remoteWorkObservation struct {
	ReceiptID    string    `json:"receipt_id"`
	Revision     string    `json:"revision"`
	BootID       string    `json:"boot_id"`
	Unit         string    `json:"unit"`
	Cgroup       string    `json:"cgroup"`
	InvocationID string    `json:"invocation_id"`
	ModelUID     int       `json:"model_uid"`
	PID          int       `json:"pid"`
	Start        string    `json:"start"`
	Phase        string    `json:"phase"`
	SourceSHA    string    `json:"source_sha"`
	ManifestSHA  string    `json:"manifest_sha"`
	ObservedAt   time.Time `json:"observed_at"`
}
type remoteWorkFrame struct {
	SchemaVersion int                   `json:"schema_version"`
	ProfileID     string                `json:"profile_id"`
	Identity      remoteWorkIdentity    `json:"identity"`
	Observe       remoteWorkObservation `json:"observe"`
	Request       remoteWorkRequest     `json:"request"`
}

func (remoteWorkFrame) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[private remote frame]")
}

type remoteWorkProfile struct {
	ID              string `json:"id"`
	CityPath        string `json:"city_path"`
	Template        string `json:"template"`
	ModelUID        int    `json:"model_uid"`
	RuntimeProvider string `json:"runtime_provider"`
	RuntimeTarget   string `json:"runtime_target"`
	ResourceDomain  string `json:"resource_domain"`
	PolicyDigest    string `json:"policy_digest"`
	SourceSHA       string `json:"source_sha"`
	ManifestSHA     string `json:"manifest_sha"`
}
type remoteWorkPolicy struct {
	SchemaVersion int                 `json:"schema_version"`
	CityUID       int                 `json:"city_uid"`
	HelperPath    string              `json:"helper_path"`
	HelperSHA     string              `json:"helper_sha256"`
	ParentArgv    []string            `json:"parent_argv"`
	JournalDir    string              `json:"journal_dir"`
	LeaseCommand  string              `json:"lease_command"`
	LeaseSHA      string              `json:"lease_sha256"`
	Profiles      []remoteWorkProfile `json:"profiles"`
}
type remoteWorkParent struct {
	PID           int
	Start         string
	UID           int
	ChildUID      int
	Argv          []string
	HelperSHA     string
	HelperSafe    bool
	PipeRoot      bool
	PipeAnonymous bool
	PipeReadOnly  bool
}

func verifyRemoteWorkParent(p remoteWorkPolicy, a remoteWorkParent) error {
	if a.PID <= 1 || a.Start == "" || a.UID != 0 || p.CityUID <= 0 || a.ChildUID != p.CityUID || !a.HelperSafe || !a.PipeRoot || !a.PipeAnonymous || !a.PipeReadOnly || len(p.ParentArgv) < 2 || !reflect.DeepEqual(a.Argv, p.ParentArgv) || a.HelperSHA != p.HelperSHA {
		return errRemoteWorkDenied
	}
	return nil
}

func decodeRemoteWorkFrame(r io.Reader) (remoteWorkFrame, error) {
	var f remoteWorkFrame
	raw, err := io.ReadAll(io.LimitReader(r, remoteWorkFrameLimit+1))
	if err != nil || len(raw) > remoteWorkFrameLimit {
		return f, errRemoteWorkDenied
	}
	if err = remoteWorkDecodeStrict(raw, &f); err != nil {
		return f, errRemoteWorkDenied
	}
	if f.SchemaVersion != 1 || !remoteWorkID.MatchString(f.ProfileID) || validateRemoteWorkRequest(f.Request) != nil {
		return f, errRemoteWorkDenied
	}
	return f, nil
}

// Reject duplicate keys as well as unknown fields. encoding/json alone accepts
// the last duplicate value, an ambiguous authority frame we never interpret.
func remoteWorkDecodeStrict(raw []byte, out any) error {
	if !utf8.Valid(raw) {
		return errRemoteWorkDenied
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if err := remoteWorkUniqueJSON(d); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errRemoteWorkDenied
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	return nil
}

func remoteWorkUniqueJSON(d *json.Decoder) error {
	t, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			k, e := d.Token()
			if e != nil {
				return e
			}
			key, ok := k.(string)
			if !ok || seen[key] {
				return errRemoteWorkDenied
			}
			seen[key] = true
			if e = remoteWorkUniqueJSON(d); e != nil {
				return e
			}
		}
		end, e := d.Token()
		if e != nil || end != json.Delim('}') {
			return errRemoteWorkDenied
		}
	case '[':
		for d.More() {
			if e := remoteWorkUniqueJSON(d); e != nil {
				return e
			}
		}
		end, e := d.Token()
		if e != nil || end != json.Delim(']') {
			return errRemoteWorkDenied
		}
	default:
		return errRemoteWorkDenied
	}
	return nil
}

func validateRemoteWorkRequest(r remoteWorkRequest) error {
	if !remoteWorkID.MatchString(r.RequestID) || len(r.Text) > 16<<10 || strings.ContainsRune(r.Text, 0) {
		return errRemoteWorkDenied
	}
	switch r.Op {
	case "claim", "current", "self_drain":
		if r.BeadID != "" || r.Text != "" || r.EvidenceSHA != "" || r.Outcome != "" {
			return errRemoteWorkDenied
		}
	case "read":
		if !remoteWorkID.MatchString(r.BeadID) || r.Text != "" || r.EvidenceSHA != "" || r.Outcome != "" {
			return errRemoteWorkDenied
		}
	case "result", "close":
		if (r.Op == "result" && !validWorkOutcome(r.Outcome)) || (r.Op == "close" && r.Outcome != "") {
			return errRemoteWorkDenied
		}
		if !remoteWorkID.MatchString(r.BeadID) || strings.TrimSpace(r.Text) == "" || !remoteWorkHex(r.EvidenceSHA, 64) {
			return errRemoteWorkDenied
		}
	default:
		return errRemoteWorkDenied
	}
	return nil
}

func remoteWorkHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	_, e := hex.DecodeString(s)
	return e == nil && s == strings.ToLower(s)
}

type remoteWorkResponse struct {
	SchemaVersion int             `json:"schema_version"`
	RequestID     string          `json:"request_id"`
	State         string          `json:"state"`
	BeadID        string          `json:"bead_id,omitempty"`
	Bead          *remoteWorkBead `json:"bead,omitempty"`
}

// Only work content, never arbitrary metadata or session credentials, crosses
// the remote response. The root adapter owns its metadata allowlist.
type remoteWorkBead struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Status      string `json:"status"`
	Assignee    string `json:"assignee"`
}

func remoteWorkPublicBead(b beads.Bead) *remoteWorkBead {
	return &remoteWorkBead{b.ID, b.Title, b.Description, b.Status, b.Assignee}
}

type remoteWorkOperations interface {
	Validate(context.Context) error
	Execute(context.Context, remoteWorkRequest) (remoteWorkResponse, error)
	Reconcile(context.Context, remoteWorkRequest) (remoteWorkResponse, error)
}

func runRemoteWork(ctx context.Context, scope string, r remoteWorkRequest, j *remoteWorkJournal, ops remoteWorkOperations) (remoteWorkResponse, error) {
	if e := validateRemoteWorkRequest(r); e != nil {
		return remoteWorkResponse{}, e
	}
	if e := ops.Validate(ctx); e != nil {
		return remoteWorkResponse{}, errRemoteWorkDenied
	}
	unlock, e := j.Lock(scope, r)
	if e != nil {
		return remoteWorkResponse{}, errRemoteWorkDenied
	}
	defer unlock()
	record, exists, e := j.Load(scope, r)
	if e != nil {
		return remoteWorkResponse{}, errRemoteWorkDenied
	}
	if exists {
		if record.Response != nil {
			return *record.Response, nil
		}
		out, e := ops.Reconcile(ctx, r)
		if e != nil {
			return remoteWorkResponse{}, errRemoteWorkDenied
		}
		if e = j.Finish(scope, r, out); e != nil {
			return remoteWorkResponse{}, errRemoteWorkDenied
		}
		return out, nil
	}
	if e = j.Intent(scope, r); e != nil {
		return remoteWorkResponse{}, errRemoteWorkDenied
	}
	// Validation is repeated after queue/lock wait and before any effect.
	if e = ops.Validate(ctx); e != nil {
		return remoteWorkResponse{}, errRemoteWorkDenied
	}
	out, e := ops.Execute(ctx, r)
	if e != nil {
		return remoteWorkResponse{}, errRemoteWorkDenied
	}
	if e = j.Finish(scope, r, out); e != nil {
		return remoteWorkResponse{}, errRemoteWorkDenied
	}
	return out, nil
}

func remoteWorkClaimResponse(raw []byte, code int) (remoteWorkResponse, error) {
	var h hookClaimJSONResult
	if len(raw) > remoteWorkFrameLimit || remoteWorkDecodeStrict(raw, &h) != nil || h.SchemaVersion != "1" || !h.OK || h.Command != "hook" {
		return remoteWorkResponse{}, errRemoteWorkDenied
	}
	if h.Action == "drain" && h.Reason == "no_work" && code == 1 && !h.DrainAcknowledged {
		return remoteWorkResponse{SchemaVersion: 1, State: "idle"}, nil
	}
	if code != 0 || h.Action != "work" || !remoteWorkID.MatchString(h.BeadID) || h.Assignee == "" || h.ContinuationGroup != "" || len(h.ContinuationAssigned) > 0 {
		return remoteWorkResponse{}, errRemoteWorkDenied
	}
	switch h.Reason {
	case "claimed", "existing_assignment", "ready_assignment":
	default:
		return remoteWorkResponse{}, errRemoteWorkDenied
	}
	return remoteWorkResponse{SchemaVersion: 1, State: "claimed", BeadID: h.BeadID}, nil
}

func validateRemoteWorkResponse(r remoteWorkRequest, out remoteWorkResponse) error {
	if out.SchemaVersion != 1 || out.RequestID != r.RequestID {
		return errRemoteWorkDenied
	}
	allowed := map[string][]string{"claim": {"claimed", "idle"}, "current": {"claimed"}, "read": {"read"}, "result": {"recorded"}, "close": {"closed"}, "self_drain": {"drain_acknowledged"}}
	ok := false
	for _, state := range allowed[r.Op] {
		if out.State == state {
			ok = true
		}
	}
	if !ok {
		return errRemoteWorkDenied
	}
	if out.State == "idle" || out.State == "drain_acknowledged" {
		if out.BeadID != "" || out.Bead != nil {
			return errRemoteWorkDenied
		}
		return nil
	}
	if !remoteWorkID.MatchString(out.BeadID) || (r.BeadID != "" && out.BeadID != r.BeadID) {
		return errRemoteWorkDenied
	}
	if out.Bead != nil && (out.Bead.ID != out.BeadID || len(out.Bead.Title) > 16<<10 || len(out.Bead.Description) > 32<<10) {
		return errRemoteWorkDenied
	}
	return nil
}

func remoteWorkRequestDigest(r remoteWorkRequest) string {
	raw, _ := json.Marshal(r)
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

func newRemoteWorkCmd(stdout, stderr io.Writer) *cobra.Command {
	var proofFD int
	cmd := &cobra.Command{Use: "remote-work", Hidden: true, Args: cobra.NoArgs, Short: "Receive one root-attested remote work operation"}
	cmd.Flags().IntVar(&proofFD, "proof-fd", -1, "private root-intake proof pipe (must be 3)")
	cmd.RunE = func(_ *cobra.Command, _ []string) error {
		if e := receiveRemoteWork(proofFD, stdout); e != nil {
			_, _ = fmt.Fprintln(stderr, "gc remote-work: authority or operation unavailable")
			return errExit
		}
		return nil
	}
	return cmd
}
