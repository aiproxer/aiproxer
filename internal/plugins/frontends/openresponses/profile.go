package openresponses

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
	proto "github.com/aiproxer/aiproxer/internal/plugins/protocols/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

type chunkWriterFunc func([]byte) (int, error)

func (f chunkWriterFunc) Write(p []byte) (int, error) {
	return f(p)
}

// ProfileID is the static identifier for the certified OpenResponses fast-path profile (Requirements 4, 17).
const ProfileID = "openresponses_v1"

// Profile implements frontendpipe.FrontendProfile for OpenResponses create (POST /openresponses/v1/responses).
type Profile struct{}

// NewProfile returns an initialized OpenResponses frontend profile.
func NewProfile() *Profile {
	return &Profile{}
}

// ProfileID returns the static identifier for this certified profile.
func (p *Profile) ProfileID() string {
	return ProfileID
}

// wireExtraState carries frontend-owned extra state for wire response processing.
type wireExtraState struct {
	ToolChoice aipapi.ToolChoice
	Options    aipapi.GenerationOptions
}

type stringInspector struct {
	totalBytes       int64
	hasNonWhitespace bool
}

func newStringInspector() *stringInspector {
	return &stringInspector{}
}

func (si *stringInspector) Write(p []byte) (int, error) {
	si.totalBytes += int64(len(p))
	if !si.hasNonWhitespace {
		for _, b := range p {
			if b != ' ' && b != '\t' && b != '\n' && b != '\r' {
				si.hasNonWhitespace = true
				break
			}
		}
	}
	return len(p), nil
}

var openResponsesKnownBodyKeys = map[string]bool{
	"model":                  true,
	"input":                  true,
	"instructions":           true,
	"tools":                  true,
	"tool_choice":            true,
	"parallel_tool_calls":    true,
	"temperature":            true,
	"top_p":                  true,
	"max_output_tokens":      true,
	"max_tool_calls":         true,
	"truncation":             true,
	"text":                   true,
	"reasoning":              true,
	"store":                  true,
	"background":             true,
	"previous_response_id":   true,
	"metadata":               true,
	"service_tier":           true,
	"safety_identifier":      true,
	"prompt_cache_key":       true,
	"prompt_cache_retention": true,
	"include":                true,
	"presence_penalty":       true,
	"frequency_penalty":      true,
	"top_logprobs":           true,
	"stream_options":         true,
	"stream":                 true,
}

