package runtime

import "context"

// RuntimeBinding is an immutable execution-location decision persisted on an
// exact session ID. It contains no credentials. Selector identifies the runtime
// factory; Endpoint and ResourceDomain are the pinned execution/admission scope.
// PolicyDigest identifies the policy under which a new launch was authorized.
type RuntimeBinding struct { //nolint:revive // Keep the cross-package routing/admission contract explicit.
	Selector       string `json:"selector"`
	Endpoint       string `json:"endpoint,omitempty"`
	ResourceDomain string `json:"resource_domain"`
	PolicyDigest   string `json:"policy_digest"`
}

// SessionRuntimeProvider exposes routing identity to admission. Binding is not
// admission: callers must independently authorize resources before Start.
type SessionRuntimeProvider interface {
	BindRuntime(ctx context.Context, sessionID, template string) (RuntimeBinding, error)
	ResolveRuntime(name string) (RuntimeBinding, error)
}
