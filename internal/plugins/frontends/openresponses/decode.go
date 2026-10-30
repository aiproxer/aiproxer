package openresponses

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/routing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/routeselect"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/sessionwire"
	proto "github.com/aiproxer/aiproxer/internal/plugins/protocols/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
)

var ErrUnauthorized = errors.New("openresponses: unauthorized")

// DecodeCompactOptions supplies context and dependencies for authenticating and decoding a compact request.
type DecodeCompactOptions struct {
	DefaultRouteSelector string
	RoutePrefixes        []string
	RouteSelector        string
	Headers              http.Header
	Auth                 Authorizer
	MaxBodyBytes         int64
	Limits               proto.Limits
	Method               string
	Path                 string
	RemoteAddr           string
	HTTPHeaders          aipsdk.HTTPHeaders
}

// DecodedCompact holds the outcome of authenticated compact request decoding.
type DecodedCompact struct {
	Call          *aipapi.Call
	Requirements  aipapi.ProtocolRequirements
	RouteSelector string
	// RouteOrigin names why the route was chosen: the client named it, or the
	// proxy chose its default route.
	RouteOrigin  routing.RouteOrigin
	Model        string
	AuthDecision sdkauth.Decision
}

// DecodeCreateOptions supplies context and dependencies for authenticating and decoding a create request.
type DecodeCreateOptions struct {
	DefaultRouteSelector string
	RoutePrefixes        []string
	RouteSelector        string
	Headers              http.Header
	Auth                 Authorizer
	MaxBodyBytes         int64
	Limits               proto.Limits
	Method               string
	Path                 string
	RemoteAddr           string
	HTTPHeaders          aipsdk.HTTPHeaders
}

// DecodedCreate holds the outcome of authenticated create request decoding.
type DecodedCreate struct {
	Call          *aipapi.Call
	Requirements  aipapi.ProtocolRequirements
	Stream        bool
	RouteSelector string
	// RouteOrigin names why the route was chosen: the client named it, the
	// proxy chose its default route, or the route stays absent for a
	// continuation to restore from its parent lineage.
	RouteOrigin        routing.RouteOrigin
	Model              string
	PreviousResponseID string
	Store              bool
	ExplicitStore      *bool
	AuthDecision       sdkauth.Decision
}

// Authorizer performs request-path authentication checks before body parsing or continuation store work.
type Authorizer interface {
	Authenticate(ctx context.Context, meta sdkauth.InboundCallMeta) (sdkauth.Decision, error)
}

type createDecodeKind int

const (
	createDecodeHTTP createDecodeKind = iota + 1
	createDecodeWebSocket
)

// AuthenticateAndDecodeCreate performs authentication checks FIRST, then decodes the request body
// into an item-authoritative canonical [aipapi.Call] with protocol requirements and invocation metadata.
func AuthenticateAndDecodeCreate(ctx context.Context, body []byte, opts DecodeCreateOptions) (*DecodedCreate, error) {
	// 1. Auth check BEFORE body or continuation/store work (Requirement 2.10, 10.8)
	decision, err := authenticateDecode(ctx, opts.Auth, opts.Method, opts.Path, opts.RemoteAddr, opts.Headers, opts.HTTPHeaders)
	if err != nil {
		return nil, err
	}

	if len(body) == 0 {
		return nil, proto.ErrDecodeFailed
	}
	if opts.MaxBodyBytes > 0 && int64(len(body)) > opts.MaxBodyBytes {
		return nil, fmt.Errorf("%w: request body exceeds configured limit", proto.ErrDecodeFailed)
	}

	// The frontend owns the create-only stream delivery control. Keep it out of
	// the protocol request codec's canonical payload until that codec exposes it.
	decodeBody, stream, err := splitStreamControl(body)
	if err != nil {
		return nil, err
	}

	decoded, err := decodeCreateBody(decodeBody, opts, createDecodeHTTP, stream)
	if err != nil {
		return nil, err
	}
	decoded.AuthDecision = decision
	return decoded, nil
}

