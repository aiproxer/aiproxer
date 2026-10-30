package aipruntime_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipruntime"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/featurehost"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/reasoninghost"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

// fullScope returns a scope populated with all egress-relevant fields.
func fullScope() scope.PrincipalScopeView {
	return scope.PrincipalScopeView{
		SubjectKind:    scope.SubjectHuman,
		PrincipalID:    scope.Known("user-123"),
		DisplayName:    scope.Known("Alice"),
		AuthMethod:     scope.Known("oidc"),
		CredentialID:   scope.Known("cred-1"),
		Roles:          []string{"admin", "editor"},
		SafeClaims:     map[string]string{"team": "core", "env": "prod"},
		TenantID:       scope.Known("t-1"),
		OrganizationID: scope.Known("org-1"),
		WorkspaceID:    scope.Known("ws-1"),
		ProjectID:      scope.Known("proj-1"),
		DepartmentID:   scope.Known("dept-1"),
		CostCenterID:   scope.Known("cc-1"),
		PolicyLabels:   map[string]string{"tier": "gold", "region": "us"},
		Origin:         scope.OriginClient,
		ParentTraceID:  scope.Known("trace-1"),
	}
}

// capturingEgressPolicy records the received scope and policy input.
type capturingEgressPolicy struct {
	decision reasoninghost.EgressDecision
	received *reasoninghost.EgressInput
	mutated  bool
}

func (c *capturingEgressPolicy) Decide(_ context.Context, in reasoninghost.EgressInput) (reasoninghost.EgressDecision, error) {
	cp := in
	c.received = &cp
	s := in.Scope()
	s.Roles = append(s.Roles, "mutated")
	s.SafeClaims["injected"] = "evil"
	s.PolicyLabels["injected"] = "evil"
	c.mutated = true
	return c.decision, nil
}

type allowExternalPolicy struct{ version string }

func (a allowExternalPolicy) Decide(_ context.Context, _ reasoninghost.EgressInput) (reasoninghost.EgressDecision, error) {
	return reasoninghost.EgressDecision{Action: reasoninghost.EgressAllow, PolicyVersion: a.version}, nil
}

type denyExternalPolicy struct{ version string }

func (a denyExternalPolicy) Decide(_ context.Context, _ reasoninghost.EgressInput) (reasoninghost.EgressDecision, error) {
	return reasoninghost.EgressDecision{Action: reasoninghost.EgressDeny, PolicyVersion: a.version}, nil
}

type redactExternalPolicy struct{ version string }

func (a redactExternalPolicy) Decide(_ context.Context, _ reasoninghost.EgressInput) (reasoninghost.EgressDecision, error) {
	return reasoninghost.EgressDecision{Action: reasoninghost.EgressRedactThenAllow, PolicyVersion: a.version}, nil
}

type stubExternalMatcher struct{ redacted string }

func (s stubExternalMatcher) ScanBytes(_ context.Context, _ []byte) ([]sdk.Finding, error) {
	return nil, nil
}

func (s stubExternalMatcher) ScanString(_ context.Context, _ string) ([]sdk.Finding, error) {
	return nil, nil
}

func (s stubExternalMatcher) RedactBytes(_ context.Context, b []byte) ([]byte, []sdk.Finding, error) {
	if s.redacted != "" {
		return []byte(strings.ReplaceAll(string(b), "SECRET", s.redacted)), nil, nil
	}
	return b, nil, nil
}

func (s stubExternalMatcher) RedactString(_ context.Context, in string) (string, []sdk.Finding, error) {
	if s.redacted != "" {
		return strings.ReplaceAll(in, "SECRET", s.redacted), nil, nil
	}
	return in, nil, nil
}

type stubExternalResolver struct{ m sdk.Matcher }

func (s stubExternalResolver) Resolve(_ context.Context) (sdk.Matcher, error) { return s.m, nil }

// typedNilExternalEgress is a typed nil implementation for external test.
type typedNilExternalEgress struct{}

func (t *typedNilExternalEgress) Decide(_ context.Context, _ reasoninghost.EgressInput) (reasoninghost.EgressDecision, error) {
	return reasoninghost.EgressDecision{Action: reasoninghost.EgressAllow, PolicyVersion: "v1"}, nil
}

type typedNilExternalResolver struct{}

func (t *typedNilExternalResolver) Resolve(_ context.Context) (sdk.Matcher, error) { return nil, nil }

