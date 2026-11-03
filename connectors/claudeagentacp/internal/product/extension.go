package product

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/acp"
)

// certifiedSidecarSHA256 binds the shipped three-file identity sidecar to the
// certified release. It is the only pin the Go side keeps for sidecar source:
// a deliberate sidecar change re-records it together with the certification
// evidence (see docs/claude-agent-acp-backend.md and make claude-gateway-certify),
// and TestShippedSidecarMatchesCertifiedPin fails until both agree.
const certifiedSidecarSHA256 = "7ed0bff33783ae1f1eccaa624d115a7e0dc9acc50b449ddc2579a33edcf30c3d"

// IdentityExtension is observed deployment identity, not runtime certification.
// Release task 3.3 installs the project-authored sidecar beside the connector.
type IdentityExtension struct {
	SupervisorExecutable                   string
	NodeExecutable, Bootstrap, AdapterRoot string
	NodeVersion, NodeSHA256                string
	ExtensionSHA256, BundleSHA256          string
}

func (e IdentityExtension) Command(source string) ([]string, error) {
	if e.NodeExecutable == "" || e.Bootstrap == "" || e.AdapterRoot == "" {
		return nil, errors.New("claudeagentacp: resolved identity extension required")
	}
	switch source {
	case "api_key", "subscription_token", "host_login", "gateway":
	default:
		return nil, errors.New("claudeagentacp: unsupported credential source")
	}
	command := []string{e.NodeExecutable, e.Bootstrap, "--adapter-root", e.AdapterRoot, "--credential-source", source}
	if source == "api_key" {
		command = append(command, "--hide-claude-auth")
	}
	return command, nil
}

// Starter is installed INSIDE AuthEnv.StarterWithGate, immediately around the
// existing OS starter. AuthEnv still creates/removes the root and owns Wait;
// this adapter only substitutes the fixed executable argv after that gate.
func (e IdentityExtension) Starter(source string, next acp.ProcessStarter) (acp.ProcessStarter, error) {
	command, err := e.Command(source)
	if err != nil {
		return nil, err
	}
	if next == nil {
		return nil, errors.New("claudeagentacp: existing process starter required")
	}
	return &identityExtensionStarter{command: command, supervisor: e.SupervisorExecutable, adapter: filepath.Join(e.AdapterRoot, "dist", "index.js"), source: source, next: next}, nil
}

type identityExtensionStarter struct {
	command         []string
	supervisor      string
	adapter, source string
	next            acp.ProcessStarter
}

func (s *identityExtensionStarter) Start(command []string, cwd string, env []string) (acp.Process, error) {
	want := 1
	if s.source == "api_key" {
		want = 2
	}
	if len(command) != want || command[0] != s.adapter || (want == 2 && command[1] != "--hide-claude-auth") || len(env) == 0 {
		return nil, errors.New("claudeagentacp: identity extension requires the prepared fixed adapter command")
	}
	marker := ""
	for _, value := range env {
		if path, ok := strings.CutPrefix(value, "AIP_CLAUDE_ACP_SHUTDOWN_FILE="); ok {
			marker = path
		}
	}
	args := append([]string(nil), s.command...)
	if marker != "" {
		if !filepath.IsAbs(s.supervisor) {
			return nil, errors.New("claudeagentacp: runtime supervisor executable required")
		}
		args = append([]string{s.supervisor, "--runtime-supervisor"}, args...)
	}
	proc, err := s.next.Start(args, cwd, append([]string(nil), env...))
	if err == nil && marker != "" {
		proc = wrapSupervisorProcess(proc, marker)
	}
	return proc, err
}

