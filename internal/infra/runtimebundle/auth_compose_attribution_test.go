package runtimebundle_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	coreauth "github.com/aiproxer/aiproxer/internal/core/auth"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	stdhttpauth "github.com/aiproxer/aiproxer/internal/stdhttp/auth"
	"github.com/aiproxer/aiproxer/internal/testkit"
	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
	"gopkg.in/yaml.v3"
)

const (
	tenantAttributionKeyA = "tenant-a-key-0123456789"
	tenantAttributionKeyB = "tenant-b-key-0123456789"
)

// tenantAttributionConfig declares the same principal and project under two
// tenants, with every safe attribution field populated.
func tenantAttributionConfig(handler, requiredLevel string) *config.Config {
	var empty yaml.Node
	return &config.Config{
		Access: config.AccessConfig{Mode: "multi_user"},
		Auth: config.AuthConfig{
			Handler:       handler,
			RequiredLevel: requiredLevel,
			LocalAPIKeys: []config.AuthLocalAPIKeyRecord{
				{
					KeyID: "key-a", PrincipalID: "alice", Key: tenantAttributionKeyA,
					Attribution: tenantTestAttribution("tenant-a"),
				},
				{
					KeyID: "key-b", PrincipalID: "alice", Key: tenantAttributionKeyB,
					Attribution: tenantTestAttribution("tenant-b"),
				},
			},
		},
		Server:     config.ServerConfig{Address: "127.0.0.1:8080", AuthMode: config.AuthModeExternal},
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{
				Kind: "openai-responses", ID: "be", Enabled: true, Config: empty,
			}},
		},
	}
}

func tenantTestAttribution(tenantID string) config.AuthLocalAttribution {
	return config.AuthLocalAttribution{
		DisplayName:    "Alice Operator",
		AuthMethod:     "operator_key",
		TenantID:       tenantID,
		OrganizationID: "org-1",
		WorkspaceID:    "ws-1",
		ProjectID:      "proj-1",
		DepartmentID:   "dept-1",
		CostCenterID:   "cc-1",
		Roles:          []string{"operator", "auditor"},
		SafeClaims:     map[string]string{"tier": "gold"},
		PolicyLabels:   map[string]string{"region": "eu"},
	}
}

// TestBuild_composedLocalAPIKey_preservesAttributionAcrossTenants proves the
// composed local_api_key path carries the operator-configured attribution into
// the trusted HTTP scope, and that the same principal and project in two
// tenants become distinct memory owners instead of aliasing one bank.
func TestBuild_composedLocalAPIKey_preservesAttributionAcrossTenants(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	cfg := tenantAttributionConfig("local_api_key", "api_key")
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	_, cand := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{PluginRegistry: reg})

	got := make([]scope.PrincipalScopeView, 0, 2)
	for _, bearer := range []string{tenantAttributionKeyA, tenantAttributionKeyB} {
		var observed scope.PrincipalScopeView
		var found bool
		h := stdhttpauth.Middleware(nil, runtimebundle.CandidateHTTPAuthProviders(cand), http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			observed, found = httpauth.ScopeFromContext(r.Context())
		}))
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("bearer key %q: status %d", keyIDForTest(bearer), rec.Code)
		}
		if !found {
			t.Fatalf("bearer key %q: trusted scope missing from request context", keyIDForTest(bearer))
		}
		got = append(got, observed)
	}

	assertTenantAttributionScope(t, got[0], "tenant-a")
	assertTenantAttributionScope(t, got[1], "tenant-b")

	pa, reason, ok := proxymemory.CheckEligible(proxymemory.Config{Principals: []string{"*"}}, got[0])
	if !ok {
		t.Fatalf("tenant-a principal ineligible: %s", reason)
	}
	pb, reason, ok := proxymemory.CheckEligible(proxymemory.Config{Principals: []string{"*"}}, got[1])
	if !ok {
		t.Fatalf("tenant-b principal ineligible: %s", reason)
	}
	if pa.PrincipalID != pb.PrincipalID || pa.PrincipalID != "alice" {
		t.Fatalf("want one shared principal for both tenants, got %q and %q", pa.PrincipalID, pb.PrincipalID)
	}
	ownerA := proxymemory.Owner{TenantID: pa.TenantID, PrincipalID: pa.PrincipalID, ProjectKey: "shared-project"}
	ownerB := proxymemory.Owner{TenantID: pb.TenantID, PrincipalID: pb.PrincipalID, ProjectKey: "shared-project"}
	if ownerA == ownerB {
		t.Fatalf("memory owner aliases across tenants: %+v", ownerA)
	}
	if ownerA.TenantID != "tenant-a" || ownerB.TenantID != "tenant-b" {
		t.Fatalf("memory owner tenants: %q and %q", ownerA.TenantID, ownerB.TenantID)
	}
}

