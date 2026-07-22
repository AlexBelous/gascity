#!/usr/bin/env bash
#
# push-ownership-guard.sh — re-checks bd bead ownership/staleness
# immediately before an in-flight `git push` executes (bead ga-fip9ps.1;
# guards the race described in ga-fip9ps).
#
# THE RACE: an agent claims a bead, does work, and queues a push. A mayor
# stand-down/deconfliction ruling (reassign, reroute, close, hold) can land
# in the gap between "work finished" and "push actually executes" — e.g. a
# backgrounded push that was already queued before the ruling arrived. A
# stale push in that gap can clobber a branch another agent has since taken
# over (this happened on PR #4243, clobbering ga-lrqmb7's base). This guard
# closes that gap by re-reading bd's live state at the last possible moment
# before the push leaves the machine.
#
# THE EXPORTED FUNCTION: assert_bead_still_claimed. Re-resolves which bead
# this push is for (branch name, falling back to this session's in-progress
# assignment), re-reads its live state from bd, and returns non-zero
# (blocking the push) unless the bead is still open/in_progress, still
# assigned to one of this session's identities (any of GC_SESSION_NAME,
# GC_SESSION_ID, GC_ALIAS, GC_AGENT — mirroring the claim path), still
# routed to this session's config identity, and not held by the mayor or an
# external actor.
#
# TWO CALL SITES (defense in depth — see ga-fip9ps.1 bead notes):
#   Layer A — .githooks/pre-push calls this unconditionally for every
#             non-deletion push, independent of what changed. Escape hatch:
#             `git push --no-verify` (git-native, skips this hook entirely).
#   Layer B — scripts/rebase-resolve-lib.sh's attempt_bounded_self_rebase
#             calls this as the last check before its own
#             --force-with-lease push, in case that push executes in a
#             context where Layer A's hook isn't wired up (e.g. a clone
#             without core.hooksPath configured).
#
# FAIL CLOSED: any ambiguity (bd unreachable, the read times out, the
# response doesn't parse) blocks the push. The only sanctioned bypass is
# `git push --no-verify` for Layer A; Layer B has no bypass by design — an
# automated force-push is exactly the case this guard exists to stop.
#
# This file ONLY defines functions and one default-value assignment;
# sourcing it must not produce output or otherwise mutate state.
#
# Set POG_DISABLE=1 to short-circuit assert_bead_still_claimed to a bare
# `return 0`. This exists for test harnesses that call
# attempt_bounded_self_rebase directly against synthetic repos with no real
# bead behind them (e.g. scripts/test-rebase-resolve.sh) and must stay
# hermetic — it is not meant to be set on a real push path.

POG_TIMEOUT_SECONDS="${POG_TIMEOUT_SECONDS:-5}"

# _pog_timeout <seconds> <cmd...>: run <cmd...> bounded by <seconds>,
# mirroring the timeout/gtimeout fallback shim in
# test/agents/graph-dispatch.sh (the only bounded-exec precedent in this
# repo). Falls back to unbounded passthrough when neither is available
# rather than failing the whole guard open or closed on a missing dev tool.
_pog_timeout() {
    local bound="$1"
    shift
    if command -v timeout >/dev/null 2>&1; then
        timeout "$bound" "$@"
    elif command -v gtimeout >/dev/null 2>&1; then
        gtimeout "$bound" "$@"
    else
        "$@"
    fi
}

# _pog_branch_bead_id: prints the bead id parsed from the current branch
# name, matched against ga-[0-9a-z]{6}(\.[0-9]+)? — the bead's own id
# format, extended with an optional sub-bead suffix because this repo's
# real branch convention is builder/<bead-id>-<slug> and sub-beads (e.g.
# ga-fip9ps.1) are routine; the literal 6-char-only pattern would
# misresolve to the parent bead on a sub-bead's own branch. Prints nothing
# if the branch doesn't match.
_pog_branch_bead_id() {
    local branch=""
    branch="$(git symbolic-ref --short HEAD 2>/dev/null || git branch --show-current 2>/dev/null || true)"
    if [[ -n "$branch" ]]; then
        grep -oE 'ga-[0-9a-z]{6}(\.[0-9]+)?' <<<"$branch" | head -1 || true
    fi
}

