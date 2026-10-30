package acp_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/acp/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// Differential/parity smoke against the external acp plugin descriptor and
// configure surface. Live ACP agents are not required; protocol fidelity is
// covered by connector-support/acp GOWORK=off tests and refbackend emulators.
func TestParity_describeConfigureContract(t *testing.T) {
	t.Parallel()
	svc := service.New()
	d, err := svc.Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if d.PluginID != "com.aiproxer.backend.acp" {
		t.Fatalf("plugin_id=%q", d.PluginID)
	}
	if len(d.Factories) != 1 || d.Factories[0].Kind != "acp" {
		t.Fatalf("factories=%+v", d.Factories)
	}
	if d.Factories[0].CredentialMode != backendplugin.CredentialModeStatic {
		t.Fatalf("credential=%s", d.Factories[0].CredentialMode)
	}
	if d.Factories[0].AccessScope != backendplugin.AccessScopeLocalOnly {
		t.Fatalf("access=%s", d.Factories[0].AccessScope)
	}
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: "acp",
		InstanceID:  "parity-1",
		ConfigYAML:  []byte("base_url: http://127.0.0.1:9\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	prof, err := inst.Resolve(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(prof.RoutePrefixes) != 1 || prof.RoutePrefixes[0] != "acp" {
		t.Fatalf("routes=%v", prof.RoutePrefixes)
	}
	_ = inst.Close(context.Background())
}
