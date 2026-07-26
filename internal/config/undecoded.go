package config

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

const agentsAliasWarning = "[agents] is a deprecated compatibility alias for [agent_defaults]; rewrite the table name to [agent_defaults]"

// retiredKey records a config key that was removed in a known version. It is no
// longer decoded into any struct, but its presence in config is a migration
// WARNING, never a fatal unknown-field error — a city that has not yet dropped
// the key still loads. This is the read-side counterpart of deleting a config
// field: register the key here in the same change that removes the field.
type retiredKey struct {
	// RemovedIn is the version anchor at which the key was retired (a released
	// version tag or an in-repo removal anchor). Surfaced in the warning so an
	// operator knows when it stopped taking effect.
	RemovedIn string
	// Note is optional migration guidance (what replaced it, or what to do).
	Note string
	// CanonicalizeOnRewrite is true only when Load folds the retired key's
	// semantics into fields emitted by City.MarshalForWrite. Structured city
	// edits may then drop the retired spelling without losing behavior.
	CanonicalizeOnRewrite bool
}

// retiredKeys registers retired config keys by dotted TOML path. A key here is
// downgraded from a fatal unknown-field error to a warning by the undecoded
// classifier (classifyUndecoded), and IsRetiredKeyWarning keeps it non-fatal +
// surfaced on the two downstream deciders that re-classify config warnings —
// strict mode (cmd/gc/strict_warnings.go) and the agent warning-emit path
// (cmd/gc/cmd_agent.go).
//
// INTEGRATION NOTES:
//   - Whole-table retirement needs BOTH the parent-table key and each leaf key
//     registered, because toml.MetaData.Undecoded() reports both. A single
//     leaf under a still-decoded table (like beads.graph_store below) needs
//     only the leaf key.
//   - The struct-round-trip rewrite guard (GuardRewriteKeyLoss in
//     site_binding.go) still refuses a retired key by default. A city key may
//     opt into CanonicalizeOnRewrite only when Load preserves its behavior in
//     the canonical City fields written back to disk.
var retiredKeys = map[string]retiredKey{
	// The b36/deploy-lineage single-infra-scope key ([beads] graph_store =
	// "sqlite" | "dolt"). Superseded by the work-bead topology axes: relocate
	// the graph class with [beads.classes.graph] backend="sqlite" (fine-grained)
	// or [beads] infra="local" (all relocatable classes at once).
	"beads.graph_store": {
		RemovedIn:             "the work-bead topology migration",
		Note:                  `set [beads.classes.graph] backend="sqlite" (or [beads] infra="local") instead`,
		CanonicalizeOnRewrite: true,
	},
}

func cityRewriteCanonicalizesRetiredKey(key string) bool {
	retired, ok := retiredKeys[key]
	return ok && retired.CanonicalizeOnRewrite
}

// foldRetiredBeadsGraphStore honors the retired [beads] graph_store key by
// mapping graph_store="sqlite" onto the replacement [beads.classes.graph]
// backend="sqlite". Without this a b36/deploy-lineage city.toml whose only
// routing knob is graph_store would upgrade, warn, and then boot graph-blind on
// bd while all molecule/step/wisp data sits in the sqlite graph store (a
// recorded destructive-restart incident class). The retired-key WARNING still
// fires via the undecoded classifier — this only applies the value. An explicit
// [beads.classes.graph] backend in the same file wins (no override). Only the
// "sqlite" value folds; graph_store="dolt" (graph stays on bd) is left alone.
func foldRetiredBeadsGraphStore(data []byte, md toml.MetaData, cfg *City) {
	if cfg == nil || !md.IsDefined("beads", "graph_store") {
		return
	}
	if entry, ok := cfg.Beads.Classes[BeadClassGraph]; ok && strings.TrimSpace(entry.Backend) != "" {
		return // explicit per-class backend wins
	}
	var probe struct {
		Beads struct {
			GraphStore string `toml:"graph_store"`
		} `toml:"beads"`
	}
	if _, err := toml.Decode(string(data), &probe); err != nil {
		return
	}
	if strings.TrimSpace(probe.Beads.GraphStore) != BeadsClassBackendSQLite {
		return
	}
	if cfg.Beads.Classes == nil {
		cfg.Beads.Classes = map[string]BeadClassConfig{}
	}
	entry := cfg.Beads.Classes[BeadClassGraph]
	entry.Backend = BeadsClassBackendSQLite
	cfg.Beads.Classes[BeadClassGraph] = entry
}

// retiredKeyWarning renders the migration warning for a retired key.
func retiredKeyWarning(source, key string, rk retiredKey) string {
	w := fmt.Sprintf("%s: %q was retired in %s and is ignored", source, key, rk.RemovedIn)
	if rk.Note != "" {
		w += "; " + rk.Note
	}
	return w
}

