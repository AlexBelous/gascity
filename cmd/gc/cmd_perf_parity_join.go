package main

// gc perf parity-join is the WD parity campaign's join tool (DETECTOR.md
// section 3b). It joins legacy reconciler trace records against detector-shadow
// records and reports, per detector family, how far the shadow sweep tracks the
// god-function it is replacing. It is deleted with the rest of the D4-retained
// perf CLI at WE (DETECTOR.md section 5).
//
// Join contract (section 3b): the shared trace-cycle handle (trace_id, tick_id)
// plus the normalized session name, cross-checked on session_bead_id, with
// records distinguished by fields.effect_owner as legacy, detector-shadow, or
// keyed. The handle is an equality join, not a window, because section 2 runs
// the sweep inside beadReconcileTick beside the legacy call — no new loop,
// timer, or goroutine, therefore no cadence skew to reconcile. The one family
// that is genuinely time-skewed, D-DRAIN, surfaces as a legacy-only record in
// one cycle and a detector-only record in the next, which section 3b already
// names (ack-timing skew) and this tool triages as such.

import (
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

const (
	parityJoinSchemaV1      = "gascity.reconciler-parity-join.v1"
	parityJoinDefaultBar    = 0.995
	parityJoinDefaultSample = 10

	parityJoinOwnerLegacy         = "legacy"
	parityJoinOwnerDetectorShadow = "detector-shadow"
	parityJoinOwnerKeyed          = "keyed"
)

type parityJoinOptions struct {
	Bar      float64
	Samples  int
	Template string
}

type parityJoinCycleStats struct {
	Scanned              int `json:"scanned"`
	Considered           int `json:"considered"`
	ExcludedRecordBudget int `json:"excluded_record_budget_exceeded"`
	ExcludedNoRollup     int `json:"excluded_no_cycle_rollup"`
	WithoutDetailArms    int `json:"without_detail_arms"`
	UnownedRecords       int `json:"unowned_records"`
	LegacyByElimination  int `json:"legacy_by_elimination"`
}

// Dispositions for an effect_owner-absent record. Exactly one is an
// attribution; the rest are refusals, each carrying why.
const (
	parityJoinAbsenceLegacy         = "legacy"
	parityJoinAbsencePhaseMarker    = "phase_marker"
	parityJoinAbsenceKeyedSeamYield = "keyed_seam_yield"
	parityJoinAbsenceUnattributable = "unattributable"
	parityJoinAbsenceNoSessionKey   = "no_session_key"
)

// parityJoinAbsenceEntry is one (site, reason, disposition) group of records
// that carried no effect_owner stamp. Every owner-absent record lands in
// exactly one group, so the readout accounts for all of them.
type parityJoinAbsenceEntry struct {
	Site        TraceSiteCode   `json:"site_code"`
	Reason      TraceReasonCode `json:"reason_code,omitempty"`
	Disposition string          `json:"disposition"`
	Note        string          `json:"note,omitempty"`
	Count       int             `json:"count"`
}

type parityJoinFamilyReport struct {
	Family       string          `json:"family"`
	Level        parityJoinLevel `json:"level"`
	Joined       int             `json:"joined"`
	LegacyOnly   int             `json:"legacy_only"`
	DetectorOnly int             `json:"detector_only"`
	Keyed        int             `json:"keyed"`
	Matched      int             `json:"matched"`
	Mismatched   int             `json:"mismatched"`
	Incomparable int             `json:"incomparable"`
	Unclassified int             `json:"unclassified"`
	MatchRate    float64         `json:"match_rate"`
	BarMet       bool            `json:"bar_met"`
}

type parityJoinTriageEntry struct {
	Family         string `json:"family"`
	Class          string `json:"class"`
	Classification string `json:"classification"`
	Count          int    `json:"count"`
}

type parityJoinSample struct {
	Family          string         `json:"family"`
	Site            TraceSiteCode  `json:"site_code"`
	Side            parityJoinSide `json:"side"`
	TraceID         string         `json:"trace_id"`
	TickID          string         `json:"tick_id"`
	SessionName     string         `json:"session_name"`
	SessionBeadID   string         `json:"session_bead_id,omitempty"`
	LegacyReason    string         `json:"legacy_reason,omitempty"`
	LegacyOutcome   string         `json:"legacy_outcome,omitempty"`
	DetectorReason  string         `json:"detector_reason,omitempty"`
	DetectorOutcome string         `json:"detector_outcome,omitempty"`
}

type parityJoinReport struct {
	SchemaVersion          string                   `json:"schema_version"`
	Bar                    float64                  `json:"bar"`
	Cycles                 parityJoinCycleStats     `json:"cycles"`
	Families               []parityJoinFamilyReport `json:"families"`
	Triage                 []parityJoinTriageEntry  `json:"triage"`
	OwnerAbsence           []parityJoinAbsenceEntry `json:"owner_absence,omitempty"`
	Unclassified           []parityJoinSample       `json:"unclassified,omitempty"`
	ShadowEffectViolations int                      `json:"shadow_effect_violations"`
	NoEvidence             bool                     `json:"no_evidence"`
	BarMet                 bool                     `json:"bar_met"`
	WEBlocker              bool                     `json:"we_blocker"`
}

// newPerfParityJoinCmd builds the hidden `gc perf parity-join` subcommand.
func newPerfParityJoinCmd(stdout io.Writer) *cobra.Command {
	var traceDir, since, template string
	var bar float64
	var samples int
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "parity-join",
		Short: "Join legacy and detector-shadow reconciler traces (WD campaign; removed at WE)",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			filter := TraceFilter{}
			if strings.TrimSpace(since) != "" {
				window, err := time.ParseDuration(since)
				if err != nil {
					return fmt.Errorf("gc perf parity-join: parsing --since: %w", err)
				}
				filter.Since = time.Now().UTC().Add(-window)
			}
			records, err := ReadTraceRecords(traceDir, filter)
			if err != nil {
				return fmt.Errorf("gc perf parity-join: reading trace store %q: %w", traceDir, err)
			}
			report := buildParityJoinReport(records, parityJoinOptions{Bar: bar, Samples: samples, Template: template})
			if jsonOut {
				err = writeCLIJSONLine(stdout, report)
			} else {
				err = writeParityJoinReport(stdout, report)
			}
			if err != nil {
				return fmt.Errorf("gc perf parity-join: writing readout: %w", err)
			}
			return parityJoinVerdictError(report)
		},
	}
	cmd.Flags().StringVar(&traceDir, "trace-dir", "", "session-reconciler-trace store directory (the one holding segments/)")
	cmd.Flags().StringVar(&since, "since", "", "only join records newer than this duration ago (e.g. 168h)")
	cmd.Flags().StringVar(&template, "template", "", "only join records for this normalized template selector")
	cmd.Flags().Float64Var(&bar, "bar", parityJoinDefaultBar, "section 3b must-match bar per family")
	cmd.Flags().IntVar(&samples, "samples", parityJoinDefaultSample, "unclassified mismatch samples to report")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit versioned JSON instead of the table")
	_ = cmd.MarkFlagRequired("trace-dir")
	return cmd
}

