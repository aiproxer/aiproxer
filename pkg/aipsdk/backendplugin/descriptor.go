package backendplugin

import (
	"fmt"
	"strings"
)

// Validate reports whether the descriptor is a valid v1 export table.
func (d PluginDescriptor) Validate() error {
	if d.ProtocolMajor != ProtocolMajorV1 {
		return ErrInvalidDescriptor
	}
	if strings.TrimSpace(d.PluginID) == "" || strings.TrimSpace(d.Version) == "" {
		return ErrInvalidDescriptor
	}
	if len(d.Factories) == 0 {
		return ErrInvalidDescriptor
	}
	if _, err := indexFeatures(d.Features); err != nil {
		return err
	}
	seen := map[string]struct{}{}
	for _, f := range d.Factories {
		kind := strings.TrimSpace(f.Kind)
		if kind == "" {
			return ErrInvalidDescriptor
		}
		if _, ok := seen[kind]; ok {
			return ErrInvalidDescriptor
		}
		seen[kind] = struct{}{}
		if err := ValidateCredentialMode(f.CredentialMode); err != nil {
			return err
		}
		if err := ValidateAccessScope(f.AccessScope); err != nil {
			return err
		}
		if err := f.ProcessSharing.Validate(); err != nil {
			return err
		}
		if err := validateFactorySecurityPosture(kind, f.CredentialMode, f.AccessScope); err != nil {
			return err
		}
	}
	return nil
}

// validateFactorySecurityPosture rejects a factory whose declared credential mode
// contradicts its declared access scope. It never infers a value: both fields stay
// mandatory declarations and this check only proves they agree.
//
// credential_mode: oauth_user is a user-scoped credential. The host principal and
// credential model never multiplexes such a credential across unrelated principals,
// so an oauth_user factory must declare access_scope: local_only. This mirrors
// manifest.ErrInconsistentExportSecurityPosture so the runtime descriptor and the
// packaged manifest cannot disagree.
func validateFactorySecurityPosture(kind string, cred CredentialMode, scope AccessScope) error {
	if cred != CredentialModeOAuthUser {
		return nil
	}
	if scope == AccessScopeLocalOnly {
		return nil
	}
	return fmt.Errorf(
		"%w: %w: factory %q declares credential_mode %q which requires access_scope %q, got %q",
		ErrInvalidDescriptor,
		ErrInconsistentSecurityPosture,
		kind,
		CredentialModeOAuthUser,
		AccessScopeLocalOnly,
		scope,
	)
}
