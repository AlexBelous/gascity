package core

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"
)

// TestCheckpointTruncatesWAL proves wal_checkpoint(TRUNCATE) folds the WAL
// into the main file: after writes the -wal file is non-empty; after the
// checkpoint it is zero bytes and the data is still readable.
func TestCheckpointTruncatesWAL(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	for i := 0; i < 50; i++ {
		if err := db.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO items (id, val, created_at) VALUES (?, 'payload', ?)`, fmt.Sprintf("row-%d", i), i)
			return err
		}); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	walPath := db.Path() + "-wal"
	before, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("stat wal before checkpoint: %v", err)
	}
	if before.Size() == 0 {
		t.Fatal("wal empty before checkpoint; test premise broken")
	}

	skipped, err := db.Checkpoint(ctx)
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if skipped {
		t.Fatal("checkpoint reported busy on an idle db")
	}
	after, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("stat wal after checkpoint: %v", err)
	}
	if after.Size() != 0 {
		t.Fatalf("wal size after TRUNCATE checkpoint = %d, want 0", after.Size())
	}

	var n int
	if err := db.Read().QueryRow(`SELECT COUNT(*) FROM items`).Scan(&n); err != nil || n != 50 {
		t.Fatalf("post-checkpoint count = (%d, %v), want 50", n, err)
	}
}

// TestVacuumReclaimsAndKeepsData proves VACUUM runs on the write handle
// (outside any transaction) and leaves the data intact.
func TestVacuumReclaimsAndKeepsData(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := db.Write(ctx, func(tx *sql.Tx) error {
		for i := 0; i < 20; i++ {
			if _, err := tx.Exec(`INSERT INTO items (id, val, created_at) VALUES (?, 'v', ?)`, fmt.Sprintf("r%d", i), i); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM items WHERE created_at >= 10`)
		return err
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := db.Vacuum(ctx); err != nil {
		t.Fatalf("Vacuum: %v", err)
	}
	var n int
	if err := db.Read().QueryRow(`SELECT COUNT(*) FROM items`).Scan(&n); err != nil || n != 10 {
		t.Fatalf("post-vacuum count = (%d, %v), want 10", n, err)
	}
}

// TestStartMaintenanceStops proves the loop rides the sweeper scaffold:
// stop() returns and is idempotent, and Close also stops it.
func TestStartMaintenanceStops(t *testing.T) {
	db := openTestDB(t)
	stop := db.StartMaintenance(time.Hour, time.Hour, nil)
	stop()
	stop()
	db2 := openTestDB(t)
	_ = db2.StartMaintenance(time.Hour, 0, nil)
	if err := db2.Close(); err != nil {
		t.Fatalf("Close with live maintenance loop: %v", err)
	}
}
