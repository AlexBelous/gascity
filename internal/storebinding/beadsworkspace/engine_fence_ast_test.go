package beadsworkspace

// The pinned-id fence has to survive a second way to open the workspace.
//
// When the fence landed there was one open call in this file, so fencing it
// fenced the provider. The credential-provider selector then added a second,
// and a fence carried by only one of them is worse than no fence: the binding
// still claims its namespaces, and a hosted workspace quietly accepts a
// caller-pinned id from outside them. Nothing about the shape of that bug is
// visible at the call site that stayed correct.
//
// The fence itself is unexported store state and Create only consults it
// against a live workspace, so the property is asserted where it is decided —
// on the source. What this pins is exactly the step a merge drops: an open
// that does not carry the fence, or an open that escapes the one function
// holding it.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// openWorkspaceFunc is the single function permitted to open the workspace.
const openWorkspaceFunc = "openWorkspace"

func TestEveryWorkspaceOpenCarriesThePinnedIDFence(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "engine.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parsing engine.go: %v", err)
	}

	opener := findFunc(file, openWorkspaceFunc)
	if opener == nil {
		t.Fatalf("engine.go no longer declares %s; the fence derivation moved and this guard is blind", openWorkspaceFunc)
	}

	fence := fenceIdent(opener)
	if fence == "" {
		t.Fatalf("%s does not derive a fence from storebinding.EngineReservedPrefixes; "+
			"the namespaces a binding claims follow from its assigned classes and nothing else", openWorkspaceFunc)
	}

	opens := 0
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !isWorkspaceOpen(call) {
			return true
		}
		opens++
		if !within(opener, call) {
			t.Errorf("%s: workspace opened outside %s; every open must route through the one function that holds the fence",
				fset.Position(call.Pos()), openWorkspaceFunc)
			return true
		}
		if !carries(call, fence) {
			t.Errorf("%s: workspace open does not pass the %q fence; a caller-pinned id from any namespace would land in this binding",
				fset.Position(call.Pos()), fence)
		}
		return true
	})

	if opens == 0 {
		t.Fatalf("engine.go opens no workspace; this guard asserts nothing and the fence is unproven")
	}
}

// findFunc returns the named top-level function, or nil.
func findFunc(file *ast.File, name string) *ast.FuncDecl {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}

// fenceIdent returns the local bound to storebinding.EngineReservedPrefixes via
// the store option, or "" if the function derives no fence.
func fenceIdent(fn *ast.FuncDecl) string {
	var name string
	ast.Inspect(fn, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok || !isSelector(call, "beads", "WithNativeDoltStoreReservedIDPrefixes") {
			return true
		}
		if !derivesFromAssignedClasses(call) {
			return true
		}
		if ident, ok := assign.Lhs[0].(*ast.Ident); ok {
			name = ident.Name
		}
		return false
	})
	return name
}

// derivesFromAssignedClasses reports whether the fence option is built from the
// shared class derivation rather than a prefix list written out here.
func derivesFromAssignedClasses(call *ast.CallExpr) bool {
	for _, arg := range call.Args {
		inner, ok := arg.(*ast.CallExpr)
		if ok && isSelector(inner, "storebinding", "EngineReservedPrefixes") {
			return true
		}
	}
	return false
}

// isWorkspaceOpen reports whether the call opens the native store behind a
// workspace, by any of the open variants the linked library exposes.
func isWorkspaceOpen(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "beads" {
		return false
	}
	return strings.HasPrefix(sel.Sel.Name, "OpenNativeDoltStoreAt")
}

// carries reports whether the fence local is among the call's arguments.
func carries(call *ast.CallExpr, fence string) bool {
	for _, arg := range call.Args {
		if ident, ok := arg.(*ast.Ident); ok && ident.Name == fence {
			return true
		}
	}
	return false
}

func isSelector(call *ast.CallExpr, pkg, name string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && ident.Name == pkg
}

func within(fn *ast.FuncDecl, node ast.Node) bool {
	return node.Pos() >= fn.Pos() && node.End() <= fn.End()
}
