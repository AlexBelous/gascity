package main

// Generalized `gc bd show` read federation over the relocated class stores
// (engdocs/design/infra-class-sqlite-stores.md, "The bd / raw-prompt story"
// item 4). The lineage is 124bca8c3's graph-only maybeRouteBdShowViaAPI —
// the read companion of the write guard — widened from BeadClassGraph to a
// loop over config.ReservedClassPrefixes(), and backed by direct per-class
// store reads instead of the controller API (the class stores are embedded
// files any process may read; no controller needs to be up).
//
// Two arms:
//
//   - A reserved-prefix id (gco-/gcn-/gcm-/gcs-) can live ONLY in its class
//     store, so it is served from there — an absent store file or row is
//     genuine absence ("no issue found", bd's shape), while a store failure
//     surfaces distinctly. The 404-vs-error discipline is the root-loss
//     lesson: a liveness probe that mis-reads a hard failure as absence
//     destroys the live thing it was checking.
//   - A MIGRATED legacy id (gc-*/mc-*, imported with its bd id preserved)
//     matches no reserved prefix, so before falling through to bd the ROUTED
//     class stores are probed by id — the storeref.Resolve probe-all shape.
//     A hit renders locally (bd swept its copy); a clean miss falls through
//     to the byte-identical bd passthrough. Unrouted classes are never
//     probed (bd is still their truth). Graph is not relocated in this tree
//     and always falls through.
//
// Reads only; writes stay guarded (cmd_bd_guard.go).

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
	messagingdb "github.com/gastownhall/gascity/internal/classdb/messaging"
	nudgesdb "github.com/gastownhall/gascity/internal/classdb/nudges"
	sessionsdb "github.com/gastownhall/gascity/internal/classdb/sessions"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/mail/beadmail"
	"github.com/gastownhall/gascity/internal/orders"
)

// maybeRouteBdShowLocal serves `gc bd show <id> [--json]` from the embedded
// class stores when the id belongs there. Returns handled=false to fall
// through to the byte-identical bd passthrough.
func maybeRouteBdShowLocal(cityPath string, cfg *config.City, bdArgs []string, stdout, stderr io.Writer) (int, bool) {
	id, jsonOut, ok := bdShowRoutable(bdArgs)
	if !ok {
		return 0, false
	}
	if class, reserved := reservedClassForBeadID(id); reserved {
		if class == config.BeadClassGraph {
			return 0, false // graph is not relocated in this tree; bd owns it
		}
		exists, err := classStoreFileExists(cityPath, class)
		if err != nil {
			fmt.Fprintf(stderr, "gc bd: show %q: %v\n", id, err) //nolint:errcheck // best-effort stderr
			return 1, true
		}
		if !exists {
			// No class store file: a reserved-prefix id has nowhere else to
			// live, so this is genuine absence.
			printBdShowNotFound(stderr, id)
			return 1, true
		}
		return renderClassShow(cityPath, class, id, jsonOut, stdout, stderr), true
	}

	// Legacy-id probe over the routed classes, in cutover order.
	for _, class := range []string{config.BeadClassOrders, config.BeadClassNudges, config.BeadClassMessaging, config.BeadClassSessions} {
		routed, err := classShowRouted(cityPath, cfg, class)
		if err != nil {
			// Routing state unknowable: falling through to bd could read a
			// migrated bead as absent. Surface the failure instead.
			fmt.Fprintf(stderr, "gc bd: show %q: %v\n", id, err) //nolint:errcheck // best-effort stderr
			return 1, true
		}
		if !routed {
			continue
		}
		b, found, err := classStoreShowBead(cityPath, class, id)
		if err != nil {
			fmt.Fprintf(stderr, "gc bd: show %q via %s class store failed: %v\n", id, class, err) //nolint:errcheck // best-effort stderr
			return 1, true
		}
		if found {
			return printBdShowBead(b, jsonOut, stdout, stderr), true
		}
	}
	return 0, false
}

