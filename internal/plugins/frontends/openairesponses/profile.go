package openairesponses

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/routing"

	"github.com/aiproxer/aiproxer/internal/compactionfacts"
	"github.com/aiproxer/aiproxer/internal/core/jsonshape"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	frontendlimits "github.com/aiproxer/aiproxer/internal/plugins/frontends/limits"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/sessionwire"
	"github.com/aiproxer/aiproxer/internal/plugins/openrouterwire"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

type chunkWriterFunc func([]byte) (int, error)

func (f chunkWriterFunc) Write(p []byte) (int, error) {
	return f(p)
}

// InputStringResolver returns a jsonshape string resolver that maps the OpenAI
// Responses top-level "input" string onto a canonical single user message whose
// text part is whitespace-trimmed in a streaming, bounded-memory way.
//
// This wire-field interpretation belongs to the OpenAI Responses protocol edge.
// Generic core compiles proofs without knowing any wire field name; a protocol
// that spells its single-prompt field differently supplies its own resolver.
func InputStringResolver(idw *largebody.CallIdentityWriter) jsonshape.StringWriterResolver {
	return func(sctx jsonshape.StringContext) (io.Writer, error) {
		if sctx.TopLevel && sctx.Key == "input" {
			mw, err := idw.BeginMessage(aipapi.RoleUser)
			if err != nil {
				return nil, err
			}
			pw, err := mw.BeginTextPart()
			if err != nil {
				return nil, err
			}
			return largebody.NewTrimSpaceWriter(pw), nil
		}
		return nil, nil
	}
}

// ProfileID is the static identifier for the certified OpenAI Responses fast-path profile (Requirements 4, 17).
const ProfileID = "openai_responses_v1"

// ErrHostedWebSearchRequiresCanonicalDecode reports that the bounded proof pass
// met a hosted web-search declaration in raw top-level tools. The proof pass
// cannot carry the frontend's declaration carrier, so it declines to canonical
// decode BEFORE parseTools can accept the shape; the canonical path preserves
// the closed declaration table for backend admission and policy. It must never
// wire-execute a hosted shape, independent of the selected backend or the
// backend's WireBackend availability.
var ErrHostedWebSearchRequiresCanonicalDecode = errors.New("openairesponses: hosted web search declaration requires canonical decode")

// parseProofTools enforces the hosted-declaration guard BEFORE the shared
// tools parser. Keeping the guard here rather than inline in CompileProof holds
// that function's measured cognitive complexity at its baseline while still
// declining to canonical processing before parseTools sees a hosted shape.
func parseProofTools(raw json.RawMessage) (parsedTools, error) {
	if hasHostedWebSearchDeclaration(raw) {
		return parsedTools{}, ErrHostedWebSearchRequiresCanonicalDecode
	}
	if hasNamespaceToolDeclaration(raw) {
		return parsedTools{}, ErrNamespaceToolsRequiresCanonicalDecode
	}
	return parseTools(raw, namespaceToolPolicyProject)
}

// namespaceHistoryField reports whether the event is a namespace field inside
// the input array, carrying the previous observation forward.
func namespaceHistoryField(e jsonshape.Event, seen bool) bool {
	if e.Type == jsonshape.EventKey && e.Key == "namespace" && len(e.Path) > 0 && e.Path[0] == "input" {
		return true
	}
	return seen
}

// Profile implements frontendpipe.FrontendProfile for OpenAI Responses create (POST /v1/responses).
type Profile struct{}

// NewProfile returns an initialized OpenAI Responses frontend profile.
func NewProfile() *Profile {
	return &Profile{}
}

// ProfileID returns the static identifier for this certified profile.
func (p *Profile) ProfileID() string {
	return ProfileID
}

type stringInspector struct {
	trimmer          *largebody.TrimSpaceWriter
	totalBytes       int64
	hasNonWhitespace bool
}

func newStringInspector() *stringInspector {
	return &stringInspector{
		trimmer: largebody.NewTrimSpaceWriter(io.Discard),
	}
}

func (si *stringInspector) Write(p []byte) (int, error) {
	n, err := si.trimmer.Write(p)
	if si.trimmer.TrimmedBytes() > 0 {
		si.hasNonWhitespace = true
	}
	return n, err
}

func (si *stringInspector) Close() error {
	err := si.trimmer.Close()
	si.totalBytes = si.trimmer.TrimmedBytes()
	if si.totalBytes > 0 {
		si.hasNonWhitespace = true
	}
	return err
}

