package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/acp"
	"github.com/aiproxer/aiproxer/connectors/claudeagentacp/internal/product"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

// Private construction dependencies substitute only filesystem/process and
// certification boundaries. Production has no fixture flags or global override.
type engineDeps struct {
	host     []string
	starter  acp.ProcessStarter
	policy   product.PolicyGate
	identity product.IdentityGate
	log      *slog.Logger
	certify  func(context.Context, product.AdapterResolver, Config, []string, []string, string) (product.RuntimeProfile, product.IdentityExtension, error)
}

func productionDeps() engineDeps {
	return engineDeps{host: os.Environ(), starter: acp.OSProcessStarter{}, log: slog.Default(), certify: certifyDeployment}
}

func certifyDeployment(ctx context.Context, r product.AdapterResolver, cfg Config, host, env []string, cwd string) (product.RuntimeProfile, product.IdentityExtension, error) {
	p, err := r.Resolve(ctx, cfg.Executable, host, cwd, env, cfg.AdminPolicy)
	if err != nil {
		return p, product.IdentityExtension{}, err
	}
	connector, err := os.Executable()
	if err != nil {
		return p, product.IdentityExtension{}, errors.New("claudeagentacp: connector artifact unavailable")
	}
	extension, err := r.ResolveIdentityExtension(ctx, p, connector, p.NodeExecutable, cwd, env, cfg.AdminPolicy)
	return p, extension, err
}

type engine struct {
	backend    acp.SubprocessEngine
	spec       *product.ClaudeSpec
	stateDir   string
	mu         sync.RWMutex
	lifeMu     sync.RWMutex
	disclosure product.Disclosure
	profileID  string
	closed     bool
	closeErr   error
	cleanupErr error // discovery cleanup is outside the shared runtime pool; guarded by mu
	starter    acp.ProcessStarter
	executable string
	workspace  acp.WorkspacePolicy
	discovery  chan struct{} // one caller-owned discovery, cancellable admission
	log        *slog.Logger
}

func newEngine(ctx context.Context, cfg Config, secrets map[string]string, deps engineDeps) (_ *engine, err error) {
	if ctx == nil {
		return nil, aipapi.ErrNilContext
	}
	auth, err := product.NewAuthEnv(product.AuthConfig{CredentialSource: cfg.CredentialSource, PermissionPolicy: cfg.PermissionPolicy, EnvAllowlist: cfg.EnvAllowlist, Gateway: cfg.Gateway}, secrets, deps.host)
	if err != nil {
		return nil, err
	}
	if cfg.AdminPolicy != "honor" {
		return nil, errors.New("claudeagentacp: potential organisation/server-delivered policy; select admin_policy: honor to honor its authority")
	}
	state, err := os.MkdirTemp("", "aip-claudeagentacp-")
	if err != nil {
		return nil, errors.New("claudeagentacp: create instance state directory failed")
	}
	defer func() {
		if err != nil && !errors.Is(err, acp.ErrCleanupIncomplete) {
			err = errors.Join(err, removeState(state))
		}
	}()
	prepared, err := auth.Prepare(state, []string{"certification"})
	if err != nil {
		return nil, err
	}
	resolver := product.AdapterResolver{Cache: &acp.ExecutableCache{}, Policy: deps.policy}
	profile, extension, certErr := deps.certify(ctx, resolver, cfg, deps.host, prepared.Environment, cfg.DefaultWorkspace)
	if err := errors.Join(certErr, prepared.Close()); err != nil {
		return nil, err
	}
	spec, err := product.NewClaudeSpec(profile.Executable, cfg.DefaultWorkspace, auth)
	if err != nil {
		return nil, err
	}
	if _, err := extension.Command(cfg.CredentialSource); err != nil {
		return nil, err
	}
	resolvedIdentity := "unobserved"
	if cfg.CredentialSource == "gateway" && auth.GatewayHost() != "" {
		// The resolved profile discloses the gateway posture and host before
		// the first conversation; header values are never part of evidence.
		resolvedIdentity = "gateway/" + auth.GatewayHost()
	}
	e := &engine{spec: spec, stateDir: state, profileID: extension.BundleSHA256, disclosure: product.Disclosure{CredentialSource: cfg.CredentialSource, RuntimeIdentity: resolvedIdentity, PolicyDisposition: "honored_potential_server_policy", PermissionPolicy: cfg.PermissionPolicy}, log: deps.log}
	start, err := extension.Starter(cfg.CredentialSource, deps.starter)
	if err != nil {
		return nil, err
	}
	gates := product.Gates{Policy: deps.policy, Identity: deps.identity, CredentialSource: cfg.CredentialSource, AdminPolicy: cfg.AdminPolicy, GatewayHost: auth.GatewayHost()}
	start = auth.ContextStarter(state, start, func(ctx context.Context, _ []string, cwd string, env []string) (string, func(context.Context) (bool, error), error) {
		current, ext, err := deps.certify(ctx, resolver, cfg, deps.host, env, cwd)
		if err != nil {
			return "", nil, err
		}
		if current != profile || ext != extension {
			return "", nil, errors.New("claudeagentacp: certified deployment changed; reconfigure the instance")
		}
		disclosure, err := gates.Startup(ctx, profile.CLIExecutable, cwd, env, deps.log)
		if err != nil {
			return "", nil, err
		}
		config := environmentValue(env, "CLAUDE_CONFIG_DIR")
		policy, err := deps.policy.Check(ctx, config, cfg.AdminPolicy)
		if err != nil {
			return "", nil, err
		}
		disclosure.PermissionPolicy = cfg.PermissionPolicy
		e.mu.Lock()
		e.disclosure = disclosure
		e.mu.Unlock()
		return policy.Fingerprint, func(ctx context.Context) (bool, error) {
			current, err := deps.policy.Check(ctx, config, cfg.AdminPolicy)
			return current.Fingerprint == policy.Fingerprint, err
		}, nil
	})
	e.starter = start
	e.executable = profile.Executable
	e.workspace = acp.WorkspacePolicy{DefaultDir: cfg.DefaultWorkspace, RequireExplicit: cfg.DefaultWorkspace == ""}
	e.discovery = make(chan struct{}, 1)
	e.backend = acp.NewSubprocessBackend(acp.SubprocessBackendConfig{
		Protocol:       acp.NewACPProtocol(spec, deps.log),
		Workspace:      acp.WorkspacePolicy{DefaultDir: cfg.DefaultWorkspace, RequireExplicit: cfg.DefaultWorkspace == ""},
		Pool:           acp.RuntimePoolConfig{IdleTimeout: time.Duration(cfg.IdleTimeoutSeconds * float64(time.Second)), ClaimWait: time.Duration(cfg.TurnWaitSeconds * float64(time.Second)), MaxRuntimes: cfg.MaxRuntimes},
		ProcessStarter: start, Log: deps.log,
	})
	return e, nil
}

