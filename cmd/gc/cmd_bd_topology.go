package main

// `gc bd topology` — the operator command for the work-bead topology
// (engdocs/design/beads-work-topology.md, "Out of scope": the config file IS
// the interface; this is the deferred init sugar). It inspects and sets the
// three topology axes — [beads] infra, [beads.work] scope, [beads.work] target —
// admitting ONLY valid forward ladder combinations, and previews (--dry-run)
// what the marker-gated boot migration would do.
//
// It is ADDITIVE and boot-neutral: without running a setter, nothing changes.
// `show` and `--dry-run` open every source READ-ONLY (the S7 file:?mode=ro path
// for the .gc/infra sqlite scope; a plain List over the Dolt work stores), write
// no config, and create no marker. The setter reuses the same atomic city.toml
// writer (writeCityConfigForEditFS) every other config-edit command uses, and
// re-loads the result to confirm it parses clean. All three axes are validated
// through the EXACT loader validators (config.ValidateBeadsTopology /
// ValidateBeadsClasses / ValidateBeadsClassPrefixes) plus the shared one-way-door
// guard (checkWorkTopologyMarkers), so the CLI can never accept a combination the
// loader would reject or a backward move the markers forbid.
//
// It hangs under `gc bd` as an intercepted pseudo-subcommand (like heartbeat /
// release-if-current), dispatched from doBd BEFORE the bd passthrough and its
// fail-closed marker guard — an operator must be able to INSPECT a city whose
// config already contradicts its markers.

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/coordclass"
	"github.com/gastownhall/gascity/internal/fsys"
	"github.com/spf13/pflag"
)

const bdTopologyCommandName = "gc bd topology"

// bdTopologyFlags holds the parsed topology flags plus which setters were
// explicitly provided (so an unset setter never overwrites a configured axis).
type bdTopologyFlags struct {
	json   bool
	dryRun bool
	help   bool
	infra  string
	scope  string
	target string

	infraSet  bool
	scopeSet  bool
	targetSet bool

	// positional is the optional read-only "show" subcommand ("" when absent).
	positional string
}

func (f bdTopologyFlags) setting() bool { return f.infraSet || f.scopeSet || f.targetSet }

// parseBdTopologyFlags parses the topology args (flags may be interspersed with
// the single optional "show" positional).
func parseBdTopologyFlags(args []string) (bdTopologyFlags, error) {
	var f bdTopologyFlags
	fs := pflag.NewFlagSet("topology", pflag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&f.json, "json", false, "emit the topology as a typed JSON document")
	fs.BoolVar(&f.dryRun, "dry-run", false, "validate the desired topology and print the boot migration plan without writing")
	fs.BoolVarP(&f.help, "help", "h", false, "show topology help")
	fs.StringVar(&f.infra, "infra", "", "set [beads] infra (local)")
	fs.StringVar(&f.scope, "scope", "", "set [beads.work] scope (scoped|unified)")
	fs.StringVar(&f.target, "target", "", "retired compatibility flag")
	// Keep parsing the retired flag only to give established operators an
	// actionable migration error. It is deliberately absent from help: GC owns
	// the local infra and scoped/unified work axes, while bd-enterprise owns a
	// physical backend cutover.
	_ = fs.MarkHidden("target")
	if err := fs.Parse(args); err != nil {
		return f, err
	}
	f.infraSet = fs.Changed("infra")
	f.scopeSet = fs.Changed("scope")
	f.targetSet = fs.Changed("target")
	if f.targetSet {
		return f, errors.New("--target is retired: gc no longer owns remote work migration; use bd-enterprise migrate storage for a physical backend cutover")
	}
	rest := fs.Args()
	if len(rest) > 1 {
		return f, fmt.Errorf("unexpected arguments after %q: %v", rest[0], rest[1:])
	}
	if len(rest) == 1 {
		f.positional = rest[0]
	}
	return f, nil
}

