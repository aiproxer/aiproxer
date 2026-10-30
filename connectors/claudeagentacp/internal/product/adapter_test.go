package product

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/acp"
)

// Fake commands prove resolver decisions only; they never certify a profile.
func TestAdapterResolver_AdmissionAndDiagnostics(t *testing.T) {
	requireCertifiedHost(t)
	for _, tc := range []struct {
		name, output, want        string
		policy, override, missing bool
		probe                     bool
		timeout                   time.Duration
	}{
		{name: "found but uncertified", output: "0.89.1", want: "not certified"},
		{name: "unknown", output: "0.90.0", want: "not certified"},
		{name: "unparsable", output: "secret-shaped arbitrary output", want: "unparsable", probe: true},
		{name: "missing", missing: true, want: "install"},
		{name: "policy before exec", policy: true, output: "0.89.1", want: "admin_policy: honor"},
		{name: "external override", override: true, output: "0.89.1", want: "CLAUDE_CODE_EXECUTABLE"},
		{name: "timeout", output: "0.89.1", timeout: time.Nanosecond, want: "timed out", probe: true},
		{name: "running adapter times out", output: "hang", timeout: 100 * time.Millisecond, want: "timed out", probe: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			marker := filepath.Join(root, "executed")
			exe := adapterFixture(t, root, tc.output, marker)
			files := fstest.MapFS{}
			if tc.policy {
				files["etc/claude-code/managed-settings.json"] = &fstest.MapFile{Data: []byte(`{}`)}
			}
			lookup := func(string) (string, error) {
				if tc.missing {
					return "", os.ErrNotExist
				}
				return exe, nil
			}
			r := AdapterResolver{Cache: acp.NewExecutableCache(lookup), Policy: PolicyGate{Files: files}, Timeout: tc.timeout}
			host := []string{}
			if tc.override {
				host = append(host, "CLAUDE_CODE_EXECUTABLE=/external")
			}
			mode := "honor"
			if tc.policy {
				mode = "refuse"
			}
			env := []string{"PATH=/usr/bin:/bin", "HOME=" + root, "CLAUDE_CONFIG_DIR=" + root}
			var err error
			if tc.probe {
				_, err = r.version(t.Context(), exe, root, env, mode)
			} else {
				_, err = r.Resolve(t.Context(), "", host, root, env, mode)
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v; want %s", err, tc.want)
			}
			if strings.Contains(err.Error(), "secret-shaped") {
				t.Fatal("raw probe output leaked")
			}
			if tc.policy || tc.override || tc.missing || tc.timeout == time.Nanosecond {
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatalf("refused admission executed adapter: %v", err)
				}
			}
		})
	}
}

func TestAdapterResolver_ResolutionOrderAndInstanceCache(t *testing.T) {
	for _, tc := range []struct{ configured, env, want string }{
		{"configured", "environment", "configured"}, {"", "environment", "environment"}, {"", "", "claude-agent-acp"},
	} {
		calls := []string{}
		r := AdapterResolver{Cache: acp.NewExecutableCache(func(name string) (string, error) { calls = append(calls, name); return "/resolved/" + name, nil })}
		for range 2 {
			got, err := r.resolveExecutable(tc.configured, []string{"CLAUDE_AGENT_ACP_BIN=" + tc.env})
			if err != nil || got != "/resolved/"+tc.want {
				t.Fatalf("resolution = %s, %v", got, err)
			}
		}
		if len(calls) != 1 || calls[0] != tc.want {
			t.Fatalf("cache/order: %v", calls)
		}
	}
}

