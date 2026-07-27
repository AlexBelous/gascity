package graphstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"

	"github.com/gastownhall/gascity/internal/graphstore/canon"
	"github.com/gastownhall/gascity/internal/graphstore/fold"
)

// ErrProjectionIDCollision reports that a fold tried to claim a node owned by
// another persistence path.
var ErrProjectionIDCollision = errors.New(
	"graphstore: fold node id collides with a non-fold-owned row",
)

// ApplyDelta applies one reducer delta to the rebuildable Tier-A projection.
func (s *Store) ApplyDelta(ctx context.Context, delta fold.Delta) error {
	return s.ApplyDeltas(ctx, []fold.Delta{delta})
}

// ApplyDeltas applies reducer deltas atomically in order.
func (s *Store) ApplyDeltas(ctx context.Context, deltas []fold.Delta) error {
	if len(deltas) == 0 {
		return nil
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		if err := openTierAGate(ctx, tx); err != nil {
			return err
		}
		for i := range deltas {
			if err := applyDeltaLocked(ctx, tx, deltas[i]); err != nil {
				return fmt.Errorf("graphstore: apply projection delta %d: %w", i, err)
			}
		}
		return closeTierAGate(ctx, tx)
	})
}

// RebuildTierA replaces a stream's projection with a deterministic refold of
// its journal. A truncated stream starts from its covering snapshot.
func (s *Store) RebuildTierA(
	ctx context.Context,
	reducer fold.Reducer,
	streamID string,
) error {
	if streamID == "" {
		return fmt.Errorf("graphstore: rebuild tier A: empty stream id")
	}
	deltas, foldedHead, err := s.rebuildDeltas(ctx, reducer, streamID)
	if err != nil {
		return err
	}
	if s.rebuildAfterRead != nil {
		s.rebuildAfterRead()
	}

	return s.write(ctx, func(tx *sql.Tx) error {
		var currentHead uint64
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(seq), 0) FROM journal WHERE stream_id = ?`,
			streamID,
		).Scan(&currentHead); err != nil {
			return fmt.Errorf(
				"graphstore: rebuild tier A %q: read head: %w",
				streamID,
				err,
			)
		}
		if currentHead != foldedHead {
			return fmt.Errorf(
				"graphstore: rebuild tier A %q: folded head %d, current head %d: %w",
				streamID,
				foldedHead,
				currentHead,
				ErrRebuildRaced,
			)
		}

		if err := openTierAGate(ctx, tx); err != nil {
			return err
		}
		if err := dropStreamTierA(ctx, tx, streamID); err != nil {
			return fmt.Errorf("graphstore: rebuild tier A %q: %w", streamID, err)
		}
		for i := range deltas {
			if err := applyDeltaLocked(ctx, tx, deltas[i]); err != nil {
				return fmt.Errorf(
					"graphstore: rebuild tier A %q: apply delta %d: %w",
					streamID,
					i,
					err,
				)
			}
		}
		return closeTierAGate(ctx, tx)
	})
}

func (s *Store) rebuildDeltas(
	ctx context.Context,
	reducer fold.Reducer,
	streamID string,
) ([]fold.Delta, uint64, error) {
	stored, err := s.ReadStream(ctx, streamID, 1, 0)
	if err != nil {
		return nil, 0, err
	}
	if len(stored) == 0 || stored[0].Seq == 1 {
		events := storedToFoldEvents(stored)
		_, deltas, err := fold.Fold(reducer, nil, events)
		if err != nil {
			return nil, 0, fmt.Errorf(
				"graphstore: rebuild tier A %q: fold: %w",
				streamID,
				err,
			)
		}
		var head uint64
		if len(stored) > 0 {
			head = stored[len(stored)-1].Seq
		}
		return deltas, head, nil
	}

	snapshot, ok, err := s.LatestSnapshot(ctx, streamID)
	if err != nil {
		return nil, 0, err
	}
	if !ok {
		return nil, 0, fmt.Errorf(
			"graphstore: rebuild tier A %q: journal starts at %d: %w",
			streamID,
			stored[0].Seq,
			ErrNoCoveringSnapshot,
		)
	}
	if canon.Hash(snapshot.State) != snapshot.StateHash {
		return nil, 0, fmt.Errorf(
			"graphstore: rebuild tier A %q: snapshot at %d: %w",
			streamID,
			snapshot.CoveredSeq,
			ErrSnapshotHashMismatch,
		)
	}
	state, err := reducer.UnmarshalSnapshot(snapshot.SnapshotFormatVersion, snapshot.State)
	if err != nil {
		return nil, 0, fmt.Errorf(
			"graphstore: rebuild tier A %q: unmarshal snapshot at %d: %w",
			streamID,
			snapshot.CoveredSeq,
			err,
		)
	}
	projector, ok := state.(fold.SnapshotProjector)
	if !ok {
		return nil, 0, fmt.Errorf(
			"graphstore: rebuild tier A %q: snapshot state %T cannot project",
			streamID,
			state,
		)
	}

	tail, err := s.ReadStream(ctx, streamID, snapshot.CoveredSeq+1, 0)
	if err != nil {
		return nil, 0, err
	}
	_, tailDeltas, err := fold.Fold(reducer, &snapshot, storedToFoldEvents(tail))
	if err != nil {
		return nil, 0, fmt.Errorf(
			"graphstore: rebuild tier A %q: fold tail: %w",
			streamID,
			err,
		)
	}
	deltas := make([]fold.Delta, 0, len(tailDeltas)+1)
	deltas = append(deltas, projector.ProjectDelta(streamID))
	deltas = append(deltas, tailDeltas...)

	head := snapshot.CoveredSeq
	if len(tail) > 0 {
		head = tail[len(tail)-1].Seq
	}
	return deltas, head, nil
}

func storedToFoldEvents(stored []StoredEvent) []fold.Event {
	events := make([]fold.Event, len(stored))
	for i, event := range stored {
		events[i] = fold.Event{
			StreamID:          event.StreamID,
			Seq:               event.Seq,
			Engine:            event.Engine,
			Substream:         event.Substream,
			Type:              event.Type,
			IRContractVersion: event.IRContractVersion,
			IdemToken:         event.IdemToken,
			Payload:           event.Payload,
		}
	}
	return events
}

func openTierAGate(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE tier_a_write_gate SET open = 1 WHERE singleton = 0`,
	); err != nil {
		return fmt.Errorf("graphstore: open tier-A write gate: %w", err)
	}
	return nil
}

