package service

import (
	"context"
	"crypto/rand"
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

// TokenProvider returns a valid access token for MiniMax inference.
type TokenProvider interface {
	Token(ctx context.Context) (string, error)
	ForceRefresh(ctx context.Context) (string, error)
}

// StaticTokenProvider serves an immutable bearer token (e.g. from secrets).
type StaticTokenProvider struct {
	AccessToken string
}

func (s *StaticTokenProvider) Token(ctx context.Context) (string, error) {
	if s.AccessToken == "" {
		return "", errors.New("minimax-oauth: static token is empty")
	}
	return s.AccessToken, nil
}

func (s *StaticTokenProvider) ForceRefresh(ctx context.Context) (string, error) {
	return s.Token(ctx)
}

// OAuthTokenProvider resolves tokens from an oauthcred session with caching and auto-refresh.
type OAuthTokenProvider struct {
	session *oauthcred.Session
}

func NewOAuthTokenProvider(session *oauthcred.Session) *OAuthTokenProvider {
	return &OAuthTokenProvider{session: session}
}

func (p *OAuthTokenProvider) Token(ctx context.Context) (string, error) {
	if p.session == nil {
		return "", errors.New("minimax-oauth: oauth session not configured")
	}
	return p.session.Token(ctx)
}

func (p *OAuthTokenProvider) ForceRefresh(ctx context.Context) (string, error) {
	if p.session == nil {
		return "", errors.New("minimax-oauth: oauth session not configured")
	}
	rec, err := p.session.Record()
	if err != nil {
		return "", fmt.Errorf("minimax-oauth: force refresh load: %w", err)
	}
	rec.Expiry = time.Time{}
	if err := p.session.Save(rec); err != nil {
		return "", fmt.Errorf("minimax-oauth: force refresh save: %w", err)
	}
	return p.session.Token(ctx)
}

func (p *OAuthTokenProvider) Session() *oauthcred.Session {
	return p.session
}

// MiniMaxOAuthRefresher implements oauthcred.Refresher against MiniMax's POST /oauth/token.
type MiniMaxOAuthRefresher struct {
	PortalBaseURL string
	ClientID      string
	HTTPClient    *http.Client
}

func (r *MiniMaxOAuthRefresher) getHTTPClient() *http.Client {
	if r.HTTPClient != nil {
		return r.HTTPClient
	}
	return http.DefaultClient
}

func (r *MiniMaxOAuthRefresher) Refresh(ctx context.Context, refreshToken string) (string, string, time.Time, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return "", "", time.Time{}, fmt.Errorf("%w: minimax-oauth: empty refresh token", oauthcred.ErrTerminalRefresh)
	}

	endpoint := fmt.Sprintf("%s/oauth/token", strings.TrimRight(r.PortalBaseURL, "/"))
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {r.ClientID},
		"refresh_token": {refreshToken},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", DefaultUserAgent)

	resp, err := r.getHTTPClient().Do(req)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("minimax-oauth: refresh network request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		errBytes, _ := io.ReadAll(io.LimitReader(resp.Body, ErrorBodyLimit))
		return "", "", time.Time{}, &oauthcred.HTTPError{
			StatusCode: resp.StatusCode,
			Body:       string(errBytes),
		}
	}

	var payload struct {
		Status       string `json:"status"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiredIn    any    `json:"expired_in"`
		BaseResp     struct {
			StatusCode int    `json:"status_code"`
			StatusMsg  string `json:"status_msg"`
		} `json:"base_resp"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", "", time.Time{}, fmt.Errorf("minimax-oauth: decode refresh response JSON: %w", err)
	}

	if payload.Status != "success" {
		msg := payload.BaseResp.StatusMsg
		if msg == "" {
			msg = payload.Status
		}
		if msg == "" {
			msg = "refresh response status not success"
		}
		return "", "", time.Time{}, fmt.Errorf("%w: minimax-oauth: %s", oauthcred.ErrTerminalRefresh, msg)
	}

	if payload.AccessToken == "" {
		return "", "", time.Time{}, fmt.Errorf("%w: minimax-oauth: refresh response missing access_token", oauthcred.ErrTerminalRefresh)
	}

	newRefreshToken := payload.RefreshToken
	if newRefreshToken == "" {
		newRefreshToken = refreshToken
	}

	expiredInInt := parseExpiredIn(payload.ExpiredIn)
	expiry := ResolveTokenExpiryUnix(expiredInInt, time.Now())

	return payload.AccessToken, newRefreshToken, expiry, nil
}

// UserCodeResponse holds fields returned from POST /oauth/code.
type UserCodeResponse struct {
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiredIn       int64  `json:"expired_in"`
	State           string `json:"state"`
	Interval        int    `json:"interval,omitempty"`
}

