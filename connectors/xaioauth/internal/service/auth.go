package service

import (
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

// TokenProvider provides an access token for xAI inference APIs and supports force-refresh.
type TokenProvider interface {
	Token(ctx context.Context) (string, error)
	ForceRefresh(ctx context.Context) (string, error)
}

// StaticTokenProvider provides a fixed token.
type StaticTokenProvider struct {
	token string
}

func NewStaticTokenProvider(token string) *StaticTokenProvider {
	return &StaticTokenProvider{token: strings.TrimSpace(token)}
}

func (p *StaticTokenProvider) Token(context.Context) (string, error) {
	if p.token == "" {
		return "", errors.New("xai-oauth: empty access token")
	}
	return p.token, nil
}

func (p *StaticTokenProvider) ForceRefresh(context.Context) (string, error) {
	if p.token == "" {
		return "", errors.New("xai-oauth: empty access token")
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
		return "", errors.New("xai-oauth: nil oauth session")
	}
	tok, err := p.session.Token(ctx)
	if err != nil {
		return "", fmt.Errorf("xai-oauth: oauth token: %w", err)
	}
	return tok, nil
}

func (p *OAuthTokenProvider) ForceRefresh(ctx context.Context) (string, error) {
	if p.session == nil {
		return "", errors.New("xai-oauth: nil oauth session")
	}
	rec, err := p.session.Record()
	if err != nil {
		return "", fmt.Errorf("xai-oauth: force refresh load: %w", err)
	}
	rec.Expiry = time.Time{}
	if err := p.session.Save(rec); err != nil {
		return "", fmt.Errorf("xai-oauth: force refresh save: %w", err)
	}
	return p.session.Token(ctx)
}

// XAIOAuthRefresher implements oauthcred.Refresher using OIDC discovery and token endpoint.
type XAIOAuthRefresher struct {
	IssuerURL     string
	TokenEndpoint string
	ClientID      string
	HTTPClient    *http.Client

	mu                  sync.Mutex
	cachedTokenEndpoint string
}

type oidcDiscoveryResponse struct {
	Issuer        string `json:"issuer"`
	TokenEndpoint string `json:"token_endpoint"`
}

type xaiTokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

func (r *XAIOAuthRefresher) discoverTokenEndpoint(ctx context.Context, hc *http.Client) (string, error) {
	r.mu.Lock()
	if r.cachedTokenEndpoint != "" {
		ep := r.cachedTokenEndpoint
		r.mu.Unlock()
		return ep, nil
	}
	r.mu.Unlock()

	if r.TokenEndpoint != "" {
		if err := validateEndpoint(r.TokenEndpoint); err != nil {
			return "", err
		}
		r.mu.Lock()
		r.cachedTokenEndpoint = r.TokenEndpoint
		r.mu.Unlock()
		return r.TokenEndpoint, nil
	}

	issuer := strings.TrimRight(r.IssuerURL, "/")
	discoveryURL := issuer + "/.well-known/openid-configuration"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", DefaultUserAgent)

	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("xai-oauth: oidc discovery request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("xai-oauth: read oidc discovery response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", &oauthcred.HTTPError{
			StatusCode: resp.StatusCode,
			Body:       string(bodyBytes),
		}
	}

	var disc oidcDiscoveryResponse
	if err := json.Unmarshal(bodyBytes, &disc); err != nil {
		return "", fmt.Errorf("xai-oauth: decode oidc discovery: %w", err)
	}

	ep := strings.TrimSpace(disc.TokenEndpoint)
	if ep == "" {
		return "", errors.New("xai-oauth: oidc discovery response missing token_endpoint")
	}

	if err := validateEndpoint(ep); err != nil {
		return "", err
	}

	r.mu.Lock()
	r.cachedTokenEndpoint = ep
	r.mu.Unlock()
	return ep, nil
}

func validateEndpoint(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("xai-oauth: invalid endpoint URL %q: %w", rawURL, err)
	}
	host := strings.ToLower(u.Hostname())
	// Allow loopback/test hosts in testing
	if host == "127.0.0.1" || host == "localhost" || strings.HasSuffix(host, ".local") {
		return nil
	}
	if u.Scheme != "https" {
		return fmt.Errorf("xai-oauth: endpoint must be HTTPS, got %q", rawURL)
	}
	if host != "x.ai" && !strings.HasSuffix(host, ".x.ai") {
		return fmt.Errorf("xai-oauth: endpoint host %q must be x.ai or a *.x.ai subdomain", host)
	}
	return nil
}

func (r *XAIOAuthRefresher) Refresh(ctx context.Context, refreshToken string) (access, refresh string, expiry time.Time, err error) {
	if strings.TrimSpace(refreshToken) == "" {
		return "", "", time.Time{}, errors.New("xai-oauth: empty refresh token")
	}

	hc := r.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}

	tokenEndpoint, err := r.discoverTokenEndpoint(ctx, hc)
	if err != nil {
		return "", "", time.Time{}, err
	}

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

	var tokResp xaiTokenResponse
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
		return "", "", time.Time{}, errors.New("xai-oauth: empty access_token in refresh response")
	}

	var exp time.Time
	if tokResp.ExpiresIn > 0 {
		exp = time.Now().Add(time.Duration(tokResp.ExpiresIn) * time.Second)
	} else {
		exp = time.Now().Add(6 * time.Hour)
	}

	newRefresh := tokResp.RefreshToken
	if newRefresh == "" {
		newRefresh = refreshToken
	}

	return tokResp.AccessToken, newRefresh, exp, nil
}
