package beads

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"

	"github.com/go-sql-driver/mysql"
)

type nativeClassificationQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// One statement keeps all class inputs in the same snapshot. The provider's
// exported QueryContext retains its closed-store check, retry and circuit
// admission; the caller retains NativeDoltStore's lifetime lock and reconnect
// budget. Do not replace this capability with a raw DB() accessor.
// This narrow projection intentionally does not validate unrelated body,
// lease or dependency fields. Ordinary full reads retain that responsibility.
const nativeClassificationQuery = `
SELECT 0 AS tier, i.id, i.issue_type, i.metadata, l.issue_id AS label_id, l.label
FROM issues i LEFT JOIN labels l ON i.id = l.issue_id
UNION ALL
SELECT 1 AS tier, i.id, i.issue_type, i.metadata, l.issue_id AS label_id, l.label
FROM wisps i LEFT JOIN wisp_labels l ON i.id = l.issue_id
ORDER BY tier, id, label`

func readNativeClassificationSQL(ctx context.Context, provider nativeClassificationQuerier) ([]ClassificationRow, error) {
	rows, err := provider.QueryContext(ctx, nativeClassificationQuery)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	census := make(map[string]*nativeClassificationInput)
	for rows.Next() {
		var tier int
		var id, kind string
		var metadata []byte
		var labelID, label sql.NullString
		if err := rows.Scan(&tier, &id, &kind, &metadata, &labelID, &label); err != nil {
			return nil, err
		}
		row := census[id]
		if row == nil || row.tier != tier {
			// Sorted tier 1 replaces tier 0, including labels. Decode only winners:
			// shadowed durable metadata may be invalid while the wisp is valid.
			row = &nativeClassificationInput{tier: tier, kind: kind, metadata: metadata}
			census[id] = row
		}
		if labelID.Valid {
			if !label.Valid {
				return nil, fmt.Errorf("null classification label for bead %q", id)
			}
			row.labels = append(row.labels, label.String)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(census))
	for id := range census {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]ClassificationRow, 0, len(ids))
	for _, id := range ids {
		row := census[id]
		// The canonical Lite scanner normalizes exactly "{}" to absent.
		if string(row.metadata) == "{}" {
			row.metadata = nil
		}
		metadata, err := metadataMapFromNative(row.metadata)
		if err != nil {
			return nil, fmt.Errorf("parsing metadata for bead %q: %w: %w", id, errNativeIssueMetadataParse, err)
		}
		result = append(result, ClassificationRow{ID: id, Type: row.kind, Labels: row.labels, Metadata: metadata})
	}
	return result, nil
}

type nativeClassificationInput struct {
	tier     int
	kind     string
	metadata []byte
	labels   []string
}

func nativeClassificationMissingTable(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1146
}
