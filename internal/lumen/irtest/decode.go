// Package irtest supplies typed fixtures for tests whose concern is lowering or
// execution rather than IR admission.
package irtest

import (
	"encoding/json"
	"fmt"

	"github.com/gastownhall/gascity/internal/lumen/ir"
)

// DecodeForLowering performs only the lossless typed decode. It is deliberately
// limited to lowering/runtime tests that construct malformed or legacy snippets
// to exercise pre-enqueue refusal paths. Admission tests and production callers
// use ir.Decode, which applies the pinned schema and semantic checks.
func DecodeForLowering(data []byte) (*ir.IR, error) {
	var doc ir.IR
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("decoding lumen.ir lowering fixture: %w", err)
	}
	return &doc, nil
}