func parityJoinVerdictError(report parityJoinReport) error {
	switch {
	case report.NoEvidence:
		return fmt.Errorf("gc perf parity-join: no evidence — %d cycles scanned, zero joined; an unarmed window records nothing durable", report.Cycles.Scanned)
	case report.WEBlocker:
		return fmt.Errorf("gc perf parity-join: WE blocker — %d unclassified mismatches, %d shadow effect violations", len(report.Unclassified), report.ShadowEffectViolations)
	default:
		return nil
	}
}

type parityJoinCycleKey struct{ TraceID, TickID string }

type parityJoinCycleBucket struct {
	rollup  *SessionReconcilerTraceRecord
	records []SessionReconcilerTraceRecord
}

type parityJoinRowKey struct {
	Site    TraceSiteCode
	Session string
}

type parityJoinAccumulator struct {
	rows    map[string]*parityJoinFamilyReport
	triage  map[parityJoinTriageEntry]int
	absence map[parityJoinAbsenceEntry]int
	samples []parityJoinSample
	opts    parityJoinOptions
	report  *parityJoinReport
}

// buildParityJoinReport joins one trace corpus per the section 3b contract.
func buildParityJoinReport(records []SessionReconcilerTraceRecord, opts parityJoinOptions) parityJoinReport {
	if opts.Bar <= 0 {
		opts.Bar = parityJoinDefaultBar
	}
	if opts.Samples <= 0 {
		opts.Samples = parityJoinDefaultSample
	}
	report := parityJoinReport{SchemaVersion: parityJoinSchemaV1, Bar: opts.Bar}
	acc := &parityJoinAccumulator{
		rows:    make(map[string]*parityJoinFamilyReport, len(parityJoinFamilySpecs)),
		triage:  make(map[parityJoinTriageEntry]int),
		absence: make(map[parityJoinAbsenceEntry]int),
		opts:    opts,
		report:  &report,
	}
	for i := range parityJoinFamilySpecs {
		spec := &parityJoinFamilySpecs[i]
		acc.rows[spec.Family] = &parityJoinFamilyReport{Family: spec.Family, Level: spec.Level}
	}

	for _, key := range parityJoinCycles(records, &report.Cycles) {
		acc.joinCycle(key.key, key.bucket)
	}

	report.Families = make([]parityJoinFamilyReport, 0, len(parityJoinFamilySpecs))
	joined := 0
	for i := range parityJoinFamilySpecs {
		row := *acc.rows[parityJoinFamilySpecs[i].Family]
		comparable := row.Matched + row.Mismatched
		if comparable > 0 {
			row.MatchRate = float64(row.Matched) / float64(comparable)
			row.BarMet = row.MatchRate >= opts.Bar
		}
		joined += row.Joined
		report.Families = append(report.Families, row)
	}
	report.Triage = parityJoinTriageLog(acc.triage)
	report.OwnerAbsence = parityJoinAbsenceLog(acc.absence)
	report.Unclassified = acc.samples
	// The evidence a section 3b readout rests on is JOINED rows. A corpus can be
	// full of owned records and still join nothing — that was exactly the day-0
	// campaign corpus, which reported no_evidence=false beside joined=0 in every
	// family. Counting owned records instead of joined ones made the flag read
	// backwards on the one case it exists to catch.
	report.NoEvidence = joined == 0
	report.WEBlocker = len(acc.samples) > 0 || report.ShadowEffectViolations > 0
	report.BarMet = !report.NoEvidence && !report.WEBlocker
	for _, row := range report.Families {
		if row.Matched+row.Mismatched > 0 && !row.BarMet {
			report.BarMet = false
		}
	}
	return report
}

