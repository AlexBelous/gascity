package beads

import "github.com/gastownhall/gascity/internal/testutil"

// beadsHangBudget bounds the pure hang-detector waits in this package that watch
// a background goroutine reach a checkpoint or finish (context-cancellation
// races in ready_context_internal_test.go and sqlite_store_ready_test.go). No
// assertion depends on how long any of these waits take — the real assertions
// run after the wait returns — so this is a hang detector, not a latency
// assertion: raising it does not slow a passing run, because a passing run
// never waits the budget out, and lowering it does not make any test stricter.
// It only changes how long a genuinely wedged goroutine takes to report.
//
// testutil.GoroutineRaceTimeout (10s) is a floor, not a target (TESTING.md
// "Floors, ceilings, and inputs"): it is the minimum safe deadline for a timer
// racing a goroutine under CI CPU saturation, not a bound tuned for any one
// package's actual goroutines. Mirrors the precedent in
// cmd/gc/hangbudget_test.go (hangBudget = 6 * testutil.GoroutineRaceTimeout).
//
// DO NOT tune beadsHangBudget to make a failing test pass. A test that needs a
// latency bound or a negative assertion ("nothing arrived within X") must keep
// its own explicit short deadline instead — this budget is the wrong tool for
// either.
const beadsHangBudget = 6 * testutil.GoroutineRaceTimeout

// beadsSequenceFloorHangBudget bounds the pure hang-detector wait in
// sqlite_store_sequence_floor_process_linux_test.go's (*sqliteSequenceFloorChild).line:
// the parent re-execs the test binary as a child that opens a SQLiteStore and
// arms a barrier before printing its "ready" protocol line over stdout: no
// assertion depends on how long that takes, only on the line's content once it
// arrives, so this is a hang detector, not a latency assertion. Structurally
// the same shape as storebinding/sqlite's sqliteFenceHangBudget (ga-sptey3): a
// re-exec'd child clearing Go runtime + package init, flag parse and test
// enumeration, plus a SQLite open, before its first protocol line.
// testutil.ExecRaceTimeout (10s) is a floor, not a target, for the same reason
// beadsHangBudget treats testutil.GoroutineRaceTimeout as a floor above.
// Mirrors the same 6x multiplier precedent (60s, well under the gate package
// timeout).
const beadsSequenceFloorHangBudget = 6 * testutil.ExecRaceTimeout
