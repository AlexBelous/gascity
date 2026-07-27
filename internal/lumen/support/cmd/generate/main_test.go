package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteMatrixUsesPublicReadMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "matrix.json")
	if err := writeMatrix(path, []byte("{}\n")); err != nil {
		t.Fatalf("writeMatrix: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("matrix mode = %o, want 644", got)
	}
}
