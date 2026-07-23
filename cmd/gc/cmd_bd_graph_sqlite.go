package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

// This file routes gcg-prefixed bd write mutations to the routed city's
// embedded SQLite graph store IN-PROCESS instead of exec'ing the external
// `bd` binary — bd 1.1.0 removed its SQLite backend, so an exec against the
// embedded graph store does not speak that store and silently resolves a
// DIFFERENT workspace (the city Dolt store). Ported from the win3-hardened
// integ lineage (2c74f8747 cmd_bd_infra_sqlite.go), adapted from its
// infra-scope gating (metadata.json backend probe) to this branch's
// marker+knob routing. The pack-used mutations on gcg- ids are
// `bd close <id> [--reason]` and `bd update <id> --set-metadata/--status/
// --assignee` (review.verdict / ci.verdict / claim-shaped assignee stamps),
// so those land in the embedded store here. Graph is bead.*-event-silent by
// design on this branch, so no emission wrap. Everything else falls through
// to doBd's normal path, where the write guard refuses reserved-prefix
// mutations — this arm must run BEFORE the guard so routed cities keep
// their legitimate graph writes.

// maybeRouteBdInfraSqliteMutation intercepts a bd write on a split city's
// embedded-sqlite infra scope and applies it in-process. It reports handled=true
// (with the process exit code) when it took ownership of the command; handled=
// false means the caller must proceed with its normal (exec) path. It only fires
// for the sqlite-backed infra scope; a Dolt-backed infra scope keeps exec'ing bd.
func maybeRouteBdGraphSqliteMutation(cityPath string, cfg *config.City, bdArgs []string, stdout, stderr io.Writer) (int, bool) {
	if len(bdArgs) == 0 {
		return 0, false
	}
	switch bdArgs[0] {
	case "close", "update":
	default:
		return 0, false
	}
	// Cheap gate: only commands mentioning a gcg-prefixed arg are this arm's
	// business (a flag VALUE cannot be told from a positional without the
	// parse below, so the precise all-ids-are-graph check happens after
	// parsing, where a mixed id set errors loudly rather than splitting).
	prefix, _ := config.ReservedClassPrefix(config.BeadClassGraph)
	sawGraphID := false
	for _, a := range bdArgs[1:] {
		if strings.HasPrefix(a, prefix+"-") {
			sawGraphID = true
			break
		}
	}
	if !sawGraphID {
		return 0, false
	}
	routed, err := graphSQLiteRoutingActive(cityPath, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "gc bd: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1, true
	}
	if !routed {
		return 0, false // unrouted: the write guard downstream refuses
	}
	switch bdArgs[0] {
	case "close":
		return doBdGraphSqliteClose(cityPath, bdArgs[1:], stdout, stderr), true
	case "update":
		return doBdGraphSqliteUpdate(cityPath, bdArgs[1:], stdout, stderr), true
	default:
		// Reads (show/list/…) and other subcommands keep current behavior. bd
		// cannot reach the embedded store, but the pack-used writes are the two
		// mutating subcommands handled above; the rest fall through unchanged.
		return 0, false
	}
}

