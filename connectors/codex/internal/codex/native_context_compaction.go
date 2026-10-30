package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/aiproxer/aiproxer/connectors/codex/internal/responseitem"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const (
	compactionTriggerType               = "compaction"
	compactionSummaryType               = "compaction_summary"
	compactionEventLimit                = 256
	compactionBytesLimit                = 12 << 20
	compactionEventLimitLine            = 12 << 20
	compactionRetainedMessageMaxBytes   = 8 << 20
	compactionRetainedMessageMaxIDBytes = aipapi.MaxItemReferenceIDBytes
)

var (
	retainedMessageFields = map[string]struct{}{
		"type": {}, "id": {}, "status": {}, "role": {}, "content": {}, "phase": {},
	}
	retainedMessageRoles = map[string]struct{}{
		"user": {}, "assistant": {}, "developer": {}, "system": {},
	}
	retainedMessageStatuses = map[string]struct{}{"completed": {}}
	retainedMessagePhases   = map[string]struct{}{"commentary": {}, "final_answer": {}}
)

var (
	errCompactionProtocol  = errors.New("compaction_protocol")
	errCompactionCanceled  = errors.New("compaction_canceled")
	errCompactionTransport = errors.New("compaction_transport")
	errCompactionHTTP      = errors.New("compaction_http")
)

type compactionReason string

const (
	reasonInvalid                compactionReason = "invalid"
	reasonEmptyPrefix            compactionReason = "empty_prefix"
	reasonTriggerDuplicate       compactionReason = "trigger_duplicate"
	reasonTriggerMissing         compactionReason = "trigger_missing"
	reasonPayloadCopy            compactionReason = "payload_copy"
	reasonEventAfterCompletion   compactionReason = "event_after_completion"
	reasonStreamBounds           compactionReason = "stream_bounds"
	reasonMalformedEvent         compactionReason = "malformed_event"
	reasonEventOrder             compactionReason = "event_order"
	reasonMalformedCreated       compactionReason = "malformed_created"
	reasonMalformedItem          compactionReason = "malformed_item"
	reasonUnexpectedOutput       compactionReason = "unexpected_output"
	reasonMultipleCompaction     compactionReason = "multiple_compaction"
	reasonDuplicateCompaction    compactionReason = "duplicate_compaction"
	reasonCompletionInvariant    compactionReason = "completion_invariant"
	reasonTerminalItemMismatch   compactionReason = "terminal_item_mismatch"
	reasonUnsuccessfulCompletion compactionReason = "unsuccessful_completion"
	reasonUnexpectedEvent        compactionReason = "unexpected_event"
	reasonStreamRead             compactionReason = "stream_read"
	reasonMissingCompletion      compactionReason = "missing_completion"
	reasonMissingStream          compactionReason = "missing_stream"
	reasonInvalidCompactionItem  compactionReason = "invalid_compaction_item"
	reasonReplacementConfig      compactionReason = "replacement_config"
	reasonNilContext             compactionReason = "nil_context"
)

type compactionError struct {
	category error
	reason   compactionReason
	cause    error
	status   int
}

func (e *compactionError) Error() string        { return e.category.Error() + ": " + string(e.reason) }
func (e *compactionError) Unwrap() error        { return e.cause }
func (e *compactionError) Is(target error) bool { return target == e.category }

func newCompactionError(category error, reason compactionReason, cause error) error {
	return &compactionError{category: category, reason: reason, cause: cause}
}

func newCompactionHTTPError(status int) error {
	return &compactionError{category: errCompactionHTTP, reason: reasonInvalid, status: status}
}

func compactionStatus(err error) int {
	if classified, ok := errors.AsType[*compactionError](err); ok {
		return classified.status
	}
	return 0
}

// CompactionRequest keeps provider credentials and transport identity separate
// from the JSON payload. It is connector-private and never crosses the ABI.
type CompactionRequest struct {
	Payload      Payload
	Account      Config
	Conversation string
	Metadata     map[string]string
}

