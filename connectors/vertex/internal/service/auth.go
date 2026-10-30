package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	// DefaultCloudPlatformScope is the OAuth2 scope for Google Cloud APIs including Vertex AI.
	DefaultCloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"
)

// TokenProvider returns an OAuth2 bearer access token for outgoing Vertex AI calls.
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

// GoogleTokenProvider wraps an oauth2.TokenSource for Google Cloud credentials.
type GoogleTokenProvider struct {
	ts oauth2.TokenSource
}

func NewGoogleTokenProvider(ts oauth2.TokenSource) *GoogleTokenProvider {
	return &GoogleTokenProvider{ts: ts}
}

func (p *GoogleTokenProvider) GetToken(ctx context.Context) (string, error) {
	if p.ts == nil {
		return "", fmt.Errorf("vertex: nil token source")
	}
	tok, err := p.ts.Token()
	if err != nil {
		return "", fmt.Errorf("vertex: google token: %w", err)
	}
	return tok.AccessToken, nil
}

// DefaultTokenProviderFactory builds a Google OAuth2 token provider:
// - If secret service_account_json is present, uses Google service-account credentials from that JSON.
// - Otherwise uses ADC / default application credentials (env, workload identity, gcloud, metadata).
func DefaultTokenProviderFactory(cfg Config, secrets backendplugin.SecretBundle) (TokenProvider, error) {
	ctx := context.Background()
	saJSON := secrets.Values["service_account_json"]
	if len(saJSON) == 0 {
		saJSON = secrets.Values["service-account-json"]
	}
	if len(saJSON) > 0 && strings.TrimSpace(string(saJSON)) != "" {
		creds, err := google.CredentialsFromJSONWithType(ctx, saJSON, google.ServiceAccount, DefaultCloudPlatformScope)
		if err != nil {
			return nil, fmt.Errorf("vertex: credentials from service account JSON: %w", err)
		}
		return NewGoogleTokenProvider(creds.TokenSource), nil
	}

	creds, err := google.FindDefaultCredentials(ctx, DefaultCloudPlatformScope)
	if err != nil {
		return nil, fmt.Errorf("vertex: find default application credentials (ADC): %w", err)
	}
	return NewGoogleTokenProvider(creds.TokenSource), nil
}