func TestEgressInput_ScopeDefensiveCloneFullFields(t *testing.T) {
	t.Parallel()
	orig := fullScope()
	in := reasoninghost.NewEgressInput("route-a", "reasoning_semantic_compression", "semantic_reasoning_text", orig)

	// First Scope() must contain all fields.
	got := in.Scope()
	if !got.PrincipalID.Equal(orig.PrincipalID) {
		t.Fatalf("PrincipalID: got %v want %v", got.PrincipalID, orig.PrincipalID)
	}
	if !got.TenantID.Equal(orig.TenantID) {
		t.Fatalf("TenantID mismatch")
	}
	if !got.OrganizationID.Equal(orig.OrganizationID) {
		t.Fatalf("OrganizationID mismatch")
	}
	if !got.WorkspaceID.Equal(orig.WorkspaceID) {
		t.Fatalf("WorkspaceID mismatch")
	}
	if !got.ProjectID.Equal(orig.ProjectID) {
		t.Fatalf("ProjectID mismatch")
	}
	if !got.DepartmentID.Equal(orig.DepartmentID) {
		t.Fatalf("DepartmentID mismatch")
	}
	if !got.CostCenterID.Equal(orig.CostCenterID) {
		t.Fatalf("CostCenterID mismatch")
	}
	if got.Origin != orig.Origin {
		t.Fatalf("Origin: got %v want %v", got.Origin, orig.Origin)
	}
	if len(got.Roles) != len(orig.Roles) {
		t.Fatalf("Roles len mismatch")
	}
	for i, r := range got.Roles {
		if r != orig.Roles[i] {
			t.Fatalf("Roles[%d] mismatch", i)
		}
	}
	if got.SafeClaims["team"] != "core" || got.SafeClaims["env"] != "prod" {
		t.Fatalf("SafeClaims mismatch: %+v", got.SafeClaims)
	}
	if got.PolicyLabels["tier"] != "gold" || got.PolicyLabels["region"] != "us" {
		t.Fatalf("PolicyLabels mismatch: %+v", got.PolicyLabels)
	}

	// Mutation of returned scope must not affect source or subsequent accesses.
	got.Roles[0] = "mutated"
	got.SafeClaims["team"] = "mutated"
	got.PolicyLabels["tier"] = "mutated"
	got2 := in.Scope()
	if got2.Roles[0] == "mutated" {
		t.Fatal("mutating Scope().Roles affected source")
	}
	if got2.SafeClaims["team"] == "mutated" {
		t.Fatal("mutating Scope().SafeClaims affected source")
	}
	if got2.PolicyLabels["tier"] == "mutated" {
		t.Fatal("mutating Scope().PolicyLabels affected source")
	}
	// Ensure injection of new keys does not leak.
	got.SafeClaims["new"] = "evil"
	got.PolicyLabels["new"] = "evil"
	got3 := in.Scope()
	if _, ok := got3.SafeClaims["new"]; ok {
		t.Fatal("injected SafeClaims key leaked into source")
	}
	if _, ok := got3.PolicyLabels["new"]; ok {
		t.Fatal("injected PolicyLabels key leaked into source")
	}
	// Original input scope must still equal orig (NewEgressInput defensively cloned).
	// Mutating orig after construction must not affect input.
	orig.Roles[0] = "orig-mutated"
	orig.SafeClaims["team"] = "orig-mutated"
	check := in.Scope()
	if check.Roles[0] == "orig-mutated" {
		t.Fatal("mutating original scope after NewEgressInput affected input")
	}
	if check.SafeClaims["team"] == "orig-mutated" {
		t.Fatal("mutating original SafeClaims after NewEgressInput affected input")
	}
}

func TestEgressInput_NewEgressInput_ClonesSource(t *testing.T) {
	t.Parallel()
	src := fullScope()
	in := reasoninghost.NewEgressInput("r", "p", "c", src)
	// Mutate src after construction.
	src.Roles = append(src.Roles, "extra")
	src.SafeClaims["extra"] = "x"
	got := in.Scope()
	if len(got.Roles) != 2 {
		t.Fatalf("Roles should be cloned, got %v", got.Roles)
	}
	if _, ok := got.SafeClaims["extra"]; ok {
		t.Fatal("SafeClaims clone leaked extra key")
	}
}

