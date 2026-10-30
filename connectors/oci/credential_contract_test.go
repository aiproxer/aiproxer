package oci_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/oci/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// TestCredentialContract_RejectsSecretAliases proves every secret-bearing
// YAML spelling fails closed, not just the historically known singular
// fields. List/plural, token and authorization aliases must never be
// silently ignored. Nonsecret OCIDs (tenancy/user/fingerprint) stay valid
// YAML.
func TestCredentialContract_RejectsSecretAliases(t *testing.T) {
	t.Parallel()
	svc := service.New(service.WithRequestSignerFactory(service.DefaultRequestSignerFactory))
	secrets, tenancy, user, fp := testKeyAndSecrets()
	base := fmt.Sprintf("region: us-chicago-1\ncompartment_id: ocid1.compartment.oc1..test\ntenancy_ocid: %s\nuser_ocid: %s\nfingerprint: %s\n", tenancy, user, fp)
	for _, raw := range []string{
		"api_keys: [hunter2]\n",
		"api_tokens: hunter2\n",
		"authorization: Bearer hunter2\n",
		"password: hunter2\n",
		"secret_key: hunter2\n",
		"credentials: {a: hunter2}\n",
	} {
		_, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
			FactoryKind: service.FactoryKind,
			InstanceID:  "cred-contract",
			ConfigYAML:  []byte(base + raw),
			Secrets:     secrets,
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

// TestCredentialContract_ProviderChainAdmitsWithoutStaticSecrets proves the
// workload classification: static private-key material is optional at
// admission. The production factory with no secrets admits via the
// provider-managed chain entrypoint (config file/instance principal
// resolved at request time), so static demands never block workload
// identity.
func TestCredentialContract_ProviderChainAdmitsWithoutStaticSecrets(t *testing.T) {
	t.Parallel()
	svc := service.NewProduction()
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "cred-contract",
		ConfigYAML:  []byte("region: us-chicago-1\ncompartment_id: ocid1.compartment.oc1..test\n"),
	})
	if err != nil {
		t.Fatalf("provider-chain admission without static secrets must succeed: %v", err)
	}
	if inst == nil {
		t.Fatal("expected a configured instance")
	}
}

// TestCredentialContract_ManifestWorkloadClassification pins the packaged
// manifest classification for the OCI export: static_nonrotatable with
// no_rotation selection over the host namespace. The runtime descriptor
// must agree; OCI request signing (static key or provider-managed chain)
// stays behind this classification.
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
		cc.EnvRoot != "OCI_API_KEY" || cc.Selection != "no_rotation" ||
		cc.QuotaDomain != "oci-generative-ai" {
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
