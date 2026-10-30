package product

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/acp"
)

// Claims (requirements 2.5–2.6): policy consent precedes exec; content changes
// invalidate reuse; identity/source mismatches and timeouts fail without secrets;
// startup disclosure contains only bounded identity and policy classifications.
// Mutants: ignore policy/read errors, reuse stale digest, accept any loggedIn
// status, inherit probe env, return raw causes, or log decoded account fields.
func TestPolicyGate_ConsentAndChangedFingerprint(t *testing.T) {
	files := fstest.MapFS{}
	g := PolicyGate{Files: files}
	first, err := g.Check(t.Context(), "/owned/claude", "")
	if err != nil || len(first.Sources) != 0 {
		t.Fatalf("absent: %+v %v", first, err)
	}
	for _, path := range []string{"etc/claude-code/managed-settings.json", "etc/claude-code/managed-settings.d/a.json", "etc/claude-code/managed-mcp.json", "owned/claude/remote-settings.json"} {
		t.Run(path, func(t *testing.T) {
			files[path] = &fstest.MapFile{Data: []byte(`{"env":{}}`)}
			_, err := g.Check(t.Context(), "/owned/claude", "refuse")
			label := "/" + path
			if strings.Contains(path, "managed-settings.d/") {
				label = "/etc/claude-code/managed-settings.d"
			}
			if strings.Contains(path, "remote-settings") {
				label = "effective configuration remote-settings.json"
			}
			if err == nil || !strings.Contains(err.Error(), "admin_policy: honor") || !strings.Contains(err.Error(), label) {
				t.Fatalf("refuse: %v", err)
			}
			present, err := g.Check(t.Context(), "/owned/claude", "honor")
			if err != nil || len(present.Sources) != 1 || present.Fingerprint == first.Fingerprint {
				t.Fatalf("honor: %+v %v", present, err)
			}
			files[path].Data = []byte(`{"env":[]}`) // same byte length and mtime must still change
			changed, err := g.Check(t.Context(), "/owned/claude", "honor")
			if err != nil || changed.Fingerprint == present.Fingerprint {
				t.Fatalf("change: %+v %v", changed, err)
			}
			delete(files, path)
		})
	}
	for _, files := range []fs.FS{unreadableFS{}, unreadableFS{FS: fstest.MapFS{"etc/claude-code/managed-settings.json": &fstest.MapFile{Data: []byte(`{}`)}}}} {
		for _, mode := range []string{"", "honor"} {
			_, err := (PolicyGate{Files: files}).Check(t.Context(), "/owned", mode)
			if err == nil || strings.Contains(fmt.Sprintf("%+v", err), "private-email") || errors.Unwrap(err) != nil {
				t.Fatalf("unreadable must be sanitized: %v", err)
			}
		}
	}
	if _, err := g.Check(t.Context(), "/owned", "bypass"); err == nil {
		t.Fatal("invalid disposition accepted")
	}
}

type unreadableFS struct{ fs.FS }

func (f unreadableFS) Stat(name string) (fs.FileInfo, error) {
	if f.FS != nil {
		return fs.Stat(f.FS, name)
	}
	return nil, errors.New("private-email secret-token")
}

func (unreadableFS) Open(string) (fs.File, error) {
	return nil, errors.New("private-email secret-token")
}

