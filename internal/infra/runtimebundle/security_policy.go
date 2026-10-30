package runtimebundle

import (
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/accessmode"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
)

func validateBackendSecurityProfiles(cfg *config.Config, reg *pluginreg.Registry) error {
	if cfg == nil || reg == nil {
		return nil
	}
	accessMode, err := cfg.EffectiveAccessMode()
	if err != nil {
		return fmt.Errorf("runtimebundle: backend security profile validation: %w", err)
	}
	multiUser := accessMode == accessmode.ModeMultiUser
	multiUserPolicy := standardplugins.HostMultiUserBackendPolicy()
	for _, p := range cfg.Plugins.Backends {
		if !p.Enabled {
			continue
		}
		factoryID := p.FactoryID()
		profile, ok := reg.BackendSecurityProfile(factoryID)
		if !ok {
			return fmt.Errorf(
				"runtimebundle: backend instance %q (factory %q): missing security profile",
				p.InstanceID(),
				factoryID,
			)
		}
		if err := validateBackendAccessScope(profile.AccessScope, p.InstanceID(), factoryID, multiUser); err != nil {
			return err
		}
		if err := validateBackendCredentialMode(profile.CredentialMode, p.InstanceID(), factoryID, multiUser); err != nil {
			return err
		}
		if err := validateBackendMultiUserApproval(multiUserPolicy, factoryID, p.InstanceID(), multiUser); err != nil {
			return err
		}
	}
	return nil
}

func validateBackendMultiUserApproval(
	policy standardplugins.MultiUserBackendPolicy,
	factoryID, instanceID string,
	multiUser bool,
) error {
	if !multiUser {
		return nil
	}
	if policy.IsApproved(factoryID) {
		return nil
	}
	return fmt.Errorf(
		"%w (instance %q factory %q): the standard distribution has not approved this backend for shared multi-user use; run a single-user loopback deployment instead, or use a backend whose credential is an operator API key, workload identity, or service account, and add an explicit entry to the host-owned multi-user approval policy",
		ErrBackendNotApprovedForMultiUser,
		instanceID,
		factoryID,
	)
}

func validateBackendAccessScope(scope pluginreg.BackendAccessScope, instanceID, factoryID string, multiUser bool) error {
	if scope == "" {
		scope = pluginreg.BackendAccessAny
	}
	switch scope {
	case pluginreg.BackendAccessAny:
		return nil
	case pluginreg.BackendAccessLocalOnly:
		if multiUser {
			return fmt.Errorf(
				"%w (instance %q factory %q)",
				ErrLocalOnlyBackendDisallowedMultiUser,
				instanceID,
				factoryID,
			)
		}
		return nil
	default:
		return fmt.Errorf(
			"%w (instance %q factory %q access_scope %q)",
			ErrUnsupportedBackendAccessScope,
			instanceID,
			factoryID,
			strings.TrimSpace(string(scope)),
		)
	}
}

func validateBackendCredentialMode(mode pluginreg.BackendCredentialMode, instanceID, factoryID string, multiUser bool) error {
	switch mode {
	case pluginreg.CredentialStatic, pluginreg.CredentialWorkload, pluginreg.CredentialNone:
		return nil
	case pluginreg.CredentialOAuthUser:
		if multiUser {
			return fmt.Errorf(
				"%w (instance %q factory %q)",
				ErrOAuthUserDisallowedMultiUser,
				instanceID,
				factoryID,
			)
		}
		return nil
	case pluginreg.CredentialUnknown, "":
		if multiUser {
			return fmt.Errorf(
				"%w (instance %q factory %q)",
				ErrUnknownCredentialMultiUser,
				instanceID,
				factoryID,
			)
		}
		return nil
	default:
		return fmt.Errorf(
			"%w (instance %q factory %q mode %q)",
			ErrUnsupportedBackendCredentialMode,
			instanceID,
			factoryID,
			strings.TrimSpace(string(mode)),
		)
	}
}