func closeTierAGate(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE tier_a_write_gate SET open = 0 WHERE singleton = 0`,
	); err != nil {
		return fmt.Errorf("graphstore: close tier-A write gate: %w", err)
	}
	return nil
}

func dropStreamTierA(ctx context.Context, tx *sql.Tx, streamID string) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM defer_wakeups
		  WHERE node_id IN (SELECT id FROM nodes WHERE stream_id = ?)`,
		streamID,
	); err != nil {
		return fmt.Errorf("clear wakeups: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM frontier WHERE root_id = ?`,
		streamID,
	); err != nil {
		return fmt.Errorf("clear frontier: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM channel_cursors WHERE stream_id = ?`,
		streamID,
	); err != nil {
		return fmt.Errorf("clear cursors: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM nodes WHERE stream_id = ? AND fold_owned = 1`,
		streamID,
	); err != nil {
		return fmt.Errorf("clear nodes: %w", err)
	}
	return nil
}

func applyDeltaLocked(ctx context.Context, tx *sql.Tx, delta fold.Delta) error {
	for _, node := range delta.NodeUpserts {
		if err := upsertNode(ctx, tx, node); err != nil {
			return err
		}
	}
	for _, edge := range delta.EdgeUpserts {
		dependencyType := edge.DepType
		if dependencyType == "" {
			dependencyType = "blocks"
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO edges(from_id, to_id, dep_type, metadata)
			 VALUES (?, ?, ?, ?)
			 ON CONFLICT(from_id, to_id, dep_type)
			 DO UPDATE SET metadata = excluded.metadata`,
			edge.FromID,
			edge.ToID,
			dependencyType,
			edge.Metadata,
		); err != nil {
			return fmt.Errorf(
				"upsert edge %s -> %s: %w",
				edge.FromID,
				edge.ToID,
				err,
			)
		}
	}
	for _, nodeID := range delta.FrontierDelete {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM frontier WHERE node_id = ?`,
			nodeID,
		); err != nil {
			return fmt.Errorf("delete frontier node %q: %w", nodeID, err)
		}
	}
	for _, frontier := range delta.FrontierInsert {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO frontier(
				node_id, root_id, route, ready_priority, created_at, id, defer_until
			) VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(node_id) DO UPDATE SET
				root_id = excluded.root_id,
				route = excluded.route,
				ready_priority = excluded.ready_priority,
				created_at = excluded.created_at,
				id = excluded.id,
				defer_until = excluded.defer_until`,
			frontier.NodeID,
			frontier.RootID,
			frontier.Route,
			frontier.ReadyPriority,
			frontier.CreatedAt,
			frontier.ID,
			nullableString(frontier.DeferUntil),
		); err != nil {
			return fmt.Errorf("upsert frontier node %q: %w", frontier.NodeID, err)
		}
	}
	for _, cursor := range delta.CursorUpserts {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO channel_cursors(
				stream_id, substream, reader_key, position, planted_seq, advanced_seq
			) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(stream_id, substream, reader_key) DO UPDATE SET
				position = excluded.position,
				planted_seq = excluded.planted_seq,
				advanced_seq = excluded.advanced_seq`,
			cursor.StreamID,
			cursor.Substream,
			cursor.ReaderKey,
			cursor.Position,
			cursor.PlantedSeq,
			cursor.AdvancedSeq,
		); err != nil {
			return fmt.Errorf(
				"upsert cursor %s/%s/%s: %w",
				cursor.StreamID,
				cursor.Substream,
				cursor.ReaderKey,
				err,
			)
		}
	}
	for _, nodeID := range delta.WakeupDeletes {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM defer_wakeups WHERE node_id = ?`,
			nodeID,
		); err != nil {
			return fmt.Errorf("delete wakeup %q: %w", nodeID, err)
		}
	}
	for _, wakeup := range delta.WakeupUpserts {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO defer_wakeups(node_id, wake_at)
			 VALUES (?, ?)
			 ON CONFLICT(node_id) DO UPDATE SET wake_at = excluded.wake_at`,
			wakeup.NodeID,
			wakeup.WakeAt,
		); err != nil {
			return fmt.Errorf("upsert wakeup %q: %w", wakeup.NodeID, err)
		}
	}
	return nil
}

