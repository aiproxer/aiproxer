package acp

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// acpProtocol adapts a SubprocessConnectorSpec (the per-connector seam
// implemented by cursorcliacp, geminicliacp, agycliacp) to the
// SubprocessProtocol interface consumed by the shared subprocessBackend
// orchestrator. It owns the ACP-session-protocol logic that is shared across
// those connectors (session/prompt send, promptStream construction,
// poolManagedStream wrapping with tool-summary flushing), parameterized by the
// vendor-specific pieces on the spec (BuildCommand, HandshakeProfile,
// CancelProfile, ServerRequestHandler, ResolveModel).
type acpProtocol struct {
	spec SubprocessConnectorSpec
	log  *slog.Logger
}

// NewACPProtocol wraps a SubprocessConnectorSpec as a SubprocessProtocol so
// the shared subprocess orchestrator can drive it. log may be nil (defaults to
// slog.Default). The tool sink is created per turn: it carries the mutable
// tool-call state of one conversation, and two concurrent conversations must
// never share it.
func NewACPProtocol(spec SubprocessConnectorSpec, log *slog.Logger) SubprocessProtocol {
	if log == nil {
		log = slog.Default()
	}
	return &acpProtocol{
		spec: spec,
		log:  log,
	}
}

// selectToolSink wires text summaries by default. Specs opting into
// StructuredToolProvenance emit canonical tool lifecycle events instead.
func selectToolSink(spec SubprocessConnectorSpec) ToolUpdateSink {
	if s, ok := spec.(StructuredToolProvenanceSpec); ok && s.StructuredToolProvenance() {
		return NewToolProvenanceSink()
	}
	return &toolSummarySink{tools: make(map[string]*toolAccum), now: time.Now}
}

func (p *acpProtocol) Label() string { return "acp subprocess" }

func (p *acpProtocol) TransportOptions() TransportOptions {
	if spec, ok := p.spec.(TransportOptionsSpec); ok {
		return spec.TransportOptions()
	}
	return TransportOptions{}
}

func (p *acpProtocol) ValidateCall(call *aipapi.Call) error { return validateACPCall(call) }

// RuntimeIdentity forwards the connector's conversation identity to the
// orchestrator. A spec that does not opt into conversation-scoped runtimes
// keeps the legacy shared runtime key.
func (p *acpProtocol) RuntimeIdentity(call *aipapi.Call) string {
	if spec, ok := p.spec.(RuntimeIdentitySpec); ok {
		return spec.RuntimeIdentity(call)
	}
	return ""
}

func (p *acpProtocol) ResolveModel(call *aipapi.Call) string {
	return p.spec.ResolveModel(CallRouteModel(call, "acp.model"))
}

func (p *acpProtocol) ResolveProcessConfig(_ *aipapi.Call) string { return "" }

func (p *acpProtocol) BuildSpawnCommand(model, workspace, _ string) ([]string, string, []string, error) {
	return p.spec.BuildCommand(model, workspace)
}

func (p *acpProtocol) Handshake(ctx context.Context, transport Transport, model, workspace string) (string, error) {
	return p.handshakeWithSystemPrompt(ctx, transport, model, workspace, "")
}

// HandshakeWithSystemPrompt runs the handshake with canonical instructions
// installed as session/new _meta.systemPrompt. A blank sysPrompt behaves
// exactly like Handshake.
func (p *acpProtocol) HandshakeWithSystemPrompt(ctx context.Context, transport Transport, model, workspace, sysPrompt string) (string, error) {
	return p.handshakeWithSystemPrompt(ctx, transport, model, workspace, sysPrompt)
}

func (p *acpProtocol) handshakeWithSystemPrompt(ctx context.Context, transport Transport, model, workspace, sysPrompt string) (string, error) {
	cli := newClientFromTransport(transport, p.log)
	hp := p.spec.HandshakeProfile()
	if sysPrompt != "" {
		hp.SessionNewMeta = mergeSessionSystemPrompt(hp.SessionNewMeta, sysPrompt)
	}
	if spec, ok := p.spec.(SessionOptionsSpec); ok {
		if workspace != "" {
			hp.SessionNewCwd = workspace
		}
		if hp.SessionNewCwd == "" {
			hp.SessionNewCwd = "/"
		}
		return handshakeSessionOptionsWithProfile(ctx, cli, hp, spec, model)
	}
	if err := runHandshake(ctx, cli, hp); err != nil {
		return "", fmt.Errorf("handshake: %w", err)
	}
	sid, err := cli.sessionNew(ctx, hp)
	if err != nil {
		return "", fmt.Errorf("session/new: %w", err)
	}
	return sid, nil
}

// handshakeSessionOptionsWithProfile runs the negotiated-options handshake
// with an explicit profile so callers can amend session/new metadata first.
func handshakeSessionOptionsWithProfile(ctx context.Context, cli *client, hp HandshakeProfile, spec SessionOptionsSpec, model string) (string, error) {
	transport, ok := cli.t.(sessionOptionsTransport)
	if !ok {
		return "", failSessionOptions(cli, fmt.Errorf("transport does not support private session option state"))
	}
	transport.acpSessionOptions().current.Store(nil)
	if err := runHandshake(ctx, cli, hp); err != nil {
		return "", err
	}
	return completeSessionOptions(ctx, cli, transport, hp, spec, model)
}

