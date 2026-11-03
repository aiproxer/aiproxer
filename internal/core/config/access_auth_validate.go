package config

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/accessmode"
	coreauth "github.com/aiproxer/aiproxer/internal/core/auth"
)

// EffectiveAccessMode returns the normalized deployment access mode (omitted access.mode defaults to single_user).
func (c *Config) EffectiveAccessMode() (accessmode.Mode, error) {
	if c == nil {
		return accessmode.ModeSingleUser, nil
	}
	return accessmode.NormalizeMode(c.Access.Mode)
}

func validateAccessAuth(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	if p := strings.TrimSpace(cfg.Auth.EventFailurePolicy); p != "" {
		switch strings.ToLower(p) {
		case "best_effort", "fail_closed":
		default:
			return fmt.Errorf("%w: want best_effort or fail_closed, got %q", ErrInvalidAuthEventFailurePolicy, cfg.Auth.EventFailurePolicy)
		}
	}
	if ed := strings.TrimSpace(cfg.Auth.EventDelivery); ed != "" {
		switch strings.ToLower(ed) {
		case "default", "disabled", "custom":
		default:
			return fmt.Errorf("%w: want default, disabled, or custom, got %q", ErrInvalidAuthEventDelivery, cfg.Auth.EventDelivery)
		}
	}
	mode, err := accessmode.NormalizeMode(cfg.Access.Mode)
	if err != nil {
		return fmt.Errorf("validate access/auth: normalize access.mode: %w", err)
	}
	cls, err := accessmode.ClassifyListenAddress(cfg.Server.Address)
	if err != nil {
		return fmt.Errorf("validate access/auth: classify server.address: %w", err)
	}
	eff := effectiveAuthPolicy(cfg)
	if err := ValidateAuthLocalAPIKeyRecords(cfg.Auth.LocalAPIKeys); err != nil {
		return err
	}
	legacy := cfg.Server.AuthMode
	if legacy == "" {
		legacy = AuthModeNoAuth
	}
	if err := accessmode.ValidatePosture(accessmode.PostureInput{
		Mode:           mode,
		Listen:         cls,
		Handler:        eff.Handler,
		RequiredLevel:  eff.RequiredLevel,
		LegacyAuthMode: string(legacy),
	}); err != nil {
		return err
	}
	h := strings.ToLower(strings.TrimSpace(eff.Handler))
	if h == "local_api_key" && len(cfg.Auth.LocalAPIKeys) == 0 {
		return fmt.Errorf("%w when auth.handler is local_api_key", ErrAuthLocalAPIKeysRequired)
	}
	auditH, auditRL := cfg.EffectiveAuthForAudit()
	if strings.EqualFold(auditH, "remote") && strings.EqualFold(auditRL, "api_key_sso") && len(cfg.Auth.LocalAPIKeys) == 0 {
		return fmt.Errorf("%w when auth policy is remote with required_level api_key_sso", ErrAuthLocalAPIKeysRequiredForRemoteSSO)
	}
	return nil
}

type effectiveAuthPolicyResult struct {
	Handler       string
	RequiredLevel string
}

// ValidateAuthLocalAPIKeyRecords converts [AuthLocalAPIKeyRecord] values to core auth records and
// delegates to [coreauth.ValidateLocalAPIKeyRecords] (duplicates, required fields, min key runes).
func ValidateAuthLocalAPIKeyRecords(records []AuthLocalAPIKeyRecord) error {
	conv := make([]coreauth.LocalAPIKeyRecord, 0, len(records))
	for _, r := range records {
		conv = append(conv, r.ToCore())
	}
	return coreauth.ValidateLocalAPIKeyRecords(conv)
}

// ToCore converts one operator-configured record into the core auth record.
// It is the single conversion authority for startup validation and runtime
// composition, so safe attribution cannot be dropped at one seam only.
func (r AuthLocalAPIKeyRecord) ToCore() coreauth.LocalAPIKeyRecord {
	return coreauth.LocalAPIKeyRecord{
		KeyID:       r.KeyID,
		PrincipalID: r.PrincipalID,
		Key:         r.Key,
		Attribution: r.Attribution.ToCore(),
	}
}

// ToCore converts operator attribution into the core auth attribution. Mutable
// slice and map fields are cloned so auth records never alias config-owned state.
func (a AuthLocalAttribution) ToCore() coreauth.LocalAttribution {
	return coreauth.LocalAttribution{
		DisplayName:    a.DisplayName,
		AuthMethod:     a.AuthMethod,
		TenantID:       a.TenantID,
		OrganizationID: a.OrganizationID,
		WorkspaceID:    a.WorkspaceID,
		ProjectID:      a.ProjectID,
		DepartmentID:   a.DepartmentID,
		CostCenterID:   a.CostCenterID,
		Roles:          slices.Clone(a.Roles),
		SafeClaims:     maps.Clone(a.SafeClaims),
		PolicyLabels:   maps.Clone(a.PolicyLabels),
	}
}

// effectiveAuthPolicy maps legacy server.auth_mode and new auth.* into a single view for posture checks.
// Empty auth.handler with no_auth (or omitted) behaves like explicit local_noop + none for validation purposes.
func effectiveAuthPolicy(cfg *Config) effectiveAuthPolicyResult {
	h := strings.TrimSpace(cfg.Auth.Handler)
	rl := strings.TrimSpace(cfg.Auth.RequiredLevel)
	if h != "" {
		return effectiveAuthPolicyResult{Handler: h, RequiredLevel: rl}
	}
	switch cfg.EffectiveServerAuthMode() {
	case AuthModeNoAuth:
		return effectiveAuthPolicyResult{Handler: "local_noop", RequiredLevel: "none"}
	case AuthModeExternal:
		return effectiveAuthPolicyResult{Handler: "", RequiredLevel: ""}
	default:
		return effectiveAuthPolicyResult{Handler: "", RequiredLevel: ""}
	}
}

// EffectiveAuthForAudit returns handler and required_level strings after legacy server.auth_mode merge.
// It is used for operator audit labels (session-start) and should stay aligned with posture validation.
func (c *Config) EffectiveAuthForAudit() (handler, requiredLevel string) {
	if c == nil {
		return "local_noop", "none"
	}
	eff := effectiveAuthPolicy(c)
	h := strings.TrimSpace(eff.Handler)
	rl := strings.TrimSpace(eff.RequiredLevel)
	if h == "" && c.EffectiveServerAuthMode() == AuthModeExternal {
		if rl == "" {
			rl = "none"
		}
		return "remote", strings.ToLower(rl)
	}
	if h == "" {
		if rl == "" {
			rl = "none"
		}
		return "local_noop", strings.ToLower(rl)
	}
	if rl == "" {
		rl = "none"
	}
	return strings.ToLower(h), strings.ToLower(rl)
}