// IsRetiredKeyWarning reports whether w is a retired-config-key migration warning
// (the retiredKeyWarning rendering). Downstream warning re-classifiers — strict
// mode and the agent warning-emit path — consult this so a retired key stays a
// surfaced, non-fatal warning rather than being re-promoted to a fatal error or
// silently dropped, keeping the retirement contract true beyond the classifier.
// Anchored to the stable rendering, so an entry's Note text does not affect it.
func IsRetiredKeyWarning(w string) bool {
	return strings.Contains(w, `" was retired in `) && strings.Contains(w, " and is ignored")
}

// classifyUndecoded maps a single undecoded TOML key to its human warning and
// whether it is FATAL. Retired keys and specialized (release-wave) keys warn but
// never fail; every other unknown key is a fatal unknown-field error. Taking the
// retired map as a parameter keeps it testable without mutating package state.
func classifyUndecoded(source, key string, known []string, retired map[string]retiredKey) (warning string, fatal bool) {
	if rk, ok := retired[key]; ok {
		return retiredKeyWarning(source, key, rk), false
	}
	if special, ok := specializedUndecodedWarning(source, key); ok {
		return special, false
	}
	return unknownFieldWarning(source, key, known), true
}

var agentDefaultsCompatibilityOverlapKeys = []string{
	"provider",
	"model",
	"wake_mode",
	"default_sling_formula",
	"allow_overlay",
	"allow_env_override",
	"append_fragments",
}

// CheckUndecodedKeys examines TOML metadata for keys that were present in
// the input but not mapped to any struct field. For each unknown key, it
// computes edit distance against known field names and suggests the closest
// match if one is within 2 edits. Returns a list of human-readable warnings.
func CheckUndecodedKeys(md toml.MetaData, source string) []string {
	var warnings []string
	warnings = append(warnings, agentDefaultsCompatibilityWarnings(md, source)...)

	undecoded := md.Undecoded()
	if len(undecoded) == 0 {
		return warnings
	}

	known := knownTOMLKeys()
	for _, key := range undecoded {
		w, _ := classifyUndecoded(source, key.String(), known, retiredKeys)
		warnings = append(warnings, w)
	}
	return warnings
}

func fatalUndecodedWarnings(md toml.MetaData, source string) []string {
	undecoded := md.Undecoded()
	if len(undecoded) == 0 {
		return nil
	}

	known := knownTOMLKeys()
	var warnings []string
	for _, key := range undecoded {
		if w, fatal := classifyUndecoded(source, key.String(), known, retiredKeys); fatal {
			warnings = append(warnings, w)
		}
	}
	return warnings
}

func validateCityAuthoringSurface(md toml.MetaData) error {
	if md.IsDefined("formulas", "dir") {
		return fmt.Errorf("[formulas].dir is no longer supported; use the well-known formulas/ directory")
	}
	return nil
}

func validatePackAuthoringSurface(md toml.MetaData, source string) error {
	if md.IsDefined("agents") {
		return fmt.Errorf("%s: [agents] is a city.toml compatibility alias for [agent_defaults], not a pack.toml field", source)
	}
	if md.IsDefined("defaults", "rig", "imports") {
		return fmt.Errorf("%s: [defaults.rig.imports] belongs in city.toml, not pack.toml", source)
	}
	if md.IsDefined("formulas", "dir") {
		return fmt.Errorf("%s: [formulas].dir is no longer supported; use the well-known formulas/ directory", source)
	}
	if md.IsDefined("patches", "rigs") {
		return fmt.Errorf("%s: [[patches.rigs]] is only valid in city.toml; pack.toml supports [[patches.agent]] only", source)
	}
	if md.IsDefined("patches", "providers") {
		return fmt.Errorf("%s: [[patches.providers]] is only valid in city.toml; pack.toml supports [[patches.agent]] only", source)
	}
	return nil
}

func unknownFieldWarning(source, key string, known []string) string {
	suggestion := suggestKey(key, known)
	w := fmt.Sprintf("%s: unknown field %q", source, key)
	if suggestion != "" {
		w += fmt.Sprintf(" (did you mean %q?)", suggestion)
	}
	return w
}

func agentDefaultsCompatibilityWarnings(md toml.MetaData, source string) []string {
	if !md.IsDefined("agents") {
		return nil
	}

	warnings := []string{fmt.Sprintf("%s: %s", source, agentsAliasWarning)}
	if md.IsDefined("agent_defaults") && agentDefaultsTablesOverlap(md) {
		warnings = append(warnings, fmt.Sprintf("%s: both [agent_defaults] and [agents] are present; canonical [agent_defaults] wins for overlapping keys", source))
	}
	return warnings
}

