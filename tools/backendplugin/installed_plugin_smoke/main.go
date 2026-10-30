package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/tools/backendplugin/runner"
	"github.com/aiproxer/aiproxer/tools/taskrunner"
	"gopkg.in/yaml.v3"
)

const (
	smokeCommandTimeout = 20 * time.Minute
	smokeProcessTimeout = 2 * time.Minute
)

type releaseMeta struct {
	Schema       string   `yaml:"schema"`
	PluginID     string   `yaml:"plugin_id"`
	FactoryKind  string   `yaml:"factory_kind"`
	Module       string   `yaml:"module"`
	Command      string   `yaml:"command"`
	ManifestTmpl string   `yaml:"manifest_template"`
	Profiles     []string `yaml:"profiles"`
}

type discoveredRelease struct {
	DirName string
	Root    string
	Meta    releaseMeta
	Exports int
}

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	if err := run(root); err != nil {
		fmt.Fprintf(os.Stderr, "installed_plugin_smoke: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("OK installed-plugin-smoke")
}

func run(repoRoot string) error {
	absRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return err
	}
	env := []string{"GOWORK=off"}
	work, err := os.MkdirTemp("", "aip-installed-smoke-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(work) }()

	binName := "aipstd"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	binPath := filepath.Join(work, binName)
	fmt.Println("== build aipstd once (GOWORK=off) ==")
	result := runner.Run(context.Background(), runner.Request{
		Argv:      []string{"go", "build", "-o", binPath, "./cmd/aipstd"},
		Dir:       absRoot,
		Env:       env,
		Timeout:   smokeCommandTimeout,
		Output:    taskrunner.Stream,
		StreamOut: os.Stdout,
		StreamErr: os.Stderr,
		Label:     "installed_plugin_smoke:build",
	})
	if result.Kind != taskrunner.Success {
		return fmt.Errorf("build aipstd: %w", runner.Error(result))
	}
	hashBefore, err := fileSHA256(binPath)
	if err != nil {
		return err
	}
	fmt.Printf("binary sha256=%s\n", hashBefore)

	minimalCfg := filepath.Join(work, "minimal.yaml")
	if err := os.WriteFile(minimalCfg, []byte(minimalConfigYAML()), 0o600); err != nil {
		return err
	}
	fmt.Println("== inspect before plugin install ==")
	beforeOut, err := runAipstd(binPath, minimalCfg, "inspect")
	if err != nil {
		return fmt.Errorf("inspect before: %w\n%s", err, beforeOut)
	}
	if err := assertInspectEssentialsOnly(beforeOut); err != nil {
		return fmt.Errorf("before install: %w", err)
	}

	rels, err := discoverReleases(absRoot)
	if err != nil {
		return err
	}
	stub, multi, err := selectReleases(rels)
	if err != nil {
		return err
	}
	fmt.Printf("== selected releases via metadata: stub=%s multi=%s ==\n", stub.DirName, multi.DirName)

	pkgDest := filepath.Join(work, "plugins")
	selectArg := stub.DirName + "," + multi.DirName
	fmt.Println("== package selected release artifacts ==")
	result = runner.Run(context.Background(), runner.Request{
		Argv: []string{
			"go", "run", "./tools/backendplugin/package_plugins",
			"-root", absRoot, "-profile", "full", "-dest", pkgDest, "-select", selectArg,
		},
		Dir:       absRoot,
		Env:       env,
		Timeout:   smokeCommandTimeout,
		Output:    taskrunner.Stream,
		StreamOut: os.Stdout,
		StreamErr: os.Stderr,
		Label:     "installed_plugin_smoke:package_plugins",
	})
	if result.Kind != taskrunner.Success {
		return fmt.Errorf("package_plugins: %w", runner.Error(result))
	}

	hashAfterPkg, err := fileSHA256(binPath)
	if err != nil {
		return err
	}
	if hashAfterPkg != hashBefore {
		return fmt.Errorf("aipstd binary hash changed after packaging (rebuild forbidden)")
	}

	stubKind := stub.Meta.FactoryKind
	multiKind := multi.Meta.FactoryKind
	pluginCfg := filepath.Join(work, "with-plugins.yaml")
	if err := os.WriteFile(pluginCfg, []byte(pluginsConfigYAML(pkgDest, stub.DirName, multi.DirName, stubKind, multiKind, true)), 0o600); err != nil {
		return err
	}

	fmt.Println("== check-config with plugins ==")
	if out, err := runAipstd(binPath, pluginCfg, "check-config"); err != nil {
		return fmt.Errorf("check-config: %w\n%s", err, out)
	}
	fmt.Println("== inspect with plugins installed ==")
	inspectOut, err := runAipstd(binPath, pluginCfg, "inspect")
	if err != nil {
		return fmt.Errorf("inspect after install: %w\n%s", err, inspectOut)
	}
	for _, kind := range []string{stubKind, multiKind} {
		if !strings.Contains(inspectOut, `"kind": "`+kind+`"`) && !strings.Contains(inspectOut, `"kind":"`+kind+`"`) {
			return fmt.Errorf("inspect missing discovered kind %q\n%s", kind, inspectOut)
		}
	}
	if err := assertKindsDiscoveredNotBuiltin(inspectOut, stubKind, multiKind); err != nil {
		return err
	}

	fmt.Println("== doctor stub instance ==")
	result = runner.Run(context.Background(), runner.Request{
		Argv:    []string{binPath, "--config", pluginCfg, "-instance", "smoke-stub", "doctor"},
		Env:     env,
		Timeout: smokeCommandTimeout,
		Output:  taskrunner.Capture,
		Label:   "installed_plugin_smoke:doctor",
	})
	if result.Kind != taskrunner.Success {
		return fmt.Errorf("doctor: %w", runner.Error(result))
	}

	fmt.Println("== invoke stub via serve (same binary) ==")
	if err := invokeStubViaServe(binPath, pluginCfg, env); err != nil {
		return err
	}

	hashAfterInvoke, err := fileSHA256(binPath)
	if err != nil {
		return err
	}
	if hashAfterInvoke != hashBefore {
		return fmt.Errorf("aipstd binary hash changed after invoke")
	}

	fmt.Println("== remove stub artifact; prove optional gone, binary unchanged ==")
	stubDir := filepath.Join(pkgDest, stub.DirName)
	if err := os.RemoveAll(stubDir); err != nil {
		return err
	}
	// Keep multi enabled/discovered; leave stub kind unconfigured so catalog can succeed.
	afterCfg := filepath.Join(work, "after-remove.yaml")
	if err := os.WriteFile(afterCfg, []byte(pluginsConfigYAML(pkgDest, stub.DirName, multi.DirName, stubKind, multiKind, false)), 0o600); err != nil {
		return err
	}
	afterRemove, err := runAipstd(binPath, afterCfg, "inspect")
	if err != nil {
		return fmt.Errorf("inspect after stub removal: %w\n%s", err, afterRemove)
	}
	if err := assertKindAbsentDiscovered(afterRemove, stubKind); err != nil {
		return err
	}
	if err := assertKindsDiscoveredNotBuiltin(afterRemove, multiKind); err != nil {
		return fmt.Errorf("multi-export after stub removal: %w\n%s", err, afterRemove)
	}
	hashFinal, err := fileSHA256(binPath)
	if err != nil {
		return err
	}
	if hashFinal != hashBefore {
		return fmt.Errorf("aipstd binary hash changed after artifact removal")
	}

	essCfg := filepath.Join(work, "essentials-still.yaml")
	if err := os.WriteFile(essCfg, []byte(minimalConfigYAML()), 0o600); err != nil {
		return err
	}
	if out, err := runAipstd(binPath, essCfg, "inspect"); err != nil {
		return fmt.Errorf("essentials inspect after removal: %w\n%s", err, out)
	}
	return nil
}

func discoverReleases(root string) ([]discoveredRelease, error) {
	base := filepath.Join(root, "connectors")
	ents, err := os.ReadDir(base)
	if err != nil {
		return nil, err
	}
	var out []discoveredRelease
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(base, e.Name())
		raw, err := os.ReadFile(filepath.Join(dir, "release.yaml"))
		if err != nil {
			continue
		}
		var meta releaseMeta
		dec := yaml.NewDecoder(bytes.NewReader(raw))
		dec.KnownFields(false)
		if err := dec.Decode(&meta); err != nil {
			continue
		}
		if meta.Schema != "aip.connector.release/v1" {
			continue
		}
		exports := countManifestExports(filepath.Join(dir, meta.ManifestTmpl))
		out = append(out, discoveredRelease{DirName: e.Name(), Root: dir, Meta: meta, Exports: exports})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DirName < out[j].DirName })
	return out, nil
}

