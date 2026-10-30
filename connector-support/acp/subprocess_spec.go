package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// SubprocessConnectorSpec is the interface that concrete ACP CLI connectors
// (Cursor, Gemini, AGY) implement to provide vendor-specific configuration to
// the shared subprocess backend runner. The runner owns the runtime pool,
// stdio transport, handshake, history coordination, and event mapping; the
// spec provides only the vendor-specific pieces.
//
// Concrete connectors supply vendor-specific command construction,
// model resolution, and workspace requirements through this interface.
type SubprocessConnectorSpec interface {
	// VendorID is the backend identifier (e.g. "cursorcliacp", "geminicliacp").
	VendorID() string

	// VendorPrefix is the model prefix for this connector (e.g. "cursor", "gemini").
	// Used for model ID resolution and vendor prefix stripping.
	VendorPrefix() string

	// BuildCommand returns the subprocess command args, working directory, and
	// environment for spawning the ACP agent. The model and workspace are
	// resolved by the runner before calling this.
	BuildCommand(model string, workspace string) (cmd []string, cwd string, env []string, err error)

	// HandshakeProfile returns the vendor-specific handshake configuration
	// (protocol version, skip-authenticate, client capabilities, etc.).
	HandshakeProfile() HandshakeProfile

	// CancelProfile returns the vendor-specific cancellation method list.
	CancelProfile() CancelProfile

	// ServerRequestHandler returns the vendor-specific handler for inbound
	// JSON-RPC requests from the agent (permissions, questions, plan approvals).
	// May return nil to use the default headless handler.
	ServerRequestHandler() ServerRequestHandler

	// RequiresExplicitWorkspace returns true if the connector requires an
	// explicit workspace directory.
	RequiresExplicitWorkspace() bool

	// ResolveModel strips the vendor prefix from the effective model and
	// returns the model identifier to use for subprocess pooling and command
	// building. If model is "auto" or empty, returns the connector's default.
	ResolveModel(effectiveModel string) string
}

// SessionOptionsSpec opts an ACP connector into exact, advertised session
// configuration. InitialSessionOptions must include "model" with the resolved
// model; model is applied first, then other options in config-ID order. Effort
// values are validated against the refreshed model-dependent advertisement.
// Specs not implementing this interface retain CLI model selection and the
// legacy reasoning_effort RPC. Returned maps are borrowed only during handshake.
type SessionOptionsSpec interface {
	EffortConfigID() string
	InitialSessionOptions(model string) map[string]string
}

// PromptAdmissionSpec opts a negotiated-options connector into a bounded fresh
// checkpoint after exact options and before prompt input. Validation receives
// the previous accepted response retained on this transport, never client data.
// The response is copied verbatim into connector-owned prompt metadata.
type PromptAdmissionSpec interface {
	PromptAdmissionMethod() string
	PromptAdmissionMetadataKey() string
	ValidatePromptAdmission(sessionID, checkpointID string, previous, response json.RawMessage) error
}

// TransportOptionsSpec optionally configures stdio routing before its reader
// starts. The zero value preserves the existing headless replies.
type TransportOptionsSpec interface {
	TransportOptions() TransportOptions
}

// RuntimeIdentitySpec opts a connector into conversation-scoped runtime
// identity: the runtime pool keeps one live subprocess per conversation
// instead of one per workspace and model, so two unrelated conversations never
// share an agent process. Connectors that do not implement it keep the legacy
// shared runtime key.
type RuntimeIdentitySpec interface {
	// RuntimeIdentity returns the conversation identity for the call, or an
	// empty string to keep the legacy shared runtime key.
	RuntimeIdentity(call *aipapi.Call) string
}

// TransportOptions is immutable for a process lifetime. No connector callbacks
// run on the transport reader; active requests still use ServerRequestHandler.
type TransportOptions struct {
	RefuseOutOfTurnRequests bool
	// AutoApproveOutOfTurnPermissions selects an offered allow_once, never
	// allow_always, without executing connector callbacks on the reader.
	// RefuseOutOfTurnRequests takes precedence. The zero value is unchanged.
	AutoApproveOutOfTurnPermissions bool
}

// SubprocessBackendConfig configures the shared subprocess backend runner.
type SubprocessBackendConfig struct {
	// Protocol is the vendor-specific strategy that owns prompt-send and
	// stream-build. For ACP-session connectors (cursor/agy/gemini) use
	// NewACPProtocol(spec, log); for the Codex app-server use the
	// codexappserver package's protocol constructor.
	Protocol SubprocessProtocol

	// Workspace is the workspace resolution policy.
	Workspace WorkspacePolicy

	// Pool configures the subprocess lifecycle (idle reaping, stale kill).
	Pool RuntimePoolConfig

	// ProcessStarter spawns the subprocess. If nil, OSProcessStarter is used.
	ProcessStarter ProcessStarter

	// Log is optional for debug logging.
	Log *slog.Logger
}

