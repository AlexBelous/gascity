#!/usr/bin/env bash
#
# test-push-ownership-guard.sh — unit tests for the pre-push bead
# ownership/staleness guard (bead ga-fip9ps.1; guards the race described in
# ga-fip9ps). Three layers:
#
#   1. assert_bead_still_claimed (scripts/push-ownership-guard.sh) exercised
#      directly against real temp git repos with a fake `bd` on PATH: allow
#      on a clean claim; block on closed/reassigned/rerouted/held; fail
#      closed when bd is unreachable or the read times out.
#   2. Bead-id resolution: branch name wins over the assignee fallback and
#      only warns (doesn't hard-fail) on disagreement; falls back to the
#      assignee lookup when the branch name doesn't match; allows when
#      neither resolves (nothing to check — required for Layer A to be
#      wired in unconditionally without blocking unrelated pushes). Also
#      pins a known limitation of that fallback — see the KNOWN LIMITATION
#      comment on _pog_resolve_bead_id in push-ownership-guard.sh.
#   3. Hook wiring: a real .githooks/pre-push, installed into a real temp
#      repo pushing to a real bare remote, actually rejects a stale-claim
#      push, actually allows a clean one, and `--no-verify` actually
#      bypasses the guard end to end.
#
# No network, no gh, no models — a fake `bd` stands in for the real one.

set -uo pipefail

TEST_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LIB="$TEST_DIR/push-ownership-guard.sh"
REPO_ROOT="$(cd "$TEST_DIR/.." && pwd)"

pass=0; fail=0
record_pass() { echo "  ok   $1"; pass=$((pass + 1)); }
record_fail() { echo "  FAIL $1 — $2"; fail=$((fail + 1)); }

# Deterministic, hermetic git identity for the temp repos.
export GIT_AUTHOR_NAME="Test Author" GIT_AUTHOR_EMAIL="author@example.com"
export GIT_COMMITTER_NAME="Test Pusher" GIT_COMMITTER_EMAIL="pusher@example.com"
export GIT_CONFIG_NOSYSTEM=1
unset GIT_DIR GIT_WORK_TREE 2>/dev/null || true

# ---------------------------------------------------------------------------
# Repo/remote helpers (mirrors scripts/test-rebase-resolve.sh's harness).
# ---------------------------------------------------------------------------

# new_repo: create an isolated git repo in a fresh tmpdir, print its path.
new_repo() {
    local d
    d="$(mktemp -d "${TMPDIR:-/tmp}/gc-pog-test.XXXXXX")"
    git -C "$d" init -q -b main
    git -C "$d" config commit.gpgsign false
    printf '%s' "$d"
}

# new_repo_with_branch <branch>: a repo with one commit on main, then
# checked out to a fresh <branch>. Prints the repo path.
new_repo_with_branch() {
    local branch="$1"
    local d; d="$(new_repo)"
    (
        cd "$d" || exit 1
        printf 'base\n' > f.txt
        git add -A && git commit -qm base
        git checkout -q -b "$branch"
    )
    printf '%s' "$d"
}

# new_bare_remote: an isolated bare repo standing in for `origin`.
new_bare_remote() {
    local d
    d="$(mktemp -d "${TMPDIR:-/tmp}/gc-pog-remote.XXXXXX")"
    git init -q --bare -b main "$d"
    printf '%s' "$d"
}

# remote_sha <remote> <ref>: current SHA of <ref> on the bare remote, or empty.
remote_sha() {
    git -C "$1" rev-parse --verify -q "$2" 2>/dev/null || true
}

# ---------------------------------------------------------------------------
# Fake `bd`: behavior driven by state files, so each test writes exactly the
# response it needs without a combinatorial helper signature.
#
#   <dir>/fake-bd-state/show-json      -- `bd show <any-id> --json` echoes
#                                          this verbatim (exit 0).
#   <dir>/fake-bd-state/show-exit      -- if present, `bd show` exits with
#                                          this code instead (no output) —
#                                          simulates bd/Dolt unreachable.
#   <dir>/fake-bd-state/show-sleep     -- if present, `bd show` sleeps this
#                                          many seconds first — simulates a
#                                          hung read for timeout tests.
#   <dir>/fake-bd-state/show-fail-count -- if present, the first N `bd show`
#                                          calls exit 1 (no output) and only
#                                          call N+1 onward falls through to
#                                          show-exit/show-json — simulates a
#                                          transient failure that clears up
#                                          after N attempts, for retry tests.
#                                          Each call increments
#                                          show-call-count (1-based) so a
#                                          test can assert exactly how many
#                                          attempts were made.
#   <dir>/fake-bd-state/list-json      -- response to `bd list ... --json`
#                                          (defaults to "[]").
#   <dir>/fake-bd-state/list-fail-count -- same as show-fail-count, for
#                                          `bd list` (counter:
#                                          list-call-count).
# ---------------------------------------------------------------------------

write_fake_bd() {
    local dir="$1"
    mkdir -p "$dir/fake-bd-state"
    cat > "$dir/bd" <<'FAKE'
#!/usr/bin/env bash
set -euo pipefail
state="$(dirname "$0")/fake-bd-state"
case "$1" in
  show)
    if [ -f "$state/show-sleep" ]; then
      sleep "$(cat "$state/show-sleep")"
    fi
    if [ -f "$state/show-fail-count" ]; then
      n="$(cat "$state/show-fail-count")"
      c=0
      [ -f "$state/show-call-count" ] && c="$(cat "$state/show-call-count")"
      c=$((c + 1))
      echo "$c" > "$state/show-call-count"
      if [ "$c" -le "$n" ]; then
        exit 1
      fi
    fi
    if [ -f "$state/show-exit" ]; then
      exit "$(cat "$state/show-exit")"
    fi
    if [ -f "$state/show-json" ]; then
      cat "$state/show-json"
      exit 0
    fi
    exit 1
    ;;
  list)
    if [ -f "$state/list-fail-count" ]; then
      n="$(cat "$state/list-fail-count")"
      c=0
      [ -f "$state/list-call-count" ] && c="$(cat "$state/list-call-count")"
      c=$((c + 1))
      echo "$c" > "$state/list-call-count"
      if [ "$c" -le "$n" ]; then
        exit 1
      fi
    fi
    if [ -f "$state/list-json" ]; then
      cat "$state/list-json"
      exit 0
    fi
    printf '[]'
    exit 0
    ;;
  *)
    exit 1
    ;;
esac
FAKE
    chmod +x "$dir/bd"
}

# write_show_json <fake-bd-dir> <id> <status> <assignee> <routed_to> [labels-json-array]
write_show_json() {
    local fbd="$1" id="$2" status="$3" assignee="$4" routed_to="$5" labels="${6:-[]}"
    mkdir -p "$fbd/fake-bd-state"
    printf '[{"id":"%s","status":"%s","assignee":"%s","metadata":{"gc.routed_to":"%s"},"labels":%s}]' \
        "$id" "$status" "$assignee" "$routed_to" "$labels" > "$fbd/fake-bd-state/show-json"
}

# write_show_json_for <fake-bd-dir> <id> <status> <assignee> <routed_to> [labels-json-array] [work-branch]
#
# Like write_show_json, but keyed by <id> so a single fake bd process can
# answer `bd show` differently for two different ids in the same test (the
# closed branch-embedded id vs. a distinct live candidate) — needed by
# write_fake_bd_multi below, which write_fake_bd's single fixed show-json
# file cannot support. Adds gc.work_branch to metadata (empty when the 7th
# arg is omitted) — the signal the same-branch substitution search verifies
# a candidate against.
write_show_json_for() {
    local fbd="$1" id="$2" status="$3" assignee="$4" routed_to="$5" labels="${6:-[]}" work_branch="${7:-}"
    mkdir -p "$fbd/fake-bd-state"
    local safe_id; safe_id="$(printf '%s' "$id" | tr -c 'A-Za-z0-9._-' '_')"
    printf '[{"id":"%s","status":"%s","assignee":"%s","metadata":{"gc.routed_to":"%s","gc.work_branch":"%s"},"labels":%s}]' \
        "$id" "$status" "$assignee" "$routed_to" "$work_branch" "$labels" > "$fbd/fake-bd-state/show-json.$safe_id"
}

# write_list_json_for <fake-bd-dir> <assignee> <json>: the response to
# `bd list --assignee=<assignee> ... --json` for exactly that identity, for
# use with write_fake_bd_multi below.
write_list_json_for() {
    local fbd="$1" assignee="$2" json="$3"
    mkdir -p "$fbd/fake-bd-state"
    local safe_a; safe_a="$(printf '%s' "$assignee" | tr -c 'A-Za-z0-9._-' '_')"
    printf '%s' "$json" > "$fbd/fake-bd-state/list-json.$safe_a"
}