func TestAipruntime_PublicTypes_NoInternalImportsInSignatures(t *testing.T) {
	t.Parallel()
	// This test ensures the external package compiles without needing internal imports.
	_ = reasoninghost.EgressInput{}
	_ = reasoninghost.EgressDecision{Action: reasoninghost.EgressAllow, PolicyVersion: "v1"}
	var _ reasoninghost.EgressPolicy = allowExternalPolicy{version: "v1"}
	var _ sdk.MatcherResolver = stubExternalResolver{m: stubExternalMatcher{redacted: "x"}}
	var _ featurehost.Binding = &reasoninghost.Binding{}
	_ = featurehost.Registration{Binding: &reasoninghost.Binding{}}

	// Verify via reflection that aipruntime Options field types do not
	// carry internal package paths.
	typ := reflect.TypeFor[aipruntime.Options]()
	for f := range typ.Fields() {
		pkgPath := f.Type.PkgPath()
		if strings.Contains(pkgPath, "/internal/") {
			t.Fatalf("Options field %s has internal package path %q", f.Name, pkgPath)
		}
	}

	// Verify FeatureHostRegistrations field exists on Options and has the correct type.
	fhrField, ok := typ.FieldByName("FeatureHostRegistrations")
	if !ok {
		t.Fatal("Options must have FeatureHostRegistrations field")
	}
	if fhrField.Type != reflect.TypeFor[[]featurehost.Registration]() {
		t.Fatalf("Options.FeatureHostRegistrations type = %v, want []featurehost.Registration", fhrField.Type)
	}

	// Verify ReasoningCompression field no longer exists on Options.
	if _, exists := typ.FieldByName("ReasoningCompression"); exists {
		t.Fatal("Options must NOT have ReasoningCompression field")
	}

	// Verify exported method signatures on EgressInput don't expose internal types.
	m, ok := reflect.TypeFor[reasoninghost.EgressInput]().MethodByName("Scope")
	if !ok {
		t.Fatal("EgressInput.Scope method missing")
	}
	ret := m.Type.Out(0)
	if strings.Contains(ret.PkgPath(), "/internal/") {
		t.Fatalf("EgressInput.Scope return type has internal path %q", ret.PkgPath())
	}
	if ret.PkgPath() != "github.com/aiproxer/aiproxer/pkg/aipsdk/scope" {
		t.Fatalf("EgressInput.Scope should return aipsdk/scope type, got %q", ret.PkgPath())
	}
}

