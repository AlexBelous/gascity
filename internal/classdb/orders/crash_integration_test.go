//go:build integration

package ordersdb

// Crash-durability contract for the orders class (design "Load-bearing
// invariants": at-most-one-extra-fire on crash). A CreateRun that returned to
// its caller is durable — the dispatch goroutine launches only after the row
// commits — so a process SIGKILLed at any point leaves every acked run as an
// orphan-open row that the startup sweep (OrphanedOpenRuns + CloseRuns) can
// observe and close. This test re-execs the test binary as a real child
// process against one shared database file, mirroring the classdb/core G0
// gate.

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/classdb/core"
	"github.com/gastownhall/gascity/internal/orders"
)

const (
	crashChildDBEnv = "ORDERSDB_CRASH_CHILD_DB"

	crashScoped = "crash/agent"
)

// TestHelperOrdersdbChildCreator is not a real test: it is the child-process
// body for the crash test below, entered only when re-exec'd with
// ORDERSDB_CRASH_CHILD_DB set. It creates open runs forever, printing an ACK
// for each CreateRun that returned (== committed), until the parent SIGKILLs
// it mid-stream.
func TestHelperOrdersdbChildCreator(t *testing.T) {
	path := os.Getenv(crashChildDBEnv)
	if path == "" {
		t.Skip("not a child process")
	}
	st, err := Open(path, core.WithSingleConn())
	if err != nil {
		t.Fatalf("child open: %v", err)
	}
	defer st.Close() //nolint:errcheck

	out := bufio.NewWriter(os.Stdout)
	for i := 0; ; i++ {
		run, err := st.CreateRun(crashScoped, orders.RunOpts{})
		if err != nil {
			t.Fatalf("child CreateRun %d: %v", i, err)
		}
		fmt.Fprintf(out, "ACK %s\n", run.ID) //nolint:errcheck
		out.Flush()                          //nolint:errcheck
	}
}

// TestCrashDurabilityOrphanOpenRunsSurviveKill SIGKILLs a child mid-CreateRun
// stream and proves (a) every acked run survives reopen as an OPEN row (the
// single-flight marker is durable before dispatch), and (b) the startup-sweep
// contract holds: OrphanedOpenRuns surfaces the leftovers and CloseRuns
// clears them, leaving no open work.
func TestCrashDurabilityOrphanOpenRunsSurviveKill(t *testing.T) {
	if testing.Short() {
		t.Skip("multi-process test skipped in -short")
	}
	dbPath := filepath.Join(t.TempDir(), "orders.db")
	// Create the schema first so the child doesn't race migration.
	seed, err := Open(dbPath)
	if err != nil {
		t.Fatalf("seed open: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("seed close: %v", err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestHelperOrdersdbChildCreator", "-test.v=false") //nolint:gosec // re-exec of the test binary itself
	cmd.Env = append(os.Environ(), crashChildDBEnv+"="+dbPath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}

	const ackTarget = 10
	acked := make([]string, 0, ackTarget)
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		if id, found := strings.CutPrefix(scanner.Text(), "ACK "); found {
			acked = append(acked, id)
			if len(acked) >= ackTarget {
				break
			}
		}
	}
	if len(acked) < ackTarget {
		t.Fatalf("child died before %d acks (got %d)", ackTarget, len(acked))
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill child: %v", err)
	}
	_ = cmd.Wait() // reap; the kill error is expected

	st, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen after kill: %v", err)
	}
	defer st.Close() //nolint:errcheck

	for _, id := range acked {
		run, err := st.Get(id)
		if err != nil {
			t.Fatalf("acked run %s lost after SIGKILL: %v — committed creates must be durable", id, err)
		}
		if !run.Open {
			t.Fatalf("acked run %s not open after SIGKILL — the single-flight marker must survive as orphan-open", id)
		}
	}

	// Startup sweep contract: the orphan read surfaces every leftover open
	// row (acked plus any committed-but-unacked trailing create), and the
	// batch close clears them all.
	front := orders.NewStoreWithTracking(st, beads.GraphStore{})
	orphans, err := front.OrphanedOpenRuns()
	if err != nil {
		t.Fatalf("OrphanedOpenRuns: %v", err)
	}
	orphanIDs := make(map[string]bool, len(orphans))
	ids := make([]string, 0, len(orphans))
	for _, run := range orphans {
		orphanIDs[run.ID] = true
		ids = append(ids, run.ID)
	}
	for _, id := range acked {
		if !orphanIDs[id] {
			t.Fatalf("acked run %s missing from OrphanedOpenRuns — the startup sweep would leak it", id)
		}
	}
	closed, err := front.CloseRuns(nil, ids, "orphaned by crash-durability test sweep")
	if err != nil {
		t.Fatalf("CloseRuns: %v", err)
	}
	if closed != len(ids) {
		t.Fatalf("CloseRuns closed %d of %d orphans", closed, len(ids))
	}
	if open, err := front.OpenRuns(); err != nil || len(open) != 0 {
		t.Fatalf("OpenRuns after sweep = (%d runs, %v), want none", len(open), err)
	}
}
