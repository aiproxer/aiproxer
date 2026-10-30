package aipapi

import (
	"fmt"
	"strconv"
	"strings"
)

// Envelope size limits for Call validation. They bound how much work a single
// request can force in the core and protect against pathological or hostile
// clients. Frontends also cap raw HTTP body size; these apply to the decoded
// canonical call.
const (
	MaxCallIDBytes          = 512
	MaxRouteSelectorBytes   = 64 * 1024
	MaxClientSessionIDBytes = 4 * 1024
	MaxContinuityKeyBytes   = 4 * 1024
	MaxALegIDBytes          = 4 * 1024
	// MaxAuthoritativeSessionIDBytes bounds proxy-owned session ids (opaque strings).
	MaxAuthoritativeSessionIDBytes = 4 * 1024
	// MaxResumeTokenBytes bounds bearer resume proofs presented on the canonical call (decoded by frontends).
	MaxResumeTokenBytes = 8 * 1024
	// MaxStructuralCauseBytes bounds the proxy-owned structural cause label on
	// a canonical call. The label is a fixed diagnostic token, never content.
	MaxStructuralCauseBytes = 64
	MaxMessages             = 4_096
	MaxInstructionMessages  = 1_024
	MaxPartsPerMessage      = 2_048
	MaxTools                = 2_048
	MaxToolNameBytes        = 256
	MaxToolDescriptionBytes = 32 * 1024
	MaxToolParametersBytes  = 256 * 1024
	MaxExtensionKeys        = 256
	MaxExtensionKeyBytes    = 256
	MaxExtensionValueBytes  = 4 * 1024 * 1024
	// Part content: align with typical HTTP request body caps (8 MiB) for text/JSON;
	// ref fields are path/URL length limits.
	MaxPartTextBytes  = 8 * 1024 * 1024
	MaxPartJSONBytes  = 8 * 1024 * 1024
	MaxRefStringBytes = 8 * 1024
	// MaxFileDataBytes bounds inline file payloads (pinned profile input_file
	// file_data base64). It matches the part text cap so a single inline file
	// cannot force unbounded allocations.
	MaxFileDataBytes = MaxPartTextBytes

	// Canonical streaming Event field caps (adapter/hook mutations).
	MaxEventDeltaBytes       = MaxPartTextBytes  // TextDelta, ReasoningDelta, ToolCallArgsDelta
	MaxEventDiagMessageBytes = 4 << 20           // WarningMessage, ErrorMessage per event
	MaxEventCodeFieldBytes   = MaxRefStringBytes // WarningCode, ErrorCode when non-empty

	MaxReasoningDialectBytes    = MaxRefStringBytes
	MaxReasoningTextBytes       = MaxPartTextBytes
	MaxReasoningSignatureBytes  = MaxRefStringBytes
	MaxReasoningOpaqueBytes     = MaxPartJSONBytes
	MaxReasoningPartsPerMessage = MaxPartsPerMessage
	MaxReasoningBytesPerCall    = MaxPartTextBytes

	MaxItems                  = 4_096
	MaxItemKindBytes          = 64
	MaxItemStatusBytes        = 64
	MaxAssistantPhaseBytes    = 64
	MaxItemReferenceIDBytes   = 512
	MaxCompactionDialectBytes = 8192
	// MaxCompactionEncryptedContentBytes bounds the provider compaction blob on a
	// compaction item (pinned profile encrypted_content). Aligned with the 8 MiB
	// part cap so a single opaque blob cannot force unbounded allocations.
	MaxCompactionEncryptedContentBytes = MaxPartJSONBytes
	MaxExtensionNamespaceBytes         = 256
	MaxExtensionTypeBytes              = 256
	MaxExtensionImplementorBytes       = 256
	MaxExtensionDirectionBytes         = 64
	MaxExtensionDataBytes              = 4 * 1024 * 1024
	MaxSemanticExtensions              = 64
	MaxSemanticExtensionDataBytes      = MaxExtensionDataBytes
	MaxContentPartsPerItem             = 2_048
	MaxJSONDepth                       = 64

	// Decision contract byte bounds. The JSON payload fields (evidence, instructions,
	// criteria, option and level descriptions) align with the part text cap so one
	// opaque decision payload cannot force unbounded allocations; question ids and
	// option names are short labels.
	MaxDecisionQuestionIDBytes        = 256
	MaxDecisionOptionNameBytes        = 256
	MaxDecisionEvidenceBytes          = MaxPartTextBytes
	MaxDecisionInstructionsBytes      = MaxPartTextBytes
	MaxDecisionCriteriaBytes          = MaxPartTextBytes
	MaxDecisionOptionDescriptionBytes = MaxPartTextBytes
	MaxDecisionLevelBytes             = MaxPartTextBytes

	// MaxAllowedToolRefs bounds the OpenResponses allowed_tools subset size to
	// the pinned wire schema maxLen (128 refs).
	MaxAllowedToolRefs = 128
)

