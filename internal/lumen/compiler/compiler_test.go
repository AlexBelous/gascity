package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const completeCompilerResult = `{"formula":{},"formulas":[],"selfStepFormulas":[],"modules":[],"exports":[],"agents":[],"sessions":[],"stepDeclarations":[],"typeAliases":[],"diagnostics":[]}` + "\n"

func fakeNode(t *testing.T, script string) {
	t.Helper()
	bin := t.TempDir()
	path := filepath.Join(bin, "node")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake node: %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestCompileForExecutionRejectsNonReproducibleSourceName(t *testing.T) {
	for _, sourceName := range []string{"", "/absolute.lumen", "../parent.lumen", "dir/../file.lumen", "dir/./file.lumen", ".", "dir\\file.lumen", "file://source.lumen", "file:source.lumen", "bad\x00name.lumen", string([]byte{0xff})} {
		t.Run(sourceName, func(t *testing.T) {
			if _, err := CompileForExecution(context.Background(), sourceName, []byte("formula f:\n  complete null\n"), ""); err == nil {
				t.Fatal("CompileForExecution accepted an unreproducible source name")
			}
		})
	}
}

func TestValidateSourceNamePreservesLegalColonAfterSlash(t *testing.T) {
	if err := validateSourceName("examples/review:quorum.lumen"); err != nil {
		t.Fatalf("validateSourceName rejected a legal reproducible source name: %v", err)
	}
}

func TestValidateSourceNameRecognizesOnlyURISchemes(t *testing.T) {
	if err := validateSourceName("1:review.lumen"); err != nil {
		t.Fatalf("validateSourceName rejected a non-scheme first segment: %v", err)
	}
	if err := validateSourceName("a+1.-:review.lumen"); err == nil {
		t.Fatal("validateSourceName accepted an ASCII URI scheme")
	}
}

func TestCompileForExecutionUsesPinnedArtifactOffline(t *testing.T) {
	nodeBin := "/home/ubuntu/.nvm/versions/node/v22.23.1/bin"
	if _, err := os.Stat(filepath.Join(nodeBin, "node")); err != nil {
		t.Skipf("pinned Node is unavailable: %v", err)
	}
	t.Setenv("PATH", nodeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	output, err := CompileForExecution(context.Background(), "review.lumen", []byte("formula review() {\n  prompt hello\n}\n"), "")
	if err != nil {
		t.Fatalf("CompileForExecution: %v", err)
	}
	var result struct {
		Formula  json.RawMessage   `json:"formula"`
		Formulas []json.RawMessage `json:"formulas"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if len(result.Formula) == 0 || len(result.Formulas) != 1 {
		t.Fatalf("compiler output does not carry the full compile closure: %s", output)
	}
	var evidence any
	if err := json.Unmarshal(output, &evidence); err != nil {
		t.Fatalf("decode compiler evidence: %v", err)
	}
	if origins := assertLogicalOrigins(t, evidence, "review.lumen"); origins == 0 {
		t.Fatal("compiler evidence contained no origins")
	}
}

func TestCompileForExecutionRejectsAuthoredParseLumen(t *testing.T) {
	nodeBin := "/home/ubuntu/.nvm/versions/node/v22.23.1/bin"
	if _, err := os.Stat(filepath.Join(nodeBin, "node")); err != nil {
		t.Skipf("pinned Node is unavailable: %v", err)
	}
	t.Setenv("PATH", nodeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := CompileForExecution(context.Background(), "unsupported.lumen", []byte("formula main() {\n  parseLumen \\\"x\\\"\n}\n"), ""); err == nil {
		t.Fatal("CompileForExecution accepted authored parseLumen")
	}
}

func TestCompileForExecutionFailsBeforeRunnerForInvalidSourceName(t *testing.T) {
	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "runner-used")
	fakeNode := filepath.Join(bin, "node")
	if err := os.WriteFile(fakeNode, []byte("#!/bin/sh\ntouch \"$MARKER\"\n"), 0o755); err != nil {
		t.Fatalf("write fake node: %v", err)
	}
	t.Setenv("MARKER", marker)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := CompileForExecution(context.Background(), "../invalid.lumen", []byte("formula main() {}"), ""); err == nil {
		t.Fatal("CompileForExecution accepted invalid source name")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("invalid source reached the compiler runner: %v", err)
	}
}

func TestCompileForExecutionRejectsWrongNodeVersion(t *testing.T) {
	fakeNode(t, "#!/bin/sh\necho v0.0.0\n")
	if _, err := CompileForExecution(context.Background(), "main.lumen", []byte("formula main() {}"), ""); err == nil {
		t.Fatal("CompileForExecution accepted a wrong Node version")
	}
}

func TestCompileForExecutionRejectsMalformedRunnerOutput(t *testing.T) {
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo v22.23.1; exit 0; fi\necho not-json\n"
	fakeNode(t, script)
	if _, err := CompileForExecution(context.Background(), "main.lumen", []byte("formula main() {}"), ""); err == nil {
		t.Fatal("CompileForExecution accepted malformed compiler output")
	}
}

func TestValidateCompilerResultRequiresTheFullCompileClosure(t *testing.T) {
	if err := validateCompilerResult([]byte(`{"formula":{},"diagnostics":[]}` + "\n")); err == nil {
		t.Fatal("validateCompilerResult accepted a partial compile result")
	}
}

func TestValidateCompilerResultRejectsNonCanonicalFraming(t *testing.T) {
	result := []byte(` {"formula":{},"formulas":[],"selfStepFormulas":[],"modules":[],"exports":[],"agents":[],"sessions":[],"stepDeclarations":[],"typeAliases":[],"diagnostics":[]}` + "\n")
	if err := validateCompilerResult(result); err == nil {
		t.Fatal("validateCompilerResult accepted whitespace outside the JSON frame")
	}
}

func TestValidateCompilerResultRequiresExactlyOneTrailingLF(t *testing.T) {
	for name, result := range map[string][]byte{
		"missing":  []byte(strings.TrimSuffix(completeCompilerResult, "\n")),
		"extra":    []byte(completeCompilerResult + "\n"),
		"prefixed": []byte("\n" + completeCompilerResult),
		"second":   []byte(completeCompilerResult + completeCompilerResult),
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateCompilerResult(result); err == nil {
				t.Fatal("validateCompilerResult accepted invalid framing")
			}
		})
	}
}

func TestValidateCompilerResultRequiresEveryMemberAndRejectsDuplicates(t *testing.T) {
	var complete map[string]json.RawMessage
	if err := json.Unmarshal([]byte(completeCompilerResult), &complete); err != nil {
		t.Fatalf("decode complete result: %v", err)
	}
	for member := range complete {
		t.Run("missing "+member, func(t *testing.T) {
			partial := make(map[string]json.RawMessage, len(complete)-1)
			for key, value := range complete {
				if key != member {
					partial[key] = value
				}
			}
			data, err := json.Marshal(partial)
			if err != nil {
				t.Fatalf("encode partial result: %v", err)
			}
			if err := validateCompilerResult(append(data, '\n')); err == nil {
				t.Fatalf("validateCompilerResult accepted missing member %q", member)
			}
		})
	}
	data := strings.TrimSuffix(completeCompilerResult, "\n")
	duplicated := strings.Replace(data, `"formulas":[]`, `"formulas":[],"formulas":[]`, 1) + "\n"
	if err := validateCompilerResult([]byte(duplicated)); err == nil {
		t.Fatal("validateCompilerResult accepted a duplicate member")
	}
	withExtra := strings.TrimSuffix(completeCompilerResult, "\n")
	withExtra = strings.TrimSuffix(withExtra, "}") + `,"extra":null}` + "\n"
	if err := validateCompilerResult([]byte(withExtra)); err == nil {
		t.Fatal("validateCompilerResult accepted an extra member")
	}
}

func assertLogicalOrigins(t *testing.T, value any, sourceName string) int {
	t.Helper()
	switch typed := value.(type) {
	case []any:
		count := 0
		for _, item := range typed {
			count += assertLogicalOrigins(t, item, sourceName)
		}
		return count
	case map[string]any:
		count := 0
		if origin, ok := typed["origin"]; ok {
			originObject, ok := origin.(map[string]any)
			if !ok {
				t.Fatalf("origin = %T, want object", origin)
			}
			uri, ok := originObject["uri"].(string)
			if !ok || uri != sourceName {
				t.Fatalf("origin URI = %q, want exact logical source %q", uri, sourceName)
			}
			count++
		}
		for _, item := range typed {
			count += assertLogicalOrigins(t, item, sourceName)
		}
		return count
	default:
		return 0
	}
}

func TestCompileForExecutionPreservesTheCanonicalSourceOrigin(t *testing.T) {
	request := filepath.Join(t.TempDir(), "request.json")
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo v22.23.1; exit 0; fi\ncat > %q\nprintf '%%s' '%s'\n", request, completeCompilerResult)
	fakeNode(t, script)
	if _, err := CompileForExecution(context.Background(), "examples/review:quorum.lumen", []byte("formula review() {}"), "review"); err != nil {
		t.Fatalf("CompileForExecution: %v", err)
	}
	var got struct {
		URI string `json:"uri"`
	}
	data, err := os.ReadFile(request)
	if err != nil {
		t.Fatalf("read compiler request: %v", err)
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode compiler request: %v", err)
	}
	if got.URI != "examples/review:quorum.lumen" {
		t.Fatalf("compiler URI = %q, want exact caller origin", got.URI)
	}
}

func TestCompileForExecutionFailsClosedForCompilerEvidence(t *testing.T) {
	for name, result := range map[string]string{
		"no selected formula": `{"formula":null,"formulas":[],"selfStepFormulas":[],"modules":[],"exports":[],"agents":[],"sessions":[],"stepDeclarations":[],"typeAliases":[],"diagnostics":[]}`,
		"error diagnostic":    `{"formula":{},"formulas":[],"selfStepFormulas":[],"modules":[],"exports":[],"agents":[],"sessions":[],"stepDeclarations":[],"typeAliases":[],"diagnostics":[{"severity":"error"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			fakeNode(t, fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo v22.23.1; exit 0; fi\nprintf '%%s\\n' '%s'\n", result))
			output, err := CompileForExecution(context.Background(), "main.lumen", []byte("formula main() {}"), "")
			if err == nil || output != nil {
				t.Fatalf("CompileForExecution = (%q, %v), want nil bytes and an error", output, err)
			}
		})
	}
}

func TestCompileForExecutionBoundsRunnerOutputAndCancellation(t *testing.T) {
	t.Run("stdout", func(t *testing.T) {
		fakeNode(t, "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo v22.23.1; exit 0; fi\ndd if=/dev/zero bs=1048577 count=8 2>/dev/null\n")
		if output, err := CompileForExecution(context.Background(), "main.lumen", []byte("formula main() {}"), ""); err == nil || output != nil {
			t.Fatalf("CompileForExecution = (%d bytes, %v), want bounded-output error", len(output), err)
		}
	})
	t.Run("stderr", func(t *testing.T) {
		fakeNode(t, "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo v22.23.1; exit 0; fi\ndd if=/dev/zero bs=1048577 count=8 2>/dev/null >&2\n")
		if output, err := CompileForExecution(context.Background(), "main.lumen", []byte("formula main() {}"), ""); err == nil || output != nil {
			t.Fatalf("CompileForExecution = (%d bytes, %v), want bounded-output error", len(output), err)
		}
	})
	t.Run("canceled", func(t *testing.T) {
		fakeNode(t, "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo v22.23.1; exit 0; fi\nsleep 5\n")
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		started := time.Now()
		if output, err := CompileForExecution(ctx, "main.lumen", []byte("formula main() {}"), ""); err == nil || output != nil {
			t.Fatalf("CompileForExecution = (%d bytes, %v), want cancellation", len(output), err)
		}
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Fatalf("CompileForExecution returned after %s, child was not killed promptly", elapsed)
		}
	})
}