type CompactionResult struct {
	// Output is the authoritative unary replacement history returned by
	// /responses/compact. Item is populated only by the legacy streamed parser.
	Output     []inputItem
	Item       opaqueResponseItem
	ResponseID string
	Usage      *completedUsage
}

func (r CompactionResult) dedicated() bool { return len(r.Output) != 0 }

func buildCompactionRequest(normal Payload, prefix []inputItem, cfg Config, conversation string, metadata map[string]string) (CompactionRequest, error) {
	if len(prefix) == 0 {
		return CompactionRequest{}, compactionProtocolError("empty_prefix")
	}
	items := cloneInputItems(prefix)
	var err error
	items, err = normalizeCompactionInputItems(items)
	if err != nil {
		return CompactionRequest{}, compactionProtocolError("payload_copy")
	}
	if isCompactionTrigger(items[len(items)-1]) {
		return CompactionRequest{}, compactionProtocolError("trigger_duplicate")
	}
	p := normal
	var cloneErr error
	p, cloneErr = clonePayload(p)
	if cloneErr != nil {
		return CompactionRequest{}, compactionProtocolError("payload_copy")
	}
	p.Input = items
	// Codex's current dedicated /responses/compact contract is unary. The
	// history itself is the compaction input; no streamed trigger item is sent.
	p.Stream = false
	// The compaction control turn is a narrow Responses request. Do not carry
	// normal-generation state that the compact endpoint does not accept.
	p.Store = false
	// Current Codex's CompactClient preserves the model reasoning controls in
	// the compact request, while the dedicated endpoint owns response delivery.
	p.Include = nil
	p.PromptCacheKey = ""
	p.PreviousResponseID = ""
	// The dedicated endpoint accepts the model, input, and instructions plus
	// compatible model controls. It does not need the normal stream trigger.
	if len(metadata) > 0 {
		p.Metadata = cloneStringMap(metadata)
	}
	return CompactionRequest{Payload: p, Account: cfg, Conversation: conversation, Metadata: cloneStringMap(metadata)}, nil
}

func clonePayload(src Payload) (Payload, error) {
	dst := src
	dst.Input = cloneInputItems(src.Input)
	dst.Tools = make([]toolPayload, len(src.Tools))
	for i, tool := range src.Tools {
		dst.Tools[i] = tool
		if tool.Parameters != nil {
			var err error
			dst.Tools[i].Parameters, err = cloneJSONValue(tool.Parameters)
			if err != nil {
				return Payload{}, err
			}
		}
	}
	dst.Include = append([]string(nil), src.Include...)
	if src.Reasoning != nil {
		value := *src.Reasoning
		dst.Reasoning = &value
	}
	if src.Text != nil {
		value := *src.Text
		dst.Text = &value
	}
	if src.ParallelToolCalls != nil {
		value := *src.ParallelToolCalls
		dst.ParallelToolCalls = &value
	}
	dst.Metadata = cloneStringMap(src.Metadata)
	return dst, nil
}

func cloneJSONValue(src map[string]any) (map[string]any, error) {
	if src == nil {
		return nil, nil
	}
	b, err := json.Marshal(src)
	if err != nil {
		return nil, err
	}
	var dst map[string]any
	if err := json.Unmarshal(b, &dst); err != nil {
		return nil, err
	}
	return dst, nil
}

func compactionTriggerRaw() json.RawMessage {
	return json.RawMessage([]byte{'{', '"', 't', 'y', 'p', 'e', '"', ':', '"', 'c', 'o', 'm', 'p', 'a', 'c', 't', 'i', 'o', 'n', '"', '}'})
}

