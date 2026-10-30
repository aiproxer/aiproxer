package aipapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
)

// PartKind classifies canonical content parts.
type PartKind string

const (
	PartText       PartKind = "text"
	PartImageRef   PartKind = "image_ref"
	PartFileRef    PartKind = "file_ref"
	PartToolResult PartKind = "tool_result"
	PartJSON       PartKind = "json"
	PartReasoning  PartKind = "reasoning"
)

// TextPart constructs a canonical text part for tests and adapters.
func TextPart(s string) Part {
	return Part{Kind: PartText, Text: s}
}

// FilePart constructs a file reference part for documents, PDFs, and other binary attachments.
func FilePart(ref, mime, name string) Part {
	return Part{Kind: PartFileRef, FileRef: ref, FileMIME: mime, FileName: name}
}

// Part is one ordered content fragment inside a message.
type Part struct {
	Kind PartKind

	Text string

	ImageRef  string
	ImageMIME string

	FileRef  string
	FileMIME string
	FileName string

	ToolCallID string
	ToolName   string
	Content    json.RawMessage

	// Reasoning carries historical assistant reasoning when Kind is PartReasoning.
	Reasoning *ReasoningPart
}

func (p Part) validate() error {
	if p.Reasoning != nil && p.Kind != PartReasoning {
		return errors.New("reasoning is only allowed when Kind is reasoning")
	}
	switch p.Kind {
	case PartText:
		if p.Text == "" {
			return errors.New("text part requires non-empty Text")
		}
	case PartImageRef:
		if p.ImageRef == "" {
			return errors.New("image_ref part requires ImageRef")
		}
	case PartFileRef:
		if p.FileRef == "" {
			return errors.New("file_ref part requires FileRef")
		}
	case PartToolResult:
		if p.ToolCallID == "" {
			return errors.New("tool_result part requires ToolCallID")
		}
	case PartJSON:
		if len(p.Content) == 0 {
			return errors.New("json part requires Content")
		}
		if !json.Valid(p.Content) {
			return errors.New("json part Content must be valid JSON")
		}
	case PartReasoning:
		if p.Text != "" || p.ImageRef != "" || p.ImageMIME != "" ||
			p.FileRef != "" || p.FileMIME != "" || p.FileName != "" ||
			p.ToolCallID != "" || p.ToolName != "" || len(p.Content) > 0 {
			return errors.New("reasoning part must not set unrelated part fields")
		}
		return validateReasoningPart(p.Reasoning)
	case "":
		return errors.New("part kind is required")
	default:
		return fmt.Errorf("unknown part kind %q", p.Kind)
	}
	return nil
}

func validateReasoningPart(rp *ReasoningPart) error {
	if rp == nil {
		return errors.New("reasoning part requires Reasoning")
	}
	normalized := NormalizeReasoningDialect(rp.Dialect)
	if normalized == "" {
		return errors.New("reasoning dialect is required")
	}
	if len(normalized) > MaxReasoningDialectBytes {
		return fmt.Errorf("reasoning dialect exceeds %d bytes", MaxReasoningDialectBytes)
	}
	if rp.Dialect != normalized {
		return errors.New("reasoning dialect must be normalized")
	}
	if rp.Text == "" && rp.Signature == "" && len(rp.Opaque) == 0 &&
		len(rp.Summary) == 0 && len(rp.Content) == 0 && !rp.EncryptedContentPresent {
		return errors.New("reasoning payload requires at least one reasoning field")
	}
	for name, raw := range map[string]json.RawMessage{
		"summary": rp.Summary,
		"content": rp.Content,
	} {
		if len(raw) == 0 {
			continue
		}
		if !json.Valid(raw) {
			return fmt.Errorf("reasoning %s must be valid JSON", name)
		}
	}
	if rp.EncryptedContentPresent && len(rp.EncryptedContent) > 0 && !json.Valid(rp.EncryptedContent) {
		return errors.New("reasoning encrypted_content must be valid JSON")
	}
	if len(rp.Text) > MaxReasoningTextBytes {
		return fmt.Errorf("reasoning text exceeds %d bytes", MaxReasoningTextBytes)
	}
	if len(rp.Signature) > MaxReasoningSignatureBytes {
		return fmt.Errorf("reasoning signature exceeds %d bytes", MaxReasoningSignatureBytes)
	}
	if len(rp.Opaque) > MaxReasoningOpaqueBytes {
		return fmt.Errorf("reasoning opaque exceeds %d bytes", MaxReasoningOpaqueBytes)
	}
	if len(rp.Summary) > MaxPartJSONBytes || len(rp.Content) > MaxPartJSONBytes || len(rp.EncryptedContent) > MaxPartJSONBytes {
		return fmt.Errorf("reasoning exact metadata exceeds %d bytes", MaxPartJSONBytes)
	}
	if len(rp.Opaque) > 0 && !json.Valid(rp.Opaque) {
		return errors.New("reasoning opaque must be valid JSON")
	}
	return nil
}

// ToolDef is a canonical function/tool declaration (not a raw provider blob).
type ToolDef struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// ToolChoiceMode selects how tool calls are admitted for this request.
type ToolChoiceMode string

const (
	ToolChoiceAuto     ToolChoiceMode = "auto"
	ToolChoiceNone     ToolChoiceMode = "none"
	ToolChoiceAny      ToolChoiceMode = "any"
	ToolChoiceRequired ToolChoiceMode = "required"
)