func authenticateDecode(ctx context.Context, auth Authorizer, method, path, remoteAddr string, headers http.Header, httpHeaders aipsdk.HTTPHeaders) (sdkauth.Decision, error) {
	if auth == nil {
		return sdkauth.Decision{}, nil
	}
	decision, err := auth.Authenticate(ctx, sdkauth.InboundCallMeta{
		Frontend:            ID,
		Method:              method,
		Path:                path,
		ClientAddr:          remoteAddr,
		AuthorizationBearer: httpHeaders.APIKeyFrom(headers),
	})
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled):
			return sdkauth.Decision{}, context.Canceled
		case errors.Is(err, context.DeadlineExceeded):
			return sdkauth.Decision{}, context.DeadlineExceeded
		}
		return sdkauth.Decision{}, fmt.Errorf("%w: auth failed", ErrUnauthorized)
	}
	if decision.Outcome != sdkauth.OutcomeAllow {
		return sdkauth.Decision{}, fmt.Errorf("%w: %s", ErrUnauthorized, decision.ReasonCode)
	}
	return decision, nil
}

// decodeCreateBody maps a create payload (stream control already stripped for HTTP,
// envelope type already stripped for WebSocket) onto the shared canonical create call.
func decodeCreateBody(body []byte, opts DecodeCreateOptions, kind createDecodeKind, stream bool) (*DecodedCreate, error) {
	limits := opts.Limits
	if limits == (proto.Limits{}) {
		limits = proto.DefaultLimits()
	}
	wireParam, canonicalCall, err := proto.DecodeRequest(body, limits)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", proto.ErrDecodeFailed, err)
	}

	if err := rejectUnsupportedControls(wireParam, createUnsupportedControls); err != nil {
		return nil, err
	}

	modelStr := ""
	if wireParam.Model != nil {
		modelStr = strings.TrimSpace(*wireParam.Model)
	}
	prevID := ""
	if wireParam.PreviousResponseID != nil {
		prevID = strings.TrimSpace(*wireParam.PreviousResponseID)
	}
	if modelStr == "" && prevID == "" {
		return nil, fmt.Errorf("%w: model is required when previous_response_id is absent", proto.ErrDecodeFailed)
	}
	clientModel := modelStr
	route, routeErr := resolveRouteSelector(modelStr, opts.RouteSelector, opts.RoutePrefixes, opts.DefaultRouteSelector)
	if routeErr != nil {
		return nil, fmt.Errorf("%w: %w", proto.ErrDecodeFailed, routeErr)
	}
	modelStr = route.Selector
	if modelStr == "" && prevID == "" {
		if strings.TrimSpace(opts.DefaultRouteSelector) == "" {
			// No route resolved and no default route to fall back to: the client
			// must name the route, and the operator may configure a default one.
			return nil, fmt.Errorf(
				"%w: route selector is required: send a model with a route prefix "+
					"(for example \"backend:model\") or configure a default route",
				proto.ErrDecodeFailed,
			)
		}
		return nil, fmt.Errorf("%w: model could not be resolved", proto.ErrDecodeFailed)
	}

	store := true
	var explicitStore *bool
	if wireParam.Store != nil {
		store = *wireParam.Store
		explicitStore = wireParam.Store
	}

	canonicalCall.Route = aipapi.RouteIntent{Selector: modelStr}
	deliveryMode := aipapi.DeliveryModeFromClientStream(stream)
	if kind == createDecodeWebSocket {
		deliveryMode = aipapi.DeliveryModeStreaming
	}
	canonicalCall.Invocation = aipapi.Invocation{
		Operation:     aipapi.OperationOpenResponsesCreate,
		DeliveryMode:  deliveryMode,
		TransportMode: aipapi.PreferredTransportMode(deliveryMode),
	}
	metadata, err := decodeMetadata(wireParam.Metadata)
	if err != nil {
		return nil, fmt.Errorf("%w: metadata: %w", proto.ErrDecodeFailed, err)
	}
	canonicalCall.Session.Metadata = metadata
	if opts.Headers != nil {
		canonicalCall.Invocation.ClientUserAgent = strings.TrimSpace(opts.Headers.Get("User-Agent"))
		sessionwire.ApplyAuthoritativeHeaders(&canonicalCall.Session, opts.Headers)
	}
	if err := sessionwire.ApplyWorkspaceHint(&canonicalCall, opts.Headers, opts.HTTPHeaders); err != nil {
		return nil, fmt.Errorf("%w: %w", proto.ErrDecodeFailed, err)
	}

	if err := canonicalCall.Validate(); err != nil {
		return nil, fmt.Errorf("%w: canonical request: %w", proto.ErrDecodeFailed, err)
	}

	return &DecodedCreate{
		Call:               &canonicalCall,
		Requirements:       aipapi.DeriveProtocolRequirements(canonicalCall),
		Stream:             stream || kind == createDecodeWebSocket,
		RouteSelector:      modelStr,
		RouteOrigin:        route.Origin,
		Model:              clientModel,
		PreviousResponseID: prevID,
		Store:              store,
		ExplicitStore:      explicitStore,
	}, nil
}