func TestIdentityGate_RecordedStatusRequiresExactSource(t *testing.T) {
	api := `{"loggedIn":true,"authMethod":"api_key","apiProvider":"firstParty","apiKeySource":"ANTHROPIC_API_KEY"}`
	token := `{"loggedIn":true,"authMethod":"oauth_token","apiProvider":"firstParty"}`
	login := `{"loggedIn":true,"authMethod":"claude.ai","apiProvider":"firstParty","subscriptionType":"max","email":"private-email","orgName":"private-org"}`
	for _, tc := range []struct{ source, status, policy, want string }{
		{"api_key", api, "honor", "api_key"},
		{"subscription_token", token, "honor", "subscription_token"},
		{"host_login", login, "honor", "host_login"},
		{"api_key", token, "honor", ""},
		{"subscription_token", api, "honor", ""},
		{"host_login", token, "honor", ""},
		{"subscription_token", login, "honor", ""},
		{"host_login", api, "honor", ""},
		{"api_key", strings.Replace(api, "firstParty", "bedrock", 1), "honor", ""},
		{"api_key", strings.Replace(api, "ANTHROPIC_API_KEY", "apiKeyHelper", 1), "honor", ""},
		{"host_login", strings.Replace(login, "max", "team", 1), "", ""},
		{"host_login", strings.Replace(login, "max", "enterprise", 1), "honor", "host_login"},
		{"host_login", strings.Replace(login, "max", "unknown", 1), "honor", ""},
		{"api_key", `{}`, "honor", ""},
		{"api_key", `null`, "honor", ""},
		{"api_key", `private-email secret-token`, "honor", ""},
		{"api_key", strings.Replace(api, "true", "false", 1), "honor", ""},
		{"gateway", `{"loggedIn":false,"authMethod":"none","apiProvider":"firstParty"}`, "honor", "gateway"},
		{"gateway", `{"loggedIn":false,"authMethod":"none","apiProvider":"firstParty","apiKeySource":"","subscriptionType":""}`, "honor", "gateway"},
		{"gateway", `{"loggedIn":true,"authMethod":"api_key","apiProvider":"firstParty","apiKeySource":"ANTHROPIC_API_KEY"}`, "honor", ""},
		{"gateway", `{"loggedIn":false,"authMethod":"oauth_token","apiProvider":"firstParty"}`, "honor", ""},
		{"gateway", `{"loggedIn":false,"authMethod":"api_key","apiProvider":"firstParty"}`, "honor", ""},
		{"gateway", `{"loggedIn":false,"authMethod":"none","apiProvider":"gateway"}`, "honor", ""},
		{"gateway", strings.Replace(api, "firstParty", "bedrock", 1), "honor", ""},
		{"gateway", `{"loggedIn":false,"authMethod":"none","subscriptionType":"max","apiProvider":"firstParty"}`, "honor", ""},
	} {
		t.Run(tc.source+tc.status+tc.policy, func(t *testing.T) {
			env := []string{"HOME=/owned/home", "CLAUDE_CONFIG_DIR=/owned/claude", "ANTHROPIC_API_KEY=synthetic-secret"}
			g := IdentityGate{Probe: func(ctx context.Context, executable, cwd string, got []string) ([]byte, error) {
				if _, ok := ctx.Deadline(); !ok || executable != "/pinned/adapter" || cwd != "/workspace" || !reflect.DeepEqual(got, env) {
					t.Fatal("probe lost exact launch identity")
				}
				return []byte(tc.status), nil
			}}
			identity, err := g.Check(t.Context(), "/pinned/adapter", "/workspace", env, tc.source, tc.policy)
			if (err == nil) != (tc.want != "") || (err == nil && identity.CredentialSource != tc.want) {
				t.Fatalf("identity=%+v err=%v", identity, err)
			}
			if tc.policy == "honor" && strings.Contains(tc.status, "enterprise") && identity.PolicyDisposition != "honored_organisation" {
				t.Fatal("organisation policy was not disclosed")
			}
			if strings.Contains(fmt.Sprintf("%+v %+v", identity, err), "private-") || strings.Contains(fmt.Sprintf("%+v %+v", identity, err), "synthetic-secret") || errors.Unwrap(err) != nil {
				t.Fatal("identity leaked private data/cause")
			}
		})
	}
}

func TestIdentityGate_TimeoutAndRawProbeCausesStayPrivate(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		g := IdentityGate{Timeout: time.Millisecond, Probe: func(ctx context.Context, _, _ string, _ []string) ([]byte, error) {
			if timeout {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return nil, errors.New("private-email secret-token")
		}}
		_, err := g.Check(t.Context(), "/pinned", "/workspace", []string{"HOME=/owned"}, "subscription_token", "honor")
		if err == nil || !strings.Contains(err.Error(), "claude setup-token") || strings.Contains(err.Error(), "private") || errors.Unwrap(err) != nil {
			t.Fatalf("unsafe failure: %v", err)
		}
	}
}