func (r CompactionRequest) marshal() ([]byte, error) {
	if len(r.Payload.Input) == 0 {
		return nil, compactionProtocolError("empty_prefix")
	}
	// Keep this wire shape separate from the normal streamed Responses payload.
	// Codex's CompactClient sends only the fields accepted by /responses/compact.
	type compactPayload struct {
		Model             string         `json:"model"`
		Input             []inputItem    `json:"input"`
		Instructions      string         `json:"instructions"`
		Tools             []toolPayload  `json:"tools,omitempty"`
		ParallelToolCalls *bool          `json:"parallel_tool_calls,omitempty"`
		Reasoning         *reasoningSpec `json:"reasoning,omitempty"`
		PromptCacheKey    string         `json:"prompt_cache_key,omitempty"`
		Text              *textSpec      `json:"text,omitempty"`
	}
	return json.Marshal(compactPayload{
		Model: r.Payload.Model, Input: r.Payload.Input, Instructions: r.Payload.Instructions,
		Tools: r.Payload.Tools, ParallelToolCalls: r.Payload.ParallelToolCalls,
		Reasoning: r.Payload.Reasoning, PromptCacheKey: r.Payload.PromptCacheKey, Text: r.Payload.Text,
	})
}

func isCompactionTrigger(item inputItem) bool {
	opaque, ok := item.(opaqueResponseItem)
	if !ok {
		return false
	}
	var header struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(opaque.raw, &header) == nil && header.Type == compactionTriggerType
}

func newCompactionClient(client *http.Client, endpoint string) *compactionClient {
	if client == nil {
		client = http.DefaultClient
	}
	return &compactionClient{client: client, endpoint: endpoint}
}

type compactionClient struct {
	client   *http.Client
	endpoint string
}

func doCompactionCodexRequest(ctx context.Context, client *http.Client, endpoint string, body []byte, cfg *Config, convID string) (*http.Response, error) {
	extra := make(http.Header)
	// Codex marks compaction as a control/sub-agent request at the transport
	// boundary. Keep this out of the JSON payload and ordinary turns.
	extra.Set("x-openai-subagent", "compact")
	extra.Set("Accept", "application/json")
	return doCodexRequestWithHeaders(ctx, client, endpoint, body, cfg, convID, extra)
}

func (c *compactionClient) Compact(ctx context.Context, request CompactionRequest) (CompactionResult, error) {
	if ctx == nil {
		return CompactionResult{}, aipNilContextError()
	}
	body, err := request.marshal()
	if err != nil {
		return CompactionResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return CompactionResult{}, compactionCanceled(err)
	}
	resp, err := doCompactionCodexRequest(ctx, c.client, c.endpoint, body, &request.Account, request.Conversation)
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return CompactionResult{}, compactionCanceled(firstContextError(ctx, err))
		}
		return CompactionResult{}, compactionTransportError(err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_ = readLimitedClose(resp)
		return CompactionResult{}, newCompactionHTTPError(resp.StatusCode)
	}
	result, err := collectCompactionResponse(ctx, resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return CompactionResult{}, err
	}
	return result, nil
}

// collectCompactionResponse accepts the authoritative unary JSON response and
// retains the SSE collector for compatible proxies/emulators.
func collectCompactionResponse(ctx context.Context, body io.Reader) (CompactionResult, error) {
	if ctx == nil {
		return CompactionResult{}, aipNilContextError()
	}
	if body == nil {
		return CompactionResult{}, compactionProtocolError("missing_stream")
	}
	limited := io.LimitReader(body, compactionBytesLimit+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return CompactionResult{}, compactionCanceled(ctxErr)
		}
		return CompactionResult{}, compactionProtocolError("stream_read")
	}
	if len(raw) > compactionBytesLimit {
		return CompactionResult{}, compactionProtocolError("stream_bounds")
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return CompactionResult{}, compactionCanceled(ctxErr)
	}
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, "data:") || strings.HasPrefix(trimmed, "event:") || strings.HasPrefix(trimmed, ":") {
		return collectCompactionSSE(ctx, strings.NewReader(trimmed))
	}
	return parseCompactionJSON([]byte(trimmed))
}

