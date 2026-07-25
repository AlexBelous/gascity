package beads

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

var (
	_ SnapshotExporter = (*BdStore)(nil)
	_ SnapshotImporter = (*BdStore)(nil)
	_ SnapshotFetcher  = (*BdStore)(nil)
)

// BDImportRunnerFunc runs `bd import` with JSONL piped to stdin, in dir with the
// given env. It mirrors PurgeRunnerFunc's env-aware shape and is injectable so
// tests can assert the import argv, the inherited env, and capability gating
// without a real bd.
type BDImportRunnerFunc func(dir string, env []string, stdin []byte, args ...string) ([]byte, error)

// SetBDImportRunner overrides the default exec-based bd import implementation.
// Used in tests to inject a fake runner.
func (s *BdStore) SetBDImportRunner(fn BDImportRunnerFunc) {
	s.importRunner = fn
}

// exportOwnerExcludeKeys are the bd config keys that silently drop owner-keyed
// beads from `bd export`. A migration must not run against a scope where either
// is set, or copy-verify would be blind to the excluded rows.
var exportOwnerExcludeKeys = []string{"export.exclude_owners", "export.exclude_owner"}

// ExportBeadSnapshots sources raw bead snapshots by shelling `bd export`, the
// canonical full-fidelity pair to `bd import`. Plain `bd export` ships every
// field and every status (closed rows included), excluding memories, templates,
// ephemeral wisps, and infra beads by default — the durable backlog the
// migration then filters to ClassWork. With opts.IncludeEphemeral it shells
// `bd export --all` (the only flag that crosses the ephemeral tier) and relies on
// decode-side filtering (DecodeSnapshot skips memories, templates, and the
// schema header; the migration's Classify pass drops infra), so wisp molecules
// in flight at unify time are not stranded. Memory lines and tombstones are
// skipped during decode, matching bd import.
//
// Preflight: a scope configured with export.exclude_owners (or the legacy
// export.exclude_owner) would drop owner-keyed beads from BOTH the stream and the
// bd-leg copy-verify, silently. `bd export` offers no bypass flag, so this leg
// refuses rather than migrate a blind stream.
func (s *BdStore) ExportBeadSnapshots(_ context.Context, opts ExportOptions) ([]Snapshot, error) {
	if err := s.checkExportOwnerExcludeUnset(); err != nil {
		return nil, err
	}
	args := []string{"export"}
	if opts.IncludeEphemeral {
		args = append(args, "--all")
	}
	out, err := s.runBDTransientRead(args...)
	if err != nil {
		return nil, fmt.Errorf("bd export: %w", err)
	}
	snaps, err := DecodeSnapshots(out)
	if err != nil {
		return nil, fmt.Errorf("bd export: %w", err)
	}
	return snaps, nil
}

// checkExportOwnerExcludeUnset fails when a bd owner-exclude config is set.
func (s *BdStore) checkExportOwnerExcludeUnset() error {
	for _, key := range exportOwnerExcludeKeys {
		out, err := s.runner(s.dir, "bd", "config", "get", key)
		if err != nil {
			// An unset key or a bd that predates the key surfaces an error; treat
			// as not-configured (the safe reading — a set value prints on stdout).
			continue
		}
		value := strings.TrimSpace(string(out))
		if value == "" || strings.Contains(value, "(not set") {
			continue
		}
		return fmt.Errorf("bd export: %s = %q: %w", key, value, ErrExportOwnerExcludeConfigured)
	}
	return nil
}

// GetBeadSnapshots reads raw snapshots for the given ids via `bd show --json`,
// the batched per-id read the copy-verify and remote stamp-check probes need
// (never a full-store export). Ids not present are omitted (bd show warns on
// stderr and returns the found subset); an all-missing lookup returns empty.
// The returned raw shape is bd show's bare issue object (no _type/counts
// wrapper), which DecodeSnapshot accepts and which carries the verify-relevant
// fields (status, closed_at, metadata, labels, dependencies).
func (s *BdStore) GetBeadSnapshots(_ context.Context, ids []string) ([]Snapshot, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := append([]string{"show", "--json"}, ids...)
	out, err := s.runBDTransientRead(args...)
	if err != nil {
		if isBdNotFound(err) {
			return nil, nil // none of the requested ids exist
		}
		return nil, fmt.Errorf("bd show: %w", err)
	}
	var rows []json.RawMessage
	if err := json.Unmarshal(extractJSON(unwrapBDJSONEnvelope(out)), &rows); err != nil {
		return nil, fmt.Errorf("bd show: parsing JSON: %s: %w", truncateRawOutput(out, 200), err)
	}
	snaps := make([]Snapshot, 0, len(rows))
	for _, row := range rows {
		snap, keep, err := DecodeSnapshot(row)
		if err != nil {
			return nil, fmt.Errorf("bd show: %w", err)
		}
		if keep {
			snaps = append(snaps, snap)
		}
	}
	return snaps, nil
}

