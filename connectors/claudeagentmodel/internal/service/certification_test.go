package service

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"github.com/aiproxer/aiproxer/connectors/claudeagentmodel/internal/bridge"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const minInstalledDependencyFiles = 20

// installedBundleIdentity is the approved/observed artifact schema. Approved
// files carry the exact artifact/runtime/closure identities; observed files also
// carry the account/runtime model catalogue, which is evidence but not approval.
type installedBundleIdentity struct {
	OS                   string   `json:"os"`
	Arch                 string   `json:"arch"`
	GoBinarySHA256       string   `json:"go_binary_sha256"`
	ManifestSHA256       string   `json:"manifest_sha256"`
	BridgeSHA256         string   `json:"sidecar_bridge_sha256"`
	SessionSHA256        string   `json:"sidecar_session_sha256"`
	ToolServerSHA256     string   `json:"sidecar_tool_server_sha256"`
	PackageJSONSHA256    string   `json:"sidecar_package_json_sha256"`
	PackageLockSHA256    string   `json:"sidecar_package_lock_sha256"`
	DependencyTreeSHA256 string   `json:"dependency_tree_sha256"`
	DependencyFileCount  int      `json:"dependency_file_count"`
	NodeVersion          string   `json:"node_version"`
	NodeSHA256           string   `json:"node_sha256"`
	CLIVersion           string   `json:"cli_version"`
	CLISHA256            string   `json:"cli_sha256"`
	SDKVersion           string   `json:"sdk_version"`
	ModelCount           int      `json:"model_count"`
	ModelIDs             []string `json:"model_ids"`
}

type certificationInputs struct {
	runtimeRoot string
	cliPath     string
	expectPath  string
	outPath     string
}

var errCertificationUnset = errors.New("certification environment is unset")

// certificationVerifyInputs separates the verify gate from candidate
// recording. An ordinary checkout skips only when all three are empty; a
// partially configured verify gate fails closed, including a missing EXPECT.
func certificationVerifyInputs(runtimeRoot, cliPath, expectPath string) (certificationInputs, error) {
	runtimeRoot, cliPath, expectPath = strings.TrimSpace(runtimeRoot), strings.TrimSpace(cliPath), strings.TrimSpace(expectPath)
	if runtimeRoot == "" && cliPath == "" && expectPath == "" {
		return certificationInputs{}, errCertificationUnset
	}
	if runtimeRoot == "" || cliPath == "" || expectPath == "" {
		return certificationInputs{}, errors.New("claudeagentmodel: verify requires AIP_CLAUDE_MODEL_CERT_RUNTIME, AIP_CLAUDE_MODEL_CERT_CLI and AIP_CLAUDE_MODEL_CERT_EXPECT together")
	}
	for name, value := range map[string]string{"AIP_CLAUDE_MODEL_CERT_RUNTIME": runtimeRoot, "AIP_CLAUDE_MODEL_CERT_CLI": cliPath, "AIP_CLAUDE_MODEL_CERT_EXPECT": expectPath} {
		if !filepath.IsAbs(value) {
			return certificationInputs{}, fmt.Errorf("claudeagentmodel: %s must be absolute", name)
		}
	}
	return certificationInputs{runtimeRoot: runtimeRoot, cliPath: cliPath, expectPath: expectPath}, nil
}

// certificationRecordInputs is the candidate-recording path. It deliberately
// does not accept EXPECT and never claims approval.
func certificationRecordInputs(runtimeRoot, cliPath, outPath string) (certificationInputs, error) {
	runtimeRoot, cliPath, outPath = strings.TrimSpace(runtimeRoot), strings.TrimSpace(cliPath), strings.TrimSpace(outPath)
	if runtimeRoot == "" || cliPath == "" || outPath == "" {
		return certificationInputs{}, errors.New("claudeagentmodel: record requires AIP_CLAUDE_MODEL_CERT_RUNTIME, AIP_CLAUDE_MODEL_CERT_CLI and AIP_CLAUDE_MODEL_CERT_OUT")
	}
	for name, value := range map[string]string{"AIP_CLAUDE_MODEL_CERT_RUNTIME": runtimeRoot, "AIP_CLAUDE_MODEL_CERT_CLI": cliPath, "AIP_CLAUDE_MODEL_CERT_OUT": outPath} {
		if !filepath.IsAbs(value) {
			return certificationInputs{}, fmt.Errorf("claudeagentmodel: %s must be absolute", name)
		}
	}
	return certificationInputs{runtimeRoot: runtimeRoot, cliPath: cliPath, outPath: outPath}, nil
}

