package procobserver

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func clientV3Fixture() (ReleasePolicyV3, ResponseV3, time.Time) {
	p, _, now := fixtureResponse()
	c, errors := v3Fixture()
	v := c.Certificates[0].Chain[1]
	r := ResponseV3{
		Schema: ResponseSchemaV3, Scope: "host_procfs", RequestNonce: strings.Repeat("e", 64),
		HelperSourceRevision: p.HelperSourceRevision, HelperBinarySHA256: p.HelperBinarySHA256, PolicyDigest: p.PolicyDigest,
		BootID: p.BootID, PIDNamespaceIdentity: p.PIDNamespaceIdentity, CallerBinding: p.CallerBinding,
		StartedAt: now.Add(-3 * time.Millisecond), FinishedAt: now, DurationMS: 3, Complete: true,
		EnumeratedCountBefore: c.Scans[0].EnumeratedCount, EnumeratedCountAfter: c.Scans[2].EnumeratedCount,
		EnumerationDigestBefore: c.Scans[0].EnumerationDigest, EnumerationDigestAfter: c.Scans[2].EnumerationDigest,
		Roots: []Root{{
			PID: v.PID, PPID: v.PPID, PGID: v.PGID, StartTicks: v.StartTicks, SessionID: v.SessionID,
			City: v.City, Template: v.Template, Epoch: v.Epoch, InstanceTokenSHA256: v.InstanceTokenSHA256, Name: v.Name,
		}},
		Errors: errors, ErrorsTotal: len(errors), KernelRelease: "6.8.fixture", KernelProofProfile: KernelProofProfile,
		Census: c, CertificateDisposition: "provisional",
	}
	return ReleasePolicyV3{Policy: p, EvidenceSchema: ResponseSchemaV3}, r, now
}

func TestClientV3PolicySelectorIsExact(t *testing.T) {
	for _, mode := range []string{"exact", "v2 selector", "missing selector", "null selector", "duplicate selector", "unknown field", "wrong source", "short source", "missing caller", "null UID"} {
		t.Run(mode, func(t *testing.T) {
			p, _, _ := clientV3Fixture()
			source := p.HelperSourceRevision
			if mode == "v2 selector" {
				p.EvidenceSchema = Schema
			}
			if mode == "wrong source" {
				source = strings.Repeat("0", 40)
			}
			if mode == "short source" {
				source = source[:8]
			}
			data, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "missing selector":
				data = bytes.Replace(data, []byte(`,"evidence_schema":"host-process-evidence/v3"`), nil, 1)
			case "null selector":
				data = bytes.Replace(data, []byte(`"evidence_schema":"host-process-evidence/v3"`), []byte(`"evidence_schema":null`), 1)
			case "duplicate selector":
				data = append(data[:len(data)-1], []byte(`,"evidence_schema":"host-process-evidence/v3"}`)...)
			case "unknown field":
				data = append(data[:len(data)-1], []byte(`,"refresh_binding":true}`)...)
			case "missing caller":
				data = bytes.Replace(data, []byte(`"caller_binding":`), []byte(`"other_binding":`), 1)
			case "null UID":
				data = bytes.Replace(data, []byte(`"helper_uid":42`), []byte(`"helper_uid":null`), 1)
			}
			_, err = decodePolicyV3(data, source)
			if (err == nil) != (mode == "exact") {
				t.Fatalf("mode %s: %v", mode, err)
			}
			if mode == "exact" {
				var old Policy
				if strictJSON(data, &old) == nil {
					t.Fatal("V2 silently consumed V3 selector")
				}
			}
		})
	}
}