// subprocessBackend is the shared ACP/Codex subprocess backend runner. It owns
// the lifecycle skeleton (workspace resolution, runtime pool, history
// divergence detection, ensure-process) and delegates the protocol-specific
// prompt-send and stream-build steps to a SubprocessProtocol.
//
// The runner acquires a runtime, spawns and initializes a process when needed,
// prepares the turn under its lock, and streams the response with that lock held.
// This lifecycle supports both ACP sessions and the Codex app-server protocol.
type subprocessBackend struct {
	proto     SubprocessProtocol
	workspace WorkspacePolicy
	pool      *RuntimePool
	starter   ProcessStarter
	history   *TranscriptHistoryCoordinator
	log       *slog.Logger
}

// SubprocessEngine is the exported subprocess prompt-turn surface used by
// product connectors. Implementations own pool lifecycle and stream mapping.
type SubprocessEngine interface {
	Open(ctx context.Context, call *aipapi.Call) (aipapi.ManagedEventStream, error)
	Close() error
}

// NewSubprocessBackend creates a shared subprocess backend from the given config.
func NewSubprocessBackend(cfg SubprocessBackendConfig) SubprocessEngine {
	log := cfg.Log
	if log == nil {
		log = cfg.Pool.Log
	}
	if log == nil {
		log = slog.Default()
	}
	starter := cfg.ProcessStarter
	if starter == nil {
		starter = OSProcessStarter{}
	}
	poolCfg := cfg.Pool
	if poolCfg.Log == nil {
		poolCfg.Log = log
	}
	pool := NewRuntimePool(poolCfg)
	return &subprocessBackend{
		proto:     cfg.Protocol,
		workspace: cfg.Workspace,
		pool:      pool,
		starter:   starter,
		history:   &TranscriptHistoryCoordinator{},
		log:       log,
	}
}

// Close shuts down the backend: closes the pool (which kills all subprocesses
// and closes their transports).
func (b *subprocessBackend) Close() error {
	return b.pool.Close()
}

// runtimeKey builds a RuntimeKey from the call's workspace, model, and client session.
func (b *subprocessBackend) runtimeKey(workspace, model, clientSession string) RuntimeKey {
	return RuntimeKey{
		Workspace:     workspace,
		Model:         model,
		ClientSession: clientSession,
	}
}

// resolveWorkspaceFromCall extracts workspace hints from the call and resolves
// the workspace directory using the configured policy.
func (b *subprocessBackend) resolveWorkspaceFromCall(call *aipapi.Call) (string, error) {
	return b.workspace.ResolveWorkspace(WorkspaceHintsFromCall(call))
}

// ensureProcess spawns and initializes the subprocess if not already running.
// Returns the live transport and session/thread id; the caller (orchestrator)
// passes these to the protocol's BindSession, which wraps them in a
// vendor-specific client. The model parameter should already be resolved
// (vendor prefix stripped) by the caller. sysPrompt carries canonical
// instructions for session creation when the protocol opts into
// authoritative system-prompt delivery; empty disables it.
func (b *subprocessBackend) ensureProcess(ctx context.Context, key RuntimeKey, model, workspace, processConfig, sysPrompt string) (EnsureProcessResult, bool, error) {
	createdWithPrompt := false
	res, err := b.pool.EnsureProcess(ctx, key, SpawnHandshakeStrategy{
		Spawn: func() ([]string, Process, Transport, error) {
			cmd, cwd, env, err := b.proto.BuildSpawnCommand(model, workspace, processConfig)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("%s: build command: %w", b.proto.Label(), err)
			}
			if len(cmd) == 0 {
				return nil, nil, nil, fmt.Errorf("%s: empty command", b.proto.Label())
			}
			var proc Process
			if starter, ok := b.starter.(interface {
				StartContext(context.Context, []string, string, []string) (Process, error)
			}); ok {
				proc, err = starter.StartContext(ctx, cmd, cwd, env)
			} else {
				proc, err = b.starter.Start(cmd, cwd, env)
			}
			if err != nil {
				return nil, nil, nil, fmt.Errorf("%s: start: %w", b.proto.Label(), err)
			}
			var options TransportOptions
			if proto, ok := b.proto.(TransportOptionsSpec); ok {
				options = proto.TransportOptions()
			}
			return cmd, proc, newStdioTransportWithOptions(proc, b.log, options), nil
		},
		Handshake: func(ctx context.Context, transport Transport) (string, error) {
			if sysPrompt != "" {
				if h, ok := b.proto.(SystemPromptSessionProtocol); ok {
					sid, err := h.HandshakeWithSystemPrompt(ctx, transport, model, workspace, sysPrompt)
					if err != nil {
						return "", fmt.Errorf("%s: %w", b.proto.Label(), err)
					}
					createdWithPrompt = true
					return sid, nil
				}
			}
			sid, err := b.proto.Handshake(ctx, transport, model, workspace)
			if err != nil {
				return "", fmt.Errorf("%s: %w", b.proto.Label(), err)
			}
			return sid, nil
		},
		Log:              b.log,
		LogPrefix:        b.proto.Label(),
		ProcessConfigKey: processConfig,
	})
	if err != nil {
		return EnsureProcessResult{}, false, err
	}
	// A prepared process can supply its immutable root-scoped fingerprint only
	// after spawn. Publish it in the existing pool configuration, under the claim.
	if rt := b.pool.Get(key); rt != nil {
		rt.mu.Lock()
		if proc, ok := rt.proc.(interface{ RuntimeFingerprint() string }); ok {
			rt.processConfig = proc.RuntimeFingerprint()
		}
		rt.mu.Unlock()
	}
	return res, createdWithPrompt, nil
}

