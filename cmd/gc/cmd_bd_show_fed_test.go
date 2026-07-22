package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	messagingdb "github.com/gastownhall/gascity/internal/classdb/messaging"
	sessionsdb "github.com/gastownhall/gascity/internal/classdb/sessions"
	"github.com/gastownhall/gascity/internal/mail/beadmail"
	"github.com/gastownhall/gascity/internal/orders"
)

func TestBdShowRoutable(t *testing.T) {
	cases := []struct {
		args           []string
		wantID         string
		wantJSON, want bool
	}{
		{[]string{"show", "gcs-1"}, "gcs-1", false, true},
		{[]string{"show", "gcs-1", "--json"}, "gcs-1", true, true},
		{[]string{"show", "--json", "gco-2"}, "gco-2", true, true},
		{[]string{"show"}, "", false, false},
		{[]string{"show", "a", "b"}, "", false, false},
		{[]string{"show", "gcs-1", "--verbose"}, "", false, false},
		{[]string{"list", "gcs-1"}, "", false, false},
	}
	for _, tc := range cases {
		id, jsonOut, ok := bdShowRoutable(tc.args)
		if id != tc.wantID || jsonOut != tc.wantJSON || ok != tc.want {
			t.Errorf("bdShowRoutable(%v) = (%q, %v, %v), want (%q, %v, %v)",
				tc.args, id, jsonOut, ok, tc.wantID, tc.wantJSON, tc.want)
		}
	}
}

// TestBdShowFedReservedIDAbsentStore pins the reserved-prefix absence shape:
// a reserved id with no class store file has nowhere to live, so the fed
// renders bd's own "no issue found" and exits non-zero — handled, never
// forwarded to bd.
func TestBdShowFedReservedIDAbsentStore(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code, handled := maybeRouteBdShowLocal(t.TempDir(), nil, []string{"show", "gcs-404"}, &stdout, &stderr)
	if !handled || code != 1 {
		t.Fatalf("= (%d, %v), want handled exit 1", code, handled)
	}
	if !strings.Contains(stderr.String(), "no issue found matching") {
		t.Fatalf("stderr %q", stderr.String())
	}
}

// TestBdShowFedGraphFallsThrough pins that gcg ids keep the byte-identical
// passthrough: graph is not relocated in this tree.
func TestBdShowFedGraphFallsThrough(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if _, handled := maybeRouteBdShowLocal(t.TempDir(), nil, []string{"show", "gcg-wisp-1"}, &stdout, &stderr); handled {
		t.Fatal("gcg id was federated; graph must fall through to bd")
	}
}

// TestBdShowFedSessionsReservedID serves a gcs id from the sessions class
// store, in both text and bd's --json array shape.
func TestBdShowFedSessionsReservedID(t *testing.T) {
	cityPath := t.TempDir()
	st, err := sessionsdb.SharedStoreFor(cityPath)
	if err != nil {
		t.Fatal(err)
	}
	created, err := st.Create(beads.Bead{Title: "agent session demo", Type: "session", Labels: []string{"gc:session"}})
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code, handled := maybeRouteBdShowLocal(cityPath, nil, []string{"show", created.ID}, &stdout, &stderr)
	if !handled || code != 0 {
		t.Fatalf("= (%d, %v), want handled 0; stderr=%s", code, handled, stderr.String())
	}
	if !strings.Contains(stdout.String(), created.ID) || !strings.Contains(stdout.String(), "agent session demo") {
		t.Fatalf("stdout %q", stdout.String())
	}

	stdout.Reset()
	code, handled = maybeRouteBdShowLocal(cityPath, nil, []string{"show", created.ID, "--json"}, &stdout, &stderr)
	if !handled || code != 0 {
		t.Fatalf("json = (%d, %v); stderr=%s", code, handled, stderr.String())
	}
	var arr []beads.Bead
	if err := json.Unmarshal(stdout.Bytes(), &arr); err != nil {
		t.Fatalf("json shape: %v; out=%s", err, stdout.String())
	}
	if len(arr) != 1 || arr[0].ID != created.ID || arr[0].Type != "session" {
		t.Fatalf("json array %+v", arr)
	}

	// A missing gcs id in a present store is genuine absence.
	stdout.Reset()
	stderr.Reset()
	code, handled = maybeRouteBdShowLocal(cityPath, nil, []string{"show", "gcs-999999"}, &stdout, &stderr)
	if !handled || code != 1 || !strings.Contains(stderr.String(), "no issue found matching") {
		t.Fatalf("miss = (%d, %v, %q)", code, handled, stderr.String())
	}
}