// doBdTopology is the `gc bd topology` entry, dispatched from doBd. It resolves
// its own city (independent of the bd passthrough guards) so an operator can
// inspect a contradictory city.
func doBdTopology(cityName string, args []string, stdout, stderr io.Writer) int {
	f, err := parseBdTopologyFlags(args)
	if err != nil {
		return bdTopologyFail(f.json, stdout, stderr, "invalid_flags", err.Error())
	}
	if f.help {
		_, _ = io.WriteString(stdout, bdTopologyUsage)
		return 0
	}
	if f.positional != "" && f.positional != "show" {
		return bdTopologyFail(f.json, stdout, stderr, "unknown_subcommand",
			fmt.Sprintf("unknown subcommand %q (use `gc bd topology [show]`, or the --infra/--scope/--target setters)", f.positional))
	}
	if f.positional == "show" && (f.setting() || f.dryRun) {
		return bdTopologyFail(f.json, stdout, stderr, "invalid_flags",
			"the `show` subcommand is read-only; drop it (and the --infra/--scope/--target/--dry-run flags describe a set/plan, not a show)")
	}

	cityPath, err := resolveBdCity(cityName)
	if err != nil {
		return bdTopologyFail(f.json, stdout, stderr, "city_resolve_failed", err.Error())
	}
	cfg, err := loadCityConfig(cityPath, stderr)
	if err != nil {
		return bdTopologyFail(f.json, stdout, stderr, "config_load_failed", fmt.Sprintf("loading config: %v", err))
	}

	switch {
	case f.setting() && !f.dryRun:
		return bdTopologySet(cityPath, cfg, f, stdout, stderr)
	case f.dryRun:
		return bdTopologyDryRun(cityPath, cfg, f, stdout, stderr)
	default:
		return bdTopologyShow(cityPath, cfg, f, stdout, stderr)
	}
}

const bdTopologyUsage = `Usage: gc bd topology [show] [flags]

Configure the local Gas City topology. Physical backend migration is owned by
bd-enterprise, not gc.

Flags:
  --infra local          set local SQLite infra stores
  --scope scoped|unified keep rig work separate or unify it in the city workspace
  --dry-run              validate and preview the next normal-start migration
  --json                 emit JSON
  -h, --help             show topology help
`

// bdTopologyFail emits a typed error (JSON or plain) and returns exit code 1.
func bdTopologyFail(jsonOut bool, stdout, stderr io.Writer, code, msg string) int {
	if jsonOut {
		return writeJSONError(stdout, stderr, code, fmt.Sprintf("%s: %s", bdTopologyCommandName, msg), 1)
	}
	fmt.Fprintf(stderr, "%s: %s\n", bdTopologyCommandName, msg) //nolint:errcheck // best-effort stderr
	return 1
}

// ── desired-config computation + validation ──────────────────────────────────

// bdTopologyDesired returns a shallow copy of cfg with the provided setter flags
// applied to the three topology axes. Maps (Classes/Policies) and the Rigs slice
// are shared read-only; only the scalar Beads axes are mutated on the copy, so
// cfg is untouched.
func bdTopologyDesired(cfg *config.City, f bdTopologyFlags) *config.City {
	desired := *cfg
	if f.infraSet {
		desired.Beads.Infra = strings.TrimSpace(f.infra)
	}
	if f.scopeSet {
		desired.Beads.Work.Scope = strings.TrimSpace(f.scope)
	}
	// The former remote target is a retired compatibility input. Any supported
	// topology edit converges an old city onto the local city workspace instead
	// of perpetuating a third topology axis.
	if f.infraSet || f.scopeSet {
		desired.Beads.Work.Target = ""
	}
	return &desired
}

// validateDesiredTopology runs the desired config through the exact loader
// validators (enum + ladder + class backends + prefix distinctness) and the
// shared one-way-door guard, so only a valid FORWARD combination is accepted and
// a backward move (unified→scoped, remote→managed, silent retarget) is refused
// with the same actionable message the boot/reload/routing surfaces use.
func validateDesiredTopology(cityPath string, desired *config.City) error {
	if err := config.ValidateBeadsTopology(desired.Beads); err != nil {
		return err
	}
	if err := config.ValidateBeadsClasses(desired.Beads); err != nil {
		return err
	}
	if err := config.ValidateBeadsClassPrefixes(desired); err != nil {
		return err
	}
	return checkWorkTopologyMarkers(cityPath, desired)
}

// ── show ─────────────────────────────────────────────────────────────────────

type bdTopologyReport struct {
	SchemaVersion string                 `json:"schema_version"`
	OK            bool                   `json:"ok"`
	City          string                 `json:"city"`
	Infra         string                 `json:"infra"` // effective: bd | local
	Scope         string                 `json:"scope"` // scoped | unified
	InfraLocal    bool                   `json:"infra_local"`
	Classes       []bdTopologyClassState `json:"classes"`
	Unified       bdTopologyMarkerState  `json:"unified"`
	Prefixes      []bdTopologyPrefix     `json:"prefixes"`
}

type bdTopologyClassState struct {
	Class   string `json:"class"`
	Backend string `json:"backend"` // bd | sqlite
	Marker  string `json:"marker"`  // present | absent
	Routing string `json:"routing"` // sqlite | bd | unknown
	// StatError carries a non-ENOENT migrated-marker stat fault (routing then
	// fails closed to "unknown"); surfaced so `show` never renders a genuine
	// fault as a silent "unknown", honoring this file's ENOENT-only contract.
	StatError string `json:"stat_error,omitempty"`
}

