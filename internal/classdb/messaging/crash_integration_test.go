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
	"time"

	"github.com/gastownhall/gascity/internal/classdb/core"
	"github.com/gastownhall/gascity/internal/extmsg"
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

const extmsgCrashChildDBEnv = "MESSAGINGDB_EXTMSG_CRASH_CHILD_DB"

// TestHelperMessagingdbChildBinder is the child-process body for the extmsg
// crash gate: per iteration it creates a binding, the conversation's
// transcript state, and one appended entry (the entry insert and the
// sequence-allocator bump share one transaction on this backend), printing
// an ACK for each iteration whose writes all returned, until the parent
// SIGKILLs it.
func TestHelperMessagingdbChildBinder(t *testing.T) {
	path := os.Getenv(extmsgCrashChildDBEnv)
	if path == "" {
		t.Skip("not a child process")
	}
	st, err := Open(path, core.WithSingleConn())
	if err != nil {
		t.Fatalf("child open: %v", err)
	}
	defer st.Close() //nolint:errcheck

	out := bufio.NewWriter(os.Stdout)
	now := time.Now().UTC()
	for i := 0; ; i++ {
		ref := extmsgCrashRef(i)
		if _, err := st.CreateBinding(extmsg.BindingCreate{
			Ref:        ref,
			SessionID:  "crash/session",
			Generation: 1,
			BoundAt:    now,
		}, "", nil); err != nil {
			t.Fatalf("child CreateBinding %d: %v", i, err)
		}
		state, err := st.Writer().CreateTranscriptState(ref)
		if err != nil {
			t.Fatalf("child CreateTranscriptState %d: %v", i, err)
		}
		if _, err := st.AppendTranscript(extmsg.TranscriptEntryCreate{
			Ref:        ref,
			Sequence:   1,
			Kind:       extmsg.TranscriptMessageInbound,
			Provenance: extmsg.TranscriptProvenanceLive,
			CreatedAt:  now,
			Text:       "crash entry",
		}, state.ID, 2, true); err != nil {
			t.Fatalf("child AppendTranscript %d: %v", i, err)
		}
		fmt.Fprintf(out, "ACK %d\n", i) //nolint:errcheck
		out.Flush()                     //nolint:errcheck
	}
}

func extmsgCrashRef(i int) extmsg.ConversationRef {
	return extmsg.ConversationRef{
		ScopeID:        "city",
		Provider:       "slack",
		AccountID:      "crash-acct",
		ConversationID: fmt.Sprintf("crash-conv-%d", i),
		Kind:           extmsg.ConversationDM,
	}
}

// TestCrashDurabilityAckedExtmsgWritesSurviveKill SIGKILLs a child mid-write
// stream and proves every acked iteration survives reopen: the binding is
// active, and the transcript entry exists WITH its sequence-allocator bump —
// the atomic pairing that closes the bd backend's create-then-bump crash
// window (an entry without the bump would re-issue its sequence on the next
// append).
func TestCrashDurabilityAckedExtmsgWritesSurviveKill(t *testing.T) {
	if testing.Short() {
		t.Skip("multi-process test skipped in -short")
	}
	dbPath := filepath.Join(t.TempDir(), "messaging.db")
	seed, err := Open(dbPath)
	if err != nil {
		t.Fatalf("seed open: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("seed close: %v", err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestHelperMessagingdbChildBinder", "-test.v=false") //nolint:gosec // re-exec of the test binary itself
	cmd.Env = append(os.Environ(), extmsgCrashChildDBEnv+"="+dbPath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}

	const ackTarget = 10
	acked := 0
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "ACK ") {
			acked++
			if acked >= ackTarget {
				break
			}
		}
	}
	if acked < ackTarget {
		t.Fatalf("child died before %d acks (got %d)", ackTarget, acked)
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

	for i := 0; i < ackTarget; i++ {
		ref := extmsgCrashRef(i)
		history, err := st.BindingHistory(ref)
		if err != nil || len(history) != 1 || history[0].Status != extmsg.BindingActive {
			t.Fatalf("acked binding %d after SIGKILL = %+v, %v; want one active — acked binds must be durable", i, history, err)
		}
		states, err := st.OpenTranscriptStates(ref)
		if err != nil || len(states) != 1 {
			t.Fatalf("acked state %d after SIGKILL = %+v, %v; want one row", i, states, err)
		}
		entries, err := st.ListTranscript(ref, 0, 1, 1, 10, false)
		if err != nil || len(entries) != 1 {
			t.Fatalf("acked entry %d after SIGKILL = %+v, %v; want one row", i, entries, err)
		}
		if states[0].NextSequence != 2 {
			t.Fatalf("acked allocator %d = %d, want 2 — the entry and its bump commit as one unit", i, states[0].NextSequence)
		}
	}
}
