package procobserver

import (
	"fmt"
	"path/filepath"
)

// CensusV3Schema identifies a separate, fail-closed grammar. V2 indices and
// proof protection are never reinterpreted as V3 scan history or certificates.
const CensusV3Schema = "bounded-process-census/v3"

// CensusIdentity contains owned, redacted incarnation fields. Each process's
// own PGID and all four UIDs are retained; ancestry is not PGID equality.
type CensusIdentity struct {
	Classification         string    `json:"classification"`
	KernelFlags            uint32    `json:"kernel_flags"`
	NoGCEnvironment        bool      `json:"no_gc_environment"`
	EnvironmentRevalidated bool      `json:"environment_revalidated"`
	PID                    int       `json:"pid"`
	PPID                   int       `json:"ppid"`
	PGID                   int       `json:"pgid"`
	UIDs                   [4]uint32 `json:"uids"`
	StartTicks             string    `json:"start_ticks"`
	SessionID              string    `json:"session_id"`
	City                   string    `json:"city"`
	Template               string    `json:"template"`
	Epoch                  int       `json:"epoch"`
	InstanceTokenSHA256    string    `json:"instance_token_sha256"`
	Name                   string    `json:"name"`
	DeclaredRoot           bool      `json:"declared_root"`
	StatRevalidated        bool      `json:"stat_revalidated"`
	PIDFDBound             bool      `json:"pidfd_bound"`
	UIDsRevalidated        bool      `json:"uids_revalidated"`
}

// CensusScanV3 is an immutable scan receipt, including identities needed to
// check certificates. Records are chronological and share one global budget.
type CensusScanV3 struct {
	CensusClosing
	Round    int              `json:"round"`
	Kind     string           `json:"kind"`
	OffsetMS int64            `json:"offset_ms"`
	Verified []CensusIdentity `json:"verified"`
}

// DescendantCertificate links a fully read prior inherited chain to an exact
// retirement witness. It is provisional until the ordinary provider join.
type DescendantCertificate struct {
	ID              int              `json:"id"`
	PriorScan       int              `json:"prior_scan"`
	Chain           []CensusIdentity `json:"chain"`
	AbsenceProof    int              `json:"absence_proof"`
	RetirementProof int              `json:"retirement_proof"`
}

// CensusProofV3 couples a kernel witness to its provisional descendant chain.
type CensusProofV3 struct {
	CensusProof
	DescendantCertificateID int `json:"descendant_certificate_id"`
}

// CensusResolution records a positive discharge without overwriting raw errors.
// Exactly one typed resolution is allowed per raw error; error indices are 1-based.
type CensusResolution struct {
	ErrorIndex     int    `json:"error_index"`
	Kind           string `json:"kind"`
	ProofIndex     int    `json:"proof_index"`
	ClassifiedScan int    `json:"classified_scan"`
	SelectedSeal   int    `json:"selected_seal"`
}

// CensusV3 retains the original scans and typed bounded-closing evidence.
type CensusV3 struct {
	Schema           string                  `json:"schema"`
	Scans            []CensusScanV3          `json:"scans"`
	SelectedClosings [2]int                  `json:"selected_closings"`
	Seal             CensusSeal              `json:"seal"`
	ReconciledCount  int                     `json:"reconciled_count"`
	ReconciledDigest string                  `json:"reconciled_digest"`
	Proofs           []CensusProofV3         `json:"proofs"`
	Certificates     []DescendantCertificate `json:"certificates"`
	Resolutions      []CensusResolution      `json:"resolutions"`
	PeakFD           int                     `json:"peak_fd"`
	RetainedBytes    int64                   `json:"retained_bytes"`
}

