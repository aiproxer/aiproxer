package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/oauthcred"
	"github.com/aiproxer/aiproxer/connectors/minimexoauth/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// newLoginTestServer emulates the MiniMax portal device flow: POST /oauth/code
// mints a user code, POST /oauth/token stays pending once then succeeds.
func newLoginTestServer(t *testing.T, pollHits *int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/oauth/code":
			_ = r.ParseForm()
			if r.PostForm.Get("code_challenge_method") != "S256" {
				http.Error(w, "expected S256 challenge", http.StatusBadRequest)
				return
			}
			if r.PostForm.Get("code_challenge") == "" || r.PostForm.Get("state") == "" {
				http.Error(w, "missing PKCE fields", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"user_code":        "LOGIN-CODE-1",
				"verification_uri": "https://portal.example/verify",
				"expired_in":       600,
				"state":            r.PostForm.Get("state"),
				"interval":         2000,
			})
		case "/oauth/token":
			if atomic.AddInt32(pollHits, 1) == 1 {
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "pending"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":        "success",
				"access_token":  "login-access-token",
				"refresh_token": "login-refresh-token",
				"expired_in":    3600,
			})
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestStartLogin_DisplaysVerificationURIAndUserCode(t *testing.T) {
	var pollHits int32
	srv := newLoginTestServer(t, &pollHits)
	t.Cleanup(srv.Close)

	sess, err := service.StartLogin(context.Background(), srv.Client(), srv.URL, "client-1", "")
	if err != nil {
		t.Fatalf("StartLogin: %v", err)
	}
	if sess.UserCode != "LOGIN-CODE-1" {
		t.Fatalf("expected user code LOGIN-CODE-1, got %q", sess.UserCode)
	}
	if sess.VerificationURI != "https://portal.example/verify" {
		t.Fatalf("expected verification URI, got %q", sess.VerificationURI)
	}
	instructions := sess.Instructions()
	if !strings.Contains(instructions, "LOGIN-CODE-1") || !strings.Contains(instructions, "https://portal.example/verify") {
		t.Fatalf("instructions must surface code + URI, got: %q", instructions)
	}
	if atomic.LoadInt32(&pollHits) != 0 {
		t.Fatalf("StartLogin must not poll the token endpoint, hits=%d", pollHits)
	}
}

func TestStartLogin_Validation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		portal string
		client string
	}{
		{"empty portal", "", "client-1"},
		{"empty client", "https://portal.example", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := service.StartLogin(context.Background(), nil, tc.portal, tc.client, ""); err == nil {
				t.Fatalf("expected validation error, got nil")
			}
		})
	}
}

func TestCompleteLogin_PollPersistRoundtrip(t *testing.T) {
	var pollHits int32
	srv := newLoginTestServer(t, &pollHits)
	t.Cleanup(srv.Close)

	sess, err := service.StartLogin(context.Background(), srv.Client(), srv.URL, "client-1", "group_id profile")
	if err != nil {
		t.Fatalf("StartLogin: %v", err)
	}

	storePath := filepath.Join(t.TempDir(), "nested", "tokens.json")
	store := oauthcred.NewFileStore(storePath)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rec, err := sess.Complete(ctx, store)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if rec.AccessToken != "login-access-token" || rec.RefreshToken != "login-refresh-token" {
		t.Fatalf("unexpected persisted record: %+v", rec)
	}
	if rec.Quarantined {
		t.Fatalf("fresh login record must not be quarantined")
	}
	if rec.Expiry.Before(time.Now().Add(30 * time.Minute)) {
		t.Fatalf("unexpected expiry: %v", rec.Expiry)
	}
	if atomic.LoadInt32(&pollHits) != 2 {
		t.Fatalf("expected pending+success polls, got %d", pollHits)
	}

	// The credential must be persisted via the existing store for later refresh.
	reloaded, err := store.Load()
	if err != nil {
		t.Fatalf("reload persisted credential: %v", err)
	}
	if reloaded.AccessToken != rec.AccessToken || reloaded.RefreshToken != rec.RefreshToken {
		t.Fatalf("persisted record mismatch: got %+v want %+v", reloaded, rec)
	}
}

func TestCompleteLogin_NilGuards(t *testing.T) {
	store := oauthcred.NewFileStore(filepath.Join(t.TempDir(), "tokens.json"))
	var sess *service.LoginSession
	if _, err := sess.Complete(context.Background(), store); err == nil {
		t.Fatalf("expected error for nil session, got nil")
	}

	var pollHits int32
	srv := newLoginTestServer(t, &pollHits)
	t.Cleanup(srv.Close)
	sess, err := service.StartLogin(context.Background(), srv.Client(), srv.URL, "client-1", "")
	if err != nil {
		t.Fatalf("StartLogin: %v", err)
	}
	if _, err := sess.Complete(context.Background(), nil); err == nil {
		t.Fatalf("expected error for nil store, got nil")
	}
}

