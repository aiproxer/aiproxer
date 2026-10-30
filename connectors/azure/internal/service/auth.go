package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

const (
	// DefaultCognitiveServicesScope is the OAuth2 scope for Azure OpenAI and Azure AI Foundry.
	DefaultCognitiveServicesScope = "https://cognitiveservices.azure.com/.default"
)

// TokenProvider returns an Entra access token for outgoing Azure OpenAI calls.
type TokenProvider interface {
	GetToken(ctx context.Context) (string, error)
}

// StaticTokenProvider is an in-memory token provider used for tests.
type StaticTokenProvider string

func (s StaticTokenProvider) GetToken(context.Context) (string, error) {
	return string(s), nil
}

// TokenProviderFactory constructs a TokenProvider from typed config and secrets.
type TokenProviderFactory func(cfg Config, secrets backendplugin.SecretBundle) (TokenProvider, error)

// AzIdentityTokenProvider wraps an azcore.TokenCredential for Microsoft Entra ID.
type AzIdentityTokenProvider struct {
	cred   azcore.TokenCredential
	scopes []string
}

func NewAzIdentityTokenProvider(cred azcore.TokenCredential, scopes ...string) *AzIdentityTokenProvider {
	if len(scopes) == 0 {
		scopes = []string{DefaultCognitiveServicesScope}
	}
	return &AzIdentityTokenProvider{
		cred:   cred,
		scopes: scopes,
	}
}

func (p *AzIdentityTokenProvider) GetToken(ctx context.Context) (string, error) {
	token, err := p.cred.GetToken(ctx, policy.TokenRequestOptions{
		Scopes: p.scopes,
	})
	if err != nil {
		return "", fmt.Errorf("azure-openai: entra token: %w", err)
	}
	return token.Token, nil
}

// DefaultTokenProviderFactory builds an Azure Identity credential chain:
// - If tenant_id, client_id, and client_secret are provided, uses ClientSecretCredential.
// - Otherwise uses DefaultAzureCredential (supports environment, workload identity, managed identity, CLI).
func DefaultTokenProviderFactory(cfg Config, secrets backendplugin.SecretBundle) (TokenProvider, error) {
	clientSecret := strings.TrimSpace(string(secrets.Values["client_secret"]))
	if cfg.TenantID != "" && cfg.ClientID != "" && clientSecret != "" {
		cred, err := azidentity.NewClientSecretCredential(cfg.TenantID, cfg.ClientID, clientSecret, nil)
		if err != nil {
			return nil, fmt.Errorf("azure-openai: client secret credential: %w", err)
		}
		return NewAzIdentityTokenProvider(cred), nil
	}

	var opts *azidentity.DefaultAzureCredentialOptions
	if cfg.TenantID != "" {
		opts = &azidentity.DefaultAzureCredentialOptions{
			TenantID: cfg.TenantID,
		}
	}
	cred, err := azidentity.NewDefaultAzureCredential(opts)
	if err != nil {
		return nil, fmt.Errorf("azure-openai: default azure credential: %w", err)
	}
	return NewAzIdentityTokenProvider(cred), nil
}
