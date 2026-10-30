package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sagemakerruntime"
)

// RuntimeClient defines the consumer-driven interface for SageMaker Runtime inference.
// Only the unary InvokeEndpoint is used: the hf-text-generation contract has
// no incremental token framing, so provider streaming
// (InvokeEndpointWithResponseStream) is intentionally not part of this
// interface to prevent unbounded event-stream buffering.
type RuntimeClient interface {
	InvokeEndpoint(ctx context.Context, params *sagemakerruntime.InvokeEndpointInput, optFns ...func(*sagemakerruntime.Options)) (*sagemakerruntime.InvokeEndpointOutput, error)
}

// AWSClientFactory constructs the SageMaker runtime inference client.
type AWSClientFactory func(ctx context.Context, cfg Config, secrets backendplugin.SecretBundle) (RuntimeClient, error)

// DefaultAWSClientFactory builds a real AWS SDK v2 SageMaker runtime client
// with SigV4 signing. The underlying HTTP client is wrapped so oversized
// response bodies fail closed before the SDK deserializer can size a buffer
// from a large declared Content-Length (see boundedResponseHTTPClient).
func DefaultAWSClientFactory(ctx context.Context, cfg Config, secrets backendplugin.SecretBundle) (RuntimeClient, error) {
	var loadOpts []func(*awsconfig.LoadOptions) error
	if cfg.Region != "" {
		loadOpts = append(loadOpts, awsconfig.WithRegion(cfg.Region))
	}
	if cfg.AWSProfile != "" {
		loadOpts = append(loadOpts, awsconfig.WithSharedConfigProfile(cfg.AWSProfile))
	}

	ak := strings.TrimSpace(string(secrets.Values["aws_access_key_id"]))
	if ak == "" {
		ak = strings.TrimSpace(string(secrets.Values["access_key"]))
	}
	sk := strings.TrimSpace(string(secrets.Values["aws_secret_access_key"]))
	if sk == "" {
		sk = strings.TrimSpace(string(secrets.Values["secret_key"]))
	}
	st := strings.TrimSpace(string(secrets.Values["aws_session_token"]))
	if st == "" {
		st = strings.TrimSpace(string(secrets.Values["session_token"]))
	}

	if ak != "" && sk != "" {
		loadOpts = append(loadOpts, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(ak, sk, st)))
	}
	// Guard the HTTP layer before the SDK deserializer runs: the
	// sagemakerruntime deserializer sizes a buffer from the declared
	// Content-Length, so this must be in place at config load.
	loadOpts = append(loadOpts, awsconfig.WithHTTPClient(&boundedResponseHTTPClient{inner: awshttp.NewBuildableClient()}))

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("sagemaker: load AWS config: %w", err)
	}

	var runtimeOptFns []func(*sagemakerruntime.Options)

	if cfg.APIOrigin != "" {
		baseEndpoint := cfg.APIOrigin
		runtimeOptFns = append(runtimeOptFns, func(o *sagemakerruntime.Options) {
			o.BaseEndpoint = aws.String(baseEndpoint)
		})
	}

	return sagemakerruntime.NewFromConfig(awsCfg, runtimeOptFns...), nil
}
