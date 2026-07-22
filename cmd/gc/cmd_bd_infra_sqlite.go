package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

// This file routes reserved-prefix bd write mutations to the split city's
// embedded SQLite infra scope IN-PROCESS instead of exec'ing the external `bd`
// binary. bd 1.1.0 removed its SQLite backend, so an exec against the embedded
// graph/infra store does not speak that store and silently resolves a DIFFERENT
// workspace (the city Dolt store) — the same failure the control-ready reader
// routes around in dispatch_control_ready.go:373-378. The pack-used mutations on
// gcg- ids are `bd close <id>` and `bd update <id> --set-metadata …` (e.g.
// review.verdict / ci.verdict / gc.work_dir), so those land in the embedded
// store here and emit the canonical bead.* event through the same emission seam
// the in-process CLI paths use (classStoreWithCLIEmission). Every other
// subcommand keeps its current behavior (the caller falls through to the exec
// path), so single-store cities and non-infra scopes are byte-identical.

// maybeRouteBdInfraSqliteMutation intercepts a bd write on a split city's
// embedded-sqlite infra scope and applies it in-process. It reports handled=true
// (with the process exit code) when it took ownership of the command; handled=
// false means the caller must proceed with its normal (exec) path. It only fires
// for the sqlite-backed infra scope; a Dolt-backed infra scope keeps exec'ing bd.
func maybeRouteBdInfraSqliteMutation(cityPath string, cfg *config.City, target execStoreTarget, bdArgs []string, stdout, stderr io.Writer) (int, bool) {
	if target.ScopeKind != "infra" || !cityInfraScopeIsSQLite(cityPath) {
		return 0, false
	}
	if len(bdArgs) == 0 {
		return 0, false
	}
	switch bdArgs[0] {
	case "close":
		return doBdInfraSqliteClose(cityPath, cfg, target, bdArgs[1:], stdout, stderr), true
	case "update":
		return doBdInfraSqliteUpdate(cityPath, cfg, target, bdArgs[1:], stdout, stderr), true
	default:
		// Reads (show/list/…) and other subcommands keep current behavior. bd
		// cannot reach the embedded store, but the pack-used writes are the two
		// mutating subcommands handled above; the rest fall through unchanged.
		return 0, false
	}
}

// openBdInfraSqliteEmittingStore opens the embedded sqlite infra store and wraps
// it in the CLI emission seam so a landed mutation appends the canonical bead.*
// event. It is the in-process analog of cliGraphStore over the infra scope.
func openBdInfraSqliteEmittingStore(cityPath string, target execStoreTarget) (beads.Store, error) {
	store, err := openStoreAtForCity(target.ScopeRoot, cityPath)
	if err != nil {
		return nil, err
	}
	return classStoreWithCLIEmission(store, cityPath), nil
}

// doBdInfraSqliteClose closes each positional bead id in-process. It preserves
// bd's `--reason` by stamping the close_reason metadata before the close, and
// rejects any other value flag rather than silently dropping it.
func doBdInfraSqliteClose(cityPath string, cfg *config.City, target execStoreTarget, args []string, stdout, stderr io.Writer) int {
	_ = cfg
	ids, reason, err := parseBdInfraCloseArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "gc bd: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	if len(ids) == 0 {
		fmt.Fprintln(stderr, "gc bd: usage: close <id> [<id>...]") //nolint:errcheck // best-effort stderr
		return 1
	}
	store, err := openBdInfraSqliteEmittingStore(cityPath, target)
	if err != nil {
		fmt.Fprintf(stderr, "gc bd: opening embedded sqlite infra store: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	for _, id := range ids {
		if reason != "" {
			if err := store.SetMetadata(id, "close_reason", reason); err != nil {
				fmt.Fprintf(stderr, "gc bd: close %s (embedded sqlite infra store): setting close_reason: %v\n", id, err) //nolint:errcheck // best-effort stderr
				return 1
			}
		}
		if err := store.Close(id); err != nil {
			fmt.Fprintf(stderr, "gc bd: close %s (embedded sqlite infra store): %v\n", id, err) //nolint:errcheck // best-effort stderr
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
func doBdInfraSqliteUpdate(cityPath string, cfg *config.City, target execStoreTarget, args []string, stdout, stderr io.Writer) int {
	_ = cfg
	id, opts, err := parseBdInfraUpdateArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "gc bd: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	store, err := openBdInfraSqliteEmittingStore(cityPath, target)
	if err != nil {
		fmt.Fprintf(stderr, "gc bd: opening embedded sqlite infra store: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	if err := store.Update(id, opts); err != nil {
		fmt.Fprintf(stderr, "gc bd: update %s (embedded sqlite infra store): %v\n", id, err) //nolint:errcheck // best-effort stderr
		return 1
	}
	fmt.Fprintf(stdout, "updated %s\n", id) //nolint:errcheck // best-effort stdout
	return 0
}

// parseBdInfraCloseArgs extracts the positional ids and the optional --reason
// from bd close args. Any other value flag is an error so it is not silently
// dropped by the in-process path.
func parseBdInfraCloseArgs(args []string) (ids []string, reason string, err error) {
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
			return nil, "", fmt.Errorf("close: flag %q is not supported for the embedded sqlite infra scope (bd cannot reach it)", name)
		}
	}
	return ids, reason, nil
}

// parseBdInfraUpdateArgs maps a bd update command's positional id and supported
// value flags onto beads.UpdateOpts. Exactly one id is required. Supported
// flags: --set-metadata key=value (repeatable), --status/-s, --assignee/-a. Any
// other flag is rejected.
func parseBdInfraUpdateArgs(args []string) (string, beads.UpdateOpts, error) {
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
			return "", opts, fmt.Errorf("update: flag %q is not supported for the embedded sqlite infra scope (bd cannot reach it)", name)
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
