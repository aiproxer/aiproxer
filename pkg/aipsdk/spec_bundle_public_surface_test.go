package aipsdk_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

// Black-box anchors for exported registration and security metadata (specification bundle).
func TestBackendCredentialModeWireStrings(t *testing.T) {
	t.Parallel()
	cases := []struct {
		mode aipsdk.BackendCredentialMode
		want string
	}{
		{aipsdk.CredentialStatic, "static"},
		{aipsdk.CredentialWorkload, "workload"},
		{aipsdk.CredentialOAuthUser, "oauth_user"},
		{aipsdk.CredentialNone, "none"},
		{aipsdk.CredentialUnknown, "unknown"},
	}
	for _, tc := range cases {
		if string(tc.mode) != tc.want {
			t.Fatalf("BackendCredentialMode %s: got %q want %q", tc.want, string(tc.mode), tc.want)
		}
	}
}

func TestBackendAccessScopeWireStrings(t *testing.T) {
	t.Parallel()
	cases := []struct {
		scope aipsdk.BackendAccessScope
		want  string
	}{
		{aipsdk.BackendAccessAny, "any"},
		{aipsdk.BackendAccessLocalOnly, "local_only"},
	}
	for _, tc := range cases {
		if string(tc.scope) != tc.want {
			t.Fatalf("BackendAccessScope %s: got %q want %q", tc.want, string(tc.scope), tc.want)
		}
	}
}

func TestBackendSecurityProfileExportedFields(t *testing.T) {
	t.Parallel()
	p := aipsdk.BackendSecurityProfile{CredentialMode: aipsdk.CredentialStatic, AccessScope: aipsdk.BackendAccessLocalOnly}
	if p.CredentialMode != aipsdk.CredentialStatic {
		t.Fatal(p)
	}
	if p.AccessScope != aipsdk.BackendAccessLocalOnly {
		t.Fatal(p)
	}
}

func TestBackendExecutionClassWireStrings(t *testing.T) {
	t.Parallel()
	cases := []struct {
		class aipsdk.BackendExecutionClass
		want  string
	}{
		{aipsdk.BackendExecutionUnknown, ""},
		{aipsdk.BackendExecutionInference, "inference"},
		{aipsdk.BackendExecutionAgentRuntime, "agent_runtime"},
	}
	for _, tc := range cases {
		if string(tc.class) != tc.want {
			t.Fatalf("BackendExecutionClass %s: got %q want %q", tc.want, string(tc.class), tc.want)
		}
	}
}

func TestBackendExecutionProfileExportedFields(t *testing.T) {
	t.Parallel()
	p := aipsdk.BackendExecutionProfile{Class: aipsdk.BackendExecutionInference}
	if p.Class != aipsdk.BackendExecutionInference {
		t.Fatal(p)
	}
	if p.EffectiveClass() != aipsdk.BackendExecutionInference {
		t.Fatal(p)
	}
}

func TestFeatureBundleSchemaVersionConstant(t *testing.T) {
	t.Parallel()
	if feature.SchemaVersionV1 != 1 {
		t.Fatalf("SchemaVersionV1 = %d", feature.SchemaVersionV1)
	}
	var b feature.FeatureBundle
	b.SchemaVersion = feature.SchemaVersionV1
	if b.SchemaVersion != 1 {
		t.Fatal(b)
	}
}
