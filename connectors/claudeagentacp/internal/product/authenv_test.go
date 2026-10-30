package product

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/acp"
)

// Claims: requirements 2.1–2.4, 3.4/3.5. Mutants: inherit host env,
// substitute credentials, omit owned directories/Wait removal, enable tools.
func TestAuthEnv_SourceIsolationAndProcessCleanup(t *testing.T) {
	for _, source := range []string{"", "api_key", "subscription_token", "host_login"} {
		t.Run(source, func(t *testing.T) {
			home := t.TempDir()
			config := filepath.Join(home, ".claude")
			if err := os.Mkdir(config, 0o700); err != nil {
				t.Fatal(err)
			}
			credential := filepath.Join(config, ".credentials.json")
			if err := os.WriteFile(credential, []byte("untouched"), 0o600); err != nil {
				t.Fatal(err)
			}
			host := []string{"PATH=/bin", "HOME=" + home, "CLAUDE_CONFIG_DIR=" + config, "LANG=C", "AWS_PROFILE=bad", "ANTHROPIC_API_KEY=host-secret", "CLAUDE_CODE_OAUTH_TOKEN=host-token", "NODE_OPTIONS=bad"}
			auth, err := NewAuthEnv(AuthConfig{CredentialSource: source, PermissionPolicy: "deny"}, map[string]string{"api_key": "test-api", "claude_oauth_token": "opaque-token"}, host)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(fmt.Sprintf("%+v %#v", auth, auth), "test-api") || strings.Contains(fmt.Sprintf("%+v %#v", auth, auth), "opaque-token") {
				t.Fatal("secret in string form")
			}
			state := t.TempDir()
			capture := &authStarter{}
			starter := auth.Starter(state, capture)
			proc, err := starter.Start([]string{"adapter"}, home, nil)
			if err != nil {
				t.Fatal(err)
			}
			env := envMap(capture.env)
			want := map[string]string{"PATH": "/bin", "LANG": "C", "CLAUDE_CODE_DISABLE_BACKGROUND_TASKS": "1"}
			if source == "host_login" {
				want["HOME"], want["CLAUDE_CONFIG_DIR"] = home, config
			} else {
				info, err := os.Stat(filepath.Dir(env["HOME"]))
				if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
					t.Fatalf("process root must be a 0700 directory: %v", err)
				}
				for _, key := range []string{"HOME", "CLAUDE_CONFIG_DIR", "XDG_CONFIG_HOME", "ANTHROPIC_CONFIG_DIR"} {
					path := env[key]
					if !strings.HasPrefix(path, state+string(os.PathSeparator)) {
						t.Fatalf("unowned %s", key)
					}
					info, err := os.Stat(path)
					if err != nil || info.Mode().Perm() != 0o700 {
						t.Fatalf("directory %s: %v", key, err)
					}
					want[key] = path
				}
				if source == "" || source == "api_key" {
					want["ANTHROPIC_API_KEY"] = "test-api"
				} else {
					want["CLAUDE_CODE_OAUTH_TOKEN"] = "opaque-token"
				}
			}
			if !reflect.DeepEqual(env, want) {
				t.Fatalf("wrong environment keys: %v", reflect.ValueOf(env).MapKeys())
			}
			wantCmd := []string{"adapter"}
			if source == "" || source == "api_key" {
				wantCmd = append(wantCmd, "--hide-claude-auth")
			}
			if !reflect.DeepEqual(capture.cmd, wantCmd) {
				t.Fatalf("argv: %v", capture.cmd)
			}
			if b, err := io.ReadAll(proc.Stderr()); err != nil || len(b) != 0 {
				t.Fatalf("stderr exposed: %q %v", b, err)
			}
			second, err := starter.Start([]string{"adapter"}, home, nil)
			if err != nil {
				t.Fatal(err)
			}
			if source != "host_login" && envMap(capture.env)["HOME"] == env["HOME"] {
				t.Fatal("reused root")
			}
			if err := proc.Wait(); err != nil {
				t.Fatal(err)
			}
			if err := second.Wait(); err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(state)
			if err != nil || len(entries) != 0 {
				t.Fatalf("roots leaked: %v %v", entries, err)
			}
			b, err := os.ReadFile(credential)
			if err != nil || string(b) != "untouched" {
				t.Fatal("host credential changed")
			}
		})
	}
}

