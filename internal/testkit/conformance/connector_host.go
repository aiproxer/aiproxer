package conformance

// Connector-host harness for the optional connector matrix columns.
//
// origin/main relocated the executable backend connectors into their own modules
// (connectors/acp, connectors/openrouter, connectors/nvidia). The root module must
// not require connector modules (TestRootGoMod_NoConnectorModules) and production
// core must not import connector modules (hybrid-backend rules), so the conformance
// harness cannot link any connector protocol adapter in-process. Instead it builds
// the actual aip-backend-* executable of the optional connector (once per test
// binary, inside the connector module with GOWORK=off) and drives each backend
// through the backendplugin host adapter APIs (adapter.DialConfiguredSession +
// adapter.Build): the connector process is the real relocated connector,
// configured with the cell's observing origin as base_url, and the host adapter
// builds the execbackend.Backend exactly like the production composition. No
// connector protocol code is duplicated in the harness.
//
// This generalizes the original ACP-only connector harness (acp_connector.go) to
// every optional connector column the authoritative 5×9 matrix references:
// ACP (constructible through the base harness selector) and the OpenRouter/NVIDIA
// compatibility columns (driven through the dedicated connector-column deploy
// path, DeployConnectorColumnFor).
//
// Each backend owns a dedicated connector process. The process is shut down via
// tb.Cleanup, so parallel conformance cells stay isolated and no connector process
// outlives its test.

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/adapter"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// connectorHostSpec identifies one optional executable backend-connector module
// the harness drives through the backendplugin host adapter.
type connectorHostSpec struct {
	// backendID is the authoritative matrix backend identity.
	backendID string
	// module is the connector module directory relative to the repo root.
	module string
	// cmd is the package (relative to the connector module) of the aip-backend-* binary.
	cmd string
	// bin is the produced executable base name (no OS suffix).
	bin string
	// listenMarker is the stderr prefix the connector prints when it is listening.
	listenMarker string
	// instanceID is the host adapter instance identity used for each backend.
	instanceID string
}

