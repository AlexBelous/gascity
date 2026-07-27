package graphstore

import (
	"bytes"
	"errors"
	"math"
	"testing"
)

func TestNewRecordValidatesAndDefensivelyCopiesCanonicalPayload(t *testing.T) {
	for _, input := range []struct {
		sequence uint64
		typ      string
		payload  []byte
	}{
		{sequence: 0, typ: "kernel.command", payload: []byte(`{}`)},
		{sequence: 1, typ: "", payload: []byte(`{}`)},
		{sequence: 1, typ: "kernel.command", payload: nil},
		{sequence: 1, typ: "kernel.command", payload: []byte(`{"a":`)},
	} {
		if _, err := NewRecord(input.sequence, input.typ, input.payload); err == nil {
			t.Fatalf("NewRecord(%d, %q, %q) succeeded, want error", input.sequence, input.typ, input.payload)
		}
	}

	payload := []byte(`{"z":2,"a":1}`)
	record, err := NewRecord(1, "kernel.command", payload)
	if err != nil {
		t.Fatalf("NewRecord: %v", err)
	}
	payload[2] = 'x'
	if got, want := string(record.Payload()), `{"a":1,"z":2}`; got != want {
		t.Fatalf("canonical payload = %s, want %s", got, want)
	}
	payloadCopy := record.Payload()
	payloadCopy[2] = 'x'
	if got, want := string(record.Payload()), `{"a":1,"z":2}`; got != want {
		t.Fatalf("record payload changed through accessor: %s, want %s", got, want)
	}
}

func TestCompletePrefixAdvancesCursorMonotonically(t *testing.T) {
	first := mustRecord(t, 3, "kernel.command", `{"id":"a"}`)
	second := mustRecord(t, 4, "kernel.observed", `{"id":"a","status":"ok"}`)
	prefix, err := NewCompletePrefix(CursorAt(2), CursorAt(4), []Record{first, second})
	if err != nil {
		t.Fatalf("NewCompletePrefix: %v", err)
	}
	if got, want := prefix.Through().Sequence(), uint64(4); got != want {
		t.Fatalf("prefix through = %d, want %d", got, want)
	}

	next, err := CursorAt(2).Advance(prefix)
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if got, want := next.Sequence(), uint64(4); got != want {
		t.Fatalf("advanced sequence = %d, want %d", got, want)
	}
	if _, err := CursorAt(1).Advance(prefix); !errors.Is(err, ErrCursorMismatch) {
		t.Fatalf("Advance from stale cursor error = %v, want ErrCursorMismatch", err)
	}
}

func TestCompletePrefixAllowsOnlyAnExactlyEmptyBoundary(t *testing.T) {
	prefix, err := NewCompletePrefix(CursorAt(5), CursorAt(5), nil)
	if err != nil {
		t.Fatalf("empty complete prefix: %v", err)
	}
	if got, want := prefix.Through().Sequence(), uint64(5); got != want {
		t.Fatalf("empty prefix through = %d, want %d", got, want)
	}
	if _, err := NewCompletePrefix(CursorAt(5), CursorAt(6), nil); !errors.Is(err, ErrIncompletePrefix) {
		t.Fatalf("empty advancing prefix error = %v, want ErrIncompletePrefix", err)
	}
}

func TestCompletePrefixRejectsMismatchedCoverage(t *testing.T) {
	three := mustRecord(t, 3, "kernel.command", `{}`)
	four := mustRecord(t, 4, "kernel.observed", `{}`)
	five := mustRecord(t, 5, "kernel.terminal", `{}`)
	for _, test := range []struct {
		name    string
		after   Cursor
		through Cursor
		records []Record
	}{
		{name: "through before after", after: CursorAt(2), through: CursorAt(1)},
		{name: "gap", after: CursorAt(2), through: CursorAt(5), records: []Record{three, five}},
		{name: "early end", after: CursorAt(2), through: CursorAt(4), records: []Record{three}},
		{name: "extra record", after: CursorAt(2), through: CursorAt(3), records: []Record{three, four}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewCompletePrefix(test.after, test.through, test.records); !errors.Is(err, ErrIncompletePrefix) {
				t.Fatalf("NewCompletePrefix error = %v, want ErrIncompletePrefix", err)
			}
		})
	}
}

func TestCompletePrefixRejectsSequenceOverflow(t *testing.T) {
	record := mustRecord(t, math.MaxUint64, "kernel.command", `{}`)
	if _, err := NewCompletePrefix(CursorAt(math.MaxUint64), CursorAt(math.MaxUint64), []Record{record}); !errors.Is(err, ErrIncompletePrefix) {
		t.Fatalf("NewCompletePrefix overflow error = %v, want ErrIncompletePrefix", err)
	}
}

func TestCompletePrefixRecordsAreImmutableCopies(t *testing.T) {
	records := []Record{{sequence: 1, typ: "kernel.command", payload: []byte(`{"z":{"b":2,"a":1}}`)}}
	prefix, err := NewCompletePrefix(CursorAt(0), CursorAt(1), records)
	if err != nil {
		t.Fatalf("NewCompletePrefix: %v", err)
	}
	nestedInput := bytes.Index(records[0].payload, []byte(`"b"`))
	if nestedInput < 0 {
		t.Fatal("test fixture has no nested key")
	}
	records[0].payload[nestedInput+1] = 'x'
	recordCopy := prefix.Records()
	nestedCopy := bytes.Index(recordCopy[0].payload, []byte(`"b"`))
	if nestedCopy < 0 {
		t.Fatal("prefix copy has no nested key")
	}
	recordCopy[0].payload[nestedCopy+1] = 'x'
	if got, want := prefix.Records()[0].Payload(), []byte(`{"z":{"a":1,"b":2}}`); !bytes.Equal(got, want) {
		t.Fatalf("prefix records changed: %s, want %s", got, want)
	}
}

func mustRecord(t *testing.T, sequence uint64, typ, payload string) Record {
	t.Helper()
	record, err := NewRecord(sequence, typ, []byte(payload))
	if err != nil {
		t.Fatalf("NewRecord(%d, %q): %v", sequence, typ, err)
	}
	return record
}