// The process owner, not a writable marker, owns shutdown and completion.
func TestAuthProcess_KillRequestsOwnerWithoutWaitingForMarker(t *testing.T) {
	for _, graceful := range []bool{true, false} {
		synctest.Test(t, func(t *testing.T) {
			child := &drainAuthProcess{testAuthProcess: &testAuthProcess{}, done: make(chan struct{}), graceful: graceful}
			child.shutdownFile = filepath.Join(t.TempDir(), "shutdown")
			if err := os.WriteFile(child.shutdownFile, []byte("ready"), 0o600); err != nil {
				t.Fatal(err)
			}
			p := &authProcess{Process: child, shutdownFile: child.shutdownFile}
			began := time.Now()
			if err := p.Kill(); err != nil {
				t.Fatal(err)
			}
			if !child.closed || child.kills != 1 || child.waits != 0 {
				t.Fatalf("closed=%v kills=%d", child.closed, child.kills)
			}
			if elapsed := time.Since(began); elapsed != 0 {
				t.Fatalf("shutdown elapsed=%v", elapsed)
			}
			if err := p.Wait(); err != nil {
				t.Fatal(err)
			}
			if child.waits != 1 {
				t.Fatalf("Wait called %d times", child.waits)
			}
		})
	}
}

type drainAuthProcess struct {
	*testAuthProcess
	done             chan struct{}
	graceful, closed bool
	kills            int
	shutdownFile     string
}

func (p *drainAuthProcess) Wait() error           { <-p.done; p.waits++; return nil }
func (p *drainAuthProcess) Kill() error           { p.kills++; close(p.done); return nil }
func (p *drainAuthProcess) Stdin() io.WriteCloser { return drainAuthInput{p} }

type drainAuthInput struct{ p *drainAuthProcess }

func (w drainAuthInput) Write(b []byte) (int, error) { return len(b), nil }
func (w drainAuthInput) Close() error {
	w.p.closed = true
	if w.p.graceful {
		return os.WriteFile(w.p.shutdownFile, []byte("stopped"), 0o600)
	}
	return nil
}

