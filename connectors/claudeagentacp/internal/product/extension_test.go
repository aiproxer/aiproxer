package product

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/acp"
)

// The installed adapter requires Node >=22, not one release's exact patch.
// Restoring an exact-version gate rejects the supported-version cases below.
func TestNodeVersion_MinimumWithoutReleasePin(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		version string
		accept  bool
	}{
		{"22.0.0", true},
		{"22.23.4", true},
		{"22.23.5", true},
		{"24.0.0", true},
		{"26.1.0", true},
		{"21.9.0", false},
		{"", false},
		{"22.1", false},
		{"22.x.0", false},
		{"22.1.-1", false},
	} {
		t.Run(tc.version, func(t *testing.T) {
			if err := validateNodeVersion(tc.version); (err == nil) != tc.accept {
				t.Fatalf("Node %q: error=%v, want accepted=%v", tc.version, err, tc.accept)
			}
		})
	}
}

// Claim: the shipped three-file identity sidecar hashes to the pin the Go
// admission gate enforces, so a sidecar edit cannot ship without its
// re-certification.
// Source: extension.go certifiedSidecarSHA256 (the ResolveIdentityExtension gate).
// Mutants: change one shipped sidecar file, or leave the pin at an older
// certification.
func TestShippedSidecarMatchesCertifiedPin(t *testing.T) {
	shipped, err := filepath.Abs(filepath.Join("..", "..", "adapter-extension"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	sidecar := filepath.Join(root, "claudeagentacp-adapter-extension")
	if err := os.Mkdir(sidecar, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bootstrap.mjs", "identity.mjs", "models.mjs"} {
		source, err := os.ReadFile(filepath.Join(shipped, name))
		if err != nil {
			t.Fatalf("shipped sidecar %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(sidecar, name), source, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	profile := RuntimeProfile{
		Executable:     filepath.Join(root, "node_modules", "@agentclientprotocol", "claude-agent-acp", "dist", "index.js"),
		AdapterVersion: "0.89.1",
		SDKVersion:     "0.3.293",
		CLIVersion:     "2.1.293",
		BundleSHA256:   "installation-closure",
	}
	extension, err := inspectIdentityExtension(t.Context(), filepath.Join(root, "connector"), profile)
	if err != nil {
		t.Fatal(err)
	}
	if extension.ExtensionSHA256 != certifiedSidecarSHA256 {
		t.Fatalf("shipped sidecar digest = %s, pin = %s: re-record the pin only with fresh certification "+
			"evidence (make claude-gateway-certify) and update docs/claude-agent-acp-backend.md",
			extension.ExtensionSHA256, certifiedSidecarSHA256)
	}
}

func TestIdentityExtension_DeterministicSidecarAndClosureBinding(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "claudeagentacp")
	profile := RuntimeProfile{Executable: filepath.Join(dir, "node_modules/@agentclientprotocol/claude-agent-acp/dist/index.js"), BundleSHA256: "installation-closure", AdapterVersion: "0.89.1", SDKVersion: "0.3.293", CLIVersion: "2.1.293"}
	if _, err := inspectIdentityExtension(context.Background(), executable, profile); err == nil {
		t.Fatal("missing sidecar admitted")
	}
	sidecar := filepath.Join(dir, "claudeagentacp-adapter-extension")
	if err := os.Mkdir(sidecar, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bootstrap.mjs", "identity.mjs", "models.mjs"} {
		if err := os.WriteFile(filepath.Join(sidecar, name), []byte("project source"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	first, err := inspectIdentityExtension(t.Context(), executable, profile)
	if err != nil {
		t.Fatal(err)
	}
	if first.Bootstrap != filepath.Join(sidecar, "bootstrap.mjs") || first.AdapterRoot != filepath.Dir(filepath.Dir(profile.Executable)) {
		t.Fatalf("wrong sidecar resolution: %+v", first)
	}
	if err := os.WriteFile(filepath.Join(sidecar, "identity.mjs"), []byte("changed source"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := inspectIdentityExtension(t.Context(), executable, profile)
	if err != nil {
		t.Fatal(err)
	}
	if first.BundleSHA256 == second.BundleSHA256 {
		t.Fatal("extension bytes not bound")
	}
	profile.BundleSHA256 = "changed imported dependency"
	third, err := inspectIdentityExtension(t.Context(), executable, profile)
	if err != nil {
		t.Fatal(err)
	}
	if second.BundleSHA256 == third.BundleSHA256 {
		t.Fatal("loaded dependency closure not bound")
	}
	for _, name := range []string{"bootstrap.mjs", "identity.mjs", "models.mjs"} {
		t.Run(name, func(t *testing.T) {
			file := filepath.Join(sidecar, name)
			before, err := inspectIdentityExtension(t.Context(), executable, profile)
			if err != nil {
				t.Fatal(err)
			}
			original, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte("changed leaf"), 0o600); err != nil {
				t.Fatal(err)
			}
			after, err := inspectIdentityExtension(t.Context(), executable, profile)
			if err != nil || before.ExtensionSHA256 == after.ExtensionSHA256 || before.BundleSHA256 == after.BundleSHA256 {
				t.Errorf("changed leaf not bound: %v", err)
			}
			if err := os.Remove(file); err != nil {
				t.Fatal(err)
			}
			if _, err := inspectIdentityExtension(t.Context(), executable, profile); err == nil {
				t.Error("missing leaf admitted")
			}
			if err := os.Symlink(filepath.Join(sidecar, "absent"), file); err != nil {
				t.Fatal(err)
			}
			if _, err := inspectIdentityExtension(t.Context(), executable, profile); err == nil {
				t.Error("linked leaf admitted")
			}
			if err := os.Remove(file); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, original, 0o600); err != nil {
				t.Fatal(err)
			}
			for _, mode := range []os.FileMode{0o606, 0} {
				if err := os.Chmod(file, mode); err != nil {
					t.Fatal(err)
				}
				if _, err := inspectIdentityExtension(t.Context(), executable, profile); err == nil {
					t.Errorf("unsafe leaf mode %o admitted", mode)
				}
			}
			if err := os.Chmod(file, 0o600); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type extensionCaptureStarter struct {
	command, env []string
	cwd          string
}

func (s *extensionCaptureStarter) Start(command []string, cwd string, env []string) (acp.Process, error) {
	s.command, s.cwd, s.env = command, cwd, env
	return &testAuthProcess{}, nil
}

func TestIdentityExtension_UsesExistingAuthRootAndProcessOwner(t *testing.T) {
	extension := IdentityExtension{NodeExecutable: "/node", Bootstrap: "/sidecar/bootstrap.mjs", AdapterRoot: "/installed/adapter"}
	next := &extensionCaptureStarter{}
	starter, err := extension.Starter("api_key", next)
	if err != nil {
		t.Fatal(err)
	}
	process, err := starter.Start([]string{"/installed/adapter/dist/index.js", "--hide-claude-auth"}, "/workspace", []string{"HOME=/existing/root", "ANTHROPIC_API_KEY=synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/node", "/sidecar/bootstrap.mjs", "--adapter-root", "/installed/adapter", "--credential-source", "api_key", "--hide-claude-auth"}
	if !reflect.DeepEqual(next.command, want) || next.cwd != "/workspace" || !reflect.DeepEqual(next.env, []string{"HOME=/existing/root", "ANTHROPIC_API_KEY=synthetic"}) || process == nil {
		t.Fatalf("wrong existing boundary: %v %q %v", next.command, next.cwd, next.env)
	}
	if _, err := starter.Start([]string{"/other"}, "/workspace", nil); err == nil {
		t.Fatal("foreign executable admitted")
	}
	gateway, err := extension.Starter("gateway", next)
	if err != nil {
		t.Fatal(err)
	}
	next.command, next.env = nil, nil
	if _, err := gateway.Start([]string{"/installed/adapter/dist/index.js"}, "/workspace", []string{"HOME=/existing/root", "CLAUDE_GATEWAY_BASE_URL=https://gw.example.com"}); err != nil {
		t.Fatal(err)
	}
	want = []string{"/node", "/sidecar/bootstrap.mjs", "--adapter-root", "/installed/adapter", "--credential-source", "gateway"}
	if !reflect.DeepEqual(next.command, want) || reflect.DeepEqual(next.env, []string{}) {
		t.Fatalf("wrong gateway boundary: %v %v", next.command, next.env)
	}
}

// Claim: a refused admin policy and an untrusted identity sidecar both stop
// ResolveIdentityExtension before the prepared Node executes, and the refusal
// never echoes the probe payload.
// Source: extension.go ResolveIdentityExtension (policy, bundle and sidecar
// gates precede the version probe).
// Mutants: remove the consent gate, or accept a changed sidecar/bundle.
// Node admission is version-based; the observed digest is reported evidence,
// not a gate (see TestCertifiedNodeExecutable_ReportsBytesWithoutPinningThem).
func TestIdentityExtension_RefusedPolicyAndUntrustedSidecarNeverExecute(t *testing.T) {
	requireCertifiedHost(t)
	for _, tc := range []struct{ name, version, mode, want string }{
		{"untrusted sidecar before the probe", "raw-private-payload", "honor", "not certified"},
		{"refused policy before probe", "v22.23.4", "refuse", "admin_policy: honor"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			exe := adapterFixture(t, root, "0.89.1", filepath.Join(root, "adapter-executed"))
			profile, err := inspectRuntime(t.Context(), exe)
			if err != nil {
				t.Fatal(err)
			}
			profile.CLIVersion = "2.1.293"
			sidecar := filepath.Join(root, "claudeagentacp-adapter-extension")
			if err := os.Mkdir(sidecar, 0o700); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"bootstrap.mjs", "identity.mjs", "models.mjs"} {
				if err := os.WriteFile(filepath.Join(sidecar, name), []byte("synthetic sidecar"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			marker := filepath.Join(root, "node-executed")
			credentialMarker := filepath.Join(root, "node-credential")
			node := filepath.Join(root, "node")
			body := "#!/bin/sh\nprintf executed > '" + marker + "'\nprintf '%s' \"$ANTHROPIC_API_KEY\" > '" + credentialMarker + "'\nprintf '%s\\n' '" + tc.version + "'\n"
			if err := os.WriteFile(node, []byte(body), 0o700); err != nil {
				t.Fatal(err)
			}
			resolver := AdapterResolver{Policy: PolicyGate{Files: fstest.MapFS{}}, Timeout: 100 * time.Millisecond}
			result, err := resolver.ResolveIdentityExtension(t.Context(), profile, filepath.Join(root, "connector"), node, root, []string{"PATH=/usr/bin:/bin", "HOME=" + root, "CLAUDE_CONFIG_DIR=" + root, "ANTHROPIC_API_KEY=synthetic"}, tc.mode)
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "raw-private") {
				t.Fatalf("unsafe Node result: %v", err)
			}
			if result.NodeExecutable != "" || result.NodeVersion != "" {
				t.Fatalf("refused resolution published observed identity: %+v", result)
			}
			for _, file := range []string{marker, credentialMarker} {
				if _, err := os.Stat(file); !os.IsNotExist(err) {
					t.Fatal("a Node executed before admission")
				}
			}
		})
	}
}

// Claim: Node admission requires a regular executable file and reports the
// observed digest without pinning exact bytes, so a rebuilt Node of an admitted
// version is accepted.
// Source: extension.go certifiedNodeExecutable (the observed digest is identity
// evidence, not an admission gate).
// Mutants: restore the exact-digest comparison, or drop the regular-file check.
func TestCertifiedNodeExecutable_ReportsBytesWithoutPinningThem(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("executable mode bits are not available on Windows")
	}
	dir := t.TempDir()
	node := filepath.Join(dir, "node")
	body := []byte("#!/bin/sh\nprintf 'v22.23.4\\n'\n")
	if err := os.WriteFile(node, body, 0o700); err != nil {
		t.Fatal(err)
	}
	resolved, digest, err := certifiedNodeExecutable(t.Context(), node)
	if err != nil {
		t.Fatalf("a regular executable was refused: %v", err)
	}
	if resolved != node {
		t.Fatalf("resolved = %q, want %q", resolved, node)
	}
	if want := fmt.Sprintf("%x", sha256.Sum256(body)); digest != want {
		t.Fatalf("digest = %q, want the observed bytes %q", digest, want)
	}
	if _, _, err := certifiedNodeExecutable(t.Context(), "node"); err == nil {
		t.Fatal("a relative Node path was admitted")
	}
	if _, _, err := certifiedNodeExecutable(t.Context(), dir); err == nil {
		t.Fatal("a directory was admitted as the Node executable")
	}
}

// Claim: ResolveIdentityExtension re-inspects the installed closure and refuses
// when its bytes changed after certification, before any Node or adapter exec.
// Mutant: skip the freshly inspected bundle comparison.
func TestIdentityExtension_ResolveRejectsChangedRuntimeClosure(t *testing.T) {
	requireCertifiedHost(t)
	root := t.TempDir()
	marker := filepath.Join(root, "adapter-executed")
	exe := adapterFixture(t, root, "0.89.1", marker)
	profile, err := inspectRuntime(t.Context(), exe)
	if err != nil {
		t.Fatal(err)
	}
	profile.CLIVersion = "2.1.293"
	stale := profile
	stale.BundleSHA256 = "stale-installation-closure"
	resolver := AdapterResolver{Policy: PolicyGate{Files: fstest.MapFS{}}, Timeout: time.Second}
	_, err = resolver.ResolveIdentityExtension(t.Context(), stale, filepath.Join(root, "connector"), filepath.Join(root, "node"), root, []string{"PATH=/usr/bin:/bin", "HOME=" + root, "CLAUDE_CONFIG_DIR=" + root}, "honor")
	if err == nil || !strings.Contains(err.Error(), "runtime closure changed") {
		t.Fatalf("stale closure admitted: %v", err)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatal("stale closure executed the adapter")
	}
}

// The installation is operator-trusted, not immutable against its owner. These
// structural checks prevent accidental special-file reads and public mutation.
func TestRuntimeFile_RejectsUntrustedOrNonregularLeaves(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "runtime.js")
	if err := os.WriteFile(file, []byte("runtime"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.js")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if _, err := hashRuntimeFile(t.Context(), link); err == nil {
		t.Error("linked leaf hashed as a trusted regular file")
	}
	if err := os.Chmod(file, 0o606); err != nil {
		t.Fatal(err)
	}
	if _, err := hashRuntimeFile(t.Context(), file); err == nil {
		t.Error("world-writable runtime admitted")
	}
	if err := os.Chmod(file, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := hashRuntimeFile(t.Context(), file); err == nil {
		t.Error("unreadable runtime admitted")
	}
}