func environmentValue(env []string, key string) string {
	for _, item := range env {
		if value, ok := strings.CutPrefix(item, key+"="); ok {
			return value
		}
	}
	return ""
}

func removeState(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return errors.New("claudeagentacp: remove instance state directory failed")
	}
	return nil
}

// LoadModels is the host's refresh boundary. The lease includes synchronous
// transport/process/root retirement; Close cannot remove state underneath it.
func (e *engine) LoadModels(ctx context.Context) (modelinventory.Snapshot, error) {
	if ctx == nil {
		return modelinventory.Snapshot{}, aipapi.ErrNilContext
	}
	e.lifeMu.RLock()
	defer e.lifeMu.RUnlock()
	if e.closed {
		return modelinventory.Snapshot{}, errors.New("claudeagentacp: instance closed")
	}
	// Freshness transitions happen inside loadModels while owning discovery
	// admission, so admission order decides the final evidence state.
	snapshot, err := e.loadModels(ctx, true)
	return snapshot, e.spec.ClassifyError(err, aipapi.PhasePreOutput)
}

// The caller already holds lifeMu. No context, goroutine or timer survives this
// operation. Cold Opens recheck after admission so one load serves all waiters.
func (e *engine) loadModels(ctx context.Context, refresh bool) (modelinventory.Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return modelinventory.Snapshot{}, err
	}
	e.mu.RLock()
	cleanupErr := e.cleanupErr
	e.mu.RUnlock()
	if cleanupErr != nil {
		return modelinventory.Snapshot{}, cleanupErr
	}
	if !refresh && e.spec.ResolveModel("") != "" {
		return e.spec.Inventory().LoadModels(ctx)
	}
	select {
	case e.discovery <- struct{}{}:
		defer func() { <-e.discovery }()
	case <-ctx.Done():
		return modelinventory.Snapshot{}, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return modelinventory.Snapshot{}, err
	}
	if !refresh && e.spec.ResolveModel("") != "" {
		return e.spec.Inventory().LoadModels(ctx)
	}
	// Freshness is decided while owning discovery admission, so admission
	// order decides the final evidence state: a failure invalidates the
	// evidence it attempted to refresh, and a later success republishes it.
	// Invalidating outside admission would let a stale failure resuming after
	// a newer success clobber valid evidence, or let a failure after a
	// success skip invalidation via a pre-admission epoch. Pre-admission
	// errors (caller cancellation, terminal cleanup) run no discovery and
	// leave the evidence untouched. A refresh that publishes and then fails
	// in transport cleanup still invalidates, so its facts are never trusted.
	if err := e.discoverModels(ctx); err != nil {
		if e.spec != nil {
			e.spec.InvalidateGradedEffortEvidence()
		}
		return modelinventory.Snapshot{}, err
	}
	// Read while still owning discovery admission: this is exactly the snapshot
	// published by this load, not a later concurrent host refresh.
	return e.spec.Inventory().LoadModels(ctx)
}