type parityJoinOrderedCycle struct {
	key    parityJoinCycleKey
	bucket *parityJoinCycleBucket
}

// parityJoinCycles buckets records by cycle handle and applies the section 3b
// exclusion rules: a cycle whose rollup reports record_budget_exceeded drops is
// dropped from the readout, and so is a cycle whose rollup never landed (its
// evidence is truncated, and counting it would understate divergence).
func parityJoinCycles(records []SessionReconcilerTraceRecord, stats *parityJoinCycleStats) []parityJoinOrderedCycle {
	buckets := make(map[parityJoinCycleKey]*parityJoinCycleBucket)
	order := make([]parityJoinCycleKey, 0, len(records))
	for i := range records {
		rec := records[i]
		key := parityJoinCycleKey{TraceID: rec.TraceID, TickID: rec.TickID}
		bucket, ok := buckets[key]
		if !ok {
			bucket = &parityJoinCycleBucket{}
			buckets[key] = bucket
			order = append(order, key)
		}
		if rec.RecordType == TraceRecordCycleResult {
			bucket.rollup = &rec
			continue
		}
		bucket.records = append(bucket.records, rec)
	}

	kept := make([]parityJoinOrderedCycle, 0, len(order))
	for _, key := range order {
		bucket := buckets[key]
		stats.Scanned++
		switch {
		case bucket.rollup == nil:
			stats.ExcludedNoRollup++
		case parityJoinDropCount(bucket.rollup, "record_budget_exceeded") > 0:
			stats.ExcludedRecordBudget++
		default:
			stats.Considered++
			if parityJoinRollupCount(bucket.rollup, "detailed_template_count") == 0 {
				stats.WithoutDetailArms++
			}
			kept = append(kept, parityJoinOrderedCycle{key: key, bucket: bucket})
		}
	}
	return kept
}

// parityJoinRollupCount reads a cycle-rollup counter from rec.Fields, which is
// where the collector writes every rollup counter
// (session_reconciler_trace_collector.go:970-983, serialized as the nested
// "fields" object). The typed mirrors on the record struct are NOT all
// maintained — nothing in the tree assigns DetailedTemplateCount — so a reader
// of the typed field sees zero on every real rollup. Values arrive as float64
// once the store has round-tripped them through JSON.
func parityJoinRollupCount(rec *SessionReconcilerTraceRecord, key string) int {
	if rec == nil {
		return 0
	}
	return parityJoinInt(rec.Fields[key])
}

