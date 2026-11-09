package product

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/aiproxer/aiproxer/connector-support/acp"
)

// AuthConfig contains only non-secret operator configuration. Secrets are
// supplied separately by ConfigureRequest.Secrets, never host environment.
type AuthConfig struct {
	CredentialSource string
	PermissionPolicy string
	EnvAllowlist     []string
	Gateway          GatewayConfig
}

// AuthEnv is immutable instance-scoped authentication. A process borrows it,
// but owns its configuration root through the existing Process.Wait lifetime.
type AuthEnv struct {
	source      string
	secret      string
	base        map[string]string
	home        string
	config      string
	policy      string
	gateway     GatewayConfig
	gatewayHost string
	headerMap   map[string]string
}

func (a *AuthEnv) String() string   { return "claudeagentacp authentication [redacted]" }
func (a *AuthEnv) GoString() string { return a.String() }

// GatewayHost names the validated, non-secret endpoint host of the gateway
// source for disclosure. Header values never pass through here.
func (a *AuthEnv) GatewayHost() string { return a.gatewayHost }

// NewAuthEnv selects one source without credential fallback. The host snapshot
// is explicit so construction cannot accidentally consult process-global state.
// Gateway header secrets are looked up by the exact configured header name and
// are never echoed; an empty connector-owned URL is re-validated here so the
// fail-closed endpoint policy cannot be bypassed through this seam.
func NewAuthEnv(cfg AuthConfig, secrets map[string]string, host []string) (*AuthEnv, error) {
	source := cfg.CredentialSource
	if source == "" {
		source = "api_key"
	}
	var secret string
	gatewayHeaders := map[string]string{}
	switch source {
	case "api_key":
		secret = secrets["api_key"]
		if strings.TrimSpace(secret) == "" {
			return nil, errors.New("claudeagentacp: api_key secret required")
		}
	case "subscription_token":
		secret = secrets["claude_oauth_token"]
		if strings.TrimSpace(secret) == "" {
			return nil, errors.New("claudeagentacp: claude_oauth_token required; provision with claude setup-token")
		}
	case "host_login":
	case "gateway":
		if err := ValidateGatewayConfig(cfg.Gateway); err != nil {
			return nil, err
		}
		for _, name := range cfg.Gateway.Headers {
			value, ok := secrets[name.Name]
			if !ok || strings.TrimSpace(value) == "" {
				return nil, errors.New("claudeagentacp: gateway header secret " + name.Name + " required")
			}
			if strings.ContainsAny(value, "\x00\r\n") {
				return nil, errors.New("claudeagentacp: invalid credential encoding")
			}
			gatewayHeaders[name.Name] = value
		}
	default:
		return nil, errors.New("claudeagentacp: credential_source must be api_key, subscription_token, host_login or gateway")
	}
	if secret != "" && strings.ContainsAny(secret, "\x00\r\n") {
		return nil, errors.New("claudeagentacp: invalid credential encoding")
	}
	if cfg.PermissionPolicy == "" {
		cfg.PermissionPolicy = "bypass"
	}
	if cfg.PermissionPolicy != "bypass" && cfg.PermissionPolicy != "deny" {
		return nil, errors.New("claudeagentacp: permission_policy must be bypass or deny")
	}
	allowed := map[string]bool{}
	for _, name := range []string{"PATH", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "SYSTEMROOT", "TEMP", "TMP", "TMPDIR", "LANG", "LC_ALL"} {
		allowed[name] = true
	}
	// Operator additions select only this finite harmless extension set. Do not
	// use a blacklist: provider/federation/secret names evolve independently.
	for _, name := range cfg.EnvAllowlist {
		if !allowed[name] && name != "TERM" && name != "TZ" && name != "LC_CTYPE" {
			return nil, errors.New("claudeagentacp: unsupported env_allowlist name")
		}
		allowed[name] = true
	}
	a := &AuthEnv{source: source, secret: secret, base: map[string]string{}, policy: cfg.PermissionPolicy, gateway: cfg.Gateway, headerMap: gatewayHeaders}
	if source == "gateway" && ValidateGatewayConfig(cfg.Gateway) == nil {
		if parsed, err := url.Parse(cfg.Gateway.BaseURL); err == nil {
			a.gatewayHost = parsed.Hostname()
		}
	}
	for _, entry := range host {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || strings.ContainsRune(value, 0) {
			continue
		}
		if allowed[name] {
			a.base[name] = value
		}
		if name == "HOME" {
			a.home = value
		}
		if name == "CLAUDE_CONFIG_DIR" {
			a.config = value
		}
	}
	if source == "host_login" {
		if a.home == "" {
			a.home = a.base["USERPROFILE"]
		}
		if a.home == "" {
			return nil, errors.New("claudeagentacp: host_login requires host home")
		}
		if a.config == "" {
			a.config = filepath.Join(a.home, ".claude")
		}
	}
	return a, nil
}

