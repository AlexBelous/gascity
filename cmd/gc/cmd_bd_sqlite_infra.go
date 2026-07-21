package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
)

// gc bd verbs served in-process against the embedded sqlite infra store.
//
// bd cannot open the embedded store: an exec against the infra scope either
// hangs or silently resolves a DIFFERENT workspace (bd walks up from the
// scope and finds the city Dolt store), so by-id mutations report "no issue
// found" and reads return wrong-store rows. That broke the worker step-close
// contract (`gc bd update <step> --set-metadata … --status closed`) for every
// infra-resident bead (ga-zeex2). The common by-id verbs are translated to
// Store calls here; anything unrecognized fails loud with the tracked-bug
// pointer instead of silently misrouting.

// maybeDoBdSQLiteInfra intercepts a gc bd invocation whose subject bead lives
// in the embedded sqlite infra store. handled=false falls through to the
// normal bd exec path.
func maybeDoBdSQLiteInfra(cityPath string, target execStoreTarget, args []string, stdout, stderr io.Writer) (int, bool) {
	if !cityInfraScopeIsSQLite(cityPath) {
		return 0, false
	}
	verb, id, rest := splitBdByIDVerb(args)
	if verb == "" || id == "" {
		return 0, false
	}
	scope := infraScopeRoot(cityPath)
	st, err := beads.OpenSQLiteStore(
		filepath.Join(scope, ".beads"),
		beads.WithSQLiteStoreIDPrefix(readScopeIssuePrefix(scope)),
	)
	if err != nil {
		fmt.Fprintf(stderr, "gc bd: opening embedded sqlite infra store: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1, true
	}
	defer func() { _ = closeBeadStoreHandle(st) }()

	// Residence decides ownership: reserved-class ids always live here; a
	// migrated legacy-prefix bead (ga-wisp-…, mc-wisp-…) is claimed by
	// point-read. A miss falls through to the normal (bd) routing so
	// work-store beads keep their existing path.
	bead, getErr := st.Get(id)
	if getErr != nil {
		if errors.Is(getErr, beads.ErrNotFound) && target.ScopeKind != "infra" {
			return 0, false
		}
		fmt.Fprintf(stderr, "gc bd: %s %s (embedded sqlite infra store): %v\n", verb, id, getErr) //nolint:errcheck // best-effort stderr
		return 1, true
	}

	switch verb {
	case "show":
		out, mErr := json.MarshalIndent([]beads.Bead{bead}, "", "  ")
		if mErr != nil {
			fmt.Fprintf(stderr, "gc bd show: %v\n", mErr) //nolint:errcheck // best-effort stderr
			return 1, true
		}
		fmt.Fprintln(stdout, string(out)) //nolint:errcheck // best-effort stdout
		return 0, true
	case "update":
		return doBdSQLiteInfraUpdate(st, id, rest, stdout, stderr), true
	case "close":
		return doBdSQLiteInfraClose(st, id, rest, stdout, stderr), true
	default:
		fmt.Fprintf(stderr, "gc bd: verb %q on infra-resident bead %s is not yet served for the embedded sqlite store (ga-zeex2); use show/update/close or the gc-native surface\n", verb, id) //nolint:errcheck // best-effort stderr
		return 1, true
	}
}

// splitBdByIDVerb recognizes the by-id shapes `bd <verb> <id> [flags…]` for
// the served verbs. Returns "" verb for anything else (list/ready/query/dep/…),
// which keeps those on the existing routing.
func splitBdByIDVerb(args []string) (verb, id string, rest []string) {
	var positionals []int
	for i, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		positionals = append(positionals, i)
		if len(positionals) == 2 {
			break
		}
	}
	if len(positionals) < 2 {
		return "", "", nil
	}
	v := args[positionals[0]]
	switch v {
	case "show", "update", "close":
	default:
		return "", "", nil
	}
	out := make([]string, 0, len(args)-2)
	for i, a := range args {
		if i == positionals[0] || i == positionals[1] {
			continue
		}
		out = append(out, a)
	}
	return v, args[positionals[1]], out
}