type bdTopologyMarkerState struct {
	MarkerPresent    bool `json:"marker_present"`
	ResidueUndrained int  `json:"residue_undrained"`
}

type bdTopologyPrefix struct {
	Scope  string `json:"scope"`
	Prefix string `json:"prefix"`
}

// bdTopologyBuildReport gathers the effective topology, each rung's marker state,
// and the prefix inventory. Marker reads honor the ENOENT-only discipline; a
// genuine stat/parse fault surfaces as an error (never a silent "absent").
func bdTopologyBuildReport(cityPath string, cfg *config.City) (bdTopologyReport, error) {
	rep := bdTopologyReport{
		SchemaVersion: "1",
		OK:            true,
		City:          cityPath,
		Infra:         effectiveInfraLabel(cfg.Beads),
		Scope:         cfg.Beads.Work.EffectiveScope(),
		InfraLocal:    cfg.Beads.EffectiveInfraLocal(),
	}
	for _, st := range classMigrationStates(cityPath, cfg) {
		cs := bdTopologyClassState{
			Class:   st.class,
			Backend: st.backend,
			Marker:  st.marker,
			Routing: st.routing,
		}
		if st.statErr != nil {
			cs.StatError = st.statErr.Error()
		}
		rep.Classes = append(rep.Classes, cs)
	}
	if m, ok, err := readWorkTopologyMarker(workUnifiedMarkerPath(cityPath)); err != nil {
		return bdTopologyReport{}, fmt.Errorf("reading work.unified marker: %w", err)
	} else if ok {
		rep.Unified = bdTopologyMarkerState{MarkerPresent: true, ResidueUndrained: m.undrainedResidueCount()}
	}
	rep.Prefixes = bdTopologyPrefixInventory(cityPath, cfg)
	return rep, nil
}

func bdTopologyShow(cityPath string, cfg *config.City, f bdTopologyFlags, stdout, stderr io.Writer) int {
	rep, err := bdTopologyBuildReport(cityPath, cfg)
	if err != nil {
		return bdTopologyFail(f.json, stdout, stderr, "topology_read_failed", err.Error())
	}
	if f.json {
		return writeCLIJSONLineOrExit(stdout, stderr, bdTopologyCommandName, rep)
	}
	renderBdTopologyReport(stdout, rep)
	return 0
}

func renderBdTopologyReport(stdout io.Writer, rep bdTopologyReport) {
	fmt.Fprintf(stdout, "work-bead topology for %s\n", rep.City) //nolint:errcheck
	fmt.Fprintf(stdout, "  infra:  %s\n", rep.Infra)             //nolint:errcheck
	fmt.Fprintf(stdout, "  scope:  %s\n", rep.Scope)             //nolint:errcheck
	fmt.Fprintln(stdout, "  infra classes:")                     //nolint:errcheck
	for _, c := range rep.Classes {
		fmt.Fprintf(stdout, "    %-9s backend=%-6s marker=%-7s routing=%s", c.Class, c.Backend, c.Marker, c.Routing) //nolint:errcheck
		if c.StatError != "" {
			fmt.Fprintf(stdout, " (marker stat: %s)", c.StatError) //nolint:errcheck
		}
		fmt.Fprintln(stdout) //nolint:errcheck
	}
	fmt.Fprintf(stdout, "  unified marker: %s", presentLabel(rep.Unified.MarkerPresent)) //nolint:errcheck
	if rep.Unified.MarkerPresent {
		fmt.Fprintf(stdout, " (residue undrained=%d)", rep.Unified.ResidueUndrained) //nolint:errcheck
	}
	fmt.Fprintln(stdout)                //nolint:errcheck
	fmt.Fprintln(stdout, "  prefixes:") //nolint:errcheck
	for _, p := range rep.Prefixes {
		fmt.Fprintf(stdout, "    %-12s %s\n", p.Scope, p.Prefix) //nolint:errcheck
	}
}

// ── set ──────────────────────────────────────────────────────────────────────

type bdTopologySetResult struct {
	SchemaVersion string         `json:"schema_version"`
	OK            bool           `json:"ok"`
	City          string         `json:"city"`
	Desired       bdTopologyAxes `json:"desired"`
	Wrote         bool           `json:"wrote"`
	NextStep      string         `json:"next_step"`
	BootSummary   string         `json:"boot_summary"`
}

type bdTopologyAxes struct {
	Infra string `json:"infra"`
	Scope string `json:"scope"`
}

func desiredAxes(desired *config.City) bdTopologyAxes {
	return bdTopologyAxes{
		Infra: effectiveInfraLabel(desired.Beads),
		Scope: desired.Beads.Work.EffectiveScope(),
	}
}