// Claim: only the gateway source treats the pinned CLI's exit-1 status as
// tolerable evidence: its exact logged-out JSON is admitted, while the same
// probe failure still refuses for every first-party source.
// Source: identity.go Check (logged-out CLI exits 1 while printing JSON); #1244.
// Mutant: drop the source != "gateway" caveat on the probe error.
func TestIdentityGate_GatewayAdmitsLoggedOutExitOneProbe(t *testing.T) {
	sub := exec.Command("false") // exit 1, normal exit, real ProcessState
	exitErr := sub.Run()
	if exitErr == nil || sub.ProcessState.ExitCode() != 1 || !sub.ProcessState.Exited() {
		t.Fatal("fixture subprocess did not exit 1")
	}
	loggedOut := []byte(`{"loggedIn":false,"authMethod":"none","apiProvider":"firstParty","apiKeySource":"","subscriptionType":""}`)
	loggedIn := []byte(`{"loggedIn":true,"authMethod":"api_key","apiProvider":"firstParty","apiKeySource":"ANTHROPIC_API_KEY"}`)
	for _, tc := range []struct {
		name, source string
		data         []byte
		want         string
		wantText     string
	}{
		{"gateway exit one logged out", "gateway", loggedOut, "gateway", ""},
		{"gateway unparseable", "gateway", []byte("private-email secret-token"), "", "verify the gateway endpoint and provisioned header secrets"},
		{"api_key exit one logged in json", "api_key", loggedIn, "", "provision the api_key secret"},
		{"host_login exit one logged out json", "host_login", loggedOut, "", "run claude auth login"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := IdentityGate{Probe: func(context.Context, string, string, []string) ([]byte, error) {
				return tc.data, exitErr
			}}
			identity, err := g.Check(t.Context(), "/pinned/adapter", "/workspace", []string{"HOME=/owned"}, tc.source, "honor")
			if (err == nil) != (tc.want != "") || (err == nil && identity.CredentialSource != tc.want) {
				t.Fatalf("identity=%+v err=%v", identity, err)
			}
			if tc.want != "" && identity.RuntimeIdentity != "gateway/gateway" {
				t.Fatalf("runtime identity = %q", identity.RuntimeIdentity)
			}
			if tc.want == "" && (!strings.Contains(err.Error(), tc.wantText) || strings.Contains(err.Error(), "private-email") || errors.Unwrap(err) != nil) {
				t.Fatalf("unsafe refusal: %v", err)
			}
		})
	}
}

// Requirement 5.2: the gateway startup disclosure names the gateway posture
// and the validated endpoint host, never header values, and the engine keeps
// that resolved identity across per-turn re-spawns.
func TestStartupGate_GatewayDisclosureNamesHost(t *testing.T) {
	files := fstest.MapFS{}
	var log bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&log, nil))
	g := Gates{Policy: PolicyGate{Files: files}, Identity: IdentityGate{Probe: func(context.Context, string, string, []string) ([]byte, error) {
		// The pre-auth CLI reports the required logged-out precondition.
		return []byte(`{"loggedIn":false,"authMethod":"none","apiProvider":"firstParty"}`), nil
	}}, CredentialSource: "gateway", AdminPolicy: "honor", GatewayHost: "gw.example.com"}
	profile, err := g.Startup(t.Context(), "/pinned", "/workspace", []string{"CLAUDE_CONFIG_DIR=/owned"}, logger)
	if err != nil || profile.CredentialSource != "gateway" || profile.RuntimeIdentity != "gateway/gw.example.com" || profile.PolicyDisposition != "honored_potential_server_policy" {
		t.Fatalf("profile %+v %v", profile, err)
	}
	if strings.Contains(log.String(), "synthetic") || strings.Contains(log.String(), "private") {
		t.Fatalf("unsafe disclosure: %s", &log)
	}
	// A pooled-process restart re-runs the gate and discloses the same host.
	profile2, err := g.Startup(t.Context(), "/pinned", "/workspace", []string{"CLAUDE_CONFIG_DIR=/owned"}, nil)
	if err != nil || profile2.RuntimeIdentity != "gateway/gw.example.com" {
		t.Fatalf("restart disclosure: %+v %v", profile2, err)
	}
	// Without a host the identity gate keeps its exact source-class identity.
	noHost := Gates{Policy: PolicyGate{Files: files}, Identity: g.Identity, CredentialSource: "gateway", AdminPolicy: "honor"}
	plain, err := noHost.Startup(t.Context(), "/pinned", "/workspace", []string{"CLAUDE_CONFIG_DIR=/owned"}, nil)
	if err != nil || plain.RuntimeIdentity != "gateway/gateway" {
		t.Fatalf("hostless disclosure: %+v %v", plain, err)
	}
}