func splitStreamControl(body []byte) ([]byte, bool, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return nil, false, fmt.Errorf("%w: invalid stream control", proto.ErrDecodeFailed)
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '{' {
		return nil, false, fmt.Errorf("%w: request must be an object", proto.ErrDecodeFailed)
	}
	type field struct {
		key      string
		raw      json.RawMessage
		keyStart int
		valueEnd int
	}
	fields := make([]field, 0, 8)
	seen := make(map[string]struct{})
	for dec.More() {
		keyToken, err := dec.Token()
		key, ok := keyToken.(string)
		if err != nil || !ok {
			return nil, false, fmt.Errorf("%w: invalid request field", proto.ErrDecodeFailed)
		}
		if _, exists := seen[key]; exists {
			return nil, false, fmt.Errorf("%w: duplicate request field %q", proto.ErrDecodeFailed, key)
		}
		keyEnd := int(dec.InputOffset())
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, false, fmt.Errorf("%w: invalid request field %q", proto.ErrDecodeFailed, key)
		}
		valueEnd := int(dec.InputOffset())
		keyStart := jsonStringStart(body, keyEnd)
		if keyStart < 0 || valueEnd > len(body) {
			return nil, false, fmt.Errorf("%w: invalid request field %q", proto.ErrDecodeFailed, key)
		}
		seen[key] = struct{}{}
		fields = append(fields, field{key: key, raw: raw, keyStart: keyStart, valueEnd: valueEnd})
	}
	if _, err := dec.Token(); err != nil {
		return nil, false, fmt.Errorf("%w: invalid request object", proto.ErrDecodeFailed)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, false, fmt.Errorf("%w: trailing request data", proto.ErrDecodeFailed)
	}
	streamIndex := -1
	var streamRaw json.RawMessage
	for i, item := range fields {
		if item.key == "stream" {
			streamIndex = i
			streamRaw = item.raw
			break
		}
	}
	if streamIndex < 0 {
		return body, false, nil
	}
	var stream bool
	if err := json.Unmarshal(streamRaw, &stream); err != nil {
		return nil, false, fmt.Errorf("%w: stream must be a boolean", proto.ErrDecodeFailed)
	}
	start := fields[streamIndex].keyStart
	end := fields[streamIndex].valueEnd
	if streamIndex > 0 {
		start = commaBefore(body, start)
	} else if streamIndex+1 < len(fields) {
		end = commaAfter(body, end)
	}
	if start < 1 || end <= start || end > len(body) {
		return nil, false, fmt.Errorf("%w: stream control", proto.ErrDecodeFailed)
	}
	withoutStream := make([]byte, 0, len(body)-(end-start))
	withoutStream = append(withoutStream, body[:start]...)
	withoutStream = append(withoutStream, body[end:]...)
	return withoutStream, stream, nil
}

func jsonStringStart(data []byte, end int) int {
	if end > len(data) {
		end = len(data)
	}
	i := end - 1
	for i >= 0 && (data[i] == ' ' || data[i] == '\t' || data[i] == '\r' || data[i] == '\n') {
		i--
	}
	if i < 0 || data[i] != '"' {
		return -1
	}
	for i--; i >= 0; i-- {
		if data[i] != '"' {
			continue
		}
		backslashes := 0
		for j := i - 1; j >= 0 && data[j] == '\\'; j-- {
			backslashes++
		}
		if backslashes%2 == 0 {
			return i
		}
	}
	return -1
}

func commaBefore(data []byte, start int) int {
	i := start - 1
	for i >= 0 && (data[i] == ' ' || data[i] == '\t' || data[i] == '\r' || data[i] == '\n') {
		i--
	}
	if i >= 0 && data[i] == ',' {
		return i
	}
	return start
}

func commaAfter(data []byte, end int) int {
	i := end
	for i < len(data) && (data[i] == ' ' || data[i] == '\t' || data[i] == '\r' || data[i] == '\n') {
		i++
	}
	if i < len(data) && data[i] == ',' {
		return i + 1
	}
	return end
}

