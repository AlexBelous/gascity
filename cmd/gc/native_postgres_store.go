package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/pgauth"
)

// openNativePostgresReadStore builds the native Postgres read store for a scope
// by wrapping the same per-call BdStore the OpenBdStore path produces. It is the
// postgres branch of the OpenNativeStore closures in main.go and api_state.go:
// the native store serves hot reads directly from Postgres and delegates writes
// (and any read it cannot serve) to the embedded bd store, so callers get the
// bd fallback for free. openBd is the scope's bd-store opener; the result must
// be a *beads.BdStore (postgres scopes are not doltlite-optimized).
//
// It injects the canonical internal/pgauth resolver as the native store's
// credential source, so the native read plane resolves the Postgres password
// through the SAME 7-tier chain (env tiers, scope .beads/.env,
// $BEADS_CREDENTIALS_FILE, ~/.config/beads/credentials, all with 0600
// enforcement) that gc's bd write/fallback plane uses — no hand-rolled parser,
// no credential-source divergence between the read and write planes.
func openNativePostgresReadStore(ctx context.Context, scopeRoot string, openBd func() (beads.Store, error)) (beads.Store, error) {
	base, err := openBd()
	if err != nil {
		return nil, fmt.Errorf("native postgres read store %s: opening bd base: %w", scopeRoot, err)
	}
	bd, ok := base.(*beads.BdStore)
	if !ok {
		return nil, fmt.Errorf("native postgres read store %s: expected *beads.BdStore base, got %T", scopeRoot, base)
	}
	return beads.OpenNativePostgresReadStore(ctx, scopeRoot, bd,
		beads.WithNativePostgresLogger(slog.Default()),
		beads.WithNativePostgresPasswordResolver(pgauthPasswordResolver),
	)
}

// pgauthPasswordResolver adapts the canonical internal/pgauth resolver to the
// beads.PostgresPasswordResolver signature. It passes envMap=nil so resolution
// uses the process environment plus on-disk tiers (the native path has no
// per-call projected env map, unlike the bd subprocess).
func pgauthPasswordResolver(scopeRoot string, endpoint beads.PostgresEndpoint) (string, error) {
	resolved, err := pgauth.ResolveFromEnv(nil, scopeRoot, pgauth.Endpoint{
		Host: endpoint.Host,
		Port: endpoint.Port,
		User: endpoint.User,
	})
	if err != nil {
		return "", err
	}
	return resolved.Password, nil
}
