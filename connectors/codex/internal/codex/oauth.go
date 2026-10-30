package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const oauthRefreshTimeout = 30 * time.Second

func refreshOAuthAccessToken(ctx context.Context, cfg Config, client *http.Client) (Config, error) {
	ctx, cancel := context.WithTimeout(ctx, oauthRefreshTimeout)
	defer cancel()
	refreshToken := strings.TrimSpace(cfg.RefreshToken)
	if refreshToken == "" {
		return cfg, fmt.Errorf("refresh token is empty")
	}
	tokenURL := strings.TrimSpace(cfg.OAuthTokenURL)
	if tokenURL == "" {
		tokenURL = DefaultOAuthTokenURL
	}
	clientID := strings.TrimSpace(cfg.OAuthClientID)
	if clientID == "" {
		clientID = DefaultOAuthClientID
	}
	if client == nil {
		client = http.DefaultClient
	}

	payload := map[string]string{
		"client_id":     clientID,
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"scope":         "openid profile email",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return cfg, fmt.Errorf("marshal refresh payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, bytes.NewReader(body))
	if err != nil {
		return cfg, fmt.Errorf("build refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return cfg, fmt.Errorf("refresh request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return cfg, fmt.Errorf("refresh HTTP %d: %s", resp.StatusCode, truncateErrorMessage(string(respBody), upstreamErrorBodyMax))
	}

	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return cfg, fmt.Errorf("decode refresh response: %w", err)
	}
	accessToken := jsonRawString(parsed, "access_token", "accessToken")
	if accessToken == "" {
		return cfg, fmt.Errorf("refresh response missing access token")
	}
	cfg.AccessToken = accessToken
	if nextRefresh := jsonRawString(parsed, "refresh_token", "refreshToken"); nextRefresh != "" {
		cfg.RefreshToken = nextRefresh
	}
	return cfg, nil
}
