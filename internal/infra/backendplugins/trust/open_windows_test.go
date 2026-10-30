//go:build windows

package trust_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	sdkmanifest "github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/manifest"
)

func TestVerify_WindowsTempPathAccepted(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	exeName := "bin/plugin.exe"
	exePath := filepath.Join(root, filepath.FromSlash(exeName))
	if err := os.MkdirAll(filepath.Dir(exePath), 0o700); err != nil {
		t.Fatal(err)
	}
	payload := []byte{'M', 'Z', 0x90, 0x00, 0x03, 0x00, 0x00, 0x00}
	if err := os.WriteFile(exePath, payload, 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	m := windowsTestManifest(exeName, hex.EncodeToString(sum[:]))
	res := trust.Verify(root, m, trust.VerifyOptions{StagingDir: t.TempDir()})
	if res.Reason != trust.ReasonOK || res.Artifact == nil {
		t.Fatalf("valid temp path rejected: %+v", res)
	}
	_ = res.Artifact.Close()
}

func TestVerify_WindowsParentJunctionEscapeRejected(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := t.TempDir()
	payload := []byte{'M', 'Z', 0x90, 0x00, 0x03, 0x00, 0x00, 0x00}
	outsideExe := filepath.Join(outside, "plugin.exe")
	if err := os.WriteFile(outsideExe, payload, 0o700); err != nil {
		t.Fatal(err)
	}
	junction := filepath.Join(root, "bin")
	if err := createJunction(t, junction, outside); err != nil {
		t.Skipf("junction unavailable: %v", err)
	}
	sum := sha256.Sum256(payload)
	m := windowsTestManifest("bin/plugin.exe", hex.EncodeToString(sum[:]))
	res := trust.Verify(root, m, trust.VerifyOptions{StagingDir: t.TempDir()})
	if res.Reason != trust.ReasonPathEscape && res.Reason != trust.ReasonSymlinkEscape && res.Reason != trust.ReasonOpenFailed {
		t.Fatalf("want junction escape reject, got %+v", res)
	}
}

func TestVerify_WindowsFinalComponentSymlinkRejected(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := t.TempDir()
	payload := []byte{'M', 'Z', 0x90, 0x00, 0x03, 0x00, 0x00, 0x00}
	realPath := filepath.Join(outside, "plugin.exe")
	if err := os.WriteFile(realPath, payload, 0o700); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(root, "bin", "plugin.exe")
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	sum := sha256.Sum256(payload)
	m := windowsTestManifest("bin/plugin.exe", hex.EncodeToString(sum[:]))
	res := trust.Verify(root, m, trust.VerifyOptions{StagingDir: t.TempDir()})
	if res.Reason != trust.ReasonSymlinkEscape && res.Reason != trust.ReasonOpenFailed && res.Reason != trust.ReasonNotRegular {
		t.Fatalf("want final-component symlink reject, got %+v", res)
	}
}

func windowsTestManifest(exe, digest string) sdkmanifest.Manifest {
	return sdkmanifest.Manifest{
		Schema: sdkmanifest.SchemaV1, PluginID: "io.t", Version: "1", BuildID: "b",
		Executable: exe, SHA256: digest, ProtocolMajor: 1,
		Platforms: []sdkmanifest.Platform{{OS: "windows", Arch: runtime.GOARCH}},
		Exports: []sdkmanifest.Export{{
			Kind: "t", CredentialMode: backendplugin.CredentialModeNone,
			AccessScope: backendplugin.AccessScopeLocalOnly, ProcessSharing: backendplugin.ProcessSharingPerInstance,
		}},
	}
}

func createJunction(t *testing.T, link, target string) error {
	t.Helper()
	// cmd mklink /J does not require elevated privileges for directories.
	cmd := exec.Command("cmd", "/c", "mklink", "/J", link, target)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return err
	}
	t.Logf("mklink: %s", string(out))
	return nil
}