func upsertNode(ctx context.Context, tx *sql.Tx, node fold.NodeRow) error {
	var foldOwned int
	err := tx.QueryRowContext(ctx,
		`SELECT fold_owned FROM nodes WHERE id = ?`,
		node.ID,
	).Scan(&foldOwned)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return fmt.Errorf("check node %q ownership: %w", node.ID, err)
	case foldOwned == 0:
		return fmt.Errorf("node %q: %w", node.ID, ErrProjectionIDCollision)
	}

	storageTier := node.StorageTier
	if storageTier == "" {
		storageTier = "history"
	}
	result, err := tx.ExecContext(ctx,
		`INSERT INTO nodes(
			id, title, status, bead_type, priority, description, assignee,
			from_actor, parent_id, ref, created_at, updated_at, defer_until,
			storage_tier, is_blocked, fold_owned, stream_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?)
		ON CONFLICT(id) DO UPDATE SET
			title = excluded.title,
			status = excluded.status,
			bead_type = excluded.bead_type,
			priority = excluded.priority,
			description = excluded.description,
			assignee = excluded.assignee,
			from_actor = excluded.from_actor,
			parent_id = excluded.parent_id,
			ref = excluded.ref,
			created_at = excluded.created_at,
			updated_at = excluded.updated_at,
			defer_until = excluded.defer_until,
			storage_tier = excluded.storage_tier,
			is_blocked = excluded.is_blocked,
			fold_owned = 1,
			stream_id = excluded.stream_id
		WHERE nodes.fold_owned = 1`,
		node.ID,
		node.Title,
		node.Status,
		node.BeadType,
		nullableInt(node.Priority),
		node.Description,
		node.Assignee,
		node.FromActor,
		node.ParentID,
		node.Ref,
		node.CreatedAt,
		node.UpdatedAt,
		nullableString(node.DeferUntil),
		storageTier,
		boolToInt(node.IsBlocked),
		node.StreamID,
	)
	if err != nil {
		return fmt.Errorf("upsert node %q: %w", node.ID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read upsert count for node %q: %w", node.ID, err)
	}
	if affected == 0 {
		return fmt.Errorf("node %q: %w", node.ID, ErrProjectionIDCollision)
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM node_labels WHERE node_id = ?`,
		node.ID,
	); err != nil {
		return fmt.Errorf("clear labels for node %q: %w", node.ID, err)
	}
	labels := append([]string(nil), node.Labels...)
	sort.Strings(labels)
	for _, label := range labels {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO node_labels(node_id, label)
			 VALUES (?, ?)
			 ON CONFLICT(node_id, label) DO NOTHING`,
			node.ID,
			label,
		); err != nil {
			return fmt.Errorf("insert node %q label %q: %w", node.ID, label, err)
		}
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM node_metadata WHERE node_id = ?`,
		node.ID,
	); err != nil {
		return fmt.Errorf("clear metadata for node %q: %w", node.ID, err)
	}
	keys := make([]string, 0, len(node.Metadata))
	for key := range node.Metadata {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := node.Metadata[key]
		if value == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO node_metadata(node_id, key, value)
			 VALUES (?, ?, ?)
			 ON CONFLICT(node_id, key) DO UPDATE SET value = excluded.value`,
			node.ID,
			key,
			value,
		); err != nil {
			return fmt.Errorf("insert node %q metadata %q: %w", node.ID, key, err)
		}
	}
	return nil
}

func nullableInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