// CompileProof compiles protocol proof and response seeds from the
// captured request replay under decode admission (Requirements 4, 13, 14, 16, 17).
// Uses the streaming proof pass without allocating or retaining the full request body.
func (p *Profile) CompileProof(ctx context.Context, in frontendpipe.ProofInput) (frontendpipe.ProofOutput, error) {
	// 1. Path verification
	if !strings.HasSuffix(in.URLPath, "/responses") && in.URLPath != "/responses" {
		return frontendpipe.ProofOutput{}, errors.New("openairesponses: unsupported url path")
	}

	if in.Source == nil {
		return frontendpipe.ProofOutput{}, errors.New("openairesponses: nil replay source")
	}

	scanCtx := in.Ctx
	if scanCtx == nil {
		scanCtx = ctx
	}
	factBudget := largebody.SemanticFactBudget(scanCtx)

	bodyBytes := in.BodyBytes
	if bodyBytes <= 0 {
		bodyBytes = in.Source.Size()
	}

	rc, err := in.Source.Open()
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: open replay source: %w", err)
	}
	defer func() { _ = rc.Close() }()

	// 2. Pass 1: Streaming scan over replay bytes using jsonshape.Scanner + SHA-256 in fixed buffers (no ReadAll).
	// Enforces:
	//   - RejectDuplicateNames (canonical-only if duplicate keys)
	//   - Track exact byte span for "model"
	//   - Detect unknown top-level keys (canonical-only if unknown)
	//   - Detect unsupported control keys: "store", "previous_response_id", "truncation" (canonical-only)
	//   - Extract small envelope fields (model, stream, instructions, tools, etc.) bounded by factBudget
	//   - Detect input presence and shape (string vs array) without materializing large input in memory
	tracker := jsonshape.NewTopLevelSpanTracker("model", "input")
	var unknownKey string
	var hasUnsupportedControl bool

	var modelBuf bytes.Buffer
	var instructionsBuf bytes.Buffer
	inputInspector := newStringInspector()
	var toolChoiceStrBuf bytes.Buffer

	var stream bool
	var parallelTools *bool
	var temperature *float64
	var topP *float64
	var maxOut *int

	// For large array input role tracking and decline (Item 2):
	var arrayItemCount int
	var largeArrayRoles []aipapi.Role
	var largeArrayDeclineErr error
	currentItemRole := aipapi.RoleUser
	var currentItemHasContent bool
	var itemRoleBuf bytes.Buffer
	var itemTypeBuf bytes.Buffer

	// Composite field buffers (tools, text, metadata, tool_choice object, input array)
	// Semantic-fact budget enforcement point 1: Bounded by factBudget.
	fieldBufs := make(map[string]*bytes.Buffer)
	for _, k := range []string{"tools", "tool_choice", "text", "metadata"} {
		fieldBufs[k] = &bytes.Buffer{}
	}
	var inputArrayBuf bytes.Buffer
	inputArrayIsLarge := bodyBytes > 2*factBudget
	var totalFactBytes int64

	var hasInput bool
	var inputIsString bool
	var inputIsArray bool
	var inputHasNamespace bool
	var toolChoiceIsString bool

	// Chunk tracking for composite extraction and number tokens across chunk boundaries
	var currentChunk []byte
	var prevChunkTail []byte
	var chunkStartOffset int64
	var totalBytesRead int64

	extractNumberStr := func(e jsonshape.Event) (string, error) {
		return frontendpipe.ExtractNumberToken(e, currentChunk, prevChunkTail, chunkStartOffset, "openairesponses")
	}

	var capturingKey string
	var capturingBuf *bytes.Buffer
	var capturingStart int64

	var capturingInputArray bool
	var inputArrayStart int64

	handler := jsonshape.EventHandlerFunc(func(e jsonshape.Event) error {
		if e.TopLevel && e.Type == jsonshape.EventKey {
			if !responsesKnownBodyKeys[e.Key] {
				unknownKey = e.Key
			}
			if e.Key == "store" || e.Key == "previous_response_id" || e.Key == "truncation" || e.Key == "prompt_cache_key" {
				hasUnsupportedControl = true
			}
			if e.Key == "input" {
				hasInput = true
			}
		}
		// Any namespace field inside the input array is a namespaced history
		// carrier. The wire path forwards raw bytes and cannot restore the
		// client identity, so decline to canonical decode regardless of where
		// the item sits or whether the input array is too large to parse.
		inputHasNamespace = namespaceHistoryField(e, inputHasNamespace)

		if e.TopLevel && e.Type != jsonshape.EventKey && e.Type != jsonshape.EventArrayEnd && e.Type != jsonshape.EventObjectEnd {
			switch e.Key {
			case "instructions":
				if e.Type != jsonshape.EventString && e.Type != jsonshape.EventNull {
					return errors.New("openairesponses: instructions must be a JSON string in this adapter")
				}
			case "tools":
				if e.Type == jsonshape.EventArrayStart {
					capturingKey = e.Key
					capturingBuf = fieldBufs[e.Key]
					capturingStart = e.Offset
					capturingBuf.Reset()
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openairesponses: tools must be an array")
				}
			case "text":
				if e.Type == jsonshape.EventObjectStart {
					capturingKey = e.Key
					capturingBuf = fieldBufs[e.Key]
					capturingStart = e.Offset
					capturingBuf.Reset()
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openairesponses: text must be an object")
				}
			case "tool_choice":
				if e.Type == jsonshape.EventString {
					toolChoiceIsString = true
				} else if e.Type == jsonshape.EventObjectStart {
					capturingKey = e.Key
					capturingBuf = fieldBufs[e.Key]
					capturingStart = e.Offset
					capturingBuf.Reset()
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openairesponses: tool_choice must be a string or object")
				}
			case "stream":
				if e.Type == jsonshape.EventTrue {
					stream = true
				} else if e.Type == jsonshape.EventFalse {
					stream = false
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openairesponses: stream must be a boolean")
				}
			case "parallel_tool_calls":
				if e.Type == jsonshape.EventTrue {
					v := true
					parallelTools = &v
				} else if e.Type == jsonshape.EventFalse {
					v := false
					parallelTools = &v
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openairesponses: parallel_tool_calls must be a boolean")
				}
			case "temperature":
				if e.Type == jsonshape.EventNumber {
					numStr, err := extractNumberStr(e)
					if err != nil {
						return err
					}
					v, err := strconv.ParseFloat(numStr, 64)
					if err != nil {
						return fmt.Errorf("openairesponses: temperature: %w", err)
					}
					temperature = &v
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openairesponses: temperature must be a number")
				}
			case "top_p":
				if e.Type == jsonshape.EventNumber {
					numStr, err := extractNumberStr(e)
					if err != nil {
						return err
					}
					v, err := strconv.ParseFloat(numStr, 64)
					if err != nil {
						return fmt.Errorf("openairesponses: top_p: %w", err)
					}
					topP = &v
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openairesponses: top_p must be a number")
				}
			case "max_output_tokens":
				if e.Type == jsonshape.EventNumber {
					numStr, err := extractNumberStr(e)
					if err != nil {
						return err
					}
					v, err := strconv.Atoi(numStr)
					if err != nil {
						return fmt.Errorf("openairesponses: max_output_tokens must be an integer: %w", err)
					}
					maxOut = &v
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openairesponses: max_output_tokens must be an integer")
				}
			case "model":
				if e.Type != jsonshape.EventString {
					return errors.New("openairesponses: model must be a string")
				}
			case "input":
				switch e.Type {
				case jsonshape.EventString:
					inputIsString = true
				case jsonshape.EventArrayStart:
					inputIsArray = true
					capturingInputArray = true
					inputArrayStart = e.Offset
					inputArrayBuf.Reset()
				default:
					return errors.New("openairesponses: input must be a string or array")
				}
			case "metadata":
				if e.Type == jsonshape.EventObjectStart {
					capturingKey = e.Key
					capturingBuf = fieldBufs[e.Key]
					capturingStart = e.Offset
					capturingBuf.Reset()
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openairesponses: metadata must be an object")
				}
			}
		}

		if e.TopLevel {
			switch e.Type {
			case jsonshape.EventArrayEnd:
				if e.Key == "input" {
					if capturingInputArray && !inputArrayIsLarge {
						start := max(chunkStartOffset, inputArrayStart)
						end := e.Offset + e.Length
						if start < end && end <= chunkStartOffset+int64(len(currentChunk)) {
							slice := currentChunk[start-chunkStartOffset : end-chunkStartOffset]
							if int64(inputArrayBuf.Len()+len(slice)) > factBudget {
								inputArrayIsLarge = true
								inputArrayBuf.Reset()
							} else {
								inputArrayBuf.Write(slice)
							}
						}
					}
					capturingInputArray = false
				} else if e.Key == capturingKey && capturingBuf != nil {
					start := max(chunkStartOffset, capturingStart)
					end := e.Offset + e.Length
					if start < end && end <= chunkStartOffset+int64(len(currentChunk)) {
						slice := currentChunk[start-chunkStartOffset : end-chunkStartOffset]
						// Semantic-fact budget enforcement point 1: retain through the
						// shared before-write budget leaf so oversize metadata never
						// lands in the buffer.
						bounded := &frontendpipe.BudgetBoundedWriter{Buf: capturingBuf, Total: &totalFactBytes, MaxBudget: factBudget}
						if _, err := bounded.Write(slice); err != nil {
							return err
						}
					}
					capturingKey = ""
					capturingBuf = nil
				}
			case jsonshape.EventObjectEnd:
				if e.Key == capturingKey && capturingBuf != nil {
					start := max(chunkStartOffset, capturingStart)
					end := e.Offset + e.Length
					if start < end && end <= chunkStartOffset+int64(len(currentChunk)) {
						slice := currentChunk[start-chunkStartOffset : end-chunkStartOffset]
						// Semantic-fact budget enforcement point 1: retain through the
						// shared before-write budget leaf so oversize metadata never
						// lands in the buffer.
						bounded := &frontendpipe.BudgetBoundedWriter{Buf: capturingBuf, Total: &totalFactBytes, MaxBudget: factBudget}
						if _, err := bounded.Write(slice); err != nil {
							return err
						}
					}
					capturingKey = ""
					capturingBuf = nil
				}
			}
		}

		// Item 2: Inspect array items inside input
		if !e.TopLevel && len(e.Path) >= 1 && e.Path[0] == "input" {
			if len(e.Path) == 1 {
				switch e.Type {
				case jsonshape.EventObjectStart:
					arrayItemCount++
					if arrayItemCount > frontendlimits.MaxMessages {
						return frontendlimits.Count("input", arrayItemCount, frontendlimits.MaxMessages)
					}
					currentItemRole = aipapi.RoleUser
					currentItemHasContent = false
				case jsonshape.EventObjectEnd:
					if !currentItemHasContent && largeArrayDeclineErr == nil {
						largeArrayDeclineErr = errors.New("openairesponses: input item missing content")
					}
					largeArrayRoles = append(largeArrayRoles, currentItemRole)
					currentItemRole = aipapi.RoleUser
					itemRoleBuf.Reset()
					itemTypeBuf.Reset()
				default:
					if largeArrayDeclineErr == nil {
						largeArrayDeclineErr = errors.New("openairesponses: input array item must be an object")
					}
				}
			} else if len(e.Path) == 2 {
				switch e.Type {
				case jsonshape.EventKey:
					if e.Key != "role" && e.Key != "content" && e.Key != "type" && largeArrayDeclineErr == nil {
						largeArrayDeclineErr = fmt.Errorf("openairesponses: unsupported input item field %q requires canonical decode", e.Key)
					}
				case jsonshape.EventString:
					switch e.Key {
					case "role":
						rStr := strings.TrimSpace(itemRoleBuf.String())
						switch rStr {
						case "user":
							currentItemRole = aipapi.RoleUser
						case "assistant":
							currentItemRole = aipapi.RoleAssistant
						case "system":
							currentItemRole = aipapi.RoleSystem
						default:
							if largeArrayDeclineErr == nil {
								largeArrayDeclineErr = fmt.Errorf("openairesponses: unsupported role %q requires canonical decode", rStr)
							}
						}
					case "type":
						tStr := strings.TrimSpace(itemTypeBuf.String())
						if tStr != "" && tStr != "message" && largeArrayDeclineErr == nil {
							largeArrayDeclineErr = fmt.Errorf("openairesponses: unsupported input item type %q requires canonical decode", tStr)
						}
					case "content":
						currentItemHasContent = true
					}
				default:
					if largeArrayDeclineErr == nil {
						largeArrayDeclineErr = fmt.Errorf("openairesponses: input item field %q must be a string", e.Key)
					}
				}
			} else {
				if largeArrayDeclineErr == nil {
					largeArrayDeclineErr = errors.New("openairesponses: complex nested input structure requires canonical decode")
				}
			}
		}

		return tracker.OnEvent(e)
	})

	strResolver := func(sctx jsonshape.StringContext) (io.Writer, error) {
		if sctx.TopLevel {
			switch sctx.Key {
			case "model":
				return &frontendpipe.BudgetBoundedWriter{Buf: &modelBuf, Total: &totalFactBytes, MaxBudget: factBudget}, nil
			case "instructions":
				return &frontendpipe.BudgetBoundedWriter{Buf: &instructionsBuf, Total: &totalFactBytes, MaxBudget: factBudget}, nil
			case "tool_choice":
				return &frontendpipe.BudgetBoundedWriter{Buf: &toolChoiceStrBuf, Total: &totalFactBytes, MaxBudget: factBudget}, nil
			case "input":
				inputIsString = true
				return inputInspector, nil
			}
		}
		if len(sctx.Path) >= 1 && sctx.Path[0] == "input" {
			if sctx.Key == "role" {
				itemRoleBuf.Reset()
				return &frontendpipe.BudgetBoundedWriter{Buf: &itemRoleBuf, Total: &totalFactBytes, MaxBudget: factBudget}, nil
			}
			if sctx.Key == "type" {
				itemTypeBuf.Reset()
				return &frontendpipe.BudgetBoundedWriter{Buf: &itemTypeBuf, Total: &totalFactBytes, MaxBudget: factBudget}, nil
			}
		}
		return nil, nil
	}

	hasher := sha256.New()
	scannerLimits := jsonshape.Limits{
		RejectDuplicateNames: true, // Carry-over nit 1: set explicitly to prevent silent relaxation
		MaxBytes:             bodyBytes,
		MaxDepth:             128,
	}

	scanner := jsonshape.NewScanner(
		scanCtx,
		scannerLimits,
		jsonshape.WithEventHandler(handler),
		jsonshape.WithStringWriterResolver(strResolver),
	)

	onChunk := func(chunk []byte) error {
		chunkEndOffset := chunkStartOffset + int64(len(chunk))

		if capturingKey != "" && capturingBuf != nil {
			start := max(chunkStartOffset, capturingStart)
			if start < chunkEndOffset {
				slice := chunk[start-chunkStartOffset:]
				// Semantic-fact budget enforcement point 1: retain through the
				// shared before-write budget leaf so oversize metadata never
				// lands in the buffer.
				bounded := &frontendpipe.BudgetBoundedWriter{Buf: capturingBuf, Total: &totalFactBytes, MaxBudget: factBudget}
				if _, err := bounded.Write(slice); err != nil {
					return err
				}
				capturingStart = chunkEndOffset
			}
		}

		if capturingInputArray && !inputArrayIsLarge {
			start := max(chunkStartOffset, inputArrayStart)
			if start < chunkEndOffset {
				slice := chunk[start-chunkStartOffset:]
				if int64(inputArrayBuf.Len()+len(slice)) > factBudget {
					inputArrayIsLarge = true
					inputArrayBuf.Reset()
				} else {
					inputArrayBuf.Write(slice)
					inputArrayStart = chunkEndOffset
				}
			}
		}

		tailLen := min(len(chunk), 256)
		prevChunkTail = append(prevChunkTail[:0], chunk[len(chunk)-tailLen:]...)

		return nil
	}

	readRes, err := largebody.ProcessReplayChunks(largebody.ReplayChunkReaderConfig{
		Reader: &frontendpipe.ChunkHookReader{
			R: rc,
			OnRead: func(chunk []byte) {
				currentChunk = chunk
				chunkStartOffset = totalBytesRead
				totalBytesRead += int64(len(chunk))
			},
		},
		MaxBytes:  bodyBytes,
		ChunkSize: 32 * 1024,
		Scanner:   scanner,
		Hasher:    hasher,
		OnChunk:   onChunk,
	})
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: process replay chunks: %w", err)
	}
	if bodyBytes <= 0 {
		bodyBytes = readRes.BytesRead
	}

	if unknownKey != "" {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: unknown body key %q", unknownKey)
	}
	switch {
	case hasUnsupportedControl:
		return frontendpipe.ProofOutput{}, errors.New("openairesponses: unsupported control key (store/previous_response_id/truncation)")
	case inputHasNamespace:
		return frontendpipe.ProofOutput{}, ErrNamespaceHistoryRequiresCanonicalDecode
	}

	if inputIsArray && inputArrayIsLarge {
		if arrayItemCount > frontendlimits.MaxMessages {
			return frontendpipe.ProofOutput{}, frontendlimits.Count("input", arrayItemCount, frontendlimits.MaxMessages)
		}
		if largeArrayDeclineErr != nil {
			return frontendpipe.ProofOutput{}, largeArrayDeclineErr
		}
	}

	// 3. Verify top-level "model" span
	modelSpanRaw, hasModel := tracker.Span("model")
	if !hasModel || modelSpanRaw.Length == 0 {
		return frontendpipe.ProofOutput{}, errors.New("openairesponses: model is required")
	}
	modelSpan := largebody.Span{Offset: modelSpanRaw.Offset, Length: modelSpanRaw.Length}
	rewrite, err := largebody.NewModelTokenRewrite(modelSpan)
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: model rewrite: %w", err)
	}

	model := strings.TrimSpace(modelBuf.String())
	if model == "" {
		return frontendpipe.ProofOutput{}, errors.New("openairesponses: model is required")
	}

	// 4. Verify input presence and shape
	if !hasInput {
		return frontendpipe.ProofOutput{}, errors.New("openairesponses: input is required")
	}
	if !inputIsString && !inputIsArray {
		return frontendpipe.ProofOutput{}, errors.New("openairesponses: input must be a string or array")
	}
	if inputIsString && !inputInspector.hasNonWhitespace {
		return frontendpipe.ProofOutput{}, errors.New("openairesponses: input string is empty")
	}

	// 5. Parse metadata
	var metadata map[string]string
	if raw := fieldBufs["metadata"].Bytes(); len(raw) > 0 {
		if err := json.Unmarshal(raw, &metadata); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: metadata: %w", err)
		}
	}
	if len(metadata) > 0 {
		if err := frontendlimits.Count("metadata", len(metadata), frontendlimits.MaxMetadata); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: %w", err)
		}
		// Requirement 14.2, 17.5: Reject body-carried AIProxer session metadata
		if sessionwire.HasSessionMetadata(metadata) {
			return frontendpipe.ProofOutput{}, largebody.ErrBodySessionMetadataRejected
		}
	}

	// 6. Resolve route selector precedence (Requirement 4.8, 17.6)
	route := routing.ExplicitRoute(in.RouteSelector)
	if route.Selector == "" && in.RouteFromBodyModel {
		route = in.RoutePrefixes.ResolveInlineOrDefault(model, in.DefaultRouteSelector)
	}
	sel := route.Selector
	if sel == "" {
		return frontendpipe.ProofOutput{}, errors.New("openairesponses: route selector is required: send a model with a route prefix (for example \"backend:model\")")
	}

	// 7. Parse instructions
	instructions := []aipapi.Message{}
	if instructionsBuf.Len() > 0 {
		s := strings.TrimSpace(instructionsBuf.String())
		if s != "" {
			instructions = []aipapi.Message{{
				Role:  aipapi.RoleSystem,
				Parts: []aipapi.Part{aipapi.TextPart(s)},
			}}
		}
	}

	// 8. Parse tools and options.
	//
	// parseProofTools recognizes the raw hosted shape BEFORE parseTools accepts
	// it and declines to canonical processing. Otherwise a hosted declaration
	// could be silently dropped or raw-forwarded to a WireBackend before
	// admission.
	parsedToolSet, err := parseProofTools(fieldBufs["tools"].Bytes())
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: tools: %w", err)
	}
	tools := parsedToolSet.Tools

	var toolChoice aipapi.ToolChoice
	if toolChoiceIsString {
		s := toolChoiceStrBuf.String()
		switch strings.TrimSpace(strings.ToLower(s)) {
		case "auto", "":
			toolChoice = aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto}
		case "none":
			toolChoice = aipapi.ToolChoice{Mode: aipapi.ToolChoiceNone}
		case "required":
			toolChoice = aipapi.ToolChoice{Mode: aipapi.ToolChoiceAny}
		default:
			return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: unsupported tool_choice string %q", s)
		}
	} else if fieldBufs["tool_choice"].Len() > 0 {
		tc, err := parseToolChoice(fieldBufs["tool_choice"].Bytes(), nil)
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: tool_choice: %w", err)
		}
		toolChoice = tc
	} else {
		toolChoice = aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto}
	}

	verbosity, remainingText, err := parseTextConfig(fieldBufs["text"].Bytes())
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: text: %w", err)
	}

	// 9. Build extensions
	modelRaw, err := json.Marshal(model)
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: marshal model: %w", err)
	}
	ext := map[string]json.RawMessage{extModelJSONKey: modelRaw}
	if b, err := json.Marshal(openrouterwire.FlavorResponses); err == nil {
		ext[openrouterwire.ExtUpstreamFlavor] = b
	}
	if len(remainingText) > 0 {
		ext[openrouterwire.ExtraBodyExtPrefix+"text"] = remainingText
	}
	if in.Headers != nil {
		openrouterwire.CaptureHeaders(in.Headers, ext)
	}

	// Explicit workspace hint projects to the canonical extension on the
	// bounded path as well; invalid values decline to canonical decode,
	// which returns the existing protocol 400 (Requirements 1.1, 1.3).
	workspaceCall := &aipapi.Call{Extensions: ext}
	if err := sessionwire.ApplyWorkspaceHint(workspaceCall, in.Headers, aipsdk.HTTPHeaders{}); err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: %w", err)
	}

	// 10. Extract session input
	// Semantic-fact budget enforcement point 2: sessionwire enforces MaxFactBytes
	sessIn, err := sessionwire.BuildSessionInput(in.Headers, metadata, sessionwire.SessionInputOptions{
		RejectBodyMetadata: true,
		MaxFactBytes:       factBudget,
	})
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: session input: %w", err)
	}

	// Requirement 16.2, 16.7: X-AIP-Session-Hint feeds ClientSessionID into wire digest
	// while canonical pre-core decode never sets it. To prevent identity divergence,
	// requests carrying a client session hint must decline to canonical processing.
	if sessIn.ClientSessionID != "" {
		return frontendpipe.ProofOutput{}, errors.New("openairesponses: session hint requires canonical decode")
	}

	// 11. Build identity configuration
	idCfg := largebody.CallIdentityConfig{
		Session: aipapi.SessionRef{
			AuthoritativeSessionID: sessIn.AuthoritativeSessionID,
			ClientSessionID:        sessIn.ClientSessionID,
			ResumeToken:            sessIn.ResumeToken.Reveal(),
		},
		SessionInput:  sessIn,
		RouteSelector: sel,
		Instructions:  instructions,
		Tools:         tools,
		ToolChoice:    toolChoice,
		Options: aipapi.GenerationOptions{
			Temperature:       temperature,
			TopP:              topP,
			MaxOutputTokens:   maxOut,
			ParallelToolCalls: parallelTools,
			Verbosity:         verbosity,
		},
		Extensions: ext,
	}

	var digest largebody.IdentityDigest
	var turnShape largebody.ClientTurnShape
	var msgs []aipapi.Message

	if inputIsString {
		// Pass 2: Stream input string directly through CompileStreamingProof without full body allocation.
		rc2, err := in.Source.Open()
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: open replay source (pass 2): %w", err)
		}
		defer func() { _ = rc2.Close() }()

		streamingCfg := largebody.StreamingProofConfig{
			Reader:               rc2,
			MaxBytes:             bodyBytes,
			ChunkSize:            32 * 1024,
			CallIdentityConfig:   idCfg,
			StringWriterResolver: InputStringResolver,
			ScannerLimits: jsonshape.Limits{
				RejectDuplicateNames: true, // Carry-over nit 1: set explicitly to prevent silent relaxation
				MaxBytes:             bodyBytes,
				MaxDepth:             128,
			},
		}
		streamRes, err := largebody.CompileStreamingProof(scanCtx, streamingCfg)
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: streaming proof: %w", err)
		}
		digest = streamRes.Digest

		// Build turn shape directly from instructions and counted input string bytes
		turnShape = largebody.ClientTurnShape{
			Items: make([]largebody.ClientTurnItemShape, 0, len(instructions)+1),
		}
		var ordinal int64
		for _, inst := range instructions {
			var instBytes int64
			for _, p := range inst.Parts {
				instBytes += int64(len(p.Text))
			}
			turnShape.Items = append(turnShape.Items, largebody.ClientTurnItemShape{
				Kind:    aipapi.ItemKindMessage,
				Role:    aipapi.RoleSystem,
				Ordinal: ordinal,
				Parts: []largebody.ClientTurnPartShape{{
					Kind:         aipapi.ContentPartText,
					ContentBytes: instBytes,
				}},
			})
			turnShape.TotalContentBytes += instBytes
			ordinal++
		}
		turnShape.Items = append(turnShape.Items, largebody.ClientTurnItemShape{
			Kind:    aipapi.ItemKindMessage,
			Role:    aipapi.RoleUser,
			Ordinal: ordinal,
			Parts: []largebody.ClientTurnPartShape{{
				Kind:         aipapi.ContentPartText,
				ContentBytes: inputInspector.totalBytes,
			}},
		})
		turnShape.TotalContentBytes += inputInspector.totalBytes
	} else if !inputArrayIsLarge {
		// Small array input: parse items into messages using canonical parseInputItem
		var items []json.RawMessage
		if err := json.Unmarshal(inputArrayBuf.Bytes(), &items); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: input array: %w", err)
		}
		if err := frontendlimits.Count("input", len(items), frontendlimits.MaxMessages); err != nil {
			return frontendpipe.ProofOutput{}, err
		}
		msgs = make([]aipapi.Message, 0, len(items))
		for i, it := range items {
			m, err := parseInputItem(it, nil, namespaceToolPolicyProject)
			if err != nil {
				// Requirement 17.3: Malformed histories/aliases that current decoders repair/drop/normalize are canonical-only.
				if errors.Is(err, errSkipMalformedHistory) {
					return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: malformed history requires canonical repair: %w", err)
				}
				return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: input[%d]: %w", i, err)
			}
			for _, part := range m.Parts {
				if part.Kind != aipapi.PartToolResult {
					continue
				}
				trimmedContent := bytes.TrimSpace(part.Content)
				if len(trimmedContent) == 0 || trimmedContent[0] != '"' {
					// Requirement 9/17: non-string function_call_output outputs are
					// re-encoded as strings by the canonical backend while the wire
					// path forwards raw bytes; keep that shape canonical-only.
					return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: input[%d]: non-string function_call_output output requires canonical encoding", i)
				}
			}
			msgs = append(msgs, m)
		}

		// Semantic-fact budget enforcement point 3: ClientTurnShapeFromCall bounds turn facts
		ts, err := largebody.ClientTurnShapeFromCall(&aipapi.Call{
			Instructions: instructions,
			Messages:     msgs,
		}, factBudget)
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: turn shape: %w", err)
		}
		turnShape = ts

		idWriter, err := largebody.NewCallIdentityWriter(idCfg)
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: identity writer: %w", err)
		}
		if msgs != nil {
			if err := idWriter.StartMessages(); err != nil {
				return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: start messages: %w", err)
			}
			for _, msg := range msgs {
				if err := idWriter.AddMessage(msg); err != nil {
					return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: add message: %w", err)
				}
			}
		}
		d, err := idWriter.Digest()
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: digest: %w", err)
		}
		digest = d
	} else {
		// Large chunked array input (e.g. 20 MiB chunked messages): stream in Pass 2
		rc2, err := in.Source.Open()
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: open replay source (pass 2): %w", err)
		}
		defer func() { _ = rc2.Close() }()

		idWriter, err := largebody.NewCallIdentityWriter(idCfg)
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: identity writer: %w", err)
		}
		if err := idWriter.StartMessages(); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: start messages: %w", err)
		}

		var msgIndex int
		var currMsgWriter *largebody.MessageIdentityWriter
		var currTrimmer *largebody.TrimSpaceWriter
		var turnItems []largebody.ClientTurnItemShape
		var totalPartBytes int64
		ordinal := int64(len(instructions))

		arrayStrResolver := func(sctx jsonshape.StringContext) (io.Writer, error) {
			if len(sctx.Path) >= 2 && sctx.Path[0] == "input" && sctx.Key == "content" {
				if currMsgWriter != nil {
					_ = currMsgWriter.EndMessage()
				}
				role := aipapi.RoleUser
				if msgIndex < len(largeArrayRoles) {
					role = largeArrayRoles[msgIndex]
				}
				mw, err := idWriter.BeginMessage(role)
				if err != nil {
					return nil, err
				}
				currMsgWriter = mw
				pw, err := mw.BeginTextPart()
				if err != nil {
					return nil, err
				}
				currTrimmer = largebody.NewTrimSpaceWriter(pw)
				return currTrimmer, nil
			}
			return nil, nil
		}

		arrayHandler := jsonshape.EventHandlerFunc(func(e jsonshape.Event) error {
			if len(e.Path) >= 2 && e.Path[0] == "input" && e.Type == jsonshape.EventString && e.Key == "content" {
				role := aipapi.RoleUser
				if msgIndex < len(largeArrayRoles) {
					role = largeArrayRoles[msgIndex]
				}
				var partBytes int64
				if currTrimmer != nil {
					partBytes = currTrimmer.TrimmedBytes()
				}
				if partBytes == 0 {
					return errors.New("openairesponses: message content string is empty")
				}
				turnItems = append(turnItems, largebody.ClientTurnItemShape{
					Kind:    aipapi.ItemKindMessage,
					Role:    role,
					Ordinal: ordinal,
					Parts: []largebody.ClientTurnPartShape{{
						Kind:         aipapi.ContentPartText,
						ContentBytes: partBytes,
					}},
				})
				totalPartBytes += partBytes
				ordinal++
				msgIndex++
			}
			return nil
		})

		arrayScanner := jsonshape.NewScanner(
			scanCtx,
			jsonshape.Limits{
				RejectDuplicateNames: true, // Carry-over nit 1: set explicitly
				MaxBytes:             bodyBytes,
				MaxDepth:             128,
			},
			jsonshape.WithEventHandler(arrayHandler),
			jsonshape.WithStringWriterResolver(arrayStrResolver),
		)

		if _, err := largebody.ProcessReplayChunks(largebody.ReplayChunkReaderConfig{
			Reader:    rc2,
			MaxBytes:  bodyBytes,
			ChunkSize: 32 * 1024,
			Scanner:   arrayScanner,
		}); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: array streaming proof: %w", err)
		}

		if currMsgWriter != nil {
			_ = currMsgWriter.EndMessage()
		}
		d, err := idWriter.Digest()
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: digest: %w", err)
		}
		digest = d

		var instItems []largebody.ClientTurnItemShape
		var instBytesTotal int64
		var instOrd int64
		for _, inst := range instructions {
			var b int64
			for _, p := range inst.Parts {
				b += int64(len(p.Text))
			}
			instItems = append(instItems, largebody.ClientTurnItemShape{
				Kind:    aipapi.ItemKindMessage,
				Role:    aipapi.RoleSystem,
				Ordinal: instOrd,
				Parts: []largebody.ClientTurnPartShape{{
					Kind:         aipapi.ContentPartText,
					ContentBytes: b,
				}},
			})
			instBytesTotal += b
			instOrd++
		}
		turnShape = largebody.ClientTurnShape{
			Items:             append(instItems, turnItems...),
			TotalContentBytes: instBytesTotal + totalPartBytes,
		}
	}

	// 12. Compute source digest from Pass 1 full replay hasher
	var sumArr [32]byte
	copy(sumArr[:], hasher.Sum(nil))
	sourceDigest := largebody.NewSourceDigest(sumArr)

	var maxTokens int64
	if maxOut != nil {
		maxTokens = int64(*maxOut)
	}

	cancellationID := ""
	if sessIn.ALegID != "" {
		cancellationID = frontendpipe.FormatOpenAICancellationCarrier(sessIn.ALegID, sessIn.AuthoritativeSessionID)
	}
	if cancellationID == "" {
		cancellationID = "resp_" + digest.Token()
	}

	seeds := frontendpipe.NewResponseStateSeeds(
		digest,
		"",
		sel,
		model,
		stream,
		sessIn,
		cancellationID,
	)

	var proofCompactionFacts compactionfacts.RequestFacts
	var proofCompactionComplete bool
	if len(turnShape.Items) > 0 {
		if facts, complete, err := compileStreamingCompactionFacts(scanCtx, in.Source, bodyBytes, instructionsBuf.String(), tools, turnShape.Items, inputIsString, factBudget); err == nil && complete {
			proofCompactionFacts = facts
			proofCompactionComplete = complete
		}
	}

	hasTools := len(tools) > 0 || hasHistoryTools(msgs)
	requiredCaps := largebody.DeriveRequiredCapabilities(turnShape, largebody.ControlRequirements{
		Delivery:          aipapi.DeliveryModeFromClientStream(stream),
		HasTools:          hasTools,
		ParallelToolCalls: parallelTools,
		ReasoningEffort:   "",
		StructuredOutputs: false,
		ItemAuthoritative: false,
	})

	proof := largebody.Proof{
		ProfileID:            ProfileID,
		Operation:            aipapi.OperationOpenAIResponses,
		Delivery:             aipapi.DeliveryModeFromClientStream(stream),
		RouteSelector:        sel,
		RouteOrigin:          route.Origin,
		ClientModel:          model,
		MaxOutputTokens:      maxTokens,
		CompactionFacts:      proofCompactionFacts,
		CompactionComplete:   proofCompactionComplete,
		RequiredCapabilities: requiredCaps,
		// Bounded classification evidence for this turn, compiled from the tool
		// definitions and the request headers already observed above. Only the
		// accepted identity and fixed category bits survive; the ToolDef slice and
		// the header map stay local to this compiler (requirements 5.2, 5.5, 12.7).
		ClassificationEvidence: frontendpipe.CompileClassificationEvidence(
			aipapi.OperationOpenAIResponses, in.Headers, tools),
		Facts: largebody.ProtocolFacts{
			RequirementsID: ProfileID,
			ControlCount:   int64(len(tools)),
		},
		Mode:      largebody.BodyModeIdentityJSON,
		Rewrite:   rewrite,
		ModelSpan: modelSpan,
		Identity:  digest,
		Turn:      turnShape,
		Session:   sessIn,
		Source:    sourceDigest,
		BodyBytes: bodyBytes,
	}

	proofOut := frontendpipe.ProofOutput{
		State: frontendpipe.FrontendWireState{
			ProfileID: ProfileID,
			Proof:     proof,
			Seeds:     seeds,
		},
	}

	// Semantic-fact budget enforcement point 4: proofOut validates Turn and Facts budgets
	if err := proofOut.Validate(factBudget); err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openairesponses: validate proof output: %w", err)
	}

	return proofOut, nil
}

