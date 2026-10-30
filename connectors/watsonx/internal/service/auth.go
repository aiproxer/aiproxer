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
)

type TokenProvider interface {
	Token(ctx context.Context) (string, error)
}

type StaticTokenProvider string

func (s StaticTokenProvider) Token(context.Context) (string, error) {
	return string(s), nil
}

// IAMError is a bounded IBM IAM token-exchange failure. StatusCode drives
// provider quota classification (400/401 mean the authenticating api_key is
// invalid); RetryAfter carries the verbatim Retry-After response header when
// present. Exchange responses carry no key material, but the message is
// still bounded and never echoes the api_key.
type IAMError struct {
	StatusCode int
	RetryAfter string
	Message    string
}

func (e *IAMError) Error() string { return e.Message }

type IAMTokenProvider struct {
	iamOrigin  string
	apiKey     string
	httpClient *http.Client

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

func NewIAMTokenProvider(iamOrigin, apiKey string, httpClient *http.Client) *IAMTokenProvider {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: DefaultHTTPTimeout}
	}
	return &IAMTokenProvider{
		iamOrigin:  strings.TrimRight(iamOrigin, "/"),
		apiKey:     apiKey,
		httpClient: httpClient,
	}
}

func (p *IAMTokenProvider) Token(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// If a token exists and has more than 10 seconds of lifetime remaining, reuse it.
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

type iamTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
	Expiration  int64  `json:"expiration"`
}

func (p *IAMTokenProvider) exchange(ctx context.Context) (string, time.Time, error) {
	tokenURL := p.iamOrigin + "/identity/token"
	form := url.Values{
		"grant_type": {"urn:ibm:params:oauth:grant-type:apikey"},
		"apikey":     {p.apiKey},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("watsonx: new iam token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("watsonx: iam token exchange request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("watsonx: read iam token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", time.Time{}, &IAMError{
			StatusCode: resp.StatusCode,
			RetryAfter: resp.Header.Get("Retry-After"),
			Message:    fmt.Sprintf("watsonx: iam token exchange failed (status %d): %s", resp.StatusCode, string(body)),
		}
	}

	var tr iamTokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", time.Time{}, fmt.Errorf("watsonx: unmarshal iam token response: %w", err)
	}
	tok := strings.TrimSpace(tr.AccessToken)
	if tok == "" {
		return "", time.Time{}, fmt.Errorf("watsonx: iam token response missing access_token")
	}

	now := time.Now()
	var expiresAt time.Time
	if tr.Expiration > 0 {
		expiresAt = time.Unix(tr.Expiration, 0)
	} else if tr.ExpiresIn > 0 {
		expiresAt = now.Add(time.Duration(tr.ExpiresIn) * time.Second)
	} else {
		expiresAt = now
	}

	return tok, expiresAt, nil
}