# write_fake_bd_multi <fake-bd-dir>: like write_fake_bd, but `bd show <id>`
# answers per-id (from write_show_json_for's show-json.<id> files) and
# `bd list --assignee=X ...` answers per-identity (from write_list_json_for's
# list-json.<X> files) instead of one fixed response for every call. Needed
# by the verified-same-branch-substitution tests below, which must give the
# primary (closed) id and a distinct candidate id different `bd show`
# answers, and different identities different `bd list` answers, within the
# same test.
#
#   <dir>/fake-bd-state/show-exit  -- if present, EVERY `bd show` call exits
#                                      with this code (no output), regardless
#                                      of id — simulates bd/Dolt unreachable.
#   <dir>/fake-bd-state/list-exit  -- same, for every `bd list` call.
#   <dir>/fake-bd-state/show-calls.log -- one line per `bd show` call, the id
#                                      requested — lets a test assert exactly
#                                      which ids were (or weren't) read.
#   <dir>/fake-bd-state/list-calls.log -- one line per `bd list` call, the
#                                      --assignee value requested.
write_fake_bd_multi() {
    local dir="$1"
    mkdir -p "$dir/fake-bd-state"
    cat > "$dir/bd" <<'FAKE'
#!/usr/bin/env bash
set -euo pipefail
state="$(dirname "$0")/fake-bd-state"
case "$1" in
  show)
    id="${2:-}"
    printf '%s\n' "$id" >> "$state/show-calls.log" 2>/dev/null || true
    if [ -f "$state/show-exit" ]; then
      exit "$(cat "$state/show-exit")"
    fi
    safe_id="$(printf '%s' "$id" | tr -c 'A-Za-z0-9._-' '_')"
    if [ -f "$state/show-json.$safe_id" ]; then
      cat "$state/show-json.$safe_id"
      exit 0
    fi
    exit 1
    ;;
  list)
    assignee=""
    for arg in "$@"; do
      case "$arg" in
        --assignee=*) assignee="${arg#--assignee=}" ;;
      esac
    done
    printf '%s\n' "$assignee" >> "$state/list-calls.log" 2>/dev/null || true
    if [ -f "$state/list-exit" ]; then
      exit "$(cat "$state/list-exit")"
    fi
    safe_a="$(printf '%s' "$assignee" | tr -c 'A-Za-z0-9._-' '_')"
    if [ -f "$state/list-json.$safe_a" ]; then
      cat "$state/list-json.$safe_a"
      exit 0
    fi
    printf '[]'
    exit 0
    ;;
  *)
    exit 1
    ;;
esac
FAKE
    chmod +x "$dir/bd"
}

# run_guard <repo> <fake-bd-dir> <gc-agent> <gc-template> [pog-timeout-seconds] [gc-session-id] [gc-session-name]
#
# Runs assert_bead_still_claimed with cwd=<repo>, PATH prefixed with
# <fake-bd-dir>, and the given env. GC_SESSION_ID/GC_SESSION_NAME are
# optional (empty by default) so existing GC_AGENT-only callers are
# unchanged; supply them to exercise the session identity-set match.
# Combined stdout+stderr is the caller's to capture; the subshell's exit
# code is assert_bead_still_claimed's.
#
# POG_READ_ATTEMPTS defaults to 1 here (not the production default of 3) so
# every pre-existing caller that doesn't care about retry behavior keeps its
# original single-shot timing untouched. Tests that exercise retries set
# POG_READ_ATTEMPTS as a prefix on the call, e.g.
# `POG_READ_ATTEMPTS=3 run_guard ...`.
run_guard() {
    local repo="$1" fbd="$2" agent="$3" template="$4" pog_timeout="${5:-5}"
    local session_id="${6:-}" session_name="${7:-}"
    local read_attempts="${POG_READ_ATTEMPTS:-1}"
    (
        cd "$repo" || exit 1
        PATH="$fbd:$PATH" GC_AGENT="$agent" GC_TEMPLATE="$template" \
            GC_SESSION_ID="$session_id" GC_SESSION_NAME="$session_name" \
            POG_TIMEOUT_SECONDS="$pog_timeout" POG_READ_ATTEMPTS="$read_attempts" LIB="$LIB" \
            bash -c '. "$LIB"; assert_bead_still_claimed'
    )
}

# run_guard_zsh: identical to run_guard, but sources and calls the guard
# under a real zsh subprocess instead of bash. push-ownership-guard.sh is
# SOURCED into the deployer's ambient interactive shell (zsh, in this fork —
# see rebase-resolve-lib.sh's attempt_bounded_self_rebase, Layer B), not
# executed via its own bash shebang, so zsh's parsing/builtin rules apply to
# assert_bead_still_claimed's body at call time (ga-xi7wi6).
run_guard_zsh() {
    local repo="$1" fbd="$2" agent="$3" template="$4" pog_timeout="${5:-5}"
    local session_id="${6:-}" session_name="${7:-}"
    (
        cd "$repo" || exit 1
        PATH="$fbd:$PATH" GC_AGENT="$agent" GC_TEMPLATE="$template" \
            GC_SESSION_ID="$session_id" GC_SESSION_NAME="$session_name" \
            POG_TIMEOUT_SECONDS="$pog_timeout" LIB="$LIB" \
            zsh -c '. "$LIB"; assert_bead_still_claimed'
    )
}

# ---------------------------------------------------------------------------
# assert_bead_still_claimed — direct tests.
# ---------------------------------------------------------------------------

test_allow_clean_claim() {
    local repo fbd out rc
    repo="$(new_repo_with_branch "builder/ga-abc123.1-my-feature")"
    fbd="$(mktemp -d "${TMPDIR:-/tmp}/gc-pog-fakebd.XXXXXX")"
    write_fake_bd "$fbd"
    write_show_json "$fbd" "ga-abc123.1" "in_progress" "agent-x" "tmpl-x" "[]"
    out="$(run_guard "$repo" "$fbd" "agent-x" "tmpl-x" 2>&1)"; rc=$?
    if [[ $rc -eq 0 ]]; then
        record_pass "allow/clean-claim"
    else
        record_fail "allow/clean-claim" "expected rc=0, got rc=$rc, output: $out"
    fi
    rm -rf "$repo" "$fbd"
}

# test_allow_clean_claim_under_zsh mirrors test_allow_clean_claim exactly,
# but runs assert_bead_still_claimed under a real zsh subprocess (ga-xi7wi6):
# a local variable named 'status' collides with zsh's read-only special
# parameter of the same name, so the guard must never bind that name.
# Skips (does not fail) when zsh isn't installed, matching the fallback
# style of _pog_timeout degrading gracefully on a missing dev tool rather
# than failing the whole suite closed.
test_allow_clean_claim_under_zsh() {
    if ! command -v zsh >/dev/null 2>&1; then
        echo "  skip allow/clean-claim-under-zsh — zsh not installed"
        return
    fi
    local repo fbd out rc
    repo="$(new_repo_with_branch "builder/ga-abc123.1-my-feature")"
    fbd="$(mktemp -d "${TMPDIR:-/tmp}/gc-pog-fakebd.XXXXXX")"
    write_fake_bd "$fbd"
    write_show_json "$fbd" "ga-abc123.1" "in_progress" "agent-x" "tmpl-x" "[]"
    out="$(run_guard_zsh "$repo" "$fbd" "agent-x" "tmpl-x" 2>&1)"; rc=$?
    if [[ $rc -eq 0 ]]; then
        record_pass "allow/clean-claim-under-zsh"
    else
        record_fail "allow/clean-claim-under-zsh" "expected rc=0, got rc=$rc, output: $out"
    fi
    rm -rf "$repo" "$fbd"
}

test_block_on_closed() {
    local repo fbd out rc
    repo="$(new_repo_with_branch "builder/ga-abc123.1-my-feature")"
    fbd="$(mktemp -d "${TMPDIR:-/tmp}/gc-pog-fakebd.XXXXXX")"
    write_fake_bd "$fbd"
    write_show_json "$fbd" "ga-abc123.1" "closed" "agent-x" "tmpl-x" "[]"
    out="$(run_guard "$repo" "$fbd" "agent-x" "tmpl-x" 2>&1)"; rc=$?
    if [[ $rc -ne 0 ]] && grep -qi "status" <<<"$out" && grep -q -- "--no-verify" <<<"$out"; then
        record_pass "block/closed (rc=$rc, names the failed check + mentions --no-verify)"
    else
        record_fail "block/closed" "expected non-zero rc mentioning status+--no-verify, got rc=$rc, output: $out"
    fi
    rm -rf "$repo" "$fbd"
}

test_block_on_reassigned() {
    local repo fbd out rc
    repo="$(new_repo_with_branch "builder/ga-abc123.1-my-feature")"
    fbd="$(mktemp -d "${TMPDIR:-/tmp}/gc-pog-fakebd.XXXXXX")"
    write_fake_bd "$fbd"
    # assignee differs from EVERY supplied session identity
    # (GC_AGENT=agent-x, GC_SESSION_ID=sess-id-x, GC_SESSION_NAME=sess-name-x)
    # → the identity-set match finds no owner → still blocks.
    write_show_json "$fbd" "ga-abc123.1" "in_progress" "someone-else" "tmpl-x" "[]"
    out="$(run_guard "$repo" "$fbd" "agent-x" "tmpl-x" 5 "sess-id-x" "sess-name-x" 2>&1)"; rc=$?
    if [[ $rc -ne 0 ]] && grep -qi "assignee" <<<"$out" && grep -q -- "--no-verify" <<<"$out"; then
        record_pass "block/reassigned (rc=$rc, names the failed check + mentions --no-verify)"
    else
        record_fail "block/reassigned" "expected non-zero rc mentioning assignee+--no-verify, got rc=$rc, output: $out"
    fi
    rm -rf "$repo" "$fbd"
}