// CompileProof compiles protocol proof and response seeds from the
// captured request replay under decode admission (Requirements 4, 14, 16, 17).
// Uses the streaming proof pass without allocating or retaining the full request body.
func (p *Profile) CompileProof(ctx context.Context, in frontendpipe.ProofInput) (frontendpipe.ProofOutput, error) {
	// 1. Path verification: only create path is supported; compact path is declined.
	if !isCreatePath(in.URLPath) {
		return frontendpipe.ProofOutput{}, errors.New("openresponses: unsupported url path")
	}
	if isCompactPath(in.URLPath) {
		return frontendpipe.ProofOutput{}, errors.New("openresponses: compact path not supported for fast path")
	}

	if in.Source == nil {
		return frontendpipe.ProofOutput{}, errors.New("openresponses: nil replay source")
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
		return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: open replay source: %w", err)
	}
	defer func() { _ = rc.Close() }()

	// 2. Pass 1: Streaming scan over replay bytes using jsonshape.Scanner + SHA-256 in fixed buffers (no ReadAll).
	tracker := jsonshape.NewTopLevelSpanTracker("model", "input")
	var unknownKey string

	var modelBuf bytes.Buffer
	var instructionsBuf bytes.Buffer
	inputInspector := newStringInspector()
	var toolChoiceStrBuf bytes.Buffer

	var stream bool
	var parallelTools *bool
	var temperature *float64
	var topP *float64
	var maxOut *int

	// store gate tracking
	var hasStore bool
	var storeIsFalse bool
	var storeVal string
	var storeBuf bytes.Buffer

	// For large array input role tracking and decline
	var arrayItemCount int
	var largeArrayRoles []aipapi.Role
	var largeArrayStatuses []aipapi.ItemStatus
	var largeArrayDeclineErr error
	var largeArrayHasID bool
	currentItemRole := aipapi.RoleUser
	var currentItemStatus aipapi.ItemStatus
	var currentItemHasContent bool
	var currentItemType string
	var itemRoleBuf bytes.Buffer
	var itemTypeBuf bytes.Buffer
	var itemStatusBuf bytes.Buffer

	// Composite field buffers (tools, tool_choice, text, reasoning, metadata)
	fieldBufs := make(map[string]*bytes.Buffer)
	for _, k := range []string{"tools", "tool_choice", "text", "reasoning", "metadata"} {
		fieldBufs[k] = &bytes.Buffer{}
	}
	var inputArrayBuf bytes.Buffer
	inputArrayIsLarge := bodyBytes > 2*factBudget
	var totalFactBytes int64

	var hasInput bool
	var inputIsString bool
	var inputIsArray bool
	var toolChoiceIsString bool

	// Chunk tracking for composite extraction and number tokens across chunk boundaries
	var currentChunk []byte
	var prevChunkTail []byte
	var chunkStartOffset int64
	var totalBytesRead int64

	extractNumberStr := func(e jsonshape.Event) (string, error) {
		return frontendpipe.ExtractNumberToken(e, currentChunk, prevChunkTail, chunkStartOffset, "openresponses")
	}

	var capturingKey string
	var capturingBuf *bytes.Buffer
	var capturingStart int64

	var capturingInputArray bool
	var inputArrayStart int64

	handler := jsonshape.EventHandlerFunc(func(e jsonshape.Event) error {
		if e.TopLevel && e.Type == jsonshape.EventKey {
			if !openResponsesKnownBodyKeys[e.Key] {
				unknownKey = e.Key
			}
			if e.Key == "input" {
				hasInput = true
			}
		}

		if e.TopLevel && e.Type != jsonshape.EventKey && e.Type != jsonshape.EventArrayEnd && e.Type != jsonshape.EventObjectEnd {
			switch e.Key {
			case "store":
				hasStore = true
				switch e.Type {
				case jsonshape.EventFalse:
					storeIsFalse = true
					storeVal = "false"
				case jsonshape.EventTrue:
					storeVal = "true"
				case jsonshape.EventNull:
					storeVal = "null"
				case jsonshape.EventString:
					storeVal = fmt.Sprintf("%q", storeBuf.String())
				case jsonshape.EventNumber:
					storeVal = "number"
				default:
					storeVal = "invalid"
				}
			case "stream":
				if e.Type == jsonshape.EventTrue {
					stream = true
				} else if e.Type == jsonshape.EventFalse {
					stream = false
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: stream must be a boolean")
				}
			case "parallel_tool_calls":
				if e.Type == jsonshape.EventTrue {
					v := true
					parallelTools = &v
				} else if e.Type == jsonshape.EventFalse {
					v := false
					parallelTools = &v
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: parallel_tool_calls must be a boolean")
				}
			case "temperature":
				if e.Type == jsonshape.EventNumber {
					numStr, err := extractNumberStr(e)
					if err != nil {
						return err
					}
					v, err := strconv.ParseFloat(numStr, 64)
					if err != nil {
						return fmt.Errorf("openresponses: temperature: %w", err)
					}
					temperature = &v
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: temperature must be a number")
				}
			case "top_p":
				if e.Type == jsonshape.EventNumber {
					numStr, err := extractNumberStr(e)
					if err != nil {
						return err
					}
					v, err := strconv.ParseFloat(numStr, 64)
					if err != nil {
						return fmt.Errorf("openresponses: top_p: %w", err)
					}
					topP = &v
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: top_p must be a number")
				}
			case "max_output_tokens":
				if e.Type == jsonshape.EventNumber {
					numStr, err := extractNumberStr(e)
					if err != nil {
						return err
					}
					if strings.Contains(numStr, ".") || strings.ContainsAny(numStr, "eE") {
						return errors.New("openresponses: max_output_tokens must be an integer")
					}
					v, err := strconv.Atoi(numStr)
					if err != nil {
						return errors.New("openresponses: max_output_tokens must be an integer")
					}
					maxOut = &v
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: max_output_tokens must be an integer")
				}
			case "instructions":
				if e.Type != jsonshape.EventString && e.Type != jsonshape.EventNull {
					return errors.New("openresponses: instructions must be a string")
				}
			case "tools":
				if e.Type == jsonshape.EventArrayStart {
					capturingKey = e.Key
					capturingBuf = fieldBufs[e.Key]
					capturingStart = e.Offset
					capturingBuf.Reset()
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: tools must be an array")
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
					return errors.New("openresponses: tool_choice must be a string or object")
				}
			case "text":
				if e.Type == jsonshape.EventObjectStart {
					capturingKey = e.Key
					capturingBuf = fieldBufs[e.Key]
					capturingStart = e.Offset
					capturingBuf.Reset()
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: text must be an object")
				}
			case "reasoning":
				if e.Type == jsonshape.EventObjectStart {
					capturingKey = e.Key
					capturingBuf = fieldBufs[e.Key]
					capturingStart = e.Offset
					capturingBuf.Reset()
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: reasoning must be an object")
				}
			case "metadata":
				if e.Type == jsonshape.EventObjectStart {
					capturingKey = e.Key
					capturingBuf = fieldBufs[e.Key]
					capturingStart = e.Offset
					capturingBuf.Reset()
				} else if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: metadata must be an object")
				}
			case "model":
				if e.Type != jsonshape.EventString {
					return errors.New("openresponses: model must be a string")
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
					return errors.New("openresponses: input must be a string or array")
				}
			// Unsupported controls fail-closed when non-null
			case "previous_response_id":
				if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: previous_response_id requires continuation state")
				}
			case "truncation":
				if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: truncation control is not supported")
				}
			case "background":
				if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: background control is not supported")
				}
			case "include":
				if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: include control is not supported")
				}
			case "presence_penalty":
				if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: presence_penalty control is not supported")
				}
			case "frequency_penalty":
				if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: frequency_penalty control is not supported")
				}
			case "stream_options":
				if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: stream_options control is not supported")
				}
			case "top_logprobs":
				if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: top_logprobs control is not supported")
				}
			case "service_tier":
				if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: service_tier control is not supported")
				}
			case "safety_identifier":
				if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: safety_identifier control is not supported")
				}
			case "prompt_cache_key":
				if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: prompt_cache_key control is not supported")
				}
			case "prompt_cache_retention":
				if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: prompt_cache_retention control is not supported")
				}
			case "max_tool_calls":
				if e.Type != jsonshape.EventNull {
					return errors.New("openresponses: max_tool_calls control is not supported")
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
						// Retain through the shared before-write budget leaf so oversize
						// metadata never lands in the buffer.
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
						// Retain through the shared before-write budget leaf so oversize
						// metadata never lands in the buffer.
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

		// Array items inside input
		if !e.TopLevel && len(e.Path) >= 1 && e.Path[0] == "input" {
			if len(e.Path) == 1 {
				switch e.Type {
				case jsonshape.EventObjectStart:
					arrayItemCount++
					if arrayItemCount > frontendlimits.MaxMessages {
						return frontendlimits.Count("input", arrayItemCount, frontendlimits.MaxMessages)
					}
					currentItemRole = aipapi.RoleUser
					currentItemStatus = ""
					currentItemHasContent = false
					currentItemType = ""
				case jsonshape.EventObjectEnd:
					if inputArrayIsLarge {
						if currentItemType != "" && currentItemType != "message" && largeArrayDeclineErr == nil {
							largeArrayDeclineErr = fmt.Errorf("openresponses: input item type %q requires canonical decode", currentItemType)
						}
						if !currentItemHasContent && largeArrayDeclineErr == nil {
							largeArrayDeclineErr = errors.New("openresponses: input item missing content requires canonical decode")
						}
					}
					largeArrayRoles = append(largeArrayRoles, currentItemRole)
					largeArrayStatuses = append(largeArrayStatuses, currentItemStatus)
					currentItemRole = aipapi.RoleUser
					currentItemStatus = ""
					itemRoleBuf.Reset()
					itemTypeBuf.Reset()
					itemStatusBuf.Reset()
				case jsonshape.EventArrayEnd:
					// Array end needs no item action.
				default:
					return errors.New("openresponses: input array item must be an object")
				}
			} else if len(e.Path) == 2 {
				switch e.Type {
				case jsonshape.EventKey:
					if e.Key == "id" {
						largeArrayHasID = true
					} else if e.Key != "role" && e.Key != "content" && e.Key != "type" && e.Key != "status" && largeArrayDeclineErr == nil {
						largeArrayDeclineErr = fmt.Errorf("openresponses: unsupported input item field %q requires canonical decode", e.Key)
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
								largeArrayDeclineErr = fmt.Errorf("openresponses: unsupported role %q requires canonical decode", rStr)
							}
						}
					case "type":
						tStr := strings.TrimSpace(itemTypeBuf.String())
						currentItemType = tStr
						if tStr != "" && tStr != "message" && largeArrayDeclineErr == nil {
							largeArrayDeclineErr = fmt.Errorf("openresponses: unsupported input item type %q requires canonical decode", tStr)
						}
					case "status":
						sStr := strings.TrimSpace(itemStatusBuf.String())
						if sStr == "received" {
							sStr = "completed"
						}
						currentItemStatus = aipapi.ItemStatus(sStr)
					case "content":
						currentItemHasContent = true
					}
				}
			} else {
				if largeArrayDeclineErr == nil {
					largeArrayDeclineErr = errors.New("openresponses: complex nested input structure requires canonical decode")
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
			case "store":
				storeBuf.Reset()
				return &frontendpipe.BudgetBoundedWriter{Buf: &storeBuf, Total: &totalFactBytes, MaxBudget: factBudget}, nil
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
			if sctx.Key == "status" {
				itemStatusBuf.Reset()
				return &frontendpipe.BudgetBoundedWriter{Buf: &itemStatusBuf, Total: &totalFactBytes, MaxBudget: factBudget}, nil
			}
		}
		return nil, nil
	}

	hasher := sha256.New()
	scannerLimits := jsonshape.Limits{
		RejectDuplicateNames: true,
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
				// Retain through the shared before-write budget leaf so oversize
				// metadata never lands in the buffer.
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
		return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: process replay chunks: %w", err)
	}
	if bodyBytes <= 0 {
		bodyBytes = readRes.BytesRead
	}

	if unknownKey != "" {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: unknown body key %q", unknownKey)
	}

	// 3. Store gate validation (Requirement 17.4)
	if !hasStore || storeVal == "null" {
		return frontendpipe.ProofOutput{}, errors.New("openresponses: store is required and must be explicitly false")
	}
	if !storeIsFalse {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: store must be false, got %s", storeVal)
	}

	// 4. Check large array decline or limits
	if inputIsArray && inputArrayIsLarge {
		if arrayItemCount > frontendlimits.MaxMessages {
			return frontendpipe.ProofOutput{}, frontendlimits.Count("input", arrayItemCount, frontendlimits.MaxMessages)
		}
		if largeArrayHasID && largeArrayDeclineErr == nil {
			largeArrayDeclineErr = errors.New("openresponses: input item id in large array requires canonical decode")
		}
		if largeArrayDeclineErr != nil {
			return frontendpipe.ProofOutput{}, largeArrayDeclineErr
		}
	}

	// 5. Verify top-level "model" span
	modelSpanRaw, hasModel := tracker.Span("model")
	if !hasModel || modelSpanRaw.Length == 0 {
		return frontendpipe.ProofOutput{}, errors.New("openresponses: model is required")
	}
	modelSpan := largebody.Span{Offset: modelSpanRaw.Offset, Length: modelSpanRaw.Length}
	rewrite, err := largebody.NewModelTokenRewrite(modelSpan)
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: model rewrite: %w", err)
	}

	model := strings.TrimSpace(modelBuf.String())
	if model == "" {
		return frontendpipe.ProofOutput{}, errors.New("openresponses: model is required")
	}

	// 6. Verify input presence and shape
	if !hasInput {
		return frontendpipe.ProofOutput{}, errors.New("openresponses: input is required and cannot be empty")
	}
	if !inputIsString && !inputIsArray {
		return frontendpipe.ProofOutput{}, errors.New("openresponses: input must be a string or array")
	}
	if inputIsString && inputInspector.totalBytes == 0 {
		return frontendpipe.ProofOutput{}, errors.New("openresponses: input is required and cannot be empty")
	}
	if inputIsArray && arrayItemCount == 0 {
		return frontendpipe.ProofOutput{}, errors.New("openresponses: input is required and cannot be empty")
	}

	// 7. Parse metadata
	var metadata map[string]string
	if raw := fieldBufs["metadata"].Bytes(); len(raw) > 0 {
		m, err := decodeMetadata(raw)
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: metadata: %w", err)
		}
		metadata = m
	}
	if len(metadata) > 0 {
		if err := frontendlimits.Count("metadata", len(metadata), frontendlimits.MaxMetadata); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: %w", err)
		}
		if sessionwire.HasSessionMetadata(metadata) {
			return frontendpipe.ProofOutput{}, largebody.ErrBodySessionMetadataRejected
		}
	}

	// 8. Route selector precedence (Requirements 4.8, 17.6)
	route := routing.ExplicitRoute(in.RouteSelector)
	if route.Selector == "" && in.RouteFromBodyModel {
		if len(in.RoutePrefixes) == 0 {
			route = routing.ExplicitRoute(model)
		} else {
			route = in.RoutePrefixes.ResolveInlineOrDefault(model, in.DefaultRouteSelector)
		}
	}
	if route.Selector == "" {
		route = routing.DefaultRoute(in.DefaultRouteSelector)
	}
	if route.Selector == "" {
		route = routing.ExplicitRoute(model)
	}
	sel := route.Selector

	// 9. Extract session input
	sessIn, err := sessionwire.BuildSessionInput(in.Headers, metadata, sessionwire.SessionInputOptions{
		RejectBodyMetadata: true,
		MaxFactBytes:       factBudget,
	})
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: session input: %w", err)
	}
	if sessIn.ClientSessionID != "" {
		return frontendpipe.ProofOutput{}, errors.New("openresponses: session hint requires canonical decode")
	}

	// 10. Parse tools and options
	canonicalTools, err := parseTools(fieldBufs["tools"].Bytes())
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: %w", err)
	}

	toolChoice, err := parseToolChoice(fieldBufs["tool_choice"].Bytes(), toolChoiceIsString, toolChoiceStrBuf.String())
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: %w", err)
	}

	responseMIME, err := parseTextConfig(fieldBufs["text"].Bytes())
	if err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: %w", err)
	}

	var reasoningEffort string
	if fieldBufs["reasoning"].Len() > 0 {
		re, err := decodeReasoningControl(fieldBufs["reasoning"].Bytes())
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: %w", err)
		}
		reasoningEffort = re
	}

	// 11. Build identity configuration
	// Explicit workspace hint projects to the canonical extension on the
	// bounded path as well; invalid values decline to canonical decode,
	// which returns the existing protocol 400 (Requirements 1.1, 1.3).
	workspaceCall := &aipapi.Call{Extensions: make(map[string]json.RawMessage)}
	if err := sessionwire.ApplyWorkspaceHint(workspaceCall, in.Headers, aipsdk.HTTPHeaders{}); err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: %w", err)
	}
	idCfg := largebody.CallIdentityConfig{
		Session: aipapi.SessionRef{
			AuthoritativeSessionID: sessIn.AuthoritativeSessionID,
			ClientSessionID:        sessIn.ClientSessionID,
			ResumeToken:            sessIn.ResumeToken.Reveal(),
			Metadata:               metadata,
		},
		SessionInput:  sessIn,
		RouteSelector: sel,
		Instructions:  nil, // In OpenResponses, instructions is placed in Items[0], NOT in Instructions
		Tools:         canonicalTools,
		ToolChoice:    toolChoice,
		Options: aipapi.GenerationOptions{
			Temperature:       temperature,
			TopP:              topP,
			MaxOutputTokens:   maxOut,
			ParallelToolCalls: parallelTools,
			ReasoningEffort:   reasoningEffort,
			ResponseMIMEType:  responseMIME,
		},
		Extensions: workspaceCall.Extensions,
	}

	var digest largebody.IdentityDigest
	var turnShape largebody.ClientTurnShape

	instructionText := instructionsBuf.String()
	hasInstructions := instructionsBuf.Len() > 0 && strings.TrimSpace(instructionText) != ""
	var canonicalItems []aipapi.Item

	if inputIsString {
		// Pass 2: Stream input string directly through BeginMessageItem + BeginTextContentPart
		rc2, err := in.Source.Open()
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: open replay source (pass 2): %w", err)
		}
		defer func() { _ = rc2.Close() }()

		idWriter, err := largebody.NewCallIdentityWriter(idCfg)
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: identity writer: %w", err)
		}

		if err := idWriter.StartItems(); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: start items: %w", err)
		}

		var turnItems []largebody.ClientTurnItemShape
		var totalContentBytes int64
		var ordinal int64

		if hasInstructions {
			if err := idWriter.AddItem(aipapi.Item{
				Kind:    aipapi.ItemKindMessage,
				Status:  aipapi.ItemStatusCompleted,
				Role:    aipapi.RoleSystem,
				Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: instructionText}},
			}); err != nil {
				return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: add instruction item: %w", err)
			}
			instLen := int64(len(instructionText))
			turnItems = append(turnItems, largebody.ClientTurnItemShape{
				Kind:    aipapi.ItemKindMessage,
				Role:    aipapi.RoleSystem,
				Ordinal: ordinal,
				Parts: []largebody.ClientTurnPartShape{{
					Kind:         aipapi.ContentPartText,
					ContentBytes: instLen,
				}},
			})
			totalContentBytes += instLen
			ordinal++
		}

		iw, err := idWriter.BeginMessageItem("", aipapi.ItemStatusCompleted, aipapi.RoleUser, "")
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: begin message item: %w", err)
		}

		pw, err := iw.BeginTextContentPart()
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: begin text content part: %w", err)
		}

		strResolver2 := func(sctx jsonshape.StringContext) (io.Writer, error) {
			if sctx.TopLevel && sctx.Key == "input" {
				return pw, nil
			}
			return nil, nil
		}

		scanner2 := jsonshape.NewScanner(
			scanCtx,
			scannerLimits,
			jsonshape.WithStringWriterResolver(strResolver2),
		)

		if _, err := largebody.ProcessReplayChunks(largebody.ReplayChunkReaderConfig{
			Reader:    rc2,
			MaxBytes:  bodyBytes,
			ChunkSize: 32 * 1024,
			Scanner:   scanner2,
		}); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: streaming proof pass 2: %w", err)
		}

		if err := iw.EndItem(); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: end item: %w", err)
		}

		d, err := idWriter.Digest()
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: digest: %w", err)
		}
		digest = d

		turnItems = append(turnItems, largebody.ClientTurnItemShape{
			Kind:    aipapi.ItemKindMessage,
			Role:    aipapi.RoleUser,
			Ordinal: ordinal,
			Parts: []largebody.ClientTurnPartShape{{
				Kind:         aipapi.ContentPartText,
				ContentBytes: inputInspector.totalBytes,
			}},
		})
		totalContentBytes += inputInspector.totalBytes

		turnShape = largebody.ClientTurnShape{
			Items:             turnItems,
			TotalContentBytes: totalContentBytes,
		}
	} else if !inputArrayIsLarge {
		// Enforce strict whitelist of simple text and text-tool shapes.
		// Any unrepresented canonical protocol features (item_reference, compaction,
		// assistant phase, reasoning, content part annotations, assistant_ref, non-text content,
		// non-text tool result output, or unknown fields) decline precommit to canonical.
		if err := validateWireInputArrayWhitelist(inputArrayBuf.Bytes()); err != nil {
			return frontendpipe.ProofOutput{}, err
		}

		// Small array input: parse items into aipapi.Item using canonical proto.DecodeItem
		var wireItems []proto.WireItem
		if err := json.Unmarshal(inputArrayBuf.Bytes(), &wireItems); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: invalid item array input: %w", err)
		}
		if err := frontendlimits.Count("input", len(wireItems), frontendlimits.MaxMessages); err != nil {
			return frontendpipe.ProofOutput{}, err
		}

		continuationRefs := 0
		for _, w := range wireItems {
			if w.Type == "item_reference" {
				continuationRefs++
			}
		}
		if err := proto.ValidateContinuationRefCount(continuationRefs, proto.DefaultLimits()); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: %w", err)
		}

		if hasInstructions {
			canonicalItems = append(canonicalItems, aipapi.Item{
				Kind:    aipapi.ItemKindMessage,
				Status:  aipapi.ItemStatusCompleted,
				Role:    aipapi.RoleSystem,
				Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: instructionText}},
			})
		}
		for i, w := range wireItems {
			item, keep, err := proto.DecodeReplayItem(w, proto.DefaultLimits())
			if err != nil {
				return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: item[%d]: %w", i, err)
			}
			if !keep {
				continue
			}
			canonicalItems = append(canonicalItems, item)
		}

		callForValidation := aipapi.Call{
			Items:      canonicalItems,
			Tools:      canonicalTools,
			ToolChoice: toolChoice,
			Options:    idCfg.Options,
		}
		if err := callForValidation.Validate(); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: canonical call validation failed: %w", err)
		}

		callForShape := aipapi.Call{
			Items: canonicalItems,
		}
		ts, err := largebody.ClientTurnShapeFromCall(&callForShape, factBudget)
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: turn shape: %w", err)
		}
		turnShape = ts

		idWriter, err := largebody.NewCallIdentityWriter(idCfg)
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: identity writer: %w", err)
		}
		if err := idWriter.StartItems(); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: start items: %w", err)
		}
		for _, item := range canonicalItems {
			if err := idWriter.AddItem(item); err != nil {
				return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: add item: %w", err)
			}
		}
		d, err := idWriter.Digest()
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: digest: %w", err)
		}
		digest = d
	} else {
		// Large chunked array input: stream in Pass 2
		rc2, err := in.Source.Open()
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: open replay source (pass 2): %w", err)
		}
		defer func() { _ = rc2.Close() }()

		idWriter, err := largebody.NewCallIdentityWriter(idCfg)
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: identity writer: %w", err)
		}
		if err := idWriter.StartItems(); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: start items: %w", err)
		}

		var turnItems []largebody.ClientTurnItemShape
		var totalContentBytes int64
		var ordinal int64

		if hasInstructions {
			if err := idWriter.AddItem(aipapi.Item{
				Kind:    aipapi.ItemKindMessage,
				Status:  aipapi.ItemStatusCompleted,
				Role:    aipapi.RoleSystem,
				Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: instructionText}},
			}); err != nil {
				return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: add instruction item: %w", err)
			}
			instLen := int64(len(instructionText))
			turnItems = append(turnItems, largebody.ClientTurnItemShape{
				Kind:    aipapi.ItemKindMessage,
				Role:    aipapi.RoleSystem,
				Ordinal: ordinal,
				Parts: []largebody.ClientTurnPartShape{{
					Kind:         aipapi.ContentPartText,
					ContentBytes: instLen,
				}},
			})
			totalContentBytes += instLen
			ordinal++
		}

		var itemIndex int
		var currItemWriter *largebody.ItemIdentityWriter
		var currPartBytes int64

		arrayStrResolver := func(sctx jsonshape.StringContext) (io.Writer, error) {
			if len(sctx.Path) >= 2 && sctx.Path[0] == "input" && sctx.Key == "content" {
				if currItemWriter != nil {
					_ = currItemWriter.EndItem()
				}
				role := aipapi.RoleUser
				if itemIndex < len(largeArrayRoles) {
					role = largeArrayRoles[itemIndex]
				}
				status := aipapi.ItemStatus("")
				if itemIndex < len(largeArrayStatuses) {
					status = largeArrayStatuses[itemIndex]
				}
				iw, err := idWriter.BeginMessageItem("", status, role, "")
				if err != nil {
					return nil, err
				}
				currItemWriter = iw
				pw, err := iw.BeginTextContentPart()
				if err != nil {
					return nil, err
				}
				currPartBytes = 0
				return &frontendpipe.ByteCountWriter{Inner: pw, Count: &currPartBytes}, nil
			}
			return nil, nil
		}

		arrayHandler := jsonshape.EventHandlerFunc(func(e jsonshape.Event) error {
			if len(e.Path) >= 2 && e.Path[0] == "input" && e.Type == jsonshape.EventString && e.Key == "content" {
				role := aipapi.RoleUser
				if itemIndex < len(largeArrayRoles) {
					role = largeArrayRoles[itemIndex]
				}
				turnItems = append(turnItems, largebody.ClientTurnItemShape{
					Kind:    aipapi.ItemKindMessage,
					Role:    role,
					Ordinal: ordinal,
					Parts: []largebody.ClientTurnPartShape{{
						Kind:         aipapi.ContentPartText,
						ContentBytes: currPartBytes,
					}},
				})
				totalContentBytes += currPartBytes
				ordinal++
				itemIndex++
			}
			return nil
		})

		arrayScanner := jsonshape.NewScanner(
			scanCtx,
			scannerLimits,
			jsonshape.WithEventHandler(arrayHandler),
			jsonshape.WithStringWriterResolver(arrayStrResolver),
		)

		if _, err := largebody.ProcessReplayChunks(largebody.ReplayChunkReaderConfig{
			Reader:    rc2,
			MaxBytes:  bodyBytes,
			ChunkSize: 32 * 1024,
			Scanner:   arrayScanner,
		}); err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: array streaming proof: %w", err)
		}

		if currItemWriter != nil {
			_ = currItemWriter.EndItem()
		}
		d, err := idWriter.Digest()
		if err != nil {
			return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: digest: %w", err)
		}
		digest = d

		turnShape = largebody.ClientTurnShape{
			Items:             turnItems,
			TotalContentBytes: totalContentBytes,
		}
	}

	// 12. Compute source digest
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

	deliveryMode := aipapi.DeliveryModeFromClientStream(stream)

	var proofCompactionFacts compactionfacts.RequestFacts
	var proofCompactionComplete bool
	if len(turnShape.Items) > 0 {
		if facts, complete, err := compileStreamingCompactionFacts(
			scanCtx,
			in.Source,
			bodyBytes,
			instructionText,
			canonicalTools,
			turnShape.Items,
			canonicalItems,
			inputIsString,
			inputArrayIsLarge,
			largeArrayRoles,
			factBudget,
		); err == nil && complete {
			proofCompactionFacts = facts
			proofCompactionComplete = complete
		}
	}

	hasTools := len(canonicalTools) > 0
	if !hasTools {
		for _, it := range canonicalItems {
			if it.Kind == aipapi.ItemKindToolCall || it.Kind == aipapi.ItemKindToolResult || it.Role == aipapi.RoleTool {
				hasTools = true
				break
			}
		}
	}

	requiredCaps := largebody.DeriveRequiredCapabilities(turnShape, largebody.ControlRequirements{
		Delivery:          deliveryMode,
		HasTools:          hasTools,
		ParallelToolCalls: parallelTools,
		ReasoningEffort:   reasoningEffort,
		StructuredOutputs: responseMIME != "",
		ItemAuthoritative: true,
	})

	proof := largebody.Proof{
		ProfileID:            ProfileID,
		Operation:            aipapi.OperationOpenResponsesCreate,
		Delivery:             deliveryMode,
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
			aipapi.OperationOpenResponsesCreate, in.Headers, canonicalTools),
		Facts: largebody.ProtocolFacts{
			RequirementsID: ProfileID,
			ControlCount:   int64(len(canonicalTools)),
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
			Extra:     wireExtraState{ToolChoice: toolChoice, Options: idCfg.Options},
		},
	}

	if err := proofOut.Validate(factBudget); err != nil {
		return frontendpipe.ProofOutput{}, fmt.Errorf("openresponses: validate proof output: %w", err)
	}

	return proofOut, nil
}

