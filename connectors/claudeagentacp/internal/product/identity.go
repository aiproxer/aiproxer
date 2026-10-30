package product

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/acp"
)

// IdentityGate probes the resolved adapter's bundled CLI, never a CLI on PATH.
// Probe is the sole external boundary substitute; it must obey cancellation.
type IdentityGate struct {
	Timeout time.Duration
	Probe   func(context.Context, string, string, []string) ([]byte, error)
}

// Disclosure deliberately contains classifications, not decoded account data.
// RuntimeIdentity means provider/auth mechanism, never account email or token.
// AuthIdentity carries the latest adapter identity push (kind and plan label
// only); empty means unobserved since startup. PermissionPolicy carries the
// active posture (bypass default, or explicit deny).
type Disclosure struct {
	CredentialSource  string `json:"credential_source"`
	RuntimeIdentity   string `json:"runtime_identity"`
	PolicyDisposition string `json:"policy_disposition"`
	AuthIdentity      string `json:"auth_identity,omitempty"`
	PermissionPolicy  string `json:"permission_policy,omitempty"`
}

func (g IdentityGate) Check(ctx context.Context, executable, cwd string, env []string, source, mode string) (Disclosure, error) {
	var result Disclosure
	if err := serverPolicyConsent(mode); err != nil {
		return result, err
	}
	timeout := g.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	probe := g.Probe
	if probe == nil {
		probe = probeIdentity
	}
	data, err := probe(ctx, executable, cwd, append([]string(nil), env...))
	failure := func() (Disclosure, error) {
		return Disclosure{}, errors.New("claudeagentacp: runtime identity unavailable or mismatched; " + authRemediation(source))
	}
	if ctx.Err() != nil || len(data) > 64<<10 {
		return failure()
	}
	// The pinned logged-out CLI exits 1 while still printing valid JSON; that
	// is the only runner failure the gateway source may tolerate. Any other
	// probe failure — unrelated exit codes, signals, cancellation, a broken
	// runner — cannot distinguish a healthy pre-auth CLI from a damaged
	// install, so it refuses for every source.
	if err != nil {
		if source != "gateway" {
			return failure()
		}
		if !isSoleExitOne(err) {
			return failure()
		}
	}
	var status struct {
		LoggedIn     *bool  `json:"loggedIn"`
		AuthMethod   string `json:"authMethod"`
		Provider     string `json:"apiProvider"`
		APIKeySource string `json:"apiKeySource"`
		Subscription string `json:"subscriptionType"`
	}
	if json.Unmarshal(data, &status) != nil || status.Provider != "firstParty" {
		return failure()
	}
	valid := false
	switch source {
	case "api_key":
		valid = status.LoggedIn != nil && *status.LoggedIn &&
			status.AuthMethod == "api_key" && status.APIKeySource == "ANTHROPIC_API_KEY"
	case "subscription_token":
		valid = status.LoggedIn != nil && *status.LoggedIn &&
			status.AuthMethod == "oauth_token" && status.APIKeySource == ""
	case "host_login":
		valid = status.LoggedIn != nil && *status.LoggedIn &&
			status.AuthMethod == "claude.ai" && status.APIKeySource == "" && status.Subscription != ""
	case "gateway":
		// The CLI cannot see the adapter's gateway override. This gate only
		// proves the required negative precondition: no first-party
		// credential is active in the spawned root. Positive gateway
		// enforcement stays with the per-turn identity observation, which
		// carries the fresh session-query-bound evidence.
		valid = status.LoggedIn != nil && !*status.LoggedIn &&
			status.AuthMethod == "none" && status.APIKeySource == "" && status.Subscription == ""
	}
	if !valid {
		return failure()
	}
	switch strings.ToLower(status.Subscription) {
	case "", "pro", "max":
		result.PolicyDisposition = "honored_potential_server_policy"
	case "team", "enterprise":
		result.PolicyDisposition = "honored_organisation"
	default:
		return failure()
	}
	identity := "firstParty/"
	if source == "gateway" {
		identity = "gateway/"
	}
	result.CredentialSource = source
	result.RuntimeIdentity = identity + source
	return result, nil
}