// ImportBeadSnapshots applies snapshots through `bd import -` (JSONL on stdin),
// which has exactly the guarded-upsert semantics the copy primitive needs:
// verbatim clocks and status, an updated_at-guarded upsert, and a batch write
// path (CreateIssuesWithFullOptions) the hook decorator does not wrap, so no
// per-issue bd hooks fire for migrated history.
//
// ConflictSkip (--conflict-skip) and per-id AllowStaleIDs both require the
// prerequisite bd slice, probed via the fork-proof workspace-prefix-mint
// capability; against a bd that lacks it the option-carrying import returns
// ErrBdCapabilityMissing. ConflictSkip and AllowStaleIDs are mutually exclusive
// (ImportOptions.validate). bd import exposes only a global --allow-stale, so
// AllowStaleIDs is applied as a SECOND, bounded import of only those ids with
// --allow-stale — and, because that flag overwrites unconditionally, each flagged
// id is first probed against the destination (GetBeadSnapshots) and dropped from
// the forced set (reported StaleSkipped) when the destination has advanced
// strictly newer than the incoming snapshot, so a live destination write is not
// clobbered.
func (s *BdStore) ImportBeadSnapshots(ctx context.Context, snaps []Snapshot, opts ImportOptions) (ImportReport, error) {
	if err := opts.validate(); err != nil {
		return ImportReport{}, err
	}
	if len(snaps) == 0 {
		return ImportReport{}, nil
	}
	allowStale := opts.allowStaleSet()
	needsCapability := opts.ConflictSkip || len(allowStale) > 0
	if needsCapability {
		capable, err := s.hasWorkspacePrefixMintCapability()
		if err != nil {
			return ImportReport{}, fmt.Errorf("bd import: capability probe: %w", err)
		}
		if !capable {
			return ImportReport{}, fmt.Errorf("bd import: %w", ErrBdCapabilityMissing)
		}
	}

	// Scale the whole-import timeout with batch size, matching the native leg, so
	// a large durable backlog does not die at the flat 120s wall mid-import.
	cctx, cancel := context.WithTimeout(ctx, nativeImportDeadline(len(snaps)))
	defer cancel()

	// The guarded pass imports every snapshot NOT flagged for the stale-guard
	// override; the flagged ids get a dedicated conditional forced pass below.
	guarded := snaps
	var forced []Snapshot
	if len(allowStale) > 0 {
		guarded = guarded[:0:0]
		for _, snap := range snaps {
			if allowStale[snap.ID()] {
				forced = append(forced, snap)
			} else {
				guarded = append(guarded, snap)
			}
		}
	}

	report := ImportReport{}
	if len(guarded) > 0 {
		guardedArgs := []string{"import", "-", "--json"}
		if opts.ConflictSkip {
			guardedArgs = append(guardedArgs, "--conflict-skip")
		}
		result, err := s.runBDImport(cctx, marshalSnapshotsJSONL(guarded), guardedArgs...)
		if err != nil {
			return ImportReport{}, err
		}
		report = result.toReport()
	}

	if len(forced) > 0 {
		if err := s.applyForcedPass(cctx, forced, &report); err != nil {
			return ImportReport{}, err
		}
	}
	return report, nil
}

