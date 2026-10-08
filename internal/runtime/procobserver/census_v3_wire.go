package procobserver

import "time"

// Exact V3 wire selectors are separate from the legacy helper protocol.
const (
	ResponseSchemaV3 = "host-process-evidence/v3"
	RequestSchemaV3  = "observe-host-processes/v3"
)

// ReleasePolicyV3 is a separate selector contract. The unchanged V2 policy
// reader rejects evidence_schema rather than silently treating V3 as V2.
// Loading/using this selector is not enabled before the producer is coherent.
type ReleasePolicyV3 struct {
	Policy
	EvidenceSchema string `json:"evidence_schema"`
}

// ResponseV3 is a separate typed process-only envelope. The helper may attest
// only provisional certificates; provider authority belongs to ordinary gc.
// Current ReadContext deliberately remains V2 until the V3 producer is coherent.
type ResponseV3 struct {
	Schema                  string          `json:"schema"`
	Scope                   string          `json:"scope"`
	RequestNonce            string          `json:"request_nonce"`
	HelperSourceRevision    string          `json:"helper_source_revision"`
	HelperBinarySHA256      string          `json:"helper_binary_sha256"`
	PolicyDigest            string          `json:"policy_digest"`
	BootID                  string          `json:"boot_id"`
	PIDNamespaceIdentity    string          `json:"pid_namespace_identity"`
	StartedAt               time.Time       `json:"started_at"`
	FinishedAt              time.Time       `json:"finished_at"`
	DurationMS              int64           `json:"duration_ms"`
	Complete                bool            `json:"complete"`
	EnumeratedCountBefore   int             `json:"enumerated_count_before"`
	EnumeratedCountAfter    int             `json:"enumerated_count_after"`
	EnumerationDigestBefore string          `json:"enumeration_digest_before"`
	EnumerationDigestAfter  string          `json:"enumeration_digest_after"`
	Roots                   []Root          `json:"roots"`
	Errors                  []EvidenceError `json:"errors"`
	ErrorsTotal             int             `json:"errors_total"`
	ErrorsTruncated         bool            `json:"errors_truncated"`
	CallerBinding           CallerBinding   `json:"caller_binding"`
	KernelRelease           string          `json:"kernel_release"`
	KernelProofProfile      string          `json:"kernel_proof_profile"`
	Census                  CensusV3        `json:"census"`
	CertificateDisposition  string          `json:"certificate_disposition"`
}
