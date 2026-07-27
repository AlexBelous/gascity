package rollout

import "github.com/gastownhall/gascity/internal/config"

// KeyBeadsHookClaimFastPath is the exported registry Key for the controller
// claim fast-path rollout gate, so composition-root code (cmd/gc, internal/api)
// can reference the gate without re-hardcoding the dotted string.
const KeyBeadsHookClaimFastPath = "beads.hook_claim_fastpath"

const keyBeadsHookClaimFastPath = KeyBeadsHookClaimFastPath

// HookClaimFastPath returns the resolved beads.hook_claim_fastpath value: true
// when a worker hook should route generated-default discovery and atomic claim
// through the controller's pooled store instead of a per-hook bd subprocess.
// Default false — the fast path is opt-in and a degraded (never-resolved) Flags
// runs the legacy subprocess path.
func (f Flags) HookClaimFastPath() bool {
	return f.hookClaimFastPath.value
}

// WithHookClaimFastPath overrides beads.hook_claim_fastpath on a ForTest Flags
// value.
func WithHookClaimFastPath(enabled bool) ForTestOption {
	return func(b *flagsBuilder) {
		b.flags.hookClaimFastPath = resolved[bool]{value: enabled, origin: OriginConfig}
	}
}

// readBeadsHookClaimFastPath reads cfg.Beads.HookClaimFastPath; a nil pointer
// means unset (the built-in default, false).
func readBeadsHookClaimFastPath(cfg *config.City) (value bool, defined bool) {
	if cfg.Beads.HookClaimFastPath == nil {
		return false, false
	}
	return *cfg.Beads.HookClaimFastPath, true
}
