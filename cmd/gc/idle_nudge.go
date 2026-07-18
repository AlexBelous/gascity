package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
)

// Session-bead metadata keys for the stalled-claim backstop. The state machine
// is PERSISTED on the pool slot's own session bead so it survives a controller
// restart — the in-memory grace map of the reverted #312 nudger did not, which
// is precisely why that one re-nudge-stormed on every restart (test-5il).
const (
	idleClaimNudgeTriggerKey = "idle_claim_nudge_trigger" // trigger bead id last acted on
	idleClaimNudgeCountKey   = "idle_claim_nudge_count"   // nudges delivered for that trigger
	idleClaimNudgeAtKey      = "idle_claim_nudge_at"      // RFC3339 of last attempt / first observation
)

// Backstop pacing. Deliberately slow: this only rescues a pool slot that was
// handed work but never began it, so a couple of minutes of latency is fine and
// keeps the backstop nowhere near anything that could read as churn.
const (
	idleClaimNudgeGrace       = 90 * time.Second // observe-before-first-nudge; lets a normal claim land
	idleClaimNudgeBackoff     = 3 * time.Minute  // between retries when a delivered nudge didn't take
	idleClaimNudgeMaxAttempts = 3                // then give up and log (manual re-nudge remains)
)

// nudgeStalledPoolClaims is a reconcile-tick backstop that runs for every
// runtime (herdr AND tmux). It re-delivers the claim nudge to a pool slot that
// is running but whose assigned trigger bead is still UNCLAIMED (open, not
// in_progress). The startup nudge can be missed — a freshly-spawned slot whose
// submit-CR was swallowed, or a warm slot that survived a `gc restart` and was
// never re-Started — leaving the worker session idle at its prompt with work it never
// began. tmux's relaunch/respawn path only heals a session that DIED; a live
// idle slot needs this demand-driven wake exactly as herdr does (activity
// reporting makes the controller SEE the slot but never nudges it to claim).
//
// SCOPE (trigger-bead-key limitation): this keys on the slot's own
// gc.trigger_bead_id, so it only rescues a slot the reconciler already bound to
// a specific bead (resume / wake-known-identity tiers). A bead slung to the
// pool AFTER the slot went idle and left UNASSIGNED (routed_to=pool, open, no
// assignee) never stamps trigger_bead_id, so it is invisible here. Widening the
// key to "any open+routed+unclaimed pool bead past the grace window" is the
// documented follow-up (see engdocs/design/idle-claim-nudge-followups.md).
//
// Churn-free by construction — it inverts every failure mode that got the #312
// idle-session nudger reverted:
//   - Keys on bead state (trigger bead == open), never "idle for N minutes", so
//     it is structurally invisible to a working agent: the instant a pool slot
//     claims, its trigger bead flips to in_progress and stops matching.
//   - State is persisted on the session bead, so a restart cannot replay it.
//   - Bounded per assignment: observe (grace) → nudge → backoff retries → give
//     up. It never spams a tick and never loops forever.
//   - Pool slots only.
func nudgeStalledPoolClaims(
	sp runtime.Provider,
	cfg *config.City,
	sessStore beads.SessionStore,
	sessionBeads []beads.Bead,
	assignedWork []beads.Bead,
	now time.Time,
	stdout io.Writer,
) {
	if sp == nil || cfg == nil || sessStore.Store == nil {
		return // hot reconcile path: never panic on a half-built dependency
	}
	workByID := make(map[string]beads.Bead, len(assignedWork))
	for _, w := range assignedWork {
		workByID[w.ID] = w
	}

	for i := range sessionBeads {
		s := &sessionBeads[i]
		if strings.TrimSpace(s.Metadata["pool_managed"]) != "true" {
			continue // pool slots only
		}
		sessName := strings.TrimSpace(s.Metadata["session_name"])
		if sessName == "" || !sp.IsRunning(sessName) {
			continue
		}
		triggerID := strings.TrimSpace(s.Metadata[beadmeta.TriggerBeadIDMetadataKey])
		if triggerID == "" {
			continue
		}

		// Act only while the trigger bead is genuinely unclaimed. A claimed bead
		// is in_progress (or closed) — either way the slot is doing its job and
		// must not be disturbed. If the bead is absent from the assigned-work
		// snapshot it's been claimed/closed/moved; clear any stale marker.
		w, ok := workByID[triggerID]
		if !ok || !isUnclaimedTrigger(w, sessName) {
			clearIdleClaimMarker(sessStore, s, stdout)
			continue
		}

		markedTrigger := strings.TrimSpace(s.Metadata[idleClaimNudgeTriggerKey])
		attempts := atoiOr0(s.Metadata[idleClaimNudgeCountKey])
		last := parseRFC3339OrZero(s.Metadata[idleClaimNudgeAtKey])

		// First observation of this assignment: start the grace clock, don't
		// nudge yet — a normal claim almost always lands within the grace window.
		if markedTrigger != triggerID {
			writeIdleClaimMarker(sessStore, s, triggerID, 0, now, stdout)
			continue
		}
		switch {
		case attempts == 0:
			if now.Sub(last) < idleClaimNudgeGrace {
				continue // still inside the observe-first grace
			}
		case attempts >= idleClaimNudgeMaxAttempts:
			continue // gave up; manual re-nudge is the escape hatch
		default:
			if now.Sub(last) < idleClaimNudgeBackoff {
				continue // waiting out the backoff before the next retry
			}
		}

		nudge := claimNudgeFor(cfg, *s)
		if nudge == "" {
			continue
		}
		if err := sp.Nudge(sessName, runtime.TextContent(nudge)); err != nil {
			fmt.Fprintf(stdout, "idle-claim-nudge: %s failed: %v\n", sessName, err) //nolint:errcheck // best-effort
			continue
		}
		fmt.Fprintf(stdout, "idle-claim-nudge: nudged %s to claim %s (attempt %d/%d)\n", sessName, triggerID, attempts+1, idleClaimNudgeMaxAttempts) //nolint:errcheck // best-effort
		writeIdleClaimMarker(sessStore, s, triggerID, attempts+1, now, stdout)
	}
}

