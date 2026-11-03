package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// ProtocolVersion is the private sidecar NDJSON protocol version. It is
// versioned independently of the public backend-plugin ABI; S2 keeps the same
// version because every added frame is additive.
const ProtocolVersion = "1"

// Location is the resolved, connector-owned sidecar bundle.
type Location struct {
	Node             string
	Script           string
	PinnedSDKVersion string
}

// Runtime carries the non-secret execution context of one configured instance.
type Runtime struct {
	CLI       string
	Workspace string
	Env       []string
	Secrets   []string
}

// Model is one runtime-discovered SDK model row. No static fallback list exists.
type Model struct {
	Value                 string   `json:"value"`
	ResolvedModel         string   `json:"resolved_model,omitempty"`
	DisplayName           string   `json:"display_name,omitempty"`
	Description           string   `json:"description,omitempty"`
	SupportedEffortLevels []string `json:"supported_effort_levels,omitempty"`
}

// Account is the non-identifying account classification used for disclosure.
// Email, organisation identifiers and tokens are never requested or retained.
type Account struct {
	SubscriptionType string `json:"subscription_type,omitempty"`
	APIProvider      string `json:"api_provider,omitempty"`
}

// Discovery is one bounded model/identity snapshot from the SDK.
type Discovery struct {
	Models      []Model
	Account     Account
	CLIVersion  string
	SDKVersion  string
	NodeVersion string
}

