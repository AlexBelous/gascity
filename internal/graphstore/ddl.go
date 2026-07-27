package graphstore

type migration struct {
	Version int
	DDL     []string
}

var migrations = []migration{
	{
		Version: 1,
		DDL: []string{
			`CREATE TABLE IF NOT EXISTS journal (
			stream_id           TEXT    NOT NULL,
			seq                 INTEGER NOT NULL,
			substream           TEXT    NOT NULL DEFAULT '',
			engine              TEXT    NOT NULL CHECK (engine IN ('lumen','v2','v1')),
			type                TEXT    NOT NULL,
			ir_contract_version TEXT    NOT NULL,
			idem_token          TEXT,
			payload             BLOB    NOT NULL,
			payload_hash        BLOB    NOT NULL CHECK (length(payload_hash) = 32),
			chain_hash          BLOB    NOT NULL CHECK (length(chain_hash) = 32),
			lease_epoch         INTEGER NOT NULL,
			appended_at         TEXT    NOT NULL,
			PRIMARY KEY (stream_id, seq)
		)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS journal_idem
			ON journal (stream_id, idem_token) WHERE idem_token IS NOT NULL`,
			`CREATE INDEX IF NOT EXISTS journal_substream
			ON journal (stream_id, substream, seq) WHERE substream <> ''`,
			`CREATE INDEX IF NOT EXISTS journal_engine_type
			ON journal (engine, type, stream_id)`,
			`CREATE TRIGGER IF NOT EXISTS journal_no_update BEFORE UPDATE ON journal
			BEGIN SELECT RAISE(ABORT, 'journal is append-only'); END`,
			`CREATE TRIGGER IF NOT EXISTS journal_no_delete BEFORE DELETE ON journal
			BEGIN SELECT RAISE(ABORT, 'journal is append-only'); END`,
			`CREATE TABLE IF NOT EXISTS writer_lease (
			stream_id  TEXT    PRIMARY KEY,
			holder     TEXT    NOT NULL,
			epoch      INTEGER NOT NULL,
			expires_at TEXT    NOT NULL
		)`,
			`CREATE TABLE IF NOT EXISTS graph_meta (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		},
	},
	{
		Version: 2,
		DDL: []string{
			`DROP TRIGGER IF EXISTS journal_no_delete`,
			`CREATE TABLE IF NOT EXISTS retention_gate (
				stream_id TEXT PRIMARY KEY,
				max_seq   INTEGER NOT NULL
			)`,
			`CREATE TRIGGER IF NOT EXISTS journal_no_delete BEFORE DELETE ON journal
				WHEN NOT EXISTS (
					SELECT 1 FROM retention_gate
					WHERE stream_id = OLD.stream_id AND OLD.seq <= max_seq
				)
				BEGIN SELECT RAISE(ABORT, 'journal is append-only'); END`,
			`CREATE TABLE IF NOT EXISTS snapshots (
				stream_id               TEXT    NOT NULL,
				covered_seq             INTEGER NOT NULL,
				engine                  TEXT    NOT NULL,
				reducer_version         INTEGER NOT NULL,
				snapshot_format_version INTEGER NOT NULL,
				state_hash              BLOB    NOT NULL CHECK (length(state_hash) = 32),
				state                   BLOB    NOT NULL,
				cut_chain_hash          BLOB CHECK (
					cut_chain_hash IS NULL OR length(cut_chain_hash) = 32
				),
				created_at              TEXT    NOT NULL,
				PRIMARY KEY (stream_id, covered_seq)
			)`,
			`CREATE TABLE IF NOT EXISTS snapshot_write_gate (
				singleton INTEGER PRIMARY KEY CHECK (singleton = 0),
				open      INTEGER NOT NULL DEFAULT 0
			)`,
			`INSERT OR IGNORE INTO snapshot_write_gate(singleton, open) VALUES (0, 0)`,
			`CREATE TRIGGER IF NOT EXISTS snapshots_no_insert BEFORE INSERT ON snapshots
				WHEN NOT EXISTS (
					SELECT 1 FROM snapshot_write_gate WHERE singleton = 0 AND open = 1
				)
				BEGIN SELECT RAISE(ABORT, 'snapshots is write-closed'); END`,
			`CREATE TRIGGER IF NOT EXISTS snapshots_no_update BEFORE UPDATE ON snapshots
				WHEN NOT EXISTS (
					SELECT 1 FROM snapshot_write_gate WHERE singleton = 0 AND open = 1
				)
				BEGIN SELECT RAISE(ABORT, 'snapshots is write-closed'); END`,
			`CREATE TRIGGER IF NOT EXISTS snapshots_no_delete BEFORE DELETE ON snapshots
				WHEN NOT EXISTS (
					SELECT 1 FROM snapshot_write_gate WHERE singleton = 0 AND open = 1
				)
				BEGIN SELECT RAISE(ABORT, 'snapshots is write-closed'); END`,
			`CREATE TABLE IF NOT EXISTS nodes (
				id           TEXT PRIMARY KEY,
				title        TEXT    NOT NULL DEFAULT '',
				status       TEXT    NOT NULL DEFAULT 'open',
				bead_type    TEXT    NOT NULL DEFAULT 'task',
				priority     INTEGER,
				description  TEXT    NOT NULL DEFAULT '',
				assignee     TEXT    NOT NULL DEFAULT '',
				from_actor   TEXT    NOT NULL DEFAULT '',
				parent_id    TEXT    NOT NULL DEFAULT '',
				ref          TEXT    NOT NULL DEFAULT '',
				created_at   TEXT    NOT NULL,
				updated_at   TEXT    NOT NULL DEFAULT '',
				defer_until  TEXT,
				storage_tier TEXT    NOT NULL DEFAULT 'history'
					CHECK (storage_tier IN ('history','no_history','ephemeral')),
				is_blocked   INTEGER NOT NULL DEFAULT 0,
				fold_owned   INTEGER NOT NULL DEFAULT 0,
				stream_id    TEXT    NOT NULL DEFAULT ''
			)`,
			`CREATE INDEX IF NOT EXISTS nodes_status
				ON nodes (status, bead_type)`,
			`CREATE INDEX IF NOT EXISTS nodes_parent
				ON nodes (parent_id) WHERE parent_id <> ''`,
			`CREATE INDEX IF NOT EXISTS nodes_assignee
				ON nodes (assignee, status) WHERE assignee <> ''`,
			`CREATE INDEX IF NOT EXISTS nodes_stream
				ON nodes (stream_id) WHERE stream_id <> ''`,
			`CREATE TABLE IF NOT EXISTS node_labels (
				node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
				label   TEXT NOT NULL,
				PRIMARY KEY (node_id, label)
			)`,
			`CREATE INDEX IF NOT EXISTS node_labels_by_label ON node_labels (label)`,
			`CREATE TABLE IF NOT EXISTS node_metadata (
				node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
				key     TEXT NOT NULL,
				value   TEXT NOT NULL,
				PRIMARY KEY (node_id, key)
			)`,
			`CREATE INDEX IF NOT EXISTS node_metadata_kv ON node_metadata (key, value)`,
			`CREATE TABLE IF NOT EXISTS edges (
				from_id  TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
				to_id    TEXT NOT NULL,
				dep_type TEXT NOT NULL DEFAULT 'blocks',
				metadata TEXT NOT NULL DEFAULT '',
				PRIMARY KEY (from_id, to_id, dep_type)
			)`,
			`CREATE INDEX IF NOT EXISTS edges_reverse ON edges (to_id)`,
			`CREATE TABLE IF NOT EXISTS frontier (
				node_id        TEXT PRIMARY KEY,
				root_id        TEXT NOT NULL,
				route          TEXT NOT NULL DEFAULT '',
				ready_priority INTEGER NOT NULL DEFAULT 2,
				created_at     TEXT NOT NULL,
				id             TEXT NOT NULL,
				defer_until    TEXT
			) WITHOUT ROWID`,
			`CREATE INDEX IF NOT EXISTS frontier_route_order
				ON frontier (route, ready_priority, created_at, id)`,
			`CREATE TABLE IF NOT EXISTS defer_wakeups (
				node_id TEXT PRIMARY KEY,
				wake_at TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS defer_wakeups_by_time
				ON defer_wakeups (wake_at, node_id)`,
			`CREATE TABLE IF NOT EXISTS channel_cursors (
				stream_id    TEXT NOT NULL,
				substream    TEXT NOT NULL,
				reader_key   TEXT NOT NULL,
				position     INTEGER NOT NULL,
				planted_seq  INTEGER NOT NULL,
				advanced_seq INTEGER NOT NULL,
				PRIMARY KEY (stream_id, substream, reader_key)
			)`,
			`CREATE TABLE IF NOT EXISTS tier_a_write_gate (
				singleton INTEGER PRIMARY KEY CHECK (singleton = 0),
				open      INTEGER NOT NULL DEFAULT 0
			)`,
			`INSERT OR IGNORE INTO tier_a_write_gate(singleton, open) VALUES (0, 0)`,
			`CREATE TRIGGER IF NOT EXISTS nodes_fold_owned_no_insert BEFORE INSERT ON nodes
				WHEN NEW.fold_owned = 1 AND NOT EXISTS (
					SELECT 1 FROM tier_a_write_gate WHERE singleton = 0 AND open = 1
				)
				BEGIN SELECT RAISE(ABORT, 'nodes: fold-owned row is write-closed'); END`,
			`CREATE TRIGGER IF NOT EXISTS nodes_fold_owned_no_update BEFORE UPDATE ON nodes
				WHEN (OLD.fold_owned = 1 OR NEW.fold_owned = 1) AND NOT EXISTS (
					SELECT 1 FROM tier_a_write_gate WHERE singleton = 0 AND open = 1
				)
				BEGIN SELECT RAISE(ABORT, 'nodes: fold-owned row is write-closed'); END`,
			`CREATE TRIGGER IF NOT EXISTS nodes_fold_owned_no_delete BEFORE DELETE ON nodes
				WHEN OLD.fold_owned = 1 AND NOT EXISTS (
					SELECT 1 FROM tier_a_write_gate WHERE singleton = 0 AND open = 1
				)
				BEGIN SELECT RAISE(ABORT, 'nodes: fold-owned row is write-closed'); END`,
			`CREATE TRIGGER IF NOT EXISTS frontier_no_insert BEFORE INSERT ON frontier
				WHEN NOT EXISTS (
					SELECT 1 FROM tier_a_write_gate WHERE singleton = 0 AND open = 1
				)
				BEGIN SELECT RAISE(ABORT, 'frontier is write-closed'); END`,
			`CREATE TRIGGER IF NOT EXISTS frontier_no_update BEFORE UPDATE ON frontier
				WHEN NOT EXISTS (
					SELECT 1 FROM tier_a_write_gate WHERE singleton = 0 AND open = 1
				)
				BEGIN SELECT RAISE(ABORT, 'frontier is write-closed'); END`,
			`CREATE TRIGGER IF NOT EXISTS frontier_no_delete BEFORE DELETE ON frontier
				WHEN NOT EXISTS (
					SELECT 1 FROM tier_a_write_gate WHERE singleton = 0 AND open = 1
				)
				BEGIN SELECT RAISE(ABORT, 'frontier is write-closed'); END`,
		},
	},
}