// bdShowRoutable reports whether a bd arg list is a plain `show <id> [--json]`
// read with exactly one positional id. Anything else (a different verb,
// multiple ids, or any other flag) is not routed and falls through.
func bdShowRoutable(bdArgs []string) (id string, jsonOut, ok bool) {
	if len(bdArgs) == 0 || bdArgs[0] != "show" {
		return "", false, false
	}
	for _, a := range bdArgs[1:] {
		switch {
		case a == "--json":
			jsonOut = true
		case strings.HasPrefix(a, "-"):
			return "", false, false
		default:
			if id != "" {
				return "", false, false
			}
			id = a
		}
	}
	if id == "" {
		return "", false, false
	}
	return id, jsonOut, true
}

// classShowRouted resolves whether a class routes to its embedded store for
// this city, through each class's own marker-first, ENOENT-only resolver.
func classShowRouted(cityPath string, cfg *config.City, class string) (bool, error) {
	switch class {
	case config.BeadClassOrders:
		return ordersSQLiteRoutingActive(cityPath, cfg)
	case config.BeadClassNudges:
		return nudgesdb.Routed(cityPath, cfg)
	case config.BeadClassMessaging:
		return messagingdb.Routed(cityPath, cfg)
	case config.BeadClassSessions:
		return sessionsdb.Routed(cityPath, cfg)
	}
	return false, nil
}

// classStoreFileExists reports whether the class's store file exists without
// creating it (opening a store creates the db file as a side effect). Only
// ENOENT reads as absence; any other stat failure surfaces.
func classStoreFileExists(cityPath, class string) (bool, error) {
	path := nudgesdb.StoreDir(cityPath) // one shared dir for every class
	switch class {
	case config.BeadClassOrders:
		path = ordersClassStorePath(cityPath)
	case config.BeadClassNudges:
		path = nudgesdb.StorePath(cityPath)
	case config.BeadClassMessaging:
		path = messagingdb.StorePath(cityPath)
	case config.BeadClassSessions:
		path = sessionsdb.StorePath(cityPath)
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("checking %s class store: %w", class, err)
	}
	return true, nil
}

// classStoreShowBead performs the per-class by-id point read and shapes the
// row as a bead for display. found=false is a clean miss; err is a hard
// store failure that must never be flattened into a miss.
func classStoreShowBead(cityPath, class, id string) (beads.Bead, bool, error) {
	switch class {
	case config.BeadClassOrders:
		st, err := ordersClassStoreFor(cityPath)
		if err != nil {
			return beads.Bead{}, false, err
		}
		run, err := st.Get(id)
		if errors.Is(err, beads.ErrNotFound) {
			return beads.Bead{}, false, nil
		}
		if err != nil {
			return beads.Bead{}, false, err
		}
		return orderRunShowBead(run), true, nil
	case config.BeadClassNudges:
		st, err := nudgesdb.SharedStoreFor(cityPath)
		if err != nil {
			return beads.Bead{}, false, err
		}
		rec, found, err := st.FindRecordIncludingTerminal(id)
		if err != nil || !found {
			return beads.Bead{}, false, err
		}
		return nudgeShowBead(rec), true, nil
	case config.BeadClassMessaging:
		st, err := messagingdb.SharedStoreFor(cityPath)
		if err != nil {
			return beads.Bead{}, false, err
		}
		rec, found, err := st.Get(id)
		if err != nil || !found {
			return beads.Bead{}, false, err
		}
		return mailShowBead(rec), true, nil
	case config.BeadClassSessions:
		st, err := sessionsdb.SharedStoreFor(cityPath)
		if err != nil {
			return beads.Bead{}, false, err
		}
		b, err := st.Get(id)
		if errors.Is(err, beads.ErrNotFound) {
			return beads.Bead{}, false, nil
		}
		if err != nil {
			return beads.Bead{}, false, err
		}
		return b, true, nil
	}
	return beads.Bead{}, false, nil
}

// renderClassShow reads a reserved-prefix id from its class store and prints
// it in bd's show shape, preserving the 404-vs-error split.
func renderClassShow(cityPath, class, id string, jsonOut bool, stdout, stderr io.Writer) int {
	b, found, err := classStoreShowBead(cityPath, class, id)
	if err != nil {
		fmt.Fprintf(stderr, "gc bd: show %q via %s class store failed: %v\n", id, class, err) //nolint:errcheck // best-effort stderr
		return 1
	}
	if !found {
		printBdShowNotFound(stderr, id)
		return 1
	}
	return printBdShowBead(b, jsonOut, stdout, stderr)
}