// applyForcedPass runs the conditional --allow-stale re-import for the flagged
// ids: it probes each id's destination clock and drops (reports StaleSkipped) any
// whose destination advanced strictly newer than the incoming snapshot, then
// force-imports the rest, classifying them Updated (they existed) or Inserted
// (absent on the destination).
func (s *BdStore) applyForcedPass(ctx context.Context, forced []Snapshot, report *ImportReport) error {
	ids := make([]string, len(forced))
	for i, snap := range forced {
		ids[i] = snap.ID()
	}
	destSnaps, err := s.GetBeadSnapshots(ctx, ids)
	if err != nil {
		return fmt.Errorf("bd import: probing destination for allow-stale: %w", err)
	}
	destAt := make(map[string]time.Time, len(destSnaps))
	for _, d := range destSnaps {
		destAt[d.ID()] = d.UpdatedAt().UTC().Truncate(time.Second)
	}

	var keep []Snapshot
	for _, snap := range forced {
		if stored, ok := destAt[snap.ID()]; ok {
			incoming := snap.UpdatedAt().UTC().Truncate(time.Second)
			if incoming.Before(stored) {
				// Destination advanced past the source: do not clobber.
				report.StaleSkipped = append(report.StaleSkipped, snap.ID())
				continue
			}
		}
		keep = append(keep, snap)
	}
	if len(keep) == 0 {
		return nil
	}
	result, err := s.runBDImport(ctx, marshalSnapshotsJSONL(keep), "import", "-", "--json", "--allow-stale")
	if err != nil {
		return err
	}
	for _, snap := range keep {
		if _, existed := destAt[snap.ID()]; existed {
			report.Updated = append(report.Updated, snap.ID())
		} else {
			report.Inserted = append(report.Inserted, snap.ID())
		}
	}
	for _, pair := range result.SkippedDependencies {
		issueID, dependsOnID := parseSkippedDependency(pair)
		report.addSkippedDependency(issueID, dependsOnID)
	}
	return nil
}

// HasWorkspacePrefixMintCapability reports whether the store's bd binary
// advertises the fork-proof workspace-prefix-mint capability. The topology
// migrations' capability gate calls it before any prefix-override mint or
// guarded-upsert option (ConflictSkip / AllowStaleIDs), refusing with a clear
// message that names the required bd when it is absent.
func (s *BdStore) HasWorkspacePrefixMintCapability() (bool, error) {
	return s.hasWorkspacePrefixMintCapability()
}

// ConfigAddToSet appends value to the database config-table set at key via the
// transactional `bd config add-to-set` primitive (the prerequisite bd slice) —
// a server-side read-modify-write that never clobbers a concurrent city's entry.
// The unify/remote config step uses it to union scope prefixes into
// allowed_prefixes. It runs through the store's scoped runner so the child sees
// the same BEADS_DIR/credentials every other bd call on this store gets.
func (s *BdStore) ConfigAddToSet(key, value string) error {
	if _, err := s.runner(s.dir, "bd", "config", "add-to-set", key, value); err != nil {
		return fmt.Errorf("bd config add-to-set %s %q: %w", key, value, err)
	}
	return nil
}

