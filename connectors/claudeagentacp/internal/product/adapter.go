package product

import (
	"context"
	"crypto/sha256"
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
	"time"

	"github.com/aiproxer/aiproxer/connector-support/acp"
)

// RuntimeProfile identifies installation bytes. Resolve checks the upstream
// bundle; ResolveIdentityExtension must also admit Node and the shipped sidecar.
type RuntimeProfile struct {
	Executable, CLIExecutable                         string
	NodeExecutable                                    string
	AdapterVersion, SDKVersion, CLIVersion            string
	AdapterSHA256, SDKSHA256, CLISHA256, BundleSHA256 string
}

// AdapterResolver belongs to one instance. The caller supplies its prepared
// AuthEnv environment/root, never host credentials for a version probe. Policy
// admission is deliberately repeated before every exec, including version checks.
type AdapterResolver struct {
	Cache   *acp.ExecutableCache
	Policy  PolicyGate
	Timeout time.Duration
}

func (r AdapterResolver) resolveExecutable(configured string, host []string) (string, error) {
	if r.Cache == nil {
		return "", errors.New("claudeagentacp: instance-owned executable cache required")
	}
	environment := ""
	for _, entry := range host {
		if value, ok := strings.CutPrefix(entry, "CLAUDE_AGENT_ACP_BIN="); ok {
			environment = strings.TrimSpace(value)
		}
	}
	for _, name := range []string{strings.TrimSpace(configured), environment, "claude-agent-acp"} {
		if name == "" {
			continue
		}
		if executable, err := r.Cache.LookPath(name); err == nil {
			return executable, nil
		}
	}
	return "", errors.New("claudeagentacp: adapter executable missing; install the certified claude-agent-acp bundle and set executable or CLAUDE_AGENT_ACP_BIN")
}

// Resolve never downloads or updates dependencies. Only the demonstrated Linux
// amd64 bundle is admitted, before executing any of its module-loading probes.
// The caller must also resolve the certified identity extension before spawn.
func (r AdapterResolver) Resolve(ctx context.Context, configured string, host []string, cwd string, env []string, mode string) (RuntimeProfile, error) {
	var result RuntimeProfile
	for _, entries := range [][]string{host, env} {
		if err := checkRuntimeOverrides(entries); err != nil {
			return result, err
		}
	}
	executable, err := r.resolveExecutable(configured, host)
	if err != nil {
		return result, err
	}
	if err := serverPolicyConsent(mode); err != nil {
		return result, err
	}
	result, err = inspectRuntime(ctx, executable)
	if err != nil {
		return result, err
	}
	if !certifiedBundle(result) {
		return result, errors.New("claudeagentacp: exact runtime bundle is not certified; install the pinned complete bundle")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		return result, errors.New("claudeagentacp: Node >=22 required")
	}
	result.NodeExecutable, _, err = certifiedNodeExecutable(ctx, node)
	if err != nil {
		return result, err
	}
	// Never execute the adapter's env-node shebang: it performs another PATH
	// lookup before the interpreter can be admitted.
	version, err := r.probeVersion(ctx, []string{result.NodeExecutable, result.Executable, "--version"}, cwd, env, mode)
	if err != nil {
		return result, err
	}
	if result.AdapterVersion != version || result.SDKVersion != "0.3.293" {
		return result, errors.New("claudeagentacp: bundled adapter/SDK version mismatch; reinstall the pinned complete bundle")
	}
	cliVersion, err := r.version(ctx, result.CLIExecutable, cwd, env, mode)
	if err != nil {
		return result, err
	}
	result.CLIVersion = strings.TrimSuffix(cliVersion, " (Claude Code)")
	if result.CLIVersion != "2.1.293" {
		return result, errors.New("claudeagentacp: bundled CLI version is not certified; reinstall the pinned complete bundle")
	}
	return result, nil
}