func validateJSONDepth(data []byte, maxDepth int) error {
	depth := 0
	inString := false
	escaped := false
	for i := range data {
		b := data[i]
		if inString {
			if escaped {
				escaped = false
			} else if b == '\\' {
				escaped = true
			} else if b == '"' {
				inString = false
			}
			continue
		}
		switch b {
		case '"':
			inString = true
		case '{', '[':
			depth++
			if depth > maxDepth {
				return fmt.Errorf("exceeds maximum JSON depth of %d", maxDepth)
			}
		case '}', ']':
			depth--
			if depth < 0 {
				return fmt.Errorf("malformed JSON structure")
			}
		}
	}
	return nil
}

func validateStringField(name, s string, maxLen int) error {
	if maxLen <= 0 {
		return nil
	}
	if len(s) > maxLen {
		return &ValidationError{Field: name, Message: fmt.Sprintf("exceeds %d bytes", maxLen)}
	}
	return nil
}

func validateExactStringField(name, s string, maxLen int) error {
	if err := validateStringField(name, s, maxLen); err != nil {
		return err
	}
	if s != "" && s != strings.TrimSpace(s) {
		return &ValidationError{Field: name, Message: "must not contain leading or trailing whitespace"}
	}
	return nil
}

func fieldIndex(field string, i int) string {
	return field + "[" + strconv.Itoa(i) + "]"
}

func (c Call) validateEnvelopeSizes() error {
	if err := validateStringField("ID", c.ID, MaxCallIDBytes); err != nil {
		return err
	}
	if err := validateStringField("Route.Selector", c.Route.Selector, MaxRouteSelectorBytes); err != nil {
		return err
	}
	if err := validateStringField("Session.ClientSessionID", c.Session.ClientSessionID, MaxClientSessionIDBytes); err != nil {
		return err
	}
	if err := validateStringField("Session.ContinuityKey", c.Session.ContinuityKey, MaxContinuityKeyBytes); err != nil {
		return err
	}
	if err := validateStringField("Session.ALegID", c.Session.ALegID, MaxALegIDBytes); err != nil {
		return err
	}
	if err := validateStringField("Session.AuthoritativeSessionID", c.Session.AuthoritativeSessionID, MaxAuthoritativeSessionIDBytes); err != nil {
		return err
	}
	if err := validateStringField("Session.ResumeToken", c.Session.ResumeToken, MaxResumeTokenBytes); err != nil {
		return err
	}
	if err := validateStringField("StructuralCause", c.StructuralCause, MaxStructuralCauseBytes); err != nil {
		return err
	}
	if len(c.Messages) > MaxMessages {
		return &ValidationError{Field: "Messages", Message: fmt.Sprintf("at most %d messages", MaxMessages)}
	}
	if len(c.Items) > MaxItems {
		return &ValidationError{Field: "Items", Message: fmt.Sprintf("at most %d items", MaxItems)}
	}
	if len(c.Instructions) > MaxInstructionMessages {
		return &ValidationError{Field: "Instructions", Message: fmt.Sprintf("at most %d instruction messages", MaxInstructionMessages)}
	}
	if len(c.Tools) > MaxTools {
		return &ValidationError{Field: "Tools", Message: fmt.Sprintf("at most %d tools", MaxTools)}
	}
	if len(c.Extensions) > MaxExtensionKeys {
		return &ValidationError{Field: "Extensions", Message: fmt.Sprintf("at most %d extension entries", MaxExtensionKeys)}
	}
	if len(c.SemanticExtensions) > MaxSemanticExtensions {
		return &ValidationError{Field: "SemanticExtensions", Message: fmt.Sprintf("at most %d semantic extensions", MaxSemanticExtensions)}
	}
	seenSemanticExtensions := make(map[string]struct{}, len(c.SemanticExtensions))
	for i, ext := range c.SemanticExtensions {
		if err := ext.validate(fmt.Sprintf("SemanticExtensions[%d]", i)); err != nil {
			return err
		}
		key := ext.Namespace + "\x00" + ext.Type + "\x00" + ext.Implementor + "\x00" + ext.Direction
		if _, exists := seenSemanticExtensions[key]; exists {
			return &ValidationError{Field: fmt.Sprintf("SemanticExtensions[%d]", i), Message: "duplicate semantic extension identity"}
		}
		seenSemanticExtensions[key] = struct{}{}
	}
	for k, v := range c.Extensions {
		if err := validateStringField(fmt.Sprintf("Extensions[%q]", k), k, MaxExtensionKeyBytes); err != nil {
			return err
		}
		if len(v) > MaxExtensionValueBytes {
			return &ValidationError{Field: fmt.Sprintf("Extensions[%q]", k), Message: fmt.Sprintf("extension value exceeds %d bytes", MaxExtensionValueBytes)}
		}
	}
	for i, m := range c.Messages {
		if len(m.Parts) > MaxPartsPerMessage {
			return &ValidationError{Field: fmt.Sprintf("Messages[%d].Parts", i), Message: fmt.Sprintf("at most %d parts per message", MaxPartsPerMessage)}
		}
		for j, p := range m.Parts {
			if err := validatePartSizes(partPath("Messages", i, j), p); err != nil {
				return err
			}
		}
	}
	for i, m := range c.Instructions {
		if len(m.Parts) > MaxPartsPerMessage {
			return &ValidationError{Field: fmt.Sprintf("Instructions[%d].Parts", i), Message: fmt.Sprintf("at most %d parts per message", MaxPartsPerMessage)}
		}
		for j, p := range m.Parts {
			if err := validatePartSizes(partPath("Instructions", i, j), p); err != nil {
				return err
			}
		}
	}
	for i, t := range c.Tools {
		if err := validateExactStringFieldPath(elementPath("Tools", i, ".Name"), t.Name, MaxToolNameBytes); err != nil {
			return err
		}
		if err := validateStringFieldPath(elementPath("Tools", i, ".Description"), t.Description, MaxToolDescriptionBytes); err != nil {
			return err
		}
		if len(t.Parameters) > MaxToolParametersBytes {
			return &ValidationError{Field: elementPath("Tools", i, ".Parameters").String(), Message: fmt.Sprintf("exceeds %d bytes", MaxToolParametersBytes)}
		}
	}
	return nil
}

