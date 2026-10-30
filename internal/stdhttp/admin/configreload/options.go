package configreload

import (
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/internal/core/accessmode"
	"github.com/aiproxer/aiproxer/internal/core/config"
)

// Fixed management paths (design Management API; req 12.3, 12.11).
const (
	ReloadPath = "/admin/config/reload"
	StatusPath = "/admin/config/status"
)

// DefaultListenAddress is the loopback-default management bind (req 12.2).
const DefaultListenAddress = "127.0.0.1:9090"

// DefaultMaxBodyBytes bounds reload request bodies (req 12.6-12.7).
const DefaultMaxBodyBytes int64 = 64

// MinBearerSecretRunes is the minimum dedicated management bearer length
// (aligned with local API key floor; req 12.5-12.6).
const MinBearerSecretRunes = 16

// DefaultShutdownTimeout bounds management HTTP drain on Shutdown.
const DefaultShutdownTimeout = 5 * time.Second

// AuthMode selects the startup-fixed management authentication posture.
type AuthMode string

const (
	// AuthModeLocalTrust permits unauthenticated local single-user loopback only.
	AuthModeLocalTrust AuthMode = "local_trust"
	// AuthModeBearer requires a dedicated Authorization: Bearer secret.
	AuthModeBearer AuthMode = "bearer"
	// AuthModeInjected requires a caller-supplied Authenticator.
	AuthModeInjected AuthMode = "injected"
)

// Authenticator is an optional injected administrator authenticator (req 12.5).
// Cookie-based browser authentication must not authorize reload.
type Authenticator interface {
	Authorize(*http.Request) error
}

// Options configures the startup-fixed management server (req 12.11).
// Fields are process-owned; they are not taken from request bodies.
type Options struct {
	// Address is the management listen address. Empty defaults to DefaultListenAddress.
	Address string
	// AccessMode is the process access mode (single_user or multi_user).
	// Empty defaults to single_user.
	AccessMode accessmode.Mode
	// AuthMode selects authentication. Empty auto-selects local_trust only when
	// single_user + loopback; otherwise startup validation fails closed.
	AuthMode AuthMode
	// BearerToken is the dedicated management secret for AuthModeBearer.
	BearerToken string
	// Authenticator is required when AuthModeInjected.
	Authenticator Authenticator
	// AllowNonLoopback must be true to bind a non-loopback address (req 12.6).
	AllowNonLoopback bool
	// AllowOrigins is the exact Origin allowlist; default empty rejects all Origins (req 12.7).
	AllowOrigins map[string]struct{}
	// MaxBodyBytes bounds POST bodies; <=0 uses DefaultMaxBodyBytes.
	MaxBodyBytes int64
	// ShutdownTimeout bounds Server.Shutdown; <=0 uses DefaultShutdownTimeout.
	ShutdownTimeout time.Duration
}

// Validate checks startup-fixed management posture (req 12.5-12.6, 12.11).
func (o Options) Validate() error {
	addr := strings.TrimSpace(o.Address)
	if addr == "" {
		addr = DefaultListenAddress
	}
	loopback := config.IsExplicitLoopbackListenAddress(addr)
	if !loopback && !o.AllowNonLoopback {
		return fmt.Errorf("configreload management: non-loopback address %q requires AllowNonLoopback", addr)
	}

	mode := o.AccessMode
	if mode == "" {
		mode = accessmode.ModeSingleUser
	}
	switch mode {
	case accessmode.ModeSingleUser, accessmode.ModeMultiUser:
	default:
		return fmt.Errorf("configreload management: unknown access mode %q", mode)
	}

	auth := o.AuthMode
	if auth == "" {
		if mode == accessmode.ModeSingleUser && loopback {
			auth = AuthModeLocalTrust
		} else {
			return fmt.Errorf("configreload management: AuthMode required for multi_user or non-loopback bind")
		}
	}

	strongRequired := mode == accessmode.ModeMultiUser || !loopback
	switch auth {
	case AuthModeLocalTrust:
		if strongRequired {
			return fmt.Errorf("configreload management: local_trust allowed only for single_user loopback")
		}
	case AuthModeBearer:
		token := strings.TrimSpace(o.BearerToken)
		if utf8.RuneCountInString(token) < MinBearerSecretRunes {
			return fmt.Errorf("configreload management: bearer token must be at least %d Unicode code points", MinBearerSecretRunes)
		}
	case AuthModeInjected:
		if o.Authenticator == nil {
			return fmt.Errorf("configreload management: AuthModeInjected requires Authenticator")
		}
	default:
		return fmt.Errorf("configreload management: unknown AuthMode %q", auth)
	}
	if strongRequired && auth == AuthModeLocalTrust {
		return fmt.Errorf("configreload management: multi_user/non-loopback requires strong dedicated auth")
	}
	return nil
}

func (o Options) resolved() Options {
	out := o
	if strings.TrimSpace(out.Address) == "" {
		out.Address = DefaultListenAddress
	}
	if out.AccessMode == "" {
		out.AccessMode = accessmode.ModeSingleUser
	}
	if out.AuthMode == "" && out.AccessMode == accessmode.ModeSingleUser &&
		config.IsExplicitLoopbackListenAddress(out.Address) {
		out.AuthMode = AuthModeLocalTrust
	}
	if out.MaxBodyBytes <= 0 {
		out.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if out.ShutdownTimeout <= 0 {
		out.ShutdownTimeout = DefaultShutdownTimeout
	}
	if out.AllowOrigins == nil {
		out.AllowOrigins = map[string]struct{}{}
	}
	return out
}