func TestInspectRuntime_RejectsMissingOrChangedBundle(t *testing.T) {
	requireCertifiedHost(t)
	root := t.TempDir()
	exe := adapterFixture(t, root, "0.89.1", filepath.Join(root, "executed"))
	first, err := inspectRuntime(context.Background(), exe)
	if err != nil || first.AdapterVersion != "0.89.1" || first.SDKVersion != "0.3.293" || len(first.BundleSHA256) != 64 {
		t.Fatalf("identity = %+v, %v", first, err)
	}
	if err := os.WriteFile(filepath.Join(root, "node_modules", "@anthropic-ai", "claude-agent-sdk", "sdk.mjs"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := inspectRuntime(t.Context(), exe)
	if err != nil || second.BundleSHA256 == first.BundleSHA256 {
		t.Fatalf("changed runtime not distinguished: %+v, %v", second, err)
	}
	if err := os.Remove(first.CLIExecutable); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectRuntime(t.Context(), exe); err == nil || !strings.Contains(err.Error(), "bundled CLI") {
		t.Fatalf("missing CLI: %v", err)
	}
}

// Claim: admission refuses any executable outside the certified bundle layout and
// any world-writable directory inside the hashed closure. Mutants: drop a layout
// basename arm, or stop checking directory write permission.
func TestInspectRuntime_RejectsForeignLayoutAndUnsafeLeaves(t *testing.T) {
	requireCertifiedHost(t)
	t.Run("foreign basename", func(t *testing.T) {
		root := t.TempDir()
		exe := adapterFixture(t, root, "0.89.1", filepath.Join(root, "executed"))
		data, err := os.ReadFile(exe)
		if err != nil {
			t.Fatal(err)
		}
		foreign := filepath.Join(filepath.Dir(exe), "bundle.js")
		if err := os.WriteFile(foreign, data, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := inspectRuntime(t.Context(), foreign); err == nil || !strings.Contains(err.Error(), "unknown adapter bundle layout") {
			t.Fatalf("foreign layout admitted: %v", err)
		}
	})
	t.Run("world-writable directory", func(t *testing.T) {
		root := t.TempDir()
		exe := adapterFixture(t, root, "0.89.1", filepath.Join(root, "executed"))
		dir := filepath.Join(root, "node_modules", "third-party")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o777); err != nil {
			t.Fatal(err)
		}
		if _, err := inspectRuntime(t.Context(), exe); err == nil || !strings.Contains(err.Error(), "runtime bundle unreadable or unsupported") {
			t.Fatalf("world-writable closure admitted: %v", err)
		}
	})
}

// Claim: every descriptor component is admission-relevant (task 2.1), not just
// semver. The real-byte positive and module-resolution audit live in r4 evidence.
func TestCertifiedBundle_ChangedDescriptorRefuses(t *testing.T) {
	known := RuntimeProfile{
		AdapterVersion: "0.89.1", SDKVersion: "0.3.293", CLIVersion: "2.1.293",
		AdapterSHA256: "4a1f0fff5cdd26b8c9adb5f9b3d314640e06f876cfff97cac437159b4551ec27",
		SDKSHA256:     "a980b792353c301eea60d7896704cff21440de7c4aeefc3fe481be079504e203",
		CLISHA256:     "8968405e26db478af44eabc4635ab5ca557057b702a54460a59c13e1b253e978",
		BundleSHA256:  "d39ea52fb6f52fdc2617b65ce4ebb8831312df708118d60d6f6ad0ccb202547f",
	}
	if !certifiedBundle(known) {
		t.Fatal("demonstrated descriptor refused")
	}
	for _, mutate := range []func(*RuntimeProfile){
		func(p *RuntimeProfile) { p.AdapterVersion = "0.89.2" },
		func(p *RuntimeProfile) { p.SDKVersion = "0.3.294" },
		func(p *RuntimeProfile) { p.CLIVersion = "2.1.294" },
		func(p *RuntimeProfile) { p.AdapterSHA256 = "changed" },
		func(p *RuntimeProfile) { p.SDKSHA256 = "changed" },
		func(p *RuntimeProfile) { p.CLISHA256 = "changed" },
		func(p *RuntimeProfile) { p.BundleSHA256 = "changed" },
	} {
		changed := known
		mutate(&changed)
		if certifiedBundle(changed) {
			t.Fatalf("changed descriptor admitted: %+v", changed)
		}
	}
}

func TestAdapterResolver_MissingCandidateFallsThroughWithoutExecuting(t *testing.T) {
	calls := []string{}
	r := AdapterResolver{Cache: acp.NewExecutableCache(func(name string) (string, error) {
		calls = append(calls, name)
		if name == "missing-configured" {
			return "", os.ErrNotExist
		}
		return "/environment/adapter", nil
	})}
	got, err := r.resolveExecutable("missing-configured", []string{"CLAUDE_AGENT_ACP_BIN=environment"})
	if err != nil || got != "/environment/adapter" || strings.Join(calls, ",") != "missing-configured,environment" {
		t.Fatalf("fallback = %s, %v; candidates %v", got, err, calls)
	}
}

// Version checks execute runtime code too; filesystem absence is insufficient.
func TestAdapterResolver_PotentialServerPolicyStopsVersionExec(t *testing.T) {
	for _, mode := range []string{"", "refuse"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			marker := filepath.Join(root, "executed")
			exe := adapterFixture(t, root, "0.89.1", marker)
			r := AdapterResolver{Cache: acp.NewExecutableCache(func(string) (string, error) { return exe, nil }), Policy: PolicyGate{Files: fstest.MapFS{}}}
			_, err := r.Resolve(t.Context(), exe, nil, root, []string{"PATH=/usr/bin:/bin", "HOME=" + root, "CLAUDE_CONFIG_DIR=" + root}, mode)
			if err == nil || !strings.Contains(err.Error(), "potential organisation/server") || !strings.Contains(err.Error(), "admin_policy: honor") {
				t.Fatalf("consent not enforced: %v", err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("version exec occurred: %v", err)
			}
		})
	}
}

// Task 2.1/requirement 1.2: unsupported metadata must reject before opening it.
// Each subprocess owns its blocked read, if any; CommandContext kills and joins
// it on failure, rather than leaking a FIFO reader goroutine in the test runner.
func TestInspectRuntime_RejectsNonregularMetadataBeforeRead(t *testing.T) {
	requireCertifiedHost(t)
	if exe := os.Getenv("AIP_TEST_METADATA_EXECUTABLE"); exe != "" {
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		_, err := inspectRuntime(ctx, exe)
		if err == nil || !strings.Contains(err.Error(), os.Getenv("AIP_TEST_METADATA_DIAGNOSTIC")) {
			t.Fatalf("unsupported metadata not rejected: %v", err)
		}
		return
	}
	for _, pkg := range []string{"@agentclientprotocol/claude-agent-acp", "@anthropic-ai/claude-agent-sdk", "@anthropic-ai/claude-agent-sdk-linux-x64", "transitive"} {
		for _, kind := range []string{"fifo", "symlink-fifo", "directory", "unreadable", "world-writable"} {
			if pkg == "transitive" && kind == "directory" {
				continue // Directories are valid within the dependency walk.
			}
			t.Run(pkg+"/"+kind, func(t *testing.T) {
				root := t.TempDir()
				exe := adapterFixture(t, root, "0.89.1", filepath.Join(root, "executed"))
				metadata := filepath.Join(root, "node_modules", pkg, "package.json")
				data := []byte(`{}`)
				if pkg == "transitive" {
					if err := os.MkdirAll(filepath.Dir(metadata), 0o700); err != nil {
						t.Fatal(err)
					}
				} else {
					var err error
					data, err = os.ReadFile(metadata)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.Remove(metadata); err != nil {
						t.Fatal(err)
					}
				}
				switch kind {
				case "fifo", "symlink-fifo":
					fifo := metadata
					if kind == "symlink-fifo" {
						fifo = filepath.Join(root, "fifo")
					}
					if output, err := exec.CommandContext(t.Context(), "mkfifo", fifo).CombinedOutput(); err != nil {
						t.Fatalf("mkfifo: %v: %s", err, output)
					}
					if kind == "symlink-fifo" {
						if err := os.Symlink(fifo, metadata); err != nil {
							t.Fatal(err)
						}
					}
				case "directory":
					if err := os.Mkdir(metadata, 0o700); err != nil {
						t.Fatal(err)
					}
				default:
					if err := os.WriteFile(metadata, data, 0o600); err != nil {
						t.Fatal(err)
					}
					mode := os.FileMode(0)
					if kind == "world-writable" {
						mode = 0o666
					}
					if err := os.Chmod(metadata, mode); err != nil {
						t.Fatal(err)
					}
				}
				want := "package"
				if pkg == "transitive" {
					want = "runtime bundle"
				}
				ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestInspectRuntime_RejectsNonregularMetadataBeforeRead$")
				cmd.Env = append(os.Environ(), "AIP_TEST_METADATA_EXECUTABLE="+exe, "AIP_TEST_METADATA_DIAGNOSTIC="+want)
				output, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("metadata rejection must complete before subprocess deadline: %v (deadline: %v): %s", err, ctx.Err(), output)
				}
			})
		}
	}
}

