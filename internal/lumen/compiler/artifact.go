// Package compiler owns the pinned, compiler-only Lumen artifact boundary.
package compiler

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
)

const (
	upstreamCommit = "faff38c7a04e41815d6e63051ff126137d5820b9"
	upstreamTree   = "9ec613ec5fa92672b53c652457c832cde3ffb175"
	nodeVersion    = "v22.23.1"
	npmVersion     = "10.9.8"
	esbuildVersion = "0.27.7"
)

var resolutionInputs = [...]string{
	"compiler-entry.ts", "packages/core/src/index.ts", "packages/core/src/language.generated.ts",
	"packages/core/src/lumen-ir-contract.generated.ts", "packages/core/src/lumen-keyword-surface.ts",
	"packages/core/src/lumen-source-printer.ts", "packages/core/src/lumen-step-catalog.generated.ts",
	"packages/core/src/runtime-storage.ts",
}

var expectedSourceHashes = map[string]string{
	"packages/core/src/index.ts":                        "e0980be51afe57374d01acf7affcae0f66b890a8ca5430bfeeab0a28527b06be",
	"packages/core/src/language.generated.ts":           "7fc10ed56d78dda2b31454b33633a57f0bc9af94ef58af279d78cd47d5afeb58",
	"packages/core/src/lumen-ir-contract.generated.ts":  "530d50d9fd54616314627db95de9452baed9af4991cd8bb499c5ce53a62fb77a",
	"packages/core/src/lumen-keyword-surface.ts":        "e078ec1ba6b2c2f780c20a20550abc4d9ea2ecd2d0b403eeb96d5a7fbf8c954a",
	"packages/core/src/lumen-source-printer.ts":         "5cac74ec1fa2fe0642a05efb42d3abd466cc59e85a6b1f8a7ddd6255db3146b4",
	"packages/core/src/lumen-step-catalog.generated.ts": "6a3e31ab7645bd4c81781438a1304464334f127554e3246da14132d1733d324e",
	"packages/core/src/runtime-storage.ts":              "3eb247495dd4cd86246ac407ea7db28431a3ac38353c1610c58cf59fc860acb1",
	"package-lock.json":                                 "077161a20420b9cfaae01aa4bd97eb3cd7eb310482c81244482e927dc9be7878",
}

type artifactPackage struct {
	Version   string `json:"version"`
	Integrity string `json:"integrity"`
}

type artifactToolchain struct {
	Node struct {
		Version string `json:"version"`
	} `json:"node"`
	NPM struct {
		Version string `json:"version"`
	} `json:"npm"`
	TypeScript      artifactPackage `json:"typescript"`
	TypesNode       artifactPackage `json:"typesNode"`
	AJV             artifactPackage `json:"ajv"`
	Esbuild         artifactPackage `json:"esbuild"`
	EsbuildLinuxX64 artifactPackage `json:"esbuildLinuxX64"`
}

type artifactImage struct {
	Reference    string `json:"reference"`
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
}

type artifactSettings struct {
	Bundle        bool   `json:"bundle"`
	Format        string `json:"format"`
	Platform      string `json:"platform"`
	Target        string `json:"target"`
	TreeShaking   bool   `json:"treeShaking"`
	Minify        bool   `json:"minify"`
	Sourcemap     bool   `json:"sourcemap"`
	LegalComments string `json:"legalComments"`
	Charset       string `json:"charset"`
}

type artifactInput struct {
	BytesInOutput int `json:"bytesInOutput"`
}

type artifactDocument struct {
	Schema           string                   `json:"schema"`
	UpstreamCommit   string                   `json:"upstreamCommit"`
	UpstreamTree     string                   `json:"upstreamTree"`
	Toolchain        artifactToolchain        `json:"toolchain"`
	Image            artifactImage            `json:"image"`
	Settings         artifactSettings         `json:"settings"`
	SourceHashes     map[string]string        `json:"sourceHashes"`
	ResolutionInputs []string                 `json:"resolutionInputs"`
	OutputInputs     map[string]artifactInput `json:"outputInputs"`
	OutputImports    []json.RawMessage        `json:"outputImports"`
	ArtifactExports  []string                 `json:"artifactExports"`
	ArtifactSHA256   string                   `json:"artifactSHA256"`
	OutputTreeSHA256 string                   `json:"outputTreeSHA256"`
}

//go:embed artifact/compiler.js
var embeddedArtifact []byte

//go:embed artifact/manifest.json
var embeddedManifest []byte

func loadArtifact() error {
	if err := validateCompactManifest(embeddedManifest); err != nil {
		return err
	}
	var document artifactDocument
	decoder := json.NewDecoder(bytes.NewReader(embeddedManifest))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return fmt.Errorf("decode Lumen compiler manifest: %w", err)
	}
	return validateArtifact(document, embeddedArtifact)
}