// Open opens a canonical event stream for one route candidate via the
// subprocess. This is the single lifecycle skeleton shared by the ACP session
// protocol (cursor/agy/gemini) and the Codex app-server protocol; the
// protocol-specific prompt-send and stream-build steps are delegated to
// [SubprocessProtocol].
func (b *subprocessBackend) Open(ctx context.Context, call *aipapi.Call) (aipapi.ManagedEventStream, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%s: %w", b.proto.Label(), aipapi.ErrNilContext)
	}
	if err := b.proto.ValidateCall(call); err != nil {
		return nil, err
	}

	// Resolve workspace.
	workspace, err := b.resolveWorkspaceFromCall(call)
	if err != nil {
		return nil, fmt.Errorf("%s: workspace: %w", b.proto.Label(), err)
	}

	// Resolve model (protocol-specific prefix stripping + defaults).
	model := b.proto.ResolveModel(call)

	// Canonical instructions for authoritative session delivery. Empty unless
	// the protocol's spec opts in; other connectors never take this path.
	sysPrompt, sysPromptOK := sessionSystemPromptForCall(b.proto, call)

	// Resolve the conversation identity that keys the runtime pool. A protocol
	// that opts into conversation-scoped runtimes supplies it; the legacy
	// shared key remains the default.
	clientSession := b.runtimeIdentity(call)

	key := b.runtimeKey(workspace, model, clientSession)

	// Acquire runtime from pool (handles idle reaping) before claiming the turn.
	acquired, err := b.pool.Acquire(key)
	if err != nil {
		return nil, fmt.Errorf("%s: acquire: %w", b.proto.Label(), err)
	}

	// Claim the runtime for this turn. Claiming is atomic with the process-scoped
	// config reset: a single stdio subprocess cannot carry two concurrent turns,
	// and killing an in-use transport would corrupt the in-flight turn, so a
	// concurrent peer waits for the running turn to release the runtime and is
	// refused explicitly instead of killing it. On a successful claim, a live
	// child spawned with a different process config (Codex model_verbosity) is
	// killed and its transcript marker reset so the new child receives a
	// complete replay below.
	processConfig := b.proto.ResolveProcessConfig(call)
	// Root-scoped configuration cannot be reconstructed from client input. Keep
	// the spawned fingerprint for the claim, then validate its freshness below.
	acquired.mu.Lock()
	if _, ok := acquired.proc.(interface{ RuntimeFingerprint() string }); ok {
		processConfig = acquired.processConfig
	}
	acquired.mu.Unlock()
	if _, outcome := b.pool.claimForTurn(ctx, key, processConfig); outcome != claimAcquired {
		// This request never owned a claim, so it cannot Release. Prune only
		// its empty slot; an incumbent claimant/retirement remains protected.
		if b.pool.cfg.MaxRuntimes > 0 {
			b.pool.removeRetiredSlot(key, acquired)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, b.claimRefusal(key, processConfig, outcome)
	}

	// The turn is prepared on the claimed runtime. The connector never retries
	// a failed turn itself: a pre-output failure is classified for the host,
	// which may select another candidate, and the next request of the same
	// conversation replays the history into a replacement process.
	stream, err := b.runTurn(ctx, call, key, model, workspace, processConfig, sysPrompt, sysPromptOK)
	if err != nil {
		b.pool.Release(key)
		return nil, err
	}
	return stream, nil
}