func agentDefaultsTablesOverlap(md toml.MetaData) bool {
	for _, key := range agentDefaultsCompatibilityOverlapKeys {
		if md.IsDefined("agent_defaults", key) && md.IsDefined("agents", key) {
			return true
		}
	}
	return false
}

func specializedUndecodedWarning(source, key string) (string, bool) {
	switch key {
	case "agent_defaults.scope", "agents.scope":
		return fmt.Sprintf("%s: %q is not supported in this release wave; keep setting scope per agent in agents/<name>/agent.toml", source, key), true
	case "agent_defaults.install_agent_hooks", "agents.install_agent_hooks":
		return fmt.Sprintf("%s: %q is not supported in this release wave; keep setting install_agent_hooks per agent in agents/<name>/agent.toml", source, key), true
	default:
		return "", false
	}
}

// suggestKey finds the closest known key to the given unknown key using
// edit distance. Returns the suggestion if the distance is <= 2, or "".
func suggestKey(unknown string, known []string) string {
	// Extract the leaf key (last component after dots).
	leaf := unknown
	if idx := strings.LastIndex(unknown, "."); idx >= 0 {
		leaf = unknown[idx+1:]
	}

	bestKey := ""
	bestDist := 3 // only suggest if distance <= 2
	for _, k := range known {
		d := editDistance(leaf, k)
		if d < bestDist {
			bestDist = d
			bestKey = k
		}
	}
	if bestKey == leaf {
		return ""
	}
	return bestKey
}

// editDistance computes the Levenshtein distance between two strings.
func editDistance(a, b string) int {
	la, lb := len(a), len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	// Single-row DP.
	prev := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr := make([]int, lb+1)
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			ins := curr[j-1] + 1
			del := prev[j] + 1
			sub := prev[j-1] + cost
			curr[j] = min(ins, min(del, sub))
		}
		prev = curr
	}
	return prev[lb]
}

// knownTOMLKeys returns a deduplicated, sorted list of all TOML key names
// used across the config structs. Built via reflection on struct tags.
func knownTOMLKeys() []string {
	seen := make(map[string]bool)
	types := []reflect.Type{
		reflect.TypeOf(City{}),
		reflect.TypeOf(Workspace{}),
		reflect.TypeOf(Agent{}),
		reflect.TypeOf(Rig{}),
		reflect.TypeOf(ProviderSpec{}),
		reflect.TypeOf(UpstreamSpec{}),
		reflect.TypeOf(UpstreamEnvBinding{}),
		reflect.TypeOf(AgentPatch{}),
		reflect.TypeOf(AgentOverride{}),
		reflect.TypeOf(BeadsConfig{}),
		reflect.TypeOf(BeadsWorkConfig{}),
		reflect.TypeOf(BeadPolicyConfig{}),
		reflect.TypeOf(BeadClassConfig{}),
		reflect.TypeOf(SessionConfig{}),
		reflect.TypeOf(MailConfig{}),
		reflect.TypeOf(EventsConfig{}),
		reflect.TypeOf(EventsRotationConfig{}),
		reflect.TypeOf(DoltConfig{}),
		reflect.TypeOf(FormulasConfig{}),
		reflect.TypeOf(DaemonConfig{}),
		reflect.TypeOf(OrdersConfig{}),
		reflect.TypeOf(APIConfig{}),
		reflect.TypeOf(ConvergenceConfig{}),
		reflect.TypeOf(Service{}),
		reflect.TypeOf(ServiceWorkflowConfig{}),
		reflect.TypeOf(ServiceProcessConfig{}),
		reflect.TypeOf(AgentDefaults{}),
		reflect.TypeOf(PackConfig{}),
		reflect.TypeOf(PackMeta{}),
		reflect.TypeOf(Import{}),
		reflect.TypeOf(NamedSession{}),
		reflect.TypeOf(PackRequirement{}),
		reflect.TypeOf(PackDoctorEntry{}),
		reflect.TypeOf(PackCommandEntry{}),
		reflect.TypeOf(PackRuntimeEntry{}),
		reflect.TypeOf(PackGlobal{}),
		reflect.TypeOf(PackDefaults{}),
		reflect.TypeOf(PackRigDefaults{}),
	}
	for _, t := range types {
		collectTOMLTags(t, seen)
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// collectTOMLTags extracts TOML key names from struct tags.
func collectTOMLTags(t reflect.Type, seen map[string]bool) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("toml")
		if tag == "" || tag == "-" {
			continue
		}
		// Parse "name,omitempty" → "name"
		name, _, _ := strings.Cut(tag, ",")
		if name != "" {
			seen[name] = true
		}
	}
}
