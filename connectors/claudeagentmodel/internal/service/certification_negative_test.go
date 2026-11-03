package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func approvedIdentityFixture() installedBundleIdentity {
	return installedBundleIdentity{
		OS:                   "linux",
		Arch:                 "amd64",
		GoBinarySHA256:       "binary",
		ManifestSHA256:       "manifest",
		BridgeSHA256:         "bridge",
		SessionSHA256:        "session",
		ToolServerSHA256:     "tool-server",
		PackageJSONSHA256:    "package-json",
		PackageLockSHA256:    "package-lock",
		DependencyTreeSHA256: "tree",
		DependencyFileCount:  106,
		NodeVersion:          "22.23.4",
		NodeSHA256:           "node",
		CLIVersion:           "2.1.311",
		CLISHA256:            "cli",
		SDKVersion:           "0.3.316",
	}
}

func TestCompareInstalledBundleIdentity_RejectsEveryIdentityFieldDrift(t *testing.T) {
	t.Parallel()
	want := approvedIdentityFixture()
	cases := []struct {
		field string
		edit  func(*installedBundleIdentity)
	}{
		{"os", func(g *installedBundleIdentity) { g.OS = "darwin" }},
		{"arch", func(g *installedBundleIdentity) { g.Arch = "arm64" }},
		{"go_binary_sha256", func(g *installedBundleIdentity) { g.GoBinarySHA256 = "drifted" }},
		{"manifest_sha256", func(g *installedBundleIdentity) { g.ManifestSHA256 = "drifted" }},
		{"sidecar_bridge_sha256", func(g *installedBundleIdentity) { g.BridgeSHA256 = "drifted" }},
		{"sidecar_session_sha256", func(g *installedBundleIdentity) { g.SessionSHA256 = "drifted" }},
		{"sidecar_tool_server_sha256", func(g *installedBundleIdentity) { g.ToolServerSHA256 = "drifted" }},
		{"sidecar_package_json_sha256", func(g *installedBundleIdentity) { g.PackageJSONSHA256 = "drifted" }},
		{"sidecar_package_lock_sha256", func(g *installedBundleIdentity) { g.PackageLockSHA256 = "drifted" }},
		{"dependency_tree_sha256", func(g *installedBundleIdentity) { g.DependencyTreeSHA256 = "drifted" }},
		{"dependency_file_count", func(g *installedBundleIdentity) { g.DependencyFileCount++ }},
		{"node_version", func(g *installedBundleIdentity) { g.NodeVersion = "18.13.0" }},
		{"node_sha256", func(g *installedBundleIdentity) { g.NodeSHA256 = "drifted" }},
		{"cli_version", func(g *installedBundleIdentity) { g.CLIVersion = "2.1.247" }},
		{"cli_sha256", func(g *installedBundleIdentity) { g.CLISHA256 = "drifted" }},
		{"sdk_version", func(g *installedBundleIdentity) { g.SDKVersion = "0.3.315" }},
	}
	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			got := want
			tc.edit(&got)
			err := compareInstalledBundleIdentity(want, got)
			if err == nil || !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("drift %s error=%v", tc.field, err)
			}
		})
	}
}

func TestCompareInstalledBundleIdentity_DynamicModelCatalogue(t *testing.T) {
	t.Parallel()
	want := approvedIdentityFixture()
	cases := []struct {
		name    string
		ids     []string
		count   int
		wantErr string
	}{
		{name: "different observed catalogue passes", ids: []string{"default", "opus", "haiku"}, count: 3},
		{name: "another changed catalogue passes", ids: []string{"default", "opus", "claude-fable-5-1[1m]", "sonnet", "haiku"}, count: 5},
		{name: "empty observed set fails", ids: nil, count: 0, wantErr: "empty"},
		{name: "duplicate observed set fails", ids: []string{"default", "default"}, count: 2, wantErr: "duplicate"},
		{name: "inconsistent observed count fails", ids: []string{"default"}, count: 2, wantErr: "model_count"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := want
			got.ModelIDs = tc.ids
			got.ModelCount = tc.count
			err := compareInstalledBundleIdentity(want, got)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("dynamic catalogue must pass: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err=%v want %q", err, tc.wantErr)
			}
		})
	}
}