# The claim path sets bead.assignee from the first non-empty of
# GC_SESSION_NAME/GC_SESSION_ID/GC_ALIAS/GC_AGENT, so a bead legitimately
# owned by this session can carry the session id (or name) as its assignee
# while GC_AGENT differs. These two tests pin that the guard accepts ANY
# current-session identity, not GC_AGENT alone (the false-block fixed here).
test_allow_when_assignee_is_session_id() {
    local repo fbd out rc
    repo="$(new_repo_with_branch "builder/ga-abc123.1-my-feature")"
    fbd="$(mktemp -d "${TMPDIR:-/tmp}/gc-pog-fakebd.XXXXXX")"
    write_fake_bd "$fbd"
    write_show_json "$fbd" "ga-abc123.1" "in_progress" "sess-id-x" "tmpl-x" "[]"
    out="$(run_guard "$repo" "$fbd" "agent-x" "tmpl-x" 5 "sess-id-x" "sess-name-x" 2>&1)"; rc=$?
    if [[ $rc -eq 0 ]]; then
        record_pass "allow/assignee-is-session-id (rc=0, GC_AGENT differs)"
    else
        record_fail "allow/assignee-is-session-id" "expected rc=0, got rc=$rc, output: $out"
    fi
    rm -rf "$repo" "$fbd"
}

test_allow_when_assignee_is_session_name() {
    local repo fbd out rc
    repo="$(new_repo_with_branch "builder/ga-abc123.1-my-feature")"
    fbd="$(mktemp -d "${TMPDIR:-/tmp}/gc-pog-fakebd.XXXXXX")"
    write_fake_bd "$fbd"
    write_show_json "$fbd" "ga-abc123.1" "in_progress" "sess-name-x" "tmpl-x" "[]"
    out="$(run_guard "$repo" "$fbd" "agent-x" "tmpl-x" 5 "sess-id-x" "sess-name-x" 2>&1)"; rc=$?
    if [[ $rc -eq 0 ]]; then
        record_pass "allow/assignee-is-session-name (rc=0, GC_AGENT differs)"
    else
        record_fail "allow/assignee-is-session-name" "expected rc=0, got rc=$rc, output: $out"
    fi
    rm -rf "$repo" "$fbd"
}

test_block_on_routed_to_changed() {
    local repo fbd out rc
    repo="$(new_repo_with_branch "builder/ga-abc123.1-my-feature")"
    fbd="$(mktemp -d "${TMPDIR:-/tmp}/gc-pog-fakebd.XXXXXX")"
    write_fake_bd "$fbd"
    write_show_json "$fbd" "ga-abc123.1" "in_progress" "agent-x" "other-tmpl" "[]"
    out="$(run_guard "$repo" "$fbd" "agent-x" "tmpl-x" 2>&1)"; rc=$?
    if [[ $rc -ne 0 ]] && grep -qi "routed_to" <<<"$out" && grep -q -- "--no-verify" <<<"$out"; then
        record_pass "block/routed-to-changed (rc=$rc, names the failed check + mentions --no-verify)"
    else
        record_fail "block/routed-to-changed" "expected non-zero rc mentioning routed_to+--no-verify, got rc=$rc, output: $out"
    fi
    rm -rf "$repo" "$fbd"
}

test_block_on_hold_mayor() {
    local repo fbd out rc
    repo="$(new_repo_with_branch "builder/ga-abc123.1-my-feature")"
    fbd="$(mktemp -d "${TMPDIR:-/tmp}/gc-pog-fakebd.XXXXXX")"
    write_fake_bd "$fbd"
    write_show_json "$fbd" "ga-abc123.1" "in_progress" "agent-x" "tmpl-x" '["hold:mayor"]'
    out="$(run_guard "$repo" "$fbd" "agent-x" "tmpl-x" 2>&1)"; rc=$?
    if [[ $rc -ne 0 ]] && grep -qi "hold:mayor" <<<"$out" && grep -q -- "--no-verify" <<<"$out"; then
        record_pass "block/hold-mayor (rc=$rc, names the failed check + mentions --no-verify)"
    else
        record_fail "block/hold-mayor" "expected non-zero rc mentioning hold:mayor+--no-verify, got rc=$rc, output: $out"
    fi
    rm -rf "$repo" "$fbd"
}

test_block_on_hold_external() {
    local repo fbd out rc
    repo="$(new_repo_with_branch "builder/ga-abc123.1-my-feature")"
    fbd="$(mktemp -d "${TMPDIR:-/tmp}/gc-pog-fakebd.XXXXXX")"
    write_fake_bd "$fbd"
    write_show_json "$fbd" "ga-abc123.1" "in_progress" "agent-x" "tmpl-x" '["hold:external"]'
    out="$(run_guard "$repo" "$fbd" "agent-x" "tmpl-x" 2>&1)"; rc=$?
    if [[ $rc -ne 0 ]] && grep -qi "hold:external" <<<"$out" && grep -q -- "--no-verify" <<<"$out"; then
        record_pass "block/hold-external (rc=$rc, names the failed check + mentions --no-verify)"
    else
        record_fail "block/hold-external" "expected non-zero rc mentioning hold:external+--no-verify, got rc=$rc, output: $out"
    fi
    rm -rf "$repo" "$fbd"
}

test_block_on_bd_unreachable() {
    local repo fbd out rc
    repo="$(new_repo_with_branch "builder/ga-abc123.1-my-feature")"
    fbd="$(mktemp -d "${TMPDIR:-/tmp}/gc-pog-fakebd.XXXXXX")"
    write_fake_bd "$fbd"
    mkdir -p "$fbd/fake-bd-state"
    echo 1 > "$fbd/fake-bd-state/show-exit"
    out="$(run_guard "$repo" "$fbd" "agent-x" "tmpl-x" 2>&1)"; rc=$?
    if [[ $rc -ne 0 ]] && grep -q -- "--no-verify" <<<"$out"; then
        record_pass "block/bd-unreachable (rc=$rc, fails closed, mentions --no-verify)"
    else
        record_fail "block/bd-unreachable" "expected non-zero rc mentioning --no-verify, got rc=$rc, output: $out"
    fi
    rm -rf "$repo" "$fbd"
}

test_block_on_bd_timeout() {
    local repo fbd out rc
    repo="$(new_repo_with_branch "builder/ga-abc123.1-my-feature")"
    fbd="$(mktemp -d "${TMPDIR:-/tmp}/gc-pog-fakebd.XXXXXX")"
    write_fake_bd "$fbd"
    mkdir -p "$fbd/fake-bd-state"
    echo 3 > "$fbd/fake-bd-state/show-sleep"
    out="$(run_guard "$repo" "$fbd" "agent-x" "tmpl-x" 1 2>&1)"; rc=$?  # POG_TIMEOUT_SECONDS=1, sleep=3
    if [[ $rc -ne 0 ]] && grep -q -- "--no-verify" <<<"$out"; then
        record_pass "block/bd-timeout (rc=$rc, bounded read killed the hang, mentions --no-verify)"
    else
        record_fail "block/bd-timeout" "expected non-zero rc mentioning --no-verify, got rc=$rc, output: $out"
    fi
    rm -rf "$repo" "$fbd"
}

# ---------------------------------------------------------------------------
# Bounded retry (ga-e8hal3): a single transient bd/Dolt read failure must
# not fail the push closed — only exhausting every attempt does. Retrying
# must never mask a genuine, successfully-read ownership change.
# ---------------------------------------------------------------------------

test_retry_recovers_from_transient_failure() {
    local repo fbd out rc
    repo="$(new_repo_with_branch "builder/ga-abc123.1-my-feature")"
    fbd="$(mktemp -d "${TMPDIR:-/tmp}/gc-pog-fakebd.XXXXXX")"
    write_fake_bd "$fbd"
    write_show_json "$fbd" "ga-abc123.1" "in_progress" "agent-x" "tmpl-x" "[]"
    echo 1 > "$fbd/fake-bd-state/show-fail-count"  # first bd show call fails, second succeeds
    out="$(POG_READ_ATTEMPTS=3 run_guard "$repo" "$fbd" "agent-x" "tmpl-x" 2>&1)"; rc=$?
    if [[ $rc -eq 0 ]]; then
        record_pass "retry/recovers-from-transient-failure (rc=0, one flaky read then success allows the push)"
    else
        record_fail "retry/recovers-from-transient-failure" "expected rc=0 after one flaky read then success, got rc=$rc, output: $out"
    fi
    rm -rf "$repo" "$fbd"
}