// connectorHostSpecs is the authoritative optional-connector table. It must stay
// exactly the matrix's optional connector columns and is pinned by the default-build
// connector_host_test.go. Adding an optional connector here is a deliberate matrix
// expansion (Task 8.5 owns authoritative list expansion), never a promotion to an
// essential backend kind.
var connectorHostSpecs = map[string]connectorHostSpec{
	BackendACP: {
		backendID:    BackendACP,
		module:       "connectors/acp",
		cmd:          "./cmd/aip-backend-acp",
		bin:          "aip-backend-acp",
		listenMarker: "aip-backend-acp listening on ",
		instanceID:   "harness-acp",
	},
	BackendOpenRouter: {
		backendID:    BackendOpenRouter,
		module:       "connectors/openrouter",
		cmd:          "./cmd/aip-backend-openrouter",
		bin:          "aip-backend-openrouter",
		listenMarker: "aip-backend-openrouter listening on ",
		instanceID:   "harness-openrouter",
	},
	BackendNVIDIA: {
		backendID:    BackendNVIDIA,
		module:       "connectors/nvidia",
		cmd:          "./cmd/aip-backend-nvidia",
		bin:          "aip-backend-nvidia",
		listenMarker: "aip-backend-nvidia listening on ",
		instanceID:   "harness-nvidia",
	},
	BackendLocalStub: {
		backendID:    BackendLocalStub,
		module:       "connectors/localstub",
		cmd:          "./cmd/aip-backend-localstub",
		bin:          "aip-backend-localstub",
		listenMarker: "aip-backend-localstub listening on ",
		instanceID:   "harness-local-stub",
	},
	BackendLlamaCpp: {
		backendID:    BackendLlamaCpp,
		module:       "connectors/llamacpp",
		cmd:          "./cmd/aip-backend-llamacpp",
		bin:          "aip-backend-llamacpp",
		listenMarker: "aip-backend-llamacpp listening on ",
		instanceID:   "harness-llamacpp",
	},
	BackendLmstudio: {
		backendID:    BackendLmstudio,
		module:       "connectors/lmstudio",
		cmd:          "./cmd/aip-backend-lmstudio",
		bin:          "aip-backend-lmstudio",
		listenMarker: "aip-backend-lmstudio listening on ",
		instanceID:   "harness-lmstudio",
	},
	BackendVllm: {
		backendID:    BackendVllm,
		module:       "connectors/vllm",
		cmd:          "./cmd/aip-backend-vllm",
		bin:          "aip-backend-vllm",
		listenMarker: "aip-backend-vllm listening on ",
		instanceID:   "harness-vllm",
	},
	BackendOllama: {
		backendID:    BackendOllama,
		module:       "connectors/ollama",
		cmd:          "./cmd/aip-backend-ollama",
		bin:          "aip-backend-ollama",
		listenMarker: "aip-backend-ollama listening on ",
		instanceID:   "harness-ollama",
	},
	BackendHuggingFace: {
		backendID:    BackendHuggingFace,
		module:       "connectors/huggingface",
		cmd:          "./cmd/aip-backend-huggingface",
		bin:          "aip-backend-huggingface",
		listenMarker: "aip-backend-huggingface listening on ",
		instanceID:   "harness-huggingface",
	},
	BackendInfomaniak: {
		backendID:    BackendInfomaniak,
		module:       "connectors/infomaniak",
		cmd:          "./cmd/aip-backend-infomaniak",
		bin:          "aip-backend-infomaniak",
		listenMarker: "aip-backend-infomaniak listening on ",
		instanceID:   "harness-infomaniak",
	},
	BackendCommandCodeOpenAI: {
		backendID:    BackendCommandCodeOpenAI,
		module:       "connectors/commandcode-openai",
		cmd:          "./cmd/aip-backend-commandcode-openai",
		bin:          "aip-backend-commandcode-openai",
		listenMarker: "aip-backend-commandcode-openai listening on ",
		instanceID:   "harness-commandcode-openai",
	},
	BackendNousPortal: {
		backendID:    BackendNousPortal,
		module:       "connectors/nousportal",
		cmd:          "./cmd/aip-backend-nousportal",
		bin:          "aip-backend-nousportal",
		listenMarker: "aip-backend-nousportal listening on ",
		instanceID:   "harness-nousportal",
	},
	BackendQwenOAuth: {
		backendID:    BackendQwenOAuth,
		module:       "connectors/qwenoauth",
		cmd:          "./cmd/aip-backend-qwenoauth",
		bin:          "aip-backend-qwenoauth",
		listenMarker: "aip-backend-qwenoauth listening on ",
		instanceID:   "harness-qwenoauth",
	},
	BackendXaiOAuth: {
		backendID:    BackendXaiOAuth,
		module:       "connectors/xaioauth",
		cmd:          "./cmd/aip-backend-xaioauth",
		bin:          "aip-backend-xaioauth",
		listenMarker: "aip-backend-xaioauth listening on ",
		instanceID:   "harness-xaioauth",
	},
	BackendAzureOpenAI: {
		backendID:    BackendAzureOpenAI,
		module:       "connectors/azure",
		cmd:          "./cmd/aip-backend-azure",
		bin:          "aip-backend-azure",
		listenMarker: "aip-backend-azure listening on ",
		instanceID:   "harness-azure-openai",
	},
	BackendCloudflare: {
		backendID:    BackendCloudflare,
		module:       "connectors/cloudflare",
		cmd:          "./cmd/aip-backend-cloudflare",
		bin:          "aip-backend-cloudflare",
		listenMarker: "aip-backend-cloudflare listening on ",
		instanceID:   "harness-cloudflare",
	},
	BackendDatabricks: {
		backendID:    BackendDatabricks,
		module:       "connectors/databricks",
		cmd:          "./cmd/aip-backend-databricks",
		bin:          "aip-backend-databricks",
		listenMarker: "aip-backend-databricks listening on ",
		instanceID:   "harness-databricks",
	},
	BackendSnowflake: {
		backendID:    BackendSnowflake,
		module:       "connectors/snowflake",
		cmd:          "./cmd/aip-backend-snowflake",
		bin:          "aip-backend-snowflake",
		listenMarker: "aip-backend-snowflake listening on ",
		instanceID:   "harness-snowflake",
	},
}

func connectorHostBackendIDs() []string {
	return []string{BackendACP, BackendOpenRouter, BackendNVIDIA, BackendLocalStub, BackendLlamaCpp, BackendLmstudio, BackendVllm, BackendOllama, BackendHuggingFace, BackendInfomaniak, BackendCommandCodeOpenAI, BackendNousPortal, BackendQwenOAuth, BackendXaiOAuth, BackendAzureOpenAI, BackendCloudflare, BackendDatabricks, BackendSnowflake}
}

func connectorHostLookup(backendID string) (connectorHostSpec, bool) {
	spec, ok := connectorHostSpecs[backendID]
	return spec, ok
}