// RotateSessionSystemPrompt issues a fresh session/new carrying the new
// instructions on the live transport and republishes negotiated model/effort
// state against it. The caller owns history reset and pool bookkeeping.
func (p *acpProtocol) RotateSessionSystemPrompt(ctx context.Context, transport Transport, model, sysPrompt string) (string, error) {
	hp := p.spec.HandshakeProfile()
	hp.SessionNewMeta = mergeSessionSystemPrompt(hp.SessionNewMeta, sysPrompt)
	cli := newClientFromTransport(transport, p.log)
	if spec, ok := p.spec.(SessionOptionsSpec); ok {
		transport, ok := transport.(sessionOptionsTransport)
		if !ok {
			return "", fmt.Errorf("acp: session rotation requires options-aware transport")
		}
		transport.acpSessionOptions().current.Store(nil)
		return completeSessionOptions(ctx, cli, transport, hp, spec, model)
	}
	return cli.sessionNew(ctx, hp)
}

// SessionSystemPrompt forwards the connector opt-in; protocols without an
// opting spec report false so orchestrators keep legacy behavior.
func (p *acpProtocol) SessionSystemPrompt(call *aipapi.Call) (string, bool) {
	if spec, ok := p.spec.(SessionSystemPromptSpec); ok && call != nil {
		return spec.SessionSystemPrompt(call)
	}
	return "", false
}

func (p *acpProtocol) BindSession(transport Transport, sessionID string) SubprocessProtocolSession {
	return &acpProtocolSession{
		proto:     p,
		cli:       newClientFromTransport(transport, p.log),
		sessionID: sessionID,
	}
}

// acpProtocolSession is the per-request ACP-session handle. It carries the
// message id from SendPrompt to BuildStream (the stream needs it for
// cancel-session RPCs), which is safe because the orchestrator creates a fresh
// session per Open call.
type acpProtocolSession struct {
	proto     *acpProtocol
	cli       *client
	sessionID string
	msgID     string
}

func (s *acpProtocolSession) SendPrompt(ctx context.Context, model, userMessage string, call *aipapi.Call) (io.ReadCloser, int64, error) {
	if spec, ok := s.proto.spec.(SessionOptionsSpec); ok {
		if err := applySessionEffort(ctx, s.cli, s.sessionID, model, spec.EffortConfigID(), call.Options.ReasoningEffort); err != nil {
			return nil, 0, err
		}
	} else if effort := strings.ToLower(strings.TrimSpace(call.Options.ReasoningEffort)); effort != "" {
		if err := s.cli.setSessionConfigOption(ctx, s.sessionID, "reasoning_effort", effort); err != nil {
			s.proto.log.Error("ACP reasoning effort configuration failed", "error", err, "effort", effort)
			return nil, 0, fmt.Errorf("reasoning effort: %w", err)
		}
	}
	callPtr := prepareTranscriptCall(call, userMessage)
	blocks, err := promptBlocksForCall(callPtr)
	if err != nil {
		return nil, 0, fmt.Errorf("session/prompt: %w", err)
	}
	s.msgID = messageIDForCall(callPtr)
	params := buildPromptParams(s.sessionID, blocks, s.msgID)
	if spec, ok := s.proto.spec.(PromptAdmissionSpec); ok {
		admission, err := admitPrompt(ctx, s.cli, s.sessionID, spec)
		if err != nil {
			return nil, 0, err
		}
		if err := ctx.Err(); err != nil {
			return nil, 0, failedAdmittedPrompt(err)
		}
		params["_meta"] = map[string]any{spec.PromptAdmissionMetadataKey(): admission}
	}
	rpcID := s.cli.rpcID()
	body, err := s.cli.sessionPrompt(ctx, params, rpcID)
	if err != nil {
		if _, ok := s.proto.spec.(PromptAdmissionSpec); ok {
			return nil, 0, failedAdmittedPrompt(err)
		}
		return nil, 0, fmt.Errorf("session/prompt: %w", err)
	}
	return body, rpcID, nil
}

func (s *acpProtocolSession) BuildStream(parent context.Context, body io.ReadCloser, rpcID int64, pool *RuntimePool, key RuntimeKey, maxPending int) (aipapi.ManagedEventStream, error) {
	// The tool sink belongs to this turn alone: it holds the mutable tool-call
	// state of one conversation, and a shared sink would let one conversation's
	// tool summary be flushed into another conversation's response.
	toolSink := selectToolSink(s.proto.spec)
	mapper := SessionUpdateMapperOptions{ToolSink: toolSink}
	_, mapper.RetainRPCError = s.proto.spec.(interface {
		ClassifyError(error, aipapi.OutputPhase) error
	})
	cancelProf := s.proto.spec.CancelProfile()
	srv := s.proto.spec.ServerRequestHandler()
	stream := newPromptNDJSONStream(parent, body, s.cli, s.sessionID, rpcID, s.msgID, mapper, srv, cancelProf, maxPending)
	_, admission := s.proto.spec.(PromptAdmissionSpec)
	stream.requireTerminalResponse = admission
	return &poolManagedStream{inner: stream, pool: pool, key: key, toolSink: toolSink, admission: admission}, nil
}