func parseTools(raw []byte) ([]aipapi.ToolDef, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var wireTools []struct {
		Type        string          `json:"type"`
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters,omitempty"`
	}
	if err := json.Unmarshal(trimmed, &wireTools); err != nil {
		return nil, errors.New("tools must be an array")
	}
	canonicalTools := make([]aipapi.ToolDef, 0, len(wireTools))
	for i, wt := range wireTools {
		if wt.Type != "function" {
			return nil, fmt.Errorf("tool[%d] has unsupported type %q", i, wt.Type)
		}
		if wt.Name == "" {
			return nil, fmt.Errorf("tool[%d] missing name", i)
		}
		canonicalTools = append(canonicalTools, aipapi.ToolDef{
			Name:        wt.Name,
			Description: wt.Description,
			Parameters:  bytes.Clone(wt.Parameters),
		})
	}
	return canonicalTools, nil
}

func parseToolChoice(raw []byte, isString bool, strVal string) (aipapi.ToolChoice, error) {
	if isString {
		switch strVal {
		case "auto":
			return aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto}, nil
		case "none":
			return aipapi.ToolChoice{Mode: aipapi.ToolChoiceNone}, nil
		case "required":
			return aipapi.ToolChoice{Mode: aipapi.ToolChoiceAny}, nil
		default:
			return aipapi.ToolChoice{}, fmt.Errorf("unknown string tool_choice %q", strVal)
		}
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return aipapi.ToolChoice{}, nil
	}
	var rawObj map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &rawObj); err != nil {
		return aipapi.ToolChoice{}, errors.New("tool_choice must be a string or object")
	}
	typeVal := ""
	if t, ok := rawObj["type"]; ok {
		_ = json.Unmarshal(t, &typeVal)
	}
	if typeVal == "function" {
		name := ""
		if rawName, ok := rawObj["name"]; ok {
			_ = json.Unmarshal(rawName, &name)
		}
		if name == "" {
			if fn, ok := rawObj["function"]; ok {
				var funcObj proto.WireToolChoiceFunctionName
				if err := json.Unmarshal(fn, &funcObj); err != nil {
					return aipapi.ToolChoice{}, errors.New("invalid function tool_choice object")
				}
				name = funcObj.Name
			}
		}
		if name == "" {
			return aipapi.ToolChoice{}, errors.New("invalid function tool_choice object missing name")
		}
		return aipapi.ToolChoice{Mode: aipapi.ToolChoiceRequired, Name: name}, nil
	}
	if typeVal == "allowed_tools" {
		return decodeAllowedTools(rawObj)
	}
	return aipapi.ToolChoice{}, fmt.Errorf("unknown object tool_choice type %q", typeVal)
}