func (e *engine) discoverModels(ctx context.Context) (err error) {
	e.mu.RLock()
	cleanupErr := e.cleanupErr
	e.mu.RUnlock()
	if cleanupErr != nil {
		return cleanupErr
	}
	cwd := e.workspace.DefaultDir
	if cwd == "" {
		cwd, err = os.MkdirTemp(e.stateDir, "discovery-")
		if err != nil {
			return errors.New("claudeagentacp: create discovery workspace failed")
		}
		defer func() {
			if !errors.Is(err, acp.ErrCleanupIncomplete) {
				err = errors.Join(err, removeState(cwd))
			}
		}()
	} else if _, err := (acp.WorkspacePolicy{DefaultDir: cwd}).ResolveWorkspace(nil); err != nil {
		return errors.New("claudeagentacp: unusable discovery workspace")
	}
	// newEngine always supplies the same context-aware AuthEnv/extension starter
	// to discovery and the pool, including certification, source and policy gates.
	starter, ok := e.starter.(interface {
		StartContext(context.Context, []string, string, []string) (acp.Process, error)
	})
	if !ok {
		return errors.New("claudeagentacp: context-aware gated starter required")
	}
	proc, err := starter.StartContext(ctx, []string{e.executable}, cwd, nil)
	if err != nil {
		return err
	}
	transport := acp.NewStdioTransportWithOptions(proc, nil, e.spec.TransportOptions())
	defer func() {
		err = errors.Join(err, transport.Close())
		// Transport Close joins the process owner and preserves incomplete cleanup.
		if errors.Is(err, acp.ErrCleanupIncomplete) {
			e.mu.Lock()
			e.cleanupErr = acp.ErrCleanupIncomplete
			e.mu.Unlock()
		}
	}()
	hp := e.spec.HandshakeProfile()
	var initialized struct {
		ProtocolVersion int `json:"protocolVersion"`
	}
	if err := discoveryRPC(ctx, transport, 1, "initialize", map[string]any{"protocolVersion": hp.ProtocolVersion, "clientCapabilities": hp.ClientCapabilities, "clientInfo": map[string]string{"name": "AIProxer", "version": "1"}}, &initialized); err != nil {
		return err
	}
	if initialized.ProtocolVersion != 1 {
		return errors.New("claudeagentacp: invalid discovery initialization")
	}
	// The gateway override belongs to the adapter process: authenticate before
	// any discovery session so that the pooled and discovery paths establish
	// the exact same connector-owned provider state from one payload.
	modelsDiscoveryID := 2
	if len(hp.AuthenticateParams) > 0 {
		var acknowledged map[string]any
		if err := discoveryRPC(ctx, transport, 2, "authenticate", hp.AuthenticateParams, &acknowledged); err != nil {
			return err
		}
		modelsDiscoveryID = 3
	}
	var result product.ModelDiscovery
	if err := discoveryRPC(ctx, transport, modelsDiscoveryID, "_aip/models_v1", map[string]any{"version": 1, "cwd": cwd}, &result); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return e.spec.PublishModels(result, time.Now())
}

