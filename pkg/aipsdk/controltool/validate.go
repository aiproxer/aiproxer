package controltool

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Bounds are measured in UTF-8 encoded bytes. DefaultMaxArgsBytes reuses the
// existing safe tool-call argument envelope (64 KiB).
const (
	MaxProviderIDBytes  = 128
	MaxIdentifierBytes  = 256
	MaxReasonCodeBytes  = 64
	MaxInstructionBytes = 64 * 1024
	MaxResultTextBytes  = 64 * 1024
	DefaultMaxArgsBytes = 64 * 1024
)

// ValidateProviderID checks the stable provider identity. The check is pure;
// callers should obtain ID once while composing a provider and retain that
// value for the generation lifetime.
func ValidateProviderID(id string) error {
	return validateString(ErrInvalidProvider, "provider identity", id, MaxProviderIDBytes, true)
}

// ProviderIdentity validates a provider's stable identity and returns the
// bounded value used by generation composition. Typed-nil providers and
// provider identity panics fail closed as invalid providers.
func ProviderIdentity(provider Provider) (id string, err error) {
	if isNilProvider(provider) {
		return "", ErrInvalidProvider
	}
	defer func() {
		if recover() != nil {
			id = ""
			err = fmt.Errorf("%w: provider identity unavailable", ErrInvalidProvider)
		}
	}()
	id = provider.ID()
	if err := ValidateProviderID(id); err != nil {
		return "", err
	}
	return id, nil
}

// ValidateProvider checks identity and the frozen Spec. Typed-nil providers
// and Spec panics fail closed.
func ValidateProvider(provider Provider) (err error) {
	if _, err := ProviderIdentity(provider); err != nil {
		return err
	}
	defer func() {
		if recover() != nil {
			err = fmt.Errorf("%w: provider spec unavailable", ErrInvalidProvider)
		}
	}()
	return provider.Spec().Validate()
}

func isNilProvider(provider Provider) bool {
	if provider == nil {
		return true
	}
	value := reflect.ValueOf(provider)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// Validate checks the frozen tool, instruction, and args budget.
func (s Spec) Validate() error { return ValidateSpec(s) }

// ValidateSpec checks the frozen tool, instruction, and args budget.
func ValidateSpec(s Spec) error {
	if err := validateToolDef(s.Tool); err != nil {
		return err
	}
	if err := s.Instruction.Validate(); err != nil {
		return err
	}
	if s.MaxArgsBytes <= 0 {
		return fmt.Errorf("%w: args budget is required", ErrInvalidSpec)
	}
	if s.MaxArgsBytes > DefaultMaxArgsBytes {
		return fmt.Errorf("%w: args budget exceeds %d bytes", ErrInvalidSpec, DefaultMaxArgsBytes)
	}
	return nil
}

func validateToolDef(tool aipapi.ToolDef) error {
	if err := validateExactName(ErrInvalidSpec, "tool name", tool.Name, aipapi.MaxToolNameBytes); err != nil {
		return err
	}
	if err := validateString(ErrInvalidSpec, "tool description", tool.Description, aipapi.MaxToolDescriptionBytes, false); err != nil {
		return err
	}
	return validateToolSchema(tool.Parameters)
}

func validateToolSchema(raw json.RawMessage) error {
	if len(raw) == 0 {
		return fmt.Errorf("%w: tool parameters are required", ErrInvalidSpec)
	}
	if len(raw) > aipapi.MaxToolParametersBytes {
		return fmt.Errorf("%w: tool parameters exceed %d bytes", ErrInvalidSpec, aipapi.MaxToolParametersBytes)
	}
	if !json.Valid(raw) {
		return fmt.Errorf("%w: tool parameters are not valid JSON", ErrInvalidSpec)
	}
	if err := checkJSONDepth(raw, aipapi.MaxJSONDepth); err != nil {
		return fmt.Errorf("%w: tool parameters %v", ErrInvalidSpec, err)
	}
	obj, err := decodeSingleObject(raw)
	if err != nil {
		return fmt.Errorf("%w: tool parameters must be a JSON object schema", ErrInvalidSpec)
	}
	if schemaHasRef(obj) {
		return fmt.Errorf("%w: tool parameters must not use $ref", ErrInvalidSpec)
	}
	if t, ok := obj["type"]; ok && !validJSONSchemaType(t) {
		return fmt.Errorf("%w: tool parameters schema type is invalid", ErrInvalidSpec)
	}
	if !isObjectSchema(obj) {
		return fmt.Errorf("%w: tool parameters must be an object JSON Schema", ErrInvalidSpec)
	}
	if props, ok := obj["properties"]; ok {
		if _, isMap := props.(map[string]any); !isMap {
			return fmt.Errorf("%w: tool parameters properties must be an object", ErrInvalidSpec)
		}
	}
	return nil
}

func decodeSingleObject(raw []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("not an object")
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("trailing json")
	}
	return obj, nil
}