test_retry_exhausted_still_blocks() {
    local repo fbd out rc calls
    repo="$(new_repo_with_branch "builder/ga-abc123.1-my-feature")"
    fbd="$(mktemp -d "${TMPDIR:-/tmp}/gc-pog-fakebd.XXXXXX")"
    write_fake_bd "$fbd"
    echo 99 > "$fbd/fake-bd-state/show-fail-count"  # never succeeds within any attempt budget
    out="$(POG_READ_ATTEMPTS=3 run_guard "$repo" "$fbd" "agent-x" "tmpl-x" 2>&1)"; rc=$?
    calls="$(cat "$fbd/fake-bd-state/show-call-count" 2>/dev/null || echo 0)"
    if [[ $rc -ne 0 ]] && [[ "$calls" -eq 3 ]] && grep -q -- "--no-verify" <<<"$out"; then
        record_pass "retry/exhausted-still-blocks (rc=$rc, retried exactly 3x then blocked, mentions --no-verify)"
    else
        record_fail "retry/exhausted-still-blocks" "expected rc!=0 after exactly 3 attempts mentioning --no-verify, got rc=$rc calls=$calls, output: $out"
    fi
    rm -rf "$repo" "$fbd"
}

test_retry_recovers_then_still_blocks_on_real_ownership_change() {
    local repo fbd out rc
    repo="$(new_repo_with_branch "builder/ga-abc123.1-my-feature")"
    fbd="$(mktemp -d "${TMPDIR:-/tmp}/gc-pog-fakebd.XXXXXX")"
    write_fake_bd "$fbd"
    write_show_json "$fbd" "ga-abc123.1" "closed" "agent-x" "tmpl-x" "[]"
    echo 1 > "$fbd/fake-bd-state/show-fail-count"
    out="$(POG_READ_ATTEMPTS=3 run_guard "$repo" "$fbd" "agent-x" "tmpl-x" 2>&1)"; rc=$?
    if [[ $rc -ne 0 ]] && grep -qi "status" <<<"$out" && grep -q -- "--no-verify" <<<"$out"; then
        record_pass "retry/recovers-then-still-blocks-on-real-ownership-change (rc=$rc, retries don't mask a genuine close)"
    else
        record_fail "retry/recovers-then-still-blocks-on-real-ownership-change" "expected non-zero rc mentioning status+--no-verify after one flaky read then a real close, got rc=$rc, output: $out"
    fi
    rm -rf "$repo" "$fbd"
}

test_retry_unreachable_message_mentions_retry_before_no_verify() {
    local repo fbd out rc before_noverify
    repo="$(new_repo_with_branch "builder/ga-abc123.1-my-feature")"
    fbd="$(mktemp -d "${TMPDIR:-/tmp}/gc-pog-fakebd.XXXXXX")"
    write_fake_bd "$fbd"
    mkdir -p "$fbd/fake-bd-state"
    echo 1 > "$fbd/fake-bd-state/show-exit"
    out="$(POG_READ_ATTEMPTS=2 run_guard "$repo" "$fbd" "agent-x" "tmpl-x" 2>&1)"; rc=$?
    before_noverify="${out%%--no-verify*}"
    if [[ $rc -ne 0 ]] && grep -qi "re-run" <<<"$before_noverify" && grep -q -- "--no-verify" <<<"$out"; then
        record_pass "retry/unreachable-message-names-retry-before-no-verify (rc=$rc)"
    else
        record_fail "retry/unreachable-message-names-retry-before-no-verify" "expected retry-first wording before --no-verify, got rc=$rc, output: $out"
    fi
    rm -rf "$repo" "$fbd"
}

test_retry_parse_failure_message_mentions_retry_before_no_verify() {
    local repo fbd out rc before_noverify
    repo="$(new_repo_with_branch "builder/ga-abc123.1-my-feature")"
    fbd="$(mktemp -d "${TMPDIR:-/tmp}/gc-pog-fakebd.XXXXXX")"
    write_fake_bd "$fbd"
    mkdir -p "$fbd/fake-bd-state"
    printf 'not valid json' > "$fbd/fake-bd-state/show-json"
    out="$(run_guard "$repo" "$fbd" "agent-x" "tmpl-x" 2>&1)"; rc=$?
    before_noverify="${out%%--no-verify*}"
    if [[ $rc -ne 0 ]] && grep -qi "re-run" <<<"$before_noverify" && grep -q -- "--no-verify" <<<"$out"; then
        record_pass "retry/parse-failure-message-names-retry-before-no-verify (rc=$rc)"
    else
        record_fail "retry/parse-failure-message-names-retry-before-no-verify" "expected retry-first wording before --no-verify, got rc=$rc, output: $out"
    fi
    rm -rf "$repo" "$fbd"
}

# ---------------------------------------------------------------------------
# Bead-id resolution.
# ---------------------------------------------------------------------------

test_bead_id_branch_wins_and_warns_on_disagreement() {
    local repo fbd out rc
    repo="$(new_repo_with_branch "builder/ga-abc123.1-my-feature")"
    fbd="$(mktemp -d "${TMPDIR:-/tmp}/gc-pog-fakebd.XXXXXX")"
    write_fake_bd "$fbd"
    # Branch resolves to ga-abc123.1; the assignee fallback disagrees.
    printf '[{"id":"ga-zzz999.2"}]' > "$fbd/fake-bd-state/list-json"
    write_show_json "$fbd" "ga-abc123.1" "in_progress" "agent-x" "tmpl-x" "[]"
    out="$(run_guard "$repo" "$fbd" "agent-x" "tmpl-x" 2>&1)"; rc=$?
    if [[ $rc -eq 0 ]] && grep -q "ga-abc123.1" <<<"$out" && grep -q "ga-zzz999.2" <<<"$out"; then
        record_pass "resolve/branch-wins-warns-on-disagreement (rc=0, warning names both ids)"
    else
        record_fail "resolve/branch-wins-warns-on-disagreement" "expected rc=0 with both ids mentioned, got rc=$rc, output: $out"
    fi
    rm -rf "$repo" "$fbd"
}

# Regression: a branch encoding a multi-level sub-bead id (a grandchild bead,
# e.g. ga-o3ko1j.4.3) must resolve to the FULL id, not truncate after the
# first dotted segment. Caught live: builder/ga-o3ko1j.4.3's push resolved to
# ga-o3ko1j.4 (a different, already-closed parent bead), blocking a healthy
# in-progress push as "stale". Forces the assignee-fallback to disagree so
# the resolver's warning names the id it actually picked — the only
# observable signal for resolution output (see the sibling
# branch-wins-warns-on-disagreement test above for the same technique).
test_bead_id_branch_resolves_multi_level_subbead_id() {
    local repo fbd out rc
    repo="$(new_repo_with_branch "builder/ga-o3ko1j.4.3-dead-assignee-fallback")"
    fbd="$(mktemp -d "${TMPDIR:-/tmp}/gc-pog-fakebd.XXXXXX")"
    write_fake_bd "$fbd"
    printf '[{"id":"ga-other01.9"}]' > "$fbd/fake-bd-state/list-json"
    write_show_json "$fbd" "ga-o3ko1j.4.3" "in_progress" "agent-x" "tmpl-x" "[]"
    out="$(run_guard "$repo" "$fbd" "agent-x" "tmpl-x" 2>&1)"; rc=$?
    if [[ $rc -eq 0 ]] && grep -q "ga-o3ko1j.4.3" <<<"$out"; then
        record_pass "resolve/branch-resolves-full-multi-level-subbead-id (rc=0, full id ga-o3ko1j.4.3 named, not truncated to ga-o3ko1j.4)"
    else
        record_fail "resolve/branch-resolves-full-multi-level-subbead-id" "expected rc=0 with full id ga-o3ko1j.4.3 in the resolver's warning, got rc=$rc, output: $out"
    fi
    rm -rf "$repo" "$fbd"
}

test_bead_id_fallback_used_when_branch_no_match() {
    local repo fbd out rc
    repo="$(new_repo_with_branch "chore/unrelated-cleanup")"
    fbd="$(mktemp -d "${TMPDIR:-/tmp}/gc-pog-fakebd.XXXXXX")"
    write_fake_bd "$fbd"
    printf '[{"id":"ga-fallbk.3"}]' > "$fbd/fake-bd-state/list-json"
    write_show_json "$fbd" "ga-fallbk.3" "in_progress" "agent-x" "tmpl-x" "[]"
    out="$(run_guard "$repo" "$fbd" "agent-x" "tmpl-x" 2>&1)"; rc=$?
    if [[ $rc -eq 0 ]]; then
        record_pass "resolve/assignee-fallback-used-when-branch-no-match (rc=0)"
    else
        record_fail "resolve/assignee-fallback-used-when-branch-no-match" "expected rc=0, got rc=$rc, output: $out"
    fi
    rm -rf "$repo" "$fbd"
}