func writeTempConfigWithCompression(t *testing.T, enabled bool, egressRef string) string {
	t.Helper()
	raw, err := os.ReadFile(repoConfigPath(t))
	if err != nil {
		t.Fatalf("read repo config: %v", err)
	}
	content := string(raw)
	if enabled {
		// Inject compression block under reasoning-output-preservation config.
		target := "          max_session_bytes: 262144"
		repl := target + "\n        compression:\n          enabled: true\n          mode: shadow\n          route: test-route\n          timeout: 5s\n          max_input_tokens: 10000\n          max_input_bytes: 100000\n          max_output_tokens: 1000\n          max_output_bytes: 100000\n          max_surrogate_bytes: 50000\n          min_source_bytes: 100\n          min_saved_bytes: 50\n          min_savings_ratio: 0.5\n          max_pending_per_session: 10\n          max_surrogate_bytes_per_session: 100000\n          max_pending_total: 100\n          max_surrogate_bytes_total: 1000000\n          egress_policy_ref: " + egressRef
		if !strings.Contains(content, target) {
			t.Fatalf("target not found in config")
		}
		content = strings.Replace(content, target, repl, 1)
	} else {
		// Ensure compression disabled: replace any enabled compression with disabled, or ensure no compression.
		if strings.Contains(content, "compression:") {
			content = strings.ReplaceAll(content, "enabled: true", "enabled: false")
		}
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return path
}

func TestBuild_CompressionEnabled_WithPublicPolicyAndResolver_Succeeds(t *testing.T) {
	t.Parallel()
	path := writeTempConfigWithCompression(t, true, "test-allow")
	ctx := context.Background()
	policy := allowExternalPolicy{version: "v1"}
	resolver := stubExternalResolver{m: stubExternalMatcher{redacted: "REDACTED"}}
	binding := &reasoninghost.Binding{
		EgressPolicies:  map[string]reasoninghost.EgressPolicy{"test-allow": policy},
		MatcherResolver: resolver,
	}
	rt, err := aipruntime.Build(ctx, aipruntime.Options{
		ConfigPath: path,
		FeatureHostRegistrations: []featurehost.Registration{
			binding.Registration(),
		},
	})
	if err != nil {
		t.Fatalf("Build with valid public policy/resolver should succeed, got %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })
	if !rt.Ready() {
		t.Fatal("expected ready runtime")
	}
}

func TestBuild_CompressionEnabled_StockZeroFailsClosed(t *testing.T) {
	t.Parallel()
	path := writeTempConfigWithCompression(t, true, "test-allow")
	ctx := context.Background()
	_, err := aipruntime.Build(ctx, aipruntime.Options{ConfigPath: path})
	if err == nil {
		t.Fatal("enabled compression with zero FeatureHostRegistrations should fail closed")
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "egresspolicy") && !strings.Contains(msg, "matcherresolver") && !strings.Contains(msg, "egress") {
		t.Fatalf("error should mention EgressPolicy/MatcherResolver, got %v", err)
	}
}

func TestBuild_CompressionDisabled_StockZeroSucceeds(t *testing.T) {
	t.Parallel()
	path := writeTempConfigWithCompression(t, false, "")
	ctx := context.Background()
	rt, err := aipruntime.Build(ctx, aipruntime.Options{ConfigPath: path})
	if err != nil {
		t.Fatalf("disabled compression with zero FeatureHostRegistrations should succeed, got %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })
	if !rt.Ready() {
		t.Fatal("expected ready runtime for disabled compression")
	}
}

func TestBuild_TypedNilPolicyAndResolver_FailClosedNotPanic(t *testing.T) {
	t.Parallel()
	path := writeTempConfigWithCompression(t, true, "test-allow")
	ctx := context.Background()
	var nilPolicy *typedNilExternalEgress
	var nilResolver *typedNilExternalResolver

	// Case 1: typed nil policy entry in reasoninghost.Binding
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("typed nil policy caused panic: %v", r)
			}
		}()
		binding := &reasoninghost.Binding{
			EgressPolicies:  map[string]reasoninghost.EgressPolicy{"test-allow": nilPolicy},
			MatcherResolver: stubExternalResolver{m: stubExternalMatcher{}},
		}
		_, err := aipruntime.Build(ctx, aipruntime.Options{
			ConfigPath: path,
			FeatureHostRegistrations: []featurehost.Registration{
				binding.Registration(),
			},
		})
		if err == nil {
			t.Fatal("typed nil policy should fail closed")
		}
		if !strings.Contains(strings.ToLower(err.Error()), "egresspolicy") {
			t.Fatalf("typed nil policy error should mention EgressPolicy, got %v", err)
		}
	}()

	// Case 2: typed nil resolver in reasoninghost.Binding
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("typed nil resolver caused panic: %v", r)
			}
		}()
		binding := &reasoninghost.Binding{
			EgressPolicies:  map[string]reasoninghost.EgressPolicy{"test-allow": allowExternalPolicy{version: "v1"}},
			MatcherResolver: nilResolver,
		}
		_, err := aipruntime.Build(ctx, aipruntime.Options{
			ConfigPath: path,
			FeatureHostRegistrations: []featurehost.Registration{
				binding.Registration(),
			},
		})
		if err == nil {
			t.Fatal("typed nil resolver should fail closed")
		}
		if !strings.Contains(strings.ToLower(err.Error()), "matcherresolver") {
			t.Fatalf("typed nil resolver error should mention MatcherResolver, got %v", err)
		}
	}()

	// Case 3: both typed nil in reasoninghost.Binding
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("both typed nil caused panic: %v", r)
			}
		}()
		binding := &reasoninghost.Binding{
			EgressPolicies:  map[string]reasoninghost.EgressPolicy{"test-allow": nilPolicy},
			MatcherResolver: nilResolver,
		}
		_, err := aipruntime.Build(ctx, aipruntime.Options{
			ConfigPath: path,
			FeatureHostRegistrations: []featurehost.Registration{
				binding.Registration(),
			},
		})
		if err == nil {
			t.Fatal("both typed nil should fail closed")
		}
	}()

	// Case 4: nil binding in registration
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("nil binding in registration caused panic: %v", r)
			}
		}()
		_, err := aipruntime.Build(ctx, aipruntime.Options{
			ConfigPath: path,
			FeatureHostRegistrations: []featurehost.Registration{
				{Binding: nil},
			},
		})
		if err == nil {
			t.Fatal("nil binding in registration should fail closed")
		}
	}()

	// Case 5: typed nil *reasoninghost.Binding in registration
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("typed nil *reasoninghost.Binding caused panic: %v", r)
			}
		}()
		var typedNilBinding *reasoninghost.Binding
		_, err := aipruntime.Build(ctx, aipruntime.Options{
			ConfigPath: path,
			FeatureHostRegistrations: []featurehost.Registration{
				{Binding: typedNilBinding},
			},
		})
		if err == nil {
			t.Fatal("typed nil binding should fail closed")
		}
	}()

	// Case 6: duplicate binding ID in registrations
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("duplicate binding ID caused panic: %v", r)
			}
		}()
		b1 := &reasoninghost.Binding{
			EgressPolicies:  map[string]reasoninghost.EgressPolicy{"test-allow": allowExternalPolicy{version: "v1"}},
			MatcherResolver: stubExternalResolver{m: stubExternalMatcher{}},
		}
		b2 := &reasoninghost.Binding{
			EgressPolicies:  map[string]reasoninghost.EgressPolicy{"test-allow": allowExternalPolicy{version: "v2"}},
			MatcherResolver: stubExternalResolver{m: stubExternalMatcher{}},
		}
		_, err := aipruntime.Build(ctx, aipruntime.Options{
			ConfigPath: path,
			FeatureHostRegistrations: []featurehost.Registration{
				b1.Registration(),
				b2.Registration(),
			},
		})
		if err == nil {
			t.Fatal("duplicate binding ID should fail closed")
		}
	}()
}