func countManifestExports(path string) int {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var m struct {
		Exports []json.RawMessage `json:"exports"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return 0
	}
	return len(m.Exports)
}

func selectReleases(rels []discoveredRelease) (stub, multi discoveredRelease, err error) {
	var stubOK, multiOK bool
	for _, r := range rels {
		if !hasProfile(r.Meta.Profiles, "full") {
			continue
		}
		kind := strings.ToLower(r.Meta.FactoryKind)
		pid := strings.ToLower(r.Meta.PluginID)
		if !stubOK && r.Exports == 1 && (strings.Contains(kind, "stub") || strings.Contains(pid, "stub")) {
			stub = r
			stubOK = true
		}
		if !multiOK && r.Exports >= 2 {
			multi = r
			multiOK = true
		}
	}
	if !stubOK {
		return discoveredRelease{}, discoveredRelease{}, fmt.Errorf("no single-export stub-like release.yaml found")
	}
	if !multiOK {
		return discoveredRelease{}, discoveredRelease{}, fmt.Errorf("no multi-export release.yaml found")
	}
	return stub, multi, nil
}

func hasProfile(profiles []string, want string) bool {
	return slices.Contains(profiles, want)
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func runAipstd(bin, cfg, cmd string) (string, error) {
	result := runner.Run(context.Background(), runner.Request{
		Argv:    []string{bin, "--config", cfg, cmd},
		Env:     []string{"GOWORK=off"},
		Timeout: smokeCommandTimeout,
		Output:  taskrunner.Capture,
		Label:   "installed_plugin_smoke:" + cmd,
	})
	return string(result.Stdout), resultError(result)
}

func resultError(result taskrunner.Result) error {
	if result.Kind == taskrunner.Success {
		return nil
	}
	return runner.Error(result)
}

func assertInspectEssentialsOnly(out string) error {
	var rep struct {
		Entries []struct {
			Source string `json:"source"`
			Kind   string `json:"kind"`
		} `json:"entries"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		return fmt.Errorf("parse inspect json: %w\n%s", err, out)
	}
	for _, e := range rep.Entries {
		if e.Source == "discovered" {
			return fmt.Errorf("unexpected discovered entry before install: %+v", e)
		}
	}
	return nil
}

