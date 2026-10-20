package runtime

import "log/slog"

// PromptText carries literal invocation-local startup text. Its ordinary
// representations are redacted; only an explicit Literal call exposes bytes.
type PromptText struct {
	literal string
	set     bool
}

// PromptTextFromLiteral preserves text without shell parsing or evaluation.
func PromptTextFromLiteral(text string) PromptText { return PromptText{literal: text, set: true} }

// IsSet distinguishes an explicit empty literal from a missing carrier.
func (p PromptText) IsSet() bool { return p.set }

// Literal exposes the exact startup bytes to an authorized delivery adapter.
func (p PromptText) Literal() string { return p.literal }

// String redacts the literal during ordinary formatting.
func (PromptText) String() string { return "[redacted startup prompt]" }

// GoString redacts the literal during Go-syntax formatting.
func (PromptText) GoString() string { return "[redacted startup prompt]" }

// LogValue prevents structured logging from exposing invocation-local text.
func (PromptText) LogValue() slog.Value { return slog.StringValue("[redacted startup prompt]") }