func schemaHasRef(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		if _, ok := typed["$ref"]; ok {
			return true
		}
		for _, child := range typed {
			if schemaHasRef(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if schemaHasRef(child) {
				return true
			}
		}
	}
	return false
}

func validJSONSchemaType(value any) bool {
	switch typed := value.(type) {
	case string:
		return isKnownJSONSchemaType(typed)
	case []any:
		if len(typed) == 0 {
			return false
		}
		for _, item := range typed {
			name, ok := item.(string)
			if !ok || !isKnownJSONSchemaType(name) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func isKnownJSONSchemaType(name string) bool {
	switch name {
	case "null", "boolean", "object", "array", "number", "string", "integer":
		return true
	default:
		return false
	}
}

func schemaTypeIncludes(value any, want string) bool {
	switch typed := value.(type) {
	case string:
		return typed == want
	case []any:
		for _, item := range typed {
			if name, ok := item.(string); ok && name == want {
				return true
			}
		}
	}
	return false
}

func isObjectSchema(node map[string]any) bool {
	if t, ok := node["type"]; ok {
		return schemaTypeIncludes(t, "object")
	}
	props, ok := node["properties"].(map[string]any)
	return ok && len(props) > 0
}

func checkJSONDepth(data []byte, maxDepth int) error {
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
				return fmt.Errorf("exceed %d JSON depth", maxDepth)
			}
		case '}', ']':
			depth--
			if depth < 0 {
				return fmt.Errorf("are malformed")
			}
		}
	}
	return nil
}

// Validate checks the instruction role and bounded text.
func (in Instruction) Validate() error { return ValidateInstruction(in) }

// ValidateInstruction checks the instruction role and bounded text.
func ValidateInstruction(in Instruction) error {
	switch in.Role {
	case aipapi.RoleSystem, aipapi.RoleDeveloper:
	default:
		return fmt.Errorf("%w: instruction role is not allowed", ErrInvalidSpec)
	}
	if err := validateString(ErrInvalidSpec, "instruction text", in.Text, MaxInstructionBytes, true); err != nil {
		return err
	}
	if containsNUL(in.Text) {
		return fmt.Errorf("%w: instruction text must not contain NUL", ErrInvalidSpec)
	}
	return nil
}

// Validate checks call identity and the supplied args budget.
func (c CompletedCall) Validate(maxArgsBytes int) error {
	return ValidateCompletedCall(c, maxArgsBytes)
}

// ValidateCompletedCall checks call identity and the supplied args budget.
func ValidateCompletedCall(c CompletedCall, maxArgsBytes int) error {
	if maxArgsBytes <= 0 {
		return fmt.Errorf("%w: args budget is required", ErrInvalidCall)
	}
	if err := validateString(ErrInvalidCall, "tool call id", c.ToolCallID, MaxIdentifierBytes, true); err != nil {
		return err
	}
	if err := validateExactName(ErrInvalidCall, "tool name", c.ToolName, aipapi.MaxToolNameBytes); err != nil {
		return err
	}
	if len(c.ArgsJSON) > maxArgsBytes {
		return fmt.Errorf("%w: arguments exceed %d bytes", ErrInvalidCall, maxArgsBytes)
	}
	return nil
}