func parseCompactionJSON(raw []byte) (CompactionResult, error) {
	if responseitem.ValidateJSONObject(raw, compactionBytesLimit) != nil {
		return CompactionResult{}, compactionProtocolError("malformed_event")
	}
	var envelope map[string]json.RawMessage
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	if err := dec.Decode(&envelope); err != nil || envelope == nil {
		return CompactionResult{}, compactionProtocolError("malformed_event")
	}
	var responseID, responseObject string
	if json.Unmarshal(envelope["id"], &responseID) != nil || strings.TrimSpace(responseID) == "" ||
		json.Unmarshal(envelope["object"], &responseObject) != nil || responseObject != "response.compaction" {
		return CompactionResult{}, compactionProtocolError("completion_invariant")
	}
	outputs, ok := envelope["output"]
	trimmedOutput := strings.TrimSpace(string(outputs))
	if !ok || len(trimmedOutput) == 0 || trimmedOutput[0] != '[' {
		return CompactionResult{}, compactionProtocolError("unexpected_output")
	}
	var outputRaw []json.RawMessage
	if err := json.Unmarshal(outputs, &outputRaw); err != nil || len(outputRaw) == 0 || len(outputRaw) > compactionEventLimit {
		return CompactionResult{}, compactionProtocolError("stream_bounds")
	}
	items, err := parseDedicatedCompactionOutput(outputRaw)
	if err != nil {
		return CompactionResult{}, err
	}
	var usage *completedUsage
	usageRaw, ok := envelope["usage"]
	if ok && !bytes.Equal(bytes.TrimSpace(usageRaw), []byte("null")) {
		var decoded completedUsage
		if json.Unmarshal(usageRaw, &decoded) != nil || !validCompletedUsage(decoded) {
			return CompactionResult{}, compactionProtocolError("malformed_event")
		}
		usage = &decoded
	}
	return CompactionResult{Output: items, ResponseID: responseID, Usage: usage}, nil
}

func validCompletedUsage(usage completedUsage) bool {
	present := false
	for _, value := range []*int64{usage.InputTokens, usage.OutputTokens, usage.TotalTokens} {
		if value == nil {
			continue
		}
		present = true
		if *value < 0 {
			return false
		}
	}
	return present
}

func parseDedicatedCompactionOutput(outputs []json.RawMessage) ([]inputItem, error) {
	items := make([]inputItem, 0, len(outputs))
	compactionCount := 0
	for _, raw := range outputs {
		if responseitem.ValidateJSONObject(raw, compactionBytesLimit) != nil {
			return nil, compactionProtocolError("malformed_item")
		}
		var header struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &header) != nil {
			return nil, compactionProtocolError("malformed_item")
		}
		switch header.Type {
		case compactionSummaryType:
			if compactionCount > 0 {
				return nil, compactionProtocolError("multiple_compaction")
			}
			compactionCount++
			canonical, err := responseitem.CanonizeCompactionSummaryItemOpaque(raw)
			if err != nil {
				return nil, compactionProtocolError("invalid_compaction_item")
			}
			items = append(items, opaqueResponseItem{raw: canonical})
		case "message":
			if compactionCount != 0 {
				return nil, compactionProtocolError("unexpected_output")
			}
			message, err := parseCompactionRetainedMessage(raw)
			if err != nil {
				return nil, err
			}
			items = append(items, message)
		default:
			// Calls, outputs, reasoning, old compaction items, and extensions are
			// not replacement history accepted from the dedicated endpoint.
			return nil, compactionProtocolError("unexpected_output")
		}
	}
	if compactionCount != 1 {
		return nil, compactionProtocolError("completion_invariant")
	}
	return items, nil
}

