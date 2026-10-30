package service_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/minimexoauth/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestDescribe(t *testing.T) {
	svc := service.NewProduction()
	desc, err := svc.Describe(context.Background())
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if desc.PluginID != service.PluginID {
		t.Fatalf("expected plugin ID %q, got %q", service.PluginID, desc.PluginID)
	}
	if len(desc.Factories) != 1 {
		t.Fatalf("expected 1 factory, got %d", len(desc.Factories))
	}
	f := desc.Factories[0]
	if f.Kind != service.FactoryKind {
		t.Fatalf("expected factory kind %q, got %q", service.FactoryKind, f.Kind)
	}
	if f.DisplayName != service.DisplayName {
		t.Fatalf("expected display name %q, got %q", service.DisplayName, f.DisplayName)
	}
}

func TestConfigure_UnexpectedKind(t *testing.T) {
	svc := service.NewProduction()
	_, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: "wrong-kind",
	})
	if err == nil {
		t.Fatalf("expected error on wrong factory kind")
	}
	if !strings.Contains(err.Error(), "unexpected factory kind") {
		t.Fatalf("expected unexpected factory kind error, got: %v", err)
	}
}

func TestConfigure_WithStaticToken(t *testing.T) {
	svc := service.NewProduction()
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		ConfigYAML:  []byte("region: global\n"),
		Secrets: backendplugin.SecretBundle{
			Values: map[string][]byte{"access_token": []byte("test-bearer-token")},
		},
	})
	if err != nil {
		t.Fatalf("Configure failed: %v", err)
	}
	prof, err := inst.Resolve(context.Background(), nil)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if !prof.Capabilities.Streaming {
		t.Fatalf("expected streaming capability to be true")
	}
}
