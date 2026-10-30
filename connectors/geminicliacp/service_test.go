package geminicliacp_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/geminicliacp/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestDescribe_kind(t *testing.T) {
	t.Parallel()
	d, err := service.New().Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Factories) != 1 || d.Factories[0].Kind != "geminicliacp" {
		t.Fatalf("%+v", d.Factories)
	}
	if d.Factories[0].AccessScope != backendplugin.AccessScopeLocalOnly {
		t.Fatalf("scope=%s", d.Factories[0].AccessScope)
	}
}
