package main

import "strings"

// Explicit operation, never an unknown managed context fallback. Keep tool
// attribution/city/supervisor signal and unrelated multiplicity exactly as
// supplied. Do not reuse withoutSessionIdentityEnv, which removes BEADS_ACTOR.
func projectBDInfrastructureChild(op bdChildOperation, raw []string) ([]string, error) {
	if op != bdChildInfrastructure || len(raw) > 4096 {
		return nil, errBDChildAuthority
	}
	out := make([]string, 0, len(raw))
	total := 0
	for _, entry := range raw {
		total += len(entry)
		key, _, ok := strings.Cut(entry, "=")
		if total > 16<<20 || !ok || key == "" || strings.ContainsRune(entry, 0) {
			return nil, errBDChildEnvironment
		}
		switch key {
		case "GC_SESSION_ID", "GC_TEMPLATE", "GC_RUNTIME_EPOCH", "GC_INSTANCE_TOKEN", "BEADS_HOLDER_TOKEN":
			continue
		default:
			out = append(out, entry)
		}
	}
	return out, nil
}
