package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/internal/core/accessmode"
	"github.com/aiproxer/aiproxer/internal/core/config"
	mgmtreload "github.com/aiproxer/aiproxer/internal/stdhttp/admin/configreload"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
	"github.com/stretchr/testify/assert"
)

type stubReloadCoord struct {
	status      sdkreload.Status
	fixedSource string
}

func (s *stubReloadCoord) Reload(context.Context, sdkreload.Trigger) sdkreload.Result {
	return sdkreload.Result{Category: sdkreload.ResultNoop, ActiveGeneration: 1}
}

func (s *stubReloadCoord) Status() sdkreload.Status {
	if s.status.ActiveGeneration == 0 {
		return sdkreload.Status{ActiveGeneration: 1}
	}
	return s.status
}

func (s *stubReloadCoord) FixedSourcePath() string {
	if s.fixedSource != "" {
		return s.fixedSource
	}
	return "/fixed/config.yaml"
}

func TestResolveManagementOptions_multiUserAbsentTokenDisabled(t *testing.T) {
	t.Setenv(reloadManagementAddressEnv, mgmtreload.DefaultListenAddress)
	t.Setenv(reloadManagementTokenEnv, "")
	_ = os.Unsetenv(reloadManagementTokenEnv)

	opts, enable, err := resolveManagementOptions(&config.Config{
		Server: config.ServerConfig{Address: "127.0.0.1:18080"},
		Access: config.AccessConfig{Mode: string(accessmode.ModeMultiUser)},
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if enable {
		t.Fatal("multi_user without dedicated token must disable management")
	}
	if opts.AuthMode != "" || opts.BearerToken != "" {
		t.Fatalf("disabled options must not carry auth material: %+v", opts)
	}
}

func TestResolveManagementOptions_multiUserValidTokenBearer(t *testing.T) {
	t.Setenv(reloadManagementAddressEnv, mgmtreload.DefaultListenAddress)
	const secret = "test-management-secret"
	t.Setenv(reloadManagementTokenEnv, secret)

	opts, enable, err := resolveManagementOptions(&config.Config{
		Server: config.ServerConfig{Address: "127.0.0.1:18080"},
		Access: config.AccessConfig{Mode: string(accessmode.ModeMultiUser)},
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !enable {
		t.Fatal("expected management enabled")
	}
	if opts.AuthMode != mgmtreload.AuthModeBearer {
		t.Fatalf("AuthMode=%q want bearer", opts.AuthMode)
	}
	if opts.BearerToken != secret {
		t.Fatal("bearer token mismatch")
	}
	if opts.AccessMode != accessmode.ModeMultiUser {
		t.Fatalf("AccessMode=%q", opts.AccessMode)
	}
	if err := opts.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestResolveManagementOptions_multiUserWeakTokenRejected(t *testing.T) {
	t.Setenv(reloadManagementAddressEnv, mgmtreload.DefaultListenAddress)
	t.Setenv(reloadManagementTokenEnv, "short")

	_, enable, err := resolveManagementOptions(&config.Config{
		Server: config.ServerConfig{Address: "127.0.0.1:18080"},
		Access: config.AccessConfig{Mode: string(accessmode.ModeMultiUser)},
	})
	if err == nil {
		t.Fatal("expected weak token rejection")
	}
	if enable {
		t.Fatal("weak token must not enable management")
	}
	if !strings.Contains(err.Error(), "16") {
		t.Fatalf("want strong-token length error, got %v", err)
	}
}

func TestResolveManagementOptions_singleUserExplicitLoopbackLocalTrust(t *testing.T) {
	t.Setenv(reloadManagementAddressEnv, mgmtreload.DefaultListenAddress)
	_ = os.Unsetenv(reloadManagementTokenEnv)

	opts, enable, err := resolveManagementOptions(&config.Config{
		Server: config.ServerConfig{Address: "127.0.0.1:18080"},
		Access: config.AccessConfig{Mode: string(accessmode.ModeSingleUser)},
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !enable {
		t.Fatal("single_user loopback must enable management")
	}
	if opts.AuthMode != mgmtreload.AuthModeLocalTrust {
		t.Fatalf("AuthMode=%q want local_trust default", opts.AuthMode)
	}
	if opts.BearerToken != "" {
		t.Fatal("single_user must not reuse bearer material by default")
	}
	if err := opts.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestResolveManagementOptions_unsetAddressPreservesStartupCompatibility(t *testing.T) {
	t.Setenv(reloadManagementAddressEnv, "")
	_ = os.Unsetenv(reloadManagementAddressEnv)
	_ = os.Unsetenv(reloadManagementTokenEnv)

	opts, enable, err := resolveManagementOptions(&config.Config{
		Server: config.ServerConfig{Address: "127.0.0.1:18080"},
		Access: config.AccessConfig{Mode: string(accessmode.ModeSingleUser)},
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if enable || opts.Address != "" {
		t.Fatalf("unset address must leave management disabled: enable=%v opts=%+v", enable, opts)
	}
}

func TestResolveManagementOptions_singleUserNonLoopbackRequiresBearer(t *testing.T) {
	t.Setenv(reloadManagementAddressEnv, "0.0.0.0:19090")
	_ = os.Unsetenv(reloadManagementTokenEnv)
	cfg := &config.Config{
		Server: config.ServerConfig{Address: "127.0.0.1:18080"},
		Access: config.AccessConfig{Mode: string(accessmode.ModeSingleUser)},
	}
	if _, enable, err := resolveManagementOptions(cfg); err != nil || enable {
		t.Fatalf("non-loopback without bearer must remain disabled: enable=%v err=%v", enable, err)
	}

	const secret = "test-management-secret"
	t.Setenv(reloadManagementTokenEnv, secret)
	opts, enable, err := resolveManagementOptions(cfg)
	if err != nil || !enable {
		t.Fatalf("explicit non-loopback with bearer must enable: enable=%v err=%v", enable, err)
	}
	if opts.AuthMode != mgmtreload.AuthModeBearer || !opts.AllowNonLoopback {
		t.Fatalf("unexpected non-loopback posture: %+v", opts)
	}
}

func TestStartManagementServer_multiUserAbsentTokenNoEndpoint(t *testing.T) {
	t.Setenv(reloadManagementAddressEnv, mgmtreload.DefaultListenAddress)
	_ = os.Unsetenv(reloadManagementTokenEnv)

	var logBuf bytes.Buffer
	res := newMultiUserFixture(&logBuf)
	coord := &stubReloadCoord{}

	srv, err := startManagementServer(context.Background(), res.Config, res.Logger, coord)
	if err != nil {
		t.Fatalf("serve composition must remain possible: %v", err)
	}
	if srv != nil {
		t.Fatal("expected nil management server when token absent")
	}
	if !strings.Contains(logBuf.String(), reloadManagementTokenEnv) {
		t.Fatalf("expected safe disabled warning mentioning %s, got %q", reloadManagementTokenEnv, logBuf.String())
	}
}

func TestStartManagementServer_multiUserValidTokenStartsBearer(t *testing.T) {
	const secret = "test-management-secret"
	t.Setenv(reloadManagementTokenEnv, secret)

	origAddr := managementListenAddress
	managementListenAddress = "127.0.0.1:0"
	t.Cleanup(func() { managementListenAddress = origAddr })

	var logBuf bytes.Buffer
	res := newMultiUserFixture(&logBuf)
	coord := &stubReloadCoord{}

	srv, err := startManagementServer(context.Background(), res.Config, res.Logger, coord)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if srv == nil {
		t.Fatal("expected started management server")
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	addr := srv.Addr()
	req, err := http.NewRequest(http.MethodGet, "http://"+addr+mgmtreload.StatusPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d want 401", resp.StatusCode)
	}

	req2, err := http.NewRequest(http.MethodGet, "http://"+addr+mgmtreload.StatusPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	req2.Header.Set("Authorization", "Bearer "+secret)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { assert.NoError(t, resp2.Body.Close()) }()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("authenticated status=%d want 200", resp2.StatusCode)
	}
}

func TestStartManagementServer_multiUserWeakTokenRejected(t *testing.T) {
	t.Setenv(reloadManagementAddressEnv, mgmtreload.DefaultListenAddress)
	t.Setenv(reloadManagementTokenEnv, "short")
	res := newMultiUserFixture(io.Discard)
	_, err := startManagementServer(context.Background(), res.Config, res.Logger, &stubReloadCoord{})
	if err == nil {
		t.Fatal("expected weak token rejection")
	}
	if !strings.Contains(err.Error(), "16") {
		t.Fatalf("want strong-token length error, got %v", err)
	}
}

func TestStartManagementServer_noSecretLogged(t *testing.T) {
	const secret = "super-secret-mgmt-token"
	if utf8.RuneCountInString(secret) < mgmtreload.MinBearerSecretRunes {
		t.Fatal("test secret too short")
	}
	t.Setenv(reloadManagementTokenEnv, secret)

	origAddr := managementListenAddress
	managementListenAddress = "127.0.0.1:0"
	t.Cleanup(func() { managementListenAddress = origAddr })

	var logBuf bytes.Buffer
	res := newMultiUserFixture(&logBuf)
	srv, err := startManagementServer(context.Background(), res.Config, res.Logger, &stubReloadCoord{})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if srv != nil {
		t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	}
	if strings.Contains(logBuf.String(), secret) {
		t.Fatalf("management secret must not appear in logs: %q", logBuf.String())
	}
}

// multiUserFixture bundles only the config/logger inputs startManagementServer
// consumes, not a fake bootstrap/host aggregate.
type multiUserFixture struct {
	Config *config.Config
	Logger *slog.Logger
}

func newMultiUserFixture(logOut io.Writer) multiUserFixture {
	if logOut == nil {
		logOut = io.Discard
	}
	logger := slog.New(slog.NewTextHandler(logOut, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return multiUserFixture{
		Config: &config.Config{
			Server: config.ServerConfig{Address: "127.0.0.1:18080"},
			Access: config.AccessConfig{Mode: string(accessmode.ModeMultiUser)},
		},
		Logger: logger,
	}
}
