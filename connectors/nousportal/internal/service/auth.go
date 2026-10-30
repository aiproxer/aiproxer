package service

import (
	"context"
	"encoding/base64"
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

// TokenProvider provides an access token for Nous inference APIs and supports force-refresh.
type TokenProvider interface {
	Token(ctx context.Context) (string, error)
	ForceRefresh(ctx context.Context) (string, error)
}

// StaticTokenProvider provides a fixed token (e.g. static API key or session key).
type StaticTokenProvider struct {
	token string
}

func NewStaticTokenProvider(token string) *StaticTokenProvider {
	return &StaticTokenProvider{token: strings.TrimSpace(token)}
}

func (p *StaticTokenProvider) Token(context.Context) (string, error) {
	if p.token == "" {
		return "", errors.New("nous-portal: empty access token")
	}
	return p.token, nil
}

func (p *StaticTokenProvider) ForceRefresh(context.Context) (string, error) {
	if p.token == "" {
		return "", errors.New("nous-portal: empty access token")
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
		return "", errors.New("nous-portal: nil oauth session")
	}
	tok, err := p.session.Token(ctx)
	if err != nil {
		return "", fmt.Errorf("nous-portal: oauth token: %w", err)
	}
	return tok, nil
}

func (p *OAuthTokenProvider) ForceRefresh(ctx context.Context) (string, error) {
	if p.session == nil {
		return "", errors.New("nous-portal: nil oauth session")
	}
	rec, err := p.session.Record()
	if err != nil {
		return "", fmt.Errorf("nous-portal: force refresh load: %w", err)
	}
	// Expire stored record to trigger a refresh via session.Token.
	rec.Expiry = time.Time{}
	if err := p.session.Save(rec); err != nil {
		return "", fmt.Errorf("nous-portal: force refresh save: %w", err)
	}
	return p.session.Token(ctx)
}

// NousOAuthRefresher implements oauthcred.Refresher for Nous Portal.
type NousOAuthRefresher struct {
	PortalURL  string
	ClientID   string
	HTTPClient *http.Client
}

type nousTokenResponse struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int64  `json:"expires_in"`
	Scope            string `json:"scope"`
	InferenceBaseURL string `json:"inference_base_url"`
	Error            string `json:"error"`
	ErrorDesc        string `json:"error_description"`
}

func (r *NousOAuthRefresher) Refresh(ctx context.Context, refreshToken string) (access, refresh string, expiry time.Time, err error) {
	tokenEndpoint := fmt.Sprintf("%s/api/oauth/token", strings.TrimRight(r.PortalURL, "/"))
	data := url.Values{
		"grant_type": {"refresh_token"},
	}
	if r.ClientID != "" {
		data.Set("client_id", r.ClientID)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return "", "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("x-nous-refresh-token", refreshToken)
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

	var tokResp nousTokenResponse
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
		return "", "", time.Time{}, errors.New("nous-portal: empty access_token in oauth refresh response")
	}

	// Validate JWT claims if token is a JWT; otherwise treat as opaque session key.
	jwtExp, isJWT, err := ValidateInvokeJWT(tokResp.AccessToken, tokResp.Scope)
	if err != nil {
		return "", "", time.Time{}, err
	}

	var exp time.Time
	if isJWT && !jwtExp.IsZero() {
		exp = jwtExp
	} else if tokResp.ExpiresIn > 0 {
		exp = time.Now().Add(time.Duration(tokResp.ExpiresIn) * time.Second)
	} else {
		exp = time.Now().Add(1 * time.Hour)
	}

	newRefresh := tokResp.RefreshToken
	if newRefresh == "" {
		newRefresh = refreshToken
	}

	return tokResp.AccessToken, newRefresh, exp, nil
}

// ValidateInvokeJWT parses unverified JWT claims if token has a JWT structure.
// When JWT structure is present, it verifies that the token contains the "inference:invoke" scope.
func ValidateInvokeJWT(token string, responseScope string) (time.Time, bool, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, false, nil
	}

	payloadBytes, err := decodeBase64URL(parts[1])
	if err != nil {
		return time.Time{}, false, nil
	}

	var claims map[string]any
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return time.Time{}, false, nil
	}

	scopes := make(map[string]bool)
	collectScopes := func(val any) {
		switch v := val.(type) {
		case string:
			for s := range strings.FieldsSeq(v) {
				scopes[strings.TrimSpace(s)] = true
			}
		case []any:
			for _, elem := range v {
				if s, ok := elem.(string); ok {
					for part := range strings.FieldsSeq(s) {
						scopes[strings.TrimSpace(part)] = true
					}
				}
			}
		}
	}
	collectScopes(claims["scope"])
	collectScopes(claims["scp"])
	collectScopes(responseScope)

	if len(scopes) > 0 && !scopes[ScopeInferenceInvoke] {
		return time.Time{}, true, fmt.Errorf("nous-portal: access token missing required %q scope", ScopeInferenceInvoke)
	}

	var expTime time.Time
	if expVal, ok := claims["exp"]; ok {
		switch v := expVal.(type) {
		case float64:
			expTime = time.Unix(int64(v), 0)
		case int64:
			expTime = time.Unix(v, 0)
		case json.Number:
			if n, err := v.Int64(); err == nil {
				expTime = time.Unix(n, 0)
			}
		}
	}

	return expTime, true, nil
}

func decodeBase64URL(s string) ([]byte, error) {
	if l := len(s) % 4; l > 0 {
		s += strings.Repeat("=", 4-l)
	}
	return base64.URLEncoding.DecodeString(s)
}
