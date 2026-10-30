package trust_test

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	sdkmanifest "github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/manifest"
)

//nolint:paralleltest // staging/handle substitution mutates shared artifact paths
func TestDigestHandleStagingSubstitutionRollback(t *testing.T) {
	root := t.TempDir()
	exeName := "bin/plugin"
	if runtime.GOOS == "windows" {
		exeName = "bin/plugin.exe"
	}
	exePath := filepath.Join(root, filepath.FromSlash(exeName))
	if err := os.MkdirAll(filepath.Dir(exePath), 0o700); err != nil {
		t.Fatal(err)
	}
	payload := nativeMagicPayload(runtime.GOOS)
	if err := os.WriteFile(exePath, payload, 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])
	m := testManifest(exeName, digest, runtime.GOOS, runtime.GOARCH)
	staging := t.TempDir()
	res := trust.Verify(root, m, trust.VerifyOptions{StagingDir: staging})
	if res.Reason != trust.ReasonOK || res.Artifact == nil {
		t.Fatalf("%+v", res)
	}
	if res.Artifact.DigestHex != digest {
		t.Fatal("digest")
	}
	if res.Artifact.OpenFile() == nil {
		t.Fatal("expected held launch handle")
	}
	staged := res.Artifact.StagedPath
	switch runtime.GOOS {
	case "windows", "darwin":
		if res.Artifact.Strategy != trust.BindingProtectedStaging || staged == "" {
			t.Fatalf("strategy=%s", res.Artifact.Strategy)
		}
	case "linux":
		if res.Artifact.Strategy != trust.BindingDescriptor {
			t.Fatalf("strategy=%s", res.Artifact.Strategy)
		}
	}
	if err := res.Artifact.Close(); err != nil {
		t.Fatal(err)
	}
	if staged != "" {
		if err := trust.CleanupStaged(staged); err != nil {
			t.Fatal(err)
		}
	}

	bad := m
	bad.SHA256 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	res2 := trust.Verify(root, bad, trust.VerifyOptions{StagingDir: t.TempDir()})
	if res2.Reason != trust.ReasonDigestMismatch {
		t.Fatalf("%v", res2.Reason)
	}

	esc := m
	esc.Executable = "../escape"
	res3 := trust.Verify(root, esc, trust.VerifyOptions{StagingDir: t.TempDir()})
	if res3.Reason != trust.ReasonPathEscape && res3.Reason != trust.ReasonOpenFailed {
		t.Fatalf("%v", res3.Reason)
	}

	if err := os.WriteFile(exePath, append(nativeMagicPayload(runtime.GOOS), []byte("tampered")...), 0o700); err != nil {
		t.Fatal(err)
	}
	res4 := trust.Verify(root, m, trust.VerifyOptions{StagingDir: t.TempDir()})
	if res4.Reason != trust.ReasonDigestMismatch {
		t.Fatalf("%v", res4.Reason)
	}
}

func TestUnsupportedPlatform(t *testing.T) {
	t.Parallel()
	m := sdkmanifest.Manifest{
		Executable: "bin/x", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Platforms: []sdkmanifest.Platform{{OS: "plan9", Arch: "amd64"}},
	}
	res := trust.Verify(t.TempDir(), m, trust.VerifyOptions{HostOS: "linux", HostArch: "amd64", StagingDir: t.TempDir()})
	if res.Reason != trust.ReasonUnsupportedPlatform {
		t.Fatalf("%v", res.Reason)
	}
}

func TestVerify_SymlinkRejected(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := t.TempDir()
	exeName := "bin/plugin"
	if runtime.GOOS == "windows" {
		exeName = "bin/plugin.exe"
	}
	realPath := filepath.Join(outside, filepath.Base(exeName))
	if err := os.WriteFile(realPath, nativeMagicPayload(runtime.GOOS), 0o700); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(root, filepath.FromSlash(exeName))
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	sum := sha256.Sum256(nativeMagicPayload(runtime.GOOS))
	m := testManifest(exeName, hex.EncodeToString(sum[:]), runtime.GOOS, runtime.GOARCH)
	res := trust.Verify(root, m, trust.VerifyOptions{StagingDir: t.TempDir()})
	if res.Reason != trust.ReasonSymlinkEscape && res.Reason != trust.ReasonOpenFailed && res.Reason != trust.ReasonNotRegular {
		t.Fatalf("want symlink/open reject, got %v (%v)", res.Reason, res.Err)
	}
}

func TestVerify_MagicAndScriptRejected(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cases := []struct {
		name string
		exe  string
		body []byte
	}{
		{name: "script-ext", exe: "bin/run.sh", body: []byte("#!/bin/sh\n")},
		{name: "no-magic", exe: map[string]string{"windows": "bin/plugin.exe", "linux": "bin/plugin", "darwin": "bin/plugin"}[runtime.GOOS], body: []byte("not-a-native-binary")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.exe == "" {
				t.Skip("unsupported GOOS")
			}
			path := filepath.Join(root, filepath.FromSlash(tc.exe))
			_ = os.Remove(path)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, tc.body, 0o700); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(tc.body)
			m := testManifest(tc.exe, hex.EncodeToString(sum[:]), runtime.GOOS, runtime.GOARCH)
			res := trust.Verify(root, m, trust.VerifyOptions{StagingDir: t.TempDir()})
			if res.Reason != trust.ReasonNotExecutableType {
				t.Fatalf("got %v", res.Reason)
			}
		})
	}
}

func TestVerify_SyntheticNativeMagicAccepted(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	exeName := "bin/plugin"
	if runtime.GOOS == "windows" {
		exeName = "bin/plugin.exe"
	}
	path := filepath.Join(root, filepath.FromSlash(exeName))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	payload := nativeMagicPayload(runtime.GOOS)
	if err := os.WriteFile(path, payload, 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	m := testManifest(exeName, hex.EncodeToString(sum[:]), runtime.GOOS, runtime.GOARCH)
	res := trust.Verify(root, m, trust.VerifyOptions{StagingDir: t.TempDir()})
	if res.Reason != trust.ReasonOK {
		t.Fatalf("%+v", res)
	}
	_ = res.Artifact.Close()
}

func testManifest(exe, digest, goos, goarch string) sdkmanifest.Manifest {
	return sdkmanifest.Manifest{
		Schema: sdkmanifest.SchemaV1, PluginID: "io.t", Version: "1", BuildID: "b",
		Executable: exe, SHA256: digest, ProtocolMajor: 1,
		Platforms: []sdkmanifest.Platform{{OS: goos, Arch: goarch}},
		Exports: []sdkmanifest.Export{{
			Kind: "t", CredentialMode: backendplugin.CredentialModeNone,
			AccessScope: backendplugin.AccessScopeLocalOnly, ProcessSharing: backendplugin.ProcessSharingPerInstance,
		}},
	}
}

func nativeMagicPayload(goos string) []byte {
	switch goos {
	case "windows":
		return []byte{'M', 'Z', 0x90, 0x00, 0x03, 0x00, 0x00, 0x00}
	case "linux":
		return []byte{0x7f, 'E', 'L', 'F', 2, 1, 1, 0}
	case "darwin":
		buf := make([]byte, 8)
		binary.LittleEndian.PutUint32(buf, 0xfeedfacf)
		return buf
	default:
		return []byte{0}
	}
}
