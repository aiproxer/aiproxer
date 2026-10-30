package bedrock

import (
	"context"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
	"github.com/aws/aws-sdk-go-v2/aws"
	bedrockapi "github.com/aws/aws-sdk-go-v2/service/bedrock"
	"github.com/aws/aws-sdk-go-v2/service/bedrock/types"
)

type fakeFoundationModelLister struct{}

func (fakeFoundationModelLister) ListFoundationModels(context.Context, *bedrockapi.ListFoundationModelsInput, ...func(*bedrockapi.Options)) (*bedrockapi.ListFoundationModelsOutput, error) {
	return &bedrockapi.ListFoundationModelsOutput{
		ModelSummaries: []types.FoundationModelSummary{{
			ModelId:      aws.String("anthropic.claude-3-5-haiku-20241022-v1:0"),
			ModelName:    aws.String("Claude 3.5 Haiku"),
			ProviderName: aws.String("Anthropic"),
		}},
	}, nil
}

func TestFoundationModelsProvider_LoadModels(t *testing.T) {
	t.Parallel()

	p := foundationModelsProvider{client: fakeFoundationModelLister{}}
	snap, err := p.LoadModels(context.Background())
	if err != nil {
		t.Fatalf("LoadModels() error = %v", err)
	}
	if len(snap.Models) != 1 {
		t.Fatalf("models len = %d", len(snap.Models))
	}
	got := snap.Models[0]
	if got.CanonicalID != "aws-bedrock/anthropic.claude-3-5-haiku-20241022-v1:0" {
		t.Fatalf("CanonicalID = %q", got.CanonicalID)
	}
	if got.NativeID != "anthropic.claude-3-5-haiku-20241022-v1:0" {
		t.Fatalf("NativeID = %q", got.NativeID)
	}
}

func TestNewFoundationModelsProvider_nilContextReturnsErrorProvider(t *testing.T) {
	t.Parallel()

	//nolint:staticcheck // defensive nil-context handling is intentional for SDK-style constructors
	p := newFoundationModelsProvider(nil, Config{})
	if _, ok := p.(modelinventory.ErrorProvider); !ok {
		t.Fatalf("provider type = %T, want modelinventory.ErrorProvider", p)
	}
}

func TestNewFoundationModelsProvider_staticCredentialsBuildsProvider(t *testing.T) {
	t.Parallel()

	p := newFoundationModelsProvider(context.Background(), Config{
		Region:          "us-west-2",
		AccessKeyID:     "test-access-key",
		SecretAccessKey: "test-secret-key",
	})
	if _, ok := p.(modelinventory.ErrorProvider); ok {
		t.Fatalf("provider type = %T, want non-error provider", p)
	}
}

func TestNewFoundationModelsProvider_disableHTTPSWithoutBaseEndpointReturnsErrorProvider(t *testing.T) {
	t.Parallel()

	p := newFoundationModelsProvider(context.Background(), Config{
		Region:       "us-west-2",
		DisableHTTPS: true,
	})
	if _, ok := p.(modelinventory.ErrorProvider); !ok {
		t.Fatalf("provider type = %T, want ErrorProvider", p)
	}
}

func TestNewFoundationModelsProvider_rejectsInsecureEndpointConfig(t *testing.T) {
	t.Parallel()

	p := newFoundationModelsProvider(context.Background(), Config{DisableHTTPS: true})
	if _, ok := p.(modelinventory.ErrorProvider); !ok {
		t.Fatalf("provider type = %T, want modelinventory.ErrorProvider", p)
	}
	_, err := p.LoadModels(context.Background())
	if err == nil || !strings.Contains(err.Error(), "disable_https requires a non-empty base_endpoint") {
		t.Fatalf("LoadModels() error = %v, want endpoint security validation error", err)
	}
}