func parseCompactionRetainedMessage(raw []byte) (inputItem, error) {
	fields, err := responseitem.ParseObjectFields(raw, retainedMessageFields, compactionRetainedMessageMaxBytes)
	if err != nil {
		return nil, compactionProtocolError("malformed_item")
	}
	var value struct {
		Type    string            `json:"type"`
		ID      string            `json:"id"`
		Status  string            `json:"status"`
		Role    string            `json:"role"`
		Content []json.RawMessage `json:"content"`
		Phase   string            `json:"phase"`
	}
	if json.Unmarshal(raw, &value) != nil || value.Type != "message" || len(value.Content) == 0 {
		return nil, compactionProtocolError("malformed_item")
	}
	idRaw, ok := fields["id"]
	var id string
	if !ok || json.Unmarshal(idRaw, &id) != nil || strings.TrimSpace(id) == "" || len(id) > compactionRetainedMessageMaxIDBytes {
		return nil, compactionProtocolError("malformed_item")
	}
	if _, ok := retainedMessageRoles[value.Role]; !ok {
		return nil, compactionProtocolError("unexpected_output")
	}
	if value.Status != "" {
		if _, ok := retainedMessageStatuses[value.Status]; !ok {
			return nil, compactionProtocolError("unsuccessful_completion")
		}
	}
	if value.Phase != "" {
		if value.Role != "assistant" {
			return nil, compactionProtocolError("unexpected_output")
		}
		if _, ok := retainedMessagePhases[value.Phase]; !ok {
			return nil, compactionProtocolError("unexpected_output")
		}
	}
	if err := validateRetainedMessageContent(value.Content); err != nil {
		return nil, err
	}
	return opaqueResponseItem{raw: append(json.RawMessage(nil), raw...)}, nil
}

func validateRetainedMessageContent(parts []json.RawMessage) error {
	for _, partRaw := range parts {
		if err := validateRetainedContentPart(partRaw); err != nil {
			return err
		}
	}
	return nil
}

func validateRetainedContentPart(raw []byte) error {
	var header struct {
		Type string `json:"type"`
	}
	if responseitem.ValidateJSONObject(raw, compactionRetainedMessageMaxBytes) != nil || json.Unmarshal(raw, &header) != nil {
		return compactionProtocolError("malformed_item")
	}
	var allowed map[string]struct{}
	switch header.Type {
	case "input_text", "output_text", "text", "summary_text", "reasoning_text":
		allowed = map[string]struct{}{"type": {}, "text": {}}
		if header.Type == "output_text" {
			allowed["annotations"] = struct{}{}
			allowed["logprobs"] = struct{}{}
		}
	case "refusal":
		allowed = map[string]struct{}{"type": {}, "refusal": {}}
	case "input_image":
		allowed = map[string]struct{}{"type": {}, "image_url": {}, "detail": {}}
	case "input_file":
		allowed = map[string]struct{}{"type": {}, "filename": {}, "file_url": {}}
	case "input_video":
		allowed = map[string]struct{}{"type": {}, "video_url": {}}
	default:
		return compactionProtocolError("malformed_item")
	}
	fields, err := responseitem.ParseObjectFields(raw, allowed, compactionRetainedMessageMaxBytes)
	if err != nil {
		return compactionProtocolError("malformed_item")
	}
	var typ string
	if json.Unmarshal(fields["type"], &typ) != nil || typ != header.Type {
		return compactionProtocolError("malformed_item")
	}
	switch typ {
	case "input_image":
		if image, ok := fields["image_url"]; !ok || (string(image) != "null" && json.Unmarshal(image, new(string)) != nil) {
			return compactionProtocolError("malformed_item")
		}
		if detail, ok := fields["detail"]; ok {
			var value string
			if json.Unmarshal(detail, &value) != nil || (value != "low" && value != "high" && value != "auto") {
				return compactionProtocolError("malformed_item")
			}
		}
	case "input_file":
		if rawURL, ok := fields["file_url"]; ok && json.Unmarshal(rawURL, new(string)) != nil {
			return compactionProtocolError("malformed_item")
		}
		if rawName, ok := fields["filename"]; ok && json.Unmarshal(rawName, new(string)) != nil {
			return compactionProtocolError("malformed_item")
		}
	case "input_video":
		if rawURL, ok := fields["video_url"]; !ok || json.Unmarshal(rawURL, new(string)) != nil {
			return compactionProtocolError("malformed_item")
		}
	case "refusal":
		refusal, ok := fields["refusal"]
		if !ok || json.Unmarshal(refusal, new(string)) != nil {
			return compactionProtocolError("malformed_item")
		}
	default:
		text, ok := fields["text"]
		if !ok || json.Unmarshal(text, new(string)) != nil {
			return compactionProtocolError("malformed_item")
		}
	}
	return nil
}

