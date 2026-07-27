package compiler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"
)

// CompileForExecution compiles one reproducibly named Lumen source file into
// untrusted private compiler evidence for the later ir025 boundary.
func CompileForExecution(ctx context.Context, sourceName string, source []byte, formulaName string) ([]byte, error) {
	if err := validateSourceName(sourceName); err != nil {
		return nil, err
	}
	if !utf8.Valid(source) {
		return nil, fmt.Errorf("lumen source %q is not valid UTF-8", sourceName)
	}
	if err := loadArtifact(); err != nil {
		return nil, err
	}
	version, err := exec.CommandContext(ctx, "node", "--version").Output()
	if err != nil {
		return nil, fmt.Errorf("read Node version: %w", err)
	}
	if strings.TrimSpace(string(version)) != nodeVersion {
		return nil, fmt.Errorf("node version = %q, want %q", strings.TrimSpace(string(version)), nodeVersion)
	}
	artifact, err := os.CreateTemp("", "gc-lumen-compiler-*.cjs")
	if err != nil {
		return nil, fmt.Errorf("create compiler artifact: %w", err)
	}
	artifactPath := artifact.Name()
	defer func() { _ = os.Remove(artifactPath) }()
	if _, err := artifact.Write(embeddedArtifact); err != nil {
		_ = artifact.Close()
		return nil, fmt.Errorf("write compiler artifact: %w", err)
	}
	if err := artifact.Close(); err != nil {
		return nil, fmt.Errorf("close compiler artifact: %w", err)
	}
	request, err := json.Marshal(struct {
		URI         string `json:"uri"`
		Text        string `json:"text"`
		FormulaName string `json:"formulaName,omitempty"`
	}{URI: sourceName, Text: string(source), FormulaName: formulaName})
	if err != nil {
		return nil, fmt.Errorf("encode compiler input: %w", err)
	}
	cmd := exec.CommandContext(ctx, "node", "-e", compilerRunner, artifactPath)
	cmd.Env = []string{}
	cmd.WaitDelay = 100 * time.Millisecond
	cmd.Stdin = bytes.NewReader(request)
	var stdout, stderr boundedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("run compiler artifact: %w", ctx.Err())
		}
		return nil, fmt.Errorf("run compiler artifact: %w: %s", err, stderr.summary())
	}
	result := stdout.Bytes()
	if err := validateCompilerResult(result); err != nil {
		return nil, err
	}
	return result, nil
}

const compilerRunner = `const compiler = require(process.argv[1]); let input = ""; process.stdin.setEncoding("utf8"); process.stdin.on("data", chunk => input += chunk); process.stdin.on("end", () => { const request = JSON.parse(input); process.stdout.write(JSON.stringify(compiler.compileForExecution({ uri: request.uri, text: request.text }, request.formulaName)) + "\n"); });`

const maxCompilerOutput = 8 << 20

type boundedBuffer struct{ bytes.Buffer }

func (b *boundedBuffer) Write(data []byte) (int, error) {
	if b.Len()+len(data) > maxCompilerOutput {
		return 0, fmt.Errorf("compiler output exceeds %d bytes", maxCompilerOutput)
	}
	return b.Buffer.Write(data)
}

func (b *boundedBuffer) summary() string {
	const maxErrorText = 4096
	if b.Len() <= maxErrorText {
		return b.String()
	}
	return b.String()[:maxErrorText] + "…"
}

func validateCompilerResult(result []byte) error {
	if len(result) < 2 || result[len(result)-1] != '\n' || bytes.Count(result, []byte("\n")) != 1 {
		return fmt.Errorf("compiler output is not compact JSON with one trailing LF")
	}
	payload := result[:len(result)-1]
	compact, err := compactJSON(payload)
	if err != nil || !bytes.Equal(payload, compact) {
		return fmt.Errorf("compiler output is not compact JSON with one trailing LF")
	}
	decoded, err := objectWithExactKeys(payload, requiredResultMembers[:]...)
	if err != nil {
		return fmt.Errorf("decode compiler output: %w", err)
	}
	if string(decoded["formula"]) == "null" {
		return fmt.Errorf("compiler selected no formula")
	}
	var diagnostics []struct {
		Severity string `json:"severity"`
	}
	if err := json.Unmarshal(decoded["diagnostics"], &diagnostics); err != nil {
		return fmt.Errorf("decode compiler diagnostics: %w", err)
	}
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == "error" {
			return fmt.Errorf("compiler reported an error diagnostic")
		}
	}
	return nil
}

var requiredResultMembers = [...]string{
	"formula", "formulas", "selfStepFormulas", "modules", "exports", "agents", "sessions", "stepDeclarations", "typeAliases", "diagnostics",
}

func validateSourceName(sourceName string) error {
	if sourceName == "" || !utf8.ValidString(sourceName) || strings.ContainsRune(sourceName, '\x00') || strings.Contains(sourceName, "\\") || strings.HasPrefix(sourceName, "/") {
		return fmt.Errorf("lumen source name %q is not a clean relative POSIX path", sourceName)
	}
	segments := strings.Split(sourceName, "/")
	if hasURIScheme(segments[0]) {
		return fmt.Errorf("lumen source name %q is not a clean relative POSIX path", sourceName)
	}
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("lumen source name %q is not a clean relative POSIX path", sourceName)
		}
	}
	return nil
}

func hasURIScheme(segment string) bool {
	colon := strings.IndexByte(segment, ':')
	if colon <= 0 || !asciiLetter(segment[0]) {
		return false
	}
	for _, character := range []byte(segment[1:colon]) {
		if !asciiLetter(character) && (character < '0' || character > '9') && character != '+' && character != '.' && character != '-' {
			return false
		}
	}
	return true
}

func asciiLetter(character byte) bool {
	return character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z'
}
