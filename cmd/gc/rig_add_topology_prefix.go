package main

// Addendum E (engdocs/design/beads-work-topology.md, "Runtime aftermath" +
// "Config step"): on a UNIFIED or REMOTE city every rig's work beads live in one
// shared work DB, and a rig can only mint <prefix>-N when that DB's
// allowed_prefixes lists its prefix. So `gc rig add` on such a city must, for the
// new rig:
//
//  1. BEFORE creating it — validate the new prefix is pairwise-distinct from the
//     HQ prefix and every existing rig prefix (case-insensitive) and is not a
//     reserved coordination-class prefix, rejecting a collision with the pair.
//  2. AFTER creating it — register the prefix into the shared work DB's
//     allowed_prefixes via the transactional `bd config add-to-set`, then treat a
//     re-read as authoritative (the exact mechanism S8's
//     configStepRemoteAllowedPrefixes uses). A read-only / hard-blocked credential
//     surfaces the same actionable error rather than silently stranding the rig.
//
// On a SCOPED (non-unified, non-remote) city this is entirely DARK: each rig
// keeps its own DB, so rigAddSharesWorkDB returns false and nothing here runs —
// byte-identical to the pre-addendum behavior.
//
// This is the LOCAL add path, where the shared work DB is locally reachable. The
// remote `--git-url` control-plane provision registers the new prefix server-side
// (the CLI has no direct handle to the org DB there).

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/gastownhall/gascity/internal/config"
)

// rigAddSharesWorkDB reports whether the city routes every rig's work beads into
// one shared database — the [beads.work] scope=unified / target=remote config, OR
// a completed unify/remote marker (a marker present but config not yet reloaded is
// still shared). A marker stat/parse fault surfaces (fail closed), matching the
// one-way guard's discipline.
func rigAddSharesWorkDB(cityPath string, cfg *config.City) (bool, error) {
	if cfg != nil && (cfg.Beads.Work.IsUnified() || cfg.Beads.Work.IsRemote()) {
		return true, nil
	}
	topo, err := loadWorkTopology(cityPath)
	if err != nil {
		return false, fmt.Errorf("reading work-topology markers: %w", err)
	}
	return topo.sharesCityDatabase(), nil
}

// validateNewRigPrefixForSharedWorkDB rejects a prospective rig whose effective
// work prefix would collide (case-insensitive) with the HQ prefix or an existing
// rig prefix, or shadow a reserved class prefix, on a shared work DB — BEFORE the
// rig is created. It names the colliding scope so the operator can pick another.
func validateNewRigPrefixForSharedWorkDB(cfg *config.City, name, prefixOverride string) error {
	newRig := config.Rig{Name: name, Prefix: strings.TrimSpace(prefixOverride)}
	key := strings.ToLower(strings.TrimSpace(newRig.EffectivePrefix()))
	if key == "" {
		return fmt.Errorf("new rig %q resolves to an empty work prefix; pass --prefix", name)
	}
	if config.IsReservedClassPrefix(key) {
		return fmt.Errorf("new rig %q prefix %q shadows a reserved coordination-class id-prefix; choose a different --prefix", name, key)
	}
	owners := map[string]string{}
	record := func(scope, prefix string) {
		if p := strings.ToLower(strings.TrimSpace(prefix)); p != "" {
			owners[p] = scope
		}
	}
	record("the HQ scope", config.EffectiveHQPrefix(cfg))
	for i := range cfg.Rigs {
		// Exclude the rig being (re-)added from its own collision scan — an
		// idempotent re-add / adopt / retry-after-failed-registration of an
		// existing rig must NOT self-collide (Provision's own path is re-add-safe
		// via detectRigReAdd), or the only automated recovery for a stranded
		// unregistered prefix would be blocked. Matches detectRigReAdd's same-name
		// match (case-insensitive).
		if strings.EqualFold(strings.TrimSpace(cfg.Rigs[i].Name), strings.TrimSpace(name)) {
			continue
		}
		record(fmt.Sprintf("rig %q", cfg.Rigs[i].Name), cfg.Rigs[i].EffectivePrefix())
	}
	if owner, ok := owners[key]; ok {
		return fmt.Errorf("new rig %q prefix %q collides with %s on this unified/remote city — the shared work DB requires pairwise-distinct prefixes so minted ids stay attributable; choose a different --prefix", name, key, owner)
	}
	return nil
}

// registerNewRigPrefixInSharedWorkDB registers a freshly-added rig's prefix into
// the shared city work DB's allowed_prefixes (best-effort add-to-set, then an
// authoritative re-read), reusing the same bd seams S8's remote config step uses.
// A prefix still absent on the re-read is a hard error naming both causes, so a
// rig whose prefix is not registered is never left silently broken.
func registerNewRigPrefixInSharedWorkDB(cityPath, prefix string, stderr io.Writer) error {
	p := strings.ToLower(strings.TrimSpace(prefix))
	if p == "" {
		return nil
	}
	store, closeFn, err := openWorkUnifyScopeStore(cityPath, cityPath)
	if err != nil {
		return fmt.Errorf("registering rig prefix %q in the shared work DB: opening city work store: %w", p, err)
	}
	defer closeFn()

	writeErr := workRemoteAddPrefixToSet(store, p)
	present, err := workRemoteReadAllowedPrefixes(store)
	if err != nil {
		return fmt.Errorf("registering rig prefix %q: reading allowed_prefixes: %w", p, err)
	}
	if present[p] {
		fmt.Fprintf(stderr, "gc rig add: registered prefix %q into the shared work DB allowed_prefixes\n", p) //nolint:errcheck // best-effort stderr
		return nil
	}
	msg := fmt.Sprintf("rig prefix %q is still missing from the shared work DB allowed_prefixes after `bd config add-to-set` — either the controller credential lacks write access to the shared/org DB config (it needs a beads:write EIA and must not be hard_blocked / over-quota), or the prefix must be provisioned server-side (beads-web / beads-provisioner); the rig cannot mint %s-N until this is fixed", p, p)
	if writeErr != nil {
		return fmt.Errorf("%s (add-to-set error: %w)", msg, writeErr)
	}
	return errors.New(msg)
}