func connectorHostSpecRequired(tb testing.TB, backendID string) connectorHostSpec {
	tb.Helper()
	spec, ok := connectorHostLookup(backendID)
	if !ok {
		tb.Fatalf("harness: connector column %q has no connector host spec", backendID)
	}
	return spec
}

// connectorBuild is the built-once-per-test-binary connector executable state.
type connectorBuild struct {
	once sync.Once
	bin  string
	err  error
	log  string
}

var connectorHostBuilds sync.Map // backendID -> *connectorBuild

// connectorHostBackend launches a dedicated connector process for backendID
// configured against originURL and returns the host-built execbackend.Backend.
// The connector executable is built once per test binary; one process is launched
// per backend so the harness never shares connector state across cells.
func connectorHostBackend(tb testing.TB, backendID, originURL string) execbackend.Backend {
	tb.Helper()
	spec := connectorHostSpecRequired(tb, backendID)
	bin := connectorHostBinary(tb, backendID)
	cmd, addr := connectorHostLaunch(tb, bin, spec)
	ctx, cancel := context.WithCancel(context.Background())

	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		cancel()
		connectorHostKill(cmd)
		tb.Fatalf("harness: dial %s connector %s: %v", backendID, addr, err)
	}

	policy := backendplugin.RuntimePolicy{
		MaxRequestBytes:     backendplugin.DefaultMaxMessageBytes,
		MaxStreamFrameBytes: backendplugin.DefaultMaxStreamFrameBytes,
	}
	cfgYAML, secrets := connectorHostConfig(spec, originURL)
	sess, profile, err := adapter.DialConfiguredSession(ctx, conn, spec.instanceID, backendID, cfgYAML, secrets, policy)
	if err != nil {
		_ = conn.Close()
		cancel()
		connectorHostKill(cmd)
		tb.Fatalf("harness: configure %s connector: %v", backendID, err)
	}

	br := adapter.Build(sess, profile, adapter.Options{
		InstanceID:    spec.instanceID,
		RoutePrefixes: profile.RoutePrefixes,
		Negotiation:   sess.Negotiation(),
	})

	tb.Cleanup(func() {
		_ = br.Cleanup()
		cancel()
		connectorHostKill(cmd)
	})
	return br.Backend
}

// StagedConnectorBackend launches the real executable for one connector-host
// column and returns the host-built backend, for staged-executable proof
// outside the conformance deployment matrix (issue #1104: the connector
// terminal-semantics matrix). The build runs once per test binary inside the
// connector module (GOWORK=off); the process is cleaned up with the test.
func StagedConnectorBackend(tb testing.TB, backendID, originURL string) execbackend.Backend {
	tb.Helper()
	return connectorHostBackend(tb, backendID, originURL)
}

// connectorHostConfig builds the connector YAML and credential bundle for one
// optional connector against an observing origin. The OpenAI-compatible connector
// columns require an api_key (their Configure fails closed without one), so the
// harness supplies the same synthetic credential the essential harness backends
// use; the reference origins accept any non-empty bearer token.
func connectorHostConfig(spec connectorHostSpec, originURL string) ([]byte, backendplugin.SecretBundle) {
	switch spec.backendID {
	case BackendNousPortal, BackendQwenOAuth, BackendXaiOAuth:
		yaml := "inference_url: " + originURL + "\n"
		return []byte(yaml),
			backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("sk-test")}}
	case BackendAzureOpenAI:
		yaml := "endpoint: " + originURL + "\napi_version: 2024-10-21\ndeployments:\n  test-dep: gpt-4o-mini\n"
		return []byte(yaml),
			backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("sk-test")}}
	case BackendDatabricks:
		// Host satisfies Configure validation; api_origin steers the
		// wire at the scripted server (BaseURL prefers api_origin).
		yaml := "host: test.databricks.example\napi_origin: " + originURL + "\n"
		return []byte(yaml),
			backendplugin.SecretBundle{Values: map[string][]byte{"api_token": []byte("sk-test")}}
	case BackendSnowflake:
		// Account satisfies Configure validation; api_origin steers the
		// wire at the scripted server.
		yaml := "account: test-acct\napi_origin: " + originURL + "\n"
		return []byte(yaml),
			backendplugin.SecretBundle{Values: map[string][]byte{"api_token": []byte("sk-test")}}
	case BackendCloudflare:
		yaml := "account_id: test-acct\napi_origin: " + originURL + "\n"
		return []byte(yaml),
			backendplugin.SecretBundle{Values: map[string][]byte{"api_token": []byte("sk-test")}}
	case BackendOpenRouter, BackendNVIDIA, BackendOllama, BackendHuggingFace, BackendInfomaniak, BackendCommandCodeOpenAI:
		yaml := "base_url: " + originURL + "\n"
		if spec.backendID == BackendInfomaniak {
			// Strict schema without base_url: product_id plus an
			// origin override pointing at the scripted server.
			yaml = "product_id: 1\napi_origin: " + originURL + "\n"
		}
		return []byte(yaml),
			backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("sk-test")}}
	default:
		return []byte("base_url: " + originURL + "\n"), backendplugin.SecretBundle{}
	}
}

