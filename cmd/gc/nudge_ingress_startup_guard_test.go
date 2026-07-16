package main

import (
	"go/ast"
	"go/token"
	"testing"
)

func TestControllerStartupWiresAuthenticatedNudgeAdmissionAfterDurableToken(t *testing.T) {
	tests := []struct {
		file     string
		function string
	}{
		{file: "controller.go", function: "runControllerWithLease"},
		{file: "cmd_supervisor.go", function: "startManagedCity"},
	}
	for _, test := range tests {
		t.Run(test.function, func(t *testing.T) {
			fset, file := parseGCTestSource(t, test.file)
			fn := findGCFunction(t, file, test.function)
			positions := controllerNudgeStartupPositions(t, fset, fn)

			if positions.socket == token.NoPos || positions.option == token.NoPos {
				t.Fatalf("socket/admission option positions = %s/%s, want both", fset.Position(positions.socket), fset.Position(positions.option))
			}
			if positions.writeToken == token.NoPos || positions.publishToken == token.NoPos || positions.writeToken >= positions.publishToken {
				t.Fatalf("durable/published token positions = %s/%s, want durable write first", fset.Position(positions.writeToken), fset.Position(positions.publishToken))
			}
			if positions.publishRuntime == token.NoPos {
				t.Fatal("city runtime nudge authority is never published")
			}
		})
	}
}

type controllerNudgeStartupCallPositions struct {
	socket         token.Pos
	option         token.Pos
	writeToken     token.Pos
	publishToken   token.Pos
	publishRuntime token.Pos
}

func controllerNudgeStartupPositions(t *testing.T, fset *token.FileSet, fn *ast.FuncDecl) controllerNudgeStartupCallPositions {
	t.Helper()
	var positions controllerNudgeStartupCallPositions
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch calledFunction(call) {
		case "startControllerSocket":
			positions.socket = uniqueNudgeStartupPosition(t, fset, "controller socket", positions.socket, call.Pos())
		case "withControllerNudgeAdmissionTokenSource":
			positions.option = uniqueNudgeStartupPosition(t, fset, "nudge admission option", positions.option, call.Pos())
			if len(call.Args) != 2 || selectorPath(call.Args[0]) != "nudgeTokenSlot.Load" || selectorPath(call.Args[1]) != "nudgeAuthoritySlot" {
				t.Errorf("nudge admission option at %s does not use the lifecycle slots", fset.Position(call.Pos()))
			}
		case "convergence.WriteToken":
			positions.writeToken = uniqueNudgeStartupPosition(t, fset, "controller token write", positions.writeToken, call.Pos())
		case "nudgeTokenSlot.Store":
			positions.publishToken = uniqueNudgeStartupPosition(t, fset, "controller token publication", positions.publishToken, call.Pos())
		case "nudgeAuthoritySlot.Store":
			positions.publishRuntime = uniqueNudgeStartupPosition(t, fset, "runtime authority publication", positions.publishRuntime, call.Pos())
		}
		return true
	})
	return positions
}

func uniqueNudgeStartupPosition(t *testing.T, fset *token.FileSet, owner string, previous, current token.Pos) token.Pos {
	t.Helper()
	if previous != token.NoPos {
		t.Errorf("multiple %s calls at %s and %s", owner, fset.Position(previous), fset.Position(current))
	}
	return current
}
