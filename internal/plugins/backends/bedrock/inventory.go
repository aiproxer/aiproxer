package bedrock

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	bedrockapi "github.com/aws/aws-sdk-go-v2/service/bedrock"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

type foundationModelLister interface {
	ListFoundationModels(context.Context, *bedrockapi.ListFoundationModelsInput, ...func(*bedrockapi.Options)) (*bedrockapi.ListFoundationModelsOutput, error)
}

type foundationModelsProvider struct {
	client foundationModelLister
}

func newFoundationModelsProvider(ctx context.Context, cfg Config) modelinventory.Provider {
	if err := validateBedrockEndpointInput(cfg); err != nil {
		return modelinventory.ErrorProvider{Err: fmt.Errorf("bedrock model inventory: validate endpoint: %w", err)}
	}
	if ctx == nil {
		return modelinventory.ErrorProvider{Err: modelinventory.ErrNilContext}
	}
	region := strings.TrimSpace(cfg.Region)
	if region == "" {
		region = "us-east-1"
	}
	loadOpts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(region)}
	if cfg.AccessKeyID != "" && cfg.SecretAccessKey != "" {
		loadOpts = append(loadOpts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, cfg.SessionToken),
		))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return modelinventory.ErrorProvider{Err: fmt.Errorf("bedrock model inventory: aws config: %w", err)}
	}
	opts := []func(*bedrockapi.Options){}
	if u := strings.TrimSpace(cfg.BaseEndpoint); u != "" {
		opts = append(opts, func(o *bedrockapi.Options) {
			o.BaseEndpoint = aws.String(u)
			if cfg.DisableHTTPS {
				o.EndpointOptions.DisableHTTPS = true
			}
		})
	}
	if cfg.HTTPClient != nil {
		opts = append(opts, func(o *bedrockapi.Options) {
			o.HTTPClient = cfg.HTTPClient
		})
	}
	return foundationModelsProvider{client: bedrockapi.NewFromConfig(awsCfg, opts...)}
}

func (p foundationModelsProvider) LoadModels(ctx context.Context) (modelinventory.Snapshot, error) {
	if ctx == nil {
		return modelinventory.Snapshot{}, modelinventory.ErrNilContext
	}
	out, err := p.client.ListFoundationModels(ctx, &bedrockapi.ListFoundationModelsInput{})
	if err != nil {
		return modelinventory.Snapshot{}, fmt.Errorf("bedrock model inventory: ListFoundationModels: %w", err)
	}
	models := make([]modelinventory.Model, 0, len(out.ModelSummaries))
	for _, row := range out.ModelSummaries {
		native := strings.TrimSpace(aws.ToString(row.ModelId))
		if native == "" {
			continue
		}
		name := strings.TrimSpace(aws.ToString(row.ModelName))
		if name == "" {
			name = native
		}
		models = append(models, modelinventory.Model{
			CanonicalID: "aws-bedrock/" + native,
			NativeID:    native,
			DisplayName: name,
		})
	}
	if len(models) == 0 {
		return modelinventory.Snapshot{}, fmt.Errorf("bedrock model inventory returned no models")
	}
	return modelinventory.Snapshot{
		Source:   modelinventory.SourceRemote,
		LoadedAt: time.Now(),
		Models:   models,
	}, nil
}
