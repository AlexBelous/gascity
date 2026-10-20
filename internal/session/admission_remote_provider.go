package session

import (
	"context"
	"errors"

	"github.com/gastownhall/gascity/internal/runtime"
)

// RemoteAdmissionProfileProvider is an optional trusted provider capability.
// Implementations supply operator-validated profiles; session beads and model
// input cannot manufacture this capability.
type RemoteAdmissionProfileProvider interface {
	RemoteAdmissionProfiles() []RemoteAdmissionProfile
}

// ContextWithProviderRemoteAdmission copies provider profiles into a context
// only when no explicit registry exists. Explicit registries never fall back;
// conflicts in matching immutable policy descriptors deny before side effects.
func ContextWithProviderRemoteAdmission(ctx context.Context, provider runtime.Provider) (context.Context, error) {
	capability, ok := provider.(RemoteAdmissionProfileProvider)
	if !ok {
		return ctx, nil
	}
	supplied := capability.RemoteAdmissionProfiles()
	providerCtx, err := WithRemoteAdmissionProfiles(context.Background(), supplied...)
	if err != nil {
		return ctx, err
	}
	candidate, _ := providerCtx.Value(remoteAdmissionProfilesKey{}).(remoteAdmissionProfiles)
	if explicit, ok := ctx.Value(remoteAdmissionProfilesKey{}).(remoteAdmissionProfiles); ok {
		for binding, profile := range explicit {
			if other, exists := candidate[binding]; exists && !sameRemoteProfile(profile, other) {
				return ctx, errors.New("remote admission provider policy conflict")
			}
		}
		return ctx, nil
	}
	return context.WithValue(ctx, remoteAdmissionProfilesKey{}, candidate), nil
}

// PreflightRemoteAdmission validates routing and its exact registered profile
// without reserving VM capacity. Controllers use it before canonical source
// preparation, then AdmitCapacityStart after the final incarnation is minted.
func PreflightRemoteAdmission(ctx context.Context, provider runtime.Provider, id, template string) (context.Context, bool, bool, string) {
	var err error
	ctx, err = ContextWithProviderRemoteAdmission(ctx, provider)
	if err != nil {
		return ctx, false, false, "remote_admission_profile_invalid"
	}
	binding, err := remoteBinding(ctx, provider, id, template)
	if err != nil {
		return ctx, false, false, "runtime_binding_invalid"
	}
	if binding == (runtime.RuntimeBinding{}) {
		return ctx, false, true, ""
	}
	profiles, _ := ctx.Value(remoteAdmissionProfilesKey{}).(remoteAdmissionProfiles)
	if _, exists := profiles[binding]; !exists {
		return ctx, true, false, "remote_admission_backend_unavailable"
	}
	return ctx, true, true, "remote_admission_preflight"
}