func bdTopologySet(cityPath string, cfg *config.City, f bdTopologyFlags, stdout, stderr io.Writer) int {
	desired := bdTopologyDesired(cfg, f)
	if err := validateDesiredTopology(cityPath, desired); err != nil {
		return bdTopologyFail(f.json, stdout, stderr, "invalid_topology", err.Error())
	}

	// Write the three axes onto the RAW (un-expanded) city.toml — preserving
	// includes/patches — through the same atomic writer every config-edit command
	// uses, then re-load the result to confirm it parses clean (deliverable B).
	fs := fsys.OSFS{}
	tomlPath := filepath.Join(cityPath, "city.toml")
	rawCfg, err := loadCityConfigForEditFS(fs, tomlPath)
	if err != nil {
		return bdTopologyFail(f.json, stdout, stderr, "config_load_failed", fmt.Sprintf("loading config for edit: %v", err))
	}
	if f.infraSet {
		rawCfg.Beads.Infra = strings.TrimSpace(f.infra)
	}
	if f.scopeSet {
		rawCfg.Beads.Work.Scope = strings.TrimSpace(f.scope)
	}
	if f.infraSet || f.scopeSet {
		rawCfg.Beads.Work.Target = ""
	}

	// Snapshot the exact bytes we are about to overwrite so a post-write reload /
	// revalidation failure (a marshal/expansion discrepancy) rolls the set back —
	// all-or-nothing — instead of stranding a self-reported-broken city.toml. The
	// write follows city.toml through any symlink, so snapshot the resolved target.
	writePath, err := config.ResolveCityRewritePath(fs, tomlPath)
	if err != nil {
		return bdTopologyFail(f.json, stdout, stderr, "config_write_failed", fmt.Sprintf("resolving city.toml write path: %v", err))
	}
	prevBytes, err := fs.ReadFile(writePath)
	if err != nil {
		return bdTopologyFail(f.json, stdout, stderr, "config_load_failed", fmt.Sprintf("reading city.toml before write: %v", err))
	}
	if err := writeCityConfigForEditFS(fs, tomlPath, rawCfg); err != nil {
		return bdTopologyFail(f.json, stdout, stderr, "config_write_failed", fmt.Sprintf("writing config: %v", err))
	}
	restore := func(cause string) int {
		if rerr := fsys.WriteFileAtomic(fs, writePath, prevBytes, 0o644); rerr != nil {
			return bdTopologyFail(f.json, stdout, stderr, "config_reload_failed",
				fmt.Sprintf("%s AND restoring the previous city.toml failed (%v) — fix it by hand", cause, rerr))
		}
		return bdTopologyFail(f.json, stdout, stderr, "config_reload_failed",
			fmt.Sprintf("%s — restored the previous city.toml (no change applied)", cause))
	}

	reloaded, err := loadCityConfig(cityPath, stderr)
	if err != nil {
		return restore(fmt.Sprintf("wrote city.toml but it no longer parses (%v)", err))
	}
	if err := validateDesiredTopology(cityPath, reloaded); err != nil {
		return restore(fmt.Sprintf("wrote city.toml but the reloaded config fails validation (%v)", err))
	}

	summary := bdTopologyBootSummary(cityPath, reloaded)
	nextStep := "restart the controller (gc stop && gc start, or bounce the supervised unit) to run the migration at boot"
	res := bdTopologySetResult{
		SchemaVersion: "1",
		OK:            true,
		City:          cityPath,
		Desired:       desiredAxes(reloaded),
		Wrote:         true,
		NextStep:      nextStep,
		BootSummary:   summary,
	}
	if f.json {
		return writeCLIJSONLineOrExit(stdout, stderr, bdTopologyCommandName, res)
	}
	fmt.Fprintf(stdout, "topology updated: infra=%s scope=%s\n", res.Desired.Infra, res.Desired.Scope) //nolint:errcheck
	if summary != "" {
		fmt.Fprintf(stdout, "  next boot will: %s\n", summary) //nolint:errcheck
	} else {
		fmt.Fprintln(stdout, "  next boot will: nothing to migrate (already at the desired rung)") //nolint:errcheck
	}
	fmt.Fprintf(stdout, "  NEXT STEP: %s\n", nextStep) //nolint:errcheck
	return 0
}

// ── dry-run plan ─────────────────────────────────────────────────────────────

type bdTopologyPlan struct {
	SchemaVersion string                `json:"schema_version"`
	OK            bool                  `json:"ok"`
	City          string                `json:"city"`
	Desired       bdTopologyAxes        `json:"desired"`
	InfraRung     bdTopologyInfraPlan   `json:"infra_rung"`
	UnifyRung     bdTopologyUnifyPlan   `json:"unify_rung"`
	Errors        []bdTopologyPlanError `json:"errors,omitempty"`
}