func TestOptions_NonMoneyArchitecture_NoBillingFields(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeFor[aipruntime.Options]()
	for f := range typ.Fields() {
		if strings.Contains(strings.ToLower(f.Name), "billing") {
			t.Fatalf("public Options must not contain Billing field %q", f.Name)
		}
		typeStr := f.Type.String()
		if strings.Contains(strings.ToLower(typeStr), "billing") {
			t.Fatalf("Options field %s type %q contains billing", f.Name, typeStr)
		}
	}

	// Verify FeatureHostRegistrations exists on Options
	if _, ok := typ.FieldByName("FeatureHostRegistrations"); !ok {
		t.Fatal("Options must have FeatureHostRegistrations field")
	}

	// Verify ReasoningCompression field does NOT exist on Options
	if _, ok := typ.FieldByName("ReasoningCompression"); ok {
		t.Fatal("Options must NOT have ReasoningCompression field")
	}

	// reasoninghost.Binding must be non-money (no billing in its fields).
	rcTyp := reflect.TypeFor[reasoninghost.Binding]()
	for f := range rcTyp.Fields() {
		if strings.Contains(strings.ToLower(f.Name), "billing") {
			t.Fatalf("reasoninghost.Binding field %q must not be billing", f.Name)
		}
	}
}

func TestEgressAction_StringAndMapping(t *testing.T) {
	t.Parallel()
	if reasoninghost.EgressAllow.String() != "allow" {
		t.Fatalf("EgressAllow String = %q", reasoninghost.EgressAllow.String())
	}
	if reasoninghost.EgressDeny.String() != "deny" {
		t.Fatalf("EgressDeny String = %q", reasoninghost.EgressDeny.String())
	}
	if reasoninghost.EgressRedactThenAllow.String() != "redact_then_allow" {
		t.Fatalf("EgressRedactThenAllow String = %q", reasoninghost.EgressRedactThenAllow.String())
	}

	// Mapping is tested via Build success with each action; verify Build with deny still succeeds (policy is consulted, not route).
	path := writeTempConfigWithCompression(t, true, "test-deny")
	ctx := context.Background()
	denyBinding := &reasoninghost.Binding{
		EgressPolicies:  map[string]reasoninghost.EgressPolicy{"test-deny": denyExternalPolicy{version: "v1"}},
		MatcherResolver: stubExternalResolver{m: stubExternalMatcher{}},
	}
	rt, err := aipruntime.Build(ctx, aipruntime.Options{
		ConfigPath: path,
		FeatureHostRegistrations: []featurehost.Registration{
			denyBinding.Registration(),
		},
	})
	if err != nil {
		t.Fatalf("Build with deny policy should still succeed (deny is valid action), got %v", err)
	}
	_ = rt.Close(ctx)

	path2 := writeTempConfigWithCompression(t, true, "test-redact")
	redactBinding := &reasoninghost.Binding{
		EgressPolicies:  map[string]reasoninghost.EgressPolicy{"test-redact": redactExternalPolicy{version: "v1"}},
		MatcherResolver: stubExternalResolver{m: stubExternalMatcher{redacted: "REDACTED"}},
	}
	rt2, err := aipruntime.Build(ctx, aipruntime.Options{
		ConfigPath: path2,
		FeatureHostRegistrations: []featurehost.Registration{
			redactBinding.Registration(),
		},
	})
	if err != nil {
		t.Fatalf("Build with redact policy should succeed, got %v", err)
	}
	_ = rt2.Close(ctx)
}
