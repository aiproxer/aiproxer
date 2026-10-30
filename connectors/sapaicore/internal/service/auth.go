package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

type TokenProvider interface {
	Token(ctx context.Context) (string, error)
}

type StaticTokenProvider string

func (s StaticTokenProvider) Token(context.Context) (string, error) {
	return string(s), nil
}

type TokenProviderFactory func(ctx context.Context, cfg Config, sk ServiceKey, secrets backendplugin.SecretBundle) (TokenProvider, error)

type OAuthTokenProvider struct {
	tokenURL     string
	clientID     string
	clientSecret string
	httpClient   *http.Client

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

func NewOAuthTokenProvider(tokenURL, clientID, clientSecret string, httpClient *http.Client) *OAuthTokenProvider {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: DefaultHTTPTimeout}
	}
	return &OAuthTokenProvider{
		tokenURL:     tokenURL,
		clientID:     clientID,
		clientSecret: clientSecret,
		httpClient:   httpClient,
	}
}

func (p *OAuthTokenProvider) Token(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// If token exists and has more than 10s lifetime remaining, reuse it.
	if p.token != "" && time.Until(p.expiresAt) > 10*time.Second {
		return p.token, nil
	}

	tok, expiresAt, err := p.exchange(ctx)
	if err != nil {
		return "", err
	}
	p.token = tok
	p.expiresAt = expiresAt
	return tok, nil
}

type oauthTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
}

func (p *OAuthTokenProvider) exchange(ctx context.Context) (string, time.Time, error) {
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {p.clientID},
		"client_secret": {p.clientSecret},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sapaicore: new oauth token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		msg := err.Error()
		if p.clientSecret != "" {
			msg = strings.ReplaceAll(msg, p.clientSecret, "[REDACTED]")
		}
		return "", time.Time{}, fmt.Errorf("sapaicore: oauth token exchange request failed: %s", msg)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sapaicore: read oauth token response: %w", err)
	}

	bodyStr := string(body)
	if p.clientSecret != "" {
		bodyStr = strings.ReplaceAll(bodyStr, p.clientSecret, "[REDACTED]")
	}

	if resp.StatusCode != http.StatusOK {
		return "", time.Time{}, fmt.Errorf("sapaicore: oauth token exchange failed (status %d): %s", resp.StatusCode, bodyStr)
	}

	var tr oauthTokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", time.Time{}, fmt.Errorf("sapaicore: unmarshal oauth token response: %w", err)
	}
	tok := strings.TrimSpace(tr.AccessToken)
	if tok == "" {
		return "", time.Time{}, fmt.Errorf("sapaicore: oauth token response missing access_token")
	}

	now := time.Now()
	var expiresAt time.Time
	if tr.ExpiresIn > 0 {
		expiresAt = now.Add(time.Duration(tr.ExpiresIn) * time.Second)
	} else {
		expiresAt = now.Add(1 * time.Hour)
	}

	return tok, expiresAt, nil
}

func DefaultOAuthTokenProviderFactory(ctx context.Context, cfg Config, sk ServiceKey, secrets backendplugin.SecretBundle) (TokenProvider, error) {
	authURL := cfg.OAuthURL(sk.URL)
	hc, err := cfg.HTTPClient()
	if err != nil {
		return nil, err
	}
	return NewOAuthTokenProvider(authURL, sk.ClientID, sk.ClientSecret, hc), nil
}
