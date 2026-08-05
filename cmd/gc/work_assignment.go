package main

import (
	"sort"
	"strings"

	sessionpkg "github.com/gastownhall/gascity/internal/session"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/mail/beadmail"
)

// workAssignment is the typed boundary façade the SESSION reconciler uses to
// read WORK beads keyed by a session identity. The reconciler's stranded/awake/
// drain probes are WORK queries (List{Assignee,Status,...} / ReadyLive) that
// happen to be keyed by a session's assignment identifiers; left raw they reach
// the WORK store *through* the session store. Routing them through this façade
// makes the WORK store the explicit source.
//
// The façade carries a beads.WorkStore (the work class), but every optional
// capability (CachedList, ReadyLive's Backing()) is asserted on the embedded
// .Store, never the wrapper — wrapping a Store in WorkStore does NOT promote
// optional capabilities, and asserting on the wrapper would silently drop the
// cache/live fast-paths (the typed-nil trap, OBJECT-MODEL-FRONT-DOOR-DESIGN.md
// invariant 3/4). On a single-store city the work store IS the same object the
// session arm uses, so the bead reads emitted here are byte-identical to the
// raw ops they replace.
type workAssignment struct {
	store beads.WorkStore
}

// workAssignmentForStore wraps a resolved WORK store as the typed assignment
// façade. The caller passes the work store value (cityWorkStore / a rig work
// store); the underlying store is unchanged, so reads stay byte-identical.
func workAssignmentForStore(store beads.WorkStore) workAssignment {
	return workAssignment{store: store}
}

// unwrapped returns the underlying generic Store for optional-capability
// assertions (CachedList, ReadyLive Backing()). Asserting on the WorkStore
// wrapper instead would fail because the wrapper only embeds the Store
// interface and does not promote optional capabilities.
func (w workAssignment) unwrapped() beads.Store {
	return w.store.Store
}

// OpenAssignedTo returns the open or in-progress beads in this store assigned
// to the given identity for the given tier mode, excluding mail message beads.
// It is the typed form of the raw
// List{Assignee,Status,Live,TierMode} probe the reconciler ran directly.
// status selects the bead status ("open" / "in_progress"); an empty status
// returns every non-closed status so callers can batch a small status set and
// filter it in memory. live mirrors the raw ListQuery.Live flag. Callers filter
// session beads (and repairable session beads) with HasNonSessionWork or the
// equivalent typed predicate. Mail message beads are filtered here (ra-59207):
// a mail wisp has no claim/routing semantics, so callers that reassign or
// release results must never see one.
func (w workAssignment) OpenAssignedTo(assignee, status string, tierMode beads.TierMode, live bool) ([]beads.Bead, error) {
	store := w.unwrapped()
	if store == nil {
		return nil, nil
	}
	items, err := store.List(beads.ListQuery{Assignee: assignee, Status: status, Live: live, TierMode: tierMode})
	if err != nil {
		return nil, err
	}
	return excludeMailMessageBeads(items), nil
}

// OpenAssignedToAny returns open or in-progress non-session work assigned to
// any supplied identity using exact live, both-tier assignee queries. It is the
// bounded pool-reuse form of repeated OpenAssignedTo probes. Keeping each read
// singular is load-bearing: production stores push down Assignee, while their
// plural-assignee compatibility paths may otherwise scan and filter the fleet.
func (w workAssignment) OpenAssignedToAny(assignees []string) ([]beads.Bead, error) {
	store := w.unwrapped()
	if store == nil {
		return nil, nil
	}
	canonical := make([]string, 0, len(assignees))
	seen := make(map[string]struct{}, len(assignees))
	for _, assignee := range assignees {
		assignee = strings.TrimSpace(assignee)
		if assignee == "" {
			continue
		}
		if _, duplicate := seen[assignee]; duplicate {
			continue
		}
		seen[assignee] = struct{}{}
		canonical = append(canonical, assignee)
	}
	if len(canonical) == 0 {
		return nil, nil
	}
	sort.Strings(canonical)
	var result []beads.Bead
	for _, assignee := range canonical {
		items, err := w.OpenAssignedTo(assignee, "", beads.TierBoth, true)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			if item.Status != "open" && item.Status != "in_progress" {
				continue
			}
			if sessionpkg.IsSessionBeadOrRepairable(item) {
				continue
			}
			result = append(result, item)
		}
	}
	return result, nil
}

// CachedOpenAssignedWisps returns cached open-assigned wisp-tier WORK beads when
// the underlying store exposes the CachedList fast-path, plus whether the cache
// answered. It is the typed form of the positive-only cache probe in
// sessionHasOpenAssignedWispWork; the assertion is on the embedded .Store so the
// fast-path is preserved.
func (w workAssignment) CachedOpenAssignedWisps(assignee, status string) ([]beads.Bead, bool) {
	store := w.unwrapped()
	if store == nil {
		return nil, false
	}
	query := beads.ListQuery{Assignee: assignee, Status: status, TierMode: beads.TierWisps}
	cache, ok := store.(interface {
		CachedList(beads.ListQuery) ([]beads.Bead, bool)
	})
	if !ok {
		return nil, false
	}
	return cache.CachedList(query)
}

