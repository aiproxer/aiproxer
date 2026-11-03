//go:build integration

package tools_test

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/manifest"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// TestPackage_ClaudeAgentModelArchiveDeployment packages the closed connector,
// installs the complete archive into a fresh root, launches the installed binary
// over the real gRPC ABI, and proves the packaged manifest, descriptor, sidecar
// bundle and local-only policy agree. It also proves a rollback that removes this
// optional connector leaves the ACP connector intact.
func TestPackage_ClaudeAgentModelArchiveDeployment(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("the claudeagentmodel manifest certifies Linux/amd64 only")
	}
	root, dest, deployed := repoRoot(t), t.TempDir(), t.TempDir()
	runTool(t, root, "./tools/backendplugin/package_plugins", "-root", root, "-profile", "full", "-dest", dest, "-select", "claudeagentmodel,claudeagentacp")
	archive := filepath.Join(t.TempDir(), "plugins.tar.gz")
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Minute)
	defer cancel()
	for _, args := range [][]string{{"-czf", archive, "-C", dest, "."}, {"-xzf", archive, "-C", deployed}} {
		if out, err := exec.CommandContext(ctx, "tar", args...).CombinedOutput(); err != nil {
			t.Fatalf("archive: %v\n%s", err, out)
		}
	}

	pluginDir := filepath.Join(deployed, "claudeagentmodel")
	binary := filepath.Join(pluginDir, "bin", "aip-backend-claudeagentmodel")
	for _, rel := range []string{
		"bin/aip-backend-claudeagentmodel",
		"plugin.backendplugin.json",
		"sidecar/bridge.mjs",
		"sidecar/session.mjs",
		"sidecar/tool-server.mjs",
		"sidecar/package.json",
		"sidecar/package-lock.json",
	} {
		info, err := os.Stat(filepath.Join(pluginDir, rel))
		if err != nil || !info.Mode().IsRegular() {
			t.Fatalf("installed artifact %q is missing: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(pluginDir, "sidecar", "node_modules")); !os.IsNotExist(err) {
		t.Fatal("packaging must not ship an operator-installed node_modules tree")
	}

	rawManifest, err := os.ReadFile(filepath.Join(pluginDir, "plugin.backendplugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	packaged, err := manifest.ParseStrictBytes(rawManifest)
	if err != nil {
		t.Fatal(err)
	}
	binaryDigest := fileDigest(t, binary)
	manifestDigest := fileDigest(t, filepath.Join(pluginDir, "plugin.backendplugin.json"))
	index := readIndex(t, dest)
	plugins, _ := index["plugins"].([]any)
	var indexed map[string]any
	for _, raw := range plugins {
		entry, _ := raw.(map[string]any)
		if entry["path"] == "claudeagentmodel" {
			indexed = entry
		}
	}
	if indexed == nil || indexed["digest"] != binaryDigest || indexed["manifest_digest"] != manifestDigest {
		t.Fatalf("package index does not bind the installed bytes: %+v", indexed)
	}

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
	pluginEnv := []string{"PATH=" + nodeDir + ":" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "TMPDIR=" + os.TempDir()}
	conn, client, stop := launchPackagedPlugin(t, ctx, binary, pluginEnv)
	defer stop()
	defer func() { _ = conn.Close() }()

	neg, err := client.Negotiate(ctx, &backendpluginv1.NegotiateRequest{HostMajor: 1, DisableTransportRetries: true})
	if err != nil || !neg.GetCompatible() || neg.GetNegotiationToken() == "" {
		t.Fatalf("deployed negotiation: %v, %v", neg, err)
	}
	desc, err := client.Describe(ctx, &backendpluginv1.DescribeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if desc.GetDescriptor_().GetPluginId() != "com.aiproxer.backend.claudeagentmodel" || desc.GetDescriptor_().GetVersion() != packaged.Version {
		t.Fatalf("packaged descriptor identity: %+v / %+v", desc.GetDescriptor_(), packaged)
	}
	factories := desc.GetDescriptor_().GetFactories()
	if len(factories) != 1 {
		t.Fatalf("deployed factories: %v", factories)
	}
	factory := factories[0]
	if factory.GetKind() != "claudeagentmodel" || factory.GetAccessScope() != backendpluginv1.AccessScope_ACCESS_SCOPE_LOCAL_ONLY ||
		factory.GetCredentialMode() != backendpluginv1.CredentialMode_CREDENTIAL_MODE_OAUTH_USER ||
		factory.GetProcessSharing() != backendpluginv1.ProcessSharing_PROCESS_SHARING_PER_INSTANCE || !factory.GetExperimental() ||
		!factory.GetSupportsDynamicInventory() || !factory.GetStaticCapabilities().GetStreaming() || !factory.GetStaticCapabilities().GetTools() ||
		!factory.GetStaticCapabilities().GetOrderedItems() || factory.GetStaticCapabilities().GetItemReferences() ||
		factory.GetStaticCapabilities().GetParallelToolCalls() || factory.GetStaticCapabilities().GetStructuredOutputs() ||
		factory.GetStaticCapabilities().GetVision() || factory.GetStaticCapabilities().GetDocuments() ||
		factory.GetStaticCapabilities().GetReasoningReplay() {
		t.Fatalf("deployed factory posture: %+v", factory)
	}
	if standardplugins.HostMultiUserBackendPolicy().IsApproved(factory.GetKind()) {
		t.Fatal("the host multi-user policy must deny this local personal connector")
	}
	if len(packaged.Exports) != 1 {
		t.Fatalf("packaged export: %+v", packaged.Exports)
	}
	export := packaged.Exports[0]
	if export.Kind != factory.GetKind() || !export.Experimental || export.CredentialMode != "oauth_user" ||
		export.AccessScope != "local_only" || export.ProcessSharing != "per_instance" || export.ExecutionClass != "agent_runtime" {
		t.Fatalf("packaged/runtime posture mismatch: %+v / %v", export, factory)
	}
	if packaged.ProtocolMajor != desc.GetDescriptor_().GetProtocolMajor() || packaged.ProtocolMaxMinor != desc.GetDescriptor_().GetProtocolMinor() {
		t.Fatalf("packaged/runtime protocol mismatch: %+v / %+v", packaged, desc.GetDescriptor_())
	}
	if len(packaged.Platforms) != 1 || packaged.Platforms[0].OS != "linux" || packaged.Platforms[0].Arch != "amd64" || packaged.SHA256 != binaryDigest {
		t.Fatalf("packaged platform/digest: %+v", packaged)
	}

	// Configure exercises the installed binary's production resolveSidecar and
	// probeRuntime with a synthetic version-only CLI; no SDK query or account is
	// touched. A missing sidecar companion rejects before any process starts.
	cli := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(cli, []byte("#!/bin/sh\necho 2.1.311\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	config := fmt.Sprintf("executable: %q\nadmin_policy: honor\ndefault_workspace: %q\n", cli, workspace)
	configure := func(instance string) error {
		instanceNeg, err := client.Negotiate(ctx, &backendpluginv1.NegotiateRequest{HostMajor: 1, DisableTransportRetries: true})
		if err != nil || !instanceNeg.GetCompatible() {
			return fmt.Errorf("negotiate configured instance: %v, %v", instanceNeg, err)
		}
		cfg, err := client.Configure(ctx, &backendpluginv1.ConfigureRequest{
			InstanceId:       instance,
			FactoryKind:      "claudeagentmodel",
			NegotiationToken: instanceNeg.GetNegotiationToken(),
			RuntimePolicy:    &backendpluginv1.RuntimePolicy{DisableTransportRetries: true},
			ConfigYaml:       []byte(config),
		})
		if err == nil && cfg == nil {
			return fmt.Errorf("nil configured response")
		}
		return err
	}
	if err := configure("installed-1"); err != nil {
		t.Fatalf("installed Configure: %v", err)
	}
	if _, err := client.CloseInstance(ctx, &backendpluginv1.CloseInstanceRequest{InstanceId: "installed-1"}); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(pluginDir, "sidecar", "tool-server.mjs")
	original, err := os.ReadFile(missing)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(missing); err != nil {
		t.Fatal(err)
	}
	if err := configure("installed-missing-sidecar"); err == nil || !strings.Contains(err.Error(), "sidecar bundle is incomplete") {
		t.Fatalf("incomplete bundle Configure=%v", err)
	}
	if err := os.WriteFile(missing, original, 0o644); err != nil {
		t.Fatal(err)
	}

	// Rollback: removing the installed optional connector must not remove or
	// corrupt the independently packaged ACP connector.
	if err := os.RemoveAll(pluginDir); err != nil {
		t.Fatal(err)
	}
	acpDir := filepath.Join(deployed, "claudeagentacp")
	acpBinary := filepath.Join(acpDir, "bin", "aip-backend-claudeagentacp")
	if _, err := os.Stat(acpBinary); err != nil {
		t.Fatalf("ACP artifact disappeared with the optional connector: %v", err)
	}
	rawACP, err := os.ReadFile(filepath.Join(acpDir, "plugin.backendplugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	acpManifest, err := manifest.ParseStrictBytes(rawACP)
	if err != nil {
		t.Fatalf("ACP manifest after rollback: %v", err)
	}
	if acpManifest.PluginID != "com.aiproxer.backend.claudeagentacp" {
		t.Fatalf("ACP manifest identity after rollback: %+v", acpManifest)
	}
	acpConn, acpClient, acpStop := launchPackagedPlugin(t, ctx, acpBinary, pluginEnv)
	defer acpStop()
	defer func() { _ = acpConn.Close() }()
	acpNeg, err := acpClient.Negotiate(ctx, &backendpluginv1.NegotiateRequest{HostMajor: 1, DisableTransportRetries: true})
	if err != nil || !acpNeg.GetCompatible() {
		t.Fatalf("ACP negotiation after rollback: %v, %v", acpNeg, err)
	}
	acpDesc, err := acpClient.Describe(ctx, &backendpluginv1.DescribeRequest{})
	if err != nil || len(acpDesc.GetDescriptor_().GetFactories()) != 1 || acpDesc.GetDescriptor_().GetFactories()[0].GetKind() != "claudeagentacp" {
		t.Fatalf("ACP description after rollback: %v, %v", acpDesc, err)
	}
}

func launchPackagedPlugin(t *testing.T, ctx context.Context, binary string, env []string) (*grpc.ClientConn, backendpluginv1.BackendPluginClient, func()) {
	t.Helper()
	cmd := exec.CommandContext(ctx, binary)
	cmd.Env = env
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
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
	var address string
	select {
	case line := <-ready:
		_, address, _ = strings.Cut(line, " listening on ")
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal("packaged connector did not start")
	}
	if address == "" {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal("no packaged connector address")
	}
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal(err)
	}
	stop := func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
	return conn, backendpluginv1.NewBackendPluginClient(conn), stop
}