// TestCertification_RecordInstalledBundle records one candidate identity. It
// is an evidence-producing operation, not an approval: only the verify gate
// compares the candidate with a committed approved identity.
func TestCertification_RecordInstalledBundle(t *testing.T) {
	if os.Getenv("AIP_CLAUDE_MODEL_CERT_RECORD") != "1" {
		t.Skip("AIP_CLAUDE_MODEL_CERT_RECORD=1 is required for candidate recording")
	}
	inputs, err := certificationRecordInputs(
		os.Getenv("AIP_CLAUDE_MODEL_CERT_RUNTIME"),
		os.Getenv("AIP_CLAUDE_MODEL_CERT_CLI"),
		os.Getenv("AIP_CLAUDE_MODEL_CERT_OUT"),
	)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := collectInstalledBundleIdentity(context.Background(), inputs.runtimeRoot, inputs.cliPath)
	if err != nil {
		t.Fatalf("record candidate identity: %v", err)
	}
	if err := writeInstalledIdentity(inputs.outPath, observed); err != nil {
		t.Fatal(err)
	}
	t.Logf("recorded candidate identity at %s; this is not an approval", inputs.outPath)
}

// TestCertification_InstalledBundle verifies the exact approved identity. It is
// the only gate that can claim the selected artifact matches the committed
// approved candidate.
func TestCertification_InstalledBundle(t *testing.T) {
	inputs, err := certificationVerifyInputs(
		os.Getenv("AIP_CLAUDE_MODEL_CERT_RUNTIME"),
		os.Getenv("AIP_CLAUDE_MODEL_CERT_CLI"),
		os.Getenv("AIP_CLAUDE_MODEL_CERT_EXPECT"),
	)
	if errors.Is(err, errCertificationUnset) {
		t.Skip("AIP_CLAUDE_MODEL_CERT_RUNTIME, AIP_CLAUDE_MODEL_CERT_CLI and AIP_CLAUDE_MODEL_CERT_EXPECT are unset; ordinary repositories skip exact-artifact certification")
	}
	if err != nil {
		t.Fatal(err)
	}
	approved, err := readApprovedIdentity(inputs.expectPath)
	if err != nil {
		t.Fatalf("read approved identity: %v", err)
	}
	observed, err := collectInstalledBundleIdentity(context.Background(), inputs.runtimeRoot, inputs.cliPath)
	if err != nil {
		t.Fatalf("collect installed identity: %v", err)
	}
	if err := compareInstalledBundleIdentity(approved, observed); err != nil {
		t.Fatal(err)
	}
	if out := strings.TrimSpace(os.Getenv("AIP_CLAUDE_MODEL_CERT_OUT")); out != "" {
		if !filepath.IsAbs(out) {
			t.Fatal("AIP_CLAUDE_MODEL_CERT_OUT must be absolute")
		}
		if err := writeInstalledIdentity(out, observed); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("approved installed-bundle identity verified: %s/%s %d models", observed.OS, observed.Arch, observed.ModelCount)
}

func readApprovedIdentity(path string) (installedBundleIdentity, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return installedBundleIdentity{}, err
	}
	var identity installedBundleIdentity
	if err := json.Unmarshal(body, &identity); err != nil {
		return installedBundleIdentity{}, fmt.Errorf("approved identity is invalid JSON: %w", err)
	}
	if identity.OS != "linux" || identity.Arch != "amd64" {
		return installedBundleIdentity{}, fmt.Errorf("approved identity platform=%s/%s; only linux/amd64 is certified", identity.OS, identity.Arch)
	}
	for name, value := range map[string]string{
		"go_binary_sha256": identity.GoBinarySHA256, "manifest_sha256": identity.ManifestSHA256,
		"sidecar_bridge_sha256": identity.BridgeSHA256, "sidecar_session_sha256": identity.SessionSHA256,
		"sidecar_tool_server_sha256": identity.ToolServerSHA256, "sidecar_package_json_sha256": identity.PackageJSONSHA256,
		"sidecar_package_lock_sha256": identity.PackageLockSHA256, "dependency_tree_sha256": identity.DependencyTreeSHA256,
		"node_version": identity.NodeVersion, "node_sha256": identity.NodeSHA256,
		"cli_version": identity.CLIVersion, "cli_sha256": identity.CLISHA256, "sdk_version": identity.SDKVersion,
	} {
		if strings.TrimSpace(value) == "" {
			return installedBundleIdentity{}, fmt.Errorf("approved identity is missing %s", name)
		}
	}
	if identity.DependencyFileCount < minInstalledDependencyFiles {
		return installedBundleIdentity{}, fmt.Errorf("approved dependency_file_count=%d is below the approved closure size", identity.DependencyFileCount)
	}
	// Model IDs are account/runtime data, not approved artifact identity. They
	// are deliberately not required or validated here.
	return identity, nil
}

