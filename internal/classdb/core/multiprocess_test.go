package core

// G0 validation (engdocs/design/infra-class-sqlite-stores.md): the ratified
// access model opens each class store file from MULTIPLE PROCESSES — the
// controller's long-lived handle plus short-lived CLI/hook one-shots — so
// modernc's multi-process WAL behavior (shm coordination, POSIX locks, busy
// arbitration, commit durability under SIGKILL) is load-bearing and must be
// proven by test, not assumed. These tests re-exec the test binary as real
// child processes against one shared database file.

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	childDBEnv   = "CLASSDB_CORE_CHILD_DB"
	childIDEnv   = "CLASSDB_CORE_CHILD_ID"
	childModeEnv = "CLASSDB_CORE_CHILD_MODE"

	childRows = 50
)

// TestHelperClassdbChildWriter is not a real test: it is the child-process
// body for the multi-process tests below, entered only when re-exec'd with
// the CLASSDB_CORE_CHILD_* environment set.
func TestHelperClassdbChildWriter(t *testing.T) {
	path := os.Getenv(childDBEnv)
	if path == "" {
		t.Skip("not a child process")
	}
	childID := os.Getenv(childIDEnv)
	db, err := Open(path, testMigrations(), WithSingleConn())
	if err != nil {
		t.Fatalf("child open: %v", err)
	}
	defer db.Close() //nolint:errcheck

	switch os.Getenv(childModeEnv) {
	case "burst":
		// Insert a fixed batch, competing with sibling processes for the
		// write lock; every insert must eventually succeed via busy retry.
		for i := 0; i < childRows; i++ {
			id := fmt.Sprintf("%s-%d", childID, i)
			if err := db.Write(context.Background(), func(tx *sql.Tx) error {
				_, err := tx.Exec(`INSERT INTO items (id, val, created_at) VALUES (?, ?, ?)`, id, "child", time.Now().UnixNano())
				return err
			}); err != nil {
				t.Fatalf("child %s write %d: %v", childID, i, err)
			}
		}
	case "ack-stream":
		// Write rows forever, printing an ACK for each COMMITTED row. The
		// parent SIGKILLs this process mid-stream; every acked row must
		// survive (synchronous=FULL commit contract).
		out := bufio.NewWriter(os.Stdout)
		for i := 0; ; i++ {
			id := fmt.Sprintf("%s-%d", childID, i)
			if err := db.Write(context.Background(), func(tx *sql.Tx) error {
				_, err := tx.Exec(`INSERT INTO items (id, val, created_at) VALUES (?, ?, ?)`, id, "acked", time.Now().UnixNano())
				return err
			}); err != nil {
				t.Fatalf("child %s write %d: %v", childID, i, err)
			}
			fmt.Fprintf(out, "ACK %s\n", id) //nolint:errcheck
			out.Flush()                      //nolint:errcheck
		}
	default:
		t.Fatalf("unknown child mode %q", os.Getenv(childModeEnv))
	}
}

func childCmd(t *testing.T, dbPath, id, mode string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperClassdbChildWriter", "-test.v=false") //nolint:gosec // re-exec of the test binary itself
	cmd.Env = append(os.Environ(),
		childDBEnv+"="+dbPath,
		childIDEnv+"="+id,
		childModeEnv+"="+mode,
	)
	return cmd
}

// TestMultiProcessConcurrentWriters proves N real processes plus this
// process's long-lived handle can write one WAL database concurrently with
// zero lost or failed writes.
func TestMultiProcessConcurrentWriters(t *testing.T) {
	if testing.Short() {
		t.Skip("multi-process test skipped in -short")
	}
	dbPath := filepath.Join(t.TempDir(), "shared.db")
	parent, err := Open(dbPath, testMigrations())
	if err != nil {
		t.Fatalf("parent open: %v", err)
	}
	defer parent.Close() //nolint:errcheck

	const children = 4
	cmds := make([]*exec.Cmd, 0, children)
	outs := make([]strings.Builder, children)
	for c := 0; c < children; c++ {
		cmd := childCmd(t, dbPath, fmt.Sprintf("child%d", c), "burst")
		cmd.Stdout = &outs[c]
		cmd.Stderr = &outs[c]
		if err := cmd.Start(); err != nil {
			t.Fatalf("start child %d: %v", c, err)
		}
		cmds = append(cmds, cmd)
	}
	// The parent writes concurrently through its persistent handle.
	for i := 0; i < childRows; i++ {
		id := fmt.Sprintf("parent-%d", i)
		if err := parent.Write(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO items (id, val, created_at) VALUES (?, ?, ?)`, id, "parent", time.Now().UnixNano())
			return err
		}); err != nil {
			t.Fatalf("parent write %d: %v", i, err)
		}
	}
	for c, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("child %d failed: %v\n%s", c, err, outs[c].String())
		}
	}

	var n int
	if err := parent.Read().QueryRow(`SELECT COUNT(*) FROM items`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	want := (children + 1) * childRows
	if n != want {
		t.Fatalf("row count = %d, want %d (lost writes under multi-process WAL)", n, want)
	}
	ok, err := parent.IntegrityCheck(context.Background())
	if err != nil || !ok {
		t.Fatalf("integrity after concurrent multi-process writes: ok=%v err=%v", ok, err)
	}
}

// TestMultiProcessKillMidWriteDurability SIGKILLs a child mid-write-stream
// and proves every write the child ACKED (committed) is present after the
// kill, and the database is uncorrupted. This is the crash-durability half of
// the G0 gate: an acked enqueue (nudge), tracking-bead create (orders), or
// session transition must never be lost to a process death.
func TestMultiProcessKillMidWriteDurability(t *testing.T) {
	if testing.Short() {
		t.Skip("multi-process test skipped in -short")
	}
	dbPath := filepath.Join(t.TempDir(), "durable.db")
	// Create the schema first so the child doesn't race migration.
	seed, err := Open(dbPath, testMigrations())
	if err != nil {
		t.Fatalf("seed open: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("seed close: %v", err)
	}

	cmd := childCmd(t, dbPath, "victim", "ack-stream")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}

	const ackTarget = 20
	acked := make([]string, 0, ackTarget)
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		if id, found := strings.CutPrefix(line, "ACK "); found {
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

	db, err := Open(dbPath, testMigrations())
	if err != nil {
		t.Fatalf("reopen after kill: %v", err)
	}
	defer db.Close() //nolint:errcheck
	ok, err := db.IntegrityCheck(context.Background())
	if err != nil || !ok {
		t.Fatalf("integrity after SIGKILL: ok=%v err=%v", ok, err)
	}
	for _, id := range acked {
		var n int
		if err := db.Read().QueryRow(`SELECT COUNT(*) FROM items WHERE id = ?`, id).Scan(&n); err != nil {
			t.Fatalf("lookup %s: %v", id, err)
		}
		if n != 1 {
			t.Fatalf("acked row %s lost after SIGKILL — committed writes must be durable", id)
		}
	}
}