func validateCompactManifest(manifest []byte) error {
	if len(manifest) == 0 || manifest[len(manifest)-1] != '\n' || bytes.Count(manifest, []byte("\n")) != 1 {
		return fmt.Errorf("lumen compiler manifest must be compact JSON with one trailing LF")
	}
	compact, err := compactJSON(manifest[:len(manifest)-1])
	if err != nil || !bytes.Equal(compact, manifest[:len(manifest)-1]) {
		return fmt.Errorf("lumen compiler manifest must be compact JSON with one trailing LF")
	}
	root, err := objectWithExactKeys(manifest, "schema", "upstreamCommit", "upstreamTree", "toolchain", "image", "settings", "sourceHashes", "resolutionInputs", "outputInputs", "outputImports", "artifactExports", "artifactSHA256", "outputTreeSHA256")
	if err != nil {
		return fmt.Errorf("lumen compiler manifest shape: %w", err)
	}
	if err := validateManifestObjects(root); err != nil {
		return fmt.Errorf("lumen compiler manifest shape: %w", err)
	}
	if !bytes.Equal(root["outputImports"], []byte("[]")) {
		return fmt.Errorf("lumen compiler manifest output imports must be exactly []")
	}
	return nil
}

func compactJSON(data []byte) ([]byte, error) {
	var compact bytes.Buffer
	if err := json.Compact(&compact, data); err != nil {
		return nil, err
	}
	return compact.Bytes(), nil
}

func objectWithExactKeys(data []byte, keys ...string) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	opening, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := opening.(json.Delim); !ok || delimiter != '{' {
		return nil, fmt.Errorf("expected an object")
	}
	object := make(map[string]json.RawMessage, len(keys))
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("expected an object key")
		}
		if _, duplicate := object[key]; duplicate {
			return nil, fmt.Errorf("duplicate member %q", key)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		object[key] = value
	}
	closing, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := closing.(json.Delim); !ok || delimiter != '}' {
		return nil, fmt.Errorf("expected object end")
	}
	if token, err := decoder.Token(); err != io.EOF || token != nil {
		return nil, fmt.Errorf("unexpected data after object")
	}
	if len(object) != len(keys) {
		return nil, fmt.Errorf("member count = %d, want %d", len(object), len(keys))
	}
	for _, key := range keys {
		if _, ok := object[key]; !ok {
			return nil, fmt.Errorf("missing member %q", key)
		}
	}
	return object, nil
}

func validateManifestObjects(root map[string]json.RawMessage) error {
	toolchain, err := objectWithExactKeys(root["toolchain"], "node", "npm", "typescript", "typesNode", "ajv", "esbuild", "esbuildLinuxX64")
	if err != nil {
		return err
	}
	if _, err := objectWithExactKeys(toolchain["node"], "version"); err != nil {
		return fmt.Errorf("node: %w", err)
	}
	if _, err := objectWithExactKeys(toolchain["npm"], "version"); err != nil {
		return fmt.Errorf("npm: %w", err)
	}
	for _, key := range []string{"typescript", "typesNode", "ajv", "esbuild", "esbuildLinuxX64"} {
		if _, err := objectWithExactKeys(toolchain[key], "version", "integrity"); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}
	if _, err := objectWithExactKeys(root["image"], "reference", "os", "architecture"); err != nil {
		return fmt.Errorf("image: %w", err)
	}
	if _, err := objectWithExactKeys(root["settings"], "bundle", "format", "platform", "target", "treeShaking", "minify", "sourcemap", "legalComments", "charset"); err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	if _, err := objectWithExactKeys(root["sourceHashes"], mapKeys(expectedSourceHashes)...); err != nil {
		return fmt.Errorf("source hashes: %w", err)
	}
	outputInputs, err := objectWithExactKeys(root["outputInputs"], resolutionInputs[:]...)
	if err != nil {
		return fmt.Errorf("output inputs: %w", err)
	}
	for _, input := range resolutionInputs {
		if _, err := objectWithExactKeys(outputInputs[input], "bytesInOutput"); err != nil {
			return fmt.Errorf("output input %s: %w", input, err)
		}
	}
	return nil
}

func mapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func validateArtifact(document artifactDocument, artifact []byte) error {
	if err := validateManifest(document); err != nil {
		return err
	}
	if got := hashArtifact(artifact); got != document.ArtifactSHA256 {
		return fmt.Errorf("lumen compiler artifact hash = %s, want %s", got, document.ArtifactSHA256)
	}
	if got, want := hashArtifact([]byte("compiler.js\x00"+document.ArtifactSHA256+"\n")), document.OutputTreeSHA256; got != want {
		return fmt.Errorf("lumen compiler artifact tree hash = %s, want %s", got, want)
	}
	for _, symbol := range []string{"runLumen", "LumenFileRuntimeStorage", "LumenSqliteRuntimeStorage", "exports.parseLumen", "exports.runLumen"} {
		if bytes.Contains(artifact, []byte(symbol)) {
			return fmt.Errorf("lumen compiler artifact contains forbidden symbol %q", symbol)
		}
	}
	return nil
}

