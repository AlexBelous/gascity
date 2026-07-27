package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/gastownhall/gascity/internal/graphstore"
	lumenstore "github.com/gastownhall/gascity/internal/lumen"
	"github.com/gastownhall/gascity/internal/lumen/engine"
)

type lumenRuntime struct {
	store *lumenstore.Store
}

func (cr *CityRuntime) lumenStore(ctx context.Context) *lumenstore.Store {
	if cr.lumen == nil {
		cr.lumen = &lumenRuntime{}
	}
	if cr.lumen.store != nil {
		return cr.lumen.store
	}
	store, err := lumenstore.Open(ctx, cr.cityPath)
	if err != nil {
		fmt.Fprintf(cr.stderr, "%s: lumen runs: %v\n", cr.logPrefix, err) //nolint:errcheck
		return nil
	}
	cr.lumen.store = store
	return store
}

func (cr *CityRuntime) closeLumenStore() {
	if cr.lumen == nil || cr.lumen.store == nil {
		return
	}
	_ = cr.lumen.store.Close()
	cr.lumen.store = nil
}

// lumenRunsTick advances every open controller-owned run once.
func (cr *CityRuntime) lumenRunsTick(ctx context.Context) {
	if cr.cfg == nil || !cr.cfg.Daemon.LumenBetaEnabled() || cr.cityPath == "" {
		return
	}
	store := cr.lumenStore(ctx)
	if store == nil {
		return
	}
	runs, err := engine.ListOpenRuns(ctx, store.Journal())
	if err != nil {
		fmt.Fprintf(cr.stderr, "%s: lumen runs: listing open runs: %v\n", cr.logPrefix, err) //nolint:errcheck
		return
	}
	for _, run := range runs {
		cr.advanceLumenRun(ctx, store, run)
	}
}

func (cr *CityRuntime) advanceLumenRun(ctx context.Context, store *lumenstore.Store, run engine.OpenRun) {
	manifest, err := engine.ReadRunManifest(ctx, store.Journal(), run.StreamID)
	if err != nil {
		fmt.Fprintf(cr.stderr, "%s: lumen runs: reading %q: %v\n", cr.logPrefix, run.StreamID, err) //nolint:errcheck
		return
	}
	if manifest.Driver == engine.DriverSelf {
		return
	}
	doc, input, err := store.Load(manifest)
	if err != nil {
		fmt.Fprintf(cr.stderr, "%s: lumen runs: loading %q: %v\n", cr.logPrefix, run.StreamID, err) //nolint:errcheck
		return
	}
	workStore := cr.cityWorkStore().Store
	if workStore == nil {
		fmt.Fprintf(cr.stderr, "%s: lumen runs: advancing %q: city work store is unavailable\n", cr.logPrefix, run.StreamID) //nolint:errcheck
		return
	}
	_, err = engine.Advance(ctx, store.Journal(), doc, run.StreamID, input, engine.Options{
		SnapshotEvery: engine.DefaultSnapshotEvery,
		PoolRouter:    lumenPoolRouter(manifest.DefaultRoute),
		DispatchWork:  lumenDispatchWork(workStore, cr.cfg),
		ObserveWork:   lumenObserveWork(workStore),
	})
	if err != nil && !retryableLumenAdvanceError(err) {
		fmt.Fprintf(cr.stderr, "%s: lumen runs: advancing %q: %v\n", cr.logPrefix, run.StreamID, err) //nolint:errcheck
	}
}

func lumenPoolRouter(defaultRoute string) func(string) (string, bool) {
	return func(agentRef string) (string, bool) {
		if agentRef != "" {
			return agentRef, true
		}
		return defaultRoute, defaultRoute != ""
	}
}

func retryableLumenAdvanceError(err error) bool {
	return errors.Is(err, graphstore.ErrWrongExpectedVersion) ||
		errors.Is(err, graphstore.ErrLeaseFenced) ||
		errors.Is(err, graphstore.ErrLeaseHeld) ||
		errors.Is(err, graphstore.ErrBusy) ||
		errors.Is(err, graphstore.ErrRebuildRaced)
}