// HandshakeProfile returns a fresh connector-owned tools-off profile for the
// deny posture, or full non-interactive bypass with only AskUserQuestion
// disabled. Environment, settings and credential-source isolation remain
// independent of tool permissions; bypass is not an OS sandbox. Gateway
// sources additionally advertise the adapter's client gateway capability and
// carry the one authenticate payload the connector owns; the adapter
// validates it against a session process tree the connector controls
// entirely.
func (a *AuthEnv) HandshakeProfile() acp.HandshakeProfile {
	capabilities := json.RawMessage(`{"fs":{"readTextFile":false,"writeTextFile":false},"terminal":false}`)
	skip, params := true, json.RawMessage(nil)
	meta := json.RawMessage(`{"claudeCode":{"options":{"agents":{},"allowDangerouslySkipPermissions":false,"disallowedTools":["Agent","Task","TaskOutput","TaskStop"],"managedSettings":{"allowManagedPermissionRulesOnly":true,"allowedProviders":["anthropic"],"disableAllHooks":true,"permissions":{"disableBypassPermissionsMode":"disable"}},"mcpServers":{},"permissionMode":"default","plugins":[],"settingSources":[],"settings":{"apiKeyHelper":"","env":{},"hooks":{},"permissions":{"allow":[],"deny":[],"ask":[]},"plugins":[]},"skills":[],"strictMcpConfig":true,"tools":[]}}}`)
	if a.source == "gateway" {
		var err error
		if capabilities, err = withGatewayCapability(capabilities); err != nil {
			// The deny shape is a compile-time constant; this cannot fail for a
			// constructed profile. Fail closed to the first-party shape on any
			// surprise, refusing the gateway handshake instead.
			capabilities = json.RawMessage(`{"fs":{"readTextFile":false,"writeTextFile":false},"terminal":false}`)
		}
		if payload, err := a.gatewayAuthenticateParams(); err == nil {
			skip, params = false, payload
		}
		meta = gatewaySessionNewMeta(meta)
	}
	profile := acp.HandshakeProfile{
		ProtocolVersion:      1,
		SkipAuthenticate:     skip,
		ClientCapabilities:   capabilities,
		AuthenticateParams:   params,
		SessionNewMCPServers: json.RawMessage(`[]`),
		SessionNewMeta:       meta,
	}
	if a.policy != "bypass" {
		return profile
	}
	var options map[string]any
	if err := json.Unmarshal(profile.SessionNewMeta, &options); err != nil {
		// The deny shape is a compile-time constant; this cannot fail for a
		// constructed profile. Fail closed to the deny shape on any surprise.
		return profile
	}
	if code, ok := options["claudeCode"].(map[string]any); ok {
		if inner, ok := code["options"].(map[string]any); ok {
			managed, ok := inner["managedSettings"].(map[string]any)
			if !ok {
				return profile
			}
			permissions, ok := managed["permissions"].(map[string]any)
			if !ok {
				return profile
			}
			delete(permissions, "disableBypassPermissionsMode")
			delete(managed, "allowManagedPermissionRulesOnly")
			delete(inner, "tools")
			inner["permissionMode"] = "bypassPermissions"
			inner["allowDangerouslySkipPermissions"] = true
			inner["disallowedTools"] = []string{"AskUserQuestion"}
		}
	}
	merged, err := json.Marshal(options)
	if err != nil {
		return profile
	}
	profile.SessionNewMeta = merged
	return profile
}

