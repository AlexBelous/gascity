package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gastownhall/gascity/internal/agentutil"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/session"
)

const (
	namedSessionMetadataKey      = session.NamedSessionMetadataKey
	namedSessionIdentityMetadata = session.NamedSessionIdentityMetadata
	namedSessionModeMetadata     = session.NamedSessionModeMetadata
)

type namedSessionSpec = session.NamedSessionSpec

func normalizeNamedSessionTarget(target string) string {
	return session.NormalizeNamedSessionTarget(target)
}

func targetBasename(target string) string {
	return session.TargetBasename(target)
}

func findNamedSessionSpec(cfg *config.City, cityName, identity string) (namedSessionSpec, bool) {
	return session.FindNamedSessionSpec(cfg, cityName, identity)
}

func namedSessionBackingTemplate(spec namedSessionSpec) string {
	return session.NamedSessionBackingTemplate(spec)
}

func resolveNamedSessionSpecForConfigTarget(cfg *config.City, cityName, target, rigContext string) (namedSessionSpec, bool, error) {
	return session.ResolveNamedSessionSpecForConfigTarget(cfg, cityName, target, rigContext)
}

func findNamedSessionSpecForTarget(cfg *config.City, cityName, target string) (namedSessionSpec, bool, error) {
	return session.FindNamedSessionSpecForTarget(cfg, cityName, target, currentRigContext(cfg))
}

func isNamedSessionBead(b beads.Bead) bool {
	return session.IsNamedSessionBead(b)
}

// isNamedSessionInfo is the session.Info mirror of isNamedSessionBead:
// session.IsNamedSessionBead reads the trimmed configured_named_session flag,
// which Info.ConfiguredNamedSession already projects identically.
func isNamedSessionInfo(i session.Info) bool {
	return i.ConfiguredNamedSession
}

func namedSessionIdentity(b beads.Bead) string {
	return session.NamedSessionIdentity(b)
}

// namedSessionIdentityInfo is the session.Info mirror of namedSessionIdentity:
// session.NamedSessionIdentityInfo reads the trimmed configured_named_identity,
// which Info.ConfiguredNamedIdentity carries verbatim.
func namedSessionIdentityInfo(i session.Info) string {
	return session.NamedSessionIdentityInfo(i)
}

func configuredNamedSessionBeadHasSpec(b beads.Bead, cfg *config.City, cityName string) bool {
	if cfg == nil || !isNamedSessionBead(b) {
		return false
	}
	identity := namedSessionIdentity(b)
	if identity == "" {
		return false
	}
	_, ok := findNamedSessionSpec(cfg, cityName, identity)
	return ok
}

// configuredNamedSessionBeadHasSpecInfo is the session.Info mirror of
// configuredNamedSessionBeadHasSpec: isNamedSessionInfo and namedSessionIdentityInfo
// are the equivalence-proven siblings, and findNamedSessionSpec keys off the
// projected identity string identically.
func configuredNamedSessionBeadHasSpecInfo(i session.Info, cfg *config.City, cityName string) bool {
	if cfg == nil || !isNamedSessionInfo(i) {
		return false
	}
	identity := namedSessionIdentityInfo(i)
	if identity == "" {
		return false
	}
	_, ok := findNamedSessionSpec(cfg, cityName, identity)
	return ok
}

func namedSessionMode(b beads.Bead) string {
	return session.NamedSessionMode(b)
}

// namedSessionModeInfo is the session.Info mirror of namedSessionMode:
// session.NamedSessionModeInfo trims the raw configured_named_mode
// (Info.ConfiguredNamedMode), identical to the bead form.
func namedSessionModeInfo(i session.Info) string {
	return session.NamedSessionModeInfo(i)
}

func namedSessionContinuityEligible(b beads.Bead) bool {
	return session.NamedSessionContinuityEligible(b)
}

func findCanonicalNamedSessionInfo(sessionBeads *sessionBeadSnapshot, spec namedSessionSpec) (session.Info, bool) {
	if sessionBeads == nil {
		return session.Info{}, false
	}
	return session.FindCanonicalNamedSessionInfo(sessionBeads.OpenInfos(), spec)
}

// findClosedNamedSessionBead searches for a closed bead that was previously
// the canonical bead for the given named session identity. Uses a targeted
// metadata query (Store.ListByMetadata) so only matching beads are returned
// — no bulk scan of all closed beads.
func findClosedNamedSessionBead(store beads.Store, identity string) (beads.Bead, bool) {
	bead, ok, _ := session.FindClosedNamedSessionBead(store, identity)
	return bead, ok
}

