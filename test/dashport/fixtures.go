//go:build integration

package dashport_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/api"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/sessionlog"
	"github.com/gastownhall/gascity/test/dashport/corpus"
)

// The corpus package is the single source of truth for the seeded scenario;
// these aliases keep the projection assertions reading short local names while
// the ids/values live in exactly one place (shared with the Playwright fake
// supervisor).
const (
	corpusCityName     = corpus.CityName
	corpusRigName      = corpus.RigName
	anchorRunID        = corpus.AnchorRunID
	anchorStepID       = corpus.AnchorStepID
	anchorFormula      = corpus.AnchorFormula
	corpusWorkBeadID   = corpus.WorkBeadID
	corpusWorkBeadName = corpus.WorkBeadTitle
	corpusMailSubject  = corpus.MailSubject

	// The transcript session is seeded test-side (it needs a live
	// session.Manager driven by *testing.T), so its well-known ids live here,
	// not in the corpus package.
	transcriptInitialUserID      = "transcript-user-1"
	transcriptInitialAssistantID = "transcript-assistant-1"
	transcriptInitialAnswer      = "Initial structured answer"
	transcriptAppendedUserID     = "transcript-user-2"
	transcriptAppendedPrompt     = "Appended structured prompt"
)

// fixtures is the loaded, seeded corpus plus the session-transcript state a
// test drives. The corpus half is shared with the Playwright fake supervisor
// via test/dashport/corpus; the session half exists only in-process because it
// requires a live session.Manager over the seeded store.
type fixtures struct {
	*corpus.Fixtures

	sessionProvider *runtime.Fake
	sessionManager  *session.Manager
	sessionID       string
	transcriptPath  string
}

type claudeTranscriptMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type claudeTranscriptEntry struct {
	UUID       string                  `json:"uuid"`
	ParentUUID string                  `json:"parentUuid"`
	Type       string                  `json:"type"`
	Timestamp  string                  `json:"timestamp"`
	SessionID  string                  `json:"sessionId,omitempty"`
	CWD        string                  `json:"cwd,omitempty"`
	Message    claudeTranscriptMessage `json:"message"`
}

// loadFixtures seeds a city from testdata/dashport via the shared corpus
// loader, layers a live transcript session on top, and registers cleanup on t.
// The seeding logic lives once in test/dashport/corpus so the same seeded state
// backs both this serve-level test (Layer A) and the Playwright fake supervisor
// (Layer B). A load error fails the test rather than returning, preserving the
// previous t.Fatal behavior.
func loadFixtures(t *testing.T) *fixtures {
	t.Helper()

	cityPath := t.TempDir()
	fx, err := corpus.Load(corpusDataDir(t), cityPath)
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}
	t.Cleanup(func() { _ = fx.Close() })

	sessionProvider, sessionManager, sessionID, transcriptPath := seedTranscriptSession(t, fx.CityStore, cityPath, corpus.TranscriptRoot(cityPath))

	return &fixtures{
		Fixtures: fx,

		sessionProvider: sessionProvider,
		sessionManager:  sessionManager,
		sessionID:       sessionID,
		transcriptPath:  transcriptPath,
	}
}

// corpusDataDir resolves the testdata/dashport directory relative to this test
// package's working directory (the package dir under `go test`).
func corpusDataDir(t *testing.T) string {
	t.Helper()
	return "testdata/dashport"
}

func seedTranscriptSession(t *testing.T, store beads.Store, cityPath, transcriptRoot string) (*runtime.Fake, *session.Manager, string, string) {
	t.Helper()
	provider := runtime.NewFake()
	manager := session.NewManagerWithOptions(store, provider)
	workDir := filepath.Join(cityPath, "rigs", corpusRigName)
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("create transcript workdir: %v", err)
	}
	info, err := manager.CreateSession(context.Background(), session.CreateOptions{
		Template: "demo/builder",
		Title:    "Structured transcript",
		Command:  "claude",
		WorkDir:  workDir,
		Provider: "claude",
		Resume: session.ProviderResume{
			ResumeFlag:    "--resume",
			ResumeStyle:   "flag",
			SessionIDFlag: "--session-id",
		},
		Hints:     runtime.Config{},
		ExtraMeta: map[string]string{"session_origin": "manual"},
	})
	if err != nil {
		t.Fatalf("create transcript session: %v", err)
	}

	transcriptDir := filepath.Join(transcriptRoot, sessionlog.ProjectSlug(workDir))
	if err := os.MkdirAll(transcriptDir, 0o755); err != nil {
		t.Fatalf("create transcript directory: %v", err)
	}
	transcriptPath := filepath.Join(transcriptDir, info.SessionKey+".jsonl")
	writeClaudeTranscript(t, transcriptPath,
		claudeTranscriptEntry{
			UUID:      transcriptInitialUserID,
			Type:      "user",
			Timestamp: "2026-07-14T00:00:00Z",
			SessionID: info.SessionKey,
			CWD:       workDir,
			Message:   claudeTranscriptMessage{Role: "user", Content: "Inspect transcript enrichment"},
		},
		claudeTranscriptEntry{
			UUID:       transcriptInitialAssistantID,
			ParentUUID: transcriptInitialUserID,
			Type:       "assistant",
			Timestamp:  "2026-07-14T00:00:01Z",
			SessionID:  info.SessionKey,
			CWD:        workDir,
			Message:    claudeTranscriptMessage{Role: "assistant", Content: transcriptInitialAnswer},
		},
	)
	return provider, manager, info.ID, transcriptPath
}

func writeClaudeTranscript(t *testing.T, path string, entries ...claudeTranscriptEntry) {
	t.Helper()
	var payload bytes.Buffer
	encoder := json.NewEncoder(&payload)
	for _, entry := range entries {
		if err := encoder.Encode(entry); err != nil {
			t.Fatalf("encode transcript entry %q: %v", entry.UUID, err)
		}
	}
	if err := os.WriteFile(path, payload.Bytes(), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
}

func appendClaudeTranscript(t *testing.T, path string, entry claudeTranscriptEntry) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open transcript for append: %v", err)
	}
	if err := json.NewEncoder(file).Encode(entry); err != nil {
		_ = file.Close()
		t.Fatalf("append transcript entry %q: %v", entry.UUID, err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close appended transcript: %v", err)
	}
}

// serveSeededCity wires the loaded corpus into the exported production seam.
// The returned stop function drains the plane's run tailers and status
// samplers; the harness calls it after the server closes (LIFO cleanup).
func serveSeededCity(ctx context.Context, fx *fixtures) (http.Handler, func(), error) {
	return api.ServeSeededCity(ctx, api.SeededCityDeps{
		CityName:        fx.CityName,
		CityPath:        fx.CityPath,
		Config:          fx.Config,
		CityBeadStore:   fx.CityStore,
		RigStores:       fx.RigStores,
		MailProvider:    fx.MailProv,
		EventProvider:   fx.EventProv,
		SessionProvider: fx.sessionProvider,
	}, "")
}
