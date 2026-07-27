// Package lumen owns durable state for the beta formula runtime.
package lumen

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gastownhall/gascity/internal/beads/contract"
	"github.com/gastownhall/gascity/internal/citylayout"
	"github.com/gastownhall/gascity/internal/fsys"
	"github.com/gastownhall/gascity/internal/graphstore"
	"github.com/gastownhall/gascity/internal/lumen/engine"
	"github.com/gastownhall/gascity/internal/lumen/ir"
)

// Store owns one city's Lumen journal and content-addressed run inputs.
type Store struct {
	root    string
	journal *graphstore.Store
}

// Open opens the dedicated Lumen store below <city>/.gc/lumen.
func Open(ctx context.Context, cityPath string) (*Store, error) {
	if strings.TrimSpace(cityPath) == "" {
		return nil, fmt.Errorf("lumen store: empty city path")
	}
	root := citylayout.RuntimePath(cityPath, "lumen")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("lumen store: creating %q: %w", root, err)
	}
	cityID, _, err := contract.ReadProjectIdentity(fsys.OSFS{}, cityPath)
	if err != nil {
		return nil, fmt.Errorf("lumen store: reading city identity: %w", err)
	}
	journal, err := graphstore.Open(ctx, filepath.Join(root, "journal.sqlite"), graphstore.Options{CityID: cityID})
	if err != nil {
		return nil, fmt.Errorf("lumen store: opening journal: %w", err)
	}
	return &Store{root: root, journal: journal}, nil
}

// Close closes the journal.
func (s *Store) Close() error {
	if s == nil || s.journal == nil {
		return nil
	}
	return s.journal.Close()
}

// Journal returns the runtime journal used by the Lumen engine.
func (s *Store) Journal() *graphstore.Store {
	if s == nil {
		return nil
	}
	return s.journal
}

// Enqueue durably stores a run's immutable inputs before appending run.started.
func (s *Store) Enqueue(
	ctx context.Context,
	doc *ir.IR,
	input map[string]any,
	formulaRef string,
	defaultRoute string,
	driver string,
) (string, error) {
	if s == nil || s.journal == nil {
		return "", fmt.Errorf("lumen store: enqueue: closed store")
	}
	if doc == nil {
		return "", fmt.Errorf("lumen store: enqueue: nil IR document")
	}
	irHash := engine.IRHash(doc)
	if err := s.writeBlob("ir", irHash, doc); err != nil {
		return "", fmt.Errorf("lumen store: enqueue: writing IR: %w", err)
	}
	inputHash := engine.InputHash(input)
	if inputHash != "" {
		if err := s.writeBlob("input", inputHash, input); err != nil {
			return "", fmt.Errorf("lumen store: enqueue: writing input: %w", err)
		}
	}
	streamID, err := engine.EnqueueRunWithDriver(
		ctx,
		s.journal,
		doc,
		input,
		formulaRef,
		defaultRoute,
		driver,
	)
	if err != nil {
		return "", fmt.Errorf("lumen store: enqueue: %w", err)
	}
	return streamID, nil
}

// Load restores the immutable IR and input named by a run manifest.
func (s *Store) Load(manifest engine.RunManifest) (*ir.IR, map[string]any, error) {
	if s == nil {
		return nil, nil, fmt.Errorf("lumen store: load: nil store")
	}
	rawIR, err := s.readBlob("ir", manifest.IRHash)
	if err != nil {
		return nil, nil, fmt.Errorf("lumen store: load IR: %w", err)
	}
	doc, err := ir.Decode(rawIR)
	if err != nil {
		return nil, nil, fmt.Errorf("lumen store: load IR %q: %w", manifest.IRHash, err)
	}
	if got := engine.IRHash(doc); got != manifest.IRHash {
		return nil, nil, fmt.Errorf("lumen store: load IR: hash mismatch: got %s, want %s", got, manifest.IRHash)
	}

	var input map[string]any
	if manifest.InputHash != "" {
		rawInput, err := s.readBlob("input", manifest.InputHash)
		if err != nil {
			return nil, nil, fmt.Errorf("lumen store: load input: %w", err)
		}
		if err := json.Unmarshal(rawInput, &input); err != nil {
			return nil, nil, fmt.Errorf("lumen store: load input %q: %w", manifest.InputHash, err)
		}
		if got := engine.InputHash(input); got != manifest.InputHash {
			return nil, nil, fmt.Errorf("lumen store: load input: hash mismatch: got %s, want %s", got, manifest.InputHash)
		}
	}
	return doc, input, nil
}

func (s *Store) writeBlob(kind, hash string, value any) error {
	path := s.blobPath(kind, hash)
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return fsys.WriteFileAtomic(fsys.OSFS{}, path, raw, 0o600)
}

func (s *Store) readBlob(kind, hash string) ([]byte, error) {
	if strings.TrimSpace(hash) == "" {
		return nil, fmt.Errorf("empty %s hash", kind)
	}
	path := s.blobPath(kind, hash)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %q: %w", path, err)
	}
	return raw, nil
}

func (s *Store) blobPath(kind, hash string) string {
	return filepath.Join(s.root, "cas", kind, hash+".json")
}