// runtimeIdentity resolves the conversation identity used to key the runtime
// pool for one request.
func (b *subprocessBackend) runtimeIdentity(call *aipapi.Call) string {
	if provider, ok := b.proto.(RuntimeIdentityProvider); ok {
		if identity := strings.TrimSpace(provider.RuntimeIdentity(call)); identity != "" {
			return identity
		}
	}
	return CallClientSession(call)
}

// claimRefusal maps a refused claim to an explicit error. The two refusals
// are distinct: a held runtime is a scheduling conflict, an unusable runtime
// cannot serve turns at all.
func (b *subprocessBackend) claimRefusal(key RuntimeKey, processConfig string, outcome claimOutcome) error {
	switch outcome {
	case claimRetiring:
		return fmt.Errorf("%s: runtime %s is retiring; its previous process is still being retired", b.proto.Label(), key)
	case claimUnusable:
		return fmt.Errorf("%s: runtime %s cannot serve a turn; its process cleanup was not confirmed", b.proto.Label(), key)
	default:
		return fmt.Errorf("%s: process config %q cannot be applied: a turn is in flight on runtime %s", b.proto.Label(), processConfig, key)
	}
}

// runTurn prepares one turn on the claimed runtime: it validates reuse,
// computes the transcript, ensures the process, sends the prompt and builds the
// event stream. The caller owns the turn claim.
func (b *subprocessBackend) runTurn(ctx context.Context, call *aipapi.Call, key RuntimeKey, model, workspace, processConfig, sysPrompt string, sysPromptOK bool) (aipapi.ManagedEventStream, error) {
	// An opted-in process may withdraw reuse after observing external policy.
	// Check only under the existing turn claim; this never creates a second owner.
	rt := b.pool.Get(key)
	if rt != nil {
		rt.mu.Lock()
		proc := rt.proc
		rt.mu.Unlock()
		if validator, ok := proc.(interface {
			ValidateReuse(context.Context) (bool, error)
		}); ok {
			reuse, err := validator.ValidateReuse(ctx)
			if err != nil || !reuse {
				_ = b.pool.KillRuntimeContext(ctx, key)
				rt.ResetHistoryState()
			}
			if err != nil {
				return nil, err
			}
		}
	}

	// Compute the transcript-based prompt from the runtime's history state.
	// On divergence (edited/truncated history), reset the agent process so it
	// re-receives the full transcript; ensureProcess spawns a fresh one.
	state := historyState{}
	if rt != nil {
		state = rt.HistoryState()
	}
	result := b.history.ComputeHistoryAndUserMessage(call.Messages, state)
	if result.ResetNeeded {
		_ = b.pool.KillRuntimeContext(ctx, key)
	}

	// Ensure process is running (spawn + handshake). The transport is stored
	// internally for lifecycle management; the protocol binds it into a session.
	res, createdWithPrompt, err := b.ensureProcess(ctx, key, model, workspace, processConfig, sysPrompt)
	if err != nil {
		return nil, err
	}

	// Authoritative instruction delivery. A fresh session created with the
	// current instructions, or a live session already carrying them, owns the
	// prompt: strip them from prompt text. Drift rotates to a fresh session
	// with history replay; rotation failure degrades explicitly to flattened
	// text, keeping the turn servable instead of failing it.
	promptCall := call
	if sysPromptOK {
		delivered := createdWithPrompt
		if rt := b.pool.Get(key); rt != nil {
			if createdWithPrompt {
				rt.RotateSession(res.SessionID, sysPrompt)
			} else if rt.SessionInstructions() != sysPrompt {
				if rp, ok := b.proto.(SystemPromptSessionProtocol); ok {
					if newSid, rerr := rp.RotateSessionSystemPrompt(ctx, res.Transport, model, sysPrompt); rerr != nil {
						b.log.Warn("acp: session rotation failed; falling back to prompt-text instructions", "error", truncErrDetail(rerr, 256))
					} else {
						res.SessionID = newSid
						rt.RotateSession(newSid, sysPrompt)
						rt.ResetHistoryState()
						result = b.history.ComputeHistoryAndUserMessage(call.Messages, historyState{})
						delivered = true
					}
				}
			} else {
				delivered = true
			}
		}
		if delivered {
			promptCall = b.deliveredPromptCall(call, result.UserMessage)
		}
	}

	// Bind the live transport + session id into a protocol session that owns
	// the prompt-send and stream-build steps.
	session := b.proto.BindSession(res.Transport, res.SessionID)

	// Send the prompt/turn. The session owns the request body construction
	// (session/prompt with transcript blocks, or turn/start with text input).
	body, rpcID, err := session.SendPrompt(ctx, model, result.UserMessage, promptCall)
	if err != nil {
		if isSessionOptionsFailure(err) || isPromptAdmissionFailure(err) {
			// The runtime is still claimed here. Withdraw ambiguous configuration
			// before release, and replay history into the replacement process.
			_ = b.pool.KillRuntimeContext(ctx, key)
			if rt := b.pool.Get(key); rt != nil {
				rt.ResetHistoryState()
			}
		}
		return nil, fmt.Errorf("%s: %w", b.proto.Label(), err)
	}

	// Commit the new history state after the prompt was accepted. On a client
	// retry with the same messages, ComputeHistoryAndUserMessage hits the
	// "same messages as last turn" branch and re-sends just the last user
	// message.
	if rt := b.pool.Get(key); rt != nil {
		rt.SetHistoryState(result.HistoryState)
	}

	// Build the protocol-specific event stream, wrapped to release the pool on
	// close (and flush tool summaries for ACP).
	stream, err := session.BuildStream(ctx, body, rpcID, b.pool, key, call.MaxPendingWireEvents)
	if err != nil {
		return nil, err
	}
	return stream, nil
}

