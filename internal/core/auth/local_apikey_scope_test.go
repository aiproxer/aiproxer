package auth

import (
	"context"
	"strings"
	"testing"

	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// TestLocalAPIKeyAuthenticator_validBearerBuildsScope proves a matched local API key
// populates the authoritative scope from validated attribution (requirement 1.4, 2.5, 3.1).
func TestLocalAPIKeyAuthenticator_validBearerBuildsScope(t *testing.T) {
	t.Parallel()
	secret := "my-api-key-value-16"
	a, err := NewLocalAPIKeyAuthenticator([]LocalAPIKeyRecord{
		{
			KeyID:       "app1",
			PrincipalID: "svc-1",
			Key:         secret,
			Attribution: LocalAttribution{
				DisplayName:  "Service One",
				TenantID:     "t1",
				Roles:        []string{"reader"},
				SafeClaims:   map[string]string{"team": "core"},
				PolicyLabels: map[string]string{"env": "prod"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	d, err := a.Authenticate(context.Background(), sdkauth.InboundCallMeta{
		AuthorizationBearer: "Bearer " + secret,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != sdkauth.OutcomeAllow {
		t.Fatalf("Outcome: got %v", d.Outcome)
	}
	if d.Scope == nil {
		t.Fatal("expected scope on local api key allow decision")
	}
	if d.Scope.SubjectKind != scope.SubjectService {
		t.Fatalf("SubjectKind: got %v want service", d.Scope.SubjectKind)
	}
	if !d.Scope.PrincipalID.Equal(scope.Known("svc-1")) {
		t.Fatalf("PrincipalID: %+v", d.Scope.PrincipalID)
	}
	if !d.Scope.CredentialID.Equal(scope.Known("app1")) {
		t.Fatalf("CredentialID: %+v", d.Scope.CredentialID)
	}
	if d.Scope.AuthMethod.String() != "local_api_key" {
		t.Fatalf("AuthMethod: got %q", d.Scope.AuthMethod)
	}
	if d.Scope.DisplayName.String() != "Service One" {
		t.Fatalf("DisplayName: got %q", d.Scope.DisplayName)
	}
	if !d.Scope.TenantID.Equal(scope.Known("t1")) {
		t.Fatalf("TenantID: %+v", d.Scope.TenantID)
	}
	if len(d.Scope.Roles) != 1 || d.Scope.Roles[0] != "reader" {
		t.Fatalf("Roles: %+v", d.Scope.Roles)
	}
	if d.Scope.SafeClaims["team"] != "core" {
		t.Fatalf("SafeClaims: %+v", d.Scope.SafeClaims)
	}
	if d.Scope.PolicyLabels["env"] != "prod" {
		t.Fatalf("PolicyLabels: %+v", d.Scope.PolicyLabels)
	}
	// Missing optional org fields remain unknown (requirement 3.5).
	if !d.Scope.ProjectID.IsUnknown() || !d.Scope.DepartmentID.IsUnknown() || !d.Scope.CostCenterID.IsUnknown() {
		t.Fatal("optional org fields must remain unknown when not configured")
	}
}

// TestLocalAPIKeyAuthenticator_scopeOmitsRawKey proves raw key material never enters the
// scope snapshot (requirement 2.6, 5.2).
func TestLocalAPIKeyAuthenticator_scopeOmitsRawKey(t *testing.T) {
	t.Parallel()
	secret := "my-api-key-value-16"
	a, err := NewLocalAPIKeyAuthenticator([]LocalAPIKeyRecord{
		{KeyID: "app1", PrincipalID: "svc-1", Key: secret},
	})
	if err != nil {
		t.Fatal(err)
	}
	d, err := a.Authenticate(context.Background(), sdkauth.InboundCallMeta{
		AuthorizationBearer: "Bearer " + secret,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Scope == nil {
		t.Fatal("expected scope")
	}
	blob := strings.Join([]string{
		d.Scope.PrincipalID.String(), d.Scope.CredentialID.String(),
		d.Scope.AuthMethod.String(), d.Scope.DisplayName.String(),
	}, " ")
	if strings.Contains(blob, secret) {
		t.Fatalf("scope leaked raw key material: %q", blob)
	}
}

// TestLocalAPIKeyAuthenticator_deniedHasNoScope proves a denied local api key attempt never
// carries a successful lifecycle scope (requirement 1.6).
func TestLocalAPIKeyAuthenticator_deniedHasNoScope(t *testing.T) {
	t.Parallel()
	a, err := NewLocalAPIKeyAuthenticator([]LocalAPIKeyRecord{
		{KeyID: "app1", PrincipalID: "svc-1", Key: "my-api-key-value-16"},
	})
	if err != nil {
		t.Fatal(err)
	}
	d, err := a.Authenticate(context.Background(), sdkauth.InboundCallMeta{
		AuthorizationBearer: "Bearer wrong",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != sdkauth.OutcomeDeny {
		t.Fatalf("Outcome: got %v", d.Outcome)
	}
	if d.Scope != nil {
		t.Fatalf("denied decision must not carry scope, got %+v", d.Scope)
	}
}
