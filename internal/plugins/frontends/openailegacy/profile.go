package openailegacy

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
	"github.com/aiproxer/aiproxer/internal/core/jsonpresence"
	"github.com/aiproxer/aiproxer/internal/core/jsonshape"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	frontendlimits "github.com/aiproxer/aiproxer/internal/plugins/frontends/limits"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/sessionwire"
	"github.com/aiproxer/aiproxer/internal/plugins/openrouterwire"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// ProfileID is the static identifier for the certified OpenAI Chat fast-path profile (Requirements 4, 17).
const ProfileID = "openai_chat_v1"

// Profile implements frontendpipe.FrontendProfile for OpenAI Chat Completions create (POST /v1/chat/completions).
type Profile struct{}

// NewProfile returns an initialized OpenAI Chat frontend profile.
func NewProfile() *Profile {
	return &Profile{}
}

// ProfileID returns the static identifier for this certified profile.
func (p *Profile) ProfileID() string {
	return ProfileID
}

var chatSupportedBodyKeys = map[string]bool{
	"model":               true,
	"messages":            true,
	"stream":              true,
	"tools":               true,
	"tool_choice":         true,
	"temperature":         true,
	"top_p":               true,
	"max_tokens":          true,
	"parallel_tool_calls": true,
	"reasoning_effort":    true,
	"verbosity":           true,
	"stream_options":      true,
	"metadata":            true,
}

type chunkWriterFunc func([]byte) (int, error)

func (f chunkWriterFunc) Write(p []byte) (int, error) {
	return f(p)
}