// parityJoinDropCount reads one drop-reason counter. This is the one rollup
// counter the collector also mirrors onto a typed field, so either copy is
// authoritative; the Fields copy is a map[string]int in memory and a
// map[string]any once decoded.
func parityJoinDropCount(rec *SessionReconcilerTraceRecord, reason string) int {
	if rec == nil {
		return 0
	}
	if count, ok := rec.DropReasonCounts[reason]; ok {
		return count
	}
	switch counts := rec.Fields["drop_reason_counts"].(type) {
	case map[string]int:
		return counts[reason]
	case map[string]any:
		return parityJoinInt(counts[reason])
	default:
		return 0
	}
}

// parityJoinInt reads a rollup counter in either shape it occurs in: the int the
// collector wrote, or the float64 it becomes once the store has decoded it.
func parityJoinInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	default:
		return 0
	}
}

func (a *parityJoinAccumulator) joinCycle(key parityJoinCycleKey, bucket *parityJoinCycleBucket) {
	legacy := make(map[parityJoinRowKey][]SessionReconcilerTraceRecord)
	shadow := make(map[parityJoinRowKey][]SessionReconcilerTraceRecord)
	rowOrder := make([]parityJoinRowKey, 0, len(bucket.records))
	seen := make(map[parityJoinRowKey]bool, len(bucket.records))

	for _, rec := range bucket.records {
		spec, ok := parityJoinSiteFamily[rec.SiteCode]
		if !ok {
			continue
		}
		if !traceTemplateMatches(rec.Template, a.opts.Template) {
			continue
		}
		owner, absence := parityJoinRecordOwner(rec)
		if absence != nil {
			a.absence[*absence]++
			if absence.Disposition != parityJoinAbsenceLegacy {
				a.report.Cycles.UnownedRecords++
				continue
			}
			a.report.Cycles.LegacyByElimination++
		}
		row := parityJoinRowKey{Site: rec.SiteCode, Session: parityJoinSessionKey(rec)}
		if !seen[row] {
			seen[row] = true
			rowOrder = append(rowOrder, row)
		}
		switch owner {
		case parityJoinOwnerLegacy:
			legacy[row] = append(legacy[row], rec)
		case parityJoinOwnerDetectorShadow:
			if parityJoinEffectApplied(rec) {
				a.report.ShadowEffectViolations++
			}
			shadow[row] = append(shadow[row], rec)
		case parityJoinOwnerKeyed:
			a.rows[spec.Family].Keyed++
		default:
			a.report.Cycles.UnownedRecords++
		}
	}

	for _, row := range rowOrder {
		left, right := legacy[row], shadow[row]
		spec := parityJoinSiteFamily[row.Site]
		for i := 0; i < len(left) || i < len(right); i++ {
			var legacyRec, shadowRec *SessionReconcilerTraceRecord
			if i < len(left) {
				legacyRec = &left[i]
			}
			if i < len(right) {
				shadowRec = &right[i]
			}
			a.classify(key, spec, row, legacyRec, shadowRec)
		}
	}
}

func (a *parityJoinAccumulator) classify(
	cycle parityJoinCycleKey,
	spec *parityJoinFamilySpec,
	row parityJoinRowKey,
	legacyRec, shadowRec *SessionReconcilerTraceRecord,
) {
	stats := a.rows[spec.Family]
	side := parityJoinSideBoth
	switch {
	case shadowRec == nil:
		side = parityJoinSideLegacyOnly
		stats.LegacyOnly++
	case legacyRec == nil:
		side = parityJoinSideDetectorOnly
		stats.DetectorOnly++
	default:
		stats.Joined++
	}

	classification, class := parityJoinClassify(spec, side, legacyRec, shadowRec)
	switch classification {
	case parityJoinMatched:
		stats.Matched++
		return
	case parityJoinIncomparable:
		stats.Incomparable++
	default:
		stats.Mismatched++
	}
	a.triage[parityJoinTriageEntry{Family: spec.Family, Class: class, Classification: classification}]++
	if class != parityJoinClassUnclassified {
		return
	}
	stats.Unclassified++
	if len(a.samples) < a.opts.Samples {
		a.samples = append(a.samples, parityJoinSampleOf(cycle, spec.Family, row, side, legacyRec, shadowRec))
	}
}