// ValidateCensusV3 validates process evidence only. It never grants provider
// authority to a certificate; anchors must survive a separate positive join.
func ValidateCensusV3(c CensusV3, errors []EvidenceError, total int, truncated bool, duration int64, controllerPID int) error {
	bad := func() error { return fmt.Errorf("observer v3 census incomplete") }
	if c.Schema != CensusV3Schema || controllerPID <= 1 || duration < 0 || duration >= 10000 ||
		c.PeakFD < 1 || c.PeakFD > 32 || c.RetainedBytes < 1 || c.RetainedBytes > 16*1024*1024 ||
		len(c.Scans) < 4 || len(c.Scans) > 10 || (len(c.Scans)-1)%3 != 0 ||
		c.Proofs == nil || len(c.Proofs) > 128 || c.Certificates == nil || len(c.Certificates) > 128 ||
		c.Resolutions == nil || errors == nil || len(errors) > 128 || total != len(errors) || truncated || len(c.Resolutions) != len(errors) {
		return bad()
	}
	byScan := map[int]map[int]CensusIdentity{}
	var previousOffset int64
	for i, s := range c.Scans {
		kind, round := "initial", 0
		if i > 0 {
			round = (i-1)/3 + 1
			kind = "closing"
			if i%3 == 0 {
				kind = "seal"
			}
		}
		if s.ScanIndex != i+1 || s.Kind != kind || s.Round != round || s.OffsetMS < previousOffset || s.OffsetMS > duration ||
			s.EnumeratedCount < 1 || s.EnumeratedCount > MaxProcesses || s.LiveCount < 1 || s.LiveCount > s.EnumeratedCount ||
			!isHex(s.EnumerationDigest, 64) || !isHex(s.LiveDigest, 64) || s.Verified == nil || len(s.Verified) > s.LiveCount {
			return bad()
		}
		previousOffset = s.OffsetMS
		rows := map[int]CensusIdentity{}
		for _, v := range s.Verified {
			if !validCensusIdentity(v, controllerPID) {
				return bad()
			}
			if _, dup := rows[v.PID]; dup {
				return bad()
			}
			rows[v.PID] = v
		}
		byScan[s.ScanIndex] = rows
	}
	// Selecting an earlier successful round while a later unresolved scan exists
	// would discard evidence. The selected seal must close this very invocation.
	n := len(c.Scans)
	first, second := c.Scans[n-3], c.Scans[n-2]
	last := c.Scans[n-1]
	if c.SelectedClosings != [2]int{first.ScanIndex, second.ScanIndex} || c.Seal.ScanIndex != last.ScanIndex ||
		c.Seal.EnumeratedCount != last.EnumeratedCount || !isHex(c.Seal.PIDDigest, 64) ||
		c.Seal.ClassifiedCount != last.LiveCount || c.Seal.ClassifiedDigest != last.LiveDigest ||
		c.ReconciledCount != last.LiveCount || c.ReconciledDigest != last.LiveDigest ||
		c.ReconciledCount > first.LiveCount || c.ReconciledCount > second.LiveCount {
		return bad()
	}
	seenProof := map[string]bool{}
	seenRetirement := map[string]bool{}
	usedCertificates := map[int]bool{}
	for i, cert := range c.Certificates {
		if cert.ID != i+1 {
			return bad()
		}
	}
	retireFirst, retireSecond := 0, 0
	for index, p := range c.Proofs {
		if p.PID <= 1 || p.PID > 2147483647 || p.PID == controllerPID || p.ScanIndex < 1 || p.ScanIndex > n ||
			p.OffsetMS < c.Scans[p.ScanIndex-1].OffsetMS || p.OffsetMS > duration {
			return bad()
		}
		start := ""
		if p.StartTicks != nil {
			start = *p.StartTicks
		}
		key := fmt.Sprintf("%d/%d/%s", p.ScanIndex, p.PID, start)
		if seenProof[key] {
			return bad()
		}
		seenProof[key] = true
		switch p.Kind {
		case "enumerated_pid_absent":
			if p.StartTicks != nil || p.Method != "pidfd_no_pid" || p.ReplacementStart != "" || p.ProtectedIdentity || p.DescendantCertificateID != 0 {
				return bad()
			}
		case "terminal_zombie":
			if p.StartTicks == nil || !positiveNumber(start) || p.Method != "pidfd_zombie_stat" ||
				p.ReplacementStart != "" || p.ProtectedIdentity || p.DescendantCertificateID != 0 {
				return bad()
			}
			// No prior positive identity may be retired through a numeric zombie
			// witness; managed descendants still require their protected chain.
			for scan := 1; scan < p.ScanIndex; scan++ {
				if _, ok := byScan[scan][p.PID]; ok {
					return bad()
				}
			}
		case "incarnation_retired":
			if p.StartTicks == nil || !positiveNumber(start) {
				return bad()
			}
			incarnation := fmt.Sprintf("%d/%s", p.PID, start)
			if seenRetirement[incarnation] {
				return bad()
			}
			seenRetirement[incarnation] = true
			// Protection is derived from prior positive identity, not solely from
			// the proof's label. A declared root is never eligible for retirement.
			for scan := 1; scan < p.ScanIndex; scan++ {
				if v, ok := byScan[scan][p.PID]; ok && v.StartTicks == start {
					if v.DeclaredRoot || infrastructureName(v.Name) || v.Classification == "managed" && !p.ProtectedIdentity {
						return bad()
					}
				}
			}
			switch p.Method {
			case "pidfd_no_pid", "pidfd_exited":
				if p.ReplacementStart != "" {
					return bad()
				}
			case "pidfd_new_incarnation":
				if c.Scans[p.ScanIndex-1].Kind == "seal" || !positiveNumber(p.ReplacementStart) || p.ReplacementStart == start {
					return bad()
				}
			default:
				return bad()
			}
			if p.ProtectedIdentity {
				id := p.DescendantCertificateID
				if id < 1 || id > len(c.Certificates) || usedCertificates[id] || p.Method != "pidfd_no_pid" ||
					!validDescendantCertificate(c, byScan, c.Certificates[id-1], index+1, controllerPID) {
					return bad()
				}
				usedCertificates[id] = true
			} else if p.DescendantCertificateID != 0 {
				return bad()
			}
			if p.ScanIndex > first.ScanIndex {
				retireFirst++
			}
			if p.ScanIndex > second.ScanIndex {
				retireSecond++
			}
		default:
			return bad()
		}
		for _, s := range c.Scans {
			if s.ScanIndex >= p.ScanIndex {
				if v, exists := byScan[s.ScanIndex][p.PID]; exists && v.StartTicks == start {
					return bad()
				}
			}
		}
	}
	if len(usedCertificates) != len(c.Certificates) || first.LiveCount-c.ReconciledCount > retireFirst || second.LiveCount-c.ReconciledCount > retireSecond ||
		first.LiveCount == c.ReconciledCount && first.LiveDigest != c.ReconciledDigest ||
		second.LiveCount == c.ReconciledCount && second.LiveDigest != c.ReconciledDigest {
		return bad()
	}
	seenErrors := map[int]bool{}
	for _, r := range c.Resolutions {
		if r.ErrorIndex < 1 || r.ErrorIndex > len(errors) || seenErrors[r.ErrorIndex] {
			return bad()
		}
		seenErrors[r.ErrorIndex] = true
		e := errors[r.ErrorIndex-1]
		if e.ScanIndex < 1 || e.ScanIndex > n || e.ResolvedBy != 0 {
			return bad()
		}
		switch r.Kind {
		case "kernel_absence":
			if r.ClassifiedScan != 0 || r.SelectedSeal != 0 || r.ProofIndex < 1 || r.ProofIndex > len(c.Proofs) ||
				e.Reason != "process_unavailable" || !(e.Operation == "stat" && e.Errno == 2 || e.Operation == "environ" && e.Errno == 3 || e.Operation == "comm" && (e.Errno == 2 || e.Errno == 3) || e.Operation == "pidfd_open" && (e.Errno == 3 || e.Errno == 22 && e.StartTicks == nil) || e.Operation == "status" && e.Errno == 3 && e.StartTicks != nil || e.Operation == "pidfd_poll" && e.Errno == 116 && e.StartTicks == nil) {
				return bad()
			}
			p := c.Proofs[r.ProofIndex-1]
			if p.PID != e.PID || p.ScanIndex < e.ScanIndex ||
				(p.Kind == "terminal_zombie") != (e.Operation == "pidfd_poll") ||
				(p.Kind != "terminal_zombie" && !sameStart(p.StartTicks, e.StartTicks)) ||
				p.Kind == "enumerated_pid_absent" && e.Operation != "stat" && !(e.Operation == "pidfd_open" && (e.Errno == 3 || e.Errno == 22 && e.StartTicks == nil)) {
				return bad()
			}
		case "fresh_classification":
			if r.ProofIndex != 0 || r.SelectedSeal != n || r.ClassifiedScan <= e.ScanIndex || r.ClassifiedScan > first.ScanIndex ||
				e.Reason != "uninspected_birth" || e.Operation != "census" || e.Errno != 0 || e.PID <= 1 {
				return bad()
			}
			v, ok := byScan[r.ClassifiedScan][e.PID]
			if !ok {
				return bad()
			}
			if e.StartTicks != nil && v.StartTicks != *e.StartTicks {
				return bad()
			}
			// A numeric-only birth may have no prior full identity. Once the
			// original scan positively classified it, preserve that exact
			// incarnation, inherited tuple, UID and root protection as well.
			original, known := byScan[e.ScanIndex][e.PID]
			if e.StartTicks != nil && !known || known && !sameFreshClassification(original, v) {
				return bad()
			}
			// Null birth is only a numeric coverage event; it grants no ancestry or
			// retirement proof. The selected pair/seal still classify its survivor.
			if !sameFreshClassification(byScan[first.ScanIndex][e.PID], v) || !sameFreshClassification(byScan[second.ScanIndex][e.PID], v) || !sameFreshClassification(byScan[n][e.PID], v) {
				return bad()
			}
		case "fresh_census":
			if r.ProofIndex != 0 || r.ClassifiedScan != 0 || r.SelectedSeal != n || e.PID != 0 || e.Errno != 0 || e.StartTicks != nil ||
				!((e.Reason == "coverage_changed" && e.Operation == "enumerate") || (e.Reason == "closing_census_changed" && e.Operation == "census")) {
				return bad()
			}
		default:
			return bad()
		}
	}
	return nil
}