func decodeMetadata(raw json.RawMessage) (map[string]string, error) {
	if len(strings.TrimSpace(string(raw))) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return nil, nil
	}
	var metadata map[string]string
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return nil, err
	}
	if err := sessionwire.ValidateMetadata(metadata); err != nil {
		return nil, err
	}
	return metadata, nil
}

// unsupportedControl describes one official request control the canonical call
// cannot represent. Admission-time rejection of a non-null value prevents it
// from reaching execution while silently ignored. Null and omitted values stay
// accepted per the pinned schema.
type unsupportedControl struct {
	name  string
	isSet func(*proto.WireResponseParam) bool
}

// createUnsupportedControls are the create-operation controls that must fail
// create admission when non-null. instructions is deliberately absent here: the
// protocol decoder maps it losslessly into a leading canonical system message
// item, so it forwards instead of being rejected. Metadata is deliberately
// excluded: it maps to Call.Session.Metadata end-to-end.
var createUnsupportedControls = []unsupportedControl{
	{"include", func(p *proto.WireResponseParam) bool { return p.Include != nil }},
	{"presence_penalty", func(p *proto.WireResponseParam) bool { return p.PresencePenalty != nil }},
	{"frequency_penalty", func(p *proto.WireResponseParam) bool { return p.FrequencyPenalty != nil }},
	{"stream_options", func(p *proto.WireResponseParam) bool { return isPresentNonNullJSON(p.StreamOptions) }},
	{"top_logprobs", func(p *proto.WireResponseParam) bool { return p.TopLogprobs != nil }},
	{"text", func(p *proto.WireResponseParam) bool { return !supportedTextFormat(p.Text) }},
	{"truncation", func(p *proto.WireResponseParam) bool { return p.Truncation != nil }},
	{"service_tier", func(p *proto.WireResponseParam) bool { return p.ServiceTier != nil }},
	{"safety_identifier", func(p *proto.WireResponseParam) bool { return p.SafetyIdentifier != nil }},
	{"prompt_cache_key", func(p *proto.WireResponseParam) bool { return p.PromptCacheKey != nil }},
	{"prompt_cache_retention", func(p *proto.WireResponseParam) bool { return p.PromptCacheRetention != nil }},
	{"max_tool_calls", func(p *proto.WireResponseParam) bool { return p.MaxToolCalls != nil }},
}

func supportedTextFormat(raw json.RawMessage) bool {
	if !isPresentNonNullJSON(raw) {
		return true
	}
	var text struct {
		Format json.RawMessage `json:"format"`
	}
	if json.Unmarshal(raw, &text) != nil {
		return false
	}
	if len(text.Format) == 0 || bytes.Equal(bytes.TrimSpace(text.Format), []byte("null")) {
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil {
			return false
		}
		return len(fields) == 0 || (len(fields) == 1 && fields["format"] != nil)
	}
	var f map[string]json.RawMessage
	if json.Unmarshal(text.Format, &f) != nil || len(f) != 1 {
		return false
	}
	var typ string
	if json.Unmarshal(f["type"], &typ) != nil {
		return false
	}
	return typ == "text" || typ == "json_object"
}