// printBdShowNotFound renders genuine absence in bd's own "no issue found"
// shape so existing parsers (and operators) see the same absence signal the
// passthrough would produce.
func printBdShowNotFound(stderr io.Writer, id string) {
	fmt.Fprintf(stderr, "Error fetching %s: no issue found matching %q\n", id, id) //nolint:errcheck // best-effort stderr
}

// printBdShowBead renders a bead in bd's show output shape: `--json` emits a
// JSON array of issues (matching bd, so pack parsers keep working); the text
// form is the id/status/title line.
func printBdShowBead(b beads.Bead, jsonOut bool, stdout, stderr io.Writer) int {
	if jsonOut {
		out, err := json.MarshalIndent([]beads.Bead{b}, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "gc bd: show %q: marshal: %v\n", b.ID, err) //nolint:errcheck // best-effort stderr
			return 1
		}
		fmt.Fprintln(stdout, string(out)) //nolint:errcheck // best-effort stdout
		return 0
	}
	fmt.Fprintf(stdout, "%s\t%s\t%s\n", b.ID, b.Status, b.Title) //nolint:errcheck // best-effort stdout
	return 0
}

// orderRunShowBead shapes an order run as a display bead, mirroring the bd
// tracking-bead vocabulary (order-tracking / order-run: / order: / seq:
// labels, outcome labels, open/closed status).
func orderRunShowBead(run orders.OrderRun) beads.Bead {
	status := "closed"
	if run.Open {
		status = "open"
	}
	labels := append([]string{"order-tracking", "order-run:" + run.Scoped, "order:" + run.Scoped}, run.Outcome.Labels()...)
	if run.Cursor != 0 {
		labels = append(labels, fmt.Sprintf("seq:%d", uint64(run.Cursor)))
	}
	return beads.Bead{
		ID:        run.ID,
		Title:     "order:" + run.Scoped,
		Status:    status,
		Type:      "task",
		Labels:    labels,
		CreatedAt: run.CreatedAt,
		UpdatedAt: run.UpdatedAt,
	}
}

// nudgeShowBead shapes a queue record as a display bead carrying the shadow
// vocabulary the bd-era nudge shadows exposed (gc:nudge label, queue state
// and terminal stamps in metadata).
func nudgeShowBead(rec nudgesdb.TerminalRecord) beads.Bead {
	shadow := rec.Shadow()
	status := "closed"
	if shadow.Open {
		status = "open"
	}
	meta := beads.StringMap{"state": shadow.State}
	if shadow.TerminalReason != "" {
		meta["terminal_reason"] = shadow.TerminalReason
	}
	if shadow.Agent != "" {
		meta["agent"] = shadow.Agent
	}
	if shadow.SessionID != "" {
		meta["session_id"] = shadow.SessionID
	}
	return beads.Bead{
		ID:          shadow.ID,
		Title:       "nudge:" + shadow.Agent,
		Status:      status,
		Type:        "task",
		Labels:      []string{"gc:nudge"},
		Description: shadow.Message,
		Metadata:    meta,
		CreatedAt:   rec.Item.CreatedAt,
	}
}

// mailShowBead shapes a mail record as a display bead, inverting the
// beadToRecord codec's core fields (Type=message, Title=subject,
// Description=body, From/Assignee addressing, thread labels).
func mailShowBead(rec beadmail.Record) beads.Bead {
	status := "closed"
	if rec.Open {
		status = "open"
	}
	var labels []string
	if rec.ThreadID != "" {
		labels = append(labels, "thread:"+rec.ThreadID)
	}
	if rec.ReplyToID != "" {
		labels = append(labels, "reply-to:"+rec.ReplyToID)
	}
	if rec.ReadLabel {
		labels = append(labels, "read")
	}
	return beads.Bead{
		ID:          rec.ID,
		Title:       rec.Subject,
		Status:      status,
		Type:        "message",
		Labels:      labels,
		Description: rec.Body,
		From:        rec.FromAddr,
		Assignee:    rec.ToAddr,
		CreatedAt:   rec.CreatedAt,
	}
}