func validCensusIdentity(v CensusIdentity, controller int) bool {
	if !(v.PID > 1 && v.PID <= 2147483647 && v.PID != controller && v.PPID >= 0 && v.PPID <= 2147483647 && v.PGID >= 0 && v.PGID <= 2147483647 &&
		(positiveNumber(v.StartTicks) || v.Classification == "kernel" && v.StartTicks == "0") && v.Name != "" && len(v.Name) <= 256 && v.StatRevalidated && v.PIDFDBound && v.UIDsRevalidated) {
		return false
	}
	emptySession := v.SessionID == "" && v.Template == "" && v.Epoch == 0 && v.InstanceTokenSHA256 == ""
	emptyTuple := emptySession && v.City == ""
	switch v.Classification {
	case "managed":
		return v.PGID > 0 && v.KernelFlags == 0 && !v.NoGCEnvironment && v.EnvironmentRevalidated && safeIdentity(v.SessionID) && safeIdentity(v.City) && filepath.IsAbs(v.City) &&
			filepath.Clean(v.City) == v.City && safeIdentity(v.Template) && v.Epoch > 0 && isHex(v.InstanceTokenSHA256, 64) && !infrastructureName(v.Name)
	case "nonmanaged":
		return v.PGID > 0 && v.KernelFlags == 0 && v.NoGCEnvironment && v.EnvironmentRevalidated && emptySession &&
			(v.City == "" || safeIdentity(v.City)) && !v.DeclaredRoot
	case "kernel":
		return v.KernelFlags == 0x00200000 && !v.NoGCEnvironment && !v.EnvironmentRevalidated && emptyTuple && !v.DeclaredRoot
	default:
		return false
	}
}