// ResolveIdentityExtension rechecks installed closure bytes and consent before
// the Node probe. Node must meet the adapter's minimum version (>=22);
// only the adapter tree and sidecar are pinned to exact bytes.
func (r AdapterResolver) ResolveIdentityExtension(ctx context.Context, runtime RuntimeProfile, connectorExecutable, nodeExecutable, cwd string, env []string, mode string) (IdentityExtension, error) {
	var empty IdentityExtension
	if err := checkRuntimeOverrides(env); err != nil {
		return empty, err
	}
	current, err := inspectRuntime(ctx, runtime.Executable)
	if err != nil || current.BundleSHA256 != runtime.BundleSHA256 {
		return empty, errors.New("claudeagentacp: runtime closure changed; recertify the complete installation")
	}
	current.CLIVersion = runtime.CLIVersion
	extension, err := inspectIdentityExtension(ctx, connectorExecutable, current)
	if err != nil {
		return empty, err
	}
	config := ""
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, "CLAUDE_CONFIG_DIR="); ok {
			config = value
		}
	}
	if _, err := r.Policy.Check(ctx, config, mode); err != nil {
		return empty, err
	}
	if err := serverPolicyConsent(mode); err != nil {
		return empty, err
	}
	if len(env) == 0 || !filepath.IsAbs(nodeExecutable) {
		return empty, errors.New("claudeagentacp: prepared environment and absolute Node executable required")
	}
	nodeExecutable, extension.NodeSHA256, err = certifiedNodeExecutable(ctx, nodeExecutable)
	if err != nil {
		return empty, err
	}
	// Three-file sidecar binds identity/exact-model guards and the connector's
	// bypass creation option. Upstream runtime and sidecar bytes remain
	// unchanged; Node is admitted by version and its observed digest is
	// reported as evidence.
	if !certifiedBundle(current) || runtime.CLIVersion != "2.1.293" ||
		extension.ExtensionSHA256 != certifiedSidecarSHA256 {
		return empty, errors.New("claudeagentacp: exact runtime or identity sidecar is not certified; reinstall the certified deployment")
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output := identityOutput{cancel: cancel}
	if err := (acp.OSProcessStarter{}).Run(probeCtx, []string{nodeExecutable, "--version"}, cwd, append([]string(nil), env...), &output, nil); err != nil || probeCtx.Err() != nil {
		return empty, errors.New("claudeagentacp: Node version probe failed or timed out; install Node >=22")
	}
	version := strings.TrimPrefix(strings.TrimSpace(string(output.data)), "v")
	if err := validateNodeVersion(version); err != nil {
		return empty, err
	}
	extension.NodeExecutable, extension.NodeVersion = nodeExecutable, version
	return extension, nil
}

// validateNodeVersion enforces the installed adapter's engines.node >=22.
func validateNodeVersion(version string) error {
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return errors.New("claudeagentacp: unsupported Node version; install Node >=22")
	}
	for i, part := range parts {
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 || (i == 0 && value < 22) {
			return errors.New("claudeagentacp: unsupported Node version; install Node >=22")
		}
	}
	return nil
}

// certifiedNodeExecutable resolves the Node entry point and requires a regular,
// executable file. It reports the observed digest for identity evidence but does
// not pin exact bytes: a rebuilt or patched Node of an admitted version is
// accepted, so a routine package upgrade does not need a new release here.
// ResolveIdentityExtension checks the minimum supported version with its probe.
//
// An unreadable file leaves the reported digest empty. Admission no longer
// depends on the bytes; the version probe or the exec itself surfaces the
// failure.
func certifiedNodeExecutable(ctx context.Context, executable string) (string, string, error) {
	if !filepath.IsAbs(executable) {
		return "", "", errors.New("claudeagentacp: absolute Node executable required")
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil || runtimeExecutable(resolved) != nil {
		return "", "", errors.New("claudeagentacp: trusted regular Node executable required")
	}
	digest, err := hashRuntimeFile(ctx, resolved)
	if err != nil {
		digest = ""
	}
	return resolved, digest, nil
}

func inspectIdentityExtension(ctx context.Context, connectorExecutable string, runtime RuntimeProfile) (IdentityExtension, error) {
	var result IdentityExtension
	if !filepath.IsAbs(connectorExecutable) || runtime.AdapterVersion != "0.89.1" || runtime.SDKVersion != "0.3.293" || runtime.CLIVersion != "2.1.293" || runtime.BundleSHA256 == "" {
		return result, errors.New("claudeagentacp: pinned observed runtime and absolute connector path required")
	}
	dir := filepath.Join(filepath.Dir(connectorExecutable), "claudeagentacp-adapter-extension")
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o002 != 0 {
		return result, errors.New("claudeagentacp: connector identity sidecar missing or untrusted")
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return result, errors.New("claudeagentacp: connector identity sidecar unavailable")
	}
	h := sha256.New()
	for _, name := range []string{"bootstrap.mjs", "identity.mjs", "models.mjs"} {
		file := filepath.Join(dir, name)
		info, err := os.Lstat(file)
		if err != nil || !info.Mode().IsRegular() {
			return result, errors.New("claudeagentacp: connector identity sidecar missing or unsupported; reinstall the complete connector artifact")
		}
		digest, err := hashRuntimeFile(ctx, file)
		if err != nil {
			return result, errors.New("claudeagentacp: connector identity sidecar unreadable")
		}
		_, _ = fmt.Fprintf(h, "%s\x00%s\n", name, digest)
	}
	result.SupervisorExecutable = connectorExecutable
	result.Bootstrap = filepath.Join(dir, "bootstrap.mjs")
	result.AdapterRoot = filepath.Dir(filepath.Dir(runtime.Executable))
	result.ExtensionSHA256 = fmt.Sprintf("%x", h.Sum(nil))
	result.BundleSHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(runtime.BundleSHA256+"\x00"+result.ExtensionSHA256)))
	return result, nil
}
