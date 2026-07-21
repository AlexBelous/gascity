//go:build integration

package nudgesdb

// Crash-durability contract for the nudges class (design "Migration &
// cutover" row 2: acked-write survival). An Enqueue that returned to its
// caller is durable — the wake-socket ping and delivery races all assume the
// item is committed — so a process SIGKILLed at any point leaves every acked
// enqueue as a pending row a later claim pass can deliver. This test
// re-execs the test binary as a real child process against one shared
// database file, mirroring the classdb/core G0 gate.

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/classdb/core"
	"github.com/gastownhall/gascity/internal/nudgequeue"
)

const crashChildDBEnv = "NUDGESDB_CRASH_CHILD_DB"

// TestHelperNudgesdbChildEnqueuer is not a real test: it is the child-process
// body for the crash test below, entered only when re-exec'd with
// NUDGESDB_CRASH_CHILD_DB set. It enqueues forever, printing an ACK for each
// Enqueue that returned (== committed), until the parent SIGKILLs it.
func TestHelperNudgesdbChildEnqueuer(t *testing.T) {
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
		now := time.Now()
		item := nudgequeue.Item{
			ID:           fmt.Sprintf("nudge-crash-%d", i),
			Agent:        "crash/agent",
			Source:       "session",
			Message:      "crash test",
			CreatedAt:    now.UTC(),
			DeliverAfter: now.UTC(),
			ExpiresAt:    now.Add(nudgequeue.DefaultTTL).UTC(),
		}
		if err := st.Enqueue(item, beads.NudgesStore{}); err != nil {
			t.Fatalf("child Enqueue %d: %v", i, err)
		}
		fmt.Fprintf(out, "ACK %s\n", item.ID) //nolint:errcheck
		out.Flush()                           //nolint:errcheck
	}
}

// TestCrashDurabilityAckedEnqueuesSurviveKill SIGKILLs a child mid-enqueue
// stream and proves every acked enqueue survives reopen as a pending row a
// claim pass can deliver — the acked-write-survival gate for the nudges
// class.
func TestCrashDurabilityAckedEnqueuesSurviveKill(t *testing.T) {
	if testing.Short() {
		t.Skip("multi-process test skipped in -short")
	}
	dbPath := filepath.Join(t.TempDir(), "nudges.db")
	// Create the schema first so the child doesn't race migration.
	seed, err := Open(dbPath)
	if err != nil {
		t.Fatalf("seed open: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("seed close: %v", err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestHelperNudgesdbChildEnqueuer", "-test.v=false") //nolint:gosec // re-exec of the test binary itself
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
		rec, ok, err := st.FindRecord(id)
		if err != nil || !ok {
			t.Fatalf("acked enqueue %s lost after SIGKILL (ok=%v, %v) — committed enqueues must be durable", id, ok, err)
		}
		if rec.QueueState != "pending" {
			t.Fatalf("acked enqueue %s state = %q, want pending", id, rec.QueueState)
		}
	}
	// A claim pass can deliver every surviving item.
	claimed, err := st.ClaimDue(nudgequeue.ClaimTarget{QueueKeys: []string{"crash/agent"}}, time.Now())
	if err != nil {
		t.Fatalf("ClaimDue after crash: %v", err)
	}
	if len(claimed) < ackTarget {
		t.Fatalf("claimed %d of %d acked items after crash", len(claimed), ackTarget)
	}
}
