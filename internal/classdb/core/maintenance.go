package core

// Store maintenance (engdocs/design/infra-class-sqlite-stores.md, "Doctor /
// storehealth / maintenance"): the class-store files get their own periodic
// WAL checkpoint and VACUUM — before this, only Dolt got maintenance, so the
// new .gc/store files would otherwise accumulate WAL forever on a long-lived
// controller.

import (
	"context"
	"fmt"
	"io"
	"time"
)

// Checkpoint runs PRAGMA wal_checkpoint(TRUNCATE) on the write handle,
// folding the WAL back into the database file and truncating it. A busy
// checkpoint (a reader or writer held the WAL) reports skipped=true and no
// error — the next cadence tick converges.
func (d *DB) Checkpoint(ctx context.Context) (skipped bool, err error) {
	var busy, walFrames, checkpointed int
	row := d.write.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
	if err := row.Scan(&busy, &walFrames, &checkpointed); err != nil {
		return false, fmt.Errorf("wal_checkpoint(TRUNCATE) on %s: %w", d.path, err)
	}
	return busy != 0, nil
}

// Vacuum rebuilds the database file, reclaiming free pages. It runs as a
// plain autocommit statement on the write handle (VACUUM cannot run inside
// a transaction) and is expected on a slow cadence — the class stores are
// small, but retention DELETEs would otherwise never return disk.
func (d *DB) Vacuum(ctx context.Context) error {
	if _, err := d.write.ExecContext(ctx, `VACUUM`); err != nil {
		return fmt.Errorf("vacuum %s: %w", d.path, err)
	}
	return nil
}

// StartMaintenance starts the per-file maintenance loop: a checkpoint every
// checkpointInterval, plus a VACUUM once vacuumInterval has elapsed since
// the loop started or last vacuumed. It rides the sweeper scaffold, so the
// loop stops when the DB closes or the returned stop is called. Failures
// are reported to warn (nil discards them); a busy checkpoint is routine
// and silent.
func (d *DB) StartMaintenance(checkpointInterval, vacuumInterval time.Duration, warn io.Writer) (stop func()) {
	lastVacuum := time.Now()
	return d.StartSweeper(checkpointInterval, func(ctx context.Context) {
		if _, err := d.Checkpoint(ctx); err != nil && warn != nil {
			fmt.Fprintf(warn, "class store maintenance: %v\n", err) //nolint:errcheck // best-effort warning
		}
		if vacuumInterval <= 0 || time.Since(lastVacuum) < vacuumInterval {
			return
		}
		if err := d.Vacuum(ctx); err != nil {
			if warn != nil {
				fmt.Fprintf(warn, "class store maintenance: %v\n", err) //nolint:errcheck // best-effort warning
			}
			return
		}
		lastVacuum = time.Now()
	})
}
