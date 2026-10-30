package runtimebundle

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// Connector staging cache. bpkit.Stage*/MaterializeExampleConfig rebuild the
// same connector binaries on every call (~1.7s of go command overhead per
// call even with a warm build cache); with dozens of call sites in this
// package that dominated the suite wall time. The binaries and manifests are
// content-identical across tests, so each is built once per test binary run
// and every caller receives an isolated per-test copy of the staged root
// (clone-before-mutate; the shared source is never handed out).

type connectorBuildSpec struct {
	name      string // cache key
	buildDir  string // working dir relative to repo root
	buildPkg  string // package to build
	binName   string // output binary name (without OS suffix)
	goWorkOff bool   // connectors are independent modules needing GOWORK=off
}

type builtBinary struct {
	path   string
	digest string
}

var (
	connectorBuildsMu sync.Mutex
	connectorBuilds   = map[string]*sync.Once{}
	builtBinaries     = map[string]builtBinary{}
	builtBinaryErrs   = map[string]error{}

	stagedRootsMu   sync.Mutex
	stagedRoots     = map[string]*sync.Once{}
	stagedRootPaths = map[string]string{}
	stagedRootErrs  = map[string]error{}
)

func onceFor(mu *sync.Mutex, m map[string]*sync.Once, key string) *sync.Once {
	mu.Lock()
	defer mu.Unlock()
	once, ok := m[key]
	if !ok {
		once = &sync.Once{}
		m[key] = once
	}
	return once
}

func cachedBuiltBinary(tb testing.TB, spec connectorBuildSpec) builtBinary {
	tb.Helper()
	once := onceFor(&connectorBuildsMu, connectorBuilds, spec.name)
	once.Do(func() {
		bin, err := buildConnectorBinary(spec)
		connectorBuildsMu.Lock()
		defer connectorBuildsMu.Unlock()
		if err != nil {
			builtBinaryErrs[spec.name] = err
			return
		}
		builtBinaries[spec.name] = bin
	})
	connectorBuildsMu.Lock()
	err := builtBinaryErrs[spec.name]
	bin := builtBinaries[spec.name]
	connectorBuildsMu.Unlock()
	if err != nil {
		tb.Fatalf("build %s: %v", spec.name, err)
	}
	return bin
}

func buildConnectorBinary(spec connectorBuildSpec) (builtBinary, error) {
	repo, err := stagingRepoRoot()
	if err != nil {
		return builtBinary{}, err
	}
	binName := spec.binName
	if runtime.GOOS == "windows" && !strings.HasSuffix(binName, ".exe") {
		binName += ".exe"
	}
	dir, err := os.MkdirTemp(stagingCacheRoot, "aip-connector-build-*")
	if err != nil {
		return builtBinary{}, err
	}
	dst := filepath.Join(dir, binName)
	cmd := exec.Command("go", "build", "-ldflags=-w", "-o", dst, spec.buildPkg)
	cmd.Dir = filepath.Join(repo, filepath.FromSlash(spec.buildDir))
	if spec.goWorkOff {
		cmd.Env = append(os.Environ(), "GOWORK=off")
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return builtBinary{}, fmt.Errorf("go build %s: %v\n%s", spec.buildPkg, err, out)
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		return builtBinary{}, err
	}
	sum := sha256.Sum256(data)
	return builtBinary{path: dst, digest: hex.EncodeToString(sum[:])}, nil
}

func stagingRepoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "connectors", "localstub", "go.mod")); err == nil {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("repo root with connectors/localstub not found above %s", wd)
		}
		dir = parent
	}
}

// sharedStagedRoot builds a plugin discovery root (bin/<name> +
// plugin.backendplugin.json) once per manifest key and returns the shared
// root. Callers must use copyStagedRoot before handing the root to a test.
func sharedStagedRoot(tb testing.TB, key string, spec connectorBuildSpec, manifest func(rel, digest string) string) string {
	tb.Helper()
	once := onceFor(&stagedRootsMu, stagedRoots, key)
	once.Do(func() {
		root, err := stageConnectorRoot(spec, manifest)
		stagedRootsMu.Lock()
		defer stagedRootsMu.Unlock()
		if err != nil {
			stagedRootErrs[key] = err
			return
		}
		stagedRootPaths[key] = root
	})
	stagedRootsMu.Lock()
	err := stagedRootErrs[key]
	root := stagedRootPaths[key]
	stagedRootsMu.Unlock()
	if err != nil {
		tb.Fatalf("stage %s: %v", key, err)
	}
	return root
}

