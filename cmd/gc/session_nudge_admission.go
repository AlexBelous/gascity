package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"

	"github.com/gastownhall/gascity/internal/api"
	"github.com/gastownhall/gascity/internal/citywriteauth"
	"github.com/gastownhall/gascity/internal/nudgequeue"
)

// productionSessionNudgeAuthority is the lifecycle-safe capability presented
// by one fully recovered city authority binding. The API adapter deliberately
// cannot construct, recover, close, or reach effects through this interface.
type productionSessionNudgeAuthority interface {
	RequesterScope() (tenantScope, cityScope, credentialClass string)
	Admit(context.Context, nudgequeue.NudgeIngressRequest) (nudgequeue.NudgeIngressResult, error)
}

type productionSessionNudgeAuthorityResolver func(string) (productionSessionNudgeAuthority, bool)

var errNudgeLegacyEffectOwnership = errors.New("legacy nudge effect ownership")

// cityRuntimeNudgeAuthoritySlot lets a controller socket exist before its
// CityRuntime is constructed without guessing that legacy ownership applies.
// Until Store publishes the runtime, admission is explicitly unavailable.
type cityRuntimeNudgeAuthoritySlot struct {
	runtime atomic.Pointer[CityRuntime]
}

func (s *cityRuntimeNudgeAuthoritySlot) Store(cr *CityRuntime) {
	if s == nil {
		return
	}
	s.runtime.Store(cr)
}

func (s *cityRuntimeNudgeAuthoritySlot) RequesterScope() (string, string, string) {
	authority := s.load()
	if authority == nil {
		return "", "", ""
	}
	return authority.RequesterScope()
}

func (s *cityRuntimeNudgeAuthoritySlot) Admit(ctx context.Context, request nudgequeue.NudgeIngressRequest) (nudgequeue.NudgeIngressResult, error) {
	authority := s.load()
	if authority == nil {
		return nudgequeue.NudgeIngressResult{}, fmt.Errorf("%w: city runtime is not published", nudgequeue.ErrLocalNudgeAuthorityUnavailable)
	}
	return authority.Admit(ctx, request)
}

func (s *cityRuntimeNudgeAuthoritySlot) load() *cityRuntimeSessionNudgeAuthority {
	if s == nil {
		return nil
	}
	return newCityRuntimeSessionNudgeAuthority(s.runtime.Load())
}

// cityRuntimeSessionNudgeAuthority is the effect-owner-aware admission
// capability for one immutable CityRuntime. It keeps durable admission and
// exact-key wake together without adding runtime capabilities to the durable
// repository binding itself.
type cityRuntimeSessionNudgeAuthority struct {
	cr      *CityRuntime
	binding productionSessionNudgeAuthority
	wake    func(context.Context, nudgeWakeHint)
}

func newCityRuntimeSessionNudgeAuthority(cr *CityRuntime) *cityRuntimeSessionNudgeAuthority {
	if cr == nil {
		return nil
	}
	return &cityRuntimeSessionNudgeAuthority{
		cr:      cr,
		binding: cr.liveNudgeAuthorityBinding(),
		wake:    cr.acceptNudgeKeyShadowHint,
	}
}

func (a *cityRuntimeSessionNudgeAuthority) RequesterScope() (string, string, string) {
	if a == nil || isNilProductionSessionNudgeAuthority(a.binding) {
		return "", "", ""
	}
	return a.binding.RequesterScope()
}

func (a *cityRuntimeSessionNudgeAuthority) Admit(ctx context.Context, request nudgequeue.NudgeIngressRequest) (nudgequeue.NudgeIngressResult, error) {
	if a == nil || a.cr == nil {
		return nudgequeue.NudgeIngressResult{}, fmt.Errorf("%w: city runtime nudge authority is unavailable", nudgequeue.ErrLocalNudgeAuthorityUnavailable)
	}
	switch a.cr.nudgeEffectOwnership {
	case nudgeEffectOwnershipLegacy:
		a.cr.submitLegacyImmediateNudgeParity(request)
		return nudgequeue.NudgeIngressResult{}, errors.Join(
			errNudgeLegacyEffectOwnership,
			nudgequeue.ErrLocalNudgeAuthorityUnavailable,
		)
	case nudgeEffectOwnershipKeyed:
		// Continue below.
	default:
		return nudgequeue.NudgeIngressResult{}, fmt.Errorf("%w: unknown nudge effect ownership %d", nudgequeue.ErrLocalNudgeAuthorityUnavailable, a.cr.nudgeEffectOwnership)
	}
	if isNilProductionSessionNudgeAuthority(a.binding) {
		return nudgequeue.NudgeIngressResult{}, fmt.Errorf("%w: city runtime durable binding is unavailable", nudgequeue.ErrLocalNudgeAuthorityUnavailable)
	}
	result, err := a.binding.Admit(ctx, request)
	if err != nil {
		return nudgequeue.NudgeIngressResult{}, err
	}
	if result.Entry.Command == nil || result.Entry.Command.ID == "" {
		return nudgequeue.NudgeIngressResult{}, fmt.Errorf("%w: durable admission returned no command identity", nudgequeue.ErrLocalNudgeAuthorityUnavailable)
	}
	if a.wake != nil {
		a.wake(ctx, nudgeWakeHint{CommandID: result.Entry.Command.ID})
	}
	return result, nil
}

