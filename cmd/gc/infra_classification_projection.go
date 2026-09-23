package main

import (
	"errors"
	"fmt"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/coordclass"
)

// readInfraContainmentIDs is only a live identity census. Actual migration,
// copy and recovery retain readInfraSnapshot, whose full payload they require.
func readInfraContainmentIDs(source beads.Store) ([]string, error) {
	if reader, ok := source.(beads.ClassificationReader); ok {
		rows, err := reader.ReadClassification()
		if err == nil {
			ids := make([]string, 0, len(rows))
			for _, row := range rows {
				if coordclass.Classify(beads.Bead{Type: row.Type, Labels: row.Labels, Metadata: row.Metadata}) != coordclass.ClassWork {
					ids = append(ids, row.ID)
				}
			}
			return ids, nil
		}
		if !errors.Is(err, beads.ErrClassificationUnsupported) {
			return nil, fmt.Errorf("reading work store classification: %w", err)
		}
	}
	rows, err := readInfraSnapshot(source)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids, nil
}
