package sagemaker_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/sagemaker/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// TestCredentialContract_RejectsSecretAliases proves every secret-bearing
// YAML spelling fails closed — not just the historically known singular
// fields. Session-token, list/plural and authorization aliases must never
// be silently ignored. The nonsecret aws_profile chain selector stays
// valid YAML.
func TestCredentialContract_RejectsSecretAliases(t *testing.T) {
	t.Parallel()
	svc := service.New(service.WithAWSClientFactory(service.DefaultAWSClientFactory))
	base := "region: us-east-1\nendpoint_name: my-ep\ninference_contract: hf-text-generation\n"
	for _, raw := range []string{
		"api_keys: [hunter2]\n",
		"api_tokens: hunter2\n",
		"authorization: Bearer hunter2\n",
		"password: hunter2\n",
		"session_token: hunter2\n",
		"secret_keys: [hunter2]\n",
	} {
		_, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
			FactoryKind: service.FactoryKind,
			InstanceID:  "cred-contract",
			ConfigYAML:  []byte(base + raw),
			Secrets:     testStaticSecrets(),
		})
		if err == nil {
			t.Fatalf("expected rejection of secret YAML alias (yaml=%q)", raw)
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Fatalf("rejection leaks secret value: %v", err)
		}
		if !strings.Contains(err.Error(), "literal secrets in configuration YAML are forbidden") {
			t.Fatalf("rejection must use the closed secret-free error: %v", err)
		}
	}
}

// TestCredentialContract_HostStaticSecretsAdmit proves the workload
// classification for the static path: host-delivered static credentials
// admit through the production AWS chain without any YAML secret, and the
// provider-managed credential chain (profile/env/instance) is untouched.
func TestCredentialContract_HostStaticSecretsAdmit(t *testing.T) {
	t.Parallel()
	svc := service.New(service.WithAWSClientFactory(service.DefaultAWSClientFactory))
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "cred-contract",
		ConfigYAML:  []byte("region: us-east-1\nendpoint_name: my-ep\ninference_contract: hf-text-generation\n"),
		Secrets:     testStaticSecrets(),
	})
	if err != nil {
		t.Fatalf("host static secrets must admit through the production chain: %v", err)
	}
	if inst == nil {
		t.Fatal("expected a configured instance")
	}
}

// TestCredentialContract_AWSProfilePreserved proves the nonsecret
// aws_profile chain selector stays valid YAML alongside host secrets: the
// workload classification distinguishes the profile pointer (config) from
// key material (secrets).
func TestCredentialContract_AWSProfilePreserved(t *testing.T) {
	t.Parallel()
	svc := service.New(service.WithStaticClients(&f2StubRuntime{}))
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "cred-contract",
		ConfigYAML:  []byte("region: us-east-1\nendpoint_name: my-ep\ninference_contract: hf-text-generation\naws_profile: test-profile\n"),
		Secrets:     testStaticSecrets(),
	})
	if err != nil {
		t.Fatalf("aws_profile YAML must stay accepted: %v", err)
	}
	if inst == nil {
		t.Fatal("expected a configured instance")
	}
}

// TestCredentialContract_ManifestWorkloadClassification pins the packaged
// manifest classification for the SageMaker export: static_nonrotatable
// with no_rotation selection over the host namespace. The runtime
// descriptor must agree; AWS SigV4 signing (static host secrets or the
// provider-managed chain) stays behind this classification.
func TestCredentialContract_ManifestWorkloadClassification(t *testing.T) {
	t.Parallel()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(wd, "manifest", "template.backendplugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Exports []struct {
			Kind               string `json:"kind"`
			CredentialMode     string `json:"credential_mode"`
			AccessScope        string `json:"access_scope"`
			CredentialContract *struct {
				Version     int    `json:"version"`
				Class       string `json:"class"`
				Source      string `json:"source"`
				EnvRoot     string `json:"env_root"`
				Slot        string `json:"slot"`
				Selection   string `json:"selection"`
				QuotaDomain string `json:"quota_domain"`
			} `json:"credential_contract"`
		} `json:"exports"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Exports) != 1 || doc.Exports[0].Kind != service.FactoryKind {
		t.Fatalf("exports=%+v", doc.Exports)
	}
	ex := doc.Exports[0]
	if ex.CredentialMode != "static" {
		t.Fatalf("credential_mode=%q, want static", ex.CredentialMode)
	}
	if ex.CredentialContract == nil {
		t.Fatal("missing credential_contract for the static export")
	}
	cc := ex.CredentialContract
	if cc.Version != 2 || cc.Class != "static_nonrotatable" || cc.Source != "host_env" ||
		cc.EnvRoot != "AWS_ACCESS_KEY_ID" || cc.Selection != "no_rotation" ||
		cc.QuotaDomain != "aws-sagemaker" {
		t.Fatalf("credential_contract=%+v", cc)
	}
	d, err := service.New().Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Factories) != 1 || d.Factories[0].CredentialMode != backendplugin.CredentialModeStatic {
		t.Fatalf("descriptor disagrees with manifest posture: %+v", d.Factories)
	}
}