func writeInstalledIdentity(path string, identity installedBundleIdentity) error {
	body, err := json.MarshalIndent(identity, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func collectInstalledBundleIdentity(ctx context.Context, runtimeRoot, cliPath string) (installedBundleIdentity, error) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return installedBundleIdentity{}, fmt.Errorf("the claudeagentmodel manifest certifies Linux/amd64 only; this host is %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	binary := filepath.Join(runtimeRoot, "bin", "aip-backend-claudeagentmodel")
	if info, err := os.Stat(binary); err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return installedBundleIdentity{}, fmt.Errorf("installed connector binary is missing or not executable: %s", binary)
	}
	binarySHA, err := fileSHA256(binary)
	if err != nil {
		return installedBundleIdentity{}, err
	}
	manifestPath := filepath.Join(runtimeRoot, "plugin.backendplugin.json")
	rawManifest, err := os.ReadFile(manifestPath)
	if err != nil {
		return installedBundleIdentity{}, err
	}
	var manifest releaseParityManifest
	if err := json.Unmarshal(rawManifest, &manifest); err != nil {
		return installedBundleIdentity{}, fmt.Errorf("installed manifest is invalid JSON: %w", err)
	}
	if manifest.PluginID != PluginID || manifest.Version != ConnectorVersion || manifest.Executable != "bin/aip-backend-claudeagentmodel" {
		return installedBundleIdentity{}, fmt.Errorf("installed manifest identity=%s/%s/%s", manifest.PluginID, manifest.Version, manifest.Executable)
	}
	if manifest.SHA256 != binarySHA {
		return installedBundleIdentity{}, fmt.Errorf("installed binary digest=%s manifest expects=%s", binarySHA, manifest.SHA256)
	}
	if manifest.ProtocolMajor != 1 || manifest.ProtocolMaxMinor != descriptorMinor {
		return installedBundleIdentity{}, fmt.Errorf("installed manifest protocol=%d/%d", manifest.ProtocolMajor, manifest.ProtocolMaxMinor)
	}
	if len(manifest.Platforms) != 1 || manifest.Platforms[0].OS != "linux" || manifest.Platforms[0].Arch != "amd64" {
		return installedBundleIdentity{}, fmt.Errorf("installed manifest platforms=%+v", manifest.Platforms)
	}
	if len(manifest.Exports) != 1 {
		return installedBundleIdentity{}, fmt.Errorf("installed manifest exports=%+v", manifest.Exports)
	}
	export := manifest.Exports[0]
	if export.Kind != FactoryKind || !export.Experimental || export.CredentialMode != "oauth_user" ||
		export.AccessScope != "local_only" || export.ProcessSharing != "per_instance" || export.ExecutionClass != "agent_runtime" {
		return installedBundleIdentity{}, fmt.Errorf("installed manifest export posture=%+v", export)
	}
	manifestSHA, err := fileSHA256(manifestPath)
	if err != nil {
		return installedBundleIdentity{}, err
	}

	sidecarDir := filepath.Join(runtimeRoot, "sidecar")
	hashes := make(map[string]string, 6)
	for _, name := range []string{"bridge.mjs", "session.mjs", "tool-server.mjs", "package.json", "package-lock.json"} {
		path := filepath.Join(sidecarDir, name)
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return installedBundleIdentity{}, fmt.Errorf("installed sidecar companion %q is missing or empty", name)
		}
		digest, err := fileSHA256(path)
		if err != nil {
			return installedBundleIdentity{}, err
		}
		hashes[name] = digest
	}
	sdkVersion, err := checkInstalledSidecarClosure(sidecarDir)
	if err != nil {
		return installedBundleIdentity{}, err
	}
	dependencyTree, dependencyFiles, err := dependencyTreeSHA256(filepath.Join(sidecarDir, "node_modules"))
	if err != nil {
		return installedBundleIdentity{}, fmt.Errorf("installed dependency tree is missing or unreadable; run npm ci in the packaged sidecar: %w", err)
	}
	if dependencyFiles < minInstalledDependencyFiles {
		return installedBundleIdentity{}, fmt.Errorf("installed dependency tree has %d files; an empty or incomplete closure is not certifiable", dependencyFiles)
	}

	location, err := resolveSidecarBeside(binary)
	if err != nil {
		return installedBundleIdentity{}, fmt.Errorf("production bundle resolver refused the installed layout: %w", err)
	}
	identity, err := probeRuntime(ctx, Config{Executable: cliPath}, location)
	if err != nil {
		return installedBundleIdentity{}, fmt.Errorf("production runtime probe refused the installation: %w", err)
	}
	nodeSHA, err := fileSHA256(location.NodePath)
	if err != nil {
		return installedBundleIdentity{}, err
	}
	cliSHA, err := fileSHA256(cliPath)
	if err != nil {
		return installedBundleIdentity{}, err
	}
	home := strings.TrimSpace(os.Getenv("HOME"))
	if home == "" {
		home = os.TempDir()
	}
	env, err := buildEnv([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}, "", "host_login")
	if err != nil {
		return installedBundleIdentity{}, err
	}
	workspace, err := os.MkdirTemp("", "claudeagentmodel-cert-*")
	if err != nil {
		return installedBundleIdentity{}, err
	}
	defer func() { _ = os.RemoveAll(workspace) }()

	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	proc, err := bridge.Start(ctx, bridge.Options{
		Node:            location.NodePath,
		Script:          location.Script,
		Env:             env,
		Dir:             workspace,
		ShutdownTimeout: 2 * time.Second,
	})
	if err != nil {
		return installedBundleIdentity{}, fmt.Errorf("installed sidecar did not start: %w", err)
	}
	hello, err := proc.AwaitHello(ctx)
	_ = proc.Close()
	if err != nil {
		return installedBundleIdentity{}, fmt.Errorf("installed sidecar did not greet with identity: %w", err)
	}
	if hello.Protocol != bridge.ProtocolVersion || hello.NodeVersion == "" || hello.SDKVersion != sdkVersion {
		return installedBundleIdentity{}, fmt.Errorf("installed sidecar hello=%+v", hello)
	}
	modelIDs, err := verifyInstalledBinary(ctx, binary, cliPath, workspace, env)
	if err != nil {
		return installedBundleIdentity{}, err
	}
	return installedBundleIdentity{
		OS:                   runtime.GOOS,
		Arch:                 runtime.GOARCH,
		GoBinarySHA256:       binarySHA,
		ManifestSHA256:       manifestSHA,
		BridgeSHA256:         hashes["bridge.mjs"],
		SessionSHA256:        hashes["session.mjs"],
		ToolServerSHA256:     hashes["tool-server.mjs"],
		PackageJSONSHA256:    hashes["package.json"],
		PackageLockSHA256:    hashes["package-lock.json"],
		DependencyTreeSHA256: dependencyTree,
		DependencyFileCount:  dependencyFiles,
		NodeVersion:          identity.NodeVersion,
		NodeSHA256:           nodeSHA,
		CLIVersion:           identity.CLIVersion,
		CLISHA256:            cliSHA,
		SDKVersion:           sdkVersion,
		ModelCount:           len(modelIDs),
		ModelIDs:             modelIDs,
	}, nil
}