func decodeAllowedTools(rawObj map[string]json.RawMessage) (aipapi.ToolChoice, error) {
	mode := aipapi.ToolChoiceAuto
	if raw, ok := rawObj["mode"]; ok {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return aipapi.ToolChoice{}, errors.New("invalid allowed_tools mode")
		}
		switch s {
		case "auto":
			mode = aipapi.ToolChoiceAuto
		case "none":
			mode = aipapi.ToolChoiceNone
		case "required":
			mode = aipapi.ToolChoiceAny
		default:
			return aipapi.ToolChoice{}, fmt.Errorf("unknown allowed_tools mode %q", s)
		}
	}

	rawTools, ok := rawObj["tools"]
	if !ok {
		return aipapi.ToolChoice{}, errors.New("allowed_tools requires a tools array")
	}
	var refs []proto.WireToolChoiceAllowedToolRef
	if err := json.Unmarshal(rawTools, &refs); err != nil {
		return aipapi.ToolChoice{}, errors.New("invalid allowed_tools tools array")
	}
	if len(refs) == 0 {
		return aipapi.ToolChoice{}, errors.New("allowed_tools tools must not be empty")
	}
	if len(refs) > aipapi.MaxAllowedToolRefs {
		return aipapi.ToolChoice{}, fmt.Errorf("allowed_tools tools array exceeds %d references", aipapi.MaxAllowedToolRefs)
	}
	names := make([]string, 0, len(refs))
	for _, ref := range refs {
		if ref.Type != "function" {
			return aipapi.ToolChoice{}, fmt.Errorf("allowed_tools tool reference has unsupported type %q", ref.Type)
		}
		if ref.Name == "" || ref.Name != strings.TrimSpace(ref.Name) {
			return aipapi.ToolChoice{}, errors.New("allowed_tools tool reference requires a valid name")
		}
		names = append(names, ref.Name)
	}
	return aipapi.ToolChoice{Mode: mode, AllowedTools: names}, nil
}