// TestBuild_composedRemoteAPIKeySSO_preservesLocalAttribution proves the
// remote/api_key_sso composition branch builds its local API-key leg from the
// same attributed record conversion.
func TestBuild_composedRemoteAPIKeySSO_preservesLocalAttribution(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	cfg := tenantAttributionConfig("remote", "api_key_sso")
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	_, cand := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
		Auth: runtimebundle.AuthOptions{
			RemoteDecider: &testkit.StubRemoteDecider{
				Decision: sdkauth.Decision{Outcome: sdkauth.OutcomeAllow},
			},
		},
	})
	providers := runtimebundle.CandidateHTTPAuthProviders(cand)
	if len(providers) != 1 {
		t.Fatalf("providers: want 1, got %d", len(providers))
	}
	pp, ok := providers[0].(*stdhttpauth.PolicyProvider)
	if !ok {
		t.Fatalf("provider type: %T", providers[0])
	}
	pa, ok := pp.Auth.(*coreauth.PolicyAuthenticator)
	if !ok {
		t.Fatalf("authenticator type: %T", pp.Auth)
	}
	if pa.APIKey == nil {
		t.Fatal("api_key_sso composition did not build its local API-key leg")
	}
	d, err := pa.APIKey.Authenticate(context.Background(), sdkauth.InboundCallMeta{
		AuthorizationBearer: "Bearer " + tenantAttributionKeyA,
	})
	if err != nil {
		t.Fatal(err)
	}
	built, err := coreauth.BuildScope(coreauth.ScopeBuildInput{Decision: d})
	if err != nil {
		t.Fatal(err)
	}
	assertTenantAttributionScope(t, built.Scope, "tenant-a")
}

func assertTenantAttributionScope(t *testing.T, got scope.PrincipalScopeView, wantTenant string) {
	t.Helper()
	values := []struct {
		name string
		got  scope.Value
		want string
	}{
		{"principal_id", got.PrincipalID, "alice"},
		{"tenant_id", got.TenantID, wantTenant},
		{"display_name", got.DisplayName, "Alice Operator"},
		{"auth_method", got.AuthMethod, "operator_key"},
		{"organization_id", got.OrganizationID, "org-1"},
		{"workspace_id", got.WorkspaceID, "ws-1"},
		{"project_id", got.ProjectID, "proj-1"},
		{"department_id", got.DepartmentID, "dept-1"},
		{"cost_center_id", got.CostCenterID, "cc-1"},
	}
	for _, v := range values {
		if !v.got.IsKnown() || v.got.String() != v.want {
			t.Errorf("%s: known=%v value=%q, want %q", v.name, v.got.IsKnown(), v.got.String(), v.want)
		}
	}
	if !slices.Equal(got.Roles, []string{"operator", "auditor"}) {
		t.Errorf("roles: %#v", got.Roles)
	}
	if got.SafeClaims["tier"] != "gold" || len(got.SafeClaims) != 1 {
		t.Errorf("safe_claims: %#v", got.SafeClaims)
	}
	if got.PolicyLabels["region"] != "eu" || len(got.PolicyLabels) != 1 {
		t.Errorf("policy_labels: %#v", got.PolicyLabels)
	}
}

// keyIDForTest names a test credential without printing its secret.
func keyIDForTest(bearer string) string {
	switch bearer {
	case tenantAttributionKeyA:
		return "key-a"
	case tenantAttributionKeyB:
		return "key-b"
	default:
		return "unknown-key"
	}
}
