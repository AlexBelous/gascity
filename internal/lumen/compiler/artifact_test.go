package compiler

import (
	"bytes"
	"encoding/json"
	"testing"
)

func embeddedDocument(t *testing.T) artifactDocument {
	t.Helper()
	var document artifactDocument
	if err := json.Unmarshal(embeddedManifest, &document); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	return document
}

func TestEmbeddedArtifactMatchesThePinnedCompilerBoundary(t *testing.T) {
	if err := loadArtifact(); err != nil {
		t.Fatalf("loadArtifact: %v", err)
	}
}

func TestArtifactManifestRejectsPinnedIdentityAndClosureDrift(t *testing.T) {
	document := embeddedDocument(t)
	document.UpstreamTree = "wrong"
	if err := validateArtifact(document, embeddedArtifact); err == nil {
		t.Fatal("validateArtifact accepted upstream tree drift")
	}

	document = embeddedDocument(t)
	document.SourceHashes["package-lock.json"] = "wrong"
	if err := validateArtifact(document, embeddedArtifact); err == nil {
		t.Fatal("validateArtifact accepted source lock drift")
	}

	document = embeddedDocument(t)
	document.OutputInputs["packages/core/src/runtime-storage.ts"] = artifactInput{BytesInOutput: 1}
	if err := validateArtifact(document, embeddedArtifact); err == nil {
		t.Fatal("validateArtifact accepted runtime storage in compiler output")
	}

	document = embeddedDocument(t)
	document.OutputImports = []json.RawMessage{json.RawMessage(`{"path":"runtime"}`)}
	if err := validateArtifact(document, embeddedArtifact); err == nil {
		t.Fatal("validateArtifact accepted an output metafile import")
	}

	document = embeddedDocument(t)
	document.OutputImports = nil
	if err := validateArtifact(document, embeddedArtifact); err == nil {
		t.Fatal("validateArtifact accepted null output metafile imports")
	}
}

func TestArtifactManifestRequiresCompactOneLineJSON(t *testing.T) {
	if err := validateCompactManifest(append(append([]byte(nil), embeddedManifest...), '\n')); err == nil {
		t.Fatal("validateCompactManifest accepted an additional line")
	}
	pretty, err := json.MarshalIndent(embeddedDocument(t), "", "  ")
	if err != nil {
		t.Fatalf("marshal indented manifest: %v", err)
	}
	if err := validateCompactManifest(append(pretty, '\n')); err == nil {
		t.Fatal("validateCompactManifest accepted non-compact JSON")
	}
}

func TestArtifactManifestRejectsDuplicateMembers(t *testing.T) {
	duplicate := bytes.Replace(embeddedManifest, []byte(`"schema":"gascity.lumen.compiler-artifact/v1",`), []byte(`"schema":"gascity.lumen.compiler-artifact/v1","schema":"gascity.lumen.compiler-artifact/v1",`), 1)
	if err := validateCompactManifest(duplicate); err == nil {
		t.Fatal("validateCompactManifest accepted a duplicate root member")
	}
	nested := bytes.Replace(embeddedManifest, []byte(`"node":{"version":"v22.23.1"}`), []byte(`"node":{"version":"v22.23.1","version":"v22.23.1"}`), 1)
	if err := validateCompactManifest(nested); err == nil {
		t.Fatal("validateCompactManifest accepted a duplicate nested member")
	}
}

func TestEmbeddedArtifactRejectsHashDrift(t *testing.T) {
	document := embeddedDocument(t)
	document.ArtifactSHA256 = "wrong"
	if err := validateArtifact(document, embeddedArtifact); err == nil {
		t.Fatal("validateArtifact accepted artifact hash drift")
	}
}