// compactUnsupportedControls are the request controls absent from the pinned
// compact schema (compactResponseMethodPublicBodySchema), which permits only
// model, input, previous_response_id, instructions, and prompt_cache_key.
// instructions and prompt_cache_key are deliberately absent here: instructions
// maps into a leading canonical system item (protocol decoder) and
// prompt_cache_key forwards on the canonical call, so both are carried rather
// than dropped. Every other pinned create control is rejected before network.
var compactUnsupportedControls = []unsupportedControl{
	{"tools", func(p *proto.WireResponseParam) bool { return len(p.Tools) > 0 }},
	{"tool_choice", func(p *proto.WireResponseParam) bool { return isPresentNonNullJSON(p.ToolChoice) }},
	{"parallel_tool_calls", func(p *proto.WireResponseParam) bool { return p.ParallelToolCalls != nil }},
	{"temperature", func(p *proto.WireResponseParam) bool { return p.Temperature != nil }},
	{"top_p", func(p *proto.WireResponseParam) bool { return p.TopP != nil }},
	{"max_output_tokens", func(p *proto.WireResponseParam) bool { return p.MaxOutputTokens != nil }},
	{"metadata", func(p *proto.WireResponseParam) bool { return isPresentNonNullJSON(p.Metadata) }},
	{"include", func(p *proto.WireResponseParam) bool { return p.Include != nil }},
	{"presence_penalty", func(p *proto.WireResponseParam) bool { return p.PresencePenalty != nil }},
	{"frequency_penalty", func(p *proto.WireResponseParam) bool { return p.FrequencyPenalty != nil }},
	{"stream_options", func(p *proto.WireResponseParam) bool { return isPresentNonNullJSON(p.StreamOptions) }},
	{"top_logprobs", func(p *proto.WireResponseParam) bool { return p.TopLogprobs != nil }},
	{"text", func(p *proto.WireResponseParam) bool { return isPresentNonNullJSON(p.Text) }},
	{"truncation", func(p *proto.WireResponseParam) bool { return p.Truncation != nil }},
	{"service_tier", func(p *proto.WireResponseParam) bool { return p.ServiceTier != nil }},
	{"safety_identifier", func(p *proto.WireResponseParam) bool { return p.SafetyIdentifier != nil }},
	{"prompt_cache_retention", func(p *proto.WireResponseParam) bool { return p.PromptCacheRetention != nil }},
	{"max_tool_calls", func(p *proto.WireResponseParam) bool { return p.MaxToolCalls != nil }},
}

// rejectUnsupportedControls fails admission when a request sets an official but
// unsupported non-null control. The error wraps proto.ErrDecodeFailed so the
// stable invalid_request classification matches existing decode failures.
func rejectUnsupportedControls(wire *proto.WireResponseParam, controls []unsupportedControl) error {
	for _, c := range controls {
		if c.isSet(wire) {
			return fmt.Errorf("%w: non-null request control %q is not supported", proto.ErrDecodeFailed, c.name)
		}
	}
	return nil
}

// resolveRouteSelector resolves the route of one request and the origin that
// produced it: the client named it, the proxy chose its default route, or the
// route stays absent for a continuation to restore from its parent lineage.
func resolveRouteSelector(model, explicit string, prefixes []string, defaultRoute string) (routing.ResolvedRoute, error) {
	if selector := strings.TrimSpace(explicit); selector != "" {
		if len(prefixes) > 0 && routeselect.NewPrefixSet(prefixes).InlineOrDefault(selector, "") == "" {
			return routing.ResolvedRoute{}, fmt.Errorf("route selector %q is not allowed by configured route prefixes", selector)
		}
		return routing.ExplicitRoute(selector), nil
	}
	if len(prefixes) == 0 {
		return routing.ExplicitRoute(model), nil
	}
	if strings.TrimSpace(model) == "" {
		return routing.ResolvedRoute{}, nil
	}
	return routeselect.NewPrefixSet(prefixes).ResolveInlineOrDefault(model, defaultRoute), nil
}

func isCompactPath(path string) bool {
	return strings.HasSuffix(strings.TrimRight(path, "/"), "/responses/compact")
}

func isCreatePath(path string) bool {
	trimmed := strings.TrimRight(path, "/")
	if isCompactPath(trimmed) {
		return false
	}
	return strings.HasSuffix(trimmed, "/responses") || trimmed == "/responses"
}

func extractBearerToken(h http.Header) string {
	return aipsdk.HTTPHeaders{}.APIKeyFrom(h)
}

