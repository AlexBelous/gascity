#!/usr/bin/env bash
# gate-lock.sh — serializes the pre-push gate's heavy test suite across
# concurrent callers (ga-scxvr8: N agents pushing at once turn a green
# suite red and manufacture bypasses, because nothing prevents the heavy
# `make test-fast-parallel` fan-out from running concurrently host-wide).
#
# THE EXPORTED FUNCTION:
#   gc_gate_serialize_run <lockfile> <timeout_seconds> <label> -- <command...>
#
#   Runs <command...> with <lockfile> held (flock), serializing against any
#   other caller using the same lockfile -- regardless of process tree, so
#   a bare `git push` is serialized host-wide with no caller-side wrapping.
#   - Uncontended: acquires immediately, runs <command...>, returns its
#     exit code unchanged.
#   - Contended: prints "waiting for N other gate run(s) (<label>)..." to
#     stderr (N = other holders/waiters on this lockfile), then blocks up
#     to <timeout_seconds> -- queue, don't fail merely due to contention.
#   - Timeout: if the lock isn't acquired within <timeout_seconds>, prints
#     a message containing "timed out" to stderr and returns 124 without
#     ever running <command...>.
#
# Usage (from .githooks/pre-push):
#   source "$repo_root/scripts/lib/gate-lock.sh"
#   gc_gate_serialize_run "$lockfile" "$timeout" "pre-push" -- make test-fast-parallel
#
# Self-test: scripts/test-gate-lock.sh (run by go test ./scripts).

gc_gate_serialize_run() {
  local lockfile="$1" timeout="$2" label="$3"
  shift 3
  if [[ "${1:-}" != "--" ]]; then
    echo "gate-lock: gc_gate_serialize_run requires -- before the command" >&2
    return 2
  fi
  shift

  local waitdir="${lockfile}.d"
  mkdir -p "$waitdir"
  local marker
  marker="$(mktemp "$waitdir/marker.XXXXXX")"

  exec 9>"$lockfile"

  if ! flock -n 9; then
    local other
    other="$(find "$waitdir" -maxdepth 1 -name 'marker.*' ! -name "$(basename "$marker")" 2>/dev/null | wc -l | tr -d ' ')"
    echo "gate-lock: waiting for ${other} other gate run(s) (${label})..." >&2
    if ! flock -w "$timeout" 9; then
      echo "gate-lock: timed out after ${timeout}s waiting for ${label} (lock: ${lockfile})" >&2
      rm -f "$marker"
      exec 9>&-
      return 124
    fi
  fi

  "$@"
  local rc=$?
  rm -f "$marker"
  exec 9>&-
  return "$rc"
}
