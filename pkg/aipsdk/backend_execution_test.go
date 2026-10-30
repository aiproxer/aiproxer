package aipsdk

import (
	"errors"
	"testing"
)

func TestBackendExecutionClass_stringValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		class BackendExecutionClass
		want  string
	}{
		{BackendExecutionUnknown, ""},
		{BackendExecutionInference, "inference"},
		{BackendExecutionAgentRuntime, "agent_runtime"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			if string(tc.class) != tc.want {
				t.Fatalf("BackendExecutionClass %v: want %q", tc.class, tc.want)
			}
		})
	}
}

func TestBackendExecutionProfile_EffectiveClassAndValidate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		profile       BackendExecutionProfile
		wantEffective BackendExecutionClass
		wantErr       error
	}{
		{
			name:          "zero value is unknown and valid",
			profile:       BackendExecutionProfile{},
			wantEffective: BackendExecutionUnknown,
			wantErr:       nil,
		},
		{
			name:          "explicit unknown is valid",
			profile:       BackendExecutionProfile{Class: BackendExecutionUnknown},
			wantEffective: BackendExecutionUnknown,
			wantErr:       nil,
		},
		{
			name:          "inference is valid",
			profile:       BackendExecutionProfile{Class: BackendExecutionInference},
			wantEffective: BackendExecutionInference,
			wantErr:       nil,
		},
		{
			name:          "agent_runtime is valid",
			profile:       BackendExecutionProfile{Class: BackendExecutionAgentRuntime},
			wantEffective: BackendExecutionAgentRuntime,
			wantErr:       nil,
		},
		{
			name:          "invalid class returns error and normalizes to unknown",
			profile:       BackendExecutionProfile{Class: "invalid_custom_class"},
			wantEffective: BackendExecutionUnknown,
			wantErr:       ErrInvalidBackendExecutionClass,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			eff := tc.profile.EffectiveClass()
			if eff != tc.wantEffective {
				t.Fatalf("EffectiveClass() = %q, want %q", eff, tc.wantEffective)
			}
			err := tc.profile.Validate()
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Validate() err = %v, want %v", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("Validate() unexpected err: %v", err)
			}
		})
	}
}

func TestBackendExecutionProfile_independentFromSecurityProfile(t *testing.T) {
	t.Parallel()
	// Prove that local_only access scope, none credentials, etc., do NOT imply agent_runtime,
	// and conversely cloud/any access scope does not imply inference.
	localInference := struct {
		sec  BackendSecurityProfile
		exec BackendExecutionProfile
	}{
		sec:  BackendSecurityProfile{AccessScope: BackendAccessLocalOnly, CredentialMode: CredentialNone},
		exec: BackendExecutionProfile{Class: BackendExecutionInference},
	}
	if localInference.exec.EffectiveClass() != BackendExecutionInference {
		t.Fatalf("local inference should have inference class, got %q", localInference.exec.EffectiveClass())
	}

	cloudAgent := struct {
		sec  BackendSecurityProfile
		exec BackendExecutionProfile
	}{
		sec:  BackendSecurityProfile{AccessScope: BackendAccessAny, CredentialMode: CredentialStatic},
		exec: BackendExecutionProfile{Class: BackendExecutionAgentRuntime},
	}
	if cloudAgent.exec.EffectiveClass() != BackendExecutionAgentRuntime {
		t.Fatalf("cloud agent should have agent_runtime class, got %q", cloudAgent.exec.EffectiveClass())
	}
}