// AuthenticateAndDecodeCompact performs authentication checks FIRST, then decodes the request body
// into an item-authoritative canonical [aipapi.Call] with context.compaction operation and protocol requirements.
func AuthenticateAndDecodeCompact(ctx context.Context, body []byte, opts DecodeCompactOptions) (*DecodedCompact, error) {
	decision, err := authenticateDecode(ctx, opts.Auth, opts.Method, opts.Path, opts.RemoteAddr, opts.Headers, opts.HTTPHeaders)
	if err != nil {
		return nil, err
	}

	if len(body) == 0 {
		return nil, proto.ErrDecodeFailed
	}
	if opts.MaxBodyBytes > 0 && int64(len(body)) > opts.MaxBodyBytes {
		return nil, fmt.Errorf("%w: request body exceeds configured limit", proto.ErrDecodeFailed)
	}

	// 2. Reject forbidden compact fields (stream, store, previous_response_id, background)
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(body, &rawMap); err != nil {
		return nil, fmt.Errorf("%w: %w", proto.ErrDecodeFailed, err)
	}
	for _, forbidden := range []string{"stream", "store", "previous_response_id", "background"} {
		if _, ok := rawMap[forbidden]; ok {
			return nil, fmt.Errorf("%w: %s field is forbidden in compact request", proto.ErrDecodeFailed, forbidden)
		}
	}

	// 3. Decode wire request using Task 2 protocol codec
	limits := opts.Limits
	if limits == (proto.Limits{}) {
		limits = proto.DefaultLimits()
	}
	wireParam, canonicalCall, err := proto.DecodeRequest(body, limits)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", proto.ErrDecodeFailed, err)
	}

	// Compact admission: reject non-null controls absent from the pinned compact
	// schema. instructions maps into a leading canonical system item in the
	// protocol decoder, and prompt_cache_key is carried on the canonical call
	// below, so both are forwarded instead of dropped.
	if err := rejectUnsupportedControls(wireParam, compactUnsupportedControls); err != nil {
		return nil, err
	}

	// 4. Validate model is required
	modelStr := ""
	if wireParam.Model != nil {
		modelStr = strings.TrimSpace(*wireParam.Model)
	}
	if modelStr == "" {
		return nil, fmt.Errorf("%w: model is required for compact request", proto.ErrDecodeFailed)
	}
	clientModel := modelStr

	// 5. Resolve route selector, validating explicit transport overrides against
	// the same prefix allowlist as body model selectors.
	var routeErr error
	route, routeErr := resolveRouteSelector(modelStr, opts.RouteSelector, opts.RoutePrefixes, opts.DefaultRouteSelector)
	if routeErr != nil {
		return nil, fmt.Errorf("%w: %w", proto.ErrDecodeFailed, routeErr)
	}
	modelStr = route.Selector
	if modelStr == "" {
		return nil, fmt.Errorf("%w: model could not be resolved", proto.ErrDecodeFailed)
	}

	// 6. Validate input is present and item-authoritative
	if len(canonicalCall.Items) == 0 || !canonicalCall.HasItemAuthority() {
		return nil, fmt.Errorf("%w: input is required for compact request", proto.ErrDecodeFailed)
	}

	// 7. Construct canonical Call for compaction
	canonicalCall.Route = aipapi.RouteIntent{Selector: modelStr}
	canonicalCall.Invocation = aipapi.Invocation{
		Operation:     aipapi.OperationContextCompaction,
		DeliveryMode:  aipapi.DeliveryModeNonStreaming,
		TransportMode: aipapi.TransportModeNonStreaming,
	}
	// The pinned compact schema permits prompt_cache_key; carry it on the
	// canonical call so the generic backend forwards it instead of dropping it.
	if wireParam.PromptCacheKey != nil {
		canonicalCall.PromptCacheKey = strings.TrimSpace(*wireParam.PromptCacheKey)
	}
	if opts.Headers != nil {
		canonicalCall.Invocation.ClientUserAgent = strings.TrimSpace(opts.Headers.Get("User-Agent"))
		sessionwire.ApplyAuthoritativeHeaders(&canonicalCall.Session, opts.Headers)
	}
	if err := sessionwire.ApplyWorkspaceHint(&canonicalCall, opts.Headers, opts.HTTPHeaders); err != nil {
		return nil, fmt.Errorf("%w: %w", proto.ErrDecodeFailed, err)
	}

	if err := canonicalCall.Validate(); err != nil {
		return nil, fmt.Errorf("%w: canonical request: %w", proto.ErrDecodeFailed, err)
	}

	// 8. Derive protocol requirements and ensure CapabilityCompaction is included
	requirements := aipapi.DeriveProtocolRequirements(canonicalCall)
	if !slices.Contains(requirements.Capabilities, aipapi.CapabilityCompaction) {
		requirements.Capabilities = append(requirements.Capabilities, aipapi.CapabilityCompaction)
		requirements = aipapi.NormalizeProtocolRequirements(requirements)
	}

	return &DecodedCompact{
		Call:          &canonicalCall,
		Requirements:  requirements,
		RouteSelector: modelStr,
		RouteOrigin:   route.Origin,
		Model:         clientModel,
		AuthDecision:  decision,
	}, nil
}