// CompileProof compiles protocol proof and response seeds from the
// captured request replay under decode admission (Requirements 4, 13, 14, 16, 17).
// Uses the streaming proof pass without allocating or retaining the full request body.
func (p *Profile) CompileProof(ctx context.Context, in frontendpipe.ProofInput) (frontendpipe.ProofOutput, error) {
	// 1. Path verification
	if !strings.HasSuffix(in.URLPath, "/chat/completions") && in.URLPath != "/chat/completions" {
		return frontendpipe.ProofOutput{}, errors.New("openailegacy: unsupported url path")
	}

	if in.Source == nil {
		return frontendpipe.ProofOutput{}, errors.New("openailegacy: nil replay source")
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
		return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: open replay source: %w", err)
	}
	defer func() { _ = rc.Close() }()

	// 2. Pass 1: Streaming scan over replay bytes using jsonshape.Scanner + SHA-256 in fixed buffers (no ReadAll).
	// Enforces:
	//   - RejectDuplicateNames (canonical-only if duplicate keys)
	//   - Track exact byte span for "model"
	//   - Detect unknown top-level keys (canonical-only if unknown)
	//   - Extract small envelope fields (model, stream, options, tools, etc.) bounded by factBudget
	//   - Detect messages presence and shape without materializing large messages in memory
	tracker := jsonshape.NewTopLevelSpanTracker("model")
	var unknownKey string

	var modelBuf bytes.Buffer
	var reasoningEffortBuf bytes.Buffer
	var verbosityBuf bytes.Buffer
	var toolChoiceStrBuf bytes.Buffer

	var stream bool
	var parallelTools *bool
	var temperature *float64
	var topP *float64
	var maxTokens *int

	var hasMessages bool
	var messagesIsArray bool
	var toolChoiceIsString bool

	// For large array tracking:
	var arrayItemCount int
	var largeArrayRoles []aipapi.Role
	var largeArrayDeclineErr error
	currentItemRole := aipapi.RoleUser
	var currentItemHasContent bool
	var currentItemHasRole bool
	var itemRoleBuf bytes.Buffer
	var itemTypeBuf bytes.Buffer

	// Composite field buffers (tools, tool_choice object, stream_options, metadata)
	// Semantic-fact budget enforcement point 1: Bounded by factBudget.
	fieldBufs := make(map[string]*bytes.Buffer)
	for _, k := range []string{"tools", "tool_choice", "stream_options", "metadata"} {
		fieldBufs[k] = &bytes.Buffer{}
	}
	var messagesArrayBuf bytes.Buffer
	messagesArrayIsLarge := bodyBytes > 2*factBudget
	var totalFactBytes int64

	// Chunk tracking for composite extraction and number tokens across chunk boundaries
	var currentChunk []byte
	var prevChunkTail []byte
	var chunkStartOffset int64
	var totalBytesRead int64

	extractNumberStr := func(e jsonshape.Event) (string, error) {
		return frontendpipe.ExtractNumberToken(e, currentChunk, prevChunkTail, chunkStartOffset, "openailegacy")
	}

	var capturingKey string
	var capturingBuf *bytes.Buffer
	var capturingStart int64

	var capturingMessagesArray bool
	var messagesArrayStart int64

	handler := jsonshape.EventHandlerFunc(func(e jsonshape.Event) error {
		if e.TopLevel && e.Type == jsonshape.EventKey {
			if !chatSupportedBodyKeys[e.Key] {
				unknownKey = e.Key
			}
			if e.Key == "messages" {
				hasMessages = true
			}
		}

		if e.TopLevel && e.Type != jsonshape.EventKey && e.Type != jsonshape.EventArrayEnd && e.Type != jsonshape.EventObjectEnd {
			switch e.Key {
			case "model":
				if e.Type != jsonshape.EventString {
					return errors.New("openailegacy: model must be a string")
				}
			case "messages":
				if e.Type == jsonshape.EventArrayStart {
					messagesIsArray = true
					capturingMessagesArray = true
					messagesArrayStart = e.Offset
					messagesArrayBuf.Reset()
				} else {
					return errors.New("openailegacy: messages must be an array")
				}
			case "stream":
				if e.Type == jsonshape.EventTrue {
					stream = true
				} else if e.Type == jsonshape.EventFalse {
					stream = false
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openailegacy: stream must be a boolean")
				}
			case "tools":
				if e.Type == jsonshape.EventArrayStart {
					capturingKey = e.Key
					capturingBuf = fieldBufs[e.Key]
					capturingStart = e.Offset
					capturingBuf.Reset()
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openailegacy: tools must be an array")
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
					return errors.New("openailegacy: tool_choice must be a string or object")
				}
			case "parallel_tool_calls":
				if e.Type == jsonshape.EventTrue {
					v := true
					parallelTools = &v
				} else if e.Type == jsonshape.EventFalse {
					v := false
					parallelTools = &v
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openailegacy: parallel_tool_calls must be a boolean")
				}
			case "temperature":
				if e.Type == jsonshape.EventNumber {
					numStr, err := extractNumberStr(e)
					if err != nil {
						return err
					}
					v, err := strconv.ParseFloat(numStr, 64)
					if err != nil {
						return fmt.Errorf("openailegacy: temperature: %w", err)
					}
					temperature = &v
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openailegacy: temperature must be a number")
				}
			case "top_p":
				if e.Type == jsonshape.EventNumber {
					numStr, err := extractNumberStr(e)
					if err != nil {
						return err
					}
					v, err := strconv.ParseFloat(numStr, 64)
					if err != nil {
						return fmt.Errorf("openailegacy: top_p: %w", err)
					}
					topP = &v
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openailegacy: top_p must be a number")
				}
			case "max_tokens":
				if e.Type == jsonshape.EventNumber {
					numStr, err := extractNumberStr(e)
					if err != nil {
						return err
					}
					v, err := strconv.Atoi(numStr)
					if err != nil {
						return fmt.Errorf("openailegacy: max_tokens must be an integer: %w", err)
					}
					maxTokens = &v
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openailegacy: max_tokens must be an integer")
				}
			case "reasoning_effort":
				if e.Type != jsonshape.EventString && e.Type != jsonshape.EventNull {
					return errors.New("openailegacy: reasoning_effort must be a string")
				}
			case "verbosity":
				if e.Type != jsonshape.EventString && e.Type != jsonshape.EventNull {
					return errors.New("openailegacy: verbosity must be a string")
				}
			case "stream_options":
				if e.Type == jsonshape.EventObjectStart {
					capturingKey = e.Key
					capturingBuf = fieldBufs[e.Key]
					capturingStart = e.Offset
					capturingBuf.Reset()
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openailegacy: stream_options must be an object")
				}
			case "metadata":
				if e.Type == jsonshape.EventObjectStart {
					capturingKey = e.Key
					capturingBuf = fieldBufs[e.Key]
					capturingStart = e.Offset
					capturingBuf.Reset()
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openailegacy: metadata must be an object")
				}
			}
		}

		if e.TopLevel {
			switch e.Type {
			case jsonshape.EventArrayEnd:
				if e.Key == "messages" {
					if capturingMessagesArray && !messagesArrayIsLarge {
						start := max(chunkStartOffset, messagesArrayStart)
						end := e.Offset + e.Length
						if start < end && end <= chunkStartOffset+int64(len(currentChunk)) {
							slice := currentChunk[start-chunkStartOffset : end-chunkStartOffset]
							if int64(messagesArrayBuf.Len()+len(slice)) > factBudget {
								messagesArrayIsLarge = true
								messagesArrayBuf.Reset()
							} else {
								messagesArrayBuf.Write(slice)
							}
						}
					}
					capturingMessagesArray = false
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

		// Item 2: Inspect message array items inside messages
		if !e.TopLevel && len(e.Path) >= 1 && e.Path[0] == "messages" {
			if len(e.Path) == 1 {
				switch e.Type {
				case jsonshape.EventObjectStart:
					arrayItemCount++
					if arrayItemCount > frontendlimits.MaxMessages {
						return frontendlimits.Count("messages", arrayItemCount, frontendlimits.MaxMessages)
					}
					currentItemRole = aipapi.RoleUser
					currentItemHasContent = false
					currentItemHasRole = false
				case jsonshape.EventObjectEnd:
					if !currentItemHasRole && largeArrayDeclineErr == nil {
						largeArrayDeclineErr = errors.New("openailegacy: message role is required")
					}
					if !currentItemHasContent && largeArrayDeclineErr == nil {
						largeArrayDeclineErr = errors.New("openailegacy: message content is required")
					}
					if int64(len(largeArrayRoles)+1)*int64(compactionfacts.ItemHashSizeBytes) > factBudget || int64(len(largeArrayRoles)+1) > factBudget {
						return fmt.Errorf("%w: message items count %d exceeds fact budget %d",
							largebody.ErrSemanticFactBudgetExceeded, len(largeArrayRoles)+1, factBudget)
					}
					largeArrayRoles = append(largeArrayRoles, currentItemRole)
					currentItemRole = aipapi.RoleUser
					itemRoleBuf.Reset()
					itemTypeBuf.Reset()
				default:
					if largeArrayDeclineErr == nil {
						largeArrayDeclineErr = errors.New("openailegacy: message array item must be an object")
					}
				}
			} else if len(e.Path) == 2 {
				switch e.Type {
				case jsonshape.EventKey:
					if e.Key != "role" && e.Key != "content" && e.Key != "type" && largeArrayDeclineErr == nil {
						largeArrayDeclineErr = fmt.Errorf("openailegacy: unsupported message item field %q requires canonical decode", e.Key)
					}
				case jsonshape.EventString:
					switch e.Key {
					case "role":
						currentItemHasRole = true
						rStr := strings.TrimSpace(itemRoleBuf.String())
						switch rStr {
						case "user":
							currentItemRole = aipapi.RoleUser
						case "assistant":
							currentItemRole = aipapi.RoleAssistant
						case "system":
							currentItemRole = aipapi.RoleSystem
						case "developer":
							if largeArrayDeclineErr == nil {
								largeArrayDeclineErr = errors.New("openailegacy: developer role requires canonical normalization")
							}
						case "tool":
							if largeArrayDeclineErr == nil {
								largeArrayDeclineErr = errors.New("openailegacy: tool role requires canonical decode")
							}
						default:
							if largeArrayDeclineErr == nil {
								largeArrayDeclineErr = fmt.Errorf("openailegacy: unsupported role %q requires canonical decode", rStr)
							}
						}
					case "type":
						tStr := strings.TrimSpace(itemTypeBuf.String())
						if tStr != "" && tStr != "message" && largeArrayDeclineErr == nil {
							largeArrayDeclineErr = fmt.Errorf("openailegacy: unsupported message item type %q requires canonical decode", tStr)
						}
					case "content":
						currentItemHasContent = true
					}
				default:
					if e.Key == "content" {
						if largeArrayDeclineErr == nil {
							largeArrayDeclineErr = errors.New("openailegacy: complex message content requires canonical decode")
						}
					} else if largeArrayDeclineErr == nil {
						largeArrayDeclineErr = fmt.Errorf("openailegacy: message item field %q must be a string", e.Key)
					}
				}
			} else {
				if largeArrayDeclineErr == nil {
					largeArrayDeclineErr = errors.New("openailegacy: complex nested message structure requires canonical decode")
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
			case "reasoning_effort":
				return &frontendpipe.BudgetBoundedWriter{Buf: &reasoningEffortBuf, Total: &totalFactBytes, MaxBudget: factBudget}, nil
			case "verbosity":
				return &frontendpipe.BudgetBoundedWriter{Buf: &verbosityBuf, Total: &totalFactBytes, MaxBudget: factBudget}, nil
			case "tool_choice":
				return &frontendpipe.BudgetBoundedWriter{Buf: &toolChoiceStrBuf, Total: &totalFactBytes, MaxBudget: factBudget}, nil
			}
		}
		if len(sctx.Path) >= 1 && sctx.Path[0] == "messages" {
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
		RejectDuplicateNames: true, // Item 6: set explicitly to prevent silent relaxation
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

		if capturingMessagesArray && !messagesArrayIsLarge {
			start := max(chunkStartOffset, messagesArrayStart)
			if start < chunkEndOffset {
				slice := chunk[start-chunkStartOffset:]
				if int64(messagesArrayBuf.Len()+len(slice)) > factBudget {
					messagesArrayIsLarge = true
					messagesArrayBuf.Reset()
				} else {
					messagesArrayBuf.Write(slice)
					messagesArrayStart = chunkEndOffset
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
		return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: json scanner feed: %w", err)
	}
	if bodyBytes <= 0 {
		bodyBytes = readRes.BytesRead
	}

	if unknownKey != "" {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: unsupported or unknown body key %q", unknownKey)
	}

	if !hasMessages {
		return frontendpipe.ProofOutput{}, errors.New("openailegacy: messages is required")
	}
	if !messagesIsArray {
		return frontendpipe.ProofOutput{}, errors.New("openailegacy: messages must be an array")
	}
	if arrayItemCount == 0 {
		return frontendpipe.ProofOutput{}, errors.New("openailegacy: messages is required")
	}

	if messagesArrayIsLarge {
		if arrayItemCount > frontendlimits.MaxMessages {
			return frontendpipe.ProofOutput{}, frontendlimits.Count("messages", arrayItemCount, frontendlimits.MaxMessages)
		}
		if largeArrayDeclineErr != nil {
			return frontendpipe.ProofOutput{}, largeArrayDeclineErr
		}
	}

	// 3. Verify top-level "model" span
	modelSpanRaw, hasModel := tracker.Span("model")
	if !hasModel || modelSpanRaw.Length == 0 {
		return frontendpipe.ProofOutput{}, errors.New("openailegacy: model is required")
	}
	modelSpan := largebody.Span{Offset: modelSpanRaw.Offset, Length: modelSpanRaw.Length}
	rewrite, err := largebody.NewModelTokenRewrite(modelSpan)
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: model rewrite: %w", err)
	}

	model := strings.TrimSpace(modelBuf.String())
	if model == "" {
		return frontendpipe.ProofOutput{}, errors.New("openailegacy: model is required")
	}

	// 4. Parse metadata
	var metadata map[string]string
	if raw := fieldBufs["metadata"].Bytes(); len(raw) > 0 {
		if err := json.Unmarshal(raw, &metadata); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: metadata: %w", err)
		}
	}
	if len(metadata) > 0 {
		if err := frontendlimits.Count("metadata", len(metadata), frontendlimits.MaxMetadata); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: %w", err)
		}
		if err := sessionwire.ValidateMetadata(metadata); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: %w", err)
		}
		// Requirement 14.2, 17.5: Reject body-carried AIProxer session metadata
		if sessionwire.HasSessionMetadata(metadata) {
			return frontendpipe.ProofOutput{}, largebody.ErrBodySessionMetadataRejected
		}
	}

	// 5. Resolve route selector precedence (Requirement 4.8, 17.6)
	route := routing.ExplicitRoute(in.RouteSelector)
	if route.Selector == "" && in.RouteFromBodyModel {
		route = in.RoutePrefixes.ResolveInlineOrDefault(model, in.DefaultRouteSelector)
	}
	sel := route.Selector
	if sel == "" {
		return frontendpipe.ProofOutput{}, errors.New("openailegacy: route selector is required: send a model with a route prefix (for example \"backend:model\")")
	}

	// 6. Parse tools, tool_choice, verbosity
	tools := []aipapi.ToolDef{}
	if fieldBufs["tools"].Len() > 0 {
		t, err := parseTools(fieldBufs["tools"].Bytes())
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: tools: %w", err)
		}
		tools = t
	}

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
			return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: unsupported tool_choice string %q", s)
		}
	} else if fieldBufs["tool_choice"].Len() > 0 {
		tc, err := parseToolChoice(fieldBufs["tool_choice"].Bytes())
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: tool_choice: %w", err)
		}
		toolChoice = tc
	} else {
		toolChoice = aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto}
	}

	verbosity, err := aipapi.ParseVerbosityLevel(strings.TrimSpace(verbosityBuf.String()))
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: verbosity: %w", err)
	}

	// 7. Build extensions
	modelRaw, err := json.Marshal(model)
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: marshal model: %w", err)
	}
	ext := map[string]json.RawMessage{extModelJSONKey: modelRaw}
	if b, err := json.Marshal(openrouterwire.FlavorChat); err == nil {
		ext[openrouterwire.ExtUpstreamFlavor] = b
	}
	if fieldBufs["stream_options"].Len() > 0 {
		raw := fieldBufs["stream_options"].Bytes()
		if jsonpresence.IsPresentNonNullJSON(raw) {
			ext[extStreamOptsJSONKey] = raw
		}
	}
	if in.Headers != nil {
		openrouterwire.CaptureHeaders(in.Headers, ext)
	}

	// Explicit workspace hint projects to the canonical extension on the
	// bounded path as well; invalid values decline to canonical decode,
	// which returns the existing protocol 400 (Requirements 1.1, 1.3).
	workspaceCall := &aipapi.Call{Extensions: ext}
	if err := sessionwire.ApplyWorkspaceHint(workspaceCall, in.Headers, aipsdk.HTTPHeaders{}); err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: %w", err)
	}

	// 8. Extract session input
	// Semantic-fact budget enforcement point 2: sessionwire enforces MaxFactBytes
	sessIn, err := sessionwire.BuildSessionInput(in.Headers, metadata, sessionwire.SessionInputOptions{
		RejectBodyMetadata: true,
		MaxFactBytes:       factBudget,
	})
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: session input: %w", err)
	}

	// Requirement 16.2, 16.7: X-AIP-Session-Hint feeds ClientSessionID into wire digest
	// while canonical pre-core decode never sets it. To prevent identity divergence,
	// requests carrying a client session hint must decline to canonical processing.
	if sessIn.ClientSessionID != "" {
		return frontendpipe.ProofOutput{}, errors.New("openailegacy: session hint requires canonical decode")
	}

	// 9. Build identity configuration
	idCfg := largebody.CallIdentityConfig{
		Session: aipapi.SessionRef{
			AuthoritativeSessionID: sessIn.AuthoritativeSessionID,
			ClientSessionID:        sessIn.ClientSessionID,
			ResumeToken:            sessIn.ResumeToken.Reveal(),
		},
		SessionInput:  sessIn,
		RouteSelector: sel,
		Tools:         tools,
		ToolChoice:    toolChoice,
		Options: aipapi.GenerationOptions{
			Temperature:       temperature,
			TopP:              topP,
			MaxOutputTokens:   maxTokens,
			ParallelToolCalls: parallelTools,
			ReasoningEffort:   strings.TrimSpace(reasoningEffortBuf.String()),
			Verbosity:         verbosity,
		},
		Extensions: ext,
	}

	var digest largebody.IdentityDigest
	var turnShape largebody.ClientTurnShape
	var proofCompactionFacts compactionfacts.RequestFacts
	var proofCompactionComplete bool
	var msgs []aipapi.Message

	if !messagesArrayIsLarge {
		var rawMsgs []json.RawMessage
		if err := json.Unmarshal(messagesArrayBuf.Bytes(), &rawMsgs); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: messages array: %w", err)
		}
		if len(rawMsgs) == 0 {
			return frontendpipe.ProofOutput{}, errors.New("openailegacy: messages is required")
		}
		if err := frontendlimits.Count("messages", len(rawMsgs), frontendlimits.MaxMessages); err != nil {
			return frontendpipe.ProofOutput{}, err
		}

		msgs = make([]aipapi.Message, 0, len(rawMsgs))
		for i, rawMsg := range rawMsgs {
			m, err := parseProfileMessage(rawMsg)
			if err != nil {
				return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: messages[%d]: %w", i, err)
			}
			msgs = append(msgs, m)
		}

		// Semantic-fact budget enforcement point 3: ClientTurnShapeFromMessages bounds turn facts
		ts, err := largebody.ClientTurnShapeFromMessages(msgs, factBudget)
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: turn shape: %w", err)
		}
		turnShape = ts

		idWriter, err := largebody.NewCallIdentityWriter(idCfg)
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: identity writer: %w", err)
		}
		if msgs != nil {
			if err := idWriter.StartMessages(); err != nil {
				return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: start messages: %w", err)
			}
			for _, msg := range msgs {
				if err := idWriter.AddMessage(msg); err != nil {
					return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: add message: %w", err)
				}
			}
		}
		d, err := idWriter.Digest()
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: digest: %w", err)
		}
		digest = d
	} else {
		// Large chunked array messages (e.g. 20 MiB chunked messages): stream in Pass 2
		rc2, err := in.Source.Open()
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: open replay source (pass 2): %w", err)
		}
		defer func() { _ = rc2.Close() }()

		idWriter, err := largebody.NewCallIdentityWriter(idCfg)
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: identity writer: %w", err)
		}
		if err := idWriter.StartMessages(); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: start messages: %w", err)
		}

		var msgIndex int
		var currMsgWriter *largebody.MessageIdentityWriter
		var currTrimmer *largebody.TrimSpaceWriter
		var turnItems []largebody.ClientTurnItemShape
		var totalPartBytes int64
		var ordinal int64

		arrayStrResolver := func(sctx jsonshape.StringContext) (io.Writer, error) {
			if len(sctx.Path) >= 2 && sctx.Path[0] == "messages" && sctx.Key == "content" {
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
			if len(e.Path) >= 2 && e.Path[0] == "messages" && e.Type == jsonshape.EventString && e.Key == "content" {
				if currTrimmer != nil {
					_ = currTrimmer.Close()
				}
				role := aipapi.RoleUser
				if msgIndex < len(largeArrayRoles) {
					role = largeArrayRoles[msgIndex]
				}
				var partBytes int64
				if currTrimmer != nil {
					partBytes = currTrimmer.TrimmedBytes()
				}
				if partBytes == 0 {
					if role == aipapi.RoleAssistant {
						return errors.New("openailegacy: empty assistant message requires canonical drop")
					}
					return errors.New("openailegacy: message content string is empty")
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
				RejectDuplicateNames: true, // Item 6: set explicitly
				MaxBytes:             bodyBytes,
				MaxDepth:             128,
			},
			jsonshape.WithEventHandler(arrayHandler),
			jsonshape.WithStringWriterResolver(arrayStrResolver),
		)

		_, err = largebody.ProcessReplayChunks(largebody.ReplayChunkReaderConfig{
			Reader:    rc2,
			MaxBytes:  bodyBytes,
			ChunkSize: 32 * 1024,
			Scanner:   arrayScanner,
		})
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: streaming proof (pass 2): %w", err)
		}

		if currMsgWriter != nil {
			_ = currMsgWriter.EndMessage()
		}

		d, err := idWriter.Digest()
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: digest: %w", err)
		}
		digest = d
		turnShape = largebody.ClientTurnShape{
			Items:             turnItems,
			TotalContentBytes: totalPartBytes,
		}
	}

	// Compile streaming compaction facts regardless of payload size using the shared streaming builder.
	if int64(len(turnShape.Items)*compactionfacts.ItemHashSizeBytes) <= factBudget && largeArrayDeclineErr == nil {
		if facts, complete, err := compileStreamingCompactionFacts(scanCtx, in.Source, bodyBytes, tools, turnShape.Items, factBudget); err == nil && complete {
			proofCompactionFacts = facts
			proofCompactionComplete = true
		}
	}

	// 10. Compute source digest
	var sumArr [32]byte
	copy(sumArr[:], hasher.Sum(nil))
	sourceDigest := largebody.NewSourceDigest(sumArr)

	var maxOutTokens int64
	if maxTokens != nil {
		maxOutTokens = int64(*maxTokens)
	}

	cancellationID := ""
	if sessIn.ALegID != "" {
		cancellationID = frontendpipe.FormatOpenAICancellationCarrier(sessIn.ALegID, sessIn.AuthoritativeSessionID)
	}
	if cancellationID == "" {
		cancellationID = "chatcmpl_" + digest.Token()
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

	hasTools := len(tools) > 0 || hasHistoryTools(msgs)
	requiredCaps := largebody.DeriveRequiredCapabilities(turnShape, largebody.ControlRequirements{
		Delivery:          aipapi.DeliveryModeFromClientStream(stream),
		HasTools:          hasTools,
		ParallelToolCalls: parallelTools,
		ReasoningEffort:   reasoningEffortBuf.String(),
		StructuredOutputs: false,
		ItemAuthoritative: false,
	})

	proof := largebody.Proof{
		ProfileID:       ProfileID,
		Operation:       aipapi.OperationOpenAIChatCompletions,
		Delivery:        aipapi.DeliveryModeFromClientStream(stream),
		RouteSelector:   sel,
		RouteOrigin:     route.Origin,
		ClientModel:     model,
		MaxOutputTokens: maxOutTokens,
		Facts: largebody.ProtocolFacts{
			RequirementsID: ProfileID,
			ControlCount:   int64(len(tools)),
		},
		Mode:                 largebody.BodyModeIdentityJSON,
		Rewrite:              rewrite,
		ModelSpan:            modelSpan,
		Identity:             digest,
		Turn:                 turnShape,
		Session:              sessIn,
		Source:               sourceDigest,
		BodyBytes:            bodyBytes,
		CompactionFacts:      proofCompactionFacts,
		CompactionComplete:   proofCompactionComplete,
		RequiredCapabilities: requiredCaps,
		// Bounded classification evidence for this turn, compiled from the tool
		// definitions and the request headers already observed above. Only the
		// accepted identity and fixed category bits survive; the ToolDef slice and
		// the header map stay local to this compiler (requirements 5.2, 5.5, 12.7).
		ClassificationEvidence: frontendpipe.CompileClassificationEvidence(
			aipapi.OperationOpenAIChatCompletions, in.Headers, tools),
	}

	proofOut := frontendpipe.ProofOutput{
		State: frontendpipe.FrontendWireState{
			ProfileID: ProfileID,
			Proof:     proof,
			Seeds:     seeds,
		},
	}

	if err := proofOut.Validate(factBudget); err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openailegacy: validate proof output: %w", err)
	}

	return proofOut, nil
}