// Reuse the rows loop with the gateway source and its header secrets.
func TestAuthEnv_GatewaySourceIsolationAndRedaction(t *testing.T) {
	home := t.TempDir()
	host := []string{"PATH=/bin", "HOME=" + home, "LANG=C", "AWS_PROFILE=bad", "ANTHROPIC_API_KEY=host-secret", "CLAUDE_CODE_OAUTH_TOKEN=host-token", "NODE_OPTIONS=bad"}
	auth, err := NewAuthEnv(AuthConfig{
		CredentialSource: "gateway",
		Gateway:          GatewayConfig{BaseURL: "https://claude-gw.example.com", Headers: []GatewayHeader{{Name: "X-Route"}}},
	}, map[string]string{"X-Route": "gateway-header-secret"}, host)
	if err != nil {
		t.Fatal(err)
	}
	if leaked := fmt.Sprintf("%+v %#v", auth, auth); strings.Contains(leaked, "gateway-header-secret") || strings.Contains(leaked, "claude-gw.example.com") {
		t.Fatalf("secret/endpoint in string form: %t", leaked != "")
	}
	state := t.TempDir()
	capture := &authStarter{}
	proc, err := auth.Starter(state, capture).Start([]string{"adapter"}, home, nil)
	if err != nil {
		t.Fatal(err)
	}
	env := envMap(capture.env)
	want := map[string]string{
		"PATH": "/bin", "LANG": "C",
		"CLAUDE_GATEWAY_BASE_URL": "https://claude-gw.example.com",
	}
	for _, key := range []string{"HOME", "CLAUDE_CONFIG_DIR", "XDG_CONFIG_HOME", "ANTHROPIC_CONFIG_DIR"} {
		info, err := os.Stat(env[key])
		if err != nil || !info.IsDir() {
			t.Fatalf("owned %s: %v", key, err)
		}
		want[key] = env[key]
	}
	// The per-runtime Linux supervisor drains through a connector-owned
	// shutdown marker; its path is part of the child environment.
	if marker, ok := env["AIP_CLAUDE_ACP_SHUTDOWN_FILE"]; !ok {
		t.Fatal("shutdown marker env missing")
	} else if info, err := os.Stat(marker); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("owned AIP_CLAUDE_ACP_SHUTDOWN_FILE: %v", err)
	} else {
		want["AIP_CLAUDE_ACP_SHUTDOWN_FILE"] = marker
	}
	if !reflect.DeepEqual(env, want) {
		t.Fatalf("wrong environment: %v", env)
	}
	if wantCmd := []string{"adapter"}; !reflect.DeepEqual(capture.cmd, wantCmd) {
		t.Fatalf("argv: %v", capture.cmd)
	}
	for _, collected := range [][]string{capture.env, capture.cmd} {
		if strings.Contains(strings.Join(collected, "\x00"), "gateway-header-secret") {
			t.Fatal("header secret in argv/env")
		}
	}
	if b, err := io.ReadAll(proc.Stderr()); err != nil || len(b) != 0 {
		t.Fatalf("stderr exposed: %q %v", b, err)
	}
	if err := proc.Wait(); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(state)
	if len(entries) != 0 {
		t.Fatal("roots leaked")
	}
	profile := auth.HandshakeProfile()
	if profile.SkipAuthenticate {
		t.Fatal("gateway must authenticate")
	}
	caps := map[string]any{}
	if json.Unmarshal(profile.ClientCapabilities, &caps) != nil || !reflect.DeepEqual(map[string]any{"_meta": map[string]any{"gateway": true}}, caps["auth"]) {
		t.Fatalf("missing gateway capability: %s", profile.ClientCapabilities)
	}
	payload := map[string]any{}
	if json.Unmarshal(profile.AuthenticateParams, &payload) != nil {
		t.Fatal("authenticate payload")
	}
	if payload["methodId"] != "gateway" {
		t.Fatalf("methodId: %v", payload)
	}
	code, ok := payload["_meta"].(map[string]any)
	if !ok {
		t.Fatal("payload _meta")
	}
	meta, ok := code["gateway"].(map[string]any)
	if !ok {
		t.Fatal("payload gateway")
	}
	if meta["baseUrl"] != "https://claude-gw.example.com" || !reflect.DeepEqual(meta["headers"], map[string]any{"X-Route": "gateway-header-secret"}) {
		t.Fatalf("payload: %v", meta)
	}
	// The deny-session meta loses only the first-party provider pin.
	var session map[string]any
	if json.Unmarshal(profile.SessionNewMeta, &session) != nil {
		t.Fatal("session meta")
	}
	claudeCode, ok := session["claudeCode"].(map[string]any)
	if !ok {
		t.Fatal("session claudeCode")
	}
	options, ok := claudeCode["options"].(map[string]any)
	if !ok {
		t.Fatal("session options")
	}
	managed, ok := options["managedSettings"].(map[string]any)
	if !ok {
		t.Fatal("session managedSettings")
	}
	if _, pin := managed["allowedProviders"]; pin {
		t.Fatalf("gateway session pins first-party provider: %v", managed)
	}
	// The merged default policy is full bypass: the managed rules lock and the
	// bypass-mode disable are removed, and the session switches to the
	// non-interactive bypass mode with only AskUserQuestion withdrawn. The
	// hooks lock and the provider-pin removal stay from the gateway posture.
	if _, locked := managed["allowManagedPermissionRulesOnly"]; locked {
		t.Fatalf("bypass posture keeps the managed rules lock: %v", managed)
	}
	if permissions, ok := managed["permissions"].(map[string]any); !ok || len(permissions) != 0 {
		t.Fatalf("managed permissions posture: %v", managed)
	}
	if managed["disableAllHooks"] != true {
		t.Fatalf("hooks lock dropped: %v", managed)
	}
	if options["permissionMode"] != "bypassPermissions" || options["allowDangerouslySkipPermissions"] != true {
		t.Fatalf("bypass mode missing: %v", options)
	}
	if tools, _ := options["disallowedTools"].([]any); len(tools) != 1 || tools[0] != "AskUserQuestion" {
		t.Fatalf("disallowed tools: %v", options["disallowedTools"])
	}
	redacted := fmt.Sprintf("%+v %#v", auth, auth)
	if strings.Contains(redacted, "gateway-header-secret") {
		t.Fatal("secret in profile formatting")
	}
}