# Regression (ga-wwswme): deploy/*-gate branches embed the id of the bead
# being GATED, which is routinely CLOSED by the time the gate branch is
# pushed (that's the whole point of a deploy gate) — the plain branch-wins
# rule misresolved these pushes to the closed gated bead and blocked them.
# Real repro pinned here verbatim: PR #4731 pushed from deploy/ga-g5ihlp-gate
# by gascity/investigator, whose actual live claim was ga-mit0gh (assigned,
# in_progress) — the guard resolved to the closed ga-g5ihlp and blocked it.
# Needs an id-aware fake bd (unlike the other resolve/* tests, which don't
# care what id `bd show` was called with) because the real discriminator
# here is the downstream effect — allowed vs blocked — not just which id
# the resolver's message happens to name.
test_bead_id_deploy_gate_branch_prefers_live_assignee() {
    local repo fbd out rc
    repo="$(new_repo_with_branch "deploy/ga-g5ihlp-gate")"
    fbd="$(mktemp -d "${TMPDIR:-/tmp}/gc-pog-fakebd.XXXXXX")"
    cat > "$fbd/bd" <<'FAKE'
#!/usr/bin/env bash
set -euo pipefail
case "$1" in
  show)
    case "$2" in
      ga-g5ihlp) printf '[{"id":"ga-g5ihlp","status":"closed","assignee":"agent-x","metadata":{"gc.routed_to":"tmpl-x"},"labels":[]}]' ;;
      ga-mit0gh) printf '[{"id":"ga-mit0gh","status":"in_progress","assignee":"agent-x","metadata":{"gc.routed_to":"tmpl-x"},"labels":[]}]' ;;
      *) exit 1 ;;
    esac
    ;;
  list)
    printf '[{"id":"ga-mit0gh"}]'
    ;;
  *)
    exit 1
    ;;
esac
FAKE
    chmod +x "$fbd/bd"
    out="$(run_guard "$repo" "$fbd" "agent-x" "tmpl-x" 2>&1)"; rc=$?
    if [[ $rc -eq 0 ]]; then
        record_pass "resolve/deploy-gate-branch-prefers-live-assignee (rc=0, live assignee ga-mit0gh used instead of closed gated bead ga-g5ihlp)"
    else
        record_fail "resolve/deploy-gate-branch-prefers-live-assignee" "expected rc=0 (live assignee ga-mit0gh must win over closed gated bead ga-g5ihlp), got rc=$rc, output: $out"
    fi
    rm -rf "$repo" "$fbd"
}

# Companion to the test above: on a deploy/*-gate branch the branch-derived
# id is deliberately discarded, so when the assignee read SUCCEEDS and finds
# no in-progress assignment there is genuinely nothing to check and the push
# is allowed — the same "no session, nothing to check" semantics every
# unmatched branch already has. Pins that allow explicitly (the same way
# test_fallback_cannot_detect_staleness_after_status_leaves_in_progress pins
# its gap) so any future change here is a deliberate, visible decision.
test_bead_id_deploy_gate_branch_allows_when_no_live_assignee() {
    local repo fbd out rc
    repo="$(new_repo_with_branch "deploy/ga-g5ihlp-gate")"
    fbd="$(mktemp -d "${TMPDIR:-/tmp}/gc-pog-fakebd.XXXXXX")"
    write_fake_bd "$fbd"
    printf '[]' > "$fbd/fake-bd-state/list-json"
    # No show-json configured: with no id resolved, `bd show` must never be
    # called at all — the fake exits 1 on any show, which would surface as a
    # BLOCKED line if resolution ever fell back to the gated branch id.
    out="$(run_guard "$repo" "$fbd" "agent-x" "tmpl-x" 2>&1)"; rc=$?
    if [[ $rc -eq 0 ]] && ! grep -qi "BLOCKED" <<<"$out"; then
        record_pass "resolve/deploy-gate-branch-allows-when-no-live-assignee (rc=0, clean empty read leaves nothing to check)"
    else
        record_fail "resolve/deploy-gate-branch-allows-when-no-live-assignee" "expected rc=0 and no BLOCKED text (a successful but empty assignee read is a clean answer, not ambiguity), got rc=$rc, output: $out"
    fi
    rm -rf "$repo" "$fbd"
}

# Fail-closed on the deploy-gate branch shape: because the branch-derived id
# is discarded there, a FAILED assignee read (bd unreachable, or off PATH)
# leaves the guard with no signal at all — that's ambiguity, and the file
# header's FAIL CLOSED contract requires it to block, exactly as an
# unreachable `bd show` blocks on every other branch shape.
test_bead_id_deploy_gate_branch_blocks_when_assignee_lookup_fails() {
    local repo fbd out rc
    repo="$(new_repo_with_branch "deploy/ga-g5ihlp-gate")"
    fbd="$(mktemp -d "${TMPDIR:-/tmp}/gc-pog-fakebd.XXXXXX")"
    write_fake_bd "$fbd"
    echo 99 > "$fbd/fake-bd-state/list-fail-count"  # never succeeds within any attempt budget
    # POG_READ_ATTEMPTS=2 exercises the retry path while keeping the inter-
    # attempt sleep to a single second.
    out="$(POG_READ_ATTEMPTS=2 run_guard "$repo" "$fbd" "agent-x" "tmpl-x" 2>&1)"; rc=$?
    if [[ $rc -ne 0 ]] && grep -q -- "--no-verify" <<<"$out"; then
        record_pass "resolve/deploy-gate-branch-blocks-when-assignee-lookup-fails (rc=$rc, fail-closed with a bypass hint)"
    else
        record_fail "resolve/deploy-gate-branch-blocks-when-assignee-lookup-fails" "expected rc!=0 with a --no-verify bypass hint (an unreadable assignee lookup is ambiguity and must block), got rc=$rc, output: $out"
    fi
    rm -rf "$repo" "$fbd"
}

test_retry_recovers_bead_id_fallback_from_transient_failure() {
    local repo fbd out rc
    repo="$(new_repo_with_branch "chore/unrelated-cleanup")"
    fbd="$(mktemp -d "${TMPDIR:-/tmp}/gc-pog-fakebd.XXXXXX")"
    write_fake_bd "$fbd"
    echo 1 > "$fbd/fake-bd-state/list-fail-count"  # first bd list call fails, second succeeds
    printf '[{"id":"ga-fallbk.3"}]' > "$fbd/fake-bd-state/list-json"
    write_show_json "$fbd" "ga-fallbk.3" "in_progress" "agent-x" "tmpl-x" "[]"
    out="$(POG_READ_ATTEMPTS=3 run_guard "$repo" "$fbd" "agent-x" "tmpl-x" 2>&1)"; rc=$?
    if [[ $rc -eq 0 ]]; then
        record_pass "retry/recovers-bead-id-fallback-from-transient-failure (rc=0, list retry then resolved+allowed)"
    else
        record_fail "retry/recovers-bead-id-fallback-from-transient-failure" "expected rc=0, got rc=$rc, output: $out"
    fi
    rm -rf "$repo" "$fbd"
}

test_allow_when_no_bead_id_resolvable() {
    local repo fbd out rc
    repo="$(new_repo_with_branch "chore/unrelated-cleanup")"
    fbd="$(mktemp -d "${TMPDIR:-/tmp}/gc-pog-fakebd.XXXXXX")"
    write_fake_bd "$fbd"
    # No list-json configured -> fake `bd list` answers "[]"; no show-json
    # configured either, since nothing should ever call `bd show`.
    out="$(run_guard "$repo" "$fbd" "agent-x" "tmpl-x" 2>&1)"; rc=$?
    if [[ $rc -eq 0 ]]; then
        record_pass "resolve/allow-when-no-bead-id-resolvable (rc=0, nothing to check)"
    else
        record_fail "resolve/allow-when-no-bead-id-resolvable" "expected rc=0, got rc=$rc, output: $out"
    fi
    rm -rf "$repo" "$fbd"
}

# Known limitation: the assignee-fallback query filters on
# --status=in_progress, so it cannot find a bead that has already left that
# status (e.g. closed by the exact mayor ruling this guard exists to catch)
# by the time the fallback runs. See the KNOWN LIMITATION comment on
# _pog_resolve_bead_id in push-ownership-guard.sh — this pins the current,
# spec-compliant behavior so a change to the fallback algorithm is a
# deliberate, visible decision, not a silent regression either direction.
# Confirmed against a real bd via manual repro (ga-fip9ps.1 bead notes);
# this test reproduces it hermetically. This gap only affects the fallback
# path (branch name doesn't encode the bead id) — this repo's real branch
# convention (builder/<bead-id>-<slug>) always encodes the id, so the
# primary resolution path is unaffected.
test_fallback_cannot_detect_staleness_after_status_leaves_in_progress() {
    local repo fbd out rc
    repo="$(new_repo_with_branch "chore/unrelated-cleanup")"
    fbd="$(mktemp -d "${TMPDIR:-/tmp}/gc-pog-fakebd.XXXXXX")"
    write_fake_bd "$fbd"
    # The fallback's `bd list --status=in_progress` sees nothing, exactly as
    # a real bd would once this session's bead flipped to closed — the same
    # query that resolves the id is filtered on the very status the guard
    # exists to catch a bead leaving.
    printf '[]' > "$fbd/fake-bd-state/list-json"
    # Configured but must never be consulted: with no id resolved,
    # assert_bead_still_claimed's "nothing to check" branch returns before
    # ever calling `bd show`. If a future change to resolution makes it
    # less strict (e.g. caching the last-seen id), this would start hitting
    # show-json below and the second assertion below would catch it.
    write_show_json "$fbd" "ga-stale01" "closed" "agent-x" "tmpl-x" "[]"
    out="$(run_guard "$repo" "$fbd" "agent-x" "tmpl-x" 2>&1)"; rc=$?
    if [[ $rc -eq 0 ]] && ! grep -qi "BLOCKED" <<<"$out"; then
        record_pass "resolve/fallback-cannot-detect-staleness-after-status-leaves-in-progress (rc=0, known limitation pinned)"
    else
        record_fail "resolve/fallback-cannot-detect-staleness-after-status-leaves-in-progress" "expected rc=0 and no BLOCKED text (this documents a known gap — if it now fails because the gap was closed, update this test and the KNOWN LIMITATION comment together), got rc=$rc, output: $out"
    fi
    rm -rf "$repo" "$fbd"
}