// parityJoinClassify applies the section 3b classification table to one joined
// row: matched, mismatched, or incomparable, plus the triage class.
func parityJoinClassify(
	spec *parityJoinFamilySpec,
	side parityJoinSide,
	legacyRec, shadowRec *SessionReconcilerTraceRecord,
) (string, string) {
	if side == parityJoinSideBoth {
		if !parityJoinBeadIDsAgree(*legacyRec, *shadowRec) {
			return parityJoinIncomparable, parityJoinClassBeadIDCrossCheck
		}
		// Detection-level families predict only (key, condition); the decision
		// arms are handler-side and deliberately not compared.
		if spec.Level == parityJoinLevelDetection {
			return parityJoinMatched, ""
		}
		if legacyRec.ReasonCode == shadowRec.ReasonCode && legacyRec.OutcomeCode == shadowRec.OutcomeCode {
			return parityJoinMatched, ""
		}
	}
	for _, rule := range slices.Concat(spec.Divergences, parityJoinGlobalDivergences) {
		if !parityJoinRuleMatches(rule, side, legacyRec, shadowRec) {
			continue
		}
		if rule.Classification != "" {
			return rule.Classification, rule.Class
		}
		return parityJoinMismatched, rule.Class
	}
	return parityJoinMismatched, parityJoinClassUnclassified
}

func parityJoinRuleMatches(
	rule parityJoinDivergenceRule,
	side parityJoinSide,
	legacyRec, shadowRec *SessionReconcilerTraceRecord,
) bool {
	if rule.Side != "" && rule.Side != side {
		return false
	}
	site := TraceSiteUnknown
	if legacyRec != nil {
		site = legacyRec.SiteCode
	} else if shadowRec != nil {
		site = shadowRec.SiteCode
	}
	if len(rule.Sites) > 0 && !slices.Contains(rule.Sites, site) {
		return false
	}
	if !parityJoinCodesMatch(legacyRec, rule.LegacyReasons, rule.LegacyOutcomes) {
		return false
	}
	if !parityJoinCodesMatch(shadowRec, rule.DetectorReasons, rule.DetectorOutcomes) {
		return false
	}
	if len(rule.AnyReasons) > 0 && !parityJoinAnyReason(rule.AnyReasons, legacyRec, shadowRec) {
		return false
	}
	if len(rule.AnyOutcomes) > 0 && !parityJoinAnyOutcome(rule.AnyOutcomes, legacyRec, shadowRec) {
		return false
	}
	return true
}

func parityJoinCodesMatch(rec *SessionReconcilerTraceRecord, reasons []TraceReasonCode, outcomes []TraceOutcomeCode) bool {
	if len(reasons) == 0 && len(outcomes) == 0 {
		return true
	}
	if rec == nil {
		return false
	}
	if len(reasons) > 0 && !slices.Contains(reasons, rec.ReasonCode) {
		return false
	}
	if len(outcomes) > 0 && !slices.Contains(outcomes, rec.OutcomeCode) {
		return false
	}
	return true
}

func parityJoinAnyReason(reasons []TraceReasonCode, recs ...*SessionReconcilerTraceRecord) bool {
	for _, rec := range recs {
		if rec != nil && slices.Contains(reasons, rec.ReasonCode) {
			return true
		}
	}
	return false
}

func parityJoinAnyOutcome(outcomes []TraceOutcomeCode, recs ...*SessionReconcilerTraceRecord) bool {
	for _, rec := range recs {
		if rec != nil && slices.Contains(outcomes, rec.OutcomeCode) {
			return true
		}
	}
	return false
}

// parityJoinBeadIDsAgree is the section 3b cross-check on the normalized
// session-name join key. Two rows that share a name but not an identity are a
// D-DUP shape, not a comparable pair.
func parityJoinBeadIDsAgree(left, right SessionReconcilerTraceRecord) bool {
	if left.SessionBeadID == "" || right.SessionBeadID == "" {
		return true
	}
	return left.SessionBeadID == right.SessionBeadID
}

func parityJoinSessionKey(rec SessionReconcilerTraceRecord) string {
	if name := strings.TrimSpace(rec.SessionName); name != "" {
		return name
	}
	if name, ok := rec.Fields["session_name"].(string); ok && strings.TrimSpace(name) != "" {
		return strings.TrimSpace(name)
	}
	return strings.TrimSpace(rec.SessionBeadID)
}