func TestAuthEnv_RejectsMissingSecretsAndUnsafeAdditions(t *testing.T) {
	for _, cfg := range []AuthConfig{{}, {CredentialSource: "subscription_token"}, {CredentialSource: "oauth"}, {CredentialSource: "host_login", PermissionPolicy: "exception"}} {
		if _, err := NewAuthEnv(cfg, nil, []string{"HOME=/host"}); err == nil {
			t.Fatalf("accepted %+v", cfg)
		}
	}
	for _, name := range []string{"ANTHROPIC_BASE_URL", "CLAUDE_CODE_EXECUTABLE", "AWS_PROFILE", "GOOGLE_APPLICATION_CREDENTIALS", "AZURE_TOKEN", "MY_SECRET", "HTTP_PROXY", "NODE_OPTIONS", "PATH=evil", "unknown"} {
		_, err := NewAuthEnv(AuthConfig{CredentialSource: "host_login", EnvAllowlist: []string{name}}, nil, []string{"HOME=/host"})
		if err == nil {
			t.Fatalf("accepted %s", name)
		}
	}
	auth, err := NewAuthEnv(AuthConfig{}, map[string]string{"api_key": "secret"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	_, err = auth.Starter(state, &authStarter{fail: true}).Start([]string{"adapter"}, "", nil)
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsafe error: %v", err)
	}
	entries, _ := os.ReadDir(state)
	if len(entries) != 0 {
		t.Fatal("failed spawn leaked root")
	}
	_, err = auth.Starter(state, &authStarter{}).Start([]string{"adapter"}, "", []string{"ANTHROPIC_API_KEY=override"})
	if err == nil {
		t.Fatal("accepted client environment")
	}
}

// Claim: a credential value containing NUL, CR, or LF is rejected at construction,
// so it can never become a malformed child environment entry. Mutant: drop the
// encoding check.
func TestAuthEnv_RejectsControlCharacterCredentials(t *testing.T) {
	for _, tc := range []struct{ source, key string }{{"api_key", "api_key"}, {"subscription_token", "claude_oauth_token"}} {
		for _, value := range []string{"secret\x00tail", "secret\rtail", "secret\ntail"} {
			_, err := NewAuthEnv(AuthConfig{CredentialSource: tc.source}, map[string]string{tc.key: value}, nil)
			if err == nil || !strings.Contains(err.Error(), "invalid credential encoding") {
				t.Fatalf("%s accepted control-character credential %q: %v", tc.source, value, err)
			}
		}
	}
	for _, value := range []string{"secret\x00tail", "secret\rtail", "secret\ntail"} {
		_, err := NewAuthEnv(AuthConfig{
			CredentialSource: "gateway",
			Gateway:          GatewayConfig{BaseURL: "https://gw.example.com", Headers: []GatewayHeader{{Name: "X-Route"}}},
		}, map[string]string{"X-Route": value}, nil)
		if err == nil || !strings.Contains(err.Error(), "invalid credential encoding") {
			t.Fatalf("gateway accepted control-character credential %q: %v", value, err)
		}
	}
}

func TestAuthEnv_RejectsGatewayMisconfiguration(t *testing.T) {
	for _, cfg := range []AuthConfig{
		{CredentialSource: "gateway"},
		{CredentialSource: "gateway", Gateway: GatewayConfig{BaseURL: "http://gw.example.com"}},
		{CredentialSource: "gateway", Gateway: GatewayConfig{BaseURL: "https://user:secret@gw.example.com"}},
	} {
		secrets := map[string]string{"X-Route": "private-route-secret", "X-A": "private-a-secret", "X-B": "private-b-secret"}
		_, err := NewAuthEnv(cfg, secrets, []string{"HOME=/host"})
		if err == nil || strings.Contains(err.Error(), "private-route-secret") || strings.Contains(err.Error(), "private-a-secret") || strings.Contains(err.Error(), "secret@gw") {
			t.Fatalf("accepted gateway misconfiguration %+v: %v", cfg, err)
		}
	}
}

// Requirement 2.2: an unusable owned-root parent must fail before spawning.
// Mutants: fall back to a temporary parent or continue after MkdirTemp fails.
func TestAuthEnv_InvalidStateRootNeverSpawns(t *testing.T) {
	for _, source := range []string{"api_key", "subscription_token"} {
		for _, kind := range []string{"empty", "missing", "file"} {
			t.Run(source+"/"+kind, func(t *testing.T) {
				parent := t.TempDir()
				state := ""
				if kind != "empty" {
					state = filepath.Join(parent, "state")
				}
				if kind == "file" {
					if err := os.WriteFile(state, []byte("unchanged"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				auth, err := NewAuthEnv(AuthConfig{CredentialSource: source}, map[string]string{"api_key": "test-api", "claude_oauth_token": "opaque-token"}, nil)
				if err != nil {
					t.Fatal(err)
				}
				capture := &authStarter{}
				proc, err := auth.Starter(state, capture).Start([]string{"adapter"}, "", nil)
				if proc != nil {
					_ = proc.Wait()
					t.Fatal("unusable state root started a process")
				}
				if err == nil || strings.Contains(err.Error(), state) && state != "" {
					t.Fatalf("missing or unsanitized root error: %v", err)
				}
				if capture.starts != 0 {
					t.Fatal("delegate Start called after root failure")
				}
				entries, err := os.ReadDir(parent)
				want := 0
				if kind == "file" {
					want = 1
					b, err := os.ReadFile(state)
					if err != nil || string(b) != "unchanged" {
						t.Fatal("state file changed")
					}
				}
				if err != nil || len(entries) != want {
					t.Fatalf("root failure leaked files: %v %v", entries, err)
				}
			})
		}
	}
}

// Requirements 2.2–2.4: repeated Wait shares one redacted result and cleanup,
// including failed exits, without deleting the operator's host configuration.
// Mutants: remove sync.Once, skip cleanup on error, or expose the child error.
func TestAuthEnv_RepeatedWaitRedactsFailureAndPreservesHostRoot(t *testing.T) {
	for _, source := range []string{"api_key", "subscription_token", "host_login"} {
		t.Run(source, func(t *testing.T) {
			home := t.TempDir()
			config := filepath.Join(home, ".claude")
			if err := os.Mkdir(config, 0o700); err != nil {
				t.Fatal(err)
			}
			credential := filepath.Join(config, ".credentials.json")
			if err := os.WriteFile(credential, []byte("untouched"), 0o600); err != nil {
				t.Fatal(err)
			}
			auth, err := NewAuthEnv(AuthConfig{CredentialSource: source}, map[string]string{"api_key": "test-api", "claude_oauth_token": "opaque-token"}, []string{"HOME=" + home, "CLAUDE_CONFIG_DIR=" + config})
			if err != nil {
				t.Fatal(err)
			}
			state := t.TempDir()
			child := &testAuthProcess{waitErr: errors.New("child leaked test-api opaque-token host-login-secret")}
			capture := &authStarter{proc: child}
			proc, err := auth.Starter(state, capture).Start([]string{"adapter"}, "", nil)
			if err != nil {
				t.Fatal(err)
			}
			first := proc.Wait()
			if first == nil || first.Error() != "claudeagentacp: adapter exited unsuccessfully" {
				t.Fatalf("exit error not redacted: %v", first)
			}
			entries, err := os.ReadDir(state)
			if err != nil || len(entries) != 0 {
				t.Fatalf("failed Wait leaked root: %v %v", entries, err)
			}
			// A second child Wait would now succeed: the wrapper must retain the
			// first failure rather than calling the already-reaped child again.
			child.waitErr = nil
			if second := proc.Wait(); second != first || child.waits != 1 {
				t.Fatalf("Wait did not share one result: calls=%d first=%v second=%v", child.waits, first, second)
			}
			b, err := os.ReadFile(credential)
			if err != nil || string(b) != "untouched" {
				t.Fatal("failed/repeated Wait changed host credentials")
			}
		})
	}
}

func TestAuthEnv_RestrictiveSessionMetadata(t *testing.T) {
	auth, err := NewAuthEnv(AuthConfig{PermissionPolicy: "deny"}, map[string]string{"api_key": "secret"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	hp := auth.HandshakeProfile()
	if !hp.SkipAuthenticate || string(hp.SessionNewMCPServers) != "[]" || string(hp.ClientCapabilities) != `{"fs":{"readTextFile":false,"writeTextFile":false},"terminal":false}` {
		t.Fatalf("unsafe handshake: %+v", hp)
	}
	const want = `{"claudeCode":{"options":{"agents":{},"allowDangerouslySkipPermissions":false,"disallowedTools":["Agent","Task","TaskOutput","TaskStop"],"managedSettings":{"allowManagedPermissionRulesOnly":true,"allowedProviders":["anthropic"],"disableAllHooks":true,"permissions":{"disableBypassPermissionsMode":"disable"}},"mcpServers":{},"permissionMode":"default","plugins":[],"settingSources":[],"settings":{"apiKeyHelper":"","env":{},"hooks":{},"permissions":{"allow":[],"deny":[],"ask":[]},"plugins":[]},"skills":[],"strictMcpConfig":true,"tools":[]}}}`
	if string(hp.SessionNewMeta) != want {
		t.Fatalf("metadata: %s", hp.SessionNewMeta)
	}
	hp.SessionNewMeta[0] = 'x'
	if string(auth.HandshakeProfile().SessionNewMeta) != want {
		t.Fatal("mutable shared metadata")
	}
}

func TestAuthEnv_BypassDefaultEnablesFullNonInteractiveRuntime(t *testing.T) {
	for _, policy := range []string{"", "bypass"} {
		auth, err := NewAuthEnv(AuthConfig{PermissionPolicy: policy}, map[string]string{"api_key": "test-only"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		var meta struct {
			ClaudeCode struct{ Options map[string]json.RawMessage }
		}
		if err := json.Unmarshal(auth.HandshakeProfile().SessionNewMeta, &meta); err != nil {
			t.Fatal(err)
		}
		opts := meta.ClaudeCode.Options
		if string(opts["permissionMode"]) != `"bypassPermissions"` || string(opts["allowDangerouslySkipPermissions"]) != "true" || string(opts["disallowedTools"]) != `["AskUserQuestion"]` || opts["tools"] != nil {
			t.Fatalf("bypass options: %s", auth.HandshakeProfile().SessionNewMeta)
		}
		var managed struct {
			AllowManagedPermissionRulesOnly json.RawMessage
			Permissions                     map[string]json.RawMessage
		}
		if err := json.Unmarshal(opts["managedSettings"], &managed); err != nil {
			t.Fatal(err)
		}
		if managed.AllowManagedPermissionRulesOnly != nil || managed.Permissions["disableBypassPermissionsMode"] != nil {
			t.Fatal("bypass locked by connector policy")
		}
		if string(opts["settingSources"]) != "[]" || string(opts["strictMcpConfig"]) != "true" || string(opts["mcpServers"]) != "{}" || string(opts["plugins"]) != "[]" || string(opts["agents"]) != "{}" {
			t.Fatal("session isolation changed")
		}
		prepared, err := auth.Prepare(t.TempDir(), []string{"adapter"})
		if err != nil {
			t.Fatal(err)
		}
		if envMap(prepared.Environment)["CLAUDE_CODE_DISABLE_BACKGROUND_TASKS"] != "" {
			t.Fatal("background tasks remain disabled")
		}
		if err := prepared.Close(); err != nil {
			t.Fatal(err)
		}
		if options := auth.TransportOptions(); options.RefuseOutOfTurnRequests || !options.AutoApproveOutOfTurnPermissions {
			t.Fatalf("bypass lifetime policy: %+v", options)
		}
	}
}

func TestAuthEnv_ExistingPoolOwnsReapReplacementAndClose(t *testing.T) {
	auth, err := NewAuthEnv(AuthConfig{CredentialSource: "subscription_token"}, map[string]string{"claude_oauth_token": "opaque"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"reap", "replacement", "close"} {
		t.Run(action, func(t *testing.T) {
			state := t.TempDir()
			capture := &authStarter{}
			proc, err := auth.Starter(state, capture).Start([]string{"adapter"}, "", nil)
			if err != nil {
				t.Fatal(err)
			}
			pool := acp.NewRuntimePool(acp.RuntimePoolConfig{IdleTimeout: time.Nanosecond})
			t.Cleanup(func() {
				if err := pool.Close(); err != nil {
					t.Error(err)
				}
			})
			key := acp.RuntimeKey{Workspace: "/work", Model: "sonnet", ClientSession: "client"}
			pool.SetProcess(key, proc, nil, "session", "", "adapter")
			switch action {
			case "reap":
				_, err = pool.Acquire(key)
				// The idle reap retires the process in a pool-owned goroutine:
				// the owned root is released when the cleanup is confirmed.
				waitForRootGone(t, state)
			case "replacement":
				err = pool.KillRuntime(key)
			case "close":
				err = pool.Close()
			}
			if err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(state)
			if err != nil || len(entries) != 0 {
				t.Fatalf("%s leaked root: %v %v", action, entries, err)
			}
		})
	}
}

func envMap(env []string) map[string]string {
	out := map[string]string{}
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		out[k] = v
	}
	return out
}

type authStarter struct {
	cmd, env []string
	fail     bool
	starts   int
	proc     *testAuthProcess
}

func (s *authStarter) Start(cmd []string, _ string, env []string) (acp.Process, error) {
	s.starts++
	s.cmd = cmd
	s.env = env
	if s.fail {
		return nil, errors.New("secret echoed by starter")
	}
	if s.proc != nil {
		return s.proc, nil
	}
	return &testAuthProcess{}, nil
}

type testAuthProcess struct {
	// mu guards the counters: the runtime pool retires a process in a
	// pool-owned goroutine, so the fake observes the retirement concurrently.
	mu      sync.Mutex
	waits   int
	waitErr error
}

// waitCount reports the number of observed Wait calls.
func (p *testAuthProcess) waitCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.waits
}

func (*testAuthProcess) PID() int              { return 0 }
func (*testAuthProcess) Stdin() io.WriteCloser { return nopWriter{} }
func (*testAuthProcess) Stdout() io.ReadCloser { return io.NopCloser(strings.NewReader("")) }
func (*testAuthProcess) Stderr() io.ReadCloser {
	return io.NopCloser(strings.NewReader("secret test-api opaque-token"))
}

func (p *testAuthProcess) Wait() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.waits++
	return p.waitErr
}
func (*testAuthProcess) Kill() error { return nil }

type nopWriter struct{}

func (nopWriter) Write(b []byte) (int, error) { return len(b), nil }
func (nopWriter) Close() error                { return nil }

// waitForRootGone waits until the owned process root is released. The idle
// reap retires the process in a pool-owned goroutine, so the root removal is
// asynchronous.
func waitForRootGone(t *testing.T, state string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		entries, err := os.ReadDir(state)
		if err == nil && len(entries) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("owned root survives: %v %v", entries, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