// gatewaySessionNewMeta removes only the managed first-party provider
// allowlist: for this source the adapter's authenticate payload IS the
// provider choice the connector owns, and the CLI otherwise refuses the
// override as a provider the managed list does not pin. Every other
// managed restriction (hooks lock, bypass-mode disable, empty settings)
// stays byte-identical to the first-party posture.
func gatewaySessionNewMeta(meta json.RawMessage) json.RawMessage {
	var options map[string]any
	if err := json.Unmarshal(meta, &options); err != nil {
		return meta
	}
	code, ok := options["claudeCode"].(map[string]any)
	if !ok {
		return meta
	}
	inner, ok := code["options"].(map[string]any)
	if !ok {
		return meta
	}
	managed, ok := inner["managedSettings"].(map[string]any)
	if !ok {
		return meta
	}
	delete(managed, "allowedProviders")
	merged, err := json.Marshal(options)
	if err != nil {
		return meta
	}
	return merged
}

func withGatewayCapability(capabilities json.RawMessage) (json.RawMessage, error) {
	var mapping map[string]any
	if err := json.Unmarshal(capabilities, &mapping); err != nil {
		return nil, err
	}
	if mapping == nil {
		return nil, errors.New("claudeagentacp: gateway capabilities must be a JSON object")
	}
	mapping["auth"] = map[string]any{"_meta": map[string]any{"gateway": true}}
	merged, err := json.Marshal(mapping)
	if err != nil {
		return nil, err
	}
	return merged, nil
}

// gatewayAuthenticateParams serializes the wire-exact gateway payload. The
// values come from the connector-owned URL and secrets, never argv or logs;
// a header value stays only inside this RPC body.
func (a *AuthEnv) gatewayAuthenticateParams() (json.RawMessage, error) {
	headers := map[string]string{}
	maps.Copy(headers, a.headerMap)
	body, err := json.Marshal(map[string]any{
		"methodId": "gateway",
		"_meta":    map[string]any{"gateway": map[string]any{"baseUrl": a.gateway.BaseURL, "headers": headers}},
	})
	if err != nil {
		return nil, err
	}
	return body, nil
}

// Starter decorates the existing process boundary, not the pool or service.
// stateDir is instance-owned and must exist; no root is allocated until Start.
func (a *AuthEnv) Starter(stateDir string, next acp.ProcessStarter) acp.ProcessStarter {
	return &authProcessStarter{auth: a, stateDir: stateDir, next: next}
}

// StarterWithGate exposes the prepared root and exact child environment before
// any adapter exec. Assembly supplies the current lifecycle context in the
// callback; this legacy ProcessStarter interface itself has no context argument.
// Rejection cleans the owned root using the existing owner, without spawning.
func (a *AuthEnv) StarterWithGate(stateDir string, next acp.ProcessStarter, gate func([]string, string, []string) error) acp.ProcessStarter {
	return &authProcessStarter{auth: a, stateDir: stateDir, next: next, gate: gate}
}

type authProcessStarter struct {
	auth        *AuthEnv
	stateDir    string
	next        acp.ProcessStarter
	gate        func([]string, string, []string) error
	contextGate func(context.Context, []string, string, []string) (string, func(context.Context) (bool, error), error)
}

func (s *authProcessStarter) Start(cmd []string, cwd string, env []string) (acp.Process, error) {
	return s.StartContext(context.Background(), cmd, cwd, env)
}

// ContextStarter keeps startup probes on the caller's lifecycle context. The
// returned reuse check runs only while the shared runtime owns the turn claim.
func (a *AuthEnv) ContextStarter(stateDir string, next acp.ProcessStarter, gate func(context.Context, []string, string, []string) (string, func(context.Context) (bool, error), error)) acp.ProcessStarter {
	return &authProcessStarter{auth: a, stateDir: stateDir, next: next, contextGate: gate}
}