func compileStreamingCompactionFacts(
	ctx context.Context,
	src largebody.Source,
	bodyBytes int64,
	instructionText string,
	tools []aipapi.ToolDef,
	items []largebody.ClientTurnItemShape,
	inputIsString bool,
	maxFactBytes int64,
) (compactionfacts.RequestFacts, bool, error) {
	if int64(len(items)*compactionfacts.ItemHashSizeBytes) > maxFactBytes {
		return compactionfacts.RequestFacts{}, false, nil
	}
	factBuilder := compactionfacts.NewBuilderWithByteBudget(aipapi.OperationOpenAIResponses, int(maxFactBytes))
	factBuilder.AddToolCount(len(tools))

	trimmedInstr := strings.TrimSpace(instructionText)
	if trimmedInstr != "" {
		instHasher := compactionfacts.NewItemHasher(aipapi.ItemKindMessage, aipapi.RoleSystem)
		pw := instHasher.BeginContentText(len(trimmedInstr))
		_, _ = pw.Write([]byte(trimmedInstr))
		instHasher.EndContentText()
		factBuilder.FeedField(trimmedInstr)
		if err := factBuilder.AddItemHash(instHasher.Sum()); err != nil {
			return compactionfacts.RequestFacts{}, false, err
		}
	}

	inputStartIdx := 0
	if trimmedInstr != "" {
		inputStartIdx = 1
	}
	inputItems := items[inputStartIdx:]
	if len(inputItems) == 0 {
		facts, err := factBuilder.Build()
		return facts, err == nil, err
	}

	rc, err := src.Open()
	if err != nil {
		return compactionfacts.RequestFacts{}, false, err
	}
	defer func() { _ = rc.Close() }()

	if inputIsString {
		var trimmer *largebody.TrimSpaceWriter
		var hasher *compactionfacts.ItemHasher
		var factErr error
		var streamed bool

		resolver := func(sctx jsonshape.StringContext) (io.Writer, error) {
			if sctx.TopLevel && sctx.Key == "input" {
				partBytes := int(inputItems[0].Parts[0].ContentBytes)
				hasher = compactionfacts.NewItemHasher(aipapi.ItemKindMessage, aipapi.RoleUser)
				pw := hasher.BeginContentText(partBytes)
				mw := io.MultiWriter(pw, chunkWriterFunc(func(p []byte) (int, error) {
					factBuilder.FeedTextChunkBytes(p)
					return len(p), nil
				}))
				trimmer = largebody.NewTrimSpaceWriter(mw)
				return trimmer, nil
			}
			return nil, nil
		}

		handler := jsonshape.EventHandlerFunc(func(e jsonshape.Event) error {
			if e.TopLevel && e.Type == jsonshape.EventString && e.Key == "input" {
				if trimmer != nil {
					_ = trimmer.Close()
				}
				if hasher != nil {
					hasher.EndContentText()
					factBuilder.EndField()
					itemHash := hasher.Sum()
					if err := factBuilder.AddItemHash(itemHash); err != nil {
						factErr = err
						return err
					}
					streamed = true
				}
			}
			return nil
		})

		scanner := jsonshape.NewScanner(
			ctx,
			jsonshape.Limits{
				RejectDuplicateNames: true,
				MaxBytes:             bodyBytes,
				MaxDepth:             128,
			},
			jsonshape.WithEventHandler(handler),
			jsonshape.WithStringWriterResolver(resolver),
		)

		_, readErr := largebody.ProcessReplayChunks(largebody.ReplayChunkReaderConfig{
			Reader:    rc,
			MaxBytes:  bodyBytes,
			ChunkSize: 32 * 1024,
			Scanner:   scanner,
		})
		if readErr != nil || factErr != nil || !streamed {
			return compactionfacts.RequestFacts{}, false, readErr
		}
	} else {
		var msgIdx int
		var trimmer *largebody.TrimSpaceWriter
		var hasher *compactionfacts.ItemHasher
		var factErr error

		resolver := func(sctx jsonshape.StringContext) (io.Writer, error) {
			if len(sctx.Path) >= 2 && sctx.Path[0] == "input" && sctx.Key == "content" {
				if msgIdx >= len(inputItems) {
					return nil, errors.New("openairesponses: unexpected extra input item in compaction pass")
				}
				itemShape := inputItems[msgIdx]
				partBytes := 0
				if len(itemShape.Parts) > 0 {
					partBytes = int(itemShape.Parts[0].ContentBytes)
				}
				hasher = compactionfacts.NewItemHasher(aipapi.ItemKindMessage, itemShape.Role)
				pw := hasher.BeginContentText(partBytes)
				mw := io.MultiWriter(pw, chunkWriterFunc(func(p []byte) (int, error) {
					factBuilder.FeedTextChunkBytes(p)
					return len(p), nil
				}))
				trimmer = largebody.NewTrimSpaceWriter(mw)
				return trimmer, nil
			}
			return nil, nil
		}

		handler := jsonshape.EventHandlerFunc(func(e jsonshape.Event) error {
			if len(e.Path) >= 2 && e.Path[0] == "input" && e.Type == jsonshape.EventString && e.Key == "content" {
				if trimmer != nil {
					_ = trimmer.Close()
				}
				if hasher != nil {
					hasher.EndContentText()
					factBuilder.EndField()
					itemHash := hasher.Sum()
					if err := factBuilder.AddItemHash(itemHash); err != nil {
						factErr = err
						return err
					}
				}
				msgIdx++
			}
			return nil
		})

		scanner := jsonshape.NewScanner(
			ctx,
			jsonshape.Limits{
				RejectDuplicateNames: true,
				MaxBytes:             bodyBytes,
				MaxDepth:             128,
			},
			jsonshape.WithEventHandler(handler),
			jsonshape.WithStringWriterResolver(resolver),
		)

		_, readErr := largebody.ProcessReplayChunks(largebody.ReplayChunkReaderConfig{
			Reader:    rc,
			MaxBytes:  bodyBytes,
			ChunkSize: 32 * 1024,
			Scanner:   scanner,
		})
		if readErr != nil || factErr != nil || msgIdx != len(inputItems) {
			return compactionfacts.RequestFacts{}, false, readErr
		}
	}

	facts, berr := factBuilder.Build()
	if berr != nil {
		return compactionfacts.RequestFacts{}, false, berr
	}
	return facts, true, nil
}

var _ frontendpipe.FrontendProfile = (*Profile)(nil)

func hasHistoryTools(msgs []aipapi.Message) bool {
	for _, m := range msgs {
		if m.Role == aipapi.RoleTool {
			return true
		}
		for _, p := range m.Parts {
			if p.Kind == aipapi.PartToolResult || (p.Kind == aipapi.PartJSON && p.ToolCallID != "") {
				return true
			}
			if p.Kind == aipapi.PartJSON && bytes.Contains(p.Content, []byte(`"function_call"`)) {
				return true
			}
		}
	}
	return false
}