func TestReadApprovedIdentity_CommittedOracleDoesNotPinModelCatalogue(t *testing.T) {
	t.Parallel()
	identity, err := readApprovedIdentity(filepath.Join(moduleRoot(t), "certification", "approved-local-candidate.json"))
	if err != nil {
		t.Fatal(err)
	}
	if identity.ModelCount != 0 || len(identity.ModelIDs) != 0 {
		t.Fatalf("approved oracle must not pin account/runtime model IDs: count=%d ids=%v", identity.ModelCount, identity.ModelIDs)
	}
}

func TestCertificationVerifyInputs_RecordCannotAuthorizeVerify(t *testing.T) {
	t.Parallel()
	if _, err := certificationVerifyInputs("", "", ""); !errors.Is(err, errCertificationUnset) {
		t.Fatalf("all-unset err=%v", err)
	}
	if _, err := certificationVerifyInputs("/runtime", "/cli", ""); err == nil || !strings.Contains(err.Error(), "AIP_CLAUDE_MODEL_CERT_EXPECT") {
		t.Fatalf("missing EXPECT err=%v", err)
	}
	out := filepath.Join(t.TempDir(), "candidate.json")
	if _, err := certificationRecordInputs("/runtime", "/cli", out); err != nil {
		t.Fatalf("record without EXPECT must be allowed: %v", err)
	}
	if err := writeInstalledIdentity(out, approvedIdentityFixture()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("record mode did not write a candidate: %v", err)
	}
	if _, err := certificationVerifyInputs("/runtime", "/cli", ""); err == nil {
		t.Fatal("a written candidate must not satisfy verify without an approved expected identity")
	}
	if _, err := certificationRecordInputs("/runtime", "/cli", ""); err == nil {
		t.Fatal("record without OUT must fail")
	}
	if _, err := certificationVerifyInputs("relative", "/cli", "/expect"); err == nil {
		t.Fatal("relative runtime must fail")
	}
}

func TestDependencyTree_RejectsEmptyAndReturnsFileCount(t *testing.T) {
	t.Parallel()
	if _, _, err := dependencyTreeSHA256(t.TempDir()); err == nil {
		t.Fatal("an empty dependency tree must be rejected")
	}
	root := t.TempDir()
	for _, rel := range []string{"one", "two", ".package-lock.json", filepath.Join(".bin", "ignored")} {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(rel), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	digest, files, err := dependencyTreeSHA256(root)
	if err != nil {
		t.Fatal(err)
	}
	if digest == "" || files != 2 {
		t.Fatalf("digest=%q files=%d", digest, files)
	}
}

func TestWriteInstalledIdentity_ForcesPrivateMode(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "observed.json")
	if err := os.WriteFile(path, []byte("old content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeInstalledIdentity(path, approvedIdentityFixture()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("observed identity mode=%o want=600", got)
	}
}

func TestInstalledSidecarClosure_RequiresPinnedSDK(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("package.json", `{"version":"0.1.0","dependencies":{"@anthropic-ai/claude-agent-sdk":"0.3.316"}}`)
	write("package-lock.json", `{"packages":{"":{"version":"0.1.0"},"node_modules/@anthropic-ai/claude-agent-sdk":{"version":"0.3.316","integrity":"sha512-x"}}}`)
	if _, err := checkInstalledSidecarClosure(dir); err == nil {
		t.Fatal("missing installed SDK package must fail")
	}
	write("node_modules/@anthropic-ai/claude-agent-sdk/package.json", `{"version":"0.3.315"}`)
	if _, err := checkInstalledSidecarClosure(dir); err == nil || !strings.Contains(err.Error(), "0.3.316") {
		t.Fatalf("wrong SDK version err=%v", err)
	}
	write("node_modules/@anthropic-ai/claude-agent-sdk/package.json", `{"version":"0.3.316"}`)
	if _, err := checkInstalledSidecarClosure(dir); err == nil {
		t.Fatal("missing SDK entrypoint must fail")
	}
	write("node_modules/@anthropic-ai/claude-agent-sdk/sdk.mjs", "export {}")
	if version, err := checkInstalledSidecarClosure(dir); err != nil || version != "0.3.316" {
		t.Fatalf("version=%q err=%v", version, err)
	}
}

func TestLaunchInstalledConnector_RejectsNonConnector(t *testing.T) {
	t.Parallel()
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skip("true executable is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, stop, err := launchInstalledConnector(ctx, truePath, []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()})
	if err == nil {
		stop()
		_ = conn.Close()
		t.Fatal("a non-connector executable must not certify")
	}
}