// Validate checks bounded provenance identifiers.
func (m Meta) Validate() error { return ValidateMeta(m) }

// ValidateMeta checks bounded provenance identifiers.
func ValidateMeta(m Meta) error {
	if err := validateString(ErrInvalidMeta, "trace id", m.TraceID, MaxIdentifierBytes, false); err != nil {
		return err
	}
	if err := validateString(ErrInvalidMeta, "a-leg id", m.ALegID, MaxIdentifierBytes, false); err != nil {
		return err
	}
	if err := validateString(ErrInvalidMeta, "b-leg id", m.BLegID, MaxIdentifierBytes, false); err != nil {
		return err
	}
	if err := validateString(ErrInvalidMeta, "candidate key", m.CandidateKey, MaxIdentifierBytes, false); err != nil {
		return err
	}
	if m.AttemptSeq < 0 {
		return fmt.Errorf("%w: attempt sequence must be non-negative", ErrInvalidMeta)
	}
	return nil
}

// Validate checks outcome kind, reason, and result-text invariants.
func (o Outcome) Validate() error { return ValidateOutcome(o) }

// ValidateOutcome checks outcome kind, reason, and result-text invariants.
func ValidateOutcome(o Outcome) error {
	if !o.Kind.IsKnown() {
		return fmt.Errorf("%w: unknown outcome kind", ErrInvalidOutcome)
	}
	if err := validateReasonCode(o.ReasonCode); err != nil {
		return err
	}
	switch o.Kind {
	case OutcomeInvalid:
		if o.ResultText != "" {
			return fmt.Errorf("%w: invalid outcome cannot carry client output", ErrInvalidOutcome)
		}
		return nil
	case OutcomeComplete:
		if err := validateString(ErrInvalidOutcome, "result text", o.ResultText, MaxResultTextBytes, true); err != nil {
			return err
		}
		if containsNUL(o.ResultText) {
			return fmt.Errorf("%w: result text must not contain NUL", ErrInvalidOutcome)
		}
		return nil
	default:
		return fmt.Errorf("%w: unknown outcome kind", ErrInvalidOutcome)
	}
}

func validateReasonCode(code string) error {
	if strings.TrimSpace(code) == "" {
		return fmt.Errorf("%w: reason code is required", ErrInvalidOutcome)
	}
	if len(code) > MaxReasonCodeBytes {
		return fmt.Errorf("%w: reason code exceeds %d bytes", ErrInvalidOutcome, MaxReasonCodeBytes)
	}
	for _, ch := range code {
		if ch > unicode.MaxASCII {
			return fmt.Errorf("%w: reason code must be ascii", ErrInvalidOutcome)
		}
		if ch != '_' && ch != '-' && ch != '.' && (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') {
			return fmt.Errorf("%w: reason code is not content-free", ErrInvalidOutcome)
		}
	}
	return nil
}

func validateExactName(sentinel error, field, value string, maxBytes int) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: %s is required", sentinel, field)
	}
	if value != strings.TrimSpace(value) {
		return fmt.Errorf("%w: %s must not contain leading or trailing whitespace", sentinel, field)
	}
	return validateString(sentinel, field, value, maxBytes, true)
}

func validateString(sentinel error, field, value string, maxBytes int, required bool) error {
	if required && strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: %s is required", sentinel, field)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%w: %s is not valid UTF-8", sentinel, field)
	}
	if len(value) > maxBytes {
		return fmt.Errorf("%w: %s exceeds %d bytes", sentinel, field, maxBytes)
	}
	return nil
}

func containsNUL(value string) bool {
	return strings.ContainsRune(value, 0)
}