func collectCompactionSSE(ctx context.Context, body io.Reader) (CompactionResult, error) {
	if ctx == nil {
		return CompactionResult{}, aipNilContextError()
	}
	if body == nil {
		return CompactionResult{}, compactionProtocolError("missing_stream")
	}
	// Keep framing behavior aligned with the normal Codex stream pump while
	// retaining a stricter, private collector contract for compaction.
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), compactionEventLimitLine)
	var (
		events          int
		bytesRead       int
		created         bool
		completed       bool
		compactionCount int
		result          CompactionResult
		compactionRaw   json.RawMessage
	)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return CompactionResult{}, compactionCanceled(err)
		}
		data, ok := sseDataLine(scanner.Text())
		if !ok {
			continue
		}
		if completed {
			return CompactionResult{}, compactionProtocolError("event_after_completion")
		}
		events++
		bytesRead += len(data)
		if events > compactionEventLimit || bytesRead > compactionBytesLimit {
			return CompactionResult{}, compactionProtocolError("stream_bounds")
		}
		var base struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(data), &base) != nil {
			return CompactionResult{}, compactionProtocolError("malformed_event")
		}
		switch base.Type {
		case "response.created":
			if created || completed {
				return CompactionResult{}, compactionProtocolError("event_order")
			}
			created = true
			var ev struct {
				Response struct {
					ID string `json:"id"`
				} `json:"response"`
			}
			if json.Unmarshal([]byte(data), &ev) != nil || strings.TrimSpace(ev.Response.ID) == "" {
				return CompactionResult{}, compactionProtocolError("malformed_created")
			}
			result.ResponseID = ev.Response.ID
		case "response.output_item.done":
			if !created || completed {
				return CompactionResult{}, compactionProtocolError("event_order")
			}
			var ev struct {
				Item json.RawMessage `json:"item"`
			}
			if json.Unmarshal([]byte(data), &ev) != nil || !json.Valid(ev.Item) {
				return CompactionResult{}, compactionProtocolError("malformed_item")
			}
			var item struct {
				Type   string `json:"type"`
				Status string `json:"status"`
			}
			if json.Unmarshal(ev.Item, &item) != nil || item.Type != compactionTriggerType {
				return CompactionResult{}, compactionProtocolError("unexpected_output")
			}
			compactionCount++
			if compactionCount > 1 {
				return CompactionResult{}, compactionProtocolError("multiple_compaction")
			}
			if compactionRaw != nil {
				return CompactionResult{}, compactionProtocolError("duplicate_compaction")
			}
			canonical, err := validateCompactionOpaque(ev.Item)
			if err != nil {
				return CompactionResult{}, err
			}
			compactionRaw = canonical
		case "response.completed":
			if !created || completed || compactionRaw == nil || compactionCount != 1 {
				return CompactionResult{}, compactionProtocolError("completion_invariant")
			}
			var ev struct {
				Response struct {
					ID     string            `json:"id"`
					Status string            `json:"status"`
					Usage  *completedUsage   `json:"usage"`
					Output []json.RawMessage `json:"output"`
				} `json:"response"`
			}
			if json.Unmarshal([]byte(data), &ev) != nil || ev.Response.Status != "completed" || strings.TrimSpace(ev.Response.ID) == "" || ev.Response.ID != result.ResponseID {
				return CompactionResult{}, compactionProtocolError("unsuccessful_completion")
			}
			if len(ev.Response.Output) != 1 {
				return CompactionResult{}, compactionProtocolError("completion_invariant")
			}
			var terminalHeader struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(ev.Response.Output[0], &terminalHeader) != nil || terminalHeader.Type != compactionTriggerType {
				return CompactionResult{}, compactionProtocolError("unexpected_output")
			}
			terminal, terminalErr := validateCompactionOpaque(ev.Response.Output[0])
			if terminalErr != nil || !jsonEqual(terminal, compactionRaw) {
				return CompactionResult{}, compactionProtocolError("terminal_item_mismatch")
			}
			if strings.TrimSpace(ev.Response.ID) != "" {
				result.ResponseID = ev.Response.ID
			}
			if ev.Response.Usage != nil && !validCompletedUsage(*ev.Response.Usage) {
				return CompactionResult{}, compactionProtocolError("malformed_event")
			}
			result.Usage = ev.Response.Usage
			result.Item = opaqueResponseItem{raw: compactionRaw}
			completed = true
		case "error", "response.failed", "response.incomplete":
			return CompactionResult{}, compactionProtocolError("unsuccessful_completion")
		case "response.output_text.delta", "response.content_part.added", "response.content_part.done", "response.output_item.added", "response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
			return CompactionResult{}, compactionProtocolError("unexpected_output")
		default:
			if !harmlessCompactionEvent(base.Type) {
				return CompactionResult{}, compactionProtocolError("unexpected_event")
			}
		}
	}
	if err := scanner.Err(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return CompactionResult{}, compactionCanceled(ctxErr)
		}
		return CompactionResult{}, compactionProtocolError("stream_read")
	}
	if !completed {
		return CompactionResult{}, compactionProtocolError("missing_completion")
	}
	return result, nil
}

