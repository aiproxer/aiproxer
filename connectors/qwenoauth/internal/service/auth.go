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
	"time"

	"github.com/aiproxer/aiproxer/connector-support/oauthcred"
)

type TokenProvider interface {
	Token(ctx context.Context) (string, error)
	ForceRefresh(ctx context.Context) (string, error)
}

type StaticTokenProvider struct {
	token string
}

func NewStaticTokenProvider(token string) *StaticTokenProvider {
	return &StaticTokenProvider{token: token}
}

func (p *StaticTokenProvider) Token(context.Context) (string, error) {
	if p.token == "" {
		return "", errors.New("qwen-oauth: static token is empty")
	}
	return p.token, nil
}

func (p *StaticTokenProvider) ForceRefresh(context.Context) (string, error) {
	if p.token == "" {
		return "", errors.New("qwen-oauth: static token is empty")
	}
	return p.token, nil
}

type OAuthTokenProvider struct {
	session *oauthcred.Session
}

func NewOAuthTokenProvider(session *oauthcred.Session) *OAuthTokenProvider {
	return &OAuthTokenProvider{session: session}
}

func (p *OAuthTokenProvider) Token(ctx context.Context) (string, error) {
	if p.session == nil {
		return "", errors.New("qwen-oauth: nil oauth session")
	}
	tok, err := p.session.Token(ctx)
	if err != nil {
		return "", fmt.Errorf("qwen-oauth: oauth token: %w", err)
	}
	return tok, nil
}

func (p *OAuthTokenProvider) ForceRefresh(ctx context.Context) (string, error) {
	if p.session == nil {
		return "", errors.New("qwen-oauth: nil oauth session")
	}
	rec, err := p.session.Record()
	if err != nil {
		return "", fmt.Errorf("qwen-oauth: force refresh load: %w", err)
	}
	rec.Expiry = time.Time{}
	if err := p.session.Save(rec); err != nil {
		return "", fmt.Errorf("qwen-oauth: force refresh save: %w", err)
	}
	return p.session.Token(ctx)
}

type QwenOAuthRefresher struct {
	TokenURL   string
	ClientID   string
	HTTPClient *http.Client
}

type qwenTokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	ResourceURL  string `json:"resource_url"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

func (r *QwenOAuthRefresher) Refresh(ctx context.Context, refreshToken string) (string, string, time.Time, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return "", "", time.Time{}, errors.New("qwen-oauth: refresh token is empty")
	}
	if strings.TrimSpace(r.ClientID) == "" {
		return "", "", time.Time{}, errors.New("qwen-oauth: client_id is required for token refresh")
	}

	tokenEndpoint := r.TokenURL
	if tokenEndpoint == "" {
		tokenEndpoint = DefaultTokenURL
	}

	u, err := url.Parse(tokenEndpoint)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("qwen-oauth: parse token endpoint %q: %w", tokenEndpoint, err)
	}
	hostname := strings.ToLower(u.Hostname())
	if hostname != "127.0.0.1" && hostname != "localhost" &&
		hostname != "chat.qwen.ai" && !strings.HasSuffix(hostname, ".qwen.ai") {
		return "", "", time.Time{}, fmt.Errorf("qwen-oauth: token endpoint %q on untrusted host %q (expected chat.qwen.ai or *.qwen.ai)", tokenEndpoint, hostname)
	}

	data := url.Values{}
	data.Set("grant_type", "refresh_token")
	data.Set("refresh_token", refreshToken)
	data.Set("client_id", r.ClientID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("qwen-oauth: create refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", DefaultUserAgent)

	hc := r.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}

	resp, err := hc.Do(req)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("qwen-oauth: refresh request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("qwen-oauth: read refresh response: %w", err)
	}

	if resp.StatusCode >= 400 {
		var errResp qwenTokenResponse
		_ = json.Unmarshal(bodyBytes, &errResp)
		errDesc := errResp.ErrorDesc
		if errDesc == "" {
			errDesc = errResp.Error
		}
		if errDesc == "" {
			errDesc = string(bodyBytes)
		}

		cleanErr := fmt.Errorf("qwen-oauth: token refresh rejected (HTTP %d): %s", resp.StatusCode, errDesc)
		isTerminal := resp.StatusCode == http.StatusBadRequest ||
			resp.StatusCode == http.StatusUnauthorized ||
			resp.StatusCode == http.StatusForbidden ||
			errResp.Error == "invalid_grant" ||
			errResp.Error == "unauthorized_client"
		if isTerminal {
			return "", "", time.Time{}, fmt.Errorf("%w: %v", oauthcred.ErrTerminalRefresh, cleanErr)
		}
		return "", "", time.Time{}, cleanErr
	}

	var tokResp qwenTokenResponse
	if err := json.Unmarshal(bodyBytes, &tokResp); err != nil {
		return "", "", time.Time{}, fmt.Errorf("qwen-oauth: decode token response: %w", err)
	}

	if tokResp.AccessToken == "" {
		return "", "", time.Time{}, errors.New("qwen-oauth: token response missing access_token")
	}

	newRefresh := tokResp.RefreshToken
	if newRefresh == "" {
		newRefresh = refreshToken
	}

	expiresIn := tokResp.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	expiry := time.Now().Add(time.Duration(expiresIn) * time.Second)

	return tokResp.AccessToken, newRefresh, expiry, nil
}