func assertKindsDiscoveredNotBuiltin(out string, kinds ...string) error {
	var rep struct {
		Entries []struct {
			Source string `json:"source"`
			Kind   string `json:"kind"`
			Reason string `json:"reason"`
		} `json:"entries"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		return fmt.Errorf("parse inspect json: %w", err)
	}
	want := map[string]bool{}
	for _, k := range kinds {
		want[k] = false
	}
	for _, e := range rep.Entries {
		if _, ok := want[e.Kind]; !ok {
			continue
		}
		if e.Source == "builtin" || e.Reason == "builtin_collision" {
			return fmt.Errorf("kind %q classified as builtin/collision: %+v", e.Kind, e)
		}
		if e.Source == "discovered" || e.Source == "configured" {
			want[e.Kind] = true
		}
	}
	for k, ok := range want {
		if !ok {
			return fmt.Errorf("kind %q not present as discovered/configured", k)
		}
	}
	return nil
}

func assertKindAbsentDiscovered(out, kind string) error {
	var rep struct {
		Entries []struct {
			Source string `json:"source"`
			Kind   string `json:"kind"`
			State  string `json:"state"`
		} `json:"entries"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		return fmt.Errorf("parse inspect json: %w", err)
	}
	for _, e := range rep.Entries {
		if e.Kind == kind && (e.Source == "discovered" || e.Source == "configured") && e.State != "failed" {
			return fmt.Errorf("kind %q still present after artifact removal: %+v", kind, e)
		}
	}
	return nil
}