type bdTopologyPlanError struct {
	Code    string `json:"code"`
	Rung    string `json:"rung"`
	Source  string `json:"source"`
	Message string `json:"message"`
}

type bdTopologyInfraPlan struct {
	Status            string                 `json:"status"` // already-done | would-run | n/a
	Classes           []bdTopologyClassPlan  `json:"classes"`
	InfraScopePresent bool                   `json:"infra_scope_present"`
	InfraScopeDir     string                 `json:"infra_scope_dir,omitempty"`
	InfraScopeCensus  *bdTopologyInfraCensus `json:"infra_scope_census,omitempty"`
}

type bdTopologyClassPlan struct {
	Class  string `json:"class"`
	Action string `json:"action"` // would-relocate | already-relocated | n/a
}

type bdTopologyInfraCensus struct {
	Scanned   int      `json:"scanned"`
	WorkClass int      `json:"work_class"`
	Orphans   int      `json:"orphans"`
	OrphanIDs []string `json:"orphan_ids,omitempty"`
	Note      string   `json:"note,omitempty"`
}

type bdTopologyUnifyPlan struct {
	Status        string              `json:"status"` // already-done | would-run | n/a
	Rigs          []bdTopologyRigPlan `json:"rigs,omitempty"`
	UnionPrefixes []string            `json:"union_allowed_prefixes,omitempty"`
	Note          string              `json:"note,omitempty"`
}

type bdTopologyRigPlan struct {
	Rig       string `json:"rig"`
	Prefix    string `json:"prefix"`
	Database  string `json:"database,omitempty"`
	WorkBeads int    `json:"work_beads"`
	Countable bool   `json:"countable"`
	Note      string `json:"note,omitempty"`
}

var (
	bdTopologyCityCommandRunner = bdCommandRunnerForCity
	bdTopologyRigCommandRunner  = bdCommandRunnerForRig
	openWorkTopologyScopeStore  = openReadOnlyWorkTopologyScopeStore
)

// openReadOnlyWorkTopologyScopeStore constructs the bd-backed census view
// without using the normal work-store opener. The normal opener may reap a
// stale JSONL export and lets bd run startup migrations; both are valid for a
// live store but violate topology dry-run's no-write contract.
func openReadOnlyWorkTopologyScopeStore(cityPath, scopeRoot string) (beads.Store, func(), error) {
	cfg, err := loadCityConfig(cityPath, io.Discard)
	if err != nil {
		return nil, func() {}, fmt.Errorf("loading city config for read-only work census: %w", err)
	}
	scopeRoot = resolveStoreScopeRoot(cityPath, scopeRoot)
	provider := rawBeadsProviderForScope(scopeRoot, cityPath)
	if provider == "file" {
		store, err := openExistingScopeLocalFileStore(scopeRoot)
		return store, func() {}, err
	}
	if !providerUsesBdStoreContract(provider) {
		return nil, func() {}, fmt.Errorf("work provider %q has no read-only topology census adapter", provider)
	}
	runner := bdTopologyCityCommandRunner(cityPath)
	if !samePath(scopeRoot, cityPath) {
		runner = bdTopologyRigCommandRunner(cityPath, cfg, scopeRoot)
	}
	store := beads.NewBdStoreWithPrefix(
		scopeRoot,
		bdTopologyReadOnlyCommandRunner(runner),
		issuePrefixForScope(scopeRoot, cityPath, cfg),
		bdStoreOptionsForConfig(cfg)...,
	)
	return store, func() {}, nil
}

func bdTopologyReadOnlyCommandRunner(next beads.CommandRunner) beads.CommandRunner {
	return func(dir, name string, args ...string) ([]byte, error) {
		if name == "bd" {
			for _, arg := range args {
				if arg == "--readonly" {
					return next(dir, name, args...)
				}
			}
			if len(args) == 0 {
				args = []string{"--readonly"}
			} else {
				readOnlyArgs := make([]string, 0, len(args)+1)
				readOnlyArgs = append(readOnlyArgs, args[0], "--readonly")
				args = append(readOnlyArgs, args[1:]...)
			}
		}
		return next(dir, name, args...)
	}
}