func stageConnectorRoot(spec connectorBuildSpec, manifest func(rel, digest string) string) (string, error) {
	bin, err := buildConnectorBinary(spec)
	if err != nil {
		return "", err
	}
	binName := filepath.Base(bin.path)
	rel := filepath.ToSlash(filepath.Join("bin", binName))
	root, err := os.MkdirTemp(stagingCacheRoot, "aip-staged-root-*")
	if err != nil {
		return "", err
	}
	dst := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return "", err
	}
	if err := copyFileMode(dst, bin.path, 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(root, "plugin.backendplugin.json"), []byte(manifest(rel, bin.digest)), 0o600); err != nil {
		return "", err
	}
	return root, nil
}

// copyStagedRoot clones a staged root into the caller's per-test temp dir so
// tests keep full isolation (mutations never touch the shared source).
func copyStagedRoot(tb testing.TB, srcRoot string) string {
	tb.Helper()
	dstRoot := tb.TempDir()
	entries, err := os.ReadDir(srcRoot)
	if err != nil {
		tb.Fatal(err)
	}
	for _, ent := range entries {
		if ent.IsDir() {
			if err := copyDirRecursive(tb, filepath.Join(srcRoot, ent.Name()), filepath.Join(dstRoot, ent.Name())); err != nil {
				tb.Fatal(err)
			}
			continue
		}
		if err := copyFileMode(filepath.Join(dstRoot, ent.Name()), filepath.Join(srcRoot, ent.Name()), 0o600); err != nil {
			tb.Fatal(err)
		}
	}
	return dstRoot
}

func copyDirRecursive(tb testing.TB, src, dst string) error {
	tb.Helper()
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, ent := range entries {
		srcPath := filepath.Join(src, ent.Name())
		dstPath := filepath.Join(dst, ent.Name())
		if ent.IsDir() {
			if err := copyDirRecursive(tb, srcPath, dstPath); err != nil {
				return err
			}
			continue
		}
		mode := os.FileMode(0o600)
		if info, err := ent.Info(); err == nil && info.Mode().Perm()&0o100 != 0 {
			mode = 0o700
		}
		if err := copyFileMode(dstPath, srcPath, mode); err != nil {
			return err
		}
	}
	return nil
}

func copyFileMode(dst, src string, perm os.FileMode) error {
	input, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	output, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	return errors.Join(copyErr, output.Close())
}

var (
	specLocalStub = connectorBuildSpec{
		name:      "localstub",
		buildDir:  "connectors/localstub",
		buildPkg:  "./cmd/aip-backend-localstub",
		binName:   "aip-backend-localstub",
		goWorkOff: true,
	}
	specCodex = connectorBuildSpec{
		name:      "codex",
		buildDir:  "connectors/codex",
		buildPkg:  "./cmd/aip-backend-codex",
		binName:   "aip-backend-codex",
		goWorkOff: true,
	}
	specOpenCode = connectorBuildSpec{
		name:      "opencode",
		buildDir:  "connectors/opencode",
		buildPkg:  "./cmd/aip-backend-opencode",
		binName:   "aip-backend-opencode",
		goWorkOff: true,
	}
	specOpenRouter = connectorBuildSpec{
		name:      "openrouter",
		buildDir:  "connectors/openrouter",
		buildPkg:  "./cmd/aip-backend-openrouter",
		binName:   "aip-backend-openrouter",
		goWorkOff: true,
	}
	specBackendPluginFake = connectorBuildSpec{
		name:     "backendplugin-fake",
		buildDir: ".",
		buildPkg: "./internal/testkit/backendplugin/cmd/aip-backendplugin-fake",
		binName:  "aip-backendplugin-fake",
	}
	specFakeCodexCLI = connectorBuildSpec{
		name:      "fake-codex-cli",
		buildDir:  "connectors/codex",
		buildPkg:  "./cmd/fake-codex-cli",
		binName:   "fake-codex-cli",
		goWorkOff: true,
	}
)

func localStubManifest(rel, digest string) string {
	return fmt.Sprintf(`{
  "schema":"aiproxer.backendplugin.manifest/v1",
  "plugin_id":"com.aiproxer.backend.localstub",
  "version":"0.1.0",
  "build_id":"test",
  "executable":%q,
  "sha256":%q,
  "protocol_major":1,
  "protocol_min_minor":0,
  "protocol_max_minor":0,
  "platforms":[{"os":%q,"arch":%q}],
  "exports":[{
    "kind":"local-stub",
    "credential_mode":"none",
    "access_scope":"any",
    "process_sharing":"per_instance"
  }]
}`, rel, digest, runtime.GOOS, runtime.GOARCH)
}