// ReplacementConfig is intentionally connector-local. The predicate is pinned
// to the Codex V2 behavior recorded by the native-context research.
type ReplacementConfig struct {
	Version               string
	PerAgentMessageTokens int64
	TotalMessageTokens    int64
	MaxImages             int
}

const replacementPredicateV1 = "codex.remote_compaction_v2.v1"

type ReplacementResult struct {
	Items                 []inputItem
	SourceEstimatedTokens int64
	ResultEstimatedTokens int64
}

func buildReplacement(history NativeHistory, compaction opaqueResponseItem, cfg ReplacementConfig) (ReplacementResult, error) {
	if cfg.Version == "" {
		cfg.Version = replacementPredicateV1
	}
	if cfg.Version != replacementPredicateV1 || cfg.PerAgentMessageTokens < 0 || cfg.TotalMessageTokens < 0 || cfg.MaxImages < 0 {
		return ReplacementResult{}, compactionProtocolError("replacement_config")
	}
	if _, err := validateCompactionOpaque(compaction.raw); err != nil {
		return ReplacementResult{}, err
	}
	if cfg.TotalMessageTokens == 0 {
		source, err := estimateReplacementSource(history)
		if err != nil {
			return ReplacementResult{}, err
		}
		opaque, err := estimateOpaqueBytes(compaction.raw)
		if err != nil {
			return ReplacementResult{}, err
		}
		return ReplacementResult{Items: []inputItem{opaqueResponseItem{raw: append(json.RawMessage(nil), compaction.raw...)}}, SourceEstimatedTokens: source, ResultEstimatedTokens: opaque}, nil
	}
	var retained []inputItem
	var total int64
	images := 0
	for _, item := range slices.Backward(history.Items) {
		keep, cost, imageCount := replacementCandidate(item, cfg)
		if !keep || total+cost > cfg.TotalMessageTokens || images+imageCount > cfg.MaxImages {
			continue
		}
		retained = append(retained, cloneInputItems([]inputItem{item})[0])
		total += cost
		images += imageCount
	}
	for i, j := 0, len(retained)-1; i < j; i, j = i+1, j-1 {
		retained[i], retained[j] = retained[j], retained[i]
	}
	retained = append(retained, opaqueResponseItem{raw: append(json.RawMessage(nil), compaction.raw...)})
	source, err := estimateReplacementSource(history)
	if err != nil {
		return ReplacementResult{}, err
	}
	opaque, err := estimateOpaqueBytes(compaction.raw)
	if err != nil {
		return ReplacementResult{}, err
	}
	return ReplacementResult{Items: retained, SourceEstimatedTokens: source, ResultEstimatedTokens: total + opaque}, nil
}

