package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDecodeBackendDiscovery_EconomicEvidencePolicy(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		yaml    string
		want    string
		wantErr bool
	}{
		{
			name: "absent selects the degrade default",
			yaml: "enabled: true\n",
			want: "degrade",
		},
		{
			name: "explicit degrade",
			yaml: "economic_evidence_policy: degrade\n",
			want: "degrade",
		},
		{
			name: "explicit strict",
			yaml: "economic_evidence_policy: strict\n",
			want: "strict",
		},
		{
			name:    "unknown value is rejected",
			yaml:    "economic_evidence_policy: refuse\n",
			wantErr: true,
		},
		{
			name:    "empty value is rejected",
			yaml:    "economic_evidence_policy: ''\n",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var node yaml.Node
			if err := yaml.Unmarshal([]byte(tc.yaml), &node); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			cfg, err := DecodeBackendDiscovery(node)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("decode accepted %q, want a validation failure", tc.yaml)
				}
				if !strings.Contains(err.Error(), "economic_evidence_policy") {
					t.Fatalf("error = %v, want it to name the policy key", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if cfg.EconomicEvidencePolicy != tc.want {
				t.Fatalf("policy = %q, want %q", cfg.EconomicEvidencePolicy, tc.want)
			}
		})
	}
}

func TestDecodeBackendDiscovery_UnknownKeyStillRejected(t *testing.T) {
	t.Parallel()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte("economic_evidence_polcy: strict\n"), &node); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, err := DecodeBackendDiscovery(node); err == nil {
		t.Fatal("an unknown backend_discovery key was accepted")
	}
}
