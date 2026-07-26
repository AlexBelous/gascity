package doltorphan

import (
	"encoding/binary"
	"slices"
	"testing"
)

func TestParseDarwinProcArgsPreservesArgvBoundaries(t *testing.T) {
	raw := make([]byte, 4)
	binary.LittleEndian.PutUint32(raw, 4)
	raw = append(raw,
		[]byte("/usr/local/bin/dolt\x00\x00\x00dolt\x00sql-server\x00--data-dir\x00/tmp/data dir with spaces\x00IGNORED=value\x00")...,
	)

	got, err := parseDarwinProcArgs(raw)
	if err != nil {
		t.Fatalf("parseDarwinProcArgs: %v", err)
	}
	want := []string{"dolt", "sql-server", "--data-dir", "/tmp/data dir with spaces"}
	if !slices.Equal(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func TestParseDarwinProcArgsRejectsMalformedBuffers(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
	}{
		{name: "missing argc", raw: []byte{1, 2, 3}},
		{name: "missing executable terminator", raw: append([]byte{1, 0, 0, 0}, []byte("/usr/bin/dolt")...)},
		{name: "missing argv", raw: append([]byte{1, 0, 0, 0}, []byte("/usr/bin/dolt\x00\x00")...)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseDarwinProcArgs(tt.raw); err == nil {
				t.Fatal("parseDarwinProcArgs succeeded for malformed input")
			}
		})
	}
}
