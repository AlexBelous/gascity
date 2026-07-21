//go:build integration

package messagingdb

// Crash-durability contract for the messaging class (design "Migration &
// cutover" row 3): a Send that returned to its caller is durable — the
// "you have mail" notification nudge and the recipient's poll all assume
// the message is committed — so a process SIGKILLed at any point leaves
// every acked send as an open unread row the inbox can list. This test
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

	"github.com/gastownhall/gascity/internal/classdb/core"
	"github.com/gastownhall/gascity/internal/mail/beadmail"
)

const crashChildDBEnv = "MESSAGINGDB_CRASH_CHILD_DB"

// TestHelperMessagingdbChildSender is not a real test: it is the
// child-process body for the crash test below, entered only when re-exec'd
// with MESSAGINGDB_CRASH_CHILD_DB set. It sends forever, printing an ACK
// for each Create that returned (== committed), until the parent SIGKILLs
// it.
func TestHelperMessagingdbChildSender(t *testing.T) {
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
		rec, err := st.Create(beadmail.NewMessage{
			Subject:  fmt.Sprintf("crash %d", i),
			Body:     "crash test",
			From:     "crash/sender",
			To:       "crash/recipient",
			ThreadID: fmt.Sprintf("thread-crash-%d", i),
		})
		if err != nil {
			t.Fatalf("child Create %d: %v", i, err)
		}
		fmt.Fprintf(out, "ACK %s\n", rec.ID) //nolint:errcheck
		out.Flush()                          //nolint:errcheck
	}
}

// TestCrashDurabilityAckedSendsSurviveKill SIGKILLs a child mid-send stream
// and proves every acked send survives reopen as an open unread row the
// inbox lists — the acked-write-survival gate for the messaging class.
func TestCrashDurabilityAckedSendsSurviveKill(t *testing.T) {
	if testing.Short() {
		t.Skip("multi-process test skipped in -short")
	}
	dbPath := filepath.Join(t.TempDir(), "messaging.db")
	// Create the schema first so the child doesn't race migration.
	seed, err := Open(dbPath)
	if err != nil {
		t.Fatalf("seed open: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("seed close: %v", err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestHelperMessagingdbChildSender", "-test.v=false") //nolint:gosec // re-exec of the test binary itself
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
		rec, ok, err := st.Get(id)
		if err != nil || !ok {
			t.Fatalf("acked send %s lost after SIGKILL (ok=%v, %v) — committed sends must be durable", id, ok, err)
		}
		if !rec.Open || rec.Read {
			t.Fatalf("acked send %s = open=%v read=%v, want open unread", id, rec.Open, rec.Read)
		}
	}
	// The inbox lists every surviving message.
	recs, err := st.ListOpenForRecipients([]string{"crash/recipient"}, false)
	if err != nil {
		t.Fatalf("ListOpenForRecipients after crash: %v", err)
	}
	if len(recs) < ackTarget {
		t.Fatalf("inbox after crash = %d messages, want >= %d", len(recs), ackTarget)
	}
}
