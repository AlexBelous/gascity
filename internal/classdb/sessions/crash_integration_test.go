//go:build integration

package sessionsdb

// Crash-durability contract for the sessions class (design "Migration &
// cutover" row 4: restart-projection equivalence). Open session rows are
// the projection the reconciler re-derives ALL runtime state from after a
// controller restart, so an acked Create or metadata patch must survive a
// SIGKILL at any point: a lost row is a session the reconciler can never
// re-adopt (the root-loss shape). The child process creates sessions and
// stamps lifecycle patches through the public front door, acking each
// committed write; the parent SIGKILLs it mid-stream, reopens the file,
// and re-derives the reconciler feed.

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
	"github.com/gastownhall/gascity/internal/session"
)

const crashChildDBEnv = "SESSIONSDB_CRASH_CHILD_DB"

// TestHelperSessionsdbChildWriter is not a real test: it is the child body
// for the crash test below, entered only when re-exec'd with
// SESSIONSDB_CRASH_CHILD_DB set. It creates session beads and stamps a
// lifecycle patch on each, printing an ACK per committed write, until the
// parent SIGKILLs it.
func TestHelperSessionsdbChildWriter(t *testing.T) {
	path := os.Getenv(crashChildDBEnv)
	if path == "" {
		t.Skip("not a child process")
	}
	st, err := Open(path, core.WithSingleConn())
	if err != nil {
		t.Fatalf("child open: %v", err)
	}
	defer st.CloseStore() //nolint:errcheck
	front := session.NewStore(beads.SessionStore{Store: st})

	out := bufio.NewWriter(os.Stdout)
	for i := 0; ; i++ {
		agent := fmt.Sprintf("crash-%d", i)
		info, err := front.CreateSessionInfo(session.CreateSpec{
			Title:     agent,
			AgentName: agent,
			Metadata: map[string]string{
				"state":        "creating",
				"session_name": "gc-" + agent,
			},
		})
		if err != nil {
			t.Fatalf("child create %d: %v", i, err)
		}
		if err := front.SetState(info.ID, session.State("awake"), "crash-test"); err != nil {
			t.Fatalf("child patch %d: %v", i, err)
		}
		fmt.Fprintf(out, "ACK %s\n", info.ID) //nolint:errcheck
		out.Flush()                           //nolint:errcheck
	}
}

// TestCrashDurabilityRestartProjectionSurvivesKill SIGKILLs a child
// mid-write stream and proves every acked session row survives reopen with
// its acked lifecycle state — the reconciler restart-projection gate for
// the sessions class.
func TestCrashDurabilityRestartProjectionSurvivesKill(t *testing.T) {
	if testing.Short() {
		t.Skip("multi-process test skipped in -short")
	}
	dbPath := filepath.Join(t.TempDir(), "sessions.db")
	// Create the schema first so the child doesn't race migration.
	seed, err := Open(dbPath)
	if err != nil {
		t.Fatalf("seed open: %v", err)
	}
	if err := seed.CloseStore(); err != nil {
		t.Fatalf("seed close: %v", err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestHelperSessionsdbChildWriter", "-test.v=false") //nolint:gosec // re-exec of the test binary itself
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
	defer st.CloseStore() //nolint:errcheck
	if ok, err := st.db.IntegrityCheck(t.Context()); err != nil || !ok {
		t.Fatalf("integrity check after SIGKILL: ok=%v err=%v", ok, err)
	}

	// The reconciler feed re-derives every acked row with its acked state.
	front := session.NewStore(beads.SessionStore{Store: st})
	rows, fingerprint, err := front.ListAllForReconcileWithFingerprint(session.ListAllOptions{Sort: beads.SortCreatedDesc})
	if err != nil {
		t.Fatalf("reconcile feed after crash: %v", err)
	}
	if fingerprint == "" {
		t.Fatal("empty fingerprint over surviving rows")
	}
	byID := make(map[string]session.Info, len(rows))
	for _, r := range rows {
		byID[r.Info.ID] = r.Info
	}
	for _, id := range acked {
		info, ok := byID[id]
		if !ok {
			t.Fatalf("acked session %s lost after SIGKILL — the restart projection is broken", id)
		}
		if info.MetadataState != "awake" || info.StateReason != "crash-test" {
			t.Fatalf("acked patch on %s lost after SIGKILL: state=%q reason=%q", id, info.MetadataState, info.StateReason)
		}
	}
	// And a wake fence write still works on the survivors (the file is
	// fully writable after recovery).
	if err := front.SetState(acked[0], session.State("asleep"), "post-crash"); err != nil {
		t.Fatalf("post-crash write: %v", err)
	}
}
