#!/usr/bin/env bash
#
# test-gate-lock.sh — tests for the pre-push gate concurrency control
# (ga-scxvr8: "pre-push gate has no concurrency control -- N agents pushing
# at once turn a green suite red and manufacture bypasses").
#
# Interface under test (scripts/lib/gate-lock.sh, not yet implemented):
#
#   gc_gate_serialize_run <lockfile> <timeout_seconds> <label> -- <command...>
#
#     Runs <command...> with <lockfile> held (flock), serializing against any
#     other caller using the same lockfile -- regardless of process tree, so
#     a bare `git push` is serialized host-wide with no caller-side wrapping.
#     - Uncontended: acquires immediately, runs <command...>, returns its
#       exit code unchanged.
#     - Contended: prints "waiting for N other gate run(s) (<label>)..." to
#       stderr (N = other holders/waiters on this lockfile), then blocks up
#       to <timeout_seconds> -- queue, don't fail merely due to contention.
#     - Timeout: if the lock isn't acquired within <timeout_seconds>, prints
#       a message containing "timed out" to stderr and returns nonzero
#       without ever running <command...>.
#
#   .githooks/pre-push sources scripts/lib/gate-lock.sh and wraps its heavy
#   `make test-fast-parallel` invocation in gc_gate_serialize_run, using
#   GC_GATE_LOCK_FILE (default /var/tmp/gc-gate-serialize.lock) and
#   GC_GATE_LOCK_TIMEOUT (default 3600) so a plain `git push` is
#   automatically serialized fleet-wide with no caller-side flock wrapping.
#
# Every invocation below passes an explicit, private lockfile under a temp
# dir -- NEVER the real shared /var/tmp/gc-gate-serialize.lock, which other
# fleet agents may be actively holding right now. Hermetic: temp git repos
# and a fake bd only, no network/gh/model calls.

set -uo pipefail

TEST_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LIB="$TEST_DIR/lib/gate-lock.sh"
REPO_ROOT="$(cd "$TEST_DIR/.." && pwd)"

# A fresh hermetic $HOME (as used by gate_lock_test.go's t.TempDir()) has no
# git identity configured, and this sandbox's hostname does not always
# resolve -- git's auto-detect-from-system fallback then hard-fails with
# "unable to auto-detect email address" instead of just warning. Pin a
# fixed identity so every commit below succeeds regardless of host state.
export GIT_AUTHOR_NAME="gate-lock-test"
export GIT_AUTHOR_EMAIL="gate-lock-test@example.invalid"
export GIT_COMMITTER_NAME="gate-lock-test"
export GIT_COMMITTER_EMAIL="gate-lock-test@example.invalid"

pass=0; fail=0
record_pass() { echo "  ok   $1"; pass=$((pass + 1)); }
record_fail() { echo "  FAIL $1 -- $2"; fail=$((fail + 1)); }

# shellcheck source=lib/gate-lock.sh disable=SC1091
source "$LIB" || true

# ---------------------------------------------------------------------------
# Helpers (mirrors scripts/test-push-ownership-guard.sh's harness).
# ---------------------------------------------------------------------------

new_bare_remote() {
    local d
    d="$(mktemp -d "${TMPDIR:-/tmp}/gc-gl-remote.XXXXXX")"
    git init -q --bare -b main "$d"
    printf '%s' "$d"
}

write_fake_bd() {
    local dir="$1"
    mkdir -p "$dir/fake-bd-state"
    cat > "$dir/bd" <<'FAKE'
#!/usr/bin/env bash
set -euo pipefail
state="$(dirname "$0")/fake-bd-state"
case "$1" in
  show)
    if [ -f "$state/show-json" ]; then
      cat "$state/show-json"
      exit 0
    fi
    exit 1
    ;;
  list)
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

write_show_json() {
    local fbd="$1" id="$2" status="$3" assignee="$4" routed_to="$5"
    mkdir -p "$fbd/fake-bd-state"
    printf '[{"id":"%s","status":"%s","assignee":"%s","metadata":{"gc.routed_to":"%s"},"labels":[]}]' \
        "$id" "$status" "$assignee" "$routed_to" > "$fbd/fake-bd-state/show-json"
}

# ---------------------------------------------------------------------------
# Library-level behavior of gc_gate_serialize_run.
# ---------------------------------------------------------------------------

test_runs_command_when_uncontended() {
    local d lockfile marker
    d="$(mktemp -d "${TMPDIR:-/tmp}/gc-gl-test.XXXXXX")"
    lockfile="$d/gate.lock"
    marker="$d/ran"
    if gc_gate_serialize_run "$lockfile" 5 "uncontended" -- bash -c "touch '$marker'"; then
        if [[ -f "$marker" ]]; then
            record_pass "lib/runs-command-when-uncontended"
        else
            record_fail "lib/runs-command-when-uncontended" "marker not created"
        fi
    else
        record_fail "lib/runs-command-when-uncontended" "gc_gate_serialize_run returned nonzero"
    fi
    rm -rf "$d"
}