// verifyInstalledBinary launches the selected Go executable and drives the real
// gRPC ABI through Configure and runtime discovery. It is intentionally not a
// path-only check: a non-connector executable must fail here.
func verifyInstalledBinary(ctx context.Context, binary, cliPath, workspace string, env []string) ([]string, error) {
	conn, client, stop, err := launchInstalledConnector(ctx, binary, env)
	if err != nil {
		return nil, fmt.Errorf("installed Go executable did not start: %w", err)
	}
	defer stop()
	defer func() { _ = conn.Close() }()

	neg, err := client.Negotiate(ctx, &backendpluginv1.NegotiateRequest{HostMajor: 1, DisableTransportRetries: true})
	if err != nil || !neg.GetCompatible() || neg.GetNegotiationToken() == "" {
		return nil, fmt.Errorf("installed Go executable negotiation: %v, %v", neg, err)
	}
	desc, err := client.Describe(ctx, &backendpluginv1.DescribeRequest{})
	if err != nil {
		return nil, fmt.Errorf("installed Go executable Describe: %w", err)
	}
	descriptor := desc.GetDescriptor_()
	if descriptor.GetPluginId() != PluginID || descriptor.GetVersion() != ConnectorVersion || len(descriptor.GetFactories()) != 1 {
		return nil, fmt.Errorf("installed Go executable descriptor identity=%+v", descriptor)
	}
	factory := descriptor.GetFactories()[0]
	if factory.GetKind() != FactoryKind || factory.GetAccessScope() != backendpluginv1.AccessScope_ACCESS_SCOPE_LOCAL_ONLY ||
		factory.GetCredentialMode() != backendpluginv1.CredentialMode_CREDENTIAL_MODE_OAUTH_USER ||
		factory.GetProcessSharing() != backendpluginv1.ProcessSharing_PROCESS_SHARING_PER_INSTANCE || !factory.GetExperimental() ||
		!factory.GetSupportsDynamicInventory() || !factory.GetStaticCapabilities().GetStreaming() || !factory.GetStaticCapabilities().GetTools() ||
		!factory.GetStaticCapabilities().GetOrderedItems() {
		return nil, fmt.Errorf("installed Go executable factory posture=%+v", factory)
	}
	config := fmt.Sprintf("executable: %q\nadmin_policy: honor\ndefault_workspace: %q\n", cliPath, workspace)
	configured, err := client.Configure(ctx, &backendpluginv1.ConfigureRequest{
		InstanceId:       "certified",
		FactoryKind:      FactoryKind,
		NegotiationToken: neg.GetNegotiationToken(),
		RuntimePolicy:    &backendpluginv1.RuntimePolicy{DisableTransportRetries: true},
		ConfigYaml:       []byte(config),
	})
	if err != nil || configured == nil {
		return nil, fmt.Errorf("installed Go executable Configure: %v", err)
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer closeCancel()
		_, _ = client.CloseInstance(closeCtx, &backendpluginv1.CloseInstanceRequest{InstanceId: "certified"})
	}()
	models, err := client.ListModels(ctx, &backendpluginv1.ListModelsRequest{InstanceId: "certified"})
	if err != nil {
		return nil, fmt.Errorf("installed Go executable ListModels: %w", err)
	}
	if models.GetErrorCode() != "" || len(models.GetModels()) == 0 {
		return nil, fmt.Errorf("installed Go executable discovery=%s models=%d", models.GetErrorCode(), len(models.GetModels()))
	}
	ids := make([]string, 0, len(models.GetModels()))
	for _, model := range models.GetModels() {
		id := strings.TrimSpace(model.GetNativeModelId())
		if id == "" {
			return nil, errors.New("installed Go executable returned an empty native model id")
		}
		ids = append(ids, id)
	}
	if !uniqueModelIDs(ids) {
		return nil, errors.New("installed Go executable returned duplicate model ids")
	}
	return ids, nil
}