// ConfigGet reads the database config-table value at key via `bd config get`,
// through the store's scoped runner (so the child sees the same
// BEADS_DIR/credentials). The remote migration's allowed_prefixes self-heal
// reads the current set with it to detect a concurrent-city eviction. The
// returned value is the raw stdout, trimmed; an unset key surfaces bd's own
// "(not set)" text, which the caller treats as an empty set.
func (s *BdStore) ConfigGet(key string) (string, error) {
	out, err := s.runner(s.dir, "bd", "config", "get", key)
	if err != nil {
		return "", fmt.Errorf("bd config get %s: %w", key, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// CredentialPreflight runs one authenticated, BOUNDED probe through the store's
// scoped env so credentials reach the child. It is the remote migration's
// pre-copy auth gate: `bd list --json --limit 1` opens an authenticated
// connection and reads at most one row — proving the endpoint is reachable and
// the credentials resolve — WITHOUT the full org-DB pull Ping's `--limit 0`
// performs. When ctx carries a deadline it is ENFORCED on the subprocess
// (exec.CommandContext kills it), so an unreachable remote degrades in the ctx's
// window (the doctor path) rather than bd's flat 120s read timeout. A failure is
// returned verbatim so the caller can name the required credential env
// (BEADS_DOLT_CREDENTIAL_COMMAND / GC_DOLT_PASSWORD).
func (s *BdStore) CredentialPreflight(ctx context.Context) error {
	if _, err := s.runBoundedRead(ctx, "list", "--json", "--limit", "1"); err != nil {
		return fmt.Errorf("bd list --limit 1: %w", err)
	}
	return nil
}

// ConfigGetContext reads the database config-table value at key with a ctx
// deadline enforced on the subprocess (the doctor's bounded allowed_prefixes
// read). See ConfigGet for the unbounded background variant.
func (s *BdStore) ConfigGetContext(ctx context.Context, key string) (string, error) {
	out, err := s.runBoundedRead(ctx, "config", "get", key)
	if err != nil {
		return "", fmt.Errorf("bd config get %s: %w", key, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// runBoundedRead runs a read-only bd command with the store's scoped env. A ctx
// WITH a deadline is enforced on the subprocess via exec.CommandContext (real
// cancellation for the doctor path); WITHOUT a deadline it falls back to the
// retrying, fake-able scoped runner (the boot path, unit-testable via s.runner).
func (s *BdStore) runBoundedRead(ctx context.Context, args ...string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		return s.runBDTransientRead(args...)
	}
	return execBDRead(ctx, s.dir, bdImportChildEnv(s.env), args...)
}

// execBDRead runs a read-only `bd` command (no stdin) under ctx via
// exec.CommandContext, so a ctx deadline actually kills the subprocess.
func execBDRead(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, "bd", args...)
	cmd.Dir = dir
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	start := time.Now()
	runErr := cmd.Run()
	TraceBDCall("go:bdstore.execBDRead", dir, args, start, bdExitCode(runErr), runErr)
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("timed out")
	}
	if runErr != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = bdStdoutErrorDetail(stdout.Bytes())
		}
		if detail != "" {
			return nil, fmt.Errorf("%w: %s", runErr, detail)
		}
		return nil, runErr
	}
	return stdout.Bytes(), nil
}

// hasWorkspacePrefixMintCapability probes `bd version --json` for the
// workspace-prefix-mint capability. The probe is fork-proof (a capabilities key,
// never a version-number comparison) because this fleet pins forked bd builds.
func (s *BdStore) hasWorkspacePrefixMintCapability() (bool, error) {
	out, err := s.runner(s.dir, "bd", "version", "--json")
	if err != nil {
		return false, fmt.Errorf("bd version: %w", err)
	}
	var payload struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := json.Unmarshal(extractJSON(unwrapBDJSONEnvelope(out)), &payload); err != nil {
		return false, fmt.Errorf("bd version: parsing capabilities: %w", err)
	}
	for _, capability := range payload.Capabilities {
		if strings.TrimSpace(capability) == snapshotWorkspacePrefixMintCapability {
			return true, nil
		}
	}
	return false, nil
}

// bdImportResult is the machine JSON `bd import --json` emits.
type bdImportResult struct {
	Created             int              `json:"created"`
	Updated             int              `json:"updated"`
	Skipped             int              `json:"skipped"`
	IDs                 []string         `json:"ids"`
	UpdatedIssues       []bdImportChange `json:"updated_issues"`
	TieKeptLocalIDs     []string         `json:"tie_kept_local_ids"`
	StaleSkippedIDs     []string         `json:"stale_skipped_ids"`
	ConflictSkippedIDs  []string         `json:"conflict_skipped_ids"`
	SkippedDependencies []string         `json:"skipped_dependencies"`
}

type bdImportChange struct {
	ID string `json:"id"`
}

// toReport maps bd import's machine JSON onto ImportReport. bd's `ids` field
// lists every processed row (inserts, updates, and ties); subtracting the update,
// tie, and conflict-skip ids yields the genuinely-inserted set. conflict_skipped_ids
// is emitted by the capable bd the ConflictSkip path already requires, so it maps
// straight into ConflictSkipped. See the ImportReport drain-signal caveat: bd
// reports only content-DIFFERING rows as tie/updated, so a content-identical
// equal-clock re-import lands in `ids` and is classified Inserted here.
func (r bdImportResult) toReport() ImportReport {
	report := ImportReport{}
	updated := make(map[string]bool, len(r.UpdatedIssues))
	for _, ch := range r.UpdatedIssues {
		if ch.ID == "" {
			continue
		}
		updated[ch.ID] = true
		report.Updated = append(report.Updated, ch.ID)
	}
	excluded := make(map[string]bool, len(r.TieKeptLocalIDs)+len(r.ConflictSkippedIDs))
	for _, id := range r.TieKeptLocalIDs {
		excluded[id] = true
	}
	for _, id := range r.ConflictSkippedIDs {
		excluded[id] = true
	}
	report.KeptLocal = append(report.KeptLocal, r.TieKeptLocalIDs...)
	report.StaleSkipped = append(report.StaleSkipped, r.StaleSkippedIDs...)
	report.ConflictSkipped = append(report.ConflictSkipped, r.ConflictSkippedIDs...)
	for _, id := range r.IDs {
		if updated[id] || excluded[id] {
			continue
		}
		report.Inserted = append(report.Inserted, id)
	}
	for _, pair := range r.SkippedDependencies {
		issueID, dependsOnID := parseSkippedDependency(pair)
		report.addSkippedDependency(issueID, dependsOnID)
	}
	return report
}

// parseSkippedDependency parses bd's "issueID -> dependsOnID: reason" skipped
// dependency line into its id pair.
func parseSkippedDependency(line string) (issueID, dependsOnID string) {
	arrow := strings.Index(line, "->")
	if arrow < 0 {
		return strings.TrimSpace(line), ""
	}
	issueID = strings.TrimSpace(line[:arrow])
	rest := line[arrow+2:]
	if colon := strings.Index(rest, ":"); colon >= 0 {
		rest = rest[:colon]
	}
	return issueID, strings.TrimSpace(rest)
}

// runBDImport pipes JSONL to `bd import` and parses the machine JSON result. It
// injects the store's scoped env (so the child sees exactly what `bd export`/`bd
// version` see) with BD_JSON_ENVELOPE stripped, and unwraps a JSON envelope
// defensively if one leaked in anyway.
func (s *BdStore) runBDImport(ctx context.Context, stdin []byte, args ...string) (bdImportResult, error) {
	childEnv := bdImportChildEnv(s.env)
	var out []byte
	var err error
	if s.importRunner != nil {
		out, err = s.importRunner(s.dir, childEnv, stdin, args...)
	} else {
		out, err = execBDImport(ctx, s.dir, childEnv, stdin, args...)
	}
	if err != nil {
		return bdImportResult{}, fmt.Errorf("bd import: %w", err)
	}
	var result bdImportResult
	if err := json.Unmarshal(extractJSON(unwrapBDJSONEnvelope(out)), &result); err != nil {
		return bdImportResult{}, fmt.Errorf("bd import: parsing JSON: %s: %w", truncateRawOutput(out, 200), err)
	}
	return result, nil
}

// bdImportChildEnv builds the child environment for a bd import exec from the
// store's scoped overrides plus the bd baseline (auto-backup opt-out), with
// BD_JSON_ENVELOPE removed so the child always emits the bare --json shape the
// parser expects.
func bdImportChildEnv(overrides map[string]string) []string {
	env := execEnvFor("bd", processEnvSnapshotExcludingNativeDoltOpen(), overrides)
	return envWithout(env, "BD_JSON_ENVELOPE")
}

// unwrapBDJSONEnvelope returns the inner `data` object when bd wrapped its --json
// output as {schema_version, data:{...}} (BD_JSON_ENVELOPE=1); otherwise it
// returns the bytes unchanged. This keeps parsing robust even if a caller's
// ambient BD_JSON_ENVELOPE leaks into a bd child that this store did not spawn
// through the env-stripping import path (e.g. the runner-backed reads).
func unwrapBDJSONEnvelope(out []byte) []byte {
	trimmed := bytes.TrimSpace(extractJSON(out))
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return out
	}
	var envelope struct {
		SchemaVersion *int            `json:"schema_version"`
		Data          json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(trimmed, &envelope); err != nil {
		return out
	}
	if envelope.SchemaVersion == nil || len(envelope.Data) == 0 {
		return out
	}
	return envelope.Data
}

// execBDImport runs `bd import` with JSONL on stdin via os/exec, using the
// caller-computed child env and a bounded timeout bound to ctx.
func execBDImport(ctx context.Context, dir string, env []string, stdin []byte, args ...string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, "bd", args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	start := time.Now()
	runErr := cmd.Run()
	TraceBDCall("go:bdstore.execBDImport", dir, args, start, bdExitCode(runErr), runErr)
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("timed out")
	}
	if runErr != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = bdStdoutErrorDetail(stdout.Bytes())
		}
		if detail != "" {
			return nil, fmt.Errorf("%w: %s", runErr, detail)
		}
		return nil, runErr
	}
	return stdout.Bytes(), nil
}

// bdExitCode returns the child exit status for a finished bd command, or -1 when
// the failure was not an *exec.ExitError (a context kill or a spawn failure).
func bdExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}