// nudgeReadyRoutedPoolClaims immediately hands newly-ready, unassigned routed
// work to a compatible warm pool slot. Desired-state reconciliation already
// creates or starts a slot when none is available; this closes the other half
// of the contract: a slot that is already running must be prompted when its
// work becomes ready, rather than relying on route-write timing or a shell
// order to have prompted it earlier.
//
// The demand map is built from Ready() only, so blocked routed work is never
// eligible. A persisted marker acknowledges one work item per idle slot and
// prevents repeated patrol ticks from injecting duplicate prompts. Slots with
// a current/trigger bead remain reserved for that work and are not disturbed.
func nudgeReadyRoutedPoolClaims(
	sp runtime.Provider,
	cfg *config.City,
	sessStore beads.SessionStore,
	sessionBeads []beads.Bead,
	readyDemand map[string]scaleCheckDemand,
	assignedWork []beads.Bead,
	assignedStoreRefs []string,
	readyAssigned map[storeScopedBeadKey]bool,
	now time.Time,
	stdout io.Writer,
) {
	if sp == nil || cfg == nil || sessStore.Store == nil || len(readyDemand) == 0 {
		return
	}

	// One work item needs at most one prompt. Reserve items acknowledged on a
	// prior patrol before assigning the remaining items to currently-idle warm
	// slots, so a delayed claim cannot create a prompt storm.
	reserved := make(map[string]map[string]bool, len(readyDemand))
	for i := range sessionBeads {
		s := &sessionBeads[i]
		if strings.TrimSpace(s.Metadata["pool_managed"]) != "true" {
			continue
		}
		template := normalizedSessionTemplate(*s, cfg)
		demand, ok := readyDemand[template]
		if !ok || len(demand.WorkBeadIDs) == 0 {
			continue
		}
		marker := strings.TrimSpace(s.Metadata[idleClaimNudgeTriggerKey])
		if marker == "" || atoiOr0(s.Metadata[idleClaimNudgeCountKey]) < 1 {
			continue
		}
		if !demandContainsWorkID(demand, marker) {
			continue
		}
		if reserved[template] == nil {
			reserved[template] = make(map[string]bool)
		}
		reserved[template][marker] = true
	}

	for i := range sessionBeads {
		s := &sessionBeads[i]
		if strings.TrimSpace(s.Metadata["pool_managed"]) != "true" {
			continue
		}
		sessName := strings.TrimSpace(s.Metadata["session_name"])
		if sessName == "" || !sp.IsRunning(sessName) {
			continue
		}
		// A live session that already owns work must finish that work before it
		// claims another queued routed bead.
		if strings.TrimSpace(s.Metadata["currently_processing_bead_id"]) != "" {
			continue
		}
		if strings.TrimSpace(s.Metadata[beadmeta.TriggerBeadIDMetadataKey]) != "" {
			continue
		}

		template := normalizedSessionTemplate(*s, cfg)
		demand, ok := readyDemand[template]
		if !ok {
			continue
		}
		// This slot already has an acknowledged prompt for a still-ready item.
		// Keep it reserved for that item rather than delivering a second prompt
		// for the next queued bead before it has had a chance to claim the first.
		if readyClaimPendingForSession(*s, cfg, readyDemand, assignedWork, assignedStoreRefs, readyAssigned) {
			continue
		}
		workID := nextUnreservedDemandWorkID(demand, reserved[template])
		if workID == "" {
			continue
		}
		if nudge := claimNudgeFor(cfg, *s); nudge != "" {
			if err := sp.Nudge(sessName, runtime.TextContent(nudge)); err != nil {
				fmt.Fprintf(stdout, "ready-routed-claim-nudge: %s failed: %v\n", sessName, err) //nolint:errcheck // best-effort
				continue
			}
			fmt.Fprintf(stdout, "ready-routed-claim-nudge: nudged %s to claim %s\n", sessName, workID) //nolint:errcheck // best-effort
			writeIdleClaimMarker(sessStore, s, workID, 1, now, stdout)
			if reserved[template] == nil {
				reserved[template] = make(map[string]bool)
			}
			reserved[template][workID] = true
		}
	}
}