func (s *authProcessStarter) StartContext(ctx context.Context, cmd []string, cwd string, env []string) (acp.Process, error) {
	if len(env) != 0 || len(cmd) != 1 || s.next == nil || s.auth == nil {
		return nil, errors.New("claudeagentacp: spawn requires connector-owned command and environment")
	}
	prepared, err := s.auth.Prepare(s.stateDir, cmd)
	if err != nil {
		return nil, err
	}
	args, values, root := prepared.Command, prepared.Environment, prepared.root
	var reuse func(context.Context) (bool, error)
	var fingerprint string
	if s.contextGate != nil {
		fingerprint, reuse, err = s.contextGate(ctx, args, cwd, values)
	} else if s.gate != nil {
		err = s.gate(append([]string(nil), args...), cwd, append([]string(nil), values...))
	}
	if err != nil {
		return nil, errors.Join(err, prepared.Close())
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, prepared.Close())
	}
	shutdownFile := prepared.shutdownFile
	proc, err := s.next.Start(args, cwd, values)
	if err != nil {
		return nil, errors.Join(errors.New("claudeagentacp: start adapter failed"), removeRoot(shutdownFile), prepared.Close())
	}
	return &authProcess{Process: proc, root: root, reuse: reuse, fingerprint: fingerprint, shutdownFile: shutdownFile}, nil
}

// PreparedSpawn owns a temporary configuration root until Close or transfer to
// the existing process owner. Never format this value: it contains credentials.
type PreparedSpawn struct {
	Command, Environment []string
	root                 string
	shutdownFile         string
}

func (*PreparedSpawn) String() string     { return "claudeagentacp prepared spawn [redacted]" }
func (p *PreparedSpawn) GoString() string { return p.String() }
func (p *PreparedSpawn) Close() error {
	return errors.Join(removeRoot(p.shutdownFile), removeRoot(p.root))
}

