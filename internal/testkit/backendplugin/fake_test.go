package backendplugin_test

import (
	"context"
	"testing"

	fakebp "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/conformance"
)

func TestFake_CommandSkeletonName(t *testing.T) {
	t.Parallel()
	if fakebp.CommandName == "" {
		t.Fatal("missing command skeleton name")
	}
}

func TestFake_ValidDescribeConfigure(t *testing.T) {
	t.Parallel()
	svc := &fakebp.FakeService{Mode: fakebp.ModeValid}
	desc, err := svc.Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := desc.Validate(); err != nil {
		t.Fatal(err)
	}
	_ = backendplugin.NewGRPCServer(backendplugin.ProtocolOffer{
		Major: 1, Minor: 0, DisableTransportRetries: true, Features: desc.Features,
	}, svc)
	rep := conformance.Run(context.Background(), svc)
	if !rep.Ok() {
		t.Fatal(rep.Failures())
	}
}