// Pinned upstream closures for Linux/amd64. The original closure has a deny
// baseline; the updated closure changes only @hono/node-server. Permissions
// are independently configured; neither tuple authorizes unknown bytes, WSL
// or other native platforms.
func certifiedBundle(p RuntimeProfile) bool {
	return p.AdapterVersion == "0.89.1" && p.SDKVersion == "0.3.293" &&
		(p.CLIVersion == "" || p.CLIVersion == "2.1.293") &&
		p.AdapterSHA256 == "4a1f0fff5cdd26b8c9adb5f9b3d314640e06f876cfff97cac437159b4551ec27" &&
		p.SDKSHA256 == "a980b792353c301eea60d7896704cff21440de7c4aeefc3fe481be079504e203" &&
		p.CLISHA256 == "8968405e26db478af44eabc4635ab5ca557057b702a54460a59c13e1b253e978" &&
		(p.BundleSHA256 == "d39ea52fb6f52fdc2617b65ce4ebb8831312df708118d60d6f6ad0ccb202547f" ||
			p.BundleSHA256 == "aec8d4fe70fd6c18bc0a33bd9e17ba6c78dd65efade14e0a8aae6dce2c1c55f6")
}

func (r AdapterResolver) version(ctx context.Context, executable, cwd string, env []string, mode string) (string, error) {
	return r.probeVersion(ctx, []string{executable, "--version"}, cwd, env, mode)
}

func (r AdapterResolver) probeVersion(ctx context.Context, command []string, cwd string, env []string, mode string) (string, error) {
	config := ""
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, "CLAUDE_CONFIG_DIR="); ok {
			config = value
		}
	}
	if _, err := r.Policy.Check(ctx, config, mode); err != nil {
		return "", err
	}
	if err := serverPolicyConsent(mode); err != nil {
		return "", err
	}
	if len(env) == 0 {
		return "", errors.New("claudeagentacp: prepared authentication environment required")
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if probeCtx.Err() != nil {
		return "", errors.New("claudeagentacp: version probe timed out; verify the installed adapter/runtime")
	}
	output := identityOutput{cancel: cancel}
	err := (acp.OSProcessStarter{}).Run(probeCtx, command, cwd, append([]string(nil), env...), &output, nil)
	if probeCtx.Err() != nil {
		return "", errors.New("claudeagentacp: version probe timed out; verify the installed adapter/runtime")
	}
	if err != nil {
		return "", errors.New("claudeagentacp: version probe failed; verify the installed adapter/runtime")
	}
	value := strings.TrimSpace(string(output.data))
	plain := strings.TrimSuffix(value, " (Claude Code)")
	parts := strings.Split(plain, ".")
	valid := len(parts) == 3
	for _, part := range parts {
		if part == "" {
			valid = false
		}
		for _, ch := range part {
			if ch < '0' || ch > '9' {
				valid = false
			}
		}
	}
	if !valid {
		return "", errors.New("claudeagentacp: unparsable version output; reinstall the certified bundle")
	}
	return value, nil
}