# ---------------------------------------------------------------------------
# Verified same-branch substitution (ga-p2kq2p / ga-p2kq2p.1).
#
# THE GAP: a branch is legitimately reused across two TDD cycles — the first
# bead closes, a second bead claims the same branch name and is genuinely
# in_progress — but the branch-embedded id always resolves to the FIRST
# (now closed) bead, and assert_bead_still_claimed hard-blocks on that
# closed status alone, even though this session's actual live claim for
# this exact branch is one `bd list` away. Real repro: PR from branch
# builder/ga-igcny0.1.2.1, closed id ga-igcny0.1.2.1, live in-progress
# candidate ga-iyfdl1 whose gc.work_branch matched the branch exactly.
#
# THE FIX (verified same-branch substitution): ONLY when the primary
# resolved id's *status* check fails (never for an assignee/routed_to/hold
# failure — those are left untouched), search this session's in-progress
# beads across all four identities (GC_SESSION_NAME, GC_SESSION_ID,
# GC_ALIAS, GC_AGENT — same precedence order as the assignee-identity check
# a few lines below) for one whose gc.work_branch string-equals the current
# branch. On exactly one such candidate, re-run the COMPLETE check sequence
# against a FRESH bd show of that candidate and return whatever that re-run
# decides. Exactly one substitution hop — no recursion. Fails closed on
# every ambiguity (no candidate, missing/mismatched gc.work_branch, the
# candidate's own recheck failing, or the search itself erroring for every
# identity) by falling straight through to today's existing BLOCKED
# behavior for the ORIGINAL id — never a new default-allow.
#
# These tests exercise assert_bead_still_claimed's intended *behavior*, not
# yet its implementation: as of this commit push-ownership-guard.sh has no
# candidate-search logic at all, so every test below that expects an allow
# is RED against current code (today it hard-blocks on the closed primary
# id unconditionally — see the status check a few lines into
# assert_bead_still_claimed, which is the insertion point this feature
# adds to). Tests that expect a block pin current AND future behavior at
# once (a no-candidate / wrong-branch / failed-recheck / unreachable-search
# case must still block once this feature lands, exactly as it does today).
# ---------------------------------------------------------------------------

test_substitution_happy_path_zero_extra_list_calls() {
    local repo fbd out rc calls
    repo="$(new_repo_with_branch "builder/ga-abc123.1-my-feature")"
    fbd="$(mktemp -d "${TMPDIR:-/tmp}/gc-pog-fakebd.XXXXXX")"
    write_fake_bd_multi "$fbd"
    write_show_json_for "$fbd" "ga-abc123.1" "in_progress" "agent-x" "tmpl-x" "[]"
    write_list_json_for "$fbd" "agent-x" '[{"id":"ga-abc123.1"}]'
    out="$(run_guard "$repo" "$fbd" "agent-x" "tmpl-x" 2>&1)"; rc=$?
    calls="$(wc -l < "$fbd/fake-bd-state/list-calls.log" 2>/dev/null || echo 0)"
    if [[ $rc -eq 0 ]] && [[ "$calls" -eq 1 ]]; then
        record_pass "substitution/happy-path-zero-extra-list-calls (rc=0, exactly 1 bd list call — resolve-phase cross-check only, candidate search never triggers when the primary status check already passes)"
    else
        record_fail "substitution/happy-path-zero-extra-list-calls" "expected rc=0 and exactly 1 bd list call, got rc=$rc calls=$calls, output: $out"
    fi
    rm -rf "$repo" "$fbd"
}

test_substitution_allows_on_verified_same_branch_candidate() {
    local repo fbd out rc
    local branch="builder/ga-igcny0.1.2.1-resumed-cycle"
    repo="$(new_repo_with_branch "$branch")"
    fbd="$(mktemp -d "${TMPDIR:-/tmp}/gc-pog-fakebd.XXXXXX")"
    write_fake_bd_multi "$fbd"
    # Branch-embedded id: this session's FIRST TDD cycle on this branch,
    # already closed.
    write_show_json_for "$fbd" "ga-igcny0.1.2.1" "closed" "agent-x" "tmpl-x" "[]"
    # A distinct in-progress bead — this session's SECOND cycle reusing the
    # same branch — whose gc.work_branch matches the current branch exactly.
    write_show_json_for "$fbd" "ga-iyfdl1" "in_progress" "agent-x" "tmpl-x" "[]" "$branch"
    write_list_json_for "$fbd" "agent-x" '[{"id":"ga-iyfdl1"}]'
    out="$(run_guard "$repo" "$fbd" "agent-x" "tmpl-x" 2>&1)"; rc=$?
    if [[ $rc -eq 0 ]] && grep -q "ga-igcny0.1.2.1" <<<"$out" && grep -q "ga-iyfdl1" <<<"$out" && ! grep -qi "BLOCKED" <<<"$out"; then
        record_pass "substitution/allows-on-verified-same-branch-candidate (rc=0, NOTE names both the closed original ga-igcny0.1.2.1 and the live candidate ga-iyfdl1)"
    else
        record_fail "substitution/allows-on-verified-same-branch-candidate" "expected rc=0 naming ga-igcny0.1.2.1 and ga-iyfdl1 with no BLOCKED text, got rc=$rc, output: $out"
    fi
    rm -rf "$repo" "$fbd"
}

test_substitution_blocks_when_no_candidate_found() {
    local repo fbd out rc
    local branch="builder/ga-igcny0.1.2.1-resumed-cycle"
    repo="$(new_repo_with_branch "$branch")"
    fbd="$(mktemp -d "${TMPDIR:-/tmp}/gc-pog-fakebd.XXXXXX")"
    write_fake_bd_multi "$fbd"
    write_show_json_for "$fbd" "ga-igcny0.1.2.1" "closed" "agent-x" "tmpl-x" "[]"
    write_list_json_for "$fbd" "agent-x" '[]'
    out="$(run_guard "$repo" "$fbd" "agent-x" "tmpl-x" 2>&1)"; rc=$?
    if [[ $rc -ne 0 ]] && grep -qi "status" <<<"$out" && grep -q "ga-igcny0.1.2.1" <<<"$out" && grep -q -- "--no-verify" <<<"$out"; then
        record_pass "substitution/blocks-when-no-candidate-found (rc=$rc, no in-progress bead anywhere -> original id's status block unchanged)"
    else
        record_fail "substitution/blocks-when-no-candidate-found" "expected non-zero rc citing ga-igcny0.1.2.1 status+--no-verify, got rc=$rc, output: $out"
    fi
    rm -rf "$repo" "$fbd"
}

test_substitution_blocks_when_candidate_wrong_branch() {
    local repo fbd out rc
    local branch="builder/ga-igcny0.1.2.1-resumed-cycle"
    repo="$(new_repo_with_branch "$branch")"
    fbd="$(mktemp -d "${TMPDIR:-/tmp}/gc-pog-fakebd.XXXXXX")"
    write_fake_bd_multi "$fbd"
    write_show_json_for "$fbd" "ga-igcny0.1.2.1" "closed" "agent-x" "tmpl-x" "[]"
    # This session has a genuine in-progress bead, but it's unrelated work on
    # a different branch -- not a candidate for THIS push.
    write_show_json_for "$fbd" "ga-unrelated.2" "in_progress" "agent-x" "tmpl-x" "[]" "builder/ga-unrelated.2-other-work"
    write_list_json_for "$fbd" "agent-x" '[{"id":"ga-unrelated.2"}]'
    out="$(run_guard "$repo" "$fbd" "agent-x" "tmpl-x" 2>&1)"; rc=$?
    if [[ $rc -ne 0 ]] && grep -q "ga-igcny0.1.2.1" <<<"$out" && grep -q -- "--no-verify" <<<"$out"; then
        record_pass "substitution/blocks-when-candidate-wrong-branch (rc=$rc, an unrelated live claim on another branch is never substituted)"
    else
        record_fail "substitution/blocks-when-candidate-wrong-branch" "expected non-zero rc citing ga-igcny0.1.2.1, got rc=$rc, output: $out"
    fi
    rm -rf "$repo" "$fbd"
}