func TestClientV3EnvelopePinsAndGrammar(t *testing.T) {
	for _, mode := range []string{"exact", "v2 wire", "v2 policy", "nonce", "helper source", "helper binary", "policy digest", "boot", "namespace", "caller", "future", "stale", "duration", "promoted helper", "partial", "wrong kernel", "status grammar pending", "UID shape", "unknown key", "duplicate key", "missing key", "null key", "root seal mismatch", "retired root", "selected closing binding", "missing sealed root"} {
		t.Run(mode, func(t *testing.T) {
			p, r, now := clientV3Fixture()
			nonce := r.RequestNonce
			switch mode {
			case "v2 wire":
				r.Schema = Schema
			case "v2 policy":
				p.EvidenceSchema = Schema
			case "nonce":
				r.RequestNonce = strings.Repeat("0", 64)
			case "helper source":
				r.HelperSourceRevision = strings.Repeat("0", 40)
			case "helper binary":
				r.HelperBinarySHA256 = strings.Repeat("0", 64)
			case "policy digest":
				r.PolicyDigest = strings.Repeat("0", 64)
			case "boot":
				r.BootID = "other"
			case "namespace":
				r.PIDNamespaceIdentity = "pid:[456]"
			case "caller":
				r.CallerBinding.PID++
			case "future":
				r.FinishedAt = now.Add(time.Second)
			case "stale":
				r.StartedAt = now.Add(-61 * time.Second)
			case "duration":
				r.DurationMS = 10000
			case "promoted helper":
				r.CertificateDisposition = "provider_verified"
			case "partial":
				r.Complete = false
			case "wrong kernel":
				r.KernelProofProfile = "fixture-only"
			case "status grammar pending":
				r.Errors[0].Operation = "status"
				r.Errors[0].Errno = 3
			case "root seal mismatch":
				r.Roots[0].StartTicks = "201"
			case "retired root":
				r.Roots[0].PID = 30
				r.Roots[0].StartTicks = "300"
			case "selected closing binding":
				r.EnumeratedCountAfter++
			case "missing sealed root":
				r.Roots = []Root{}
			}
			data, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "UID shape":
				data = bytes.ReplaceAll(data, []byte(`"uids":[1000,1000,1000,1000]`), []byte(`"uids":[1000,1000,1000]`))
			case "unknown key":
				data = append(data[:len(data)-1], []byte(`,"secret_environment":"hidden"}`)...)
			case "duplicate key":
				data = append(data[:len(data)-1], []byte(`,"complete":true}`)...)
			case "missing key":
				data = bytes.Replace(data, []byte(`"complete":true,`), nil, 1)
			case "null key":
				data = bytes.Replace(data, []byte(`"complete":true`), []byte(`"complete":null`), 1)
			}
			got, err := DecodeResponseV3(data, p, nonce, now)
			if (err == nil) != (mode == "exact") {
				t.Fatalf("mode %s: %v", mode, err)
			}
			if mode == "partial" && (got.Schema != ResponseSchemaV3 || got.CertificateDisposition != "provisional" || len(got.Census.Certificates) != 1) {
				t.Fatal("trusted partial history lost")
			}
			if mode == "exact" {
				if len(got.ObservedRoots()) != 1 {
					t.Fatal("root lost")
				}
				anchors, e := got.RetirementAnchors()
				if e != nil || len(anchors) != 1 || anchors[0].Runtime.PID != 20 {
					t.Fatalf("anchors %v %v", anchors, e)
				}
			}
			if err != nil && strings.Contains(err.Error(), "hidden") {
				t.Fatal("input leaked in error")
			}
		})
	}
}

func TestClientV3CanceledReadDoesNotOpenTransport(t *testing.T) {
	p, _, _ := clientV3Fixture()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReadContextV3(ctx, p); err == nil {
		t.Fatal("canceled request accepted")
	}
}