func TestCompleteToSession_ClearsQuarantineLatch(t *testing.T) {
	store := oauthcred.NewFileStore(filepath.Join(t.TempDir(), "tokens.json"))
	if err := store.Save(oauthcred.TokenRecord{
		AccessToken:  "expired-tok",
		RefreshToken: "ref-tok",
		Expiry:       time.Now().Add(-10 * time.Minute),
	}); err != nil {
		t.Fatalf("seed credential: %v", err)
	}

	var refreshCalls atomic.Int32
	refresher := oauthcred.RefresherFunc(func(_ context.Context, _ string) (string, string, time.Time, error) {
		refreshCalls.Add(1)
		return "", "", time.Time{}, errors.New("oauth: server returned invalid_grant: token revoked")
	})
	sess := oauthcred.NewSession(store, refresher)

	if _, err := sess.Token(context.Background()); !errors.Is(err, oauthcred.ErrTerminalRefresh) {
		t.Fatalf("expected ErrTerminalRefresh, got %v", err)
	}
	if _, err := sess.Token(context.Background()); !errors.Is(err, oauthcred.ErrQuarantined) {
		t.Fatalf("expected latched ErrQuarantined, got %v", err)
	}
	if got := refreshCalls.Load(); got != 1 {
		t.Fatalf("expected 1 refresher call before login, got %d", got)
	}

	var pollHits int32
	srv := newLoginTestServer(t, &pollHits)
	t.Cleanup(srv.Close)
	loginSess, err := service.StartLogin(context.Background(), srv.Client(), srv.URL, "client-1", "")
	if err != nil {
		t.Fatalf("StartLogin: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rec, err := loginSess.CompleteToSession(ctx, sess)
	if err != nil {
		t.Fatalf("CompleteToSession: %v", err)
	}
	if rec.AccessToken != "login-access-token" {
		t.Fatalf("unexpected access token %q", rec.AccessToken)
	}
	if rec.Quarantined {
		t.Fatalf("fresh login record must not be quarantined")
	}
	if got := refreshCalls.Load(); got != 1 {
		t.Fatalf("CompleteToSession must not replay refresher, calls=%d", got)
	}

	tok, err := sess.Token(context.Background())
	if err != nil {
		t.Fatalf("Token after CompleteToSession: %v", err)
	}
	if tok != "login-access-token" {
		t.Fatalf("expected fresh login token, got %q", tok)
	}
	if got := refreshCalls.Load(); got != 1 {
		t.Fatalf("Token after login must serve cached credential without refresher replay, calls=%d", got)
	}
}

func TestCompleteToSession_NilGuards(t *testing.T) {
	store := oauthcred.NewFileStore(filepath.Join(t.TempDir(), "tokens.json"))
	sess := oauthcred.NewSession(store, oauthcred.RefresherFunc(
		func(_ context.Context, _ string) (string, string, time.Time, error) {
			return "", "", time.Time{}, errors.New("must not be called")
		}))

	var nilSess *service.LoginSession
	if _, err := nilSess.CompleteToSession(context.Background(), sess); err == nil {
		t.Fatalf("expected error for nil login session, got nil")
	}

	var pollHits int32
	srv := newLoginTestServer(t, &pollHits)
	t.Cleanup(srv.Close)
	loginSess, err := service.StartLogin(context.Background(), srv.Client(), srv.URL, "client-1", "")
	if err != nil {
		t.Fatalf("StartLogin: %v", err)
	}
	if _, err := loginSess.CompleteToSession(context.Background(), nil); err == nil {
		t.Fatalf("expected error for nil credential session, got nil")
	}
}

func TestConfigure_WithoutCredential_Actionable(t *testing.T) {
	missing := filepath.ToSlash(filepath.Join(t.TempDir(), "tokens.json"))
	svc := service.NewProduction()
	_, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		ConfigYAML:  []byte("oauth_token_file: " + missing + "\noauth_client_id: client-1\n"),
		Secrets:     backendplugin.SecretBundle{},
	})
	if err == nil {
		t.Fatalf("expected Configure to fail without a credential, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, missing) {
		t.Fatalf("error must name the credential path, got: %v", err)
	}
	if !strings.Contains(msg, "login flow first") {
		t.Fatalf("error must direct the operator to run login first, got: %v", err)
	}
	if strings.Contains(msg, "no such file") {
		t.Fatalf("error must be actionable, not a raw file-not-found, got: %v", err)
	}
}

func TestConfigure_EmptyCredential_Actionable(t *testing.T) {
	tokenPath := writeTokenFile(t, oauthcred.TokenRecord{})
	svc := service.NewProduction()
	_, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		ConfigYAML:  []byte("oauth_token_file: " + tokenPath + "\noauth_client_id: client-1\n"),
		Secrets:     backendplugin.SecretBundle{},
	})
	if err == nil {
		t.Fatalf("expected Configure to fail on token-less credential, got nil")
	}
	if !strings.Contains(err.Error(), "holds no tokens") {
		t.Fatalf("error must report empty credential, got: %v", err)
	}
}