func bdTopologyDryRun(cityPath string, cfg *config.City, f bdTopologyFlags, stdout, stderr io.Writer) int {
	desired := bdTopologyDesired(cfg, f)
	if err := validateDesiredTopology(cityPath, desired); err != nil {
		return bdTopologyFail(f.json, stdout, stderr, "invalid_topology", err.Error())
	}
	plan := bdTopologyPlan{
		SchemaVersion: "1",
		City:          cityPath,
		Desired:       desiredAxes(desired),
		InfraRung:     bdTopologyPlanInfraRung(cityPath, desired),
		UnifyRung:     bdTopologyPlanUnifyRung(cityPath, desired),
	}
	plan.Errors = bdTopologyPlanErrors(plan)
	plan.OK = len(plan.Errors) == 0
	if f.json {
		if code := writeCLIJSONLineOrExit(stdout, stderr, bdTopologyCommandName, plan); code != 0 {
			return code
		}
		if !plan.OK {
			return 1
		}
		return 0
	}
	renderBdTopologyPlan(stdout, plan)
	if !plan.OK {
		for _, problem := range plan.Errors {
			fmt.Fprintf(stderr, "%s: %s (%s/%s): %s\n", bdTopologyCommandName, problem.Code, problem.Rung, problem.Source, problem.Message) //nolint:errcheck
		}
		return 1
	}
	return 0
}

func bdTopologyPlanErrors(plan bdTopologyPlan) []bdTopologyPlanError {
	var problems []bdTopologyPlanError
	if census := plan.InfraRung.InfraScopeCensus; census != nil {
		if census.Note != "" {
			problems = append(problems, bdTopologyPlanError{
				Code:    "infra_census_failed",
				Rung:    "infra",
				Source:  plan.InfraRung.InfraScopeDir,
				Message: census.Note,
			})
		}
		if census.Orphans > 0 {
			problems = append(problems, bdTopologyPlanError{
				Code:    "infra_work_orphans",
				Rung:    "infra",
				Source:  plan.InfraRung.InfraScopeDir,
				Message: fmt.Sprintf("%d work-class bead(s) are absent from the work store: %s", census.Orphans, strings.Join(census.OrphanIDs, ", ")),
			})
		}
	}
	if plan.UnifyRung.Status == "would-run" {
		for _, rig := range plan.UnifyRung.Rigs {
			if rig.Countable {
				continue
			}
			problems = append(problems, bdTopologyPlanError{
				Code:    "work_census_failed",
				Rung:    "unify",
				Source:  rig.Rig,
				Message: rig.Note,
			})
		}
	}
	return problems
}

// bdTopologyPlanInfraRung computes the infra rung READ-ONLY: which of the five
// relocatable classes would move to their embedded sqlite store, plus (on a
// window-3 .gc/infra city) the G1 zero-ClassWork census over the read-only
// combined scope.
func bdTopologyPlanInfraRung(cityPath string, desired *config.City) bdTopologyInfraPlan {
	plan := bdTopologyInfraPlan{Status: "n/a"}
	sqliteDesired, pending := 0, 0
	for _, st := range classMigrationStates(cityPath, desired) {
		cp := bdTopologyClassPlan{Class: st.class, Action: "n/a"}
		if desired.Beads.ClassBackend(st.class) == config.BeadsClassBackendSQLite {
			sqliteDesired++
			if st.marker == "present" {
				cp.Action = "already-relocated"
			} else {
				cp.Action = "would-relocate"
				pending++
			}
		}
		plan.Classes = append(plan.Classes, cp)
	}
	switch {
	case sqliteDesired == 0:
		plan.Status = "n/a"
	case pending == 0:
		plan.Status = "already-done"
	default:
		plan.Status = "would-run"
	}
	// The window-3 combined infra scope only feeds the class migrations when the
	// infra rung is active (infra effectively local), so the census — and the
	// read-only open it costs — is only relevant then.
	if sqliteDesired > 0 {
		if dir, present := infraScopeMigrationSource(cityPath); present {
			plan.InfraScopePresent = true
			plan.InfraScopeDir = dir
			plan.InfraScopeCensus = bdTopologyInfraScopeCensus(cityPath)
		}
	}
	return plan
}

