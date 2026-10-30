package service_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/oauthcred"
	"github.com/aiproxer/aiproxer/connectors/minimexoauth/internal/service"
)

func writeTokenFile(t *testing.T, rec oauthcred.TokenRecord) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "token.json")
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatalf("marshal token record: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	return filepath.ToSlash(path)
}

func TestStaticTokenProvider(t *testing.T) {
	p := &service.StaticTokenProvider{AccessToken: "my-token"}
	tok, err := p.Token(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "my-token" {
		t.Fatalf("expected 'my-token', got %q", tok)
	}

	empty := &service.StaticTokenProvider{}
	_, err = empty.Token(context.Background())
	if err == nil {
		t.Fatalf("expected error on empty static token")
	}
}

func TestResolveTokenExpiryUnix(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)

	// Case 1: TTL seconds (e.g. 3600)
	gotTTL := service.ResolveTokenExpiryUnix(3600, now)
	expectedTTL := now.Add(3600 * time.Second)
	if !gotTTL.Equal(expectedTTL) {
		t.Fatalf("expected %v, got %v", expectedTTL, gotTTL)
	}

	// Case 2: Unix milliseconds timestamp (e.g. now + 1 hour in ms)
	futureMs := now.Add(1 * time.Hour).UnixMilli()
	gotMs := service.ResolveTokenExpiryUnix(futureMs, now)
	expectedMs := time.UnixMilli(futureMs)
	if !gotMs.Equal(expectedMs) {
		t.Fatalf("expected %v, got %v", expectedMs, gotMs)
	}
}

func TestRequestUserCode_HappyPathAndStateMismatch(t *testing.T) {
	var capturedForm map[string]string
	var returnedState string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/code" {
			http.NotFound(w, r)
			return
		}
		_ = r.ParseForm()
		capturedForm = make(map[string]string)
		for k, v := range r.PostForm {
			if len(v) > 0 {
				capturedForm[k] = v[0]
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"user_code":        "ABCD-1234",
			"verification_uri": "https://minimax.io/verify",
			"expired_in":       300,
			"state":            returnedState,
			"interval":         2000,
		})
	}))
	t.Cleanup(srv.Close)

	// Happy path
	returnedState = "state-abc"
	resp, err := service.RequestUserCode(context.Background(), srv.Client(), srv.URL, "client-id", "scope", "challenge-s256", "state-abc")
	if err != nil {
		t.Fatalf("RequestUserCode failed: %v", err)
	}
	if resp.UserCode != "ABCD-1234" {
		t.Fatalf("expected user code ABCD-1234, got %q", resp.UserCode)
	}
	if resp.VerificationURI != "https://minimax.io/verify" {
		t.Fatalf("expected uri https://minimax.io/verify, got %q", resp.VerificationURI)
	}
	if capturedForm["code_challenge_method"] != "S256" {
		t.Fatalf("expected code_challenge_method S256, got %q", capturedForm["code_challenge_method"])
	}

	// State mismatch (CSRF guard)
	returnedState = "different-evil-state"
	_, err = service.RequestUserCode(context.Background(), srv.Client(), srv.URL, "client-id", "scope", "challenge-s256", "state-abc")
	if err == nil {
		t.Fatalf("expected state mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "state mismatch") {
		t.Fatalf("expected state mismatch in error, got: %v", err)
	}
}

func TestPollToken_PendingThenSuccess(t *testing.T) {
	var pollHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			http.NotFound(w, r)
			return
		}
		hits := atomic.AddInt32(&pollHits, 1)
		w.Header().Set("Content-Type", "application/json")
		if hits == 1 {
			// First attempt pending
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "pending",
			})
			return
		}
		// Second attempt success
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":        "success",
			"access_token":  "minted-access-token",
			"refresh_token": "minted-refresh-token",
			"expired_in":    3600,
		})
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rec, err := service.PollToken(ctx, srv.Client(), srv.URL, "client-1", "code-xyz", "verifier-abc", 300, 500)
	if err != nil {
		t.Fatalf("PollToken failed: %v", err)
	}
	if rec.AccessToken != "minted-access-token" {
		t.Fatalf("expected access token 'minted-access-token', got %q", rec.AccessToken)
	}
	if rec.RefreshToken != "minted-refresh-token" {
		t.Fatalf("expected refresh token 'minted-refresh-token', got %q", rec.RefreshToken)
	}
	if atomic.LoadInt32(&pollHits) != 2 {
		t.Fatalf("expected 2 poll hits, got %d", pollHits)
	}
}

func TestPollToken_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "error",
			"base_resp": map[string]any{
				"status_msg": "user rejected authorization",
			},
		})
	}))
	t.Cleanup(srv.Close)

	_, err := service.PollToken(context.Background(), srv.Client(), srv.URL, "client-1", "code-xyz", "verifier-abc", 300, 500)
	if err == nil {
		t.Fatalf("expected error on status=error, got nil")
	}
	if !strings.Contains(err.Error(), "user rejected authorization") {
		t.Fatalf("expected error msg, got: %v", err)
	}
}