func parseProfileMessage(raw json.RawMessage) (aipapi.Message, error) {
	var probe struct {
		Role             string          `json:"role"`
		Content          json.RawMessage `json:"content"`
		ToolCallID       string          `json:"tool_call_id"`
		ToolCalls        json.RawMessage `json:"tool_calls"`
		FunctionCall     json.RawMessage `json:"function_call"`
		ReasoningContent *string         `json:"reasoning_content"`
		Reasoning        *string         `json:"reasoning"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return aipapi.Message{}, fmt.Errorf("message json: %w", err)
	}

	roleStr := strings.TrimSpace(strings.ToLower(probe.Role))
	// Requirement 17.3: Developer role is normalized to system by canonical encoder; keep canonical-only.
	if roleStr == "developer" {
		return aipapi.Message{}, errors.New("developer role requires canonical normalization")
	}
	role, err := mapRole(probe.Role)
	if err != nil {
		return aipapi.Message{}, fmt.Errorf("role: %w", err)
	}

	switch role {
	case aipapi.RoleTool:
		if strings.TrimSpace(probe.ToolCallID) == "" {
			return aipapi.Message{}, fmt.Errorf("malformed tool message requires canonical repair: %w", errSkipMalformedHistory)
		}
		trimmed := bytes.TrimSpace(probe.Content)
		if len(trimmed) == 0 || trimmed[0] != '"' {
			// Non-string tool content is re-encoded as string by canonical backend; keep canonical-only.
			return aipapi.Message{}, errors.New("non-string tool content requires canonical encoding")
		}
		content, err := parseToolMessageContent(probe.Content)
		if err != nil {
			return aipapi.Message{}, fmt.Errorf("tool message content: %w", err)
		}
		rawJSON, err := json.Marshal(content)
		if err != nil {
			return aipapi.Message{}, fmt.Errorf("tool message marshal: %w", err)
		}
		return aipapi.Message{
			Role: aipapi.RoleTool,
			Parts: []aipapi.Part{{
				Kind:       aipapi.PartToolResult,
				ToolCallID: strings.TrimSpace(probe.ToolCallID),
				Content:    rawJSON,
			}},
		}, nil

	case aipapi.RoleAssistant:
		if len(probe.FunctionCall) > 0 {
			return aipapi.Message{}, errors.New("legacy function_call requires canonical normalization")
		}
		if probe.Reasoning != nil {
			return aipapi.Message{}, errors.New("reasoning alias requires canonical normalization")
		}
		if jsonpresence.IsPresentNonNullJSON(probe.ToolCalls) {
			if err := frontendlimits.Bytes("tool_calls", len(probe.ToolCalls), frontendlimits.MaxRawJSONPayload); err != nil {
				return aipapi.Message{}, err
			}
			var rawCalls []json.RawMessage
			if err := json.Unmarshal(probe.ToolCalls, &rawCalls); err != nil {
				return aipapi.Message{}, fmt.Errorf("tool_calls: %w", err)
			}
			for tcIdx, rc := range rawCalls {
				if !json.Valid(rc) {
					return aipapi.Message{}, errors.New("invalid tool_calls entry")
				}
				var wire struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string          `json:"name"`
						Arguments json.RawMessage `json:"arguments"`
					} `json:"function"`
				}
				if err := json.Unmarshal(rc, &wire); err != nil {
					return aipapi.Message{}, fmt.Errorf("tool_calls[%d]: %w", tcIdx, err)
				}
				if strings.TrimSpace(wire.ID) == "" || strings.TrimSpace(wire.Function.Name) == "" {
					return aipapi.Message{}, fmt.Errorf("tool_calls[%d]: malformed tool call requires canonical repair: %w", tcIdx, errSkipMalformedHistory)
				}
				if jsonpresence.IsPresentNonNullJSON(wire.Function.Arguments) {
					trimmedArgs := bytes.TrimSpace(wire.Function.Arguments)
					if len(trimmedArgs) == 0 || trimmedArgs[0] != '"' {
						return aipapi.Message{}, fmt.Errorf("tool_calls[%d]: non-string tool_calls arguments requires canonical encoding", tcIdx)
					}
				}
			}
		}
		parts, err := parseAssistantParts(probe.Content, probe.ToolCalls, probe.FunctionCall, probe.ReasoningContent, probe.Reasoning)
		if err != nil {
			if errors.Is(err, errEmptyAssistantMessage) {
				return aipapi.Message{}, fmt.Errorf("empty assistant message requires canonical drop: %w", err)
			}
			if errors.Is(err, errSkipMalformedHistory) {
				return aipapi.Message{}, fmt.Errorf("malformed history requires canonical repair: %w", err)
			}
			return aipapi.Message{}, fmt.Errorf("assistant message: %w", err)
		}
		return aipapi.Message{Role: aipapi.RoleAssistant, Parts: parts}, nil

	default:
		parts, err := parseChatContent(probe.Content)
		if err != nil {
			return aipapi.Message{}, fmt.Errorf("message content: %w", err)
		}
		return aipapi.Message{Role: role, Parts: parts}, nil
	}
}

var _ frontendpipe.FrontendProfile = (*Profile)(nil)

func compileStreamingCompactionFacts(
	ctx context.Context,
	src largebody.Source,
	bodyBytes int64,
	tools []aipapi.ToolDef,
	items []largebody.ClientTurnItemShape,
	maxFactBytes int64,
) (compactionfacts.RequestFacts, bool, error) {
	if int64(len(items)*compactionfacts.ItemHashSizeBytes) > maxFactBytes {
		return compactionfacts.RequestFacts{}, false, nil
	}
	for _, it := range items {
		if len(it.Parts) != 1 || it.Parts[0].Kind != aipapi.ContentPartText {
			return compactionfacts.RequestFacts{}, false, nil
		}
	}
	rc, err := src.Open()
	if err != nil {
		return compactionfacts.RequestFacts{}, false, err
	}
	defer func() { _ = rc.Close() }()

	factBuilder := compactionfacts.NewBuilderWithByteBudget(aipapi.OperationOpenAIChatCompletions, int(maxFactBytes))
	factBuilder.AddToolCount(len(tools))

	var msgIdx int
	var trimmer *largebody.TrimSpaceWriter
	var hasher *compactionfacts.ItemHasher
	var factErr error

	resolver := func(sctx jsonshape.StringContext) (io.Writer, error) {
		if len(sctx.Path) >= 2 && sctx.Path[0] == "messages" && sctx.Key == "content" {
			if msgIdx >= len(items) {
				return nil, errors.New("openailegacy: unexpected extra message in compaction pass")
			}
			itemShape := items[msgIdx]
			partBytes := 0
			if len(itemShape.Parts) > 0 {
				partBytes = int(itemShape.Parts[0].ContentBytes)
			}
			role := itemShape.Role
			hasher = compactionfacts.NewItemHasher(aipapi.ItemKindMessage, role)
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
		if len(e.Path) >= 2 && e.Path[0] == "messages" && e.Type == jsonshape.EventString && e.Key == "content" {
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
	if readErr != nil || factErr != nil || msgIdx != len(items) {
		return compactionfacts.RequestFacts{}, false, readErr
	}
	facts, berr := factBuilder.Build()
	if berr != nil {
		return compactionfacts.RequestFacts{}, false, berr
	}
	return facts, true, nil
}

func hasHistoryTools(msgs []aipapi.Message) bool {
	for _, m := range msgs {
		if m.Role == aipapi.RoleTool {
			return true
		}
		for _, p := range m.Parts {
			if p.Kind == aipapi.PartToolResult || (p.Kind == aipapi.PartJSON && p.ToolCallID != "") {
				return true
			}
		}
	}
	return false
}