// parityJoinRecordOwner resolves which population wrote a record. A stamped
// record is taken at its word. An unstamped one is classified by ELIMINATION:
// every keyed handler stamps "keyed" and the sweep stamps "detector-shadow" (or
// "keyed" for a condition it routed), so at a site where the god function is
// the remaining writer, absence IS the legacy signature.
//
// Elimination beats teaching the god function to stamp for two reasons. The
// stamp would be scaffolding threaded through ~19 legacy decision sites in a
// function scheduled for deletion at WE, and it would need to survive every
// remaining WD slice; the classification rule is one function that dies with
// the tool. And it is sound where a log-line census would not be: a log line
// carries no engine attribution — the correction recorded on ga-ij8mh turned on
// exactly that, a legacy-looking line that no engine could be pinned to — while
// a trace record carries the site code and the stamp its keyed and detector
// writers already emit, which is enough to eliminate them.
//
// The guard is that absence only attributes inside the section 1 legacy
// vocabulary. A phase site's per-cycle marker, a coexistence-seam yield whose
// effect belongs to the keyed population, a keyed-owned or shared-writer site,
// and a record with no session identity to join on are each refused with the
// reason, counted, and surfaced — never silently binned as legacy.
func parityJoinRecordOwner(rec SessionReconcilerTraceRecord) (string, *parityJoinAbsenceEntry) {
	stamped, _ := rec.Fields["effect_owner"].(string)
	if owner := strings.TrimSpace(stamped); owner != "" {
		return owner, nil
	}
	disposition := parityJoinSiteDispositions[rec.SiteCode]
	absence := &parityJoinAbsenceEntry{Site: rec.SiteCode, Reason: rec.ReasonCode, Note: disposition.Note}
	switch {
	case disposition.Attribution == parityJoinSitePhase:
		absence.Disposition = parityJoinAbsencePhaseMarker
	case disposition.Attribution != parityJoinSiteLegacy:
		absence.Disposition = parityJoinAbsenceUnattributable
	case parityJoinKeyedSeamYieldReasons[rec.ReasonCode]:
		absence.Disposition = parityJoinAbsenceKeyedSeamYield
		absence.Note = "legacy yielded this key to the keyed population; the effect is keyed's"
	case parityJoinSessionKey(rec) == "":
		absence.Disposition = parityJoinAbsenceNoSessionKey
		absence.Note = "no session identity: not a row in a per-session join"
	default:
		absence.Disposition = parityJoinAbsenceLegacy
		return parityJoinOwnerLegacy, absence
	}
	return "", absence
}

func parityJoinAbsenceLog(counts map[parityJoinAbsenceEntry]int) []parityJoinAbsenceEntry {
	log := make([]parityJoinAbsenceEntry, 0, len(counts))
	for entry, count := range counts {
		entry.Count = count
		log = append(log, entry)
	}
	sort.Slice(log, func(i, j int) bool {
		if log[i].Site != log[j].Site {
			return log[i].Site < log[j].Site
		}
		return log[i].Reason < log[j].Reason
	})
	return log
}

func parityJoinEffectApplied(rec SessionReconcilerTraceRecord) bool {
	applied, _ := rec.Fields["effect_applied"].(bool)
	return applied
}

func parityJoinSampleOf(
	cycle parityJoinCycleKey,
	family string,
	row parityJoinRowKey,
	side parityJoinSide,
	legacyRec, shadowRec *SessionReconcilerTraceRecord,
) parityJoinSample {
	sample := parityJoinSample{
		Family:      family,
		Site:        row.Site,
		Side:        side,
		TraceID:     cycle.TraceID,
		TickID:      cycle.TickID,
		SessionName: row.Session,
	}
	if legacyRec != nil {
		sample.SessionBeadID = legacyRec.SessionBeadID
		sample.LegacyReason = string(legacyRec.ReasonCode)
		sample.LegacyOutcome = string(legacyRec.OutcomeCode)
	}
	if shadowRec != nil {
		if sample.SessionBeadID == "" {
			sample.SessionBeadID = shadowRec.SessionBeadID
		}
		sample.DetectorReason = string(shadowRec.ReasonCode)
		sample.DetectorOutcome = string(shadowRec.OutcomeCode)
	}
	return sample
}