// bdTopologyInfraScopeCensus reruns the G1 preflight READ-ONLY over the .gc/infra
// combined scope and reports the ClassWork census (scanned rows, ClassWork
// count, and any orphan whose id is absent from the work store — the beads the
// class flip would strand). An unopenable scope or work store is retained as a
// Note in the rendered plan and classified as a dry-run error.
func bdTopologyInfraScopeCensus(cityPath string) *bdTopologyInfraCensus {
	census := &bdTopologyInfraCensus{}
	scope, closeScope, present, err := openInfraCombinedScopeSource(cityPath)
	defer closeScope()
	if err != nil {
		census.Note = fmt.Sprintf("combined infra scope present but unopenable: %v", err)
		return census
	}
	if !present {
		return census
	}
	rows, err := scope.List(beads.ListQuery{IncludeClosed: true, TierMode: beads.TierBoth, AllowScan: true})
	if err != nil {
		census.Note = fmt.Sprintf("scanning combined infra scope: %v", err)
		return census
	}
	census.Scanned = len(rows)
	var workRows []beads.Bead
	for _, b := range rows {
		if coordclass.Classify(b) == coordclass.ClassWork {
			workRows = append(workRows, b)
		}
	}
	census.WorkClass = len(workRows)
	if len(workRows) == 0 {
		return census
	}
	// A ClassWork bead in .gc/infra is imported by no class migration. Confirm each
	// is a safe duplicate already in the work store; an absent id is an orphan that
	// would block the boot flip (matches ensureInfraScopeClassifierClean).
	workStore, closeWork, werr := openWorkTopologyScopeStore(cityPath, cityPath)
	if werr != nil {
		census.Note = fmt.Sprintf("work store unavailable; G1 orphan check deferred to boot: %v", werr)
		return census
	}
	defer closeWork()
	var orphans []string
	for _, b := range workRows {
		if _, gerr := workStore.Get(b.ID); gerr == nil {
			continue
		} else if beadsIsNotFound(gerr) {
			orphans = append(orphans, b.ID)
		} else {
			census.Note = fmt.Sprintf("checking work store for %q: %v", b.ID, gerr)
			return census
		}
	}
	census.Orphans = len(orphans)
	census.OrphanIDs = cappedInfraOrphanIDs(orphans)
	return census
}

// bdTopologyPlanUnifyRung computes the unify rung READ-ONLY: which rig work DBs
// would merge into the city DB (with a ClassWork bead count per rig) and the
// union allowed_prefixes set.
func bdTopologyPlanUnifyRung(cityPath string, desired *config.City) bdTopologyUnifyPlan {
	plan := bdTopologyUnifyPlan{Status: "n/a"}
	if !desired.Beads.Work.IsUnified() {
		return plan
	}
	plan.UnionPrefixes = cityScopePrefixes(desired)
	plan.Note = "a rig added later to this unified/remote city has its prefix auto-registered into the shared allowed_prefixes by `gc rig add`, so this union stays consistent"

	if present, err := workMarkerPresent(workUnifiedMarkerPath(cityPath)); err == nil && present {
		plan.Status = "already-done"
		return plan
	}
	plan.Status = "would-run"

	resolveRigPaths(cityPath, desired.Rigs)
	cityID, cityErr := workUnifyResolveIdentity(cityPath, cityPath)
	for i := range desired.Rigs {
		rig := desired.Rigs[i]
		if strings.TrimSpace(rig.Path) == "" {
			continue
		}
		root := resolveStoreScopeRoot(cityPath, rig.Path)
		if samePath(root, cityPath) {
			continue
		}
		rp := bdTopologyRigPlan{Rig: rig.Name, Prefix: rig.EffectivePrefix()}
		id, idErr := workUnifyResolveIdentity(cityPath, root)
		if idErr == nil {
			rp.Database = id.database
			if cityErr == nil && id.sameEndpoint(cityID) {
				continue // already resolves to the shared city database
			}
		}
		if n, ok, note := bdTopologyCountWorkBeads(cityPath, root); ok {
			rp.WorkBeads, rp.Countable = n, true
		} else {
			rp.Note = note
		}
		plan.Rigs = append(plan.Rigs, rp)
	}
	return plan
}

// bdTopologyCountWorkBeads opens a scope's work store READ-ONLY and counts its
// ClassWork beads (durable + ephemeral). Best-effort: an unopenable store or a
// failed List returns ok=false with an explanatory note; the caller retains the
// plan for diagnostics and makes the dry-run fail closed.
func bdTopologyCountWorkBeads(cityPath, scopeRoot string) (int, bool, string) {
	store, closeFn, err := openWorkTopologyScopeStore(cityPath, scopeRoot)
	if err != nil {
		return 0, false, fmt.Sprintf("work store unavailable (count deferred to boot): %v", err)
	}
	defer closeFn()
	rows, err := store.List(beads.ListQuery{IncludeClosed: true, TierMode: beads.TierBoth, AllowScan: true})
	if err != nil {
		return 0, false, fmt.Sprintf("listing work store: %v", err)
	}
	n := 0
	for _, b := range rows {
		if coordclass.Classify(b) == coordclass.ClassWork {
			n++
		}
	}
	return n, true, ""
}