func parseTextConfig(raw []byte) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", nil
	}
	if !supportedTextFormat(raw) {
		return "", errors.New("unsupported text format")
	}
	var text struct {
		Format *struct {
			Type string `json:"type"`
		} `json:"format"`
	}
	if err := json.Unmarshal(trimmed, &text); err != nil {
		return "", errors.New("unsupported text format")
	}
	if text.Format == nil {
		return "", nil
	}
	switch text.Format.Type {
	case "text":
		return "text/plain", nil
	case "json_object":
		return "application/json", nil
	default:
		return "", errors.New("unsupported text format")
	}
}

func decodeReasoningControl(raw []byte) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil || fields == nil {
		return "", errors.New("reasoning must be an object")
	}
	for key := range fields {
		if key != "effort" {
			return "", fmt.Errorf("reasoning field %q is unsupported", key)
		}
	}
	rawEffort, ok := fields["effort"]
	if !ok || len(rawEffort) == 0 || bytes.Equal(bytes.TrimSpace(rawEffort), []byte("null")) {
		return "", nil
	}
	var effort string
	if err := json.Unmarshal(rawEffort, &effort); err != nil {
		return "", errors.New("reasoning.effort must be a string")
	}
	effort = strings.TrimSpace(effort)
	if effort == "" {
		return "", errors.New("reasoning.effort must not be empty")
	}
	switch effort {
	case "none", "low", "medium", "high", "xhigh":
		return effort, nil
	default:
		return "", fmt.Errorf("reasoning.effort %q is not supported", effort)
	}
}

