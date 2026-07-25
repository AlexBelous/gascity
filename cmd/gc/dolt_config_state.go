package main

import (
	"path/filepath"

	"github.com/gastownhall/gascity/internal/beads/contract"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/fsys"
)

func resolveDesiredScopeEndpointState(cityPath, scopeRoot, issuePrefix, scopeLabel string, desired contract.ConfigState) (contract.ConfigState, bool, error) {
	resolved, err := contract.ResolveScopeConfigState(fsys.OSFS{}, cityPath, scopeRoot, issuePrefix)
	if err != nil {
		return contract.ConfigState{}, false, wrapInvalidEndpointStateError(scopeLabel, err)
	}
	if resolved.Kind == contract.ScopeConfigAuthoritative {
		return resolved.State, true, nil
	}
	return desired, false, nil
}

// resolveDesiredCityEndpointState returns the topology-aware desired city
// endpoint state. The second result mirrors resolveDesiredScopeEndpointState's
// authoritative-vs-derived signal for symmetry with the rig path; callers
// currently only need the state and the error.
//
//nolint:unparam // bool result kept for resolver-shape symmetry with resolveDesiredScopeEndpointState
func resolveDesiredCityEndpointState(cityPath string, cityDolt config.DoltConfig, cityPrefix string) (contract.ConfigState, bool, error) {
	// A remote work marker re-points the city endpoint. The topology-desired
	// state WINS over any stale on-disk authoritative config (managed_city
	// pre-re-point) so a crash between marker and re-point self-heals on the
	// next boot's canonicalization. A marker stat/parse error surfaces here and
	// aborts the whole canonicalization pass (never a silent legacy fallback).
	topo, err := loadWorkTopology(cityPath)
	if err != nil {
		return contract.ConfigState{}, false, wrapInvalidEndpointStateError("city", err)
	}
	if topo.remote {
		if state, ok := workTopologyDesiredCityState(cityPath, cityPrefix); ok {
			return state, true, nil
		}
	}
	return resolveDesiredScopeEndpointState(cityPath, cityPath, cityPrefix, "city", desiredCityDoltConfigState(cityPath, cityDolt, cityPrefix))
}

func resolveDesiredRigEndpointState(cityPath string, rig config.Rig, cityState contract.ConfigState) (contract.ConfigState, error) {
	rig = normalizedRigConfig(cityPath, rig)
	desired := desiredRigDoltConfigState(cityPath, rig, cityState)
	// Under a unified/remote work marker a rig's work beads live in the city
	// database; its endpoint is inherited_city (never explicit), mirroring the
	// city under a canonical/remote city. The topology-desired state WINS over
	// any stale on-disk authoritative config so a late-bound legacy rig
	// converges. Marker stat/parse errors surface and abort the pass.
	topo, err := loadWorkTopology(cityPath)
	if err != nil {
		return contract.ConfigState{}, wrapInvalidEndpointStateError("rig", err)
	}
	if topo.sharesCityDatabase() {
		return inheritedRigDoltConfigState(rig.Path, rig.EffectivePrefix(), cityState), nil
	}
	resolved, err := contract.ResolveScopeConfigState(fsys.OSFS{}, cityPath, rig.Path, rig.EffectivePrefix())
	if err != nil {
		if cfg, ok, readErr := contract.ReadConfigState(fsys.OSFS{}, filepath.Join(rig.Path, ".beads", "config.yaml")); readErr == nil && ok && cfg.EndpointOrigin == contract.EndpointOriginInheritedCity {
			return desired, nil
		}
		return contract.ConfigState{}, wrapInvalidEndpointStateError("rig", err)
	}
	if resolved.Kind == contract.ScopeConfigAuthoritative {
		return resolved.State, nil
	}
	return desired, nil
}
