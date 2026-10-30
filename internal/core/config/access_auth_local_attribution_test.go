package config_test

import (
	"errors"
	"strings"
	"testing"

	coreauth "github.com/aiproxer/aiproxer/internal/core/auth"
	"github.com/aiproxer/aiproxer/internal/core/config"
)

func localAPIKeyAttributionRecord() config.AuthLocalAPIKeyRecord {
	return config.AuthLocalAPIKeyRecord{
		KeyID:       "k1",
		PrincipalID: "svc-1",
		Key:         "test-local-api-key-16",
		Attribution: config.AuthLocalAttribution{
			DisplayName:    "Service One",
			AuthMethod:     "local_api_key",
			TenantID:       "t1",
			OrganizationID: "org-1",
			WorkspaceID:    "ws-1",
			ProjectID:      "proj-1",
			DepartmentID:   "dept-1",
			CostCenterID:   "cc-1",
			Roles:          []string{"reader", "writer"},
			SafeClaims:     map[string]string{"team": "core"},
			PolicyLabels:   map[string]string{"env": "prod"},
		},
	}
}

// TestValidate_auth_localAPIKeyAttributionAccepted proves operator-controlled safe
// attribution is accepted at startup (requirement 3.1, 2.5).
func TestValidate_auth_localAPIKeyAttributionAccepted(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Access: config.AccessConfig{Mode: "multi_user"},
		Server: config.ServerConfig{Address: "127.0.0.1:8080", AuthMode: config.AuthModeExternal},
		Auth: config.AuthConfig{
			Handler:       "local_api_key",
			RequiredLevel: "api_key",
			LocalAPIKeys:  []config.AuthLocalAPIKeyRecord{localAPIKeyAttributionRecord()},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins:    minimalPlugins(),
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// TestValidate_auth_localAPIKeyAttributionMissingOptionalStaysUnknown proves a record with
// no attribution validates (missing optional fields remain unknown, requirement 3.5).
func TestValidate_auth_localAPIKeyAttributionMissingOptionalStaysUnknown(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Access: config.AccessConfig{Mode: "single_user"},
		Server: config.ServerConfig{Address: "127.0.0.1:8080"},
		Auth: config.AuthConfig{
			Handler:       "local_api_key",
			RequiredLevel: "api_key",
			LocalAPIKeys: []config.AuthLocalAPIKeyRecord{
				{KeyID: "k1", PrincipalID: "p1", Key: "test-local-api-key-16"},
			},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins:    minimalPlugins(),
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// TestValidate_auth_localAPIKeyAttributionRejectsEmptyRole proves roles entries must be
// non-empty when provided (requirement 3.2, 5.4).
func TestValidate_auth_localAPIKeyAttributionRejectsEmptyRole(t *testing.T) {
	t.Parallel()
	rec := localAPIKeyAttributionRecord()
	rec.Attribution.Roles = []string{"reader", "  "}
	cfg := &config.Config{
		Access: config.AccessConfig{Mode: "single_user"},
		Server: config.ServerConfig{Address: "127.0.0.1:8080"},
		Auth: config.AuthConfig{
			Handler:       "local_api_key",
			RequiredLevel: "api_key",
			LocalAPIKeys:  []config.AuthLocalAPIKeyRecord{rec},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins:    minimalPlugins(),
	}
	err := config.Validate(cfg)
	if err == nil || !errors.Is(err, coreauth.ErrInvalidLocalAttribution) {
		t.Fatalf("want ErrInvalidLocalAttribution, got %v", err)
	}
}

// TestValidate_auth_localAPIKeyAttributionRejectsEmptySafeClaimKey proves safe claim keys
// must be non-empty trimmed (requirement 3.2).
func TestValidate_auth_localAPIKeyAttributionRejectsEmptySafeClaimKey(t *testing.T) {
	t.Parallel()
	rec := localAPIKeyAttributionRecord()
	rec.Attribution.SafeClaims = map[string]string{"": "v"}
	cfg := &config.Config{
		Access: config.AccessConfig{Mode: "single_user"},
		Server: config.ServerConfig{Address: "127.0.0.1:8080"},
		Auth: config.AuthConfig{
			Handler:       "local_api_key",
			RequiredLevel: "api_key",
			LocalAPIKeys:  []config.AuthLocalAPIKeyRecord{rec},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins:    minimalPlugins(),
	}
	err := config.Validate(cfg)
	if err == nil || !errors.Is(err, coreauth.ErrInvalidLocalAttribution) {
		t.Fatalf("want ErrInvalidLocalAttribution, got %v", err)
	}
}

// TestValidate_auth_localAPIKeyAttributionRejectsCredentialLikeValue proves unsafe
// attribution values are rejected at startup (requirement 2.6, 5.4).
func TestValidate_auth_localAPIKeyAttributionRejectsCredentialLikeValue(t *testing.T) {
	t.Parallel()
	rec := localAPIKeyAttributionRecord()
	rec.Attribution.DisplayName = "Bearer abcdef0123456789"
	cfg := &config.Config{
		Access: config.AccessConfig{Mode: "single_user"},
		Server: config.ServerConfig{Address: "127.0.0.1:8080"},
		Auth: config.AuthConfig{
			Handler:       "local_api_key",
			RequiredLevel: "api_key",
			LocalAPIKeys:  []config.AuthLocalAPIKeyRecord{rec},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins:    minimalPlugins(),
	}
	err := config.Validate(cfg)
	if err == nil || !errors.Is(err, coreauth.ErrInvalidLocalAttribution) {
		t.Fatalf("want ErrInvalidLocalAttribution, got %v", err)
	}
	if !strings.Contains(err.Error(), "display_name") {
		t.Fatalf("error should name the offending field: %v", err)
	}
}

// TestValidateAuthLocalAPIKeyRecords_attributionConverted proves the config validator
// forwards attribution into core auth records.
func TestValidateAuthLocalAPIKeyRecords_attributionConverted(t *testing.T) {
	t.Parallel()
	rec := localAPIKeyAttributionRecord()
	if err := config.ValidateAuthLocalAPIKeyRecords([]config.AuthLocalAPIKeyRecord{rec}); err != nil {
		t.Fatalf("ValidateAuthLocalAPIKeyRecords: %v", err)
	}
}
