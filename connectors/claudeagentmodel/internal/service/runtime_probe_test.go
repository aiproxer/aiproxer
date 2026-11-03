package service

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func versionScript(t *testing.T, version string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "version-probe")
	body := "#!/bin/sh\nprintf '%s\\n' " + version + "\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProbeRuntime_ExactIdentityMatrix(t *testing.T) {
	t.Parallel()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	goodCLI := versionScript(t, "2.1.311")
	failedCLI := filepath.Join(t.TempDir(), "failed-version-probe")
	if err := os.WriteFile(failedCLI, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	base := sidecarLocation{NodePath: node, Script: filepath.Join(t.TempDir(), "bridge.mjs"), SDKVersion: pinnedSDKVersion}

	identity, err := probeRuntime(context.Background(), Config{Executable: goodCLI}, base)
	if err != nil {
		t.Fatal(err)
	}
	if identity.NodeVersion == "" || identity.CLIVersion != "2.1.311" || identity.SDKVersion != pinnedSDKVersion {
		t.Fatalf("identity=%+v", identity)
	}

	cases := []struct {
		name    string
		cfg     Config
		loc     sidecarLocation
		wantErr string
	}{
		{name: "missing cli", cfg: Config{Executable: filepath.Join(t.TempDir(), "missing-claude")}, loc: base, wantErr: "executable must be"},
		{name: "failed cli identity is not an authentication diagnosis", cfg: Config{Executable: failedCLI}, loc: base, wantErr: "verify the executable and launch environment"},
		{name: "old cli", cfg: Config{Executable: versionScript(t, "2.1.247")}, loc: base, wantErr: "2.1.248"},
		{name: "unparseable cli", cfg: Config{Executable: versionScript(t, "not-a-version")}, loc: base, wantErr: "2.1.248"},
		{name: "sdk digest mismatch", cfg: Config{Executable: goodCLI}, loc: sidecarLocation{NodePath: node, Script: base.Script, SDKVersion: "0.0.1"}, wantErr: "SDK identity mismatch"},
		{name: "old node", cfg: Config{Executable: goodCLI}, loc: sidecarLocation{NodePath: versionScript(t, "18.13.0"), Script: base.Script, SDKVersion: pinnedSDKVersion}, wantErr: "Node 18.14"},
		{name: "unparseable node", cfg: Config{Executable: goodCLI}, loc: sidecarLocation{NodePath: versionScript(t, "not-a-version"), Script: base.Script, SDKVersion: pinnedSDKVersion}, wantErr: "Node 18.14"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := probeRuntime(context.Background(), tc.cfg, tc.loc)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err=%v want %q", err, tc.wantErr)
			}
		})
	}
}