func replacementCandidate(item inputItem, cfg ReplacementConfig) (bool, int64, int) {
	text, role, phase, ok := inputMessageShape(item)
	if !ok {
		// Reasoning, calls, and outputs are represented by the checkpoint and must
		// not be duplicated in the retained window.
		return false, 0, 0
	}
	if role == "assistant" && phase != "commentary" {
		return false, 0, 0
	}
	cost := int64((len(text) + 3) / 4)
	if role == "assistant" && cfg.PerAgentMessageTokens > 0 && cost > cfg.PerAgentMessageTokens {
		return false, 0, 0
	}
	return role == "user" || role == "developer" || role == "assistant", cost, countInputImages(item)
}

func inputMessageShape(item inputItem) (text, role, phase string, ok bool) {
	switch v := item.(type) {
	case textMessageItem:
		return v.Content, v.Role, v.phase, true
	case richMessageItem:
		var b strings.Builder
		for _, part := range v.Content {
			if p, ok := part.(inputTextPart); ok {
				b.WriteString(p.Text)
			}
		}
		return b.String(), v.Role, v.phase, true
	default:
		return "", "", "", false
	}
}

func countInputImages(item inputItem) int {
	v, ok := item.(richMessageItem)
	if !ok {
		return 0
	}
	count := 0
	for _, part := range v.Content {
		if _, ok := part.(inputImagePart); ok {
			count++
		}
	}
	return count
}

func estimateReplacementSource(history NativeHistory) (int64, error) {
	estimate, err := estimateHistory(context.Background(), deterministicHistoryEstimator{}, history)
	if err != nil {
		return 0, err
	}
	return estimate.Tokens, nil
}

func estimateOpaqueBytes(raw []byte) (int64, error) {
	estimate, err := estimateHistory(context.Background(), deterministicHistoryEstimator{}, NativeHistory{
		Items: []inputItem{opaqueResponseItem{raw: append(json.RawMessage(nil), raw...)}},
	})
	if err != nil {
		return 0, err
	}
	return estimate.OpaqueTokens, nil
}

func validateCompactionOpaque(raw []byte) (json.RawMessage, error) {
	canonical, err := responseitem.CanonizeCompactionItemOpaque(raw)
	if err != nil {
		return nil, compactionProtocolError("invalid_compaction_item")
	}
	return canonical, nil
}

func compactionProtocolError(reason string) error {
	return newCompactionError(errCompactionProtocol, normalizeCompactionReason(reason), nil)
}

func compactionCanceled(err error) error {
	return newCompactionError(errCompactionCanceled, reasonInvalid, err)
}

func compactionTransportError(err error) error {
	return newCompactionError(errCompactionTransport, reasonInvalid, nil)
}

func aipNilContextError() error {
	return newCompactionError(errCompactionProtocol, reasonNilContext, nil)
}

func normalizeCompactionReason(reason string) compactionReason {
	r := compactionReason(reason)
	switch r {
	case reasonEmptyPrefix, reasonTriggerDuplicate, reasonTriggerMissing, reasonPayloadCopy,
		reasonEventAfterCompletion, reasonStreamBounds, reasonMalformedEvent, reasonEventOrder,
		reasonMalformedCreated, reasonMalformedItem, reasonUnexpectedOutput, reasonMultipleCompaction,
		reasonDuplicateCompaction, reasonCompletionInvariant, reasonTerminalItemMismatch,
		reasonUnsuccessfulCompletion, reasonUnexpectedEvent, reasonStreamRead, reasonMissingCompletion,
		reasonMissingStream, reasonInvalidCompactionItem, reasonReplacementConfig, reasonNilContext:
		return r
	default:
		return reasonInvalid
	}
}

func firstContextError(ctx context.Context, fallback error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fallback
}

func jsonEqual(a, b []byte) bool {
	return bytes.Equal(bytes.TrimSpace(a), bytes.TrimSpace(b))
}

func harmlessCompactionEvent(typ string) bool {
	switch typ {
	case "response.queued", "response.in_progress", "response.usage":
		return true
	default:
		return false
	}
}

func cloneStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]string, len(src))
	maps.Copy(dst, src)
	return dst
}