type productionSessionNudgeAdmission struct {
	resolve productionSessionNudgeAuthorityResolver
}

var _ api.SessionNudgeAdmission = (*productionSessionNudgeAdmission)(nil)

func newProductionSessionNudgeAdmission(resolve productionSessionNudgeAuthorityResolver) *productionSessionNudgeAdmission {
	return &productionSessionNudgeAdmission{resolve: resolve}
}

func installSupervisorProductionNudgeAdmission(mux *api.SupervisorMux, registry *cityRegistry) {
	mux.WithSessionNudgeAdmission(newProductionSessionNudgeAdmission(registry.resolveProductionNudgeAuthority))
}

func (a *productionSessionNudgeAdmission) AdmitSessionNudge(
	ctx context.Context,
	city string,
	request nudgequeue.NudgeIngressRequest,
) (nudgequeue.NudgeIngressResult, error) {
	grant, ok := api.VerifiedCityWriteGrantFromContext(ctx)
	if !ok {
		return nudgequeue.NudgeIngressResult{}, fmt.Errorf("%w: verified city write grant is required", nudgequeue.ErrNudgeAuthorizationDenied)
	}
	if err := validateVerifiedNudgeGrantCity(grant, city); err != nil {
		return nudgequeue.NudgeIngressResult{}, err
	}
	if a == nil || a.resolve == nil {
		return nudgequeue.NudgeIngressResult{}, fmt.Errorf("%w: production nudge authority is not configured", nudgequeue.ErrLocalNudgeAuthorityUnavailable)
	}
	authority, live := a.resolve(city)
	if !live || isNilProductionSessionNudgeAuthority(authority) {
		return nudgequeue.NudgeIngressResult{}, fmt.Errorf("%w: production nudge authority is not live", nudgequeue.ErrLocalNudgeAuthorityUnavailable)
	}
	tenantScope, cityScope, credentialClass := authority.RequesterScope()
	requester, err := authenticatedNudgeRequesterFromVerifiedGrant(
		grant,
		city,
		tenantScope,
		cityScope,
		credentialClass,
	)
	if err != nil {
		return nudgequeue.NudgeIngressResult{}, err
	}
	return authority.Admit(nudgequeue.WithAuthenticatedNudgeRequester(ctx, requester), request)
}

func authenticatedNudgeRequesterFromVerifiedGrant(
	grant citywriteauth.Grant,
	routeCity string,
	tenantScope string,
	cityScope string,
	credentialClass string,
) (nudgequeue.AuthenticatedNudgeRequester, error) {
	if err := validateVerifiedNudgeGrantCity(grant, routeCity); err != nil {
		return nudgequeue.AuthenticatedNudgeRequester{}, err
	}
	if tenantScope == "" || cityScope == "" || credentialClass == "" {
		return nudgequeue.AuthenticatedNudgeRequester{}, fmt.Errorf("%w: production nudge requester scope is incomplete", nudgequeue.ErrLocalNudgeAuthorityUnavailable)
	}
	return nudgequeue.AuthenticatedNudgeRequester{
		PrincipalID:     grant.Kid,
		TenantScope:     tenantScope,
		CityScope:       cityScope,
		CredentialClass: credentialClass,
		EvidenceID:      grant.JTI,
	}, nil
}

func validateVerifiedNudgeGrantCity(grant citywriteauth.Grant, routeCity string) error {
	if routeCity == "" || grant.City != routeCity || grant.Kid == "" || grant.JTI == "" {
		return fmt.Errorf("%w: verified city write grant does not cover the requested city", nudgequeue.ErrNudgeAuthorizationDenied)
	}
	return nil
}

func isNilProductionSessionNudgeAuthority(authority productionSessionNudgeAuthority) bool {
	if authority == nil {
		return true
	}
	value := reflect.ValueOf(authority)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