// RequestUserCode initiates the PKCE device authorization flow against POST /oauth/code.
func RequestUserCode(ctx context.Context, client *http.Client, portalBaseURL, clientID, scope, challenge, state string) (UserCodeResponse, error) {
	if client == nil {
		client = http.DefaultClient
	}
	endpoint := fmt.Sprintf("%s/oauth/code", strings.TrimRight(portalBaseURL, "/"))
	form := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"scope":                 {scope},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"state":                 {state},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return UserCodeResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", DefaultUserAgent)
	req.Header.Set("x-request-id", newUUID())

	resp, err := client.Do(req)
	if err != nil {
		return UserCodeResponse{}, fmt.Errorf("minimax-oauth: request user code: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		errText := readBoundedError(resp, ErrorBodyLimit)
		return UserCodeResponse{}, fmt.Errorf("minimax-oauth: request user code failed (status %d): %s", resp.StatusCode, errText)
	}

	var raw struct {
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		ExpiredIn       any    `json:"expired_in"`
		State           string `json:"state"`
		Interval        any    `json:"interval"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return UserCodeResponse{}, fmt.Errorf("minimax-oauth: decode user code response: %w", err)
	}

	if raw.UserCode == "" || raw.VerificationURI == "" || raw.ExpiredIn == nil {
		return UserCodeResponse{}, errors.New("minimax-oauth: response missing required fields (user_code, verification_uri, expired_in)")
	}

	if raw.State != state {
		return UserCodeResponse{}, fmt.Errorf("minimax-oauth: state mismatch (possible CSRF); expected %q, got %q", state, raw.State)
	}

	expiredInInt := parseExpiredIn(raw.ExpiredIn)
	intervalInt := int(parseExpiredIn(raw.Interval))

	return UserCodeResponse{
		UserCode:        raw.UserCode,
		VerificationURI: raw.VerificationURI,
		ExpiredIn:       expiredInInt,
		State:           raw.State,
		Interval:        intervalInt,
	}, nil
}

// PollToken polls POST /oauth/token with user_code and code_verifier until completion or timeout.
func PollToken(ctx context.Context, client *http.Client, portalBaseURL, clientID, userCode, codeVerifier string, expiredIn int64, intervalMs int) (oauthcred.TokenRecord, error) {
	if client == nil {
		client = http.DefaultClient
	}
	endpoint := fmt.Sprintf("%s/oauth/token", strings.TrimRight(portalBaseURL, "/"))
	deadline := ResolveTokenExpiryUnix(expiredIn, time.Now())

	if intervalMs <= 0 {
		intervalMs = 2000
	}
	pollInterval := max(time.Duration(intervalMs)*time.Millisecond, 2*time.Second)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return oauthcred.TokenRecord{}, ctx.Err()
		default:
		}

		form := url.Values{
			"grant_type":    {DefaultGrantType},
			"client_id":     {clientID},
			"user_code":     {userCode},
			"code_verifier": {codeVerifier},
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
		if err != nil {
			return oauthcred.TokenRecord{}, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", DefaultUserAgent)

		resp, err := client.Do(req)
		if err != nil {
			return oauthcred.TokenRecord{}, fmt.Errorf("minimax-oauth: poll token network error: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			errText := readBoundedError(resp, ErrorBodyLimit)
			_ = resp.Body.Close()
			return oauthcred.TokenRecord{}, fmt.Errorf("minimax-oauth: token exchange failed (status %d): %s", resp.StatusCode, errText)
		}

		var payload struct {
			Status       string `json:"status"`
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiredIn    any    `json:"expired_in"`
			BaseResp     struct {
				StatusCode int    `json:"status_code"`
				StatusMsg  string `json:"status_msg"`
			} `json:"base_resp"`
		}

		err = json.NewDecoder(resp.Body).Decode(&payload)
		_ = resp.Body.Close()
		if err != nil {
			return oauthcred.TokenRecord{}, fmt.Errorf("minimax-oauth: decode token response: %w", err)
		}

		if payload.Status == "error" {
			msg := payload.BaseResp.StatusMsg
			if msg == "" {
				msg = "authorization denied"
			}
			return oauthcred.TokenRecord{}, fmt.Errorf("minimax-oauth: %s", msg)
		}

		if payload.Status == "success" {
			if payload.AccessToken == "" || payload.RefreshToken == "" {
				return oauthcred.TokenRecord{}, errors.New("minimax-oauth: success payload missing required token fields")
			}
			expInt := parseExpiredIn(payload.ExpiredIn)
			expiry := ResolveTokenExpiryUnix(expInt, time.Now())
			return oauthcred.TokenRecord{
				AccessToken:  payload.AccessToken,
				RefreshToken: payload.RefreshToken,
				Expiry:       expiry,
			}, nil
		}

		// Status is "pending" or other: wait pollInterval and retry
		select {
		case <-ctx.Done():
			return oauthcred.TokenRecord{}, ctx.Err()
		case <-time.After(pollInterval):
		}
	}

	return oauthcred.TokenRecord{}, errors.New("minimax-oauth: timed out waiting for authorization")
}

// ResolveTokenExpiryUnix parses expired_in (which can be seconds TTL or absolute unix milliseconds) into a time.Time.
func ResolveTokenExpiryUnix(expiredIn int64, now time.Time) time.Time {
	nowMs := now.UnixMilli()
	if expiredIn > (nowMs / 2) {
		return time.UnixMilli(expiredIn)
	}
	if expiredIn <= 0 {
		expiredIn = 3600
	}
	return now.Add(time.Duration(expiredIn) * time.Second)
}

func parseExpiredIn(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return i
		}
	}
	return 0
}

func readBoundedError(resp *http.Response, limit int) string {
	if limit <= 0 {
		limit = ErrorBodyLimit
	}
	lr := io.LimitReader(resp.Body, int64(limit+1))
	data, _ := io.ReadAll(lr)
	if len(data) > limit {
		return string(data[:limit]) + "...[truncated]"
	}
	return string(data)
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