func invokeStubViaServe(bin, cfg string, env []string) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	raw, err := os.ReadFile(cfg)
	if err != nil {
		return err
	}
	patched := strings.Replace(string(raw), `address: "127.0.0.1:0"`, `address: "`+addr+`"`, 1)
	serveCfg := cfg + ".serve.yaml"
	if err := os.WriteFile(serveCfg, []byte(patched), 0o600); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), smokeProcessTimeout)
	defer cancel()
	resultCh := make(chan taskrunner.Result, 1)
	go func() {
		resultCh <- runner.Run(ctx, runner.Request{
			Argv:    []string{bin, "--config", serveCfg, "serve"},
			Env:     env,
			Timeout: smokeProcessTimeout,
			Output:  taskrunner.Capture,
			Label:   "installed_plugin_smoke:serve",
		})
	}()
	stopServer := func() taskrunner.Result {
		cancel()
		return <-resultCh
	}

	deadline := time.Now().Add(45 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/v1/responses", strings.NewReader(`{"model":"stub-default","input":"hi"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer test")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			select {
			case result := <-resultCh:
				return fmt.Errorf("serve exited before readiness: %w", runner.Error(result))
			default:
			}
			lastErr = err
			time.Sleep(200 * time.Millisecond)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			result := stopServer()
			if result.Kind != taskrunner.DeadlineExceeded && result.Kind != taskrunner.ChildFailure {
				return fmt.Errorf("serve cleanup: %w", runner.Error(result))
			}
			return nil
		}
		lastErr = fmt.Errorf("status=%d body=%s", resp.StatusCode, truncate(string(body), 400))
		time.Sleep(200 * time.Millisecond)
	}
	result := stopServer()
	if result.Kind != taskrunner.DeadlineExceeded && result.Kind != taskrunner.ChildFailure {
		return fmt.Errorf("invoke stub via serve: %v; cleanup: %w", lastErr, runner.Error(result))
	}
	if result.Stderr != nil {
		return fmt.Errorf("invoke stub via serve: %v; stderr: %s", lastErr, truncate(string(result.Stderr), 400))
	}
	return fmt.Errorf("invoke stub via serve: %v", lastErr)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func minimalConfigYAML() string {
	return `server:
  address: "127.0.0.1:0"
access:
  mode: single_user
routing:
  max_attempts: 3
  default_route: "openai-responses:gpt-4o-mini"
continuity:
  in_memory: true
  store: memory
plugins:
  backend_discovery:
    enabled: false
  frontends:
    - id: openai-responses
      enabled: true
      config: {}
    - id: openai-legacy
      enabled: true
      config: {}
    - id: anthropic
      enabled: true
      config: {}
    - id: gemini
      enabled: true
      config: {}
  backends:
    - id: openai-responses
      kind: openai-responses
      enabled: true
      config:
        api_key: smoke-test-key
    - id: openai-legacy
      enabled: false
      config: {}
    - id: anthropic
      enabled: false
      config: {}
    - id: gemini
      enabled: false
      config: {}
    - id: bedrock
      enabled: false
      config: {}
  features:
    - id: submit-noop
      enabled: true
      config: {}
    - id: parts-noop
      enabled: true
      config: {}
    - id: tool-reactor-noop
      enabled: true
      config: {}
`
}

func pluginsConfigYAML(pluginRoot, stubDir, multiDir, stubKind, multiKind string, enableStub bool) string {
	stubPath := filepath.ToSlash(filepath.Join(pluginRoot, stubDir))
	multiPath := filepath.ToSlash(filepath.Join(pluginRoot, multiDir))
	stubEnabled := "false"
	route := "openai-responses:gpt-4o-mini"
	if enableStub {
		stubEnabled = "true"
		route = "smoke-stub:stub-default"
	}
	paths := fmt.Sprintf("      - %q\n", multiPath)
	if enableStub || dirExists(filepath.Join(pluginRoot, stubDir)) {
		paths = fmt.Sprintf("      - %q\n      - %q\n", stubPath, multiPath)
	}
	return fmt.Sprintf(`server:
  address: "127.0.0.1:0"
access:
  mode: single_user
routing:
  max_attempts: 3
  default_route: %q
continuity:
  in_memory: true
  store: memory
plugins:
  backend_discovery:
    enabled: true
    development_mode: true
    paths:
%s  frontends:
    - id: openai-responses
      enabled: true
      config: {}
    - id: openai-legacy
      enabled: true
      config: {}
    - id: anthropic
      enabled: true
      config: {}
    - id: gemini
      enabled: true
      config: {}
  backends:
    - id: openai-responses
      kind: openai-responses
      enabled: true
      config:
        api_key: smoke-test-key
    - id: smoke-stub
      kind: %s
      enabled: %s
      config:
        model: stub-default
    - id: smoke-multi
      kind: %s
      enabled: false
      config: {}
    - id: openai-legacy
      enabled: false
      config: {}
    - id: anthropic
      enabled: false
      config: {}
    - id: gemini
      enabled: false
      config: {}
    - id: bedrock
      enabled: false
      config: {}
  features:
    - id: submit-noop
      enabled: true
      config: {}
    - id: parts-noop
      enabled: true
      config: {}
    - id: tool-reactor-noop
      enabled: true
      config: {}
`, route, paths, stubKind, stubEnabled, multiKind)
}

func dirExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}