// Requirement 2 / 5: the gateway negative precondition tolerates only the
// certified logged-out runner behavior — exit 1 with the exact logged-out
// JSON. Any other runner failure (other exit codes, signals, broken runners,
// cancellation) refuses even when the JSON document looks acceptable.
func TestIdentityGate_GatewayProbeToleratesOnlyCertifiedExitOne(t *testing.T) {
	loggedOut := []byte(`{"loggedIn":false,"authMethod":"none","apiProvider":"firstParty"}`)
	run := func(args []string) error { return exec.Command(args[0], args[1:]...).Run() }
	exit1 := run([]string{"false"})
	exit3 := run([]string{"sh", "-c", "exit 3"})
	signaled := func() error {
		cmd := exec.Command("sleep", "30")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		if err := cmd.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		return cmd.Wait()
	}()
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"exit-one-logged-out", exit1, true},
		{"exit-one-wrapped", fmt.Errorf("runner: %w", exit1), true},
		{"runner-single-joined-exit", errors.Join(nil, nil, exit1), true},
		{"exit-one-with-cleanup-failure", errors.Join(exit1, errors.New("process-tree cleanup FAILED")), false},
		{"exit-one-with-wait-failure", errors.Join(errors.New("wait failed"), exit1), false},
		{"wrapped-joined-cleanup-failure", fmt.Errorf("runner: %w", errors.Join(exit1, errors.New("cleanup failed"))), false},
		{"exit-one-with-independent-exit", errors.Join(exit1, exit1), false},
		{"exit-three", exit3, false},
		{"signaled", signaled, false},
		{"generic-runner-failure", errors.New("broken runner"), false},
		{"nil-exit-error", (*exec.ExitError)(nil), false},
		{"missing-process-state", &exec.ExitError{}, false},
		{"canceled", context.Canceled, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := IdentityGate{Probe: func(ctx context.Context, executable, cwd string, env []string) ([]byte, error) {
				return loggedOut, tc.err
			}}
			_, err := g.Check(t.Context(), "/pinned/adapter", "/workspace", []string{"HOME=/owned"}, "gateway", "honor")
			if tc.want && err != nil {
				t.Fatalf("expected certified exit-1 acceptance: %v", err)
			}
			if !tc.want && err == nil {
				t.Fatalf("expected refusal for %s", tc.name)
			}
			if !tc.want && strings.Contains(fmt.Sprintf("%+v", err), "logged") {
				t.Fatalf("refusal leaked probe internals: %v", err)
			}
		})
	}
	// Other sources never tolerate even the certified exit-1 runner.
	g := IdentityGate{Probe: func(ctx context.Context, executable, cwd string, env []string) ([]byte, error) {
		return []byte(`{"loggedIn":true,"authMethod":"api_key","apiProvider":"firstParty","apiKeySource":"ANTHROPIC_API_KEY"}`), exit1
	}}
	if _, err := g.Check(t.Context(), "/pinned/adapter", "/workspace", []string{"HOME=/owned"}, "api_key", "honor"); err == nil {
		t.Fatal("non-gateway source must refuse any probe failure")
	}
}

