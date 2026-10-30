package config

import "errors"

// Sentinel errors for access/auth validation. Use [errors.Is] after [Validate] or [LoadFile].
var (
	ErrInvalidAuthEventDelivery             = errors.New("config: invalid auth.event_delivery")
	ErrInvalidAuthEventFailurePolicy        = errors.New("config: invalid auth.event_failure_policy")
	ErrAuthLocalAPIKeysRequired             = errors.New("config: auth.local_api_keys required for local_api_key handler")
	ErrAuthLocalAPIKeysRequiredForRemoteSSO = errors.New("config: auth.local_api_keys required for remote api_key_sso")
)