func renderBdTopologyPlan(stdout io.Writer, plan bdTopologyPlan) {
	fmt.Fprintf(stdout, "work-topology migration plan for %s\n", plan.City)                       //nolint:errcheck
	fmt.Fprintf(stdout, "  desired: infra=%s scope=%s\n", plan.Desired.Infra, plan.Desired.Scope) //nolint:errcheck
	fmt.Fprintf(stdout, "  infra rung [%s]:\n", plan.InfraRung.Status)                            //nolint:errcheck
	for _, c := range plan.InfraRung.Classes {
		fmt.Fprintf(stdout, "    %-9s %s\n", c.Class, c.Action) //nolint:errcheck
	}
	if plan.InfraRung.InfraScopePresent {
		fmt.Fprintf(stdout, "    window-3 combined infra scope: %s\n", plan.InfraRung.InfraScopeDir) //nolint:errcheck
		if c := plan.InfraRung.InfraScopeCensus; c != nil {
			fmt.Fprintf(stdout, "      census: scanned=%d work-class=%d orphans=%d", c.Scanned, c.WorkClass, c.Orphans) //nolint:errcheck
			if len(c.OrphanIDs) > 0 {
				fmt.Fprintf(stdout, " [%s]", strings.Join(c.OrphanIDs, ", ")) //nolint:errcheck
			}
			fmt.Fprintln(stdout) //nolint:errcheck
			if c.Orphans > 0 {
				fmt.Fprintln(stdout, "      WARNING: orphan work-class beads would BLOCK the boot flip (G1)") //nolint:errcheck
			}
			if c.Note != "" {
				fmt.Fprintf(stdout, "      note: %s\n", c.Note) //nolint:errcheck
			}
		}
	}
	fmt.Fprintf(stdout, "  unify rung [%s]:\n", plan.UnifyRung.Status) //nolint:errcheck
	for _, r := range plan.UnifyRung.Rigs {
		line := fmt.Sprintf("    rig %s (prefix %s)", r.Rig, r.Prefix)
		if r.Database != "" {
			line += fmt.Sprintf(" db=%s", r.Database)
		}
		if r.Countable {
			line += fmt.Sprintf(" work-beads=%d", r.WorkBeads)
		} else if r.Note != "" {
			line += fmt.Sprintf(" (%s)", r.Note)
		}
		fmt.Fprintln(stdout, line) //nolint:errcheck
	}
	if len(plan.UnifyRung.UnionPrefixes) > 0 {
		fmt.Fprintf(stdout, "    union allowed_prefixes: %s\n", strings.Join(plan.UnifyRung.UnionPrefixes, ", ")) //nolint:errcheck
	}
	if plan.UnifyRung.Note != "" {
		fmt.Fprintf(stdout, "    note: %s\n", plan.UnifyRung.Note) //nolint:errcheck
	}
}

// ── shared helpers ────────────────────────────────────────────────────────────

// effectiveInfraLabel renders the effective infra axis: "local" when the
// aggregate resolves infra classes to their embedded stores (explicit infra, or
// implied by unified/remote), else "bd".
func effectiveInfraLabel(b config.BeadsConfig) string {
	if b.EffectiveInfraLocal() {
		return "local"
	}
	return "bd"
}

func presentLabel(present bool) string {
	if present {
		return "present"
	}
	return "absent"
}

// bdTopologyPrefixInventory returns the HQ prefix plus every rig's effective
// prefix, in scope order (hq first, then rigs as declared).
func bdTopologyPrefixInventory(cityPath string, cfg *config.City) []bdTopologyPrefix {
	resolveRigPaths(cityPath, cfg.Rigs)
	inv := []bdTopologyPrefix{{Scope: "hq", Prefix: config.EffectiveHQPrefix(cfg)}}
	for i := range cfg.Rigs {
		inv = append(inv, bdTopologyPrefix{Scope: cfg.Rigs[i].Name, Prefix: cfg.Rigs[i].EffectivePrefix()})
	}
	return inv
}

// bdTopologyBootSummary describes, in one line, what the marker-gated boot
// migration would do for the desired config given the current markers.
func bdTopologyBootSummary(cityPath string, desired *config.City) string {
	var parts []string
	// Infra rung.
	if desired.Beads.EffectiveInfraLocal() {
		pending := false
		for _, st := range classMigrationStates(cityPath, desired) {
			if desired.Beads.ClassBackend(st.class) == config.BeadsClassBackendSQLite && st.marker != "present" {
				pending = true
				break
			}
		}
		if pending {
			parts = append(parts, "relocate infra classes to embedded sqlite stores")
		}
	}
	// Unify rung.
	if desired.Beads.Work.IsUnified() {
		if present, err := workMarkerPresent(workUnifiedMarkerPath(cityPath)); err != nil || !present {
			parts = append(parts, "unify rig work beads into the city database")
		}
	}
	return strings.Join(parts, "; ")
}

// beadsIsNotFound reports whether err is beads.ErrNotFound.
func beadsIsNotFound(err error) bool {
	return errors.Is(err, beads.ErrNotFound)
}
