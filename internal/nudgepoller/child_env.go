package nudgepoller

import (
	"strings"

	"github.com/gastownhall/gascity/internal/execenv"
)

// ChildEnv removes the spawning worker's identity and claim credential from a
// detached infrastructure poller. Its destination remains in CommandArgs, not
// ambient session identity. City context and unrelated entries are preserved.
// The session-scoped keys mirror managedDoltSessionScopedEnvKeys in cmd/gc;
// FilterInherited is deliberately broader and would also remove credentials
// needed by existing infrastructure backends.
func ChildEnv(parent []string) []string {
	out := make([]string, 0, len(parent))
	for _, entry := range parent {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "GC_SESSION_ID", "GC_SESSION_NAME", "GC_ALIAS", "GC_AGENT",
			"GC_TEMPLATE", "GC_SESSION_ORIGIN", "GC_RUNTIME_EPOCH",
			"GC_CONTINUATION_EPOCH", "GC_INSTANCE_TOKEN", "BEADS_HOLDER_TOKEN",
			"BEADS_ACTOR", "CLAUDE_CODE_SESSION_ID":
			continue
		default:
			out = append(out, entry)
		}
	}
	return execenv.WithUsageMetricsDisabled(out)
}