func launchInstalledConnector(ctx context.Context, binary string, env []string) (*grpc.ClientConn, backendpluginv1.BackendPluginClient, func(), error) {
	cmd := exec.CommandContext(ctx, binary)
	cmd.Env = append([]string(nil), env...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, nil, err
	}
	stop := func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stderr)
		if scanner.Scan() {
			ready <- scanner.Text()
		} else {
			ready <- ""
		}
	}()
	select {
	case line := <-ready:
		_, address, ok := strings.Cut(line, " listening on ")
		if !ok || strings.TrimSpace(address) == "" {
			stop()
			return nil, nil, nil, fmt.Errorf("installed executable did not report a gRPC listen address")
		}
		conn, err := grpc.NewClient(strings.TrimSpace(address), grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			stop()
			return nil, nil, nil, err
		}
		return conn, backendpluginv1.NewBackendPluginClient(conn), stop, nil
	case <-ctx.Done():
		stop()
		return nil, nil, nil, ctx.Err()
	}
}

func compareInstalledBundleIdentity(want, got installedBundleIdentity) error {
	fields := []struct {
		name string
		want string
		got  string
	}{
		{"os", want.OS, got.OS},
		{"arch", want.Arch, got.Arch},
		{"go_binary_sha256", want.GoBinarySHA256, got.GoBinarySHA256},
		{"manifest_sha256", want.ManifestSHA256, got.ManifestSHA256},
		{"sidecar_bridge_sha256", want.BridgeSHA256, got.BridgeSHA256},
		{"sidecar_session_sha256", want.SessionSHA256, got.SessionSHA256},
		{"sidecar_tool_server_sha256", want.ToolServerSHA256, got.ToolServerSHA256},
		{"sidecar_package_json_sha256", want.PackageJSONSHA256, got.PackageJSONSHA256},
		{"sidecar_package_lock_sha256", want.PackageLockSHA256, got.PackageLockSHA256},
		{"dependency_tree_sha256", want.DependencyTreeSHA256, got.DependencyTreeSHA256},
		{"node_version", want.NodeVersion, got.NodeVersion},
		{"node_sha256", want.NodeSHA256, got.NodeSHA256},
		{"cli_version", want.CLIVersion, got.CLIVersion},
		{"cli_sha256", want.CLISHA256, got.CLISHA256},
		{"sdk_version", want.SDKVersion, got.SDKVersion},
	}
	for _, field := range fields {
		if field.want != field.got {
			return fmt.Errorf("approved identity mismatch: %s expected %q observed %q", field.name, field.want, field.got)
		}
	}
	if want.DependencyFileCount != got.DependencyFileCount {
		return fmt.Errorf("approved identity mismatch: dependency_file_count expected %d observed %d", want.DependencyFileCount, got.DependencyFileCount)
	}
	// The discovered model catalogue is account/runtime data. It must be a
	// coherent, non-empty observed set, but it is never compared with the
	// approved artifact identity and may change without artifact drift.
	if len(got.ModelIDs) == 0 {
		return errors.New("observed model_ids is empty")
	}
	if got.ModelCount != len(got.ModelIDs) {
		return fmt.Errorf("observed model_count=%d does not match %d model_ids", got.ModelCount, len(got.ModelIDs))
	}
	if !uniqueModelIDs(got.ModelIDs) {
		return errors.New("observed model_ids contains duplicates")
	}
	return nil
}