// Gates has no runtime ownership or retained state. The task 3.1 assembly invokes
// Startup once per spawn via StarterWithGate, retains its resolved disclosure and
// re-checks Policy before each turn using the spawned root and pool processConfig.
type Gates struct {
	Policy           PolicyGate
	Identity         IdentityGate
	CredentialSource string
	AdminPolicy      string
	// GatewayHost is the validated non-secret endpoint host of the gateway
	// source. Requirement 5.2 names it in the startup and resolved-profile
	// disclosure; header values stay out.
	GatewayHost string
}

func (g Gates) Startup(ctx context.Context, executable, cwd string, env []string, log *slog.Logger) (Disclosure, error) {
	config := ""
	for _, item := range env {
		if value, ok := strings.CutPrefix(item, "CLAUDE_CONFIG_DIR="); ok {
			config = value
		}
	}
	policy, err := g.Policy.Check(ctx, config, g.AdminPolicy)
	if err != nil {
		return Disclosure{}, err
	}
	if err := serverPolicyConsent(g.AdminPolicy); err != nil {
		return Disclosure{}, err
	}
	identity, err := g.Identity.Check(ctx, executable, cwd, env, g.CredentialSource, g.AdminPolicy)
	if err != nil {
		return Disclosure{}, err
	}
	if g.CredentialSource == "gateway" && g.GatewayHost != "" {
		// Requirement 5.2: the startup disclosure names the gateway posture
		// and host. Configuration already validated the endpoint and the
		// per-turn observation re-binds it, so the host is disclosure, not
		// authorization.
		identity.RuntimeIdentity = "gateway/" + g.GatewayHost
	}
	if len(policy.Sources) > 0 && identity.PolicyDisposition != "honored_organisation" {
		identity.PolicyDisposition = "honored_administrator"
	}
	if log != nil {
		log.Info("claudeagentacp startup", "credential_source", identity.CredentialSource, "runtime_identity", identity.RuntimeIdentity, "policy_disposition", identity.PolicyDisposition)
	}
	return identity, nil
}

// isSoleExitOne permits only a normal exit 1 with no independent runner failure.
// OSProcessStarter.Run joins wait, cleanup and command errors; a clean run with
// exit 1 therefore has one non-nil cause. Single-cause wrappers are harmless,
// but finding an ExitError somewhere in a multi-cause tree is not sufficient.
// No non-nil cleanup outcome is treated as benign by this gate.
func isSoleExitOne(err error) bool {
	for err != nil {
		switch e := err.(type) {
		case *exec.ExitError:
			return e != nil && e.ProcessState != nil && e.Exited() && e.ExitCode() == 1
		case interface{ Unwrap() []error }:
			causes := e.Unwrap()
			if len(causes) != 1 {
				return false
			}
			err = causes[0]
		case interface{ Unwrap() error }:
			err = e.Unwrap()
		default:
			return false
		}
	}
	return false
}

// probeIdentity invokes the resolved bundled native CLI, not the adapter's --cli
// entrypoint, using the existing process-tree owner for the short-lived probe.
// The context-aware runner owns output pumps and tree cleanup. Stderr is
// discarded and the bounded stdout writer cancels the probe on overflow.
func probeIdentity(ctx context.Context, executable, cwd string, env []string) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if executable == "" || len(env) == 0 {
		return nil, errors.New("identity launch parameters required")
	}
	probeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	output := identityOutput{cancel: cancel}
	// Run's error is returned with the captured stdout: the logged-out CLI
	// exits 1 while still printing valid JSON, and the caller's per-source
	// exact match decides, not the runner's exit status alone.
	err := (acp.OSProcessStarter{}).Run(probeCtx, []string{executable, "auth", "status", "--json"}, cwd, env, &output, nil)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil && !json.Valid(bytes.TrimSpace(output.data)) {
		return nil, errors.New("identity probe failed")
	}
	return output.data, err
}

// Written only by the runner's stdout pump; read only after Run joins it.
type identityOutput struct {
	data   []byte
	cancel context.CancelFunc
}

func (w *identityOutput) Write(p []byte) (int, error) {
	if len(p) > (64<<10)-len(w.data) {
		w.cancel()
		return 0, errors.New("identity output exceeds limit")
	}
	w.data = append(w.data, p...)
	return len(p), nil
}