var _ frontendpipe.FrontendProfile = (*Profile)(nil)

func compileStreamingCompactionFacts(
	ctx context.Context,
	src largebody.Source,
	bodyBytes int64,
	instructionText string,
	tools []aipapi.ToolDef,
	items []largebody.ClientTurnItemShape,
	canonicalItems []aipapi.Item,
	inputIsString bool,
	inputArrayIsLarge bool,
	largeArrayRoles []aipapi.Role,
	maxFactBytes int64,
) (compactionfacts.RequestFacts, bool, error) {
	if int64(len(items)*compactionfacts.ItemHashSizeBytes) > maxFactBytes {
		return compactionfacts.RequestFacts{}, false, nil
	}
	factBuilder := compactionfacts.NewBuilderWithByteBudget(aipapi.OperationOpenResponsesCreate, int(maxFactBytes))
	factBuilder.AddToolCount(len(tools))

	// Fast path: small array input was already decoded into canonicalItems
	if len(canonicalItems) > 0 {
		for _, it := range canonicalItems {
			if err := factBuilder.AddItem(it); err != nil {
				return compactionfacts.RequestFacts{}, false, err
			}
		}
		facts, err := factBuilder.Build()
		return facts, err == nil, err
	}

	hasInstructions := strings.TrimSpace(instructionText) != ""
	if hasInstructions {
		instItem := aipapi.Item{
			Kind:    aipapi.ItemKindMessage,
			Status:  aipapi.ItemStatusCompleted,
			Role:    aipapi.RoleSystem,
			Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: instructionText}},
		}
		if err := factBuilder.AddItem(instItem); err != nil {
			return compactionfacts.RequestFacts{}, false, err
		}
	}

	inputStartIdx := 0
	if hasInstructions {
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
				return mw, nil
			}
			return nil, nil
		}

		handler := jsonshape.EventHandlerFunc(func(e jsonshape.Event) error {
			if e.TopLevel && e.Type == jsonshape.EventString && e.Key == "input" {
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
	} else if inputArrayIsLarge {
		var itemIndex int
		var hasher *compactionfacts.ItemHasher
		var factErr error

		resolver := func(sctx jsonshape.StringContext) (io.Writer, error) {
			if len(sctx.Path) >= 2 && sctx.Path[0] == "input" && sctx.Key == "content" {
				if itemIndex >= len(inputItems) {
					return nil, errors.New("openresponses: unexpected extra item in compaction pass")
				}
				role := aipapi.RoleUser
				if itemIndex < len(largeArrayRoles) {
					role = largeArrayRoles[itemIndex]
				}
				partBytes := int(inputItems[itemIndex].Parts[0].ContentBytes)
				hasher = compactionfacts.NewItemHasher(aipapi.ItemKindMessage, role)
				pw := hasher.BeginContentText(partBytes)
				mw := io.MultiWriter(pw, chunkWriterFunc(func(p []byte) (int, error) {
					factBuilder.FeedTextChunkBytes(p)
					return len(p), nil
				}))
				return mw, nil
			}
			return nil, nil
		}

		handler := jsonshape.EventHandlerFunc(func(e jsonshape.Event) error {
			if len(e.Path) >= 2 && e.Path[0] == "input" && e.Type == jsonshape.EventString && e.Key == "content" {
				if hasher != nil {
					hasher.EndContentText()
					factBuilder.EndField()
					itemHash := hasher.Sum()
					if err := factBuilder.AddItemHash(itemHash); err != nil {
						factErr = err
						return err
					}
					itemIndex++
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
		if readErr != nil || factErr != nil || itemIndex != len(inputItems) {
			return compactionfacts.RequestFacts{}, false, readErr
		}
	}

	facts, berr := factBuilder.Build()
	if berr != nil {
		return compactionfacts.RequestFacts{}, false, berr
	}
	return facts, true, nil
}

func validateWireInputArrayWhitelist(raw []byte) error {
	var rawItems []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rawItems); err != nil {
		return fmt.Errorf("openresponses: invalid item array input: %w", err)
	}
	for i, rawItem := range rawItems {
		var itemType string
		if rawType, ok := rawItem["type"]; ok {
			if err := json.Unmarshal(rawType, &itemType); err != nil {
				return fmt.Errorf("openresponses: item[%d]: invalid type: %w", i, err)
			}
		}
		itemType = strings.TrimSpace(itemType)
		switch itemType {
		case "", "message":
			for k := range rawItem {
				switch k {
				case "id", "type", "status", "role", "content":
					// allowed
				default:
					return fmt.Errorf("openresponses: item[%d]: unsupported field %q requires canonical decode", i, k)
				}
			}
			if rawContent, ok := rawItem["content"]; ok {
				if err := validateWireMessageContent(i, rawContent); err != nil {
					return err
				}
			}
		case "function_call":
			for k := range rawItem {
				switch k {
				case "id", "type", "call_id", "name", "arguments", "status":
					// allowed
				default:
					return fmt.Errorf("openresponses: item[%d]: unsupported function_call field %q requires canonical decode", i, k)
				}
			}
		case "function_call_output", "function_output":
			for k := range rawItem {
				switch k {
				case "id", "type", "call_id", "output", "status":
					// allowed
				default:
					return fmt.Errorf("openresponses: item[%d]: unsupported tool result field %q requires canonical decode", i, k)
				}
			}
			if rawOutput, ok := rawItem["output"]; ok {
				if err := validateWireToolOutput(i, rawOutput); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("openresponses: item[%d]: unsupported item type %q requires canonical decode", i, itemType)
		}
	}
	return nil
}

func validateWireMessageContent(itemIdx int, raw json.RawMessage) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return fmt.Errorf("openresponses: item[%d]: invalid content string: %w", itemIdx, err)
		}
		return nil
	}
	if trimmed[0] == '[' {
		var parts []map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &parts); err != nil {
			return fmt.Errorf("openresponses: item[%d]: invalid content parts array: %w", itemIdx, err)
		}
		for partIdx, part := range parts {
			var partType string
			if rawPT, ok := part["type"]; ok {
				if err := json.Unmarshal(rawPT, &partType); err != nil {
					return fmt.Errorf("openresponses: item[%d].parts[%d]: invalid part type: %w", itemIdx, partIdx, err)
				}
			}
			partType = strings.TrimSpace(partType)
			switch partType {
			case "text", "input_text", "output_text":
				for pk := range part {
					switch pk {
					case "type", "text":
						// allowed
					default:
						return fmt.Errorf("openresponses: item[%d].parts[%d]: unsupported content part field %q requires canonical decode", itemIdx, partIdx, pk)
					}
				}
			default:
				return fmt.Errorf("openresponses: item[%d].parts[%d]: unsupported content part type %q requires canonical decode", itemIdx, partIdx, partType)
			}
		}
		return nil
	}
	return fmt.Errorf("openresponses: item[%d]: non-string, non-array content requires canonical decode", itemIdx)
}

func validateWireToolOutput(itemIdx int, raw json.RawMessage) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return fmt.Errorf("openresponses: item[%d]: invalid output string: %w", itemIdx, err)
		}
		return nil
	}
	if trimmed[0] == '[' {
		var parts []map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &parts); err != nil {
			return fmt.Errorf("openresponses: item[%d]: invalid output parts array: %w", itemIdx, err)
		}
		for partIdx, part := range parts {
			var partType string
			if rawPT, ok := part["type"]; ok {
				if err := json.Unmarshal(rawPT, &partType); err != nil {
					return fmt.Errorf("openresponses: item[%d].output[%d]: invalid part type: %w", itemIdx, partIdx, err)
				}
			}
			partType = strings.TrimSpace(partType)
			switch partType {
			case "text", "input_text", "output_text":
				for pk := range part {
					switch pk {
					case "type", "text":
						// allowed
					default:
						return fmt.Errorf("openresponses: item[%d].output[%d]: unsupported output part field %q requires canonical decode", itemIdx, partIdx, pk)
					}
				}
			default:
				return fmt.Errorf("openresponses: item[%d].output[%d]: unsupported output part type %q requires canonical decode", itemIdx, partIdx, partType)
			}
		}
		return nil
	}
	return fmt.Errorf("openresponses: item[%d]: non-text tool output requires canonical decode", itemIdx)
}
