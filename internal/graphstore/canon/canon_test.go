package canon

import (
	"bytes"
	"testing"
)

func TestCanonicalizeIsByteStableAcrossMapOrdering(t *testing.T) {
	first, err := Canonicalize([]byte(`{"z":{"b":2,"a":1},"a":[3,2,1]}`))
	if err != nil {
		t.Fatalf("canonicalize first: %v", err)
	}
	second, err := Canonicalize([]byte(` { "a" : [ 3, 2, 1 ], "z" : { "a" : 1, "b" : 2 } } `))
	if err != nil {
		t.Fatalf("canonicalize second: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("canonical bytes differ: %q vs %q", first, second)
	}
	if got, want := string(first), `{"a":[3,2,1],"z":{"a":1,"b":2}}`; got != want {
		t.Fatalf("canonical bytes = %q, want %q", got, want)
	}
}

func TestCanonicalizeRejectsDuplicateObjectFields(t *testing.T) {
	for _, input := range []string{
		`{"type":"one","type":"two"}`,
		`{"payload":{"key":1,"key":2}}`,
	} {
		if _, err := Canonicalize([]byte(input)); err == nil {
			t.Fatalf("Canonicalize(%s) succeeded, want duplicate-field error", input)
		}
	}
}

func TestCanonicalizeRejectsInvalidUTF8BeforeItCanCollide(t *testing.T) {
	invalid := []byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}
	if _, err := Canonicalize(invalid); err == nil {
		t.Fatal("Canonicalize accepted invalid UTF-8")
	}
}

func TestCanonicalizeRejectsLoneUTF16SurrogatesWithoutReplacementCollision(t *testing.T) {
	literalReplacement, err := Canonicalize([]byte(`{"x":"�"}`))
	if err != nil {
		t.Fatalf("canonicalize literal replacement: %v", err)
	}
	for _, input := range []string{
		`{"x":"\ud800"}`,
		`{"x":"\udc00"}`,
	} {
		if got, err := Canonicalize([]byte(input)); err == nil {
			t.Fatalf("Canonicalize(%s) = %s, want lone-surrogate error instead of collision with %s", input, got, literalReplacement)
		}
	}

	pair, err := Canonicalize([]byte(`{"x":"\ud83d\ude00"}`))
	if err != nil {
		t.Fatalf("canonicalize valid surrogate pair: %v", err)
	}
	if got, want := string(pair), `{"x":"😀"}`; got != want {
		t.Fatalf("surrogate pair canonical bytes = %s, want %s", got, want)
	}
}

func TestCanonicalizeIsIdempotentAndNormalizesNumbers(t *testing.T) {
	got, err := Canonicalize([]byte(`{"z":-0.0,"f":1.50,"e":1e2}`))
	if err != nil {
		t.Fatalf("Canonicalize: %v", err)
	}
	if want := `{"e":100,"f":1.5,"z":0}`; string(got) != want {
		t.Fatalf("canonical numbers = %s, want %s", got, want)
	}
	again, err := Canonicalize(got)
	if err != nil {
		t.Fatalf("Canonicalize canonical bytes: %v", err)
	}
	if !bytes.Equal(got, again) {
		t.Fatalf("canonicalization is not idempotent: %s vs %s", got, again)
	}
}

func TestCanonicalizeRejectsTrailingDataAndNonFiniteNumbers(t *testing.T) {
	for _, input := range [][]byte{
		[]byte(`{"x":1} trailing`),
		[]byte(`{"x":1e400}`),
	} {
		if _, err := Canonicalize(input); err == nil {
			t.Fatalf("Canonicalize(%q) succeeded, want error", input)
		}
	}
}

func TestHashIsInvariantForEquivalentCanonicalPayloads(t *testing.T) {
	first, err := Canonicalize([]byte(`{"z":2,"a":1}`))
	if err != nil {
		t.Fatalf("canonicalize first: %v", err)
	}
	second, err := Canonicalize([]byte(` { "a" : 1, "z" : 2 } `))
	if err != nil {
		t.Fatalf("canonicalize second: %v", err)
	}
	if Hash(first) != Hash(second) {
		t.Fatalf("hashes differ: %x vs %x", Hash(first), Hash(second))
	}
}
