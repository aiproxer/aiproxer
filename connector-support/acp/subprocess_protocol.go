package acp

import (
	"context"
	"io"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// SubprocessProtocol is the strategy injected into the shared subprocess
// orchestrator (subprocessBackend). It owns the protocol-specific steps that
// differ between the ACP session protocol (cursorcliacp, geminicliacp,
// agycliacp) and the Codex app-server protocol: model resolution, subprocess
// command building, handshake, and the prompt-send + stream-build flow. The
// orchestrator owns the lifecycle skeleton (workspace resolution, pool
// acquire/release, history divergence detection, ensure-process) that is
// identical across protocols.
//
// This collapses the previously parallel subprocessBackend.Open and
// codexBackend.Open methods into a single orchestrator driven by this
// strategy, while keeping the genuinely different prompt/stream steps behind
// the protocol seam.
type SubprocessProtocol interface {
	// Label is the human-readable protocol name used in error wrapping
	// (e.g. "acp subprocess", "codex app-server").
	Label() string

	// ValidateCall rejects calls the protocol cannot serve (nil call, tool
	// constraints, etc.).
	ValidateCall(call *aipapi.Call) error

	// ResolveModel extracts the route model from the call and applies
	// vendor-specific prefix stripping and defaults. Returns the model id to
	// use for runtime-pool keying and command building.
	ResolveModel(call *aipapi.Call) string

	// ResolveProcessConfig returns the process-scoped configuration variant for
	// this call. ACP-session protocols return an empty string; Codex uses the
	// effective model verbosity.
	ResolveProcessConfig(call *aipapi.Call) string

	// BuildSpawnCommand returns the subprocess command args, working directory,
	// and environment for spawning the agent. The model and workspace are
	// resolved by the orchestrator before calling this.
	BuildSpawnCommand(model, workspace, processConfig string) (cmd []string, cwd string, env []string, err error)

	// Handshake runs the vendor-specific initialize/session-or-thread exchange
	// over the transport and returns the session/thread id to register in the
	// runtime pool. model and workspace are the resolved values the orchestrator
	// already computed; protocols whose handshake does not need them (e.g. ACP
	// session/new) may ignore them.
	Handshake(ctx context.Context, transport Transport, model, workspace string) (sessionID string, err error)

	// BindSession wraps the live transport and session/thread id into a
	// per-request session that owns the prompt-send and stream-build steps.
	// Called after a successful ensure-process; the session owns its own client
	// wrapper over transport.
	BindSession(transport Transport, sessionID string) SubprocessProtocolSession
}

// SubprocessProtocolSession is the per-request handle returned by
// [SubprocessProtocol.BindSession]. It owns the protocol-specific prompt-send
// and stream-construction steps using the bound transport.
type SubprocessProtocolSession interface {
	// SendPrompt sends the prompt/turn request and returns the response body
	// stream plus the rpc id used to match the terminal response. The orchestrator
	// commits the history state after this succeeds.
	SendPrompt(ctx context.Context, model, userMessage string, call *aipapi.Call) (body io.ReadCloser, rpcID int64, err error)

	// BuildStream wraps the response body into a managed event stream that
	// releases the pool on close. The protocol owns stream construction
	// (mapper, cancel profile, server-request handler, tool-summary flushing)
	// using the session's client and the pool/key for lifecycle.
	BuildStream(parent context.Context, body io.ReadCloser, rpcID int64, pool *RuntimePool, key RuntimeKey, maxPending int) (aipapi.ManagedEventStream, error)
}

// RuntimeIdentityProvider is an optional protocol extension: it supplies the
// conversation identity that keys the runtime pool for one request.
//
// A protocol that implements it keeps one live subprocess per conversation
// instead of one per workspace and model, so two unrelated conversations never
// share an agent process. A protocol that does not implement it keeps the
// legacy shared runtime key.
type RuntimeIdentityProvider interface {
	// RuntimeIdentity returns the conversation identity for the call, or an
	// empty string to keep the legacy shared runtime key.
	RuntimeIdentity(call *aipapi.Call) string
}
