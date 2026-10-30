package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/oauthcred"
)

func TestRunLogin_MissingClientID_Fails(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	err := RunLogin(context.Background(), &stdout, &stderr, http.DefaultClient, []string{
		"--token-file", "test.json",
	})
	if err == nil || !strings.Contains(err.Error(), "oauth_client_id is required") {
		t.Fatalf("expected missing client-id error, got: %v", err)
	}
}

func TestRunLogin_MissingTokenFile_Fails(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	err := RunLogin(context.Background(), &stdout, &stderr, http.DefaultClient, []string{
		"--client-id", "client-123",
	})
	if err == nil || !strings.Contains(err.Error(), "oauth_token_file is required") {
		t.Fatalf("expected missing token-file error, got: %v", err)
	}
}

func TestRunLogin_SuccessFlow(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/oauth/code":
			_ = r.ParseForm()
			state := r.Form.Get("state")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status_code":      0,
				"status_msg":       "ok",
				"user_code":        "ABCD-1234",
				"verification_uri": "https://auth.example.com/verify",
				"expired_in":       600,
				"interval":         1,
				"state":            state,
			})
		case "/oauth/token":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":        "success",
				"access_token":  "mock-access-token",
				"refresh_token": "mock-refresh-token",
				"expired_in":    7200,
				"token_type":    "Bearer",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	tmpDir := t.TempDir()
	tokenFile := filepath.Join(tmpDir, "token.json")

	var stdout, stderr bytes.Buffer
	err := RunLogin(context.Background(), &stdout, &stderr, srv.Client(), []string{
		"--client-id", "my-client-id",
		"--token-file", tokenFile,
		"--portal-url", srv.URL,
	})
	if err != nil {
		t.Fatalf("RunLogin failed: %v", err)
	}

	outStr := stdout.String()
	if !strings.Contains(outStr, "https://auth.example.com/verify") || !strings.Contains(outStr, "ABCD-1234") {
		t.Fatalf("stdout missing verification URI or user code, got:\n%s", outStr)
	}
	if !strings.Contains(outStr, "Successfully authenticated") {
		t.Fatalf("stdout missing success message, got:\n%s", outStr)
	}

	// Verify token was saved
	store := oauthcred.NewFileStore(tokenFile)
	rec, err := store.Load()
	if err != nil {
		t.Fatalf("failed to load saved token: %v", err)
	}
	if rec.AccessToken != "mock-access-token" || rec.RefreshToken != "mock-refresh-token" {
		t.Fatalf("saved token mismatch: %+v", rec)
	}
}

func TestRunLogin_ConfigYAML_SuccessFlow(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/oauth/code":
			_ = r.ParseForm()
			state := r.Form.Get("state")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status_code":      0,
				"status_msg":       "ok",
				"user_code":        "WXYZ-9999",
				"verification_uri": "https://auth.example.com/device",
				"expired_in":       300,
				"interval":         1,
				"state":            state,
			})
		case "/oauth/token":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":        "success",
				"access_token":  "cfg-access-token",
				"refresh_token": "cfg-refresh-token",
				"expired_in":    3600,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	tmpDir := t.TempDir()
	tokenFile := filepath.Join(tmpDir, "cfg_token.json")
	cfgFile := filepath.Join(tmpDir, "config.yaml")

	cfgContent := "oauth_client_id: yaml-client-id\noauth_token_file: " + tokenFile + "\nportal_base_url: " + srv.URL + "\n"
	if err := os.WriteFile(cfgFile, []byte(cfgContent), 0o600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	var stdout, stderr bytes.Buffer
	err := RunLogin(context.Background(), &stdout, &stderr, srv.Client(), []string{
		"--config", cfgFile,
	})
	if err != nil {
		t.Fatalf("RunLogin with --config failed: %v", err)
	}

	store := oauthcred.NewFileStore(tokenFile)
	rec, err := store.Load()
	if err != nil {
		t.Fatalf("failed to load token: %v", err)
	}
	if rec.AccessToken != "cfg-access-token" {
		t.Fatalf("token mismatch: %+v", rec)
	}
}

func TestRunLogin_ContextCancelled(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/oauth/code" {
			_ = r.ParseForm()
			state := r.Form.Get("state")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status_code":      0,
				"status_msg":       "ok",
				"user_code":        "WAIT-0000",
				"verification_uri": "https://auth.example.com/wait",
				"expired_in":       300,
				"interval":         1,
				"state":            state,
			})
			return
		}
		// Token endpoint simulates authorization pending
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status_code": 1001,
			"status_msg":  "authorization_pending",
		})
	}))
	t.Cleanup(srv.Close)

	tmpDir := t.TempDir()
	tokenFile := filepath.Join(tmpDir, "cancel_token.json")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	var stdout, stderr bytes.Buffer
	err := RunLogin(ctx, &stdout, &stderr, srv.Client(), []string{
		"--client-id", "test-client",
		"--token-file", tokenFile,
		"--portal-url", srv.URL,
	})
	if err == nil {
		t.Fatalf("expected timeout/cancellation error, got nil")
	}
}