func TestStartupGate_DisclosesOnlySafeFieldsAndStopsSpawn(t *testing.T) {
	files := fstest.MapFS{}
	var log bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&log, nil))
	g := Gates{Policy: PolicyGate{Files: files}, Identity: IdentityGate{Probe: func(context.Context, string, string, []string) ([]byte, error) {
		return []byte(`{"loggedIn":true,"authMethod":"api_key","apiProvider":"firstParty","apiKeySource":"ANTHROPIC_API_KEY","email":"private-email"}`), nil
	}}, CredentialSource: "api_key", AdminPolicy: "honor"}
	profile, err := g.Startup(t.Context(), "/pinned", "/workspace", []string{"CLAUDE_CONFIG_DIR=/owned"}, logger)
	if err != nil || profile.CredentialSource != "api_key" || profile.RuntimeIdentity != "firstParty/api_key" || profile.PolicyDisposition != "honored_potential_server_policy" {
		t.Fatalf("profile %+v %v", profile, err)
	}
	if strings.Count(log.String(), "\n") != 1 || strings.Contains(log.String(), "private") || !strings.Contains(log.String(), "policy_disposition") {
		t.Fatalf("unsafe disclosure: %s", &log)
	}
	files["etc/claude-code/managed-settings.json"] = &fstest.MapFile{Data: []byte(`{}`)}
	profile, err = g.Startup(t.Context(), "/pinned", "/workspace", []string{"CLAUDE_CONFIG_DIR=/owned"}, nil)
	if err != nil || profile.PolicyDisposition != "honored_administrator" {
		t.Fatalf("known local authority lost: %+v %v", profile, err)
	}
	g.AdminPolicy = "refuse"
	a, err := NewAuthEnv(AuthConfig{}, map[string]string{"api_key": "synthetic-secret"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	next := &authStarter{}
	state := t.TempDir()
	starter := a.StarterWithGate(state, next, func(cmd []string, cwd string, env []string) error {
		_, err := g.Startup(t.Context(), cmd[0], cwd, env, logger)
		return err
	})
	if _, err := starter.Start([]string{"/pinned"}, "/workspace", nil); err == nil {
		t.Fatal("policy did not stop spawn")
	}
	if next.starts != 0 {
		t.Fatal("delegate spawned despite policy")
	}
	if entries, err := os.ReadDir(state); err != nil || len(entries) != 0 {
		t.Fatalf("rejected spawn leaked root: %v %v", entries, err)
	}
	if strings.Count(log.String(), "\n") != 1 {
		t.Fatal("failed spawn logged successful disclosure")
	}
}

func TestPolicyFingerprint_ExistingOwnerReplacesRuntime(t *testing.T) {
	files := fstest.MapFS{"etc/claude-code/managed-settings.json": &fstest.MapFile{Data: []byte(`{}`)}}
	g := PolicyGate{Files: files}
	before, err := g.Check(t.Context(), "/owned", "honor")
	if err != nil {
		t.Fatal(err)
	}
	child := &testAuthProcess{}
	pool := acp.NewRuntimePool(acp.RuntimePoolConfig{})
	t.Cleanup(func() {
		if err := pool.Close(); err != nil {
			t.Error(err)
		}
	})
	key := acp.RuntimeKey{Workspace: "/work", Model: "sonnet", ClientSession: "client"}
	pool.SetProcess(key, child, nil, "session", before.Fingerprint, "adapter")
	runtime, busy := pool.ClaimForTurn(t.Context(), key, before.Fingerprint)
	if busy || runtime.SessionID() != "session" || child.waitCount() != 0 {
		t.Fatal("unchanged policy replaced runtime")
	}
	pool.Release(key)
	files["etc/claude-code/managed-settings.json"].Data = []byte(`{"env":{}}`)
	after, err := g.Check(t.Context(), "/owned", "honor")
	if err != nil {
		t.Fatal(err)
	}
	runtime, busy = pool.ClaimForTurn(t.Context(), key, after.Fingerprint)
	if busy || runtime.SessionID() != "" {
		t.Fatal("changed policy did not quiesce runtime through existing owner")
	}
	// The retirement of the replaced child runs in a pool-owned goroutine: the
	// claim must not block on the process-tree cleanup.
	waitForProcess(t, child, 1)
	if string(files["etc/claude-code/managed-settings.json"].Data) != `{"env":{}}` {
		t.Fatal("honor modified trusted policy")
	}
	pool.Release(key)
}

// waitForProcess waits until the fake process observed the expected number of
// Wait calls. The runtime pool retires a replaced child in a pool-owned
// goroutine, so the observation is asynchronous.
func waitForProcess(t *testing.T, proc *testAuthProcess, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		waits := proc.waitCount()
		if waits >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("process waits = %d, want %d", waits, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
