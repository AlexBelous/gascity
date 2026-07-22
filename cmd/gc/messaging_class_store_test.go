package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	messagingdb "github.com/gastownhall/gascity/internal/classdb/messaging"
)

// writeMessagingRoutedCity builds a city directory committed to
// sqlite-backed messaging routing: [beads.classes.messaging]
// backend="sqlite" plus the migrated marker (both keys of the routing
// decision).
func writeMessagingRoutedCity(t *testing.T) string {
	t.Helper()
	cityPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(cityPath, "city.toml"), []byte("[workspace]\nname = \"test\"\n\n[beads.classes.messaging]\nbackend = \"sqlite\"\n"), 0o644); err != nil {
		t.Fatalf("writing city.toml: %v", err)
	}
	if err := os.MkdirAll(messagingdb.StoreDir(cityPath), 0o755); err != nil {
		t.Fatalf("creating store dir: %v", err)
	}
	if err := os.WriteFile(messagingdb.MigratedMarkerPath(cityPath), []byte("messaging class migrated\n"), 0o644); err != nil {
		t.Fatalf("writing migrated marker: %v", err)
	}
	return cityPath
}

func TestMessagingRoutingForResolvesClassStore(t *testing.T) {
	unrouted := t.TempDir()
	routing, err := messagingRoutingFor(unrouted, nil)
	if err != nil || routing.class != nil {
		t.Fatalf("messagingRoutingFor(unrouted) = %+v, %v; want bd", routing, err)
	}

	cityPath := writeMessagingRoutedCity(t)
	routing, err = messagingRoutingFor(cityPath, nil)
	if err != nil {
		t.Fatalf("messagingRoutingFor(routed): %v", err)
	}
	if routing.class == nil {
		t.Fatal("messagingRoutingFor(routed) resolved no class store")
	}
}

// The session-repair paths see only a store handle; the registry recorded at
// the store-opening roots maps it back to its city so messaging-class
// routing can be resolved eight frames below closeBead.
func TestMessagingRepairClassForUsesRegisteredCity(t *testing.T) {
	unregistered := beads.NewMemStore()
	class, err := messagingRepairClassFor(unregistered)
	if err != nil || class != nil {
		t.Fatalf("messagingRepairClassFor(unregistered) = %v, %v; want bd", class, err)
	}

	cityPath := writeMessagingRoutedCity(t)
	store := beads.NewMemStore()
	registerMessagingRepairCity(store, cityPath)
	class, err = messagingRepairClassFor(store)
	if err != nil {
		t.Fatalf("messagingRepairClassFor(routed): %v", err)
	}
	if class == nil {
		t.Fatal("messagingRepairClassFor(routed) resolved no class store")
	}

	// A distinct store value for an unrouted city resolves to bd.
	other := beads.NewMemStore()
	registerMessagingRepairCity(other, t.TempDir())
	class, err = messagingRepairClassFor(other)
	if err != nil || class != nil {
		t.Fatalf("messagingRepairClassFor(unrouted city) = %v, %v; want bd", class, err)
	}
}

// TestMessagingSeamIsTheOnlyConstructionPoint is the completeness ratchet
// for the messaging routing seam: production cmd/gc code must never
// construct a beadmail provider, extmsg service fabric, or messaging class
// store directly — a direct construction would bypass the
// [beads.classes.messaging] backend dispatch and split the class (mail AND
// extmsg records) across two backends on a migrated city.
func TestMessagingSeamIsTheOnlyConstructionPoint(t *testing.T) {
	// messaging_class_store.go IS the seam; providers.go holds the bd leg of
	// the [mail] provider-knob dispatch the seam delegates to, plus the CLI
	// opener's routed branch.
	allowedFiles := map[string]bool{
		"messaging_class_store.go": true,
		"providers.go":             true,
	}
	forbidden := []string{
		"beadmail.New(",
		"beadmail.NewCached(",
		"beadmail.NewWithStores(",
		"beadmail.NewCachedWithStores(",
		"beadmail.NewWithBackend(",
		"beadmail.NewCachedWithBackend(",
		"extmsg.NewServices",
		"messagingdb.Open(",
		"messagingdb.SharedStoreFor(",
		"messagingdb.RoutedStoreFor(",
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if allowedFiles[name] {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		content := string(data)
		for _, needle := range forbidden {
			if strings.Contains(content, needle) {
				t.Errorf("%s contains %q — messaging-class construction must route through the messaging_class_store.go seam", name, needle)
			}
		}
	}
}