func demandContainsWorkID(demand scaleCheckDemand, workID string) bool {
	for _, id := range demand.WorkBeadIDs {
		if strings.TrimSpace(id) == workID {
			return true
		}
	}
	return false
}

func nextUnreservedDemandWorkID(demand scaleCheckDemand, reserved map[string]bool) string {
	for _, id := range demand.WorkBeadIDs {
		id = strings.TrimSpace(id)
		if id != "" && !reserved[id] {
			return id
		}
	}
	return ""
}

// nudgeReadyAssignedSessionClaims gives a running session one immediate claim
// prompt when work explicitly assigned to that session becomes ready. The
// normal reconciler already starts a stopped matching session; this handles the
// warm-session case, where marking the session awake alone does not create a
// new agent turn. Readiness is the store-scoped ReadyAssigned verdict, never a
// status-only inference, so blocked assignments remain silent.
func nudgeReadyAssignedSessionClaims(
	sp runtime.Provider,
	cfg *config.City,
	sessStore beads.SessionStore,
	sessionBeads []beads.Bead,
	assignedWork []beads.Bead,
	assignedStoreRefs []string,
	readyAssigned map[storeScopedBeadKey]bool,
	readyRouted map[string]scaleCheckDemand,
	now time.Time,
	stdout io.Writer,
) {
	if sp == nil || cfg == nil || sessStore.Store == nil || len(assignedWork) == 0 || len(readyAssigned) == 0 {
		return
	}
	for i := range sessionBeads {
		s := &sessionBeads[i]
		sessName := strings.TrimSpace(s.Metadata["session_name"])
		if sessName == "" || !sp.IsRunning(sessName) {
			continue
		}
		if strings.TrimSpace(s.Metadata["currently_processing_bead_id"]) != "" ||
			strings.TrimSpace(s.Metadata[beadmeta.TriggerBeadIDMetadataKey]) != "" {
			continue // the live slot already owns work
		}
		readyIDs := readyAssignedWorkIDsForSession(*s, assignedWork, assignedStoreRefs, readyAssigned)
		if len(readyIDs) == 0 {
			continue
		}
		if readyClaimPendingForSession(*s, cfg, readyRouted, assignedWork, assignedStoreRefs, readyAssigned) {
			continue // an earlier patrol already handed this session its next turn
		}
		nudge := claimNudgeFor(cfg, *s)
		if nudge == "" {
			continue
		}
		workID := readyIDs[0]
		if err := sp.Nudge(sessName, runtime.TextContent(nudge)); err != nil {
			fmt.Fprintf(stdout, "ready-assigned-claim-nudge: %s failed: %v\n", sessName, err) //nolint:errcheck // best-effort
			continue
		}
		fmt.Fprintf(stdout, "ready-assigned-claim-nudge: nudged %s to claim %s\n", sessName, workID) //nolint:errcheck // best-effort
		writeIdleClaimMarker(sessStore, s, workID, 1, now, stdout)
	}
}

func readyAssignedWorkIDsForSession(
	sessionBead beads.Bead,
	assignedWork []beads.Bead,
	assignedStoreRefs []string,
	readyAssigned map[storeScopedBeadKey]bool,
) []string {
	identities := make(map[string]bool)
	for _, identity := range sessionBeadAssigneeIdentities(sessionBead) {
		if identity = strings.TrimSpace(identity); identity != "" {
			identities[identity] = true
		}
	}
	var ids []string
	for i, work := range assignedWork {
		if strings.TrimSpace(work.Status) != "open" || !identities[strings.TrimSpace(work.Assignee)] {
			continue
		}
		storeRef := ""
		if i < len(assignedStoreRefs) {
			storeRef = assignedStoreRefs[i]
		}
		if !readyAssigned[storeScopedBeadKey{StoreRef: storeRef, ID: work.ID}] {
			continue
		}
		ids = append(ids, work.ID)
	}
	return ids
}