func sameFreshClassification(a, b CensusIdentity) bool {
	if a.Classification == "kernel" && b.Classification == "kernel" && a.KernelFlags == 0x00200000 && b.KernelFlags == 0x00200000 {
		// Genuine PF_KTHREAD makes comm a work label, not incarnation identity.
		a.Name = ""
		b.Name = ""
	}
	return a == b
}

func sameInheritedTuple(a, b CensusIdentity) bool {
	return a.SessionID == b.SessionID && a.City == b.City && a.Template == b.Template && a.Epoch == b.Epoch && a.InstanceTokenSHA256 == b.InstanceTokenSHA256 && a.UIDs == b.UIDs
}

func validDescendantCertificate(c CensusV3, rows map[int]map[int]CensusIdentity, cert DescendantCertificate, proofIndex, controller int) bool {
	if cert.ID < 1 || cert.ID > len(c.Certificates) || c.Certificates[cert.ID-1].ID != cert.ID || c.Proofs[proofIndex-1].DescendantCertificateID != cert.ID || cert.RetirementProof != proofIndex ||
		cert.AbsenceProof < 1 || cert.AbsenceProof >= proofIndex || cert.PriorScan < 1 || cert.PriorScan > len(c.Scans) ||
		len(cert.Chain) < 2 || len(cert.Chain) > MaxProcesses {
		return false
	}
	retirement, absence := c.Proofs[proofIndex-1], c.Proofs[cert.AbsenceProof-1]
	leaf := cert.Chain[0]
	if retirement.StartTicks == nil || leaf.PID != retirement.PID || leaf.StartTicks != *retirement.StartTicks || leaf.DeclaredRoot ||
		cert.PriorScan >= retirement.ScanIndex || absence.Kind != "enumerated_pid_absent" || absence.Method != "pidfd_no_pid" ||
		absence.StartTicks != nil || absence.PID != leaf.PID || absence.ScanIndex != retirement.ScanIndex || absence.OffsetMS != retirement.OffsetMS {
		return false
	}
	seen := map[int]bool{}
	for i, v := range cert.Chain {
		if !validCensusIdentity(v, controller) || v.Classification != "managed" || seen[v.PID] || !sameInheritedTuple(leaf, v) || rows[cert.PriorScan][v.PID] != v {
			return false
		}
		seen[v.PID] = true
		if v.DeclaredRoot != (i == len(cert.Chain)-1) {
			return false
		}
		if i+1 < len(cert.Chain) && v.PPID != cert.Chain[i+1].PID {
			return false
		}
		if i > 0 {
			for scan := cert.PriorScan + 1; scan <= len(c.Scans); scan++ {
				if rows[scan][v.PID] != v {
					return false
				}
			}
		}
	}
	return true
}
