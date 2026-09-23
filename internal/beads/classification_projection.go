package beads

import "errors"

// ClassificationRow is the complete input to coordination-class classification,
// not a hydrated bead. It must never be used as a migration or write payload.
type ClassificationRow struct {
	ID       string
	Type     string
	Labels   []string
	Metadata map[string]string
}

// ClassificationReader optionally reads every resident's classification fields,
// including closed rows and both storage tiers. Each call observes the backing
// store anew. Unsupported stores must report ErrClassificationUnsupported;
// read or decode failures must not be represented as an empty census.
type ClassificationReader interface {
	ReadClassification() ([]ClassificationRow, error)
}

// ErrClassificationUnsupported asks the caller to use its full-read fallback.
var ErrClassificationUnsupported = errors.New("classification projection unsupported")
