package rollout

import "github.com/gastownhall/gascity/internal/config"

// KeyDaemonSessionStartReconciler is the registry key for boot-latched
// session-start ownership.
const KeyDaemonSessionStartReconciler = "daemon.session_start_reconciler"

const keyDaemonSessionStartReconciler = KeyDaemonSessionStartReconciler

// SessionStartReconciler returns the resolved daemon.session_start_reconciler
// mode.
func (f Flags) SessionStartReconciler() Mode {
	return f.sessionStartReconciler.value
}

// WithSessionStartReconciler overrides daemon.session_start_reconciler on a
// ForTest Flags value.
func WithSessionStartReconciler(mode Mode) ForTestOption {
	return func(b *flagsBuilder) {
		b.flags.sessionStartReconciler = resolved[Mode]{value: mode, origin: OriginConfig}
	}
}

func readDaemonSessionStartReconciler(cfg *config.City) (raw string, defined bool) {
	raw = cfg.Daemon.SessionStartReconciler
	return raw, raw != ""
}