func uniqueModelIDs(ids []string) bool {
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func checkInstalledSidecarClosure(sidecarDir string) (string, error) {
	if err := checkInstalledPackageIdentity(sidecarDir); err != nil {
		return "", err
	}
	sdkDir := filepath.Join(sidecarDir, "node_modules", "@anthropic-ai", "claude-agent-sdk")
	raw, err := os.ReadFile(filepath.Join(sdkDir, "package.json"))
	if err != nil {
		return "", fmt.Errorf("installed Agent SDK package is missing: %w", err)
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return "", fmt.Errorf("installed Agent SDK package is invalid: %w", err)
	}
	if pkg.Version != pinnedSDKVersion {
		return "", fmt.Errorf("installed Agent SDK version=%s want=%s", pkg.Version, pinnedSDKVersion)
	}
	info, err := os.Stat(filepath.Join(sdkDir, "sdk.mjs"))
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return "", errors.New("installed Agent SDK entrypoint is missing")
	}
	return pkg.Version, nil
}

func checkInstalledPackageIdentity(sidecarDir string) error {
	rawPackage, err := os.ReadFile(filepath.Join(sidecarDir, "package.json"))
	if err != nil {
		return err
	}
	var pkg struct {
		Version      string            `json:"version"`
		Dependencies map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal(rawPackage, &pkg); err != nil {
		return fmt.Errorf("packaged sidecar package.json is invalid: %w", err)
	}
	if pkg.Version != ConnectorVersion || pkg.Dependencies["@anthropic-ai/claude-agent-sdk"] != pinnedSDKVersion {
		return fmt.Errorf("packaged sidecar package identity drifted from the pinned connector")
	}
	rawLock, err := os.ReadFile(filepath.Join(sidecarDir, "package-lock.json"))
	if err != nil {
		return err
	}
	var lock struct {
		Packages map[string]struct {
			Version   string `json:"version"`
			Integrity string `json:"integrity"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(rawLock, &lock); err != nil {
		return fmt.Errorf("packaged sidecar lock is invalid: %w", err)
	}
	sdk, ok := lock.Packages["node_modules/@anthropic-ai/claude-agent-sdk"]
	if !ok || sdk.Version != pinnedSDKVersion || sdk.Integrity == "" {
		return fmt.Errorf("packaged sidecar lock does not pin the certified Agent SDK")
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, readErr := io.Copy(h, file)
	closeErr := file.Close()
	if readErr != nil {
		return "", fmt.Errorf("hash %s: %w", path, readErr)
	}
	if closeErr != nil {
		return "", fmt.Errorf("hash %s: %w", path, closeErr)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// dependencyTreeSHA256 is the installed npm closure identity: lexical depth-first
// order, each relative forward-slash path, NUL, file SHA-256 and newline. The
// root .package-lock.json bookkeeping file and .bin links are excluded because
// they differ between npm majors for an identical package tree. An empty tree is
// an incomplete installation, not a valid digest.
func dependencyTreeSHA256(root string) (string, int, error) {
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return "", 0, fmt.Errorf("node_modules is required")
	}
	h := sha256.New()
	files := 0
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == ".package-lock.json" {
			return nil
		}
		if entry.IsDir() {
			if entry.Name() == ".bin" {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("unsupported installed dependency entry %q", rel)
		}
		digest, err := fileSHA256(path)
		if err != nil {
			return err
		}
		files++
		_, _ = fmt.Fprintf(h, "%s\x00%s\n", filepath.ToSlash(rel), digest)
		return nil
	})
	if err != nil {
		return "", 0, err
	}
	if files == 0 {
		return "", 0, errors.New("installed dependency tree is empty")
	}
	return hex.EncodeToString(h.Sum(nil)), files, nil
}