// Frame is one private NDJSON message. Unknown kinds are a protocol failure.
type Frame struct {
	Kind        string   `json:"kind"`
	RequestID   string   `json:"request_id,omitempty"`
	Sequence    uint64   `json:"sequence,omitempty"`
	Text        string   `json:"text,omitempty"`
	Code        string   `json:"code,omitempty"`
	Message     string   `json:"message,omitempty"`
	StopReason  string   `json:"stop_reason,omitempty"`
	Models      []Model  `json:"models,omitempty"`
	Account     *Account `json:"account,omitempty"`
	CLIVersion  string   `json:"cli_version,omitempty"`
	SDKVersion  string   `json:"sdk_version,omitempty"`
	NodeVersion string   `json:"node_version,omitempty"`

	// Tool-call traffic (S2). ToolCallID is the native opaque id captured from
	// PreToolUse; Arguments keeps the exact JSON value of the complete call.
	ToolCallID         string          `json:"tool_call_id,omitempty"`
	NativeName         string          `json:"native_name,omitempty"`
	ToolName           string          `json:"tool_name,omitempty"`
	Arguments          json.RawMessage `json:"arguments,omitempty"`
	AwaitingToolResult bool            `json:"awaiting_tool_result,omitempty"`

	InputTokens      *int64 `json:"input_tokens,omitempty"`
	OutputTokens     *int64 `json:"output_tokens,omitempty"`
	CacheReadTokens  *int64 `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens *int64 `json:"cache_write_tokens,omitempty"`
	TotalTokens      *int64 `json:"total_tokens,omitempty"`
	ReasoningTokens  *int64 `json:"reasoning_tokens,omitempty"`
}

// SidecarError is a typed, bounded failure reported by the private bridge.
// Message is connector-owned: raw sidecar prose is never retained or forwarded
// because it can carry prompts, payloads, credentials, or truncated fragments.
type SidecarError struct {
	Code    string
	Message string
}

func (e *SidecarError) Error() string {
	if e == nil {
		return "claudeagentmodel: sidecar error"
	}
	if e.Code == "" {
		return "claudeagentmodel: " + boundedText(e.Message)
	}
	return fmt.Sprintf("claudeagentmodel: %s: %s", e.Code, boundedText(e.Message))
}

// clientFailureMessages maps the private sidecar failure taxonomy to
// connector-owned, bounded client summaries. Known codes stay distinct; an
// unknown or malformed code falls back to a generic typed failure.
var clientFailureMessages = map[string]string{
	"authentication_failed":           "Claude authentication failed; run claude auth login and retry",
	"auth_required":                   "Claude authentication is required; run claude auth login and retry",
	"oauth_org_not_allowed":           "the Claude organisation does not allow this login",
	"account_on_hold":                 "the Claude account is on hold",
	"verification_required":           "the Claude account requires verification",
	"cloud_credential_error":          "the Claude cloud credential could not be used",
	"rate_limited":                    "the Claude subscription rate limit was reached",
	"plan_exhausted":                  "the Claude subscription usage limit was reached",
	"third_party_extra_usage":         "Claude requires third-party extra usage for this request; add extra usage at claude.ai/settings/usage",
	"permission_denied":               "the Claude Agent SDK denied the request",
	"model_not_found":                 "the requested model is not available in the current runtime inventory",
	"invalid_request":                 "the Claude Agent SDK rejected the request",
	"max_output_tokens":               "the model output reached its token limit",
	"max_turns":                       "the Claude Agent SDK reached its turn limit",
	"structured_output_unsupported":   "structured output is not supported by this connector slice",
	"sdk_error":                       "the Claude Agent SDK reported a failure",
	"discovery_empty":                 "the Claude Agent SDK reported no models",
	"identity_unavailable":            "the Claude Agent SDK did not report its runtime identity",
	"not_configured":                  "the sidecar was not configured",
	"invalid_configuration":           "the sidecar configuration was invalid",
	"busy":                            "the sidecar already has an active request",
	"protocol_error":                  "the sidecar protocol failed",
	"protocol_mismatch":               "the sidecar protocol failed",
	"unavailable":                     "the Claude Agent SDK is unavailable",
	"sdk_unavailable":                 "the Claude Agent SDK is unavailable",
	"native_tool_use":                 "the SDK attempted a tool call that the request did not admit",
	"native_tools_exposed":            "the SDK exposed tools outside the admitted caller catalogue",
	"native_mcp_exposed":              "the SDK exposed an MCP server that the connector did not configure",
	"mcp_unavailable":                 "the transient caller-tool server is unavailable",
	"tool_correlation_failed":         "the SDK tool call did not match the committed caller tool call",
	"unsupported_parallel_tool_calls": "a second outstanding native tool call is not supported",
	"tool_abandoned":                  "an admitted tool call never reached the connector",
	"canceled":                        "the tool call was canceled",
	"superseded_output_committed":     "the SDK replaced assistant output after it was committed to the client",
	"unsupported_post_tool_text":      "the model returned text after a tool_use block, which this connector cannot preserve in order",
	"empty_response":                  "the SDK completed without producing a response",
}

// newSidecarError builds a typed failure whose client-visible message is a
// connector-owned constant. The raw frame message is deliberately discarded.
func newSidecarError(code string) *SidecarError {
	code = safeSidecarCode(code)
	message, ok := clientFailureMessages[code]
	if !ok {
		message = clientFailureMessages["sdk_error"]
	}
	return &SidecarError{Code: code, Message: message}
}

// safeSidecarCode accepts only the private taxonomy alphabet; anything else is
// an unknown failure rather than operator- or client-controlled text.
func safeSidecarCode(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || len(value) > 64 {
		return "sdk_error"
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			continue
		}
		return "sdk_error"
	}
	return value
}

func boundedText(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\n", " "))
	if len(value) > 512 {
		return value[:512]
	}
	return value
}

func startProcess(ctx context.Context, loc Location, runtime Runtime) (*Process, error) {
	proc, err := Start(ctx, Options{
		Node:    loc.Node,
		Script:  loc.Script,
		Env:     runtime.Env,
		Dir:     runtime.Workspace,
		Secrets: runtime.Secrets,
	})
	if err != nil {
		return nil, err
	}
	hello, err := proc.AwaitHello(ctx)
	if err != nil {
		_ = proc.Close()
		return nil, err
	}
	if hello.Protocol != ProtocolVersion {
		_ = proc.Close()
		return nil, errors.New("claudeagentmodel: sidecar protocol mismatch")
	}
	if loc.PinnedSDKVersion != "" && hello.SDKVersion != loc.PinnedSDKVersion {
		_ = proc.Close()
		return nil, errors.New("claudeagentmodel: sidecar SDK identity mismatch")
	}
	if err := proc.SendContext(ctx, map[string]any{
		"kind":      "configure",
		"cli":       runtime.CLI,
		"workspace": runtime.Workspace,
	}); err != nil {
		_ = proc.Close()
		return nil, err
	}
	return proc, nil
}

// Discover opens one short-lived sidecar, reads the exact runtime model catalog
// and identity, and closes the child. It never falls back to a static list.
func Discover(ctx context.Context, loc Location, runtime Runtime) (Discovery, error) {
	proc, err := startProcess(ctx, loc, runtime)
	if err != nil {
		return Discovery{}, err
	}
	defer func() { _ = proc.Close() }()
	if err := proc.SendContext(ctx, map[string]any{"kind": "discover", "request_id": "discover"}); err != nil {
		return Discovery{}, err
	}
	for range 32 {
		line, err := proc.Recv(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return Discovery{}, &SidecarError{Code: "protocol_error", Message: "the sidecar ended before the discovery response completed"}
			}
			return Discovery{}, err
		}
		var frame Frame
		if err := json.Unmarshal(line, &frame); err != nil {
			return Discovery{}, errors.New("claudeagentmodel: malformed discovery frame")
		}
		switch frame.Kind {
		case "models":
			if len(frame.Models) == 0 {
				return Discovery{}, &SidecarError{Code: "discovery_empty", Message: "the SDK reported no models"}
			}
			out := Discovery{Models: frame.Models, CLIVersion: frame.CLIVersion, SDKVersion: frame.SDKVersion, NodeVersion: frame.NodeVersion}
			if frame.Account != nil {
				out.Account = *frame.Account
			}
			return out, nil
		case "error":
			return Discovery{}, newSidecarError(frame.Code)
		case "hello":
			continue
		default:
			return Discovery{}, errors.New("claudeagentmodel: unexpected discovery frame")
		}
	}
	return Discovery{}, errors.New("claudeagentmodel: discovery exceeded frame bound")
}

// TurnRequest is one admitted canonical step that opens a fresh native turn.
type TurnRequest struct {
	RequestID string
	Model     string
	System    string
	Prompt    string
	Catalog   *Catalog
	// SessionKey is the host-validated proxy-owned session id, or empty for a
	// stateless client.
	SessionKey string
	// Prefix digests the whole client-visible request transcript before this
	// turn's response.
	Prefix [32]byte
	// ItemAuthority records whether Prefix is an ordered-item digest or a
	// legacy message digest. A subsequent emitted call extends it accordingly.
	ItemAuthority bool
	// Effort is the validated SDK effort level, empty when the request carried
	// none or the selected model does not support the requested level.
	Effort string
	// PendingTTL bounds how long a committed tool call may wait for its result.
	PendingTTL time.Duration
}

// ResumeSpec is one validated continuation of a parked native turn.
type ResumeSpec struct {
	CallID        string
	Model         string
	CatalogDigest [32]byte
	// RetainCatalog permits a continuation that omitted its tool definitions to
	// reuse the parked snapshot. Only the engine sets it after proving that the
	// request carried no catalogue and the pending turn did.
	RetainCatalog bool
	Prefix        [32]byte
	Effort        string
	Result        ToolResult
	// NameOnly selects the Gemini name-only proof form. The client wire has no
	// opaque call id; the native CallID is resolved from the parked pending call.
	NameOnly bool
	// ArgsDigest is the exact canonical argument digest proved from the
	// client-visible assistant call for the name-only form.
	ArgsDigest [32]byte
	// ItemAuthority records whether the continuation request uses ordered items.
	ItemAuthority bool
	// TranscriptPrefix digests the whole continuation request transcript; a
	// further tool call emitted in this step extends it.
	TranscriptPrefix [32]byte
}

type sessionState int

const (
	sessionRunning sessionState = iota
	sessionWaiting
	sessionResuming
	sessionClosed
)

// Session owns exactly one sidecar child, one serialized native turn and the
// pending deferred invocation of one conversation.
type Session struct {
	mgr *Manager
	now func() time.Time

	mu      sync.Mutex
	proc    *Process
	key     string
	catalog *Catalog
	state   sessionState
	pending *PendingCall
	// pendingTTLOverride is the per-turn TTL selected by the request. The
	// manager default is used when it is zero.
	pendingTTLOverride time.Duration
	active             *Turn
	closed             bool
	expiry             *time.Timer
	idle               *time.Timer
	gate               chan struct{}
	release            Release
}

func newSession(mgr *Manager, proc *Process, key string, catalog *Catalog, release Release) *Session {
	if catalog == nil {
		catalog = EmptyCatalog()
	}
	return &Session{
		mgr:     mgr,
		now:     mgr.now,
		proc:    proc,
		key:     strings.TrimSpace(key),
		catalog: catalog,
		state:   sessionRunning,
		gate:    make(chan struct{}, 1),
		release: release,
	}
}

// acquire takes this conversation's single turn slot. It is bounded so a
// concurrent request cannot wait forever on another HTTP response.
func (s *Session) acquire(ctx context.Context, wait time.Duration) error {
	if wait <= 0 {
		wait = 30 * time.Second
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case s.gate <- struct{}{}:
		return nil
	case <-timer.C:
		return continuationError(CodeConversationBusy, "the conversation already has an active turn")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Session) releaseSlot() {
	select {
	case <-s.gate:
	default:
	}
}

type turnContext struct {
	prefix        [32]byte
	model         string
	effort        string
	catalog       *Catalog
	itemAuthority bool
}

// Turn is one HTTP response reader over a session-owned native turn. Closing it
// detaches only this reader; retiring the native turn is an explicit action.
type Turn struct {
	sess      *Session
	requestID string
	tc        turnContext
	items     chan item
	done      chan struct{}
	doneOnce  sync.Once
	wg        sync.WaitGroup
	terminal  atomic.Bool
	parked    atomic.Bool
	events    turnEvents
	pending   *PendingCall
	text      strings.Builder
}

type item struct {
	event aipapi.Event
	err   error
}

func (s *Session) newTurn(requestID string, tc turnContext) *Turn {
	if tc.catalog == nil {
		tc.catalog = EmptyCatalog()
	}
	return &Turn{
		sess:      s,
		requestID: requestID,
		tc:        tc,
		items:     make(chan item, 16),
		done:      make(chan struct{}),
		events:    turnEvents{sanitize: s.proc.Sanitize},
	}
}

// Start opens one sidecar and one fresh native turn. The manager bounds live
// sidecars for the configured instance and refuses rather than evicting a
// waiting conversation.
func (m *Manager) Start(ctx context.Context, loc Location, runtime Runtime, req TurnRequest) (*Turn, error) {
	if strings.TrimSpace(req.RequestID) == "" || strings.TrimSpace(req.Prompt) == "" {
		return nil, errors.New("claudeagentmodel: turn requires a request id and prompt")
	}
	slot, err := m.admission.Acquire(ctx, 0)
	if err != nil {
		return nil, err
	}
	proc, err := startProcess(ctx, loc, runtime)
	if err != nil {
		slot.Release()
		return nil, err
	}
	sess := newSession(m, proc, req.SessionKey, req.Catalog, slot)
	sess.pendingTTLOverride = req.PendingTTL
	turn := sess.newTurn(req.RequestID, turnContext{prefix: req.Prefix, model: req.Model, effort: req.Effort, catalog: req.Catalog, itemAuthority: req.ItemAuthority})
	if err := sess.acquire(ctx, m.turnWait); err != nil {
		_ = proc.Close()
		slot.Release()
		return nil, err
	}
	if err := m.register(sess); err != nil {
		sess.releaseSlot()
		_ = proc.Close()
		slot.Release()
		return nil, err
	}
	sess.mu.Lock()
	sess.active = turn
	sess.mu.Unlock()
	if err := sess.sendStart(ctx, req); err != nil {
		sess.retire()
		return nil, err
	}
	turn.wg.Go(turn.readLoop)
	return turn, nil
}

func (s *Session) sendStart(ctx context.Context, req TurnRequest) error {
	frame := map[string]any{
		"kind":       "start",
		"request_id": req.RequestID,
		"model":      req.Model,
		"system":     req.System,
		"prompt":     req.Prompt,
	}
	if req.Effort != "" {
		frame["effort"] = req.Effort
	}
	if req.Catalog != nil {
		if specs := req.Catalog.Specs(); len(specs) > 0 {
			frame["tools"] = specs
		}
	}
	if req.PendingTTL > 0 {
		frame["pending_ttl_ms"] = req.PendingTTL.Milliseconds()
	}
	return s.proc.SendContext(ctx, frame)
}

// Resume releases exactly one validated deferred invocation and attaches a new
// HTTP reader to the same native turn.
func (s *Session) Resume(ctx context.Context, spec ResumeSpec, requestID string) (*Turn, error) {
	if strings.TrimSpace(requestID) == "" {
		return nil, errors.New("claudeagentmodel: continuation requires a request id")
	}
	content, err := spec.Result.Content()
	if err != nil {
		return nil, err
	}
	if err := s.acquire(ctx, s.mgr.turnWait); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		s.releaseSlot()
		return nil, continuationError(CodeContinuationFailed, "the parked native turn is no longer available")
	}
	pending := s.pending
	if pending == nil || s.state != sessionWaiting {
		s.mu.Unlock()
		s.releaseSlot()
		return nil, continuationError(CodeContinuationMismatch, "the conversation has no pending tool call")
	}
	if pending.released {
		s.mu.Unlock()
		s.releaseSlot()
		return nil, continuationError(CodeContinuationConsumed, "the pending tool call was already released")
	}
	if s.now().After(pending.ExpiresAt) {
		s.mu.Unlock()
		s.releaseSlot()
		s.expire(pending)
		return nil, continuationError(CodeContinuationExpired, "the pending tool call expired before its result arrived")
	}
	if spec.NameOnly {
		name := strings.TrimSpace(spec.Result.Name)
		if name == "" {
			name = strings.TrimSpace(spec.Result.CallID)
		}
		if pending.CallID != spec.CallID {
			s.mu.Unlock()
			s.releaseSlot()
			return nil, continuationError(CodeContinuationMismatch, "the tool result does not match the pending native call")
		}
		if pending.Name != name {
			s.mu.Unlock()
			s.releaseSlot()
			return nil, continuationError(CodeContinuationMismatch, "the tool result name does not match the pending native call")
		}
		if !PrefixesEqual(pending.PrefixByName, spec.Prefix) {
			s.mu.Unlock()
			s.releaseSlot()
			return nil, continuationError(CodeContinuationMismatch, "the request history does not match the committed name-only tool-call prefix")
		}
		if pending.ArgsDigest != spec.ArgsDigest {
			s.mu.Unlock()
			s.releaseSlot()
			return nil, continuationError(CodeContinuationMismatch, "the committed tool arguments do not match the parked turn")
		}
	} else {
		if pending.CallID != spec.CallID {
			s.mu.Unlock()
			s.releaseSlot()
			return nil, continuationError(CodeContinuationMismatch, "the tool result does not match the pending native call")
		}
		if !PrefixesEqual(pending.Prefix, spec.Prefix) &&
			(pending.PrefixSeparateText == ([32]byte{}) || !PrefixesEqual(pending.PrefixSeparateText, spec.Prefix)) {
			s.mu.Unlock()
			s.releaseSlot()
			return nil, continuationError(CodeContinuationMismatch, "the request history does not match the committed tool-call prefix")
		}
	}
	if pending.Model != spec.Model {
		s.mu.Unlock()
		s.releaseSlot()
		return nil, continuationError(CodeContinuationMismatch, "the request model does not match the parked turn")
	}
	if pending.Effort != spec.Effort {
		s.mu.Unlock()
		s.releaseSlot()
		return nil, continuationError(CodeContinuationMismatch, "the request generation controls do not match the parked turn")
	}
	if pending.CatalogDigest != spec.CatalogDigest && !spec.RetainCatalog {
		s.mu.Unlock()
		s.releaseSlot()
		return nil, continuationError(CodeContinuationMismatch, "the request tool catalogue does not match the parked turn")
	}
	pending.released = true
	s.state = sessionResuming
	if s.expiry != nil {
		s.expiry.Stop()
		s.expiry = nil
	}
	turn := s.newTurn(requestID, turnContext{prefix: spec.TranscriptPrefix, model: spec.Model, effort: spec.Effort, catalog: s.catalog, itemAuthority: spec.ItemAuthority})
	s.active = turn
	s.mu.Unlock()

	if err := s.proc.SendContext(ctx, map[string]any{
		"kind":         "tool_result",
		"request_id":   requestID,
		"tool_call_id": pending.CallID,
		"content":      content,
	}); err != nil {
		s.mgr.tombstone(s, pending, CodeContinuationFailed)
		s.retire()
		return nil, &SidecarError{Code: "unavailable", Message: "the parked native turn could not be released"}
	}
	s.mu.Lock()
	s.state = sessionRunning
	if s.pending == pending {
		s.pending = nil
	}
	s.mu.Unlock()
	s.mgr.consume(s, pending)
	if idle := s.mgr.idleTTL; idle > 0 {
		timer := time.AfterFunc(idle, func() { s.retireIdle(turn) })
		s.mu.Lock()
		if s.closed {
			timer.Stop()
		} else {
			s.idle = timer
		}
		s.mu.Unlock()
	}
	turn.wg.Go(turn.readLoop)
	return turn, nil
}

// acceptToolCall validates one complete native tool call and records exactly one
// pending entry before the MCP handler may wait.
func (s *Session) acceptToolCall(frame Frame, tc turnContext, emittedText string) (*PendingCall, error) {
	callID := strings.TrimSpace(frame.ToolCallID)
	if callID == "" {
		return nil, errors.New("claudeagentmodel: sidecar tool call omitted its native id")
	}
	if strings.TrimSpace(frame.NativeName) == "" {
		return nil, errors.New("claudeagentmodel: sidecar tool call omitted its native name")
	}
	spec, ok := tc.catalog.ByNative(frame.NativeName)
	if !ok {
		return nil, continuationError(CodeContinuationMismatch, "the model called a tool that the request did not admit")
	}
	if frame.ToolName != "" && frame.ToolName != spec.Name {
		return nil, continuationError(CodeContinuationMismatch, "the sidecar tool name does not match the admitted catalogue")
	}
	args := frame.Arguments
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	var prefix, prefixSeparateText, prefixByName [32]byte
	var err error
	if tc.itemAuthority {
		prefix, err = extendItemPrefixWithCall(tc.prefix, emittedText, callID, spec.Name, args)
	} else {
		prefix, err = ExtendPrefixWithCall(tc.prefix, emittedText, callID, spec.Name, args)
		if err == nil {
			// An item-authority continuation projected to legacy messages places
			// the assistant text in its own message; accept that exact ordered
			// form as well as the inline Chat/Anthropic form.
			prefixSeparateText, err = extendPrefixWithSeparateText(tc.prefix, emittedText, callID, spec.Name, args)
		}
		if err == nil {
			// The name-only form is a second, independent proof used by Gemini,
			// whose wire has no distinct call id. It hashes the name in the call
			// identity slot and binds the exact arguments separately.
			prefixByName, err = ExtendPrefixWithCall(tc.prefix, emittedText, spec.Name, spec.Name, args)
		}
	}
	if err != nil {
		return nil, err
	}
	digest, err := argsDigest(args)
	if err != nil {
		return nil, err
	}
	now := s.now()
	pending := &PendingCall{
		CallID:             callID,
		Name:               spec.Name,
		NativeName:         frame.NativeName,
		Args:               append(json.RawMessage(nil), args...),
		Prefix:             prefix,
		PrefixSeparateText: prefixSeparateText,
		PrefixByName:       prefixByName,
		ArgsDigest:         digest,
		Model:              tc.model,
		Effort:             tc.effort,
		CatalogDigest:      catalogDigestOf(tc.catalog),
		ToolCount:          tc.catalog.Len(),
		ItemAuthority:      tc.itemAuthority,
		CreatedAt:          now,
		ExpiresAt:          now.Add(s.pendingTTL()),
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, continuationError(CodeContinuationFailed, "the conversation is not accepting a tool call")
	}
	if s.pending != nil {
		s.mu.Unlock()
		return nil, continuationError(CodeUnsupportedParallelToolCalls, "a second outstanding native tool call is not supported")
	}
	s.pending = pending
	s.mu.Unlock()
	s.mgr.indexPending(s, pending)
	return pending, nil
}

func (s *Session) pendingTTL() time.Duration {
	if s.pendingTTLOverride > 0 {
		return s.pendingTTLOverride
	}
	if ttl := s.mgr.pendingTTL; ttl > 0 {
		return ttl
	}
	return 30 * time.Minute
}

func catalogDigestOf(catalog *Catalog) [32]byte {
	if catalog == nil {
		return [32]byte{}
	}
	return catalog.Digest()
}

// expire retires one expired pending call without releasing it.
func (s *Session) expire(pending *PendingCall) {
	s.mu.Lock()
	if s.closed || s.pending != pending || pending.released {
		s.mu.Unlock()
		return
	}
	s.pending = nil
	s.mu.Unlock()
	s.mgr.tombstone(s, pending, CodeContinuationExpired)
	s.retire()
}

func (s *Session) scheduleExpiry(pending *PendingCall) {
	delay := max(time.Until(pending.ExpiresAt), 0)
	s.mu.Lock()
	if s.closed || s.pending != pending {
		s.mu.Unlock()
		return
	}
	s.expiry = time.AfterFunc(delay, func() { s.expire(pending) })
	s.mu.Unlock()
}

// retire closes the owned process exactly once and removes every index entry.
func (s *Session) retire() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.state = sessionClosed
	pending := s.pending
	s.pending = nil
	if s.expiry != nil {
		s.expiry.Stop()
		s.expiry = nil
	}
	if s.idle != nil {
		s.idle.Stop()
		s.idle = nil
	}
	proc := s.proc
	s.mu.Unlock()

	if pending != nil && !pending.released {
		s.mgr.tombstone(s, pending, CodeContinuationFailed)
	}
	if proc != nil {
		// Close owns the bounded shutdown write; a separate unbounded Send here
		// could pin teardown behind a child that stopped reading stdin.
		_ = proc.Close()
	}
	s.releaseSlot()
	if s.release != nil {
		s.release.Release()
	}
	s.mgr.unregister(s)
}

// retireIdle bounds a released continuation that never produces a terminal.
func (s *Session) retireIdle(turn *Turn) {
	s.mu.Lock()
	if s.closed || s.active != turn {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.retire()
}

// isWaiting reports a committed park ready for a validated result.
func (s *Session) isWaiting() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state == sessionWaiting && s.pending != nil
}

func (s *Session) markWaiting(owner *Turn) {
	s.mu.Lock()
	if !s.closed && s.pending != nil {
		s.state = sessionWaiting
	}
	// Timer cleanup is ownership-conditional: a new reader may already own the
	// session's idle timer.
	if s.active == owner && s.idle != nil {
		s.idle.Stop()
		s.idle = nil
	}
	s.mu.Unlock()
}

// detach marks the active reader finished without touching the session.
func (s *Session) detach(turn *Turn) {
	s.mu.Lock()
	if s.active == turn {
		s.active = nil
		if s.idle != nil {
			s.idle.Stop()
			s.idle = nil
		}
	}
	s.mu.Unlock()
}

func (t *Turn) readLoop() {
	defer close(t.items)
	var sequence uint64
	for {
		line, err := t.sess.proc.Recv(context.Background())
		if err != nil {
			if errors.Is(err, io.EOF) {
				// A clean child exit is not a completed response. EOF before an
				// observed response_finished must never become a successful
				// truncated completion in the shared forwarding path.
				t.fail(newSidecarError("protocol_error"))
				return
			}
			t.fail(err)
			return
		}
		var frame Frame
		if err := json.Unmarshal(line, &frame); err != nil {
			t.fail(newSidecarError("protocol_error"))
			return
		}
		if frame.RequestID != "" && frame.RequestID != t.requestID {
			t.fail(errors.New("claudeagentmodel: sidecar frame belongs to another request"))
			return
		}
		if frame.Sequence != 0 {
			if frame.Sequence <= sequence {
				t.fail(errors.New("claudeagentmodel: out-of-order sidecar frame"))
				return
			}
			sequence = frame.Sequence
		}
		if frame.Kind == "tool_call" {
			if err := t.acceptToolCall(frame); err != nil {
				t.fail(err)
				return
			}
			continue
		}
		events, terminal, err := t.events.mapFrame(frame)
		if err != nil {
			t.fail(err)
			return
		}
		for _, event := range events {
			if event.Kind == aipapi.EventTextDelta {
				t.text.WriteString(event.Delta)
			}
		}
		if terminal {
			if frame.AwaitingToolResult {
				if t.pending == nil {
					// The sidecar parked without a committed call: a failed exchange,
					// not a committed park.
					t.fail(continuationError(CodeContinuationMismatch, "the sidecar parked without a committed tool call"))
					return
				}
				t.parked.Store(true)
			} else {
				t.terminal.Store(true)
			}
		}
		emitted := true
		for _, event := range events {
			if !t.emit(event) {
				// The downstream reader is gone. Terminal ownership is still
				// finalized below; only this reader's delivery stops.
				emitted = false
				break
			}
		}
		if !terminal {
			continue
		}
		// Finalize the terminal exactly once regardless of downstream delivery:
		// a committed park registers its wait, a completed turn retires.
		if t.parked.Load() {
			t.sess.markWaiting(t)
			t.sess.scheduleExpiry(t.pending)
		} else {
			t.sess.retire()
		}
		// Detach and release the per-turn slot only after the old reader's own
		// timer ownership is gone, so a new reader installed in between keeps its
		// timer.
		t.sess.detach(t)
		t.sess.releaseSlot()
		if !emitted {
			return
		}
		return
	}
}

func (t *Turn) acceptToolCall(frame Frame) error {
	pending, err := t.sess.acceptToolCall(frame, t.tc, t.text.String())
	if err != nil {
		return err
	}
	t.pending = pending
	for _, event := range toolCallEvents(pending) {
		if !t.emit(event) {
			return context.Canceled
		}
	}
	return nil
}

func (t *Turn) fail(err error) {
	t.emitError(err)
	t.sess.retire()
	t.sess.releaseSlot()
}

func (t *Turn) emit(event aipapi.Event) bool {
	select {
	case t.items <- item{event: event}:
		return true
	case <-t.done:
		return false
	}
}

func (t *Turn) emitError(err error) {
	select {
	case t.items <- item{err: err}:
	case <-t.done:
	}
}

// Recv returns the next canonical event. io.EOF follows the terminal event.
func (t *Turn) Recv(ctx context.Context) (aipapi.Event, error) {
	select {
	case <-ctx.Done():
		return aipapi.Event{}, ctx.Err()
	case next, ok := <-t.items:
		if !ok {
			return aipapi.Event{}, io.EOF
		}
		return next.event, next.err
	}
}

// Close detaches this HTTP reader. It must not kill a legitimate parked native
// turn: only cancel, error, TTL, shutdown or process death retire the turn.
func (t *Turn) Close() error {
	t.doneOnce.Do(func() { close(t.done) })
	if !t.terminal.Load() && !t.parked.Load() {
		t.sess.retire()
	}
	t.wg.Wait()
	return nil
}

// Cancel explicitly retires the conversation, including a parked turn, and is
// joinable after Close.
func (t *Turn) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	t.doneOnce.Do(func() { close(t.done) })
	t.sess.retire()
	t.wg.Wait()
	return aipapi.CancelResult{Mode: aipapi.CancelModeTransport}
}

// Parked reports whether this response ended at a committed tool call while the
// native turn stays alive.
func (t *Turn) Parked() bool { return t.parked.Load() }
