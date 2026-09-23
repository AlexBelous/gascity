package main

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/fsys"
)

// A store open that already has a composed config must not read it again
// merely to select credential transport. A later nil-config operation must
// still observe current disk state, including invalid configuration.
func TestHostedCredentialSelectionReusesOnlySuppliedConfig(t *testing.T) {
	fs := fsys.NewFake()
	city := "/cities/one"
	fs.Files[city+"/city.toml"] = []byte(hostedBeadsCityTOML("https://beads.example", "gasworks", false))
	cfg, _, err := config.LoadWithIncludes(fs, city+"/city.toml")
	if err != nil {
		t.Fatal(err)
	}
	fs.Calls = nil
	fs.Files[city+"/city.toml"] = []byte("[invalid")
	selected, err := citySelectsHostedBeadsCredentialProviderFS(fs, city, cfg)
	if err != nil || !selected {
		t.Fatalf("supplied config: selected=%v err=%v", selected, err)
	}
	if len(fs.Calls) != 0 {
		t.Fatalf("supplied config reread filesystem: %v", fs.Calls)
	}
	if _, err := citySelectsHostedBeadsCredentialProviderFS(fs, city, nil); err == nil {
		t.Fatal("fresh operation ignored invalid config")
	}
	fs.Files[city+"/city.toml"] = []byte("[workspace]\nname = \"local\"\n")
	if selected, err := citySelectsHostedBeadsCredentialProviderFS(fs, city, nil); err != nil || selected {
		t.Fatalf("fresh local config: selected=%v err=%v", selected, err)
	}
	if selected, err := citySelectsHostedBeadsCredentialProviderFS(fs, "/cities/other", nil); err != nil || selected {
		t.Fatalf("missing city: selected=%v err=%v", selected, err)
	}
	fs.Errors[city+"/city.toml"] = os.ErrPermission
	if _, err := citySelectsHostedBeadsCredentialProviderFS(fs, city, nil); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("permission failure lost: %v", err)
	}
}

// The credential selector may reuse a config only at the initial native-open
// boundary. Retained command runners and reopen callbacks must not capture it.
func TestHostedCredentialConfigReuseBoundaries(t *testing.T) {
	for _, edge := range []struct {
		file, caller, callee string
		index                int
		want                 string
	}{
		{"bd_env.go", "nativeDoltOpenEnvForScopeContext", "bdRuntimeEnvWithConfigRecoveryContext", 2, "cfg"},
		{"bd_env.go", "nativeDoltOpenEnvForScopeContext", "bdRuntimeEnvForRigWithCredentialConfigContext", 3, "cfg"},
		{"bd_env.go", "bdRuntimeEnvForRigWithErrorRecoveryContext", "bdRuntimeEnvForRigWithCredentialConfigContext", 3, "nil"},
		{"bd_env.go", "bdRuntimeEnvWithErrorRecoveryContext", "bdRuntimeEnvWithConfigRecoveryContext", 2, "nil"},
		{"bd_env.go", "bdRuntimeEnvWithConfigRecoveryContext", "applyHostedBeadsCredentialEnvWithConfig", 2, "cfg"},
		{"bd_env.go", "bdRuntimeEnvForRigWithCredentialConfigContext", "bdRuntimeEnvWithConfigRecoveryContext", 2, "credentialCfg"},
		{"main.go", "openStoreResultAtForCityWithConfig", "nativeDoltOpenEnvForScopeContext", 2, "nil"},
		{"main.go", "openStoreResultAtForCityWithConfig", "nativeDoltOneShotOpenEnvForScopeContext", 2, "nil"},
		{"api_state.go", "openRigStore", "nativeDoltOpenEnvForScopeContext", 2, "nil"},
	} {
		t.Run(edge.caller+"/"+edge.callee, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), edge.file, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			var fn *ast.FuncDecl
			for _, decl := range file.Decls {
				if candidate, ok := decl.(*ast.FuncDecl); ok && candidate.Name.Name == edge.caller {
					fn = candidate
					break
				}
			}
			if fn == nil {
				t.Fatalf("missing caller %s", edge.caller)
			}
			calls := 0
			ast.Inspect(fn, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				name, ok := call.Fun.(*ast.Ident)
				if !ok || name.Name != edge.callee {
					return true
				}
				calls++
				if len(call.Args) <= edge.index || exprText(call.Args[edge.index]) != edge.want {
					t.Errorf("%s must pass %s as credential config", edge.callee, edge.want)
				}
				return true
			})
			if calls != 1 {
				t.Fatalf("got %d calls, want 1", calls)
			}
		})
	}
}
