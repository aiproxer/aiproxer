package acp_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/acp/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestDescribe_factoryKind(t *testing.T) {
	t.Parallel()
	svc := service.New()
	d, err := svc.Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Factories) != 1 || d.Factories[0].Kind != "acp" {
		t.Fatalf("factories=%+v", d.Factories)
	}
	if d.Factories[0].AccessScope != backendplugin.AccessScopeLocalOnly {
		t.Fatalf("access=%s", d.Factories[0].AccessScope)
	}
}

func TestConfigure_requiresBaseURL(t *testing.T) {
	t.Parallel()
	svc := service.New()
	_, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: "acp",
		InstanceID:  "i1",
		ConfigYAML:  []byte(`{}`),
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestConfigure_ok(t *testing.T) {
	t.Parallel()
	svc := service.New()
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: "acp",
		InstanceID:  "i1",
		ConfigYAML:  []byte("base_url: http://127.0.0.1:9\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = inst.Close(context.Background())
}
