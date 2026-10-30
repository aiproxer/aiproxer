package accessmode

import "errors"

// Sentinel errors for [ValidatePosture] and [NormalizeMode]. Use [errors.Is] in tests and callers.
var (
	ErrUnknownAccessMode = errors.New("access.mode: unknown value")

	ErrSingleUserBroadBind          = errors.New("access.mode: single_user must not bind to all interfaces")
	ErrSingleUserNonLoopback        = errors.New("access.mode: single_user allows only loopback listener addresses")
	ErrSingleUserMalformedAddress   = errors.New("access.mode: single_user requires a valid loopback server.address")
	ErrSingleUserUnknownListenClass = errors.New("access.mode: single_user: unknown listen classification")

	ErrMultiUserIncompatibleNoAuth   = errors.New("access.mode: multi_user is incompatible with server.auth_mode no_auth")
	ErrMultiUserHandlerRequired      = errors.New("access.mode: multi_user requires auth.handler")
	ErrMultiUserLocalNoopDisallowed  = errors.New("access.mode: multi_user requires authentication stronger than local_noop")
	ErrMultiUserRequiredLevelTooWeak = errors.New("access.mode: multi_user requires auth.required_level beyond none")
	ErrMultiUserUnknownHandler       = errors.New("access.mode: multi_user unknown auth.handler")
	ErrMultiUserUnknownRequiredLevel = errors.New("access.mode: multi_user unknown auth.required_level")
	ErrMultiUserInternalUnknownMode  = errors.New("access.mode: internal error, unknown mode")

	ErrMultiUserFlagRequired     = errors.New("access.mode: multi_user requires the --multi-user flag on serve")
	ErrMultiUserFlagInconsistent = errors.New("access.mode: --multi-user flag is inconsistent with non-multi_user config")
)

// ErrMalformedListenAddress is returned by [ClassifyListenAddress] when the address is not
// a usable host:port bind string (e.g. empty, missing port, or unparseable).
var ErrMalformedListenAddress = errors.New("accessmode: malformed listen address")
