package graphstore

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSubstrateImportsStayStorageAndLumenFree(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	allowed := map[string]bool{
		"errors": true, "fmt": true, "math": true,
		"github.com/gastownhall/gascity/internal/graphstore/canon": true,
	}
	path := filepath.Join(filepath.Dir(file), "record.go")
	parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse record.go: %v", err)
	}
	for _, imported := range parsed.Imports {
		importPath := strings.Trim(imported.Path.Value, `"`)
		if !allowed[importPath] {
			t.Fatalf("record.go imports %q; the A3 substrate must stay storage- and Lumen-free", importPath)
		}
	}
}