// doBdInfraSqliteClose closes each positional bead id in-process. It preserves
// bd's `--reason` by stamping the close_reason metadata before the close, and
// rejects any other value flag rather than silently dropping it.
func doBdGraphSqliteClose(cityPath string, args []string, stdout, stderr io.Writer) int {
	ids, reason, err := parseBdGraphCloseArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "gc bd: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	if len(ids) == 0 {
		fmt.Fprintln(stderr, "gc bd: usage: close <id> [<id>...]") //nolint:errcheck // best-effort stderr
		return 1
	}
	if err := requireAllGraphIDs(ids); err != nil {
		fmt.Fprintf(stderr, "gc bd: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	store, err := graphClassStoreFor(cityPath)
	if err != nil {
		fmt.Fprintf(stderr, "gc bd: opening embedded graph store: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	for _, id := range ids {
		if reason != "" {
			if err := store.SetMetadata(id, "close_reason", reason); err != nil {
				fmt.Fprintf(stderr, "gc bd: close %s (embedded graph store): setting close_reason: %v\n", id, err) //nolint:errcheck // best-effort stderr
				return 1
			}
		}
		if err := store.Close(id); err != nil {
			fmt.Fprintf(stderr, "gc bd: close %s (embedded graph store): %v\n", id, err) //nolint:errcheck // best-effort stderr
			return 1
		}
		fmt.Fprintf(stdout, "closed %s\n", id) //nolint:errcheck // best-effort stdout
	}
	return 0
}

// doBdInfraSqliteUpdate applies an update to a single positional bead id
// in-process, mapping the supported bd update flags (--set-metadata, --status,
// --assignee) onto beads.UpdateOpts. An unsupported update flag is rejected
// loudly rather than exec'd (which would silently misroute the write).
func doBdGraphSqliteUpdate(cityPath string, args []string, stdout, stderr io.Writer) int {
	id, opts, err := parseBdGraphUpdateArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "gc bd: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	store, err := graphClassStoreFor(cityPath)
	if err != nil {
		fmt.Fprintf(stderr, "gc bd: opening embedded graph store: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	if err := requireAllGraphIDs([]string{id}); err != nil {
		fmt.Fprintf(stderr, "gc bd: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	if err := store.Update(id, opts); err != nil {
		fmt.Fprintf(stderr, "gc bd: update %s (embedded graph store): %v\n", id, err) //nolint:errcheck // best-effort stderr
		return 1
	}
	fmt.Fprintf(stdout, "updated %s\n", id) //nolint:errcheck // best-effort stdout
	return 0
}

// parseBdGraphCloseArgs extracts the positional ids and the optional --reason
// from bd close args. Any other value flag is an error so it is not silently
// dropped by the in-process path.
func parseBdGraphCloseArgs(args []string) (ids []string, reason string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			ids = append(ids, a)
			continue
		}
		name, val, hasEq := strings.Cut(a, "=")
		take := func() (string, bool) {
			if hasEq {
				return val, true
			}
			if i+1 < len(args) {
				i++
				return args[i], true
			}
			return "", false
		}
		switch name {
		case "--reason", "-r":
			v, ok := take()
			if !ok {
				return nil, "", fmt.Errorf("close: flag %s requires a value", name)
			}
			reason = v
		case "--session":
			// bd stamps the acting session; the in-process store does not need it.
			if _, ok := take(); !ok {
				return nil, "", fmt.Errorf("close: flag %s requires a value", name)
			}
		default:
			return nil, "", fmt.Errorf("close: flag %q is not supported for the embedded graph store (bd cannot reach it)", name)
		}
	}
	return ids, reason, nil
}

// parseBdGraphUpdateArgs maps a bd update command's positional id and supported
// value flags onto beads.UpdateOpts. Exactly one id is required. Supported
// flags: --set-metadata key=value (repeatable), --status/-s, --assignee/-a. Any
// other flag is rejected.
func parseBdGraphUpdateArgs(args []string) (string, beads.UpdateOpts, error) {
	var ids []string
	var opts beads.UpdateOpts
	metadata := map[string]string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			ids = append(ids, a)
			continue
		}
		name, val, hasEq := strings.Cut(a, "=")
		take := func() (string, bool) {
			if hasEq {
				return val, true
			}
			if i+1 < len(args) {
				i++
				return args[i], true
			}
			return "", false
		}
		switch name {
		case "--set-metadata":
			v, ok := take()
			if !ok {
				return "", opts, fmt.Errorf("update: flag --set-metadata requires a value")
			}
			key, value, hasKV := strings.Cut(v, "=")
			if !hasKV || strings.TrimSpace(key) == "" {
				return "", opts, fmt.Errorf("update: --set-metadata expects key=value, got %q", v)
			}
			metadata[key] = value
		case "--status", "-s":
			v, ok := take()
			if !ok {
				return "", opts, fmt.Errorf("update: flag %s requires a value", name)
			}
			status := v
			opts.Status = &status
		case "--assignee", "-a":
			v, ok := take()
			if !ok {
				return "", opts, fmt.Errorf("update: flag %s requires a value", name)
			}
			assignee := v
			opts.Assignee = &assignee
		default:
			return "", opts, fmt.Errorf("update: flag %q is not supported for the embedded graph store (bd cannot reach it)", name)
		}
	}
	if len(ids) != 1 {
		return "", opts, fmt.Errorf("update: expected exactly one bead id, got %d", len(ids))
	}
	if len(metadata) > 0 {
		opts.Metadata = metadata
	}
	return ids[0], opts, nil
}

// requireAllGraphIDs rejects a graph-arm mutation whose id set mixes graph
// and non-graph ids: splitting one command across two stores can never be
// right, and silently dropping the foreign ids would be worse.
func requireAllGraphIDs(ids []string) error {
	prefix, _ := config.ReservedClassPrefix(config.BeadClassGraph)
	for _, id := range ids {
		if !strings.HasPrefix(id, prefix+"-") {
			return fmt.Errorf("mixed id set: %q is not a graph-class (%s-) id; split the command per store", id, prefix)
		}
	}
	return nil
}
