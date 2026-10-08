package beads

import "context"

// ExecCommandRunnerWithExactEntriesContext preserves a caller-owned raw ENV
// projection without merging the ambient process or changing ordinary bd
// execution, timeout, telemetry, transport/claim fencing, or error behavior.
func ExecCommandRunnerWithExactEntriesContext(ctx context.Context, entries []string) CommandRunner {
	owned := append([]string(nil), entries...)
	return execCommandRunner(ctx, nil, false, func() []string { return append([]string(nil), owned...) })
}