// Prepare is shared by configure-time certification and actual process spawn.
// Both use identical environment construction, but each owns a fresh root.
func (a *AuthEnv) Prepare(stateDir string, cmd []string) (*PreparedSpawn, error) {
	if len(cmd) != 1 {
		return nil, errors.New("claudeagentacp: fixed command required")
	}
	child := make(map[string]string, len(a.base)+6)
	maps.Copy(child, a.base)
	if a.policy == "deny" {
		child["CLAUDE_CODE_DISABLE_BACKGROUND_TASKS"] = "1"
	}
	root := ""
	if a.source == "host_login" {
		child["HOME"], child["CLAUDE_CONFIG_DIR"] = a.home, a.config
	} else {
		if stateDir == "" {
			return nil, errors.New("claudeagentacp: instance state directory required")
		}
		var err error
		root, err = os.MkdirTemp(stateDir, "claude-process-")
		if err != nil {
			return nil, errors.New("claudeagentacp: create process root failed")
		}
		for key, dir := range map[string]string{"HOME": "home", "CLAUDE_CONFIG_DIR": "claude", "XDG_CONFIG_HOME": "xdg", "ANTHROPIC_CONFIG_DIR": "anthropic"} {
			path := filepath.Join(root, dir)
			if err := os.Mkdir(path, 0o700); err != nil {
				return nil, errors.Join(errors.New("claudeagentacp: create configuration directory failed"), removeRoot(root))
			}
			child[key] = path
		}
		// Windows runtimes use these home/config selectors instead of HOME/XDG.
		for _, key := range []string{"USERPROFILE", "APPDATA", "LOCALAPPDATA"} {
			if _, present := child[key]; present {
				child[key] = child["HOME"]
			}
		}
		switch a.source {
		case "api_key":
			child["ANTHROPIC_API_KEY"] = a.secret
		case "subscription_token":
			child["CLAUDE_CODE_OAUTH_TOKEN"] = a.secret
		case "gateway":
			child["CLAUDE_GATEWAY_BASE_URL"] = a.gateway.BaseURL
		}
	}
	args := append([]string(nil), cmd...)
	if a.source == "api_key" {
		args = append(args, "--hide-claude-auth")
	}
	shutdownFile := ""
	if a.policy == "bypass" {
		parent := root
		if parent == "" {
			parent = stateDir
		}
		file, err := os.CreateTemp(parent, "claude-shutdown-")
		if err != nil {
			return nil, errors.Join(errors.New("claudeagentacp: create shutdown marker failed"), removeRoot(root))
		}
		shutdownFile = file.Name()
		if err := file.Close(); err != nil {
			return nil, errors.Join(errors.New("claudeagentacp: close shutdown marker failed"), removeRoot(shutdownFile), removeRoot(root))
		}
		child["AIP_CLAUDE_ACP_SHUTDOWN_FILE"] = shutdownFile
	}
	keys := make([]string, 0, len(child))
	for k := range child {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	values := make([]string, 0, len(keys))
	for _, k := range keys {
		values = append(values, k+"="+child[k])
	}
	return &PreparedSpawn{Command: args, Environment: values, root: root, shutdownFile: shutdownFile}, nil
}

func removeRoot(root string) error {
	if root != "" {
		if err := os.RemoveAll(root); err != nil {
			return errors.New("claudeagentacp: remove process root failed")
		}
	}
	return nil
}

type authProcess struct {
	acp.Process
	root         string
	wait         sync.Once
	waitErr      error
	reuse        func(context.Context) (bool, error)
	fingerprint  string
	shutdownFile string
	kill         sync.Once
	killErr      error
}

// RuntimeFingerprint is immutable and belongs to this prepared process/root.
func (p *authProcess) RuntimeFingerprint() string { return p.fingerprint }

func (p *authProcess) ValidateReuse(ctx context.Context) (bool, error) {
	if p.reuse == nil {
		return true, nil
	}
	return p.reuse(ctx)
}

func (p *authProcess) Wait() error {
	p.wait.Do(func() {
		if err := p.Process.Wait(); err != nil {
			if errors.Is(err, acp.ErrCleanupIncomplete) {
				p.waitErr = err
				return
			}
			p.waitErr = errors.New("claudeagentacp: adapter exited unsuccessfully")
		}
		p.waitErr = errors.Join(p.waitErr, removeRoot(p.shutdownFile), removeRoot(p.root))
	})
	return p.waitErr
}

// WaitCleanup joins this owner's cached outcome without signalling a numeric
// PID. The pool can safely use it even after PID identity has been invalidated.
func (p *authProcess) WaitCleanup() error { return p.Wait() }

func (p *authProcess) Kill() error {
	if p.shutdownFile != "" {
		p.kill.Do(func() {
			// The supervised process requests shutdown without killing its OS
			// owner. Only verified owner exit authorizes root removal in Wait.
			// The advisory marker must never authorize skipping cleanup.
			_ = p.Process.Stdin().Close()
			if err := p.Process.Kill(); err != nil {
				p.killErr = errors.New("claudeagentacp: stop adapter failed")
			}
		})
		return p.killErr
	}
	if err := p.Process.Kill(); err != nil {
		return errors.New("claudeagentacp: stop adapter failed")
	}
	return nil
}

// Untrusted stderr may contain credentials (including CLI-managed host login).
// Drain without logging rather than relying on token-shaped text detection.
func (p *authProcess) Stderr() io.ReadCloser { return silentStderr{p.Process.Stderr()} }

func (p *authProcess) String() string {
	return fmt.Sprintf("claudeagentacp process pid=%d [redacted]", p.PID())
}
func (p *authProcess) GoString() string { return p.String() }

type silentStderr struct{ io.ReadCloser }

func (r silentStderr) Read(_ []byte) (int, error) {
	if _, err := io.Copy(io.Discard, r.ReadCloser); err != nil {
		return 0, errors.New("claudeagentacp: stderr drain failed")
	}
	return 0, io.EOF
}