// TestBdShowFedOrdersReservedID serves a gco id from the orders class store
// with the tracking-bead label vocabulary.
func TestBdShowFedOrdersReservedID(t *testing.T) {
	cityPath := t.TempDir()
	st, err := ordersClassStoreFor(cityPath)
	if err != nil {
		t.Fatal(err)
	}
	run, err := orders.NewStoreWithTracking(st, beads.GraphStore{Store: beads.NewMemStore()}).CreateRun("digest", orders.RunOpts{})
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code, handled := maybeRouteBdShowLocal(cityPath, nil, []string{"show", run.ID, "--json"}, &stdout, &stderr)
	if !handled || code != 0 {
		t.Fatalf("= (%d, %v); stderr=%s", code, handled, stderr.String())
	}
	var arr []beads.Bead
	if err := json.Unmarshal(stdout.Bytes(), &arr); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(arr) != 1 || arr[0].ID != run.ID || arr[0].Status != "open" {
		t.Fatalf("bead %+v", arr)
	}
	joined := strings.Join(arr[0].Labels, ",")
	if !strings.Contains(joined, "order-tracking") || !strings.Contains(joined, "order-run:digest") {
		t.Fatalf("labels %v", arr[0].Labels)
	}
}

// TestBdShowFedLegacyIDProbe pins the migrated-legacy-id arm: an imported
// bd-era id (no reserved prefix) is served from a ROUTED class store, and
// falls through to bd on an unrouted city.
func TestBdShowFedLegacyIDProbe(t *testing.T) {
	cityPath := t.TempDir()
	cfg := classMigrationConfig(t, `
[beads.classes.sessions]
backend = "sqlite"

[beads.classes.messaging]
backend = "sqlite"
`)

	// Unrouted (no markers): the probe never fires, bd keeps the read.
	var stdout, stderr bytes.Buffer
	if _, handled := maybeRouteBdShowLocal(cityPath, cfg, []string{"show", "gc-legacy-1"}, &stdout, &stderr); handled {
		t.Fatal("legacy id federated on an unrouted city")
	}

	// Routed sessions + messaging with imported legacy rows.
	writeSessionsMigratedMarker(t, cityPath)
	if err := os.WriteFile(messagingdb.MigratedMarkerPath(cityPath), []byte("migrated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := sessionsdb.SharedStoreFor(cityPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Create(beads.Bead{ID: "gc-legacy-1", Title: "imported session", Type: "session", Labels: []string{"gc:session"}}); err != nil {
		t.Fatal(err)
	}
	ms, err := messagingdb.SharedStoreFor(cityPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := ms.ImportMessage(beadmail.Record{ID: "mc-legacy-2", Subject: "imported mail", Body: "hello", FromAddr: "a", ToAddr: "b", Open: true, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	code, handled := maybeRouteBdShowLocal(cityPath, cfg, []string{"show", "gc-legacy-1"}, &stdout, &stderr)
	if !handled || code != 0 || !strings.Contains(stdout.String(), "imported session") {
		t.Fatalf("sessions legacy = (%d, %v, %q) stderr=%s", code, handled, stdout.String(), stderr.String())
	}

	stdout.Reset()
	code, handled = maybeRouteBdShowLocal(cityPath, cfg, []string{"show", "mc-legacy-2", "--json"}, &stdout, &stderr)
	if !handled || code != 0 {
		t.Fatalf("messaging legacy = (%d, %v) stderr=%s", code, handled, stderr.String())
	}
	var arr []beads.Bead
	if err := json.Unmarshal(stdout.Bytes(), &arr); err != nil {
		t.Fatal(err)
	}
	if len(arr) != 1 || arr[0].ID != "mc-legacy-2" || arr[0].Type != "message" || arr[0].Title != "imported mail" {
		t.Fatalf("mail bead %+v", arr)
	}

	// A clean miss on a routed city still falls through to bd (work beads).
	stdout.Reset()
	if _, handled := maybeRouteBdShowLocal(cityPath, cfg, []string{"show", "gc-unmigrated-work"}, &stdout, &stderr); handled {
		t.Fatal("work-bead miss must fall through to bd")
	}
}