test_substitution_blocks_when_candidate_missing_work_branch() {
    local repo fbd out rc
    local branch="builder/ga-igcny0.1.2.1-resumed-cycle"
    repo="$(new_repo_with_branch "$branch")"
    fbd="$(mktemp -d "${TMPDIR:-/tmp}/gc-pog-fakebd.XXXXXX")"
    write_fake_bd_multi "$fbd"
    write_show_json_for "$fbd" "ga-igcny0.1.2.1" "closed" "agent-x" "tmpl-x" "[]"
    # In-progress and otherwise plausible, but carries no gc.work_branch at
    # all -- absence of the verifying signal must never be treated as a
    # match.
    write_show_json_for "$fbd" "ga-nobranch.1" "in_progress" "agent-x" "tmpl-x" "[]" ""
    write_list_json_for "$fbd" "agent-x" '[{"id":"ga-nobranch.1"}]'
    out="$(run_guard "$repo" "$fbd" "agent-x" "tmpl-x" 2>&1)"; rc=$?
    if [[ $rc -ne 0 ]] && grep -q "ga-igcny0.1.2.1" <<<"$out" && grep -q -- "--no-verify" <<<"$out"; then
        record_pass "substitution/blocks-when-candidate-missing-work-branch (rc=$rc, empty gc.work_branch never counts as a match)"
    else
        record_fail "substitution/blocks-when-candidate-missing-work-branch" "expected non-zero rc citing ga-igcny0.1.2.1, got rc=$rc, output: $out"
    fi
    rm -rf "$repo" "$fbd"
}

test_substitution_blocks_when_candidate_closed_on_recheck() {
    local repo fbd out rc
    local branch="builder/ga-igcny0.1.2.1-resumed-cycle"
    repo="$(new_repo_with_branch "$branch")"
    fbd="$(mktemp -d "${TMPDIR:-/tmp}/gc-pog-fakebd.XXXXXX")"
    write_fake_bd_multi "$fbd"
    write_show_json_for "$fbd" "ga-igcny0.1.2.1" "closed" "agent-x" "tmpl-x" "[]"
    # bd list found it in_progress, but the required FRESH bd show (not a
    # reuse of the list snapshot) reveals it closed in the interim.
    write_show_json_for "$fbd" "ga-candidat.1" "closed" "agent-x" "tmpl-x" "[]" "$branch"
    write_list_json_for "$fbd" "agent-x" '[{"id":"ga-candidat.1"}]'
    out="$(run_guard "$repo" "$fbd" "agent-x" "tmpl-x" 2>&1)"; rc=$?
    if [[ $rc -ne 0 ]] && grep -qi "status" <<<"$out" && grep -q "ga-candidat.1" <<<"$out" && grep -q -- "--no-verify" <<<"$out"; then
        record_pass "substitution/blocks-when-candidate-closed-on-recheck (rc=$rc, blocks citing the CANDIDATE's own id, single substitution hop, no further search)"
    else
        record_fail "substitution/blocks-when-candidate-closed-on-recheck" "expected non-zero rc citing ga-candidat.1 status, got rc=$rc, output: $out"
    fi
    rm -rf "$repo" "$fbd"
}

test_substitution_blocks_when_candidate_reassigned_on_recheck() {
    local repo fbd out rc
    local branch="builder/ga-igcny0.1.2.1-resumed-cycle"
    repo="$(new_repo_with_branch "$branch")"
    fbd="$(mktemp -d "${TMPDIR:-/tmp}/gc-pog-fakebd.XXXXXX")"
    write_fake_bd_multi "$fbd"
    write_show_json_for "$fbd" "ga-igcny0.1.2.1" "closed" "agent-x" "tmpl-x" "[]"
    # in_progress and branch-matched, but its assignee is no longer this
    # session by the time of the fresh recheck.
    write_show_json_for "$fbd" "ga-candidat.2" "in_progress" "someone-else" "tmpl-x" "[]" "$branch"
    write_list_json_for "$fbd" "agent-x" '[{"id":"ga-candidat.2"}]'
    out="$(run_guard "$repo" "$fbd" "agent-x" "tmpl-x" 2>&1)"; rc=$?
    if [[ $rc -ne 0 ]] && grep -qi "assignee" <<<"$out" && grep -q "ga-candidat.2" <<<"$out" && grep -q -- "--no-verify" <<<"$out"; then
        record_pass "substitution/blocks-when-candidate-reassigned-on-recheck (rc=$rc, blocks citing the CANDIDATE's own id)"
    else
        record_fail "substitution/blocks-when-candidate-reassigned-on-recheck" "expected non-zero rc citing ga-candidat.2 assignee, got rc=$rc, output: $out"
    fi
    rm -rf "$repo" "$fbd"
}

test_substitution_blocks_when_candidate_held_on_recheck() {
    local repo fbd out rc
    local branch="builder/ga-igcny0.1.2.1-resumed-cycle"
    repo="$(new_repo_with_branch "$branch")"
    fbd="$(mktemp -d "${TMPDIR:-/tmp}/gc-pog-fakebd.XXXXXX")"
    write_fake_bd_multi "$fbd"
    write_show_json_for "$fbd" "ga-igcny0.1.2.1" "closed" "agent-x" "tmpl-x" "[]"
    write_show_json_for "$fbd" "ga-candidat.3" "in_progress" "agent-x" "tmpl-x" '["hold:mayor"]' "$branch"
    write_list_json_for "$fbd" "agent-x" '[{"id":"ga-candidat.3"}]'
    out="$(run_guard "$repo" "$fbd" "agent-x" "tmpl-x" 2>&1)"; rc=$?
    if [[ $rc -ne 0 ]] && grep -qi "hold:mayor" <<<"$out" && grep -q "ga-candidat.3" <<<"$out" && grep -q -- "--no-verify" <<<"$out"; then
        record_pass "substitution/blocks-when-candidate-held-on-recheck (rc=$rc, blocks citing the CANDIDATE's own id)"
    else
        record_fail "substitution/blocks-when-candidate-held-on-recheck" "expected non-zero rc citing ga-candidat.3 hold:mayor, got rc=$rc, output: $out"
    fi
    rm -rf "$repo" "$fbd"
}

test_substitution_blocks_when_candidate_search_unreachable() {
    local repo fbd out rc
    local branch="builder/ga-igcny0.1.2.1-resumed-cycle"
    repo="$(new_repo_with_branch "$branch")"
    fbd="$(mktemp -d "${TMPDIR:-/tmp}/gc-pog-fakebd.XXXXXX")"
    write_fake_bd_multi "$fbd"
    write_show_json_for "$fbd" "ga-igcny0.1.2.1" "closed" "agent-x" "tmpl-x" "[]"
    # Every `bd list` call fails, for every identity -- the candidate search
    # itself is unreachable, not just empty.
    echo 1 > "$fbd/fake-bd-state/list-exit"
    out="$(run_guard "$repo" "$fbd" "agent-x" "tmpl-x" 2>&1)"; rc=$?
    if [[ $rc -ne 0 ]] && grep -qi "status" <<<"$out" && grep -q "ga-igcny0.1.2.1" <<<"$out" && grep -q -- "--no-verify" <<<"$out"; then
        record_pass "substitution/blocks-when-candidate-search-unreachable (rc=$rc, search failing for every identity falls through to the ORIGINAL id's message, not a new ambiguity message)"
    else
        record_fail "substitution/blocks-when-candidate-search-unreachable" "expected non-zero rc citing ga-igcny0.1.2.1 status, got rc=$rc, output: $out"
    fi
    rm -rf "$repo" "$fbd"
}

test_substitution_finds_candidate_at_lower_precedence_identity() {
    local repo fbd out rc
    local branch="builder/ga-igcny0.1.2.1-resumed-cycle"
    repo="$(new_repo_with_branch "$branch")"
    fbd="$(mktemp -d "${TMPDIR:-/tmp}/gc-pog-fakebd.XXXXXX")"
    write_fake_bd_multi "$fbd"
    write_show_json_for "$fbd" "ga-igcny0.1.2.1" "closed" "agent-x" "tmpl-x" "[]"
    # GC_SESSION_NAME is checked FIRST in the identity precedence order and
    # has an in-progress bead -- a result, just not a match (wrong branch).
    write_show_json_for "$fbd" "ga-unrelated.9" "in_progress" "sess-name-x" "tmpl-x" "[]" "builder/ga-unrelated.9-other"
    write_list_json_for "$fbd" "sess-name-x" '[{"id":"ga-unrelated.9"}]'
    # GC_AGENT is checked LAST and has the actual matching candidate. The
    # search must not stop at the first identity that returns *any* result.
    write_show_json_for "$fbd" "ga-iyfdl1" "in_progress" "agent-x" "tmpl-x" "[]" "$branch"
    write_list_json_for "$fbd" "agent-x" '[{"id":"ga-iyfdl1"}]'
    out="$(run_guard "$repo" "$fbd" "agent-x" "tmpl-x" 5 "" "sess-name-x" 2>&1)"; rc=$?
    if [[ $rc -eq 0 ]] && grep -q "ga-iyfdl1" <<<"$out"; then
        record_pass "substitution/finds-candidate-at-lower-precedence-identity (rc=0, search continues past a non-matching higher-precedence identity to find the real candidate under GC_AGENT)"
    else
        record_fail "substitution/finds-candidate-at-lower-precedence-identity" "expected rc=0 naming ga-iyfdl1, got rc=$rc, output: $out"
    fi
    rm -rf "$repo" "$fbd"
}

