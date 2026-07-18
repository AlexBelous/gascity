//go:build integration

// This file is intentionally a NON-test .go file (no _test suffix) so it sorts
// alphabetically before emission_projection_test.go and establishes the base
// package name "dashport_test" for go/build before the _test files are parsed —
// otherwise a _test file sorting ahead of the non-test fixtures.go/harness.go is
// misread as an external "dashport" test package. It carries the emission Layer A
// harness the emission_projection_test.go assertions drive.
package dashport_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gastownhall/gascity/internal/api"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/test/dashport/emitseed"
)

// emissionHarness is a running seeded-city server whose entire state was produced
// by driving the real event-emission pipeline (package emitseed), plus the
// emission result carrying the raw events.jsonl path.
type emissionHarness struct {
	*harness
	res *emitseed.Result
}

// newEmissionHarness drives SeedByEmission over a temp city, serves the resulting
// live stores + event provider through the production api.ServeSeededCity seam,
// and returns a harness plus the emission result. The served run views and home
// page are therefore populated exclusively by genuine emissions.
func newEmissionHarness(t *testing.T) *emissionHarness {
	t.Helper()

	cityPath := t.TempDir()
	res, err := emitseed.SeedByEmission(cityPath)
	if err != nil {
		t.Fatalf("SeedByEmission: %v", err)
	}
	t.Cleanup(func() { _ = res.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	handler, stop, err := api.ServeSeededCity(ctx, api.SeededCityDeps{
		CityName:      res.CityName,
		CityPath:      res.CityPath,
		Config:        res.Config,
		CityBeadStore: res.CityStore,
		RigStores:     res.RigStores,
		EventProvider: res.EventProv,
	}, "")
	if err != nil {
		t.Fatalf("ServeSeededCity: %v", err)
	}
	t.Cleanup(stop)

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	return &emissionHarness{
		harness: &harness{
			t:        t,
			server:   srv,
			cityName: res.CityName,
			cityPath: res.CityPath,
			client:   srv.Client(),
		},
		res: res,
	}
}

// readEmittedEvents reads and decodes every record from a plain events.jsonl,
// preserving file (seq) order.
func readEmittedEvents(t *testing.T, path string) []events.Event {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open emitted event log %s: %v", path, err)
	}
	defer f.Close() //nolint:errcheck
	var out []events.Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e events.Event
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatalf("decode emitted event %q: %v", string(line), err)
		}
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan emitted event log %s: %v", path, err)
	}
	return out
}