func parityJoinTriageLog(counts map[parityJoinTriageEntry]int) []parityJoinTriageEntry {
	log := make([]parityJoinTriageEntry, 0, len(counts))
	for entry, count := range counts {
		entry.Count = count
		log = append(log, entry)
	}
	sort.Slice(log, func(i, j int) bool {
		if log[i].Family != log[j].Family {
			return log[i].Family < log[j].Family
		}
		return log[i].Class < log[j].Class
	})
	return log
}

// writeParityJoinReport renders the human readout.
func writeParityJoinReport(w io.Writer, report parityJoinReport) error {
	var b strings.Builder
	fmt.Fprintf(&b, "\ngc perf parity-join (DETECTOR.md 3b, bar %.3f)\n\n", report.Bar)
	fmt.Fprintf(&b, "cycles: %d scanned, %d considered, %d excluded (record_budget_exceeded=%d, no_rollup=%d)\n",
		report.Cycles.Scanned, report.Cycles.Considered,
		report.Cycles.ExcludedRecordBudget+report.Cycles.ExcludedNoRollup,
		report.Cycles.ExcludedRecordBudget, report.Cycles.ExcludedNoRollup)
	fmt.Fprintf(&b, "arms:   %d considered cycles carried no detail arm\n", report.Cycles.WithoutDetailArms)
	fmt.Fprintf(&b, "owner:  %d unstamped records classified legacy by elimination; %d refused (see OWNER-ABSENT)\n\n",
		report.Cycles.LegacyByElimination, report.Cycles.UnownedRecords)

	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "FAMILY\tLEVEL\tJOINED\tLEG-ONLY\tDET-ONLY\tKEYED\tMATCH\tMISMATCH\tINCOMP\tUNCLASS\tRATE\tBAR") //nolint:errcheck
	for _, row := range report.Families {
		rate := "-"
		if row.Matched+row.Mismatched > 0 {
			rate = fmt.Sprintf("%.2f%%", row.MatchRate*100)
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%s\t%s\n", //nolint:errcheck
			row.Family, row.Level, row.Joined, row.LegacyOnly, row.DetectorOnly, row.Keyed,
			row.Matched, row.Mismatched, row.Incomparable, row.Unclassified, rate,
			parityJoinBarCell(row))
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	if len(report.Triage) > 0 {
		b.WriteString("\nTRIAGE\n")
		for _, entry := range report.Triage {
			fmt.Fprintf(&b, "  %-14s %-40s %-12s %d\n", entry.Family, entry.Class, entry.Classification, entry.Count)
		}
	}
	if len(report.OwnerAbsence) > 0 {
		b.WriteString("\nOWNER-ABSENT (records carrying no effect_owner stamp)\n")
		for _, entry := range report.OwnerAbsence {
			fmt.Fprintf(&b, "  %-44s %-26s %-18s %6d  %s\n",
				entry.Site, entry.Reason, entry.Disposition, entry.Count, entry.Note)
		}
	}
	for _, sample := range report.Unclassified {
		fmt.Fprintf(&b, "  unclassified: %s %s %s session=%s bead=%s legacy=(%s,%s) detector=(%s,%s) cycle=%s/%s\n",
			sample.Family, sample.Site, sample.Side, sample.SessionName, sample.SessionBeadID,
			sample.LegacyReason, sample.LegacyOutcome, sample.DetectorReason, sample.DetectorOutcome,
			sample.TraceID, sample.TickID)
	}
	fmt.Fprintf(&b, "\nRESULT: %s\n", parityJoinVerdict(report))
	_, err := io.WriteString(w, b.String())
	return err
}

func parityJoinBarCell(row parityJoinFamilyReport) string {
	switch {
	case row.Matched+row.Mismatched == 0:
		return "no-data"
	case row.BarMet:
		return "ok"
	default:
		return "BELOW"
	}
}

func parityJoinVerdict(report parityJoinReport) string {
	switch {
	case report.NoEvidence:
		return "NO EVIDENCE — zero joined records; an unarmed window records nothing durable"
	case report.ShadowEffectViolations > 0:
		return fmt.Sprintf("WE BLOCKER — %d detector-shadow records claimed an applied effect", report.ShadowEffectViolations)
	case len(report.Unclassified) > 0:
		return fmt.Sprintf("WE BLOCKER — unclassified mismatches: %d; triage them into the 3b table", len(report.Unclassified))
	case !report.BarMet:
		return fmt.Sprintf("BELOW BAR — a must-match family is under %.3f", report.Bar)
	default:
		return "PASS"
	}
}