// callElementPath names a nested element of a Call envelope for diagnostics.
// Its label is rendered only from validation error paths: a valid request must
// not pay for building "Messages[3].Parts[17]" once per part, which dominated
// allocation counts for large agent histories on the request path.
type callElementPath struct {
	container string // "Messages", "Instructions", or "Tools"
	index     int    // element index within the container
	part      int    // part index, or -1 when the path names the element itself
	suffix    string // trailing field name, e.g. ".Text" or ".Name"
}

// elementPath names container[index] with a trailing field suffix.
func elementPath(container string, index int, suffix string) callElementPath {
	return callElementPath{container: container, index: index, part: -1, suffix: suffix}
}

// partPath names container[index].Parts[part]; the field suffix is applied with
// withSuffix inside the size checks.
func partPath(container string, index, part int) callElementPath {
	return callElementPath{container: container, index: index, part: part}
}

// withSuffix returns a copy of the path carrying a different trailing field.
func (p callElementPath) withSuffix(suffix string) callElementPath {
	p.suffix = suffix
	return p
}

// String renders the full field path. It runs only when a limit is violated.
func (p callElementPath) String() string {
	if p.part < 0 {
		return p.container + "[" + strconv.Itoa(p.index) + "]" + p.suffix
	}
	return p.container + "[" + strconv.Itoa(p.index) + "].Parts[" + strconv.Itoa(p.part) + "]" + p.suffix
}

// validateStringFieldPath is validateStringField for a call element path whose
// label is rendered only when the field exceeds its limit. The guard is the
// exact negation of validateStringField's own check, so the reported error is
// identical to one built from an eagerly rendered label.
func validateStringFieldPath(path callElementPath, s string, maxLen int) error {
	if maxLen <= 0 || len(s) <= maxLen {
		return nil
	}
	return validateStringField(path.String(), s, maxLen)
}

// validateExactStringFieldPath is validateExactStringField for a call element
// path whose label is rendered only when validation fails.
func validateExactStringFieldPath(path callElementPath, s string, maxLen int) error {
	if (maxLen <= 0 || len(s) <= maxLen) && (s == "" || s == strings.TrimSpace(s)) {
		return nil
	}
	return validateExactStringField(path.String(), s, maxLen)
}