test_propagates_wrapped_exit_code() {
    local d lockfile rc
    d="$(mktemp -d "${TMPDIR:-/tmp}/gc-gl-test.XXXXXX")"
    lockfile="$d/gate.lock"
    gc_gate_serialize_run "$lockfile" 5 "failing" -- bash -c "exit 7"
    rc=$?
    if [[ $rc -eq 7 ]]; then
        record_pass "lib/propagates-wrapped-exit-code"
    else
        record_fail "lib/propagates-wrapped-exit-code" "expected rc=7, got rc=$rc"
    fi
    rm -rf "$d"
}

test_serializes_concurrent_runs() {
    local d lockfile log seq
    d="$(mktemp -d "${TMPDIR:-/tmp}/gc-gl-test.XXXXXX")"
    lockfile="$d/gate.lock"
    log="$d/log"
    : > "$log"
    (
        gc_gate_serialize_run "$lockfile" 20 "runner-a" -- bash -c \
            "echo start-a >> '$log'; sleep 0.6; echo end-a >> '$log'"
    ) &
    local pid_a=$!
    sleep 0.15
    (
        gc_gate_serialize_run "$lockfile" 20 "runner-b" -- bash -c \
            "echo start-b >> '$log'; sleep 0.6; echo end-b >> '$log'"
    ) &
    local pid_b=$!
    wait "$pid_a" "$pid_b"

    seq="$(tr '\n' ' ' < "$log" | sed 's/ *$//')"
    if [[ "$seq" == "start-a end-a start-b end-b" ]] || [[ "$seq" == "start-b end-b start-a end-a" ]]; then
        record_pass "lib/serializes-concurrent-runs (no interleaving)"
    else
        record_fail "lib/serializes-concurrent-runs" "events interleaved or missing (overlap): '$seq'"
    fi
    rm -rf "$d"
}

test_emits_waiting_message_for_blocked_run() {
    local d lockfile out_b
    d="$(mktemp -d "${TMPDIR:-/tmp}/gc-gl-test.XXXXXX")"
    lockfile="$d/gate.lock"
    (
        gc_gate_serialize_run "$lockfile" 20 "holder" -- sleep 0.8
    ) &
    local pid_a=$!
    sleep 0.2
    out_b="$(gc_gate_serialize_run "$lockfile" 20 "waiter" -- true 2>&1 1>/dev/null)"
    wait "$pid_a"
    if echo "$out_b" | grep -Eq "waiting for [0-9]+ other gate run"; then
        record_pass "lib/emits-waiting-message-for-blocked-run"
    else
        record_fail "lib/emits-waiting-message-for-blocked-run" "stderr did not contain expected message: '$out_b'"
    fi
    rm -rf "$d"
}

test_blocked_run_eventually_succeeds() {
    local d lockfile rc
    d="$(mktemp -d "${TMPDIR:-/tmp}/gc-gl-test.XXXXXX")"
    lockfile="$d/gate.lock"
    (
        gc_gate_serialize_run "$lockfile" 20 "holder" -- sleep 0.6
    ) &
    local pid_a=$!
    sleep 0.2
    gc_gate_serialize_run "$lockfile" 20 "waiter" -- true
    rc=$?
    wait "$pid_a"
    if [[ $rc -eq 0 ]]; then
        record_pass "lib/blocked-run-eventually-succeeds (queue, don't fail)"
    else
        record_fail "lib/blocked-run-eventually-succeeds" "expected rc=0 after waiting, got rc=$rc"
    fi
    rm -rf "$d"
}

test_times_out_past_generous_deadline() {
    local d lockfile rc out_b
    d="$(mktemp -d "${TMPDIR:-/tmp}/gc-gl-test.XXXXXX")"
    lockfile="$d/gate.lock"
    (
        gc_gate_serialize_run "$lockfile" 20 "holder" -- sleep 2
    ) &
    local pid_a=$!
    sleep 0.2
    out_b="$(gc_gate_serialize_run "$lockfile" 0.3 "impatient" -- true 2>&1 1>/dev/null)"
    rc=$?
    wait "$pid_a"
    if [[ $rc -ne 0 ]] && echo "$out_b" | grep -qi "timed out"; then
        record_pass "lib/times-out-past-generous-deadline"
    else
        record_fail "lib/times-out-past-generous-deadline" "expected nonzero rc with timeout message, got rc=$rc output='$out_b'"
    fi
    rm -rf "$d"
}

# ---------------------------------------------------------------------------
# Static wiring check -- mirrors test-push-ownership-guard.sh's style of
# grepping the real source file rather than re-deriving its behavior.
# ---------------------------------------------------------------------------

test_pre_push_wires_gate_lock() {
    local hook="$REPO_ROOT/.githooks/pre-push"
    if grep -q "gate-lock.sh" "$hook" 2>/dev/null && grep -q "gc_gate_serialize_run" "$hook" 2>/dev/null; then
        record_pass "wiring/pre-push-wires-gate-lock"
    else
        record_fail "wiring/pre-push-wires-gate-lock" "$hook does not source scripts/lib/gate-lock.sh and call gc_gate_serialize_run"
    fi
}