// Private versioned discovery uses the existing transport, not a second SDK
// client/session stack. Never retain peer messages, data or decoder diagnostics.
func discoveryRPC(ctx context.Context, transport acp.Transport, id int, method string, params any, result any) error {
	invalid := func() error { return errors.New("claudeagentacp: invalid model discovery response") }
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return invalid()
	}
	raw, err := transport.CallUnary(ctx, body, 200)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("claudeagentacp: model discovery transport failed")
	}
	var response struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int             `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}
	if len(raw) > 8<<20 || json.Unmarshal(raw, &response) != nil || response.JSONRPC != "2.0" || response.ID != id {
		return invalid()
	}
	if len(response.Error) > 0 {
		var rpc struct {
			Code *int64 `json:"code"`
		}
		if len(response.Result) > 0 || json.Unmarshal(response.Error, &rpc) != nil || rpc.Code == nil {
			return invalid()
		}
		return &acp.RPCError{Method: method, Code: *rpc.Code, ErrorKind: acp.ErrorKindFromData(response.Error)}
	}
	if len(response.Result) == 0 || string(response.Result) == "null" || json.Unmarshal(response.Result, result) != nil {
		return invalid()
	}
	return nil
}

func (e *engine) Open(ctx context.Context, call *aipapi.Call) (aipapi.ManagedEventStream, error) {
	if ctx == nil {
		return nil, aipapi.ErrNilContext
	}
	e.lifeMu.RLock()
	defer e.lifeMu.RUnlock()
	if e.closed {
		return nil, errors.New("claudeagentacp: instance closed")
	}
	call, dropped := withoutClientInstructions(call)
	if dropped > 0 && e.log != nil {
		e.log.Warn("claudeagentacp: client instructions dropped; Claude Code keeps its own system prompt", "dropped_messages", dropped)
	}
	if err := validateWorkspacePathExtension(call); err != nil {
		// The validator already owns a safe, workspace-specific capability reason.
		return nil, err
	}
	if _, err := e.workspace.ResolveWorkspace(acp.WorkspaceHintsFromCall(call)); err != nil {
		return nil, e.spec.ClassifyError(err, aipapi.PhasePreOutput)
	}
	if _, err := e.loadModels(ctx, false); err != nil {
		return nil, e.spec.ClassifyError(err, aipapi.PhasePreOutput)
	}
	stream, err := e.backend.Open(ctx, call)
	if err != nil {
		return nil, e.spec.ClassifyError(err, aipapi.PhasePreOutput)
	}
	return &classifiedStream{ManagedEventStream: stream, spec: e.spec, phase: aipapi.PhasePreOutput, onAuthStatus: e.noteAuthStatus}, nil
}

// noteAuthStatus records an adapter identity push into diagnostics. It runs
// on the turn's Recv path while the backend holds no locks; engine state is
// guarded by the engine mutex.
func (e *engine) noteAuthStatus(summary string) {
	if e.log != nil {
		e.log.Info("claudeagentacp: auth identity update", "summary", summary)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.disclosure.AuthIdentity != summary {
		e.disclosure.AuthIdentity = summary
	}
}

func (e *engine) Close() error {
	e.lifeMu.Lock()
	defer e.lifeMu.Unlock()
	if e.closed {
		return e.closeErr
	}
	e.closed = true
	e.mu.RLock()
	cleanupErr := e.cleanupErr
	e.mu.RUnlock()
	e.closeErr = errors.Join(e.backend.Close(), cleanupErr)
	if e.closeErr == nil {
		e.closeErr = removeState(e.stateDir)
	}
	return e.closeErr
}

type classifiedStream struct {
	aipapi.ManagedEventStream
	spec  *product.ClaudeSpec
	phase aipapi.OutputPhase
	// onAuthStatus consumes adapter identity pushes into diagnostics. It
	// keeps them off the client-visible stream: identity posture is
	// operator diagnostics, not turn content.
	onAuthStatus func(summary string)
}

func (s *classifiedStream) Recv(ctx context.Context) (aipapi.Event, error) {
	for {
		event, err := s.ManagedEventStream.Recv(ctx)
		if err != nil && !errors.Is(err, io.EOF) {
			return aipapi.Event{}, s.spec.ClassifyError(err, s.phase)
		}
		if err == nil && event.Kind == aipapi.EventWarning && event.WarningCode == "auth_status" {
			if s.onAuthStatus != nil {
				s.onAuthStatus(event.WarningMessage)
			}
			continue
		}
		if event.Kind == aipapi.EventUsageDelta {
			// The ACP usage object reports inputTokens outside the cached
			// and the written prompt subsets, so both relations are declared
			// with the counters they qualify: a frontend then counts the
			// prompt in its own semantics instead of reporting an input
			// smaller than the cache reads and writes it also reports.
			event.Accounting.InputTokensExcludeCacheRead = true
			event.Accounting.InputTokensExcludeCacheWrite = true
		}
		if event.Kind == aipapi.EventTextDelta || event.Kind == aipapi.EventReasoningDelta {
			s.phase = aipapi.PhasePostOutput
		}
		return event, err
	}
}

// withoutClientInstructions returns call without the client's instructions and
// system/developer messages, plus how many were dropped. Claude Code is the
// agent here: a client harness's system prompt, persona and tool catalogue
// must never reach it. The caller's call is not modified.
func withoutClientInstructions(call *aipapi.Call) (*aipapi.Call, int) {
	if call == nil {
		return nil, 0
	}
	dropped := len(call.Instructions)
	messages := make([]aipapi.Message, 0, len(call.Messages))
	for _, m := range call.Messages {
		if m.Role == aipapi.RoleSystem || m.Role == aipapi.RoleDeveloper {
			dropped++
			continue
		}
		messages = append(messages, m)
	}
	if dropped == 0 {
		return call, 0
	}
	out := *call
	out.Instructions = nil
	out.Messages = messages
	return &out, dropped
}