func doBdSQLiteInfraUpdate(st beads.Store, id string, flags []string, stdout, stderr io.Writer) int {
	opts := beads.UpdateOpts{}
	meta := map[string]string{}
	claim := false
	for i := 0; i < len(flags); i++ {
		f := flags[i]
		val := func() (string, bool) {
			if eq := strings.IndexByte(f, '='); eq >= 0 && strings.HasPrefix(f, "--") && strings.Contains(f[:eq], "-") {
				return f[eq+1:], true
			}
			if i+1 < len(flags) {
				i++
				return flags[i], true
			}
			return "", false
		}
		switch {
		case f == "--claim":
			claim = true
		case f == "--force" || f == "--json" || f == "--sandbox" || f == "--readonly":
			// accepted no-ops for contract-command parity
		case f == "--set-metadata" || strings.HasPrefix(f, "--set-metadata="):
			v, ok := val()
			if !ok {
				fmt.Fprintln(stderr, "gc bd update: --set-metadata requires k=v") //nolint:errcheck // best-effort stderr
				return 1
			}
			kv := strings.SplitN(v, "=", 2)
			if len(kv) != 2 {
				fmt.Fprintf(stderr, "gc bd update: bad --set-metadata %q\n", v) //nolint:errcheck // best-effort stderr
				return 1
			}
			meta[kv[0]] = kv[1]
		case f == "--status" || strings.HasPrefix(f, "--status="):
			v, ok := val()
			if !ok {
				fmt.Fprintln(stderr, "gc bd update: --status requires a value") //nolint:errcheck // best-effort stderr
				return 1
			}
			opts.Status = &v
		case f == "--assignee" || strings.HasPrefix(f, "--assignee="):
			v, ok := val()
			if !ok {
				fmt.Fprintln(stderr, "gc bd update: --assignee requires a value") //nolint:errcheck // best-effort stderr
				return 1
			}
			opts.Assignee = &v
		default:
			fmt.Fprintf(stderr, "gc bd update: flag %q not served for embedded sqlite infra beads (ga-zeex2); supported: --claim --set-metadata --status --assignee --force\n", f) //nolint:errcheck // best-effort stderr
			return 1
		}
	}
	if claim {
		assignee := strings.TrimSpace(os.Getenv("BEADS_ACTOR"))
		if assignee == "" {
			assignee = strings.TrimSpace(os.Getenv("GC_SESSION_NAME"))
		}
		if assignee == "" {
			fmt.Fprintln(stderr, "gc bd update --claim: no BEADS_ACTOR/GC_SESSION_NAME in env") //nolint:errcheck // best-effort stderr
			return 1
		}
		claimer, ok := st.(interface {
			Claim(id, assignee string) (beads.Bead, bool, error)
		})
		if !ok {
			fmt.Fprintln(stderr, "gc bd update --claim: store has no native claim") //nolint:errcheck // best-effort stderr
			return 1
		}
		if _, won, cErr := claimer.Claim(id, assignee); cErr != nil {
			fmt.Fprintf(stderr, "gc bd update --claim: %v\n", cErr) //nolint:errcheck // best-effort stderr
			return 1
		} else if !won {
			fmt.Fprintf(stderr, "gc bd update --claim: %s already held by another assignee\n", id) //nolint:errcheck // best-effort stderr
			return 1
		}
	}
	if len(meta) > 0 {
		opts.Metadata = meta
	}
	if opts.Status != nil || opts.Assignee != nil || opts.Metadata != nil {
		if err := st.Update(id, opts); err != nil {
			fmt.Fprintf(stderr, "gc bd update: %v\n", err) //nolint:errcheck // best-effort stderr
			return 1
		}
	}
	fmt.Fprintf(stdout, "✓ Updated issue: %s\n", id) //nolint:errcheck // best-effort stdout
	return 0
}

func doBdSQLiteInfraClose(st beads.Store, id string, flags []string, stdout, stderr io.Writer) int {
	meta := map[string]string{}
	for i := 0; i < len(flags); i++ {
		f := flags[i]
		switch {
		case f == "--reason" || strings.HasPrefix(f, "--reason="):
			v := strings.TrimPrefix(f, "--reason=")
			if v == f { // separate-arg form
				if i+1 < len(flags) {
					i++
					v = flags[i]
				} else {
					v = ""
				}
			}
			if v != "" {
				meta["close_reason"] = v
			}
		case f == "--force" || f == "--json" || f == "--sandbox" || f == "--readonly" || f == "--continue":
			// --continue consumes a value in bd; accept and skip it.
			if f == "--continue" && i+1 < len(flags) && !strings.HasPrefix(flags[i+1], "-") {
				i++
			}
		case strings.HasPrefix(f, "--set-metadata"):
			v := strings.TrimPrefix(f, "--set-metadata=")
			if v == f {
				if i+1 < len(flags) {
					i++
					v = flags[i]
				}
			}
			kv := strings.SplitN(v, "=", 2)
			if len(kv) == 2 {
				meta[kv[0]] = kv[1]
			}
		default:
			fmt.Fprintf(stderr, "gc bd close: flag %q not served for embedded sqlite infra beads (ga-zeex2)\n", f) //nolint:errcheck // best-effort stderr
			return 1
		}
	}
	if len(meta) > 0 {
		if err := st.Update(id, beads.UpdateOpts{Metadata: meta}); err != nil {
			fmt.Fprintf(stderr, "gc bd close: recording metadata: %v\n", err) //nolint:errcheck // best-effort stderr
			return 1
		}
	}
	if err := st.Close(id); err != nil {
		fmt.Fprintf(stderr, "gc bd close: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	fmt.Fprintf(stdout, "✓ Closed %s\n", id) //nolint:errcheck // best-effort stdout
	return 0
}