# ---------------------------------------------------------------------------
# Hook wiring -- real .githooks/pre-push, real bare remote. A bare `git push`
# must serialize the heavy suite without the caller wrapping anything in
# flock itself (ga-scxvr8's core acceptance criterion).
# ---------------------------------------------------------------------------

install_gate_lock_hook() {
    local repo="$1"
    mkdir -p "$repo/scripts/lib" "$repo/.githooks"
    cp "$REPO_ROOT/scripts/push-ownership-guard.sh" "$repo/scripts/push-ownership-guard.sh"
    cp "$LIB" "$repo/scripts/lib/gate-lock.sh"
    cp "$REPO_ROOT/.githooks/pre-push" "$repo/.githooks/pre-push"
    chmod +x "$repo/.githooks/pre-push"
    cat > "$repo/Makefile" <<'MAKEFILE'
test-fast-parallel:
	@echo "start-$$GATE_TEST_MARKER" >> "$$GATE_TEST_HEAVY_LOG"
	@sleep 0.6
	@echo "end-$$GATE_TEST_MARKER" >> "$$GATE_TEST_HEAVY_LOG"
MAKEFILE
    git -C "$repo" config core.hooksPath .githooks
}

setup_gate_lock_push_scenario() {
    local branch="$1" bead_id="$2"
    local remote work fbd
    remote="$(new_bare_remote)"
    work="$(mktemp -d "${TMPDIR:-/tmp}/gc-gl-hookwork.XXXXXX")"
    git clone -q "$remote" "$work" 2>/dev/null
    (
        cd "$work" || exit 1
        git config commit.gpgsign false
        install_gate_lock_hook "$work"
        printf 'base\n' > f.txt; git add -A; git commit -qm base
        git checkout -q -b "$branch"
        printf 'more\n' >> f.txt; git add -A; git commit -qm work
    )
    fbd="$(mktemp -d "${TMPDIR:-/tmp}/gc-gl-fakebd.XXXXXX")"
    write_fake_bd "$fbd"
    write_show_json "$fbd" "$bead_id" "in_progress" "agent-x" "tmpl-x"
    printf '%s %s %s' "$remote" "$work" "$fbd"
}

test_bare_push_serializes_heavy_suite_host_wide() {
    local d heavy_log lockfile
    local remote_a work_a fbd_a remote_b work_b fbd_b
    d="$(mktemp -d "${TMPDIR:-/tmp}/gc-gl-hooktest.XXXXXX")"
    heavy_log="$d/heavy.log"; : > "$heavy_log"
    lockfile="$d/shared-gate.lock"

    read -r remote_a work_a fbd_a <<<"$(setup_gate_lock_push_scenario "ga-abc111.1-first" "ga-abc111.1")"
    read -r remote_b work_b fbd_b <<<"$(setup_gate_lock_push_scenario "ga-abc222.1-second" "ga-abc222.1")"

    (
        cd "$work_a" && PATH="$fbd_a:$PATH" GC_AGENT="agent-x" GC_TEMPLATE="tmpl-x" \
            GC_GATE_LOCK_FILE="$lockfile" GATE_TEST_MARKER="a" GATE_TEST_HEAVY_LOG="$heavy_log" \
            GIT_TERMINAL_PROMPT=0 git push origin ga-abc111.1-first >/dev/null 2>&1
    ) &
    local pid_a=$!
    sleep 0.15
    (
        cd "$work_b" && PATH="$fbd_b:$PATH" GC_AGENT="agent-x" GC_TEMPLATE="tmpl-x" \
            GC_GATE_LOCK_FILE="$lockfile" GATE_TEST_MARKER="b" GATE_TEST_HEAVY_LOG="$heavy_log" \
            GIT_TERMINAL_PROMPT=0 git push origin ga-abc222.1-second >/dev/null 2>&1
    ) &
    local pid_b=$!
    wait "$pid_a" "$pid_b"

    local seq
    seq="$(tr '\n' ' ' < "$heavy_log" | sed 's/ *$//')"
    if [[ "$seq" == "start-a end-a start-b end-b" ]] || [[ "$seq" == "start-b end-b start-a end-a" ]]; then
        record_pass "hook/bare-push-serializes-heavy-suite-host-wide"
    else
        record_fail "hook/bare-push-serializes-heavy-suite-host-wide" "heavy suite did not serialize: '$seq'"
    fi
    rm -rf "$remote_a" "$work_a" "$fbd_a" "$remote_b" "$work_b" "$fbd_b" "$d"
}

# ---------------------------------------------------------------------------
# Runner
# ---------------------------------------------------------------------------

run_all() {
    test_runs_command_when_uncontended
    test_propagates_wrapped_exit_code
    test_serializes_concurrent_runs
    test_emits_waiting_message_for_blocked_run
    test_blocked_run_eventually_succeeds
    test_times_out_past_generous_deadline
    test_pre_push_wires_gate_lock
    test_bare_push_serializes_heavy_suite_host_wide

    echo
    echo "pass=$pass fail=$fail"
    [[ $fail -eq 0 ]]
}

run_all
