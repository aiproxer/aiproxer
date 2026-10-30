package auth

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// MinLocalAPIKeyRunes is the minimum accepted length (in Unicode code points) for [LocalAPIKeyRecord.Key].
// Shorter keys are rejected at validation to reduce trivial online guessing when the listener is exposed.
const MinLocalAPIKeyRunes = 16

// LocalAttribution carries optional operator-controlled safe attribution for a local API key
// record. Zero values mean "not configured" and map to unknown scope fields (no inference).
// Raw key material, bearer tokens, and transport headers must never be placed here.
type LocalAttribution struct {
	DisplayName    string
	AuthMethod     string
	TenantID       string
	OrganizationID string
	WorkspaceID    string
	ProjectID      string
	DepartmentID   string
	CostCenterID   string
	Roles          []string
	SafeClaims     map[string]string
	PolicyLabels   map[string]string
}

// LocalAPIKeyRecord is one operator-configured API key for [LocalAPIKeyAuthenticator].
// It mirrors config-layer YAML records without importing internal/core/config.
type LocalAPIKeyRecord struct {
	KeyID       string
	PrincipalID string
	Key         string
	Attribution LocalAttribution
}

// ValidateLocalAPIKeyRecords checks records for duplicates, required fields, min key length,
// and safe attribution (non-empty roles/claim/label keys, no credential-like values).
func ValidateLocalAPIKeyRecords(records []LocalAPIKeyRecord) error {
	seen := make(map[string]struct{}, len(records))
	seenSecrets := make(map[string]struct{}, len(records))
	for i, r := range records {
		kid := strings.TrimSpace(r.KeyID)
		pid := strings.TrimSpace(r.PrincipalID)
		key := strings.TrimSpace(r.Key)
		if kid == "" && pid == "" && key == "" {
			return fmt.Errorf("auth.local_api_keys[%d]: empty record (requires key_id, principal_id, and key)", i)
		}
		if kid == "" {
			return fmt.Errorf("auth.local_api_keys[%d]: key_id is required", i)
		}
		if pid == "" {
			return fmt.Errorf("auth.local_api_keys[%d]: principal_id is required for key_id %q", i, kid)
		}
		if key == "" {
			return fmt.Errorf("%w: index %d key_id %q", ErrLocalAPIKeyEmpty, i, kid)
		}
		if n := utf8.RuneCountInString(key); n < MinLocalAPIKeyRunes {
			return fmt.Errorf(
				"auth.local_api_keys[%d]: key for key_id %q must be at least %d characters (got %d)",
				i, kid, MinLocalAPIKeyRunes, n,
			)
		}
		if _, dup := seen[kid]; dup {
			return fmt.Errorf("%w: %q", ErrDuplicateLocalAPIKeyID, kid)
		}
		seen[kid] = struct{}{}
		if _, dup := seenSecrets[key]; dup {
			return fmt.Errorf("%w: key material reused for a different key_id is not allowed", ErrDuplicateLocalAPIKeyMaterial)
		}
		seenSecrets[key] = struct{}{}
		if err := validateLocalAttribution(r.Attribution); err != nil {
			return fmt.Errorf("auth.local_api_keys[%d] key_id %q: %w", i, kid, err)
		}
	}
	return nil
}

func validateLocalAttribution(a LocalAttribution) error {
	stringFields := []struct {
		name, v string
	}{
		{"display_name", a.DisplayName},
		{"auth_method", a.AuthMethod},
		{"tenant_id", a.TenantID},
		{"organization_id", a.OrganizationID},
		{"workspace_id", a.WorkspaceID},
		{"project_id", a.ProjectID},
		{"department_id", a.DepartmentID},
		{"cost_center_id", a.CostCenterID},
	}
	for _, f := range stringFields {
		val := strings.TrimSpace(f.v)
		if val == "" {
			continue
		}
		if looksCredentialLike(val) {
			return fmt.Errorf("%w: %s contains credential-like material", ErrInvalidLocalAttribution, f.name)
		}
	}
	for i, role := range a.Roles {
		if strings.TrimSpace(role) == "" {
			return fmt.Errorf("%w: roles[%d] is empty", ErrInvalidLocalAttribution, i)
		}
		if looksCredentialLike(role) {
			return fmt.Errorf("%w: roles[%d] contains credential-like material", ErrInvalidLocalAttribution, i)
		}
	}
	if err := validateStringMapKeys(a.SafeClaims, "safe_claims"); err != nil {
		return err
	}
	if err := validateStringMapKeys(a.PolicyLabels, "policy_labels"); err != nil {
		return err
	}
	return nil
}

func validateStringMapKeys(m map[string]string, field string) error {
	for k, v := range m {
		if strings.TrimSpace(k) == "" {
			return fmt.Errorf("%w: %s has empty key", ErrInvalidLocalAttribution, field)
		}
		if looksCredentialLike(k) || looksCredentialLike(v) {
			return fmt.Errorf("%w: %s contains credential-like material", ErrInvalidLocalAttribution, field)
		}
	}
	return nil
}