func TestMiniMaxOAuthRefresher_QuarantineContract(t *testing.T) {
	tests := []struct {
		name             string
		handler          http.HandlerFunc
		expectQuarantine bool
	}{
		{
			name: "1. HTTP 400 + invalid_grant",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error":             "invalid_grant",
					"error_description": "Refresh token is invalid or expired",
				})
			},
			expectQuarantine: true,
		},
		{
			name: "2a. HTTP 200 JSON status=error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"status": "error",
					"base_resp": map[string]any{
						"status_code": 1001,
						"status_msg":  "token has expired",
					},
				})
			},
			expectQuarantine: true,
		},
		{
			name: "2b. HTTP 200 success-shaped with empty access_token",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"status":        "success",
					"access_token":  "",
					"refresh_token": "some-refresh",
					"expired_in":    3600,
				})
			},
			expectQuarantine: true,
		},
		{
			name: "3. HTTP 400 body refresh_token_reused",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error":             "refresh_token_reused",
					"error_description": "Refresh token was already used",
				})
			},
			expectQuarantine: true,
		},
		{
			name: "4. HTTP 410 Gone",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/plain")
				w.WriteHeader(http.StatusGone)
				_, _ = w.Write([]byte("Gone"))
			},
			expectQuarantine: true,
		},
		{
			name: "5. HTTP 429 Too Many Requests (transient, no quarantine)",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/plain")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte("rate limit exceeded"))
			},
			expectQuarantine: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var tokenHits int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&tokenHits, 1)
				tc.handler(w, r)
			}))
			t.Cleanup(srv.Close)

			tokenPath := writeTokenFile(t, oauthcred.TokenRecord{
				RefreshToken: "test-refresh-token",
			})

			store := oauthcred.NewFileStore(tokenPath)
			refresher := &service.MiniMaxOAuthRefresher{
				PortalBaseURL: srv.URL,
				ClientID:      "test-client-id",
				HTTPClient:    srv.Client(),
			}
			session := oauthcred.NewSession(store, refresher, oauthcred.WithSkew(service.TokenRefreshSkew))
			tp := service.NewOAuthTokenProvider(session)

			// Attempt 1: Refresh fails
			_, err1 := tp.Token(context.Background())
			if err1 == nil {
				t.Fatalf("expected error on attempt 1, got nil")
			}
			if atomic.LoadInt32(&tokenHits) != 1 {
				t.Fatalf("expected 1 token hit on attempt 1, got %d", tokenHits)
			}

			// Attempt 2
			_, err2 := tp.Token(context.Background())
			if tc.expectQuarantine {
				// Second call must fail closed with ErrQuarantined without touching server!
				if err2 == nil {
					t.Fatalf("expected quarantined error on attempt 2, got nil")
				}
				if atomic.LoadInt32(&tokenHits) != 1 {
					t.Fatalf("quarantined session hit server again on attempt 2 (tokenHits=%d)", tokenHits)
				}
			} else {
				// Non-quarantined (e.g. 429) must hit server again
				if atomic.LoadInt32(&tokenHits) != 2 {
					t.Fatalf("transient error should have hit server again on attempt 2 (tokenHits=%d)", tokenHits)
				}
				if err2 == nil {
					t.Fatalf("expected error on attempt 2 for transient failure, got nil")
				}
			}
		})
	}
}

func TestPollToken_IntervalEnforcesMinTwoSecondsAndGrantType(t *testing.T) {
	var pollTimes []time.Time
	var pollGrantTypes []string
	var mu sync.Mutex

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			http.NotFound(w, r)
			return
		}
		_ = r.ParseForm()
		mu.Lock()
		pollTimes = append(pollTimes, time.Now())
		pollGrantTypes = append(pollGrantTypes, r.FormValue("grant_type"))
		hits := len(pollTimes)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if hits < 3 {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "pending",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":        "success",
			"access_token":  "minted-access-token",
			"refresh_token": "minted-refresh-token",
			"expired_in":    3600,
		})
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rec, err := service.PollToken(ctx, srv.Client(), srv.URL, "client-1", "code-xyz", "verifier-abc", 300, 200)
	if err != nil {
		t.Fatalf("PollToken failed: %v", err)
	}
	if rec.AccessToken != "minted-access-token" {
		t.Fatalf("expected access token 'minted-access-token', got %q", rec.AccessToken)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(pollTimes) < 3 {
		t.Fatalf("expected at least 3 poll hits, got %d", len(pollTimes))
	}
	for i, gt := range pollGrantTypes {
		if gt != "urn:ietf:params:oauth:grant-type:user_code" {
			t.Fatalf("poll %d had grant_type %q, expected urn:ietf:params:oauth:grant-type:user_code", i, gt)
		}
	}
	// Verify interval between pending poll 1 and pending poll 2 is at least ~2s apart
	diff := pollTimes[1].Sub(pollTimes[0])
	if diff < 1900*time.Millisecond {
		t.Fatalf("expected at least ~2s between pending polls, got %v", diff)
	}
	// Verify interval between pending poll 2 and poll 3 is at least ~2s apart
	diff2 := pollTimes[2].Sub(pollTimes[1])
	if diff2 < 1900*time.Millisecond {
		t.Fatalf("expected at least ~2s between pending polls, got %v", diff2)
	}
}