// ToolChoice constrains tool usage for the call.
type ToolChoice struct {
	Mode ToolChoiceMode
	// Name is used when Mode requires a specific tool.
	Name string
	// AllowedTools is the OpenResponses allowed_tools subset: when non-empty,
	// only tools named here may be invoked, while the full Tools list stays
	// visible to the model (cache-preserving control surface). Mode still
	// governs whether tools may/should/must be called (auto/none/any).
	// Empty means no subset restriction.
	AllowedTools []string
}

func (tc ToolChoice) validate(toolCount int, tools []ToolDef) error {
	switch tc.Mode {
	case "", ToolChoiceAuto, ToolChoiceAny, ToolChoiceNone, ToolChoiceRequired:
	default:
		return &ValidationError{Field: "ToolChoice.Mode", Message: fmt.Sprintf("unknown tool choice mode %q", tc.Mode)}
	}
	mode := tc.Mode
	if mode == "" {
		mode = ToolChoiceAuto
	}
	if len(tc.AllowedTools) > 0 {
		return tc.validateAllowedTools(tools, mode)
	}
	if mode == ToolChoiceNone && toolCount > 0 {
		return &ValidationError{Field: "ToolChoice", Message: "ToolChoiceNone is incompatible with declared tools"}
	}
	if tc.Name != "" {
		if err := validateExactStringField("ToolChoice.Name", tc.Name, MaxToolNameBytes); err != nil {
			return err
		}
	}
	if tc.Name != "" && mode != ToolChoiceRequired {
		return &ValidationError{Field: "ToolChoice.Name", Message: "ToolChoice.Name is only allowed with ToolChoiceRequired mode"}
	}
	if mode == ToolChoiceRequired {
		if toolCount == 0 {
			return &ValidationError{Field: "Tools", Message: "ToolChoiceRequired requires at least one tool definition"}
		}
		if tc.Name == "" {
			return &ValidationError{Field: "ToolChoice.Name", Message: "ToolChoiceRequired requires ToolChoice.Name"}
		}
		found := false
		for _, t := range tools {
			if t.Name == tc.Name {
				found = true
				break
			}
		}
		if !found {
			return &ValidationError{Field: "ToolChoice.Name", Message: "ToolChoiceRequired name must match a declared tool"}
		}
	}
	return nil
}

// validateAllowedTools enforces the allowed_tools subset contract: every
// allowed name must be a non-empty, whitespace-free, duplicate-free reference
// to a declared tool, and the mode must be an auto/none/any combination (a
// forced single-function Mode Required or a Name is contradictory with a
// subset). Mode None is legal here (the subset is vacuous), so the plain
// "none with declared tools" incompatibility rule does not apply.
func (tc ToolChoice) validateAllowedTools(tools []ToolDef, mode ToolChoiceMode) error {
	if mode == ToolChoiceRequired {
		return &ValidationError{Field: "ToolChoice.Mode", Message: "ToolChoice.Mode required is incompatible with ToolChoice.AllowedTools"}
	}
	if tc.Name != "" {
		return &ValidationError{Field: "ToolChoice.Name", Message: "ToolChoice.Name is incompatible with ToolChoice.AllowedTools"}
	}
	declared := make(map[string]bool, len(tools))
	for _, t := range tools {
		declared[t.Name] = true
	}
	seen := make(map[string]bool, len(tc.AllowedTools))
	for _, name := range tc.AllowedTools {
		if err := validateExactStringField("ToolChoice.AllowedTools", name, MaxToolNameBytes); err != nil {
			return err
		}
		if name == "" {
			return &ValidationError{Field: "ToolChoice.AllowedTools", Message: "ToolChoice.AllowedTools entry cannot be empty"}
		}
		if seen[name] {
			return &ValidationError{Field: "ToolChoice.AllowedTools", Message: fmt.Sprintf("duplicate allowed tool %q", name)}
		}
		seen[name] = true
		if !declared[name] {
			return &ValidationError{Field: "ToolChoice.AllowedTools", Message: fmt.Sprintf("allowed tool %q must match a declared tool", name)}
		}
	}
	return nil
}

// ValidateToolChoice checks tool choice against declared tools.
func ValidateToolChoice(tc ToolChoice, tools []ToolDef) error {
	return tc.validate(len(tools), tools)
}

// GenerationOptions captures cross-protocol generation controls.
// Pointer fields mean "unset" (no override). Non-pointer strings use "" as unset;
// validation applies only when a field participates in an invariant (see validateOptionStrings).
type GenerationOptions struct {
	Temperature       *float64
	MaxOutputTokens   *int
	TopP              *float64
	ReasoningEffort   string
	Verbosity         VerbosityLevel
	ResponseMIMEType  string
	ParallelToolCalls *bool
}

func (o GenerationOptions) validate() error {
	if o.Temperature != nil {
		t := *o.Temperature
		// NaN fails both range comparisons, so reject it explicitly; int32 casts of
		// NaN downstream are implementation-defined and would corrupt wire options.
		if math.IsNaN(t) || t < 0 || t > 2 {
			return &ValidationError{Field: "Options.Temperature", Message: "temperature must be between 0 and 2"}
		}
	}
	if o.TopP != nil {
		p := *o.TopP
		if math.IsNaN(p) || p < 0 || p > 1 {
			return &ValidationError{Field: "Options.TopP", Message: "top_p must be between 0 and 1"}
		}
	}
	if o.MaxOutputTokens != nil {
		v := *o.MaxOutputTokens
		if v < 0 {
			return &ValidationError{Field: "Options.MaxOutputTokens", Message: "max_output_tokens must be non-negative"}
		}
		if v > math.MaxInt32 {
			return &ValidationError{Field: "Options.MaxOutputTokens", Message: fmt.Sprintf("max_output_tokens must be at most %d", math.MaxInt32)}
		}
	}
	return validateOptionStrings(o)
}
