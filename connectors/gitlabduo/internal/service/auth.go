package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/oauthcred"
)

// TokenProvider returns a valid bearer token for GitLab instance APIs.
type TokenProvider interface {
	Token(ctx context.Context) (string, error)
}

// StaticPATProvider provides a static Personal Access Token.
type StaticPATProvider struct {
	token string
}

func NewStaticPATProvider(token string) *StaticPATProvider {
	return &StaticPATProvider{token: strings.TrimSpace(token)}
}

func (p *StaticPATProvider) Token(context.Context) (string, error) {
	if p.token == "" {
		return "", errors.New("gitlab-duo: empty personal access token")
	}
	return p.token, nil
}

// OAuthTokenProvider wraps oauthcred.Session.
type OAuthTokenProvider struct {
	session *oauthcred.Session
}

func NewOAuthTokenProvider(session *oauthcred.Session) *OAuthTokenProvider {
	return &OAuthTokenProvider{session: session}
}

func (p *OAuthTokenProvider) Token(ctx context.Context) (string, error) {
	if p.session == nil {
		return "", errors.New("gitlab-duo: nil oauth session")
	}
	tok, err := p.session.Token(ctx)
	if err != nil {
		return "", fmt.Errorf("gitlab-duo: oauth token: %w", err)
	}
	return tok, nil
}

// GitLabOAuthRefresher implements oauthcred.Refresher for GitLab.
type GitLabOAuthRefresher struct {
	InstanceURL string
	ClientID    string
	HTTPClient  *http.Client
}

type gitlabTokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	CreatedAt    int64  `json:"created_at"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

func (r *GitLabOAuthRefresher) Refresh(ctx context.Context, refreshToken string) (access, refresh string, expiry time.Time, err error) {
	tokenEndpoint := fmt.Sprintf("%s/oauth/token", strings.TrimRight(r.InstanceURL, "/"))
	data := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	}
	if r.ClientID != "" {
		data.Set("client_id", r.ClientID)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return "", "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", DefaultUserAgent)

	hc := r.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}

	resp, err := hc.Do(req)
	if err != nil {
		return "", "", time.Time{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("read oauth response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", "", time.Time{}, &oauthcred.HTTPError{
			StatusCode: resp.StatusCode,
			Body:       string(bodyBytes),
		}
	}

	var tokResp gitlabTokenResponse
	if err := json.Unmarshal(bodyBytes, &tokResp); err != nil {
		return "", "", time.Time{}, fmt.Errorf("decode oauth response: %w", err)
	}

	if tokResp.Error != "" {
		return "", "", time.Time{}, &oauthcred.HTTPError{
			StatusCode: http.StatusBadRequest,
			Body:       fmt.Sprintf("%s: %s", tokResp.Error, tokResp.ErrorDesc),
		}
	}

	if tokResp.AccessToken == "" {
		return "", "", time.Time{}, errors.New("empty access_token in oauth refresh response")
	}

	exp := time.Now().Add(time.Duration(tokResp.ExpiresIn) * time.Second)
	newRefresh := tokResp.RefreshToken
	if newRefresh == "" {
		newRefresh = refreshToken
	}

	return tokResp.AccessToken, newRefresh, exp, nil
}

// DirectAccessToken represents the credentials returned by GitLab's direct_access endpoint.
type DirectAccessToken struct {
	Token   string            `json:"token"`
	Headers map[string]string `json:"headers"`
	BaseURL string            `json:"base_url,omitempty"`
}

type directAccessRequest struct {
	FeatureFlags map[string]bool `json:"feature_flags,omitempty"`
}

type directAccessResponse struct {
	Token     string            `json:"token"`
	Headers   map[string]string `json:"headers"`
	BaseURL   string            `json:"base_url,omitempty"`
	ExpiresAt any               `json:"expires_at,omitempty"`
}

// DirectAccessManager retrieves and caches short-lived direct access tokens for GitLab AI Gateway.
type DirectAccessManager interface {
	GetDirectAccessToken(ctx context.Context, forceRefresh bool) (DirectAccessToken, error)
}

type HTTPDirectAccessManager struct {
	InstanceURL   string
	TokenProvider TokenProvider
	HTTPClient    *http.Client

	mu        sync.Mutex
	cached    DirectAccessToken
	expiresAt time.Time
}

func NewHTTPDirectAccessManager(instanceURL string, tp TokenProvider, hc *http.Client) *HTTPDirectAccessManager {
	return &HTTPDirectAccessManager{
		InstanceURL:   strings.TrimRight(instanceURL, "/"),
		TokenProvider: tp,
		HTTPClient:    hc,
	}
}

func (m *HTTPDirectAccessManager) GetDirectAccessToken(ctx context.Context, forceRefresh bool) (DirectAccessToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	if !forceRefresh && m.cached.Token != "" && m.expiresAt.After(now.Add(60*time.Second)) {
		return m.cached, nil
	}

	endpoint := fmt.Sprintf("%s/api/v4/ai/third_party_agents/direct_access", m.InstanceURL)
	reqPayload := directAccessRequest{
		FeatureFlags: map[string]bool{
			"duo_agent_platform":              true,
			"duo_agent_platform_agentic_chat": true,
		},
	}
	reqBody, err := json.Marshal(reqPayload)
	if err != nil {
		return DirectAccessToken{}, err
	}

	hc := m.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}

	for attempt := range 2 {
		token, err := m.TokenProvider.Token(ctx)
		if err != nil {
			return DirectAccessToken{}, fmt.Errorf("gitlab-duo: get auth token: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
		if err != nil {
			return DirectAccessToken{}, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", DefaultUserAgent)

		resp, err := hc.Do(req)
		if err != nil {
			return DirectAccessToken{}, fmt.Errorf("gitlab-duo: direct_access request failed: %w", err)
		}

		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if err != nil {
			return DirectAccessToken{}, fmt.Errorf("gitlab-duo: read direct_access body: %w", err)
		}

		if resp.StatusCode == http.StatusForbidden {
			// Entitlement fail-closed: do not retry 403
			return DirectAccessToken{}, fmt.Errorf("gitlab-duo: access denied (403): GitLab Duo requires GitLab Ultimate with Duo Enterprise add-on / AI access: %s", string(body))
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			// Retry once for 401
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return DirectAccessToken{}, fmt.Errorf("gitlab-duo: direct_access failed (status %d): %s", resp.StatusCode, string(body))
		}

		var parsedResp directAccessResponse
		if err := json.Unmarshal(body, &parsedResp); err != nil {
			return DirectAccessToken{}, fmt.Errorf("gitlab-duo: parse direct_access response: %w", err)
		}

		if parsedResp.Token == "" {
			return DirectAccessToken{}, errors.New("gitlab-duo: direct_access returned empty token")
		}

		parsed := DirectAccessToken{
			Token:   parsedResp.Token,
			Headers: parsedResp.Headers,
			BaseURL: strings.TrimRight(parsedResp.BaseURL, "/"),
		}

		m.cached = parsed
		m.expiresAt = now.Add(25 * time.Minute)
		if parsedResp.ExpiresAt != nil {
			switch v := parsedResp.ExpiresAt.(type) {
			case string:
				if t, err := time.Parse(time.RFC3339, v); err == nil {
					m.expiresAt = t
				}
			case float64:
				m.expiresAt = time.Unix(int64(v), 0)
			}
		}
		return parsed, nil
	}

	return DirectAccessToken{}, errors.New("gitlab-duo: direct_access failed after retry")
}