func codexManifest(rel, digest string) string {
	return fmt.Sprintf(`{
  "schema":"aiproxer.backendplugin.manifest/v1",
  "plugin_id":"com.aiproxer.backend.codex",
  "version":"0.1.0",
  "build_id":"test",
  "executable":%q,
  "sha256":%q,
  "protocol_major":1,
  "protocol_min_minor":0,
  "protocol_max_minor":0,
  "platforms":[{"os":%q,"arch":%q}],
  "exports":[
    {"kind":"openai-codex","credential_mode":"static","access_scope":"local_only","process_sharing":"per_instance","credential_contract":{"version":2,"class":"static_nonrotatable","source":"host_env","env_root":"OPENAI_CODEX_ACCESS_TOKEN","slot":"api_token","selection":"no_rotation","quota_domain":"codex"}},
    {"kind":"openai-codex-app-server","credential_mode":"none","access_scope":"local_only","process_sharing":"per_instance"}
  ]
}`, rel, digest, runtime.GOOS, runtime.GOARCH)
}

func openCodeManifest(rel, digest string) string {
	return fmt.Sprintf(`{
  "schema":"aiproxer.backendplugin.manifest/v1",
  "plugin_id":"com.aiproxer.backend.opencode",
  "version":"0.1.0",
  "build_id":"test",
  "executable":%q,
  "sha256":%q,
  "protocol_major":1,
  "protocol_min_minor":0,
  "protocol_max_minor":0,
  "platforms":[{"os":%q,"arch":%q}],
  "exports":[
    {"kind":"opencode-go","credential_mode":"static","access_scope":"any","process_sharing":"per_instance","credential_contract":{"version":2,"class":"api_key","source":"host_env","env_root":"OPENCODE_GO_API_KEY","slot":"api_key","selection":"quota_pre_output","quota_domain":"opencode-go"}},
    {"kind":"opencode-zen","credential_mode":"static","access_scope":"any","process_sharing":"per_instance","credential_contract":{"version":2,"class":"api_key","source":"host_env","env_root":"OPENCODE_ZEN_API_KEY","slot":"api_key","selection":"quota_pre_output","quota_domain":"opencode-zen"}}
  ]
}`, rel, digest, runtime.GOOS, runtime.GOARCH)
}

func openRouterManifest(rel, digest string) string {
	return fmt.Sprintf(`{
  "schema":"aiproxer.backendplugin.manifest/v1",
  "plugin_id":"com.aiproxer.backend.openrouter",
  "version":"0.1.0",
  "build_id":"test",
  "executable":%q,
  "sha256":%q,
  "protocol_major":1,
  "protocol_min_minor":0,
  "protocol_max_minor":0,
  "platforms":[{"os":%q,"arch":%q}],
  "exports":[{
    "kind":"openrouter",
    "credential_mode":"static",
    "access_scope":"any",
    "process_sharing":"per_instance",
    "credential_contract":{"version":2,"class":"api_key","source":"host_env","env_root":"OPENROUTER_API_KEY","slot":"api_key","selection":"quota_pre_output","quota_domain":"openrouter"}
  }]
}`, rel, digest, runtime.GOOS, runtime.GOARCH)
}

// StageLocalStubForTest mirrors bpkit.StageLocalStub but builds the connector
// once per test binary run; each call returns an isolated per-test copy.
func StageLocalStubForTest(tb testing.TB) string {
	tb.Helper()
	return copyStagedRoot(tb, sharedStagedRoot(tb, "localstub", specLocalStub, localStubManifest))
}

// StageCodexForTest mirrors bpkit.StageCodex with a per-run cached build.
func StageCodexForTest(tb testing.TB) string {
	tb.Helper()
	return copyStagedRoot(tb, sharedStagedRoot(tb, "codex", specCodex, codexManifest))
}

// StageOpenCodeForTest mirrors bpkit.StageOpenCode with a per-run cached build.
func StageOpenCodeForTest(tb testing.TB) string {
	tb.Helper()
	return copyStagedRoot(tb, sharedStagedRoot(tb, "opencode", specOpenCode, openCodeManifest))
}

// StageOpenRouterForTest mirrors bpkit.StageOpenRouter with a per-run cached build.
func StageOpenRouterForTest(tb testing.TB) string {
	tb.Helper()
	return copyStagedRoot(tb, sharedStagedRoot(tb, "openrouter", specOpenRouter, openRouterManifest))
}

// BackendPluginFakeBinaryForTest returns a per-test copy of the shared
// aip-backendplugin-fake binary (built once per test binary run).
func BackendPluginFakeBinaryForTest(tb testing.TB) string {
	tb.Helper()
	bin := cachedBuiltBinary(tb, specBackendPluginFake)
	dst := filepath.Join(tb.TempDir(), filepath.Base(bin.path))
	if err := copyFileMode(dst, bin.path, 0o700); err != nil {
		tb.Fatal(err)
	}
	return dst
}

