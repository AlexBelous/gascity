// fixtureseed creates only fresh, named no-model acceptance state.
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/coordclass"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/storebinding"
	sqliteprovider "github.com/gastownhall/gascity/internal/storebinding/sqlite"
)

const template = "queue-fixture-worker"

type publicSeed struct {
	SID           string                 `json:"sid"`
	BeadID        string                 `json:"bead_id"`
	RootID        string                 `json:"root_id"`
	Template      string                 `json:"template"`
	RuntimeHandle string                 `json:"runtime_handle"`
	Binding       runtime.RuntimeBinding `json:"binding"`
}

type privateSeed struct {
	Public        publicSeed `json:"public"`
	Generation    string     `json:"generation"`
	InstanceToken string     `json:"instance_token"`
	Attempt       string     `json:"attempt"`
}

func randomHex() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func openClass(root, prefix string) (beads.Store, error) {
	classes := []coordclass.Class{}
	for _, c := range coordclass.Classes() {
		if c.IsInfrastructure() {
			classes = append(classes, c)
		}
	}
	served, err := storebinding.NewClassSet(classes...)
	if err != nil {
		return nil, err
	}
	path, err := sqliteprovider.GraphPath(filepath.Join(root, ".gc/store"))
	if err != nil {
		return nil, err
	}
	return beads.OpenSQLiteStore(filepath.Dir(path), beads.WithSQLiteStoreIDPrefix(prefix), beads.WithSQLiteStoreReservedIDPrefixes(storebinding.EngineReservedPrefixes(served)...))
}

func closeStore(s beads.Store) error { return s.(interface{ CloseStore() error }).CloseStore() }

func seed(root string, converge func(string) error) (publicSeed, error) {
	var out publicSeed
	if converge == nil {
		return out, errors.New("canonical convergence callback required")
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return out, errors.New("fixture city must be absolute and canonical")
	}
	// Refuse all existing paths; no cleanup, migration or overwrite.
	if err := os.Mkdir(root, 0o700); err != nil {
		return out, err
	}
	toml := `[workspace]
name = "queue-fixture"
prefix = "gc"
[beads]
provider = "file"
bd_compatibility = "bd-1.0.5"
[session]
provider = "tmux"
[providers.fixture]
command = "/usr/bin/false"
[[agent]]
name = "queue-fixture-worker"
runtime = "ssh:repairer-transport@127.0.0.1:22208"
provider = "fixture"
min_active_sessions = 0
max_active_sessions = 1
[storage.classes]
work = "work"
graph = "infra"
sessions = "infra"
messaging = "infra"
orders = "infra"
nudges = "infra"
[storage.bindings.infra]
provider = "sqlite-beads"
path = ".gc/store"
`
	if err := os.WriteFile(filepath.Join(root, "city.toml"), []byte(toml), 0o600); err != nil {
		return out, err
	}
	// This fresh, exclusively created city has no controller or external writers.
	// The real CLI proves and persists convergence before any infrastructure bead.
	if err := converge(root); err != nil {
		return out, err
	}
	token, err := randomHex()
	if err != nil {
		return out, err
	}
	attempt, err := randomHex()
	if err != nil {
		return out, err
	}
	// Opaque stable fixture match tag only; the legacy label is not a resource
	// policy digest. Real admission is the full root guard profile and its digest.
	digest := sha256.Sum256([]byte("isolated-queue-tmux-no-model-208"))
	binding := runtime.RuntimeBinding{Selector: "ssh:repairer-transport@127.0.0.1:22208", Endpoint: "repairer-transport@127.0.0.1:22208", ResourceDomain: "fixture-uid988-no-model", PolicyDigest: hex.EncodeToString(digest[:])}
	raw, err := json.Marshal(binding)
	if err != nil {
		return out, err
	}
	sp, ok := config.ReservedClassPrefix(config.BeadClassSessions)
	if !ok {
		return out, errors.New("missing session namespace")
	}
	sessions, err := openClass(root, sp)
	if err != nil {
		return out, err
	}
	sb, createErr := sessions.Create(beads.Bead{Type: "session", Title: "isolated no-model fixture", Status: "open", Labels: []string{"gc:session"}, Metadata: map[string]string{"template": template, "session_name": "queue-fixture-runtime", "generation": "1", "instance_token": token, "state": "active", "session_origin": "ephemeral", "runtime_binding_v1": string(raw)}})
	closeErr := closeStore(sessions)
	if err = errors.Join(createErr, closeErr); err != nil {
		return out, err
	}
	gp, ok := config.ReservedClassPrefix(config.BeadClassGraph)
	if !ok {
		return out, errors.New("missing graph namespace")
	}
	graph, err := openClass(root, gp)
	if err != nil {
		return out, err
	}
	rootBead, err := graph.Create(beads.Bead{Type: "task", Title: "isolated fixture workflow", Status: "open", Metadata: map[string]string{"gc.kind": "workflow", "gc.formula_contract": "graph.v2"}})
	if err != nil {
		_ = closeStore(graph)
		return out, err
	}
	child, createErr := graph.Create(beads.Bead{Type: "task", Title: "isolated fixture plain gated work", Status: "open", Metadata: map[string]string{"gc.root_bead_id": rootBead.ID, "gc.step_id": "repair", "gc.routed_to": template}})
	closeErr = closeStore(graph)
	if err = errors.Join(createErr, closeErr); err != nil {
		return out, err
	}
	out = publicSeed{SID: sb.ID, BeadID: child.ID, RootID: rootBead.ID, Template: template, RuntimeHandle: "queue-fixture-runtime", Binding: binding}
	private, err := json.Marshal(privateSeed{out, "1", token, attempt})
	if err != nil {
		return out, err
	}
	if err = os.WriteFile(filepath.Join(root, "fixture-private.json"), private, 0o600); err != nil {
		return out, err
	}
	public, err := json.Marshal(out)
	if err != nil {
		return out, err
	}
	if err = os.WriteFile(filepath.Join(root, "fixture-public.json"), public, 0o600); err != nil {
		return out, err
	}
	return out, nil
}

// migrateEmptyCity calls the real SDK front door, never manufactures its marker.
func migrateEmptyCity(gc, root string) error {
	if !filepath.IsAbs(gc) || filepath.Clean(gc) != gc {
		return errors.New("gc must be absolute and canonical")
	}
	info, err := os.Lstat(gc)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return errors.New("gc must be an executable regular file")
	}
	cmd := exec.Command(gc, "storage", "migrate", "--from-work", "--fleet-stopped")
	cmd.Dir = root
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + root, "USER=gc-build", "LOGNAME=gc-build", "GC_CITY_PATH=" + root}
	// Keep stdout reserved for the public seed JSON; migration diagnostics are public.
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	return cmd.Run()
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: fixtureseed ABSOLUTE-REVIEWED-GC ABSENT-ABSOLUTE-CITY")
		os.Exit(2)
	}
	out, err := seed(os.Args[2], func(root string) error { return migrateEmptyCity(os.Args[1], root) })
	if err != nil {
		fmt.Fprintln(os.Stderr, "fixture seed refused")
		os.Exit(1)
	}
	if err = json.NewEncoder(os.Stdout).Encode(out); err != nil {
		os.Exit(1)
	}
}