# _pog_assignee_bead_id: prints this session's single in-progress bd
# assignment (bd list --assignee="$GC_AGENT" --status=in_progress --json).
# Prints nothing if none resolves.
#
# KNOWN LIMITATION (confirmed by manual repro, not yet filed as its own
# bead): this query filters on --status=in_progress, so it cannot find a
# bead that has already left that status (e.g. closed by the exact mayor
# ruling this guard exists to catch) by the time it runs. When
# _pog_branch_bead_id also doesn't resolve, that means no id resolves at
# all and assert_bead_still_claimed's "nothing to check" branch allows the
# push — see test_fallback_cannot_detect_staleness_after_status_leaves_in_progress
# in scripts/test-push-ownership-guard.sh. This does NOT affect the branch
# path: this repo's real branch convention (builder/<bead-id>-<slug>)
# always encodes the bead id. Widening this query (e.g. dropping the status
# filter) trades this gap for ambiguous multi-match resolution against an
# agent's whole bead history — a real design decision, not a mechanical
# fix — left for a follow-up bead.
_pog_assignee_bead_id() {
    if [[ -n "${GC_AGENT:-}" ]] && command -v bd >/dev/null 2>&1; then
        local list_json
        list_json="$(_pog_timeout "$POG_TIMEOUT_SECONDS" bd list --assignee="$GC_AGENT" --status=in_progress --json 2>/dev/null || true)"
        if [[ -n "$list_json" ]]; then
            jq -r '.[0].id // empty' <<<"$list_json" 2>/dev/null || true
        fi
    fi
}