// StageColliderExportingEssentialForTest stages the backendplugin fake with a
// manifest exporting the given (builtin-colliding) kind; the binary build is
// cached per run, only the kind-parameterized manifest is written per test.
func StageColliderExportingEssentialForTest(tb testing.TB, kind string) string {
	tb.Helper()
	bin := cachedBuiltBinary(tb, specBackendPluginFake)
	root := tb.TempDir()
	rel := filepath.ToSlash(filepath.Join("bin", filepath.Base(bin.path)))
	dst := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		tb.Fatal(err)
	}
	if err := copyFileMode(dst, bin.path, 0o700); err != nil {
		tb.Fatal(err)
	}
	body := fmt.Sprintf(`{
  "schema":"aiproxer.backendplugin.manifest/v1",
  "plugin_id":"com.aiproxer.backend.collider",
  "version":"1.0.0",
  "build_id":"b",
  "executable":%q,
  "sha256":%q,
  "protocol_major":1,
  "protocol_min_minor":0,
  "protocol_max_minor":0,
  "platforms":[{"os":%q,"arch":%q}],
  "exports":[{
    "kind":%q,
    "credential_mode":"none",
    "access_scope":"local_only",
    "process_sharing":"per_instance"
  }]
}`, rel, bin.digest, runtime.GOOS, runtime.GOARCH, kind)
	if err := os.WriteFile(filepath.Join(root, "plugin.backendplugin.json"), []byte(body), 0o600); err != nil {
		tb.Fatal(err)
	}
	return root
}

// StageFakeCodexCLIForTest returns a per-test copy of the fake-codex-cli
// binary (built once per test binary run).
func StageFakeCodexCLIForTest(tb testing.TB) string {
	tb.Helper()
	bin := cachedBuiltBinary(tb, specFakeCodexCLI)
	dst := filepath.Join(tb.TempDir(), filepath.Base(bin.path))
	if err := copyFileMode(dst, bin.path, 0o700); err != nil {
		tb.Fatal(err)
	}
	return dst
}

var (
	reLocalStubKind   = regexp.MustCompile(`(?m)^[ \t]*-[ \t]*kind:[ \t]*local-stub\b`)
	reMultiUserAccess = regexp.MustCompile(`(?m)^[ \t]*mode:[ \t]*multi_user\b`)
)

// MaterializeExampleConfigForTest mirrors bpkit.MaterializeExampleConfig but
// sources the localstub discovery root from the per-run shared staging cache.
func MaterializeExampleConfigForTest(tb testing.TB, srcPath string) string {
	tb.Helper()
	raw, err := os.ReadFile(srcPath)
	if err != nil {
		tb.Fatal(err)
	}
	body := string(raw)
	if !reLocalStubKind.MatchString(body) {
		return srcPath
	}
	pluginRoot := StageLocalStubForTest(tb)
	devMode := "true"
	if reMultiUserAccess.MatchString(body) {
		devMode = "false"
	}
	discovery := "  backend_discovery:\n" +
		"    enabled: true\n" +
		"    development_mode: " + devMode + "\n" +
		"    paths:\n" +
		"      - " + filepath.ToSlash(pluginRoot) + "\n"
	if rewritten, ok := replaceBackendDiscoveryBlock(body, discovery); ok {
		body = rewritten
	} else {
		idx := strings.Index(body, "\nplugins:\n")
		if idx < 0 {
			idx = strings.Index(body, "\nplugins:\r\n")
		}
		if idx < 0 {
			tb.Fatalf("example %s uses local-stub but has no plugins: block", srcPath)
		}
		insertAt := idx + len("\nplugins:\n")
		if strings.HasPrefix(body[idx:], "\nplugins:\r\n") {
			insertAt = idx + len("\nplugins:\r\n")
		}
		body = body[:insertAt] + discovery + body[insertAt:]
	}
	dst := filepath.Join(tb.TempDir(), filepath.Base(srcPath))
	if err := os.WriteFile(dst, []byte(body), 0o600); err != nil {
		tb.Fatal(err)
	}
	return dst
}

// replaceBackendDiscoveryBlock replaces only the backend_discovery mapping and
// its nested keys, preserving sibling keys under plugins.
func replaceBackendDiscoveryBlock(body, discovery string) (string, bool) {
	lines := strings.SplitAfter(body, "\n")
	start := -1
	baseIndent := ""
	for i, line := range lines {
		trim := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trim, "backend_discovery:") {
			start = i
			baseIndent = line[:len(line)-len(trim)]
			break
		}
	}
	if start < 0 {
		return body, false
	}
	end := start + 1
	for end < len(lines) {
		line := lines[end]
		if strings.TrimSpace(line) == "" {
			end++
			continue
		}
		if !strings.HasPrefix(line, baseIndent) {
			break
		}
		rest := line[len(baseIndent):]
		if rest == "" || rest[0] == ' ' || rest[0] == '\t' {
			end++
			continue
		}
		break
	}
	var b strings.Builder
	for _, line := range lines[:start] {
		b.WriteString(line)
	}
	b.WriteString(discovery)
	for _, line := range lines[end:] {
		b.WriteString(line)
	}
	return b.String(), true
}