// ReadyAssignedTo returns the ready (unblocked, actionable) WORK beads assigned
// to the given identity for the given tier mode. It is the typed form of the raw
// beads.ReadyLive(ReadyQuery{Assignee,TierMode}) probe. ReadyLive is called on
// the embedded .Store so its Backing() live-read fast-path is preserved.
func (w workAssignment) ReadyAssignedTo(assignee string, tierMode beads.TierMode) ([]beads.Bead, error) {
	store := w.unwrapped()
	if store == nil {
		return nil, nil
	}
	return beads.ReadyLive(store, beads.ReadyQuery{Assignee: assignee, TierMode: tierMode})
}

// HasNonSessionWork reports whether any bead in items is non-session WORK
// (skipping session beads and repairable session beads). Shared filter for the
// boolean readiness/open probes.
func (w workAssignment) HasNonSessionWork(items []beads.Bead) bool {
	for _, item := range items {
		if sessionpkg.IsSessionBeadOrRepairable(item) {
			continue
		}
		return true
	}
	return false
}

// OpenAssignedToBasic returns the WORK beads assigned to the given identity with
// the given status, using the no-flags List{Assignee,Status} query (no Live, no
// TierMode). It is the typed form of the raw probe in
// releaseWorkFromClosedSessionBead, kept distinct from OpenAssignedTo because the
// close-release path deliberately runs the unflagged query — making it byte-
// identical to OpenAssignedTo's flagged query would change the emitted bead op.
// Like OpenAssignedTo, mail message beads are excluded (ra-59207): they are not
// WORK and have no claim/routing semantics for the release path to act on.
func (w workAssignment) OpenAssignedToBasic(assignee, status string) ([]beads.Bead, error) {
	store := w.unwrapped()
	if store == nil {
		return nil, nil
	}
	items, err := store.List(beads.ListQuery{Assignee: assignee, Status: status})
	if err != nil {
		return nil, err
	}
	return excludeMailMessageBeads(items), nil
}

// excludeMailMessageBeads filters mail message beads (beadmail.IsMessageBead)
// out of a WORK query result. A mail wisp is a delivery route, not a claimable
// unit of work — it can be neither released nor reassigned — so every WORK
// enumeration in this file (and every caller downstream, all of which treat
// their results as releasable/reassignable WORK) must exclude it at the source
// rather than repeat the check at each call site (ra-59207: the session-close
// WORK-RELEASE sweep clearing a mail bead's assignee silently destroyed its
// only route to an inbox).
func excludeMailMessageBeads(items []beads.Bead) []beads.Bead {
	if len(items) == 0 {
		return items
	}
	out := items[:0:0]
	for _, item := range items {
		if beadmail.IsMessageBead(item) {
			continue
		}
		out = append(out, item)
	}
	return out
}

// ReleaseWorkBead detaches one WORK bead from its (closed/retired) session: it
// clears the assignee (empty-string clear), clears stale session-affinity
// metadata, and resets an in_progress bead to open so a fresh worker can
// re-claim it via the routed queue. When runTargetFallback is non-empty AND the
// bead carries neither run_target nor routed_to, the fallback route is stamped so
// the reopened work stays reachable by the controller demand query. It emits the
// exact beads.UpdateOpts the raw release ops in releaseWorkFromClosedSessionBead
// and unclaimWorkAssignedToRetiredSessionBead emitted (proven byte-identical by
// the recording-fake write tests). Pass runTargetFallback="" for the close-
// release path, which never stamps a fallback.
func (w workAssignment) ReleaseWorkBead(item beads.Bead, runTargetFallback string) error {
	store := w.unwrapped()
	if store == nil {
		return nil
	}
	empty := ""
	update := beads.UpdateOpts{
		Assignee: &empty,
		Metadata: clearedSessionAffinityMetadata(),
	}
	if item.Status == "in_progress" {
		open := "open"
		update.Status = &open
	}
	if runTargetFallback != "" &&
		strings.TrimSpace(item.Metadata[beadmeta.RunTargetMetadataKey]) == "" &&
		strings.TrimSpace(item.Metadata[beadmeta.RoutedToMetadataKey]) == "" {
		update.Metadata[beadmeta.RunTargetMetadataKey] = runTargetFallback
	}
	return store.Update(item.ID, update)
}

// ReassignWorkBead re-homes one WORK bead onto a new session identity, emitting
// the exact Update{Assignee:&new} the raw reassign op in
// reassignWorkAssignedToRetiredSessionBead emitted. It deliberately touches
// neither Status nor Metadata.
func (w workAssignment) ReassignWorkBead(beadID, newSessionID string) error {
	store := w.unwrapped()
	if store == nil {
		return nil
	}
	return store.Update(beadID, beads.UpdateOpts{Assignee: &newSessionID})
}

// ClearDetachedProbe clears the detached-probe metadata contract on a WORK bead,
// emitting SetMetadata(id, gc.detached, "") — the empty-string clear semantics
// the raw clearDetachedProbeMetadata op used. Best-effort: a nil store or empty
// id is a no-op, and a write error is returned for the caller to log.
func (w workAssignment) ClearDetachedProbe(beadID string) error {
	store := w.unwrapped()
	if store == nil || beadID == "" {
		return nil
	}
	return store.SetMetadata(beadID, beadmeta.DetachedMetadataKey, "")
}