func findClosedNamedSessionBeadForSessionName(store beads.Store, identity, sessionName string) (beads.Bead, bool) {
	bead, ok, _ := session.FindClosedNamedSessionBeadForSessionName(store, identity, sessionName)
	return bead, ok
}

func findNamedSessionConflictInfo(sessionBeads *sessionBeadSnapshot, spec namedSessionSpec) (session.Info, bool) {
	if sessionBeads == nil {
		return session.Info{}, false
	}
	return session.FindNamedSessionConflictInfo(sessionBeads.OpenInfos(), spec)
}

func findConflictingNamedSessionSpecForBead(cfg *config.City, cityName string, b beads.Bead) (namedSessionSpec, bool, error) {
	return session.FindConflictingNamedSessionSpecForBead(cfg, cityName, b)
}

// resolvePoolShapedNamedSessionInstance checks whether target names a numbered
// pool instance ("<identity>-N") of a pool-shaped configured named session —
// one whose backing agent has pool (not singleton) capacity and mode !=
// "always" (config.IsPoolShapedNamedSession). It mirrors
// ResolveNamedSessionSpecForConfigTarget's qualified and bare-leaf match
// shapes, then delegates slot-range validation to the existing pool-instance
// resolver family via agentutil.ResolveAgent so no second range-check parser
// is introduced.
//
// A structural match ("<identity>-<suffix>") whose suffix fails to parse or
// falls out of range returns ok=false with a non-nil error naming the bad
// target, so callers can surface a clear error instead of silently falling
// through to an unrelated resolution path. A target that doesn't structurally
// match any pool-shaped named session returns ok=false, err=nil.
func resolvePoolShapedNamedSessionInstance(cfg *config.City, cityName, target string) (namedSessionSpec, bool, error) {
	target = normalizeNamedSessionTarget(target)
	if cfg == nil || target == "" {
		return namedSessionSpec{}, false, nil
	}

	qualified := strings.ContainsAny(target, "/.")
	rigContext := currentRigContext(cfg)

	for i := range cfg.NamedSessions {
		ns := &cfg.NamedSessions[i]
		identity := ns.QualifiedName()
		spec, ok := findNamedSessionSpec(cfg, cityName, identity)
		if !ok || !config.IsPoolShapedNamedSession(spec.Agent, spec.Named) {
			continue
		}

		suffix, matched := strings.CutPrefix(target, identity+"-")
		if !matched && !qualified && (ns.Dir == "" || (rigContext != "" && ns.Dir == rigContext)) {
			suffix, matched = strings.CutPrefix(target, ns.IdentityName()+"-")
		}
		if !matched {
			continue
		}

		n, err := strconv.Atoi(suffix)
		if err != nil || n < 1 {
			return namedSessionSpec{}, false, fmt.Errorf("%w: %q is not a valid pool instance slot for named session %q", session.ErrSessionNotFound, target, identity)
		}

		instanceTarget := spec.Agent.QualifiedName() + "-" + suffix
		instanceAgent, ok := agentutil.ResolveAgent(cfg, instanceTarget, agentutil.ResolveOpts{AllowPoolMembers: true})
		if !ok {
			return namedSessionSpec{}, false, fmt.Errorf("%w: %q is out of range for pool-shaped named session %q", session.ErrSessionNotFound, target, identity)
		}

		// Re-stamp the synthesized instance's identity fields onto the named
		// session's own identity rather than the backing template's: when
		// NamedSession.Name is set and differs from Template (its documented
		// purpose), agentutil.ResolveAgent above validated the slot against
		// the template's QualifiedName() and so returns an agent identified
		// by "<template>-N", not "<name>-N". Downstream materialization
		// (materializeSessionForAgentConfig) creates the session under
		// whatever identity this Agent carries, so leaving it template-keyed
		// would create/address the session under the wrong name. Only the
		// identity fields are overridden; capacity/provider/start-command
		// fields stay as resolved from the backing template.
		bareIdentity := ns.Name
		if bareIdentity == "" {
			bareIdentity = ns.Template
		}
		instanceAgent.BindingName = ns.BindingName
		instanceAgent.Name = bareIdentity + "-" + suffix
		instanceAgent.Dir = ns.Dir

		instanceIdentity := identity + "-" + suffix
		return namedSessionSpec{
			Named:       spec.Named,
			Agent:       &instanceAgent,
			Identity:    instanceIdentity,
			SessionName: config.NamedSessionRuntimeName(cityName, cfg.Workspace, instanceIdentity),
			Mode:        spec.Mode,
		}, true, nil
	}

	return namedSessionSpec{}, false, nil
}