// connectorHostBinary returns the path to a locally built optional connector
// executable, building it once per test binary. The build runs inside the
// connector module (GOWORK=off) so the root module graph stays independent of the
// connector modules.
func connectorHostBinary(tb testing.TB, backendID string) string {
	tb.Helper()
	spec := connectorHostSpecRequired(tb, backendID)
	cb := &connectorBuild{}
	actual, _ := connectorHostBuilds.LoadOrStore(backendID, cb)
	var ok bool
	if cb, ok = actual.(*connectorBuild); !ok {
		tb.Fatalf("harness: connector host build registry corrupted for %q", backendID)
	}
	cb.once.Do(func() {
		cb.bin, cb.log, cb.err = buildConnectorBinary(spec)
	})
	if cb.err != nil {
		if cb.log != "" {
			tb.Fatalf("harness: build %s executable: %v\n%s", spec.module, cb.err, cb.log)
		}
		tb.Fatalf("harness: build %s executable: %v", spec.module, cb.err)
	}
	return cb.bin
}

func buildConnectorBinary(spec connectorHostSpec) (bin string, log string, err error) {
	root, buildErr := connectorHostRepoRoot(spec.module)
	if buildErr != nil {
		return "", "", buildErr
	}
	// Use the repo's allowlisted connector-build prefix so the leaked OS temp
	// dir (one per test binary, reclaimable by `make tmp-clean`) matches
	// scripts/tmp-clean.ps1 ownership.
	dir, buildErr := os.MkdirTemp("", "aip-connector-build-*")
	if buildErr != nil {
		return "", "", buildErr
	}
	name := spec.bin
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	bin = filepath.Join(dir, name)
	cmd := exec.Command("go", "build", "-o", bin, spec.cmd)
	cmd.Dir = filepath.Join(root, spec.module)
	cmd.Env = append(os.Environ(), "GOWORK=off")
	out, buildErr := cmd.CombinedOutput()
	if buildErr != nil {
		return "", string(out), buildErr
	}
	return bin, "", nil
}

func connectorHostRepoRoot(module string) (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, module, "go.mod")); err == nil {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("repo root with %s not found from %s", module, wd)
		}
		dir = parent
	}
}

// connectorHostLaunch starts a fresh connector process on an ephemeral loopback
// port and waits for its "listening on" address. The stderr pipe is drained for
// the process lifetime so the connector never blocks on a full pipe.
func connectorHostLaunch(tb testing.TB, bin string, spec connectorHostSpec) (*exec.Cmd, string) {
	tb.Helper()
	cmd := exec.Command(bin, "-listen", "127.0.0.1:0")
	env := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "AIP_PLUGIN_CHANNEL_") {
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = env
	stderr, err := cmd.StderrPipe()
	if err != nil {
		tb.Fatalf("harness: %s connector stderr pipe: %v", spec.backendID, err)
	}
	if err := cmd.Start(); err != nil {
		tb.Fatalf("harness: start %s connector: %v", spec.backendID, err)
	}
	addrCh := make(chan string, 1)
	go func() {
		sent := false
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			line := sc.Text()
			if !sent {
				if after, ok := strings.CutPrefix(line, spec.listenMarker); ok {
					sent = true
					addrCh <- strings.TrimSpace(after)
				}
			}
		}
		if !sent {
			addrCh <- ""
		}
	}()
	select {
	case addr := <-addrCh:
		if addr == "" {
			connectorHostKill(cmd)
			tb.Fatalf("harness: %s connector did not report a listen address", spec.backendID)
		}
		return cmd, addr
	case <-time.After(20 * time.Second):
		connectorHostKill(cmd)
		tb.Fatalf("harness: timeout waiting for %s connector listen address", spec.backendID)
	}
	return nil, ""
}

// connectorHostKill best-effort terminates a launched connector process. It is
// safe to call from tb.Cleanup (never fails the test).
func connectorHostKill(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}
