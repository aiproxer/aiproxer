package auth

import "errors"

// ErrDuplicateLocalAPIKeyID is returned when two records share the same key_id.
var (
	ErrDuplicateLocalAPIKeyID       = errors.New("auth.local_api_keys: duplicate key_id")
	ErrDuplicateLocalAPIKeyMaterial = errors.New("auth.local_api_keys: duplicate key material")
	ErrLocalAPIKeyEmpty             = errors.New("auth.local_api_keys: key is required")
	ErrInvalidLocalAttribution      = errors.New("auth.local_api_keys: invalid attribution")

	// ErrDeniedNoScope is returned by [BuildScope] when the auth decision is not an allow,
	// so denied or challenged requests do not create a successful lifecycle scope.
	ErrDeniedNoScope = errors.New("auth: denied or challenged decision has no lifecycle scope")
	// ErrNoIdentity is returned by [BuildScope] when an allow decision carries no trusted
	// scope, no legacy principal, and no local fallback is permitted.
	ErrNoIdentity = errors.New("auth: no trusted identity or local fallback for scope")
	// ErrUnsafeScope is returned by [BuildScope] when a trusted scope value looks like
	// credential material and is rejected before entering request lifecycle evidence.
	ErrUnsafeScope = errors.New("auth: scope value rejected as unsafe")
)
