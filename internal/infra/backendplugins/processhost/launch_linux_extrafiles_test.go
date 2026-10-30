//go:build linux

package processhost_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	inframanifest "github.com/aiproxer/aiproxer/internal/infra/backendplugins/manifest"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
)

// TestLaunch_ExtraFilesDoesNotDisplaceExecutableFD proves channel ExtraFiles[0]
// (child FD 3) does not steal the /proc/self/fd exec target from the verified
// artifact descriptor (historical permission-denied on /proc/self/fd/3).
func TestLaunch_ExtraFilesDoesNotDisplaceExecutableFD(t *testing.T) {
	t.Parallel()
	root := stageExecFixture(t)
	raw, err := os.ReadFile(filepath.Join(root, "plugin.backendplugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := inframanifest.ParseStrictBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	res := trust.Verify(root, m, trust.VerifyOptions{})
	if res.Reason != trust.ReasonOK || res.Artifact == nil {
		t.Fatalf("verify: %+v", res)
	}
	t.Cleanup(func() { _ = res.Artifact.Close() })

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })

	proc, err := processhost.NewPlatformLauncher().Launch(context.Background(), processhost.LaunchSpec{
		Artifact:   res.Artifact,
		Generation: 1,
		Env:        []string{"PATH=/usr/bin:/bin"},
		ExtraFiles: []*os.File{w},
	})
	if err != nil {
		t.Fatalf("Launch with ExtraFiles: %v", err)
	}
	t.Cleanup(func() { _ = proc.Close() })
	if proc.PID() <= 0 {
		t.Fatal("expected live pid")
	}
}

// stageExecFixture stages a trivial pre-existing executable as a discovery
// root without invoking `go build` (a cold localstub connector build costs
// ~60s). The launcher execs the verified bytes through /proc/self/fd/<n>
// regardless of payload and the test never handshakes the child, so any
// executable preserves the same FD-displacement regression coverage.
func stageExecFixture(t *testing.T) string {
	t.Helper()
	return stageExecutable(t, firstExecutable(t, "/bin/true", "/usr/bin/true", "/bin/sh", "/usr/bin/sh"))
}

// stageExecutable stages the given pre-existing executable as a verified
// discovery root.
func stageExecutable(t *testing.T, src string) string {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	rel := "bin/aip-backend-execfixture"
	dst := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	body := fmt.Sprintf(`{
  "schema":"aiproxer.backendplugin.manifest/v1",
  "plugin_id":"com.aiproxer.backend.execfixture",
  "version":"0.1.0",
  "build_id":"test",
  "executable":%q,
  "sha256":%q,
  "protocol_major":1,
  "protocol_min_minor":0,
  "protocol_max_minor":0,
  "platforms":[{"os":%q,"arch":%q}],
  "exports":[{
    "kind":"exec-fixture",
    "credential_mode":"none",
    "access_scope":"any",
    "process_sharing":"per_instance"
  }]
}`, rel, hex.EncodeToString(sum[:]), runtime.GOOS, runtime.GOARCH)
	if err := os.WriteFile(filepath.Join(root, "plugin.backendplugin.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func firstExecutable(t *testing.T, candidates ...string) string {
	t.Helper()
	for _, c := range candidates {
		fi, err := os.Stat(c)
		if err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0 {
			return c
		}
	}
	t.Fatalf("no executable fixture found among %v", candidates)
	return ""
}
