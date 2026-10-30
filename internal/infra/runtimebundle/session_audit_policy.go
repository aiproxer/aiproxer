package runtimebundle

import (
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/accessmode"
	coreauth "github.com/aiproxer/aiproxer/internal/core/auth"
	"github.com/aiproxer/aiproxer/internal/core/config"
	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
)

func buildSessionAuditPolicy(cfg *config.Config) (coreauth.SessionAuditPolicy, error) {
	if cfg == nil {
		return coreauth.SessionAuditPolicy{}, fmt.Errorf("runtimebundle: nil config")
	}
	m, err := cfg.EffectiveAccessMode()
	if err != nil {
		return coreauth.SessionAuditPolicy{}, fmt.Errorf("runtimebundle: session audit policy: %w", err)
	}
	h, rl := cfg.EffectiveAuthForAudit()
	hk, err := parseAuditHandlerKind(h)
	if err != nil {
		return coreauth.SessionAuditPolicy{}, err
	}
	lvl, err := parseAuditRequiredLevel(rl)
	if err != nil {
		return coreauth.SessionAuditPolicy{}, err
	}
	var access sdkauth.AccessMode
	switch m {
	case accessmode.ModeMultiUser:
		access = sdkauth.AccessMultiUser
	default:
		access = sdkauth.AccessSingleUser
	}
	return coreauth.SessionAuditPolicy{
		AccessMode:    access,
		HandlerKind:   hk,
		RequiredLevel: lvl,
	}, nil
}

func parseAuditHandlerKind(s string) (sdkauth.HandlerKind, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case string(sdkauth.HandlerLocalNoop):
		return sdkauth.HandlerLocalNoop, nil
	case string(sdkauth.HandlerLocalAPIKey):
		return sdkauth.HandlerLocalAPIKey, nil
	case string(sdkauth.HandlerRemote):
		return sdkauth.HandlerRemote, nil
	default:
		return "", fmt.Errorf("runtimebundle: unknown auth handler %q for audit policy", s)
	}
}

func parseAuditRequiredLevel(s string) (sdkauth.RequiredLevel, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case string(sdkauth.LevelNone):
		return sdkauth.LevelNone, nil
	case string(sdkauth.LevelAPIKey):
		return sdkauth.LevelAPIKey, nil
	case string(sdkauth.LevelAPIKeySSO):
		return sdkauth.LevelAPIKeySSO, nil
	default:
		return "", fmt.Errorf("runtimebundle: unknown auth required_level %q for audit policy", s)
	}
}
