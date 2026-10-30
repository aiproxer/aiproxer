package accessmode

import (
	"fmt"
	"strings"

	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
)

// PostureInput carries effective access, listener, and auth policy for startup validation.
// LegacyAuthMode is the raw server.auth_mode value ("", "no_auth", "external").
type PostureInput struct {
	Mode           Mode
	Listen         ListenClassification
	Handler        string
	RequiredLevel  string
	LegacyAuthMode string
}

// ValidatePosture enforces access-mode and authentication posture rules (tasks 2.3, design AccessMode).
// It does not validate backend credential eligibility (separate task 7.x).
func ValidatePosture(in PostureInput) error {
	switch in.Mode {
	case ModeSingleUser:
		return validateSingleUserListen(in)
	case ModeMultiUser:
		return validateMultiUserAuth(in)
	default:
		return fmt.Errorf("%w: %q", ErrMultiUserInternalUnknownMode, in.Mode)
	}
}

func validateSingleUserListen(in PostureInput) error {
	switch in.Listen.Surface {
	case SurfaceLoopback:
		return nil
	case SurfaceMalformed:
		return fmt.Errorf(
			"%w (127.0.0.1, ::1, or localhost with a port); got malformed %q",
			ErrSingleUserMalformedAddress,
			strings.TrimSpace(in.Listen.Raw),
		)
	case SurfaceBroad:
		return fmt.Errorf(
			"%w; use a loopback address (e.g. 127.0.0.1:8080) or set access.mode to multi_user with authentication",
			ErrSingleUserBroadBind,
		)
	case SurfaceNonLoopback:
		return fmt.Errorf(
			"%w; got non-loopback bind %q",
			ErrSingleUserNonLoopback,
			strings.TrimSpace(in.Listen.Raw),
		)
	default:
		return fmt.Errorf("%w: %q", ErrSingleUserUnknownListenClass, in.Listen.Surface)
	}
}

func validateMultiUserAuth(in PostureInput) error {
	legacy := strings.ToLower(strings.TrimSpace(in.LegacyAuthMode))
	if legacy == "no_auth" {
		return fmt.Errorf(
			"%w; set server.auth_mode: external and configure auth.handler (or omit access.mode for single-user)",
			ErrMultiUserIncompatibleNoAuth,
		)
	}

	h := strings.ToLower(strings.TrimSpace(in.Handler))
	rl := strings.ToLower(strings.TrimSpace(in.RequiredLevel))

	if h == "" {
		return fmt.Errorf("%w", ErrMultiUserHandlerRequired)
	}
	if h == string(sdkauth.HandlerLocalNoop) {
		return fmt.Errorf(
			"%w (configure auth.handler local_api_key or remote)",
			ErrMultiUserLocalNoopDisallowed,
		)
	}
	if rl == "" || rl == string(sdkauth.LevelNone) {
		return fmt.Errorf(
			"%w (e.g. api_key or api_key_sso)",
			ErrMultiUserRequiredLevelTooWeak,
		)
	}

	switch h {
	case string(sdkauth.HandlerLocalAPIKey), string(sdkauth.HandlerRemote):
	default:
		return fmt.Errorf("%w: %q", ErrMultiUserUnknownHandler, in.Handler)
	}

	switch rl {
	case string(sdkauth.LevelAPIKey), string(sdkauth.LevelAPIKeySSO):
	default:
		return fmt.Errorf("%w: %q", ErrMultiUserUnknownRequiredLevel, in.RequiredLevel)
	}

	return nil
}
