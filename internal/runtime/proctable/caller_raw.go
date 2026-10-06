package proctable

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

const (
	callerMaxBytes   = 16 << 20
	callerMaxEntries = 4096
)

var errCallerUnknown = errors.New("caller incarnation evidence unavailable")

// CallerEnvironment carries private entries and separate acquisition completeness.
// No diagnostic representation exposes any process environment values.
// A syntactic decoder cannot grant OS provenance or establish omitted key absence.
type CallerEnvironment struct {
	entries  []string
	complete bool
}

// Format hides all private environment entries in diagnostic representations.
func (CallerEnvironment) Format(s fmt.State, _ rune) {
	_, _ = s.Write([]byte("[private caller environment]"))
}

// Complete reports whether a dedicated OS ENV region was captured and rechecked.
// False must never be interpreted as a known absence of ownership keys.
func (e CallerEnvironment) Complete() bool { return e.complete }

// Entries returns a private copy, never a diagnostic representation.
func (e CallerEnvironment) Entries() []string { return append([]string(nil), e.entries...) }

func callerStrictNUL(data []byte) (CallerEnvironment, error) {
	if len(data) == 0 || len(data) > callerMaxBytes || data[len(data)-1] != 0 {
		return CallerEnvironment{}, errCallerUnknown
	}
	entries := []string{}
	seen := map[string]bool{}
	for at := 0; at < len(data); {
		end := bytes.IndexByte(data[at:], 0)
		if end < 0 {
			return CallerEnvironment{}, errCallerUnknown
		}
		entry := string(data[at : at+end])
		at += end + 1
		if entry == "" {
			for _, b := range data[at:] {
				if b != 0 {
					return CallerEnvironment{}, errCallerUnknown
				}
			}
			break
		}
		key, _, ok := strings.Cut(entry, "=")
		if !ok || key == "" || len(entries) >= callerMaxEntries {
			return CallerEnvironment{}, errCallerUnknown
		}
		for _, b := range []byte(key) {
			if b <= 32 || b == 127 {
				return CallerEnvironment{}, errCallerUnknown
			}
		}
		relevant := strings.HasPrefix(key, "GC_") || key == "BEADS_HOLDER_TOKEN" || key == "BEADS_ACTOR"
		if relevant && seen[key] {
			return CallerEnvironment{}, errCallerUnknown
		}
		if relevant {
			seen[key] = true
		}
		entries = append(entries, entry)
	}
	if len(entries) == 0 {
		return CallerEnvironment{}, errCallerUnknown
	}
	return CallerEnvironment{entries: entries}, nil
}

// kern.procargs2 begins with native argc, executable path and padding, then
// exactly argc NUL-delimited argv strings. Only the following region is ENV.
// A restricted/omitted ENV cannot establish key absence and remains UNKNOWN.
// Even a syntactically valid slice is NOT a complete Darwin ENV proof: XNU
// exposes no envc/endenvv, and ENV alignment can be followed by Apple strings.
// The production-neutral chain reader requires a separate completeness fact;
// Darwin acquisition below deliberately never asserts it.
func callerDarwinProcargs(data []byte) (CallerEnvironment, error) {
	if len(data) < 5 || len(data) > callerMaxBytes {
		return CallerEnvironment{}, errCallerUnknown
	}
	argc := int(binary.NativeEndian.Uint32(data[:4]))
	if argc < 1 || argc > callerMaxEntries {
		return CallerEnvironment{}, errCallerUnknown
	}
	end := bytes.IndexByte(data[4:], 0)
	if end <= 0 {
		return CallerEnvironment{}, errCallerUnknown
	}
	at := 4 + end + 1
	for at < len(data) && data[at] == 0 {
		at++
	}
	for i := 0; i < argc; i++ {
		if at >= len(data) {
			return CallerEnvironment{}, errCallerUnknown
		}
		end = bytes.IndexByte(data[at:], 0)
		if end < 0 {
			return CallerEnvironment{}, errCallerUnknown
		}
		at += end + 1
	}
	if at >= len(data) {
		return CallerEnvironment{}, errCallerUnknown
	}
	return callerStrictNUL(data[at:])
}