func validatePartSizes(path callElementPath, p Part) error {
	switch p.Kind {
	case PartText:
		if len(p.Text) > MaxPartTextBytes {
			return &ValidationError{Field: path.withSuffix(".Text").String(), Message: fmt.Sprintf("text exceeds %d bytes", MaxPartTextBytes)}
		}
	case PartImageRef:
		if err := validateStringFieldPath(path.withSuffix(".ImageRef"), p.ImageRef, MaxRefStringBytes); err != nil {
			return err
		}
		if err := validateStringFieldPath(path.withSuffix(".ImageMIME"), p.ImageMIME, MaxRefStringBytes); err != nil {
			return err
		}
	case PartFileRef:
		if err := validateStringFieldPath(path.withSuffix(".FileRef"), p.FileRef, MaxRefStringBytes); err != nil {
			return err
		}
		if err := validateStringFieldPath(path.withSuffix(".FileMIME"), p.FileMIME, MaxRefStringBytes); err != nil {
			return err
		}
		if err := validateStringFieldPath(path.withSuffix(".FileName"), p.FileName, MaxRefStringBytes); err != nil {
			return err
		}
	case PartToolResult:
		if err := validateStringFieldPath(path.withSuffix(".ToolCallID"), p.ToolCallID, MaxRefStringBytes); err != nil {
			return err
		}
		if err := validateExactStringFieldPath(path.withSuffix(".ToolName"), p.ToolName, MaxToolNameBytes); err != nil {
			return err
		}
		if len(p.Text) > MaxPartTextBytes {
			return &ValidationError{Field: path.withSuffix(".Text").String(), Message: fmt.Sprintf("text exceeds %d bytes", MaxPartTextBytes)}
		}
		if len(p.Content) > MaxPartJSONBytes {
			return &ValidationError{Field: path.withSuffix(".Content").String(), Message: fmt.Sprintf("content exceeds %d bytes", MaxPartJSONBytes)}
		}
	case PartJSON:
		if len(p.Content) > MaxPartJSONBytes {
			return &ValidationError{Field: path.withSuffix(".Content").String(), Message: fmt.Sprintf("content exceeds %d bytes", MaxPartJSONBytes)}
		}
	case PartReasoning:
		if p.Reasoning != nil {
			if len(p.Reasoning.Dialect) > MaxReasoningDialectBytes {
				return &ValidationError{Field: path.withSuffix(".Reasoning.Dialect").String(), Message: fmt.Sprintf("dialect exceeds %d bytes", MaxReasoningDialectBytes)}
			}
			if len(p.Reasoning.Text) > MaxReasoningTextBytes {
				return &ValidationError{Field: path.withSuffix(".Reasoning.Text").String(), Message: fmt.Sprintf("text exceeds %d bytes", MaxReasoningTextBytes)}
			}
			if len(p.Reasoning.Signature) > MaxReasoningSignatureBytes {
				return &ValidationError{Field: path.withSuffix(".Reasoning.Signature").String(), Message: fmt.Sprintf("signature exceeds %d bytes", MaxReasoningSignatureBytes)}
			}
			if len(p.Reasoning.Opaque) > MaxReasoningOpaqueBytes {
				return &ValidationError{Field: path.withSuffix(".Reasoning.Opaque").String(), Message: fmt.Sprintf("opaque exceeds %d bytes", MaxReasoningOpaqueBytes)}
			}
		}
	case "":
		// validated in validate()
	default:
	}
	if p.ToolCallID != "" && len(p.ToolCallID) > MaxRefStringBytes {
		return &ValidationError{Field: path.withSuffix(".ToolCallID").String(), Message: fmt.Sprintf("exceeds %d bytes", MaxRefStringBytes)}
	}
	if p.ToolName != "" && len(p.ToolName) > MaxRefStringBytes {
		return &ValidationError{Field: path.withSuffix(".ToolName").String(), Message: fmt.Sprintf("exceeds %d bytes", MaxRefStringBytes)}
	}
	return nil
}

// ReasoningEffort / MIME strings and similar option strings.
const MaxOptionStringBytes = 4 * 1024

func validateOptionStrings(o GenerationOptions) error {
	if o.ReasoningEffort != "" && len(o.ReasoningEffort) > MaxOptionStringBytes {
		return &ValidationError{Field: "Options.ReasoningEffort", Message: fmt.Sprintf("exceeds %d bytes", MaxOptionStringBytes)}
	}
	if !o.Verbosity.valid() {
		return &ValidationError{Field: "Options.Verbosity", Message: fmt.Sprintf("must be one of %q, %q, or %q", VerbosityLow, VerbosityMedium, VerbosityHigh)}
	}
	if o.ResponseMIMEType != "" && len(o.ResponseMIMEType) > MaxOptionStringBytes {
		return &ValidationError{Field: "Options.ResponseMIMEType", Message: fmt.Sprintf("exceeds %d bytes", MaxOptionStringBytes)}
	}
	return nil
}
