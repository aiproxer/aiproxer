package aipsdk

import "testing"

func TestBackendCredentialMode_stringValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		mode BackendCredentialMode
		want string
	}{
		{CredentialStatic, "static"},
		{CredentialWorkload, "workload"},
		{CredentialOAuthUser, "oauth_user"},
		{CredentialNone, "none"},
		{CredentialUnknown, "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			if string(tc.mode) != tc.want {
				t.Fatalf("BackendCredentialMode %v: want %q", tc.mode, tc.want)
			}
		})
	}
}

func TestBackendAccessScope_stringValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		scope BackendAccessScope
		want  string
	}{
		{BackendAccessAny, "any"},
		{BackendAccessLocalOnly, "local_only"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			if string(tc.scope) != tc.want {
				t.Fatalf("BackendAccessScope %v: want %q", tc.scope, tc.want)
			}
		})
	}
}

// ZeroValueBackendSecurityProfile is explicit 'unknown' posture; registry validation
// and startup rules treat unknown as conservative (see spec task 7.x).
func TestBackendSecurityProfile_zeroIsUnknownMode(t *testing.T) {
	t.Parallel()
	var p BackendSecurityProfile
	if p.CredentialMode != "" {
		t.Fatalf("zero profile CredentialMode: got %q, want empty before normalization", p.CredentialMode)
	}
	if p.AccessScope != "" {
		t.Fatalf("zero profile AccessScope: got %q, want empty before normalization", p.AccessScope)
	}
}