func validateManifest(document artifactDocument) error {
	if document.Schema != "gascity.lumen.compiler-artifact/v1" || document.UpstreamCommit != upstreamCommit || document.UpstreamTree != upstreamTree {
		return fmt.Errorf("lumen compiler artifact upstream identity does not match the pin")
	}
	if document.Toolchain.Node.Version != nodeVersion || document.Toolchain.NPM.Version != npmVersion ||
		document.Toolchain.TypeScript != (artifactPackage{Version: "5.9.3", Integrity: "sha512-jl1vZzPDinLr9eUt3J/t7V6FgNEw9QjvBPdysz9KfQDD41fQrC2Y4vKQdiaUpFT4bXlb1RHhLpp8wtm6M5TgSw=="}) ||
		document.Toolchain.TypesNode != (artifactPackage{Version: "22.19.19", Integrity: "sha512-dyh/xO2Fh5bYrfWaaqGrRQQGkNdmYw6AmaAUvYeUMNTWQtvb796ikLdmTchRmOlOiIJ1TDXfWgVx1QkUlQ6Hew=="}) ||
		document.Toolchain.AJV != (artifactPackage{Version: "8.20.0", Integrity: "sha512-Thbli+OlOj+iMPYFBVBfJ3OmCAnaSyNn4M1vz9T6Gka5Jt9ba/HIR56joy65tY6kx/FCF5VXNB819Y7/GUrBGA=="}) ||
		document.Toolchain.Esbuild != (artifactPackage{Version: esbuildVersion, Integrity: "sha512-IxpibTjyVnmrIQo5aqNpCgoACA/dTKLTlhMHihVHhdkxKyPO1uBBthumT0rdHmcsk9uMonIWS0m4FljWzILh3w=="}) ||
		document.Toolchain.EsbuildLinuxX64 != (artifactPackage{Version: esbuildVersion, Integrity: "sha512-hzznmADPt+OmsYzw1EE33ccA+HPdIqiCRq7cQeL1Jlq2gb1+OyWBkMCrYGBJ+sxVzve2ZJEVeePbLM2iEIZSxA=="}) {
		return fmt.Errorf("lumen compiler artifact toolchain does not match the pinned build")
	}
	if document.Image != (artifactImage{Reference: "node:22.23.1-bookworm-slim@sha256:8607a9064d4a571140998ae9e52a3b3fcf9cff361d04642d5971e6cd76d39e27", OS: "linux", Architecture: "amd64"}) {
		return fmt.Errorf("lumen compiler artifact image does not match the pinned build")
	}
	if document.Settings != (artifactSettings{Bundle: true, Format: "cjs", Platform: "node", Target: "node22", TreeShaking: true, Minify: false, Sourcemap: false, LegalComments: "none", Charset: "utf8"}) {
		return fmt.Errorf("lumen compiler artifact settings do not match the pinned build")
	}
	if !sameStrings(document.ResolutionInputs, resolutionInputs[:]) || !sameStringMap(document.SourceHashes, expectedSourceHashes) {
		return fmt.Errorf("lumen compiler artifact source closure does not match the pin")
	}
	if !sameStrings(document.ArtifactExports, []string{"compileForExecution"}) {
		return fmt.Errorf("lumen compiler artifact exports do not match the compiler-only boundary")
	}
	if document.OutputImports == nil || len(document.OutputImports) != 0 {
		return fmt.Errorf("lumen compiler artifact has output imports")
	}
	for _, input := range resolutionInputs {
		output, ok := document.OutputInputs[input]
		if !ok {
			return fmt.Errorf("lumen compiler artifact output is missing input %q", input)
		}
		switch input {
		case "packages/core/src/language.generated.ts", "packages/core/src/lumen-source-printer.ts", "packages/core/src/runtime-storage.ts":
			if output.BytesInOutput != 0 {
				return fmt.Errorf("lumen compiler artifact emits forbidden input %q", input)
			}
		default:
			if output.BytesInOutput <= 0 {
				return fmt.Errorf("lumen compiler artifact does not emit required input %q", input)
			}
		}
	}
	return nil
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range want {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func sameStringMap(got, want map[string]string) bool {
	if len(got) != len(want) {
		return false
	}
	for key, wantValue := range want {
		if got[key] != wantValue {
			return false
		}
	}
	return true
}

func hashArtifact(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