// inspectRuntime performs only reads. A complete node_modules digest includes
// transitive JS dependencies, rather than treating index.js as the whole adapter.
// Links are rejected except the operator's executable symlink (resolved first).
func inspectRuntime(ctx context.Context, executable string) (RuntimeProfile, error) {
	var result RuntimeProfile
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return result, errors.New("claudeagentacp: runtime profile not certified on this platform")
	}
	kernel, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil || strings.Contains(strings.ToLower(string(kernel)), "microsoft") || strings.Contains(strings.ToLower(string(kernel)), "wsl") {
		return result, errors.New("claudeagentacp: runtime profile not certified on WSL or unknown Linux kernels")
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return result, errors.New("claudeagentacp: adapter path unavailable; reinstall the certified bundle")
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return result, errors.New("claudeagentacp: absolute adapter path required")
	}
	adapter := filepath.Dir(filepath.Dir(resolved))
	nodes := filepath.Dir(filepath.Dir(adapter))
	if filepath.Base(resolved) != "index.js" || filepath.Base(filepath.Dir(resolved)) != "dist" || filepath.Base(adapter) != "claude-agent-acp" || filepath.Base(nodes) != "node_modules" {
		return result, errors.New("claudeagentacp: unknown adapter bundle layout; use the installed npm bundle executable")
	}
	readVersion := func(dir, name string) (string, error) {
		file := filepath.Join(dir, "package.json")
		if err := runtimeFile(file); err != nil {
			return "", errors.New("claudeagentacp: trusted regular bundled package identity required; reinstall the complete bundle")
		}
		data, err := os.ReadFile(file)
		var metadata struct{ Name, Version string }
		if err != nil || json.Unmarshal(data, &metadata) != nil || metadata.Name != name || metadata.Version == "" {
			return "", errors.New("claudeagentacp: missing or unparsable bundled package identity; reinstall the complete bundle")
		}
		return metadata.Version, nil
	}
	result.Executable = resolved
	result.AdapterVersion, err = readVersion(adapter, "@agentclientprotocol/claude-agent-acp")
	if err != nil {
		return result, err
	}
	sdk := filepath.Join(nodes, "@anthropic-ai", "claude-agent-sdk")
	result.SDKVersion, err = readVersion(sdk, "@anthropic-ai/claude-agent-sdk")
	if err != nil {
		return result, err
	}
	cliDir := filepath.Join(nodes, "@anthropic-ai", "claude-agent-sdk-linux-x64")
	cliPackage, err := readVersion(cliDir, "@anthropic-ai/claude-agent-sdk-linux-x64")
	if err != nil || cliPackage != result.SDKVersion {
		return result, errors.New("claudeagentacp: missing or mismatched bundled CLI package; reinstall the complete bundle")
	}
	result.CLIExecutable = filepath.Join(cliDir, "claude")
	for _, file := range []string{resolved, result.CLIExecutable} {
		if err := runtimeExecutable(file); err != nil {
			return result, errors.New("claudeagentacp: trusted regular bundled CLI and adapter executable required; reinstall the complete bundle")
		}
	}
	for file, target := range map[string]*string{resolved: &result.AdapterSHA256, filepath.Join(sdk, "sdk.mjs"): &result.SDKSHA256, result.CLIExecutable: &result.CLISHA256} {
		fileHash, err := hashRuntimeFile(ctx, file)
		if err != nil {
			return result, errors.New("claudeagentacp: bundled CLI or runtime file unavailable; reinstall the complete bundle")
		}
		*target = fileHash
	}
	h := sha256.New()
	err = filepath.WalkDir(nodes, func(file string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(nodes, file)
		if err != nil {
			return err
		}
		// npm's per-install bookkeeping file is metadata, not executed code, and
		// its bytes differ between npm majors for an identical package tree.
		// Excluding it keeps the certified identity a function of the installed
		// packages alone. Only the node_modules root is excluded: a nested file
		// with the same name belongs to a package and stays hashed.
		if rel == ".package-lock.json" {
			return nil
		}
		if entry.IsDir() {
			if entry.Name() == ".bin" {
				return filepath.SkipDir
			}
			info, err := entry.Info()
			if err != nil || info.Mode().Perm()&0o002 != 0 {
				return errors.New("world-writable or unreadable runtime directory")
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return errors.New("unsupported runtime link or file")
		}
		digest, err := hashRuntimeFile(ctx, file)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(h, "%s\x00%s\n", filepath.ToSlash(rel), digest)
		return nil
	})
	if err != nil {
		return result, errors.New("claudeagentacp: runtime bundle unreadable or unsupported; reinstall the complete bundle")
	}
	result.BundleSHA256 = fmt.Sprintf("%x", h.Sum(nil))
	return result, nil
}

func hashRuntimeFile(ctx context.Context, name string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := runtimeFile(name); err != nil {
		return "", err
	}
	file, err := os.Open(name)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, readErr := io.Copy(h, file)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return "", errors.Join(readErr, closeErr)
	}
	return fmt.Sprintf("%x", h.Sum(nil)), ctx.Err()
}

func runtimeFile(name string) error {
	info, err := os.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o002 != 0 || info.Mode().Perm()&0o444 == 0 {
		return errors.New("unsupported or untrusted runtime file")
	}
	return nil
}

func runtimeExecutable(file string) error {
	info, err := os.Lstat(file)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 || info.Mode().Perm()&0o002 != 0 {
		return errors.New("claudeagentacp: trusted regular runtime executable required")
	}
	return nil
}

func checkRuntimeOverrides(env []string) error {
	for _, entry := range env {
		name, value, _ := strings.Cut(entry, "=")
		switch name {
		case "NODE_OPTIONS", "NODE_PATH", "LD_PRELOAD", "LD_LIBRARY_PATH", "CLAUDE_CODE_EXECUTABLE", "CLAUDE_AGENT_ACP_EXPERIMENTAL_V2":
			if value != "" {
				return errors.New("claudeagentacp: external CLAUDE_CODE_EXECUTABLE or runtime loader override unsupported; use the certified deployment")
			}
		}
	}
	return nil
}