// Claim: the checked-in certification recipe reproduces the admitted bundle
// identity. Opt-in, because it reads a real operator-installed tree instead of a
// fixture: install package.json and package-lock.json from install/ and point
// AIP_CLAUDE_ACP_INSTALL_DIR at the install root.
func TestInstalledBundleIdentity(t *testing.T) {
	requireCertifiedHost(t)
	root := strings.TrimSpace(os.Getenv("AIP_CLAUDE_ACP_INSTALL_DIR"))
	if root == "" {
		t.Skip("set AIP_CLAUDE_ACP_INSTALL_DIR to an installed certified runtime")
	}
	exe := filepath.Join(root, "node_modules", ".bin", "claude-agent-acp")
	profile, err := inspectRuntime(t.Context(), exe)
	if err != nil {
		t.Fatalf("inspect installed bundle: %v", err)
	}
	if got, want := profile.BundleSHA256, "d39ea52fb6f52fdc2617b65ce4ebb8831312df708118d60d6f6ad0ccb202547f"; got != want {
		t.Fatalf("installed bundle digest = %s, want %s from the checked-in recipe", got, want)
	}
	if !certifiedBundle(profile) {
		t.Fatalf("installed bundle is not certified: %+v", profile)
	}
}

// Claim: the identity covers installed package bytes, not npm's root install
// bookkeeping. Rewriting node_modules/.package-lock.json must not change the
// digest (npm majors format it differently for the same packages), while a
// package's own file of that name stays hashed.
func TestInspectRuntime_RootBookkeepingExcluded(t *testing.T) {
	requireCertifiedHost(t)
	root := t.TempDir()
	exe := adapterFixture(t, root, "0.89.1", filepath.Join(root, "executed"))
	nodes := filepath.Join(root, "node_modules")
	bookkeeping := filepath.Join(nodes, ".package-lock.json")
	if err := os.WriteFile(bookkeeping, []byte(`{"lockfileVersion":3}`), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := inspectRuntime(t.Context(), exe)
	if err != nil {
		t.Fatalf("inspect with bookkeeping: %v", err)
	}
	if err := os.WriteFile(bookkeeping, []byte(`{"lockfileVersion":3,"writtenBy":"another npm major"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := inspectRuntime(t.Context(), exe)
	if err != nil || second.BundleSHA256 != first.BundleSHA256 {
		t.Fatalf("root bookkeeping changed the identity: %q != %q (%v)", second.BundleSHA256, first.BundleSHA256, err)
	}
	nested := filepath.Join(nodes, "@anthropic-ai", "claude-agent-sdk", ".package-lock.json")
	if err := os.WriteFile(nested, []byte("package-owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	third, err := inspectRuntime(t.Context(), exe)
	if err != nil {
		t.Fatalf("inspect with package-owned file: %v", err)
	}
	if third.BundleSHA256 == first.BundleSHA256 {
		t.Fatal("a package-owned .package-lock.json must stay part of the identity")
	}
}

func adapterFixture(t *testing.T, root, version, marker string) string {
	t.Helper()
	command := "printf '%s\\n' '" + version + "'\n"
	if version == "hang" {
		command = "sleep 30\n"
	}
	files := map[string]string{
		"@agentclientprotocol/claude-agent-acp/package.json":    `{"name":"@agentclientprotocol/claude-agent-acp","version":"0.89.1"}`,
		"@agentclientprotocol/claude-agent-acp/dist/index.js":   "#!/bin/sh\nprintf executed > '" + marker + "'\n" + command,
		"@anthropic-ai/claude-agent-sdk/package.json":           `{"name":"@anthropic-ai/claude-agent-sdk","version":"0.3.293"}`,
		"@anthropic-ai/claude-agent-sdk/sdk.mjs":                "synthetic SDK",
		"@anthropic-ai/claude-agent-sdk-linux-x64/package.json": `{"name":"@anthropic-ai/claude-agent-sdk-linux-x64","version":"0.3.293"}`,
		"@anthropic-ai/claude-agent-sdk-linux-x64/claude":       "#!/bin/sh\nprintf '2.1.293 (Claude Code)\\n'\n",
	}
	for name, data := range files {
		path := filepath.Join(root, "node_modules", name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(root, "node_modules", "@agentclientprotocol/claude-agent-acp/dist/index.js")
}

func requireCertifiedHost(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("runtime profile is certified only on Linux/amd64")
	}
	kernel, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		t.Skip("runtime kernel cannot be identified")
	}
	lower := strings.ToLower(string(kernel))
	if strings.Contains(lower, "microsoft") || strings.Contains(lower, "wsl") {
		t.Skip("runtime profile is not certified on WSL")
	}
}