# _pog_check_bead_claimed <id>: the core ownership/staleness check against
# one resolved bead id. Prints a BLOCKED explanation to stderr and returns
# non-zero if the check fails, 0 if it passes.
#   Return codes:
#     0 — claim confirmed, still valid.
#     2 — blocked because the bead's status is terminal (not
#         in_progress/open). This is the ONLY failure mode
#         assert_bead_still_claimed treats as retryable against a fallback
#         id, since a branch-encoded bead closing via a normal handoff
#         looks identical, from here, to one closed out from under this
#         push — the caller distinguishes them by whether a separate,
#         still-valid assignment exists.
#     1 — blocked for any other reason (bd unreachable, timed out,
#         unparseable, reassigned, rerouted, held). Not retryable: these
#         mean this session's specific claim was actively superseded, which
#         a fallback id must never paper over.
_pog_check_bead_claimed() {
    local id="$1"

    if ! command -v bd >/dev/null 2>&1; then
        echo "push-ownership-guard: BLOCKED — bd is not on PATH, cannot verify $id is still claimed. Bypass with: git push --no-verify" >&2
        return 1
    fi

    local json
    if ! json="$(_pog_timeout "$POG_TIMEOUT_SECONDS" bd show "$id" --json 2>/dev/null)" || [[ -z "$json" ]]; then
        echo "push-ownership-guard: BLOCKED — bd show $id timed out or bd/Dolt is unreachable; cannot confirm $id is still claimed. Bypass with: git push --no-verify" >&2
        return 1
    fi
    if ! jq -e '.' <<<"$json" >/dev/null 2>&1; then
        echo "push-ownership-guard: BLOCKED — bd show $id --json returned unparseable output; cannot confirm $id is still claimed. Bypass with: git push --no-verify" >&2
        return 1
    fi

    local status assignee routed_to labels
    status="$(jq -r '.[0].status // empty' <<<"$json")"
    assignee="$(jq -r '.[0].assignee // empty' <<<"$json")"
    routed_to="$(jq -r '.[0].metadata."gc.routed_to" // empty' <<<"$json")"
    labels="$(jq -r '.[0].labels[]? // empty' <<<"$json")"

    if [[ "$status" != "in_progress" && "$status" != "open" ]]; then
        echo "push-ownership-guard: BLOCKED — $id status is '$status', not in_progress/open; the claim behind this push is stale. Bypass with: git push --no-verify" >&2
        return 2
    fi

    # A session-run claim sets bead.assignee from the first non-empty of
    # GC_SESSION_NAME, GC_SESSION_ID, GC_ALIAS, GC_AGENT (see
    # cmd/gc/cmd_hook.go's firstNonEmptyHookValue). Accept ANY of this
    # session's live identities — GC_AGENT alone falsely blocks a push whose
    # bead is legitimately assigned to the session name/id. Fail-closed
    # semantics preserved: with identities present, an assignee matching none
    # (including empty) still blocks.
    local -a _pog_identities=()
    local _pog_ident
    for _pog_ident in "${GC_SESSION_NAME:-}" "${GC_SESSION_ID:-}" "${GC_ALIAS:-}" "${GC_AGENT:-}"; do
        [[ -n "$_pog_ident" ]] && _pog_identities+=("$_pog_ident")
    done
    if [[ ${#_pog_identities[@]} -gt 0 ]]; then
        local _pog_owned=0
        for _pog_ident in "${_pog_identities[@]}"; do
            if [[ -n "$assignee" && "$assignee" == "$_pog_ident" ]]; then _pog_owned=1; break; fi
        done
        if [[ $_pog_owned -eq 0 ]]; then
            echo "push-ownership-guard: BLOCKED — $id assignee is '$assignee', not any current-session identity (${_pog_identities[*]}); it was reassigned since this push began. Bypass with: git push --no-verify" >&2
            return 1
        fi
    fi

    if [[ -n "${GC_TEMPLATE:-}" && -n "$routed_to" && "$routed_to" != "$GC_TEMPLATE" ]]; then
        echo "push-ownership-guard: BLOCKED — $id gc.routed_to is '$routed_to', not this session's config identity ($GC_TEMPLATE); it was rerouted since this push began. Bypass with: git push --no-verify" >&2
        return 1
    fi

    if grep -qx 'hold:mayor' <<<"$labels"; then
        echo "push-ownership-guard: BLOCKED — $id is held (hold:mayor); a mayor ruling is pending. Bypass with: git push --no-verify" >&2
        return 1
    fi
    if grep -qx 'hold:external' <<<"$labels"; then
        echo "push-ownership-guard: BLOCKED — $id is held (hold:external). Bypass with: git push --no-verify" >&2
        return 1
    fi

    return 0
}

# assert_bead_still_claimed: the exported guard. Returns 0 to allow the
# push, non-zero to block it. See file header for the full contract.
#
# Resolves branch_id and assignee_id separately (rather than collapsing to
# one winner up front) because it needs both: branch_id is checked first
# (it's the more specific signal — see _pog_branch_bead_id), but when
# branch_id's bead has legitimately closed (e.g. ga-hilw2y — a builder's
# branch still names the bead that originally spawned it after that bead
# closed normally into a separate deploy-tracking bead), this retries the
# full check against assignee_id rather than treating an ordinary handoff
# as staleness. If both resolve and disagree, a warning goes to stderr
# either way — a best-effort cross-check, not a hard failure, since
# branch-naming habits can legitimately drift from bd's bookkeeping.
assert_bead_still_claimed() {
    if [[ "${POG_DISABLE:-0}" == "1" ]]; then
        return 0
    fi

    local branch_id assignee_id
    branch_id="$(_pog_branch_bead_id)"
    assignee_id="$(_pog_assignee_bead_id)"

    if [[ -n "$branch_id" && -n "$assignee_id" && "$branch_id" != "$assignee_id" ]]; then
        echo "push-ownership-guard: WARNING branch name resolves to $branch_id but this session's in-progress assignment is $assignee_id; using $branch_id (branch name wins)" >&2
    fi

    local id="$branch_id"
    [[ -z "$id" ]] && id="$assignee_id"
    if [[ -z "$id" ]]; then
        return 0  # nothing to check
    fi

    _pog_check_bead_claimed "$id"
    local rc=$?
    if [[ $rc -eq 0 ]]; then
        return 0
    fi

    # Retry against the assignee bead ONLY when the branch-encoded bead
    # specifically failed on terminal status (rc=2) and a distinct
    # assignee bead exists — e.g. ga-hilw2y, where the branch still names
    # the bead that originally spawned it after that bead closed normally
    # into a separate deploy-tracking bead. Any other block reason
    # (reassigned/rerouted/held, rc=1) is NOT retried: those mean this
    # session's specific claim was actively superseded, which must keep
    # blocking exactly as before even though branch_id's bead is still
    # in_progress/open.
    if [[ $rc -eq 2 && "$id" == "$branch_id" && -n "$assignee_id" && "$assignee_id" != "$branch_id" ]]; then
        echo "push-ownership-guard: $branch_id is closed but this session's own in-progress assignment is $assignee_id; retrying against it before blocking" >&2
        if _pog_check_bead_claimed "$assignee_id"; then
            return 0
        fi
        return 1
    fi

    return 1
}