# ---------------------------------------------------------------------------
# Hook wiring — real .githooks/pre-push, real bare remote.
#
# install_guard_hook copies the REAL guard lib and the REAL
# .githooks/pre-push (not a re-implementation) into the temp repo, so these
# tests catch a future edit to either file breaking the wiring. A trivial
# Makefile stands in for the real one: pushing a brand-new branch makes the
# hook's `go_changed` gate trip (no remote counterpart to diff against) and
# fall through to `exec make test-fast-parallel`, which these tests don't
# want to actually run — only the ownership guard's wiring is under test
# here.
# ---------------------------------------------------------------------------

install_guard_hook() {
    local repo="$1"
    mkdir -p "$repo/scripts" "$repo/.githooks"
    cp "$LIB" "$repo/scripts/push-ownership-guard.sh"
    cp "$REPO_ROOT/.githooks/pre-push" "$repo/.githooks/pre-push"
    chmod +x "$repo/.githooks/pre-push"
    printf 'test-fast-parallel:\n\t@true\n' > "$repo/Makefile"
    git -C "$repo" config core.hooksPath .githooks
}

# setup_hook_push_scenario <status>: bare remote + a work clone with the
# guard hook installed and one commit queued on a fresh bead-shaped branch,
# plus a fake bd reporting the given status for that bead. Echoes
# "<remote-dir> <work-dir> <fake-bd-dir> <branch>" on one line.
setup_hook_push_scenario() {
    local status="$1"
    local remote work fbd branch="ga-abc123.1-my-feature"
    remote="$(new_bare_remote)"
    work="$(mktemp -d "${TMPDIR:-/tmp}/gc-pog-hookwork.XXXXXX")"
    git clone -q "$remote" "$work" 2>/dev/null
    (
        cd "$work" || exit 1
        git config commit.gpgsign false
        install_guard_hook "$work"
        printf 'base\n' > f.txt; git add -A; git commit -qm base
        git checkout -q -b "$branch"
        printf 'more\n' >> f.txt; git add -A; git commit -qm work
    )
    fbd="$(mktemp -d "${TMPDIR:-/tmp}/gc-pog-fakebd.XXXXXX")"
    write_fake_bd "$fbd"
    write_show_json "$fbd" "ga-abc123.1" "$status" "agent-x" "tmpl-x" "[]"
    printf '%s %s %s %s' "$remote" "$work" "$fbd" "$branch"
}

test_hook_blocks_push_on_stale_claim() {
    local remote work fbd branch out rc
    read -r remote work fbd branch <<<"$(setup_hook_push_scenario closed)"
    out="$(cd "$work" && PATH="$fbd:$PATH" GC_AGENT="agent-x" GC_TEMPLATE="tmpl-x" GIT_TERMINAL_PROMPT=0 git push origin "$branch" 2>&1)"; rc=$?
    if [[ $rc -ne 0 ]] && [[ -z "$(remote_sha "$remote" "refs/heads/$branch")" ]]; then
        record_pass "hook/blocks-push-on-stale-claim (rejected, remote untouched)"
    else
        record_fail "hook/blocks-push-on-stale-claim" "expected rejected push with remote untouched, got rc=$rc remote_sha=$(remote_sha "$remote" "refs/heads/$branch") output=$out"
    fi
    rm -rf "$remote" "$work" "$fbd"
}

test_hook_no_verify_bypasses_guard() {
    local remote work fbd branch out rc
    read -r remote work fbd branch <<<"$(setup_hook_push_scenario closed)"
    out="$(cd "$work" && PATH="$fbd:$PATH" GC_AGENT="agent-x" GC_TEMPLATE="tmpl-x" GIT_TERMINAL_PROMPT=0 git push --no-verify origin "$branch" 2>&1)"; rc=$?
    if [[ $rc -eq 0 ]] && [[ -n "$(remote_sha "$remote" "refs/heads/$branch")" ]]; then
        record_pass "hook/no-verify-bypasses-guard (push succeeded despite stale claim)"
    else
        record_fail "hook/no-verify-bypasses-guard" "expected successful push, got rc=$rc output=$out"
    fi
    rm -rf "$remote" "$work" "$fbd"
}

test_hook_allows_push_on_clean_claim() {
    local remote work fbd branch out rc
    read -r remote work fbd branch <<<"$(setup_hook_push_scenario in_progress)"
    out="$(cd "$work" && PATH="$fbd:$PATH" GC_AGENT="agent-x" GC_TEMPLATE="tmpl-x" GIT_TERMINAL_PROMPT=0 git push origin "$branch" 2>&1)"; rc=$?
    if [[ $rc -eq 0 ]] && [[ -n "$(remote_sha "$remote" "refs/heads/$branch")" ]]; then
        record_pass "hook/allows-push-on-clean-claim"
    else
        record_fail "hook/allows-push-on-clean-claim" "expected successful push, got rc=$rc output=$out"
    fi
    rm -rf "$remote" "$work" "$fbd"
}

# ---------------------------------------------------------------------------
# Static wiring checks — mirrors test-rebase-resolve.sh's style of grepping
# the real source files rather than re-deriving their behavior.
# ---------------------------------------------------------------------------

test_pre_push_hook_sources_and_calls_guard() {
    local hook="$REPO_ROOT/.githooks/pre-push"
    if grep -q "push-ownership-guard.sh" "$hook" 2>/dev/null && grep -q "assert_bead_still_claimed" "$hook" 2>/dev/null; then
        record_pass "wiring/pre-push-hook-sources-and-calls-guard"
    else
        record_fail "wiring/pre-push-hook-sources-and-calls-guard" "$hook does not source+call assert_bead_still_claimed"
    fi
}

test_rebase_lib_calls_guard_before_force_with_lease() {
    local rebase_lib="$REPO_ROOT/scripts/rebase-resolve-lib.sh"
    local guard_line lease_line push_line
    guard_line="$(grep -nF "assert_bead_still_claimed" "$rebase_lib" 2>/dev/null | tail -1 | cut -d: -f1)"
    # SC2016 intentional: literal-text search of rebase-resolve-lib.sh source.
    # shellcheck disable=SC2016
    lease_line="$(grep -nF 'lease_arg="--force-with-lease=$branch:$expected_remote_sha"' "$rebase_lib" 2>/dev/null | tail -1 | cut -d: -f1)"
    # shellcheck disable=SC2016
    push_line="$(grep -nF 'git push "$lease_arg" origin "$branch"' "$rebase_lib" 2>/dev/null | tail -1 | cut -d: -f1)"
    if [[ -n "$guard_line" && -n "$lease_line" && -n "$push_line" \
        && "$guard_line" -lt "$lease_line" && "$lease_line" -lt "$push_line" ]]; then
        record_pass "wiring/rebase-lib-calls-guard-before-force-with-lease"
    else
        record_fail "wiring/rebase-lib-calls-guard-before-force-with-lease" "guard_line=$guard_line lease_line=$lease_line push_line=$push_line (guard must precede lease construction and guarded push)"
    fi
}

# ---------------------------------------------------------------------------
# Runner
# ---------------------------------------------------------------------------

run_all() {
    test_allow_clean_claim
    test_allow_clean_claim_under_zsh
    test_block_on_closed
    test_block_on_reassigned
    test_allow_when_assignee_is_session_id
    test_allow_when_assignee_is_session_name
    test_block_on_routed_to_changed
    test_block_on_hold_mayor
    test_block_on_hold_external
    test_block_on_bd_unreachable
    test_block_on_bd_timeout
    test_retry_recovers_from_transient_failure
    test_retry_exhausted_still_blocks
    test_retry_recovers_then_still_blocks_on_real_ownership_change
    test_retry_unreachable_message_mentions_retry_before_no_verify
    test_retry_parse_failure_message_mentions_retry_before_no_verify
    test_bead_id_branch_wins_and_warns_on_disagreement
    test_bead_id_branch_resolves_multi_level_subbead_id
    test_bead_id_fallback_used_when_branch_no_match
    test_bead_id_deploy_gate_branch_prefers_live_assignee
    test_bead_id_deploy_gate_branch_allows_when_no_live_assignee
    test_bead_id_deploy_gate_branch_blocks_when_assignee_lookup_fails
    test_retry_recovers_bead_id_fallback_from_transient_failure
    test_allow_when_no_bead_id_resolvable
    test_fallback_cannot_detect_staleness_after_status_leaves_in_progress
    test_substitution_happy_path_zero_extra_list_calls
    test_substitution_allows_on_verified_same_branch_candidate
    test_substitution_blocks_when_no_candidate_found
    test_substitution_blocks_when_candidate_wrong_branch
    test_substitution_blocks_when_candidate_missing_work_branch
    test_substitution_blocks_when_candidate_closed_on_recheck
    test_substitution_blocks_when_candidate_reassigned_on_recheck
    test_substitution_blocks_when_candidate_held_on_recheck
    test_substitution_blocks_when_candidate_search_unreachable
    test_substitution_finds_candidate_at_lower_precedence_identity
    test_hook_blocks_push_on_stale_claim
    test_hook_no_verify_bypasses_guard
    test_hook_allows_push_on_clean_claim
    test_pre_push_hook_sources_and_calls_guard
    test_rebase_lib_calls_guard_before_force_with_lease

    echo
    echo "pass=$pass fail=$fail"
    [[ $fail -eq 0 ]]
}

run_all