// poolManagedStream wraps a promptStream to release the runtime pool on Close
// and flush pending tool summaries when the stream ends (EOF).
type poolManagedStream struct {
	inner           *promptStream
	pool            *RuntimePool
	key             RuntimeKey
	toolSink        ToolUpdateSink
	closed          bool
	flushed         bool
	admission       bool
	completed       bool
	cancelled       bool
	admissionFailed bool
	mu              sync.Mutex
}

var _ aipapi.ManagedEventStream = (*poolManagedStream)(nil)

func (s *poolManagedStream) Recv(ctx context.Context) (aipapi.Event, error) {
	ev, err := s.inner.Recv(ctx)
	if s.admission {
		s.mu.Lock()
		if ev.Kind == aipapi.EventError || (err != nil && !errors.Is(err, io.EOF)) {
			s.admissionFailed = true
		}
		if ev.Kind == aipapi.EventResponseFinished {
			s.completed = !s.admissionFailed && !s.cancelled && ev.FinishReason != "cancelled"
		}
		s.mu.Unlock()
	}
	// When the inner stream returns EOF, flush incomplete tool summaries
	// as text deltas BEFORE returning EOF so the consumer sees them.
	if err != nil && !s.flushed {
		s.mu.Lock()
		if !s.flushed && s.toolSink != nil {
			s.flushed = true
			evts := s.toolSink.FlushIncomplete()
			s.mu.Unlock()
			// If there are flushed events, return the first one now and
			// buffer the rest for subsequent Recv calls. The inner stream
			// is at EOF, so we inject these as the final events.
			if len(evts) > 0 {
				for i := 1; i < len(evts); i++ {
					_ = s.inner.PushPendingLocked(evts[i])
				}
				return evts[0], nil
			}
		} else {
			s.mu.Unlock()
		}
	}
	return ev, err
}

func (s *poolManagedStream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	withdraw := s.admission && !s.completed
	s.mu.Unlock()

	// The runtime is detached before the claim is released: the detachment is
	// the retirement reservation, so a peer that claims the released slot can
	// never reuse this process, and the retirement that follows can only ever
	// target the incarnation this turn owned. A turn that ends without a clean
	// completion must not leave a reusable process behind.
	var detached DetachedRuntime
	detachedOK := false
	if withdraw {
		s.pool.MarkWithdrawn(s.key)
		detached, detachedOK = s.pool.DetachRuntime(s.key)
	}

	// The turn claim is released exactly once, and the release never waits for
	// a retirement: a retirement can block until the process tree is confirmed
	// extinct, and a blocked retirement must never keep a runtime claimed.
	s.pool.Release(s.key)

	if detachedOK {
		// The retirement of the incarnation this turn owned is joined here: the
		// fail-closed lifecycle requires the process tree to be confirmed
		// extinct before the instance state is released. The join can only
		// target the detached incarnation, never a successor's process.
		<-detached.done
	}
	return s.inner.Close()
}

func (s *poolManagedStream) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	if s.admission {
		s.mu.Lock()
		s.cancelled, s.completed = true, false
		s.mu.Unlock()
	}
	return s.inner.Cancel(ctx, cause)
}
