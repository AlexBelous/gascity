package core

import (
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/orders"
)

// readOrder parses an order TOML from the embedded pack FS and restores the
// Name the scanner would normally derive from the filename (Parse leaves it
// blank because Name is not a TOML field).
func readOrder(t *testing.T, file string) orders.Order {
	t.Helper()
	data, err := fs.ReadFile(PackFS, "orders/"+file)
	if err != nil {
		t.Fatalf("reading orders/%s: %v", file, err)
	}
	o, err := orders.Parse(data)
	if err != nil {
		t.Fatalf("parsing orders/%s: %v", file, err)
	}
	o.Name = strings.TrimSuffix(file, ".toml")
	return o
}

// TestCoreOrdersValidate asserts every embedded order TOML parses and passes
// structural validation, so a malformed order can never ship in the gc
// binary's bundled core pack.
func TestCoreOrdersValidate(t *testing.T) {
	entries, err := fs.ReadDir(PackFS, "orders")
	if err != nil {
		t.Fatalf("reading orders dir: %v", err)
	}
	saw := false
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		saw = true
		o := readOrder(t, e.Name())
		if err := orders.Validate(o); err != nil {
			t.Errorf("order %s failed validation: %v", e.Name(), err)
		}
	}
	if !saw {
		t.Fatal("no order TOML files found in embedded pack")
	}
}

// Readiness-triggered work dispatch belongs to the controller reconciler. The
// core pack must not reintroduce an event-order nudge that can run before a
// routed or assigned bead is dependency-ready.
func TestCorePackDoesNotShipReadinessNudgeOrders(t *testing.T) {
	for _, name := range []string{
		"orders/nudge-on-route.toml",
		"orders/cascade-nudge-on-blocker-close.toml",
	} {
		_, err := fs.ReadFile(PackFS, name)
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s is still installed; readiness nudge must be reconciler-owned", name)
		}
	}
}