// readyClaimPendingForSession keeps a session's acknowledged prompt scoped to
// work it can actually claim. Bare bead IDs are not globally unique across
// independent city and rig stores, so a ready bead for another session must
// never suppress this session's wake.
func readyClaimPendingForSession(
	sessionBead beads.Bead,
	cfg *config.City,
	readyRouted map[string]scaleCheckDemand,
	assignedWork []beads.Bead,
	assignedStoreRefs []string,
	readyAssigned map[storeScopedBeadKey]bool,
) bool {
	marker := strings.TrimSpace(sessionBead.Metadata[idleClaimNudgeTriggerKey])
	if marker == "" || atoiOr0(sessionBead.Metadata[idleClaimNudgeCountKey]) < 1 {
		return false
	}
	if demandContainsWorkID(readyRouted[normalizedSessionTemplate(sessionBead, cfg)], marker) {
		return true
	}
	for _, id := range readyAssignedWorkIDsForSession(sessionBead, assignedWork, assignedStoreRefs, readyAssigned) {
		if id == marker {
			return true
		}
	}
	return false
}

// isUnclaimedTrigger reports whether the pool slot's trigger bead is still
// waiting to be claimed: status open and not already assigned to this slot
// (a non-empty assignee equal to the session means the claim is mid-flight).
func isUnclaimedTrigger(w beads.Bead, sessName string) bool {
	if !strings.EqualFold(strings.TrimSpace(w.Status), "open") {
		return false // in_progress / closed / blocked → not ours to nudge
	}
	if assignee := strings.TrimSpace(w.Assignee); assignee != "" && assignee == sessName {
		return false
	}
	return true
}

// claimNudgeFor resolves the slot's configured startup nudge (the worker's
// `gc hook --claim` line) from the agent template behind this session bead.
func claimNudgeFor(cfg *config.City, session beads.Bead) string {
	template := normalizedSessionTemplate(session, cfg)
	if template == "" {
		return ""
	}
	agent := findAgentByTemplate(cfg, template)
	if agent == nil {
		return ""
	}
	return strings.TrimSpace(agent.Nudge)
}

// writeIdleClaimMarker persists the backstop state machine onto the session
// bead and mirrors it into the in-memory snapshot so the rest of this tick
// reads the just-written values.
func writeIdleClaimMarker(sessStore beads.SessionStore, s *beads.Bead, triggerID string, attempts int, now time.Time, stdout io.Writer) {
	kvs := map[string]string{
		idleClaimNudgeTriggerKey: triggerID,
		idleClaimNudgeCountKey:   strconv.Itoa(attempts),
		idleClaimNudgeAtKey:      now.UTC().Format(time.RFC3339),
	}
	if err := sessStore.SetMetadataBatch(s.ID, kvs); err != nil {
		fmt.Fprintf(stdout, "idle-claim-nudge: marking %s failed: %v\n", s.ID, err) //nolint:errcheck // best-effort
		return
	}
	if s.Metadata == nil {
		s.Metadata = make(map[string]string, len(kvs))
	}
	for k, v := range kvs {
		s.Metadata[k] = v
	}
}

// clearIdleClaimMarker wipes the marker once the slot no longer has unclaimed
// work, so the next assignment starts its grace clock fresh. No-op (no store
// write) when there is nothing to clear, so steady-state ticks stay silent.
func clearIdleClaimMarker(sessStore beads.SessionStore, s *beads.Bead, stdout io.Writer) {
	if s.Metadata[idleClaimNudgeTriggerKey] == "" &&
		s.Metadata[idleClaimNudgeCountKey] == "" &&
		s.Metadata[idleClaimNudgeAtKey] == "" {
		return
	}
	kvs := map[string]string{
		idleClaimNudgeTriggerKey: "",
		idleClaimNudgeCountKey:   "",
		idleClaimNudgeAtKey:      "",
	}
	if err := sessStore.SetMetadataBatch(s.ID, kvs); err != nil {
		fmt.Fprintf(stdout, "idle-claim-nudge: clearing %s failed: %v\n", s.ID, err) //nolint:errcheck // best-effort
		return
	}
	for k := range kvs {
		delete(s.Metadata, k)
	}
}

func atoiOr0(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

func parseRFC3339OrZero(s string) time.Time {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return time.Time{}
	}
	return t
}
