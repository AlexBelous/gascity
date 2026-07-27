package docsync

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestLumenPackagesKeepThePrivateCompilerEdgeOutward(t *testing.T) {
	tests := []struct {
		dir       string
		forbidden map[string]bool
	}{
		{
			dir: "internal/lumen/program",
			forbidden: map[string]bool{
				"github.com/gastownhall/gascity/internal/lumen/compiler": true,
				"github.com/gastownhall/gascity/internal/lumen/ir025":    true,
			},
		},
		{
			dir: "internal/lumen/ir025",
			forbidden: map[string]bool{
				"github.com/gastownhall/gascity/internal/lumen/compiler": true,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.dir, func(t *testing.T) {
			packages, err := parser.ParseDir(
				token.NewFileSet(),
				filepath.Join(repoRoot(), test.dir),
				func(info fs.FileInfo) bool {
					return !strings.HasSuffix(info.Name(), "_test.go")
				},
				parser.ImportsOnly,
			)
			if err != nil {
				t.Fatalf("parse %s: %v", test.dir, err)
			}
			for _, pkg := range packages {
				for filename, file := range pkg.Files {
					for _, spec := range file.Imports {
						imported, err := strconv.Unquote(spec.Path.Value)
						if err != nil {
							t.Fatalf("unquote import in %s: %v", filename, err)
						}
						if test.forbidden[imported] {
							t.Errorf("%s imports forbidden package %s", filename, imported)
						}
					}
				}
			}
		})
	}
}