func TestClientV3RootProjectionRequiresDeclaredIdentity(t *testing.T) {
	p, r, now := clientV3Fixture()
	for i := range r.Census.Scans {
		root := r.Census.Scans[i].Verified[0]
		root.DeclaredRoot = false
		r.Census.Scans[i].Verified = []CensusIdentity{root}
		r.Census.Scans[i].EnumeratedCount = 1
		r.Census.Scans[i].LiveCount = 1
		r.Census.Scans[i].EnumerationDigest = r.EnumerationDigestBefore
		r.Census.Scans[i].LiveDigest = r.Census.ReconciledDigest
	}
	r.Census.Proofs = []CensusProofV3{}
	r.Census.Certificates = []DescendantCertificate{}
	r.Census.Resolutions = []CensusResolution{}
	r.Errors = []EvidenceError{}
	r.ErrorsTotal = 0
	r.EnumeratedCountBefore = 1
	r.EnumeratedCountAfter = 1
	if err := ValidateCensusV3(r.Census, r.Errors, r.ErrorsTotal, r.ErrorsTruncated, r.DurationMS, p.CallerBinding.PID); err != nil {
		t.Fatalf("synthetic census invalid before projection check: %v", err)
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeResponseV3(data, p, r.RequestNonce, now); err == nil {
		t.Fatal("non-root managed identity was accepted as a root")
	}
	r.Roots = []Root{}
	data, err = json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeResponseV3(data, p, r.RequestNonce, now)
	if err != nil {
		t.Fatal(err)
	}
	anchors, err := got.RetirementAnchors()
	if err != nil || anchors == nil || len(anchors) != 0 {
		t.Fatal("empty valid certificate set lost its explicit non-nil contract")
	}
}

func TestCaptureDiagnosticNegativeOnly(t *testing.T) {
	yes, no := true, false
	start := "123"
	base := EvidenceError{Reason: "process_unavailable", Operation: "environ", Errno: 3, PID: 42, StartTicks: &start, ScanIndex: 6}
	cases := []struct {
		name string
		d    CaptureDiagnostic
		want bool
	}{
		{"first actual exit", CaptureDiagnostic{1, &yes, &no, "valid_pollin", &yes}, true},
		{"second read withheld", CaptureDiagnostic{2, &yes, &yes, "not_performed", &no}, true},
		{"poll failure", CaptureDiagnostic{1, &yes, &no, "failure", &no}, true},
		{"poll zero", CaptureDiagnostic{1, &yes, &no, "zero", &no}, true},
		{"refused flags", CaptureDiagnostic{1, &yes, &no, "refused_flags", &no}, true},
		{"valid poll with refused deadline", CaptureDiagnostic{1, &yes, &no, "valid_pollin", &no}, true},
		{"missing required bool", CaptureDiagnostic{1, nil, &no, "zero", &no}, false},
		{"arbitrary value", CaptureDiagnostic{1, &yes, &no, "secret-argv", &no}, false},
		{"false witness on second", CaptureDiagnostic{2, &yes, &yes, "not_performed", &yes}, false},
		{"unbound purported exit", CaptureDiagnostic{1, &no, &no, "valid_pollin", &yes}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := base
			e.CaptureDiagnostic = &c.d
			if got := validCaptureDiagnostic(e, false); got != c.want {
				t.Fatalf("valid=%v want=%v", got, c.want)
			}
			if validCaptureDiagnostic(e, true) {
				t.Fatal("negative category accepted on COMPLETE frame")
			}
		})
	}
}

func TestCaptureDiagnosticOwnedCodec(t *testing.T) {
	for _, key := range []string{"GC_TEST_CAPTURE_FIRST_VECTOR", "GC_TEST_CAPTURE_SECOND_VECTOR"} {
		path := os.Getenv(key)
		if path == "" {
			t.Skip("owned C diagnostic vectors not supplied")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var errors []EvidenceError
		if err := DecodeStrict(raw, &errors); err != nil {
			t.Fatal(err)
		}
		for _, bad := range [][]byte{
			bytes.ReplaceAll(raw, []byte(`"bound_live":true`), []byte(`"bound_live":null`)),
			bytes.ReplaceAll(raw, []byte(`"exit_poll":`), []byte(`"argv":"forbidden","exit_poll":`)),
			bytes.ReplaceAll(raw, []byte(`"fields_pass":`), []byte(`"fields_pass":1,"fields_pass":`)),
		} {
			var rejected []EvidenceError
			if DecodeStrict(bad, &rejected) == nil {
				t.Fatal("invalid/null/duplicate categorical wire accepted")
			}
		}
		if len(errors) != 1 || errors[0].Reason != "process_unavailable" || errors[0].Errno != 3 || errors[0].Operation != "environ" || errors[0].ScanIndex != 6 || errors[0].ResolvedBy != 0 || errors[0].StartTicks == nil {
			t.Fatal("raw C fault changed")
		}
		d := errors[0].CaptureDiagnostic
		if d == nil || !validCaptureDiagnostic(errors[0], false) || validCaptureDiagnostic(errors[0], true) {
			t.Fatal("C negative category lost or promoted")
		}
		wantPass := 1
		if key == "GC_TEST_CAPTURE_SECOND_VECTOR" {
			wantPass = 2
		}
		if d.FieldsPass != wantPass {
			t.Fatal("wrong actual bounded read pass")
		}
	}
}

func TestCaptureDiagnosticDoesNotExpandLegacyResponse(t *testing.T) {
	p, r, now := fixtureResponse()
	yes, no := true, false
	start := "123"
	r.Complete = false
	r.ErrorsTotal = 1
	r.Errors = []EvidenceError{{Reason: "process_unavailable", PID: 42, Operation: "environ", Errno: 3, ScanIndex: 1, StartTicks: &start, CaptureDiagnostic: &CaptureDiagnostic{1, &yes, &no, "zero", &no}}}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeResponse(raw, p, r.RequestNonce, now)
	if err == nil || decoded.Schema != "" {
		t.Fatal("V3 diagnostic accepted on legacy envelope")
	}
}
