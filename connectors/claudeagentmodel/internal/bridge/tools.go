package bridge

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const (
	// MCPPrefix is the model-visible namespace of the transient SDK MCP server.
	// The sidecar registers the short name and the pinned CLI prefixes it with
	// the server name at the model boundary.
	MCPPrefix = "mcp__aiproxer__"
	// MCPServerName is the connector-owned MCP server name. It is never
	// configurable and never client-supplied.
	MCPServerName = "aiproxer"
	// MaxNativeToolNameBytes is the model-visible tool name bound. The pinned
	// SDK and Claude tool API reject longer names.
	MaxNativeToolNameBytes = 64
	// MaxCatalogTools bounds one admitted catalogue below the canonical request
	// bound, because every admitted tool becomes a public MCP definition.
	MaxCatalogTools = min(aipapi.MaxTools, 128)
	// MaxCatalogBytes bounds the total name/description/schema bytes so one start
	// frame stays inside the private transport bound together with the maximum
	// prompt and system text.
	MaxCatalogBytes = 1 << 20
	nameHashBytes   = 10
)

var (
	errToolCatalog         = errors.New("claudeagentmodel: invalid tool catalogue")
	errToolCatalogStateful = errors.New("claudeagentmodel: tool catalogue is not executable")
)

// ToolSpec is one admitted caller tool with its reversible native mapping. The
// original name, description and schema bytes are never rewritten.
type ToolSpec struct {
	// Name is the caller's original tool name; it is what canonical tool-call
	// events and the client wire carry.
	Name string `json:"name"`
	// MCPName is the short name published by the transient MCP server.
	MCPName string `json:"mcp_name"`
	// NativeName is the model-visible mcp__aiproxer__<short> name.
	NativeName  string          `json:"native_name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

// Catalog is one deterministic, bounded tool snapshot for a single SDK step.
type Catalog struct {
	specs    []ToolSpec
	byMCP    map[string]ToolSpec
	byNative map[string]ToolSpec
	digest   [32]byte
}

// EmptyCatalog reports a catalogue that exposes no tools.
func EmptyCatalog() *Catalog { return &Catalog{} }

// Len reports the number of exposed tools.
func (c *Catalog) Len() int {
	if c == nil {
		return 0
	}
	return len(c.specs)
}

// Specs returns the admitted specs in request order.
func (c *Catalog) Specs() []ToolSpec {
	if c == nil {
		return nil
	}
	return append([]ToolSpec(nil), c.specs...)
}

// Digest identifies the exact original names, descriptions and schemas.
func (c *Catalog) Digest() [32]byte {
	if c == nil {
		return [32]byte{}
	}
	return c.digest
}

// ByMCP resolves the short name the MCP handler receives back to its spec.
func (c *Catalog) ByMCP(name string) (ToolSpec, bool) {
	if c == nil {
		return ToolSpec{}, false
	}
	spec, ok := c.byMCP[name]
	return spec, ok
}

// ByNative resolves a model-visible tool name back to its spec.
func (c *Catalog) ByNative(name string) (ToolSpec, bool) {
	if c == nil {
		return ToolSpec{}, false
	}
	spec, ok := c.byNative[name]
	return spec, ok
}

// BuildCatalog validates one canonical tool catalogue and derives the reversible
// native names. It rejects the whole catalogue before any upstream I/O on a
// duplicate, oversized or unrepresentable definition; it never repairs a schema.
//
// ToolChoice:none exposes no tools. A forced or required choice cannot be
// enforced by the pinned SDK MCP path, so it produces an observable downgrade or
// a strict reject instead of a false enforcement claim.
func BuildCatalog(tools []aipapi.ToolDef, choice aipapi.ToolChoice, strict bool) (*Catalog, []aipapi.Event, error) {
	catalog := EmptyCatalog()
	if choice.Mode == aipapi.ToolChoiceNone {
		return catalog, nil, nil
	}
	warnings := make([]aipapi.Event, 0, 2)
	if forcedToolChoice(choice) {
		warnings = append(warnings, aipapi.Event{
			Kind:           aipapi.EventWarning,
			WarningCode:    "tool_choice_degraded",
			WarningMessage: "the pinned SDK MCP path cannot enforce a forced or required tool choice; the admitted tools are exposed with automatic choice",
		})
	}
	if len(choice.AllowedTools) > 0 {
		warnings = append(warnings, aipapi.Event{
			Kind:           aipapi.EventWarning,
			WarningCode:    "tool_choice_allowed_subset_degraded",
			WarningMessage: "the pinned SDK MCP path cannot restrict the model to a tool-choice subset; every admitted tool stays invocable",
		})
	}
	if strict && len(warnings) > 0 {
		return nil, nil, fmt.Errorf("%w: requested tool choice cannot be represented faithfully in strict mode", aipapi.ErrCapabilityReject)
	}
	if len(tools) > MaxCatalogTools {
		return nil, nil, fmt.Errorf("%w: catalogue exceeds %d tools", errToolCatalog, MaxCatalogTools)
	}
	catalogBytes := 0
	for _, tool := range tools {
		catalogBytes += len(tool.Name) + len(tool.Description) + len(tool.Parameters)
	}
	if catalogBytes > MaxCatalogBytes {
		return nil, nil, fmt.Errorf("%w: catalogue exceeds %d bytes", errToolCatalog, MaxCatalogBytes)
	}
	if len(tools) == 0 {
		return catalog, warnings, nil
	}
	seenName := make(map[string]struct{}, len(tools))
	specs := make([]ToolSpec, 0, len(tools))
	byMCP := make(map[string]ToolSpec, len(tools))
	byNative := make(map[string]ToolSpec, len(tools))
	for i, tool := range tools {
		if err := validateToolDef(tool); err != nil {
			return nil, nil, fmt.Errorf("tools[%d]: %w", i, err)
		}
		if _, dup := seenName[tool.Name]; dup {
			return nil, nil, fmt.Errorf("%w: duplicate original tool name %q", errToolCatalog, tool.Name)
		}
		seenName[tool.Name] = struct{}{}
		mcpName := shortToolName(tool.Name)
		native := MCPPrefix + mcpName
		if len(native) > MaxNativeToolNameBytes {
			return nil, nil, fmt.Errorf("%w: native name for %q exceeds %d bytes", errToolCatalog, tool.Name, MaxNativeToolNameBytes)
		}
		if other, collision := byMCP[mcpName]; collision {
			return nil, nil, fmt.Errorf("%w: tools %q and %q share the native name %q", errToolCatalog, other.Name, tool.Name, native)
		}
		if other, collision := byNative[native]; collision {
			return nil, nil, fmt.Errorf("%w: tools %q and %q share the native name %q", errToolCatalog, other.Name, tool.Name, native)
		}
		spec := ToolSpec{
			Name:        tool.Name,
			MCPName:     mcpName,
			NativeName:  native,
			Description: tool.Description,
			Parameters:  append(json.RawMessage(nil), tool.Parameters...),
		}
		specs = append(specs, spec)
		byMCP[mcpName] = spec
		byNative[native] = spec
	}
	catalog.specs = specs
	catalog.byMCP = byMCP
	catalog.byNative = byNative
	catalog.digest = catalogDigest(specs)
	return catalog, warnings, nil
}

func forcedToolChoice(choice aipapi.ToolChoice) bool {
	switch choice.Mode {
	case aipapi.ToolChoiceAny, aipapi.ToolChoiceRequired:
		return true
	}
	return strings.TrimSpace(choice.Name) != ""
}

func validateToolDef(tool aipapi.ToolDef) error {
	name := tool.Name
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("%w: tool name is required", errToolCatalog)
	}
	if len(name) > aipapi.MaxToolNameBytes {
		return fmt.Errorf("%w: tool name exceeds %d bytes", errToolCatalog, aipapi.MaxToolNameBytes)
	}
	if !utf8.ValidString(name) {
		return fmt.Errorf("%w: tool name is not valid UTF-8", errToolCatalog)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%w: tool name contains a control character", errToolCatalog)
		}
	}
	if len(tool.Description) > aipapi.MaxToolDescriptionBytes {
		return fmt.Errorf("%w: description for %q exceeds %d bytes", errToolCatalog, name, aipapi.MaxToolDescriptionBytes)
	}
	if !utf8.ValidString(tool.Description) {
		return fmt.Errorf("%w: description for %q is not valid UTF-8", errToolCatalog, name)
	}
	params := bytes.TrimSpace(tool.Parameters)
	if len(params) == 0 {
		return fmt.Errorf("%w: parameters for %q are required", errToolCatalog, name)
	}
	if len(tool.Parameters) > aipapi.MaxToolParametersBytes {
		return fmt.Errorf("%w: parameters for %q exceed %d bytes", errToolCatalog, name, aipapi.MaxToolParametersBytes)
	}
	if !utf8.Valid(tool.Parameters) {
		return fmt.Errorf("%w: parameters for %q are not valid UTF-8", errToolCatalog, name)
	}
	var probe any
	if err := json.Unmarshal(params, &probe); err != nil {
		return fmt.Errorf("%w: parameters for %q are not valid JSON", errToolCatalog, name)
	}
	schema, ok := probe.(map[string]any)
	if !ok {
		return fmt.Errorf("%w: parameters for %q must be a JSON object schema", errToolCatalog, name)
	}
	if err := validateSchemaShape(schema); err != nil {
		return fmt.Errorf("%w: parameters for %q: %v", errToolCatalog, name, err)
	}
	return nil
}

// validateSchemaShape admits one coherent JSON Schema dialect. The root must be
// an object schema because the transient MCP server publishes it as the tool
// input schema, and the installed MCP client rejects a non-object root. The
// admitted vocabulary is validated recursively; unsupported keywords and $ref
// are refused explicitly rather than passed through unvalidated. Validation never
// rewrites the schema.
func validateSchemaShape(schema map[string]any) error {
	if raw, ok := schema["type"]; !ok {
		return errors.New(`parameters must declare "type":"object" at the schema root`)
	} else if name, ok := raw.(string); !ok || name != "object" {
		return errors.New(`parameters must declare "type":"object" at the schema root`)
	}
	return validateSchema(schema, 0)
}

const maxSchemaDepth = 32

func validateSchema(schema map[string]any, depth int) error {
	if depth > maxSchemaDepth {
		return errors.New("schema nesting exceeds the supported depth")
	}
	for keyword, value := range schema {
		switch keyword {
		case "type":
			if err := validateSchemaType(value); err != nil {
				return err
			}
		case "required":
			if err := validateStringList(value, true); err != nil {
				return fmt.Errorf("required: %w", err)
			}
		case "enum":
			if _, ok := value.([]any); !ok {
				return errors.New("enum must be a list")
			}
		case "const", "default":
			// Any JSON value is a valid constant/default annotation.
		case "examples":
			if _, ok := value.([]any); !ok {
				return errors.New("examples must be a list")
			}
		case "$ref":
			return errors.New("schema $ref is not supported by this connector slice")
		case "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum":
			if !isJSONNumber(value) {
				return fmt.Errorf("%s must be a number", keyword)
			}
		case "multipleOf":
			number, ok := schemaNumber(value)
			if !ok || number <= 0 {
				return errors.New("multipleOf must be a number strictly greater than zero")
			}
		case "minLength", "maxLength", "minItems", "maxItems", "minProperties", "maxProperties":
			count, ok := schemaInteger(value)
			if !ok || count < 0 {
				return fmt.Errorf("%s must be a non-negative integer", keyword)
			}
		case "uniqueItems", "deprecated", "readOnly", "writeOnly":
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("%s must be a boolean", keyword)
			}
		case "pattern":
			// ECMAScript regex semantics cannot be validated faithfully with Go's
			// RE2 parser (which rejects valid lookahead), so this slice refuses the
			// keyword instead of half-validating it. The schema is not rewritten.
			return errors.New(`schema keyword "pattern" is not supported by this connector slice (ECMAScript regex validation is unavailable)`)
		case "patternProperties":
			// Same reason: the property keys are ECMAScript regexes.
			return errors.New(`schema keyword "patternProperties" is not supported by this connector slice (ECMAScript regex validation is unavailable)`)
		case "description", "title", "format", "$comment", "$schema", "$id", "$anchor", "contentEncoding", "contentMediaType":
			if _, ok := value.(string); !ok {
				return fmt.Errorf("%s must be a string", keyword)
			}
		case "properties", "dependentSchemas":
			if err := validateSchemaMap(value, depth); err != nil {
				return fmt.Errorf("%s: %w", keyword, err)
			}
		case "$defs", "definitions":
			if err := validateSchemaMap(value, depth); err != nil {
				return fmt.Errorf("%s: %w", keyword, err)
			}
		case "dependentRequired":
			mapping, ok := value.(map[string]any)
			if !ok {
				return errors.New("dependentRequired must be an object of string lists")
			}
			for key, entry := range mapping {
				if err := validateStringList(entry, true); err != nil {
					return fmt.Errorf("dependentRequired.%s: %w", key, err)
				}
			}
		case "dependencies":
			mapping, ok := value.(map[string]any)
			if !ok {
				return errors.New("dependencies must be an object")
			}
			for key, entry := range mapping {
				switch typed := entry.(type) {
				case []any:
					if err := validateStringList(typed, true); err != nil {
						return fmt.Errorf("dependencies.%s: %w", key, err)
					}
				case map[string]any:
					if err := validateSchema(typed, depth+1); err != nil {
						return fmt.Errorf("dependencies.%s: %w", key, err)
					}
				default:
					return fmt.Errorf("dependencies.%s must be a list of property names or a schema object", key)
				}
			}
		case "additionalProperties", "additionalItems", "unevaluatedProperties", "unevaluatedItems":
			switch typed := value.(type) {
			case bool:
			case map[string]any:
				if err := validateSchema(typed, depth+1); err != nil {
					return fmt.Errorf("%s: %w", keyword, err)
				}
			default:
				return fmt.Errorf("%s must be a boolean or a schema object", keyword)
			}
		case "items":
			switch typed := value.(type) {
			case map[string]any:
				if err := validateSchema(typed, depth+1); err != nil {
					return fmt.Errorf("items: %w", err)
				}
			case []any:
				if err := validateSchemaList(typed, depth, "items"); err != nil {
					return err
				}
			default:
				return errors.New("items must be a schema object or a list of schema objects")
			}
		case "prefixItems":
			list, ok := value.([]any)
			if !ok {
				return errors.New("prefixItems must be a list of schema objects")
			}
			if err := validateSchemaList(list, depth, "prefixItems"); err != nil {
				return err
			}
		case "contains", "propertyNames", "not", "if", "then", "else":
			nested, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("%s must be a schema object", keyword)
			}
			if err := validateSchema(nested, depth+1); err != nil {
				return fmt.Errorf("%s: %w", keyword, err)
			}
		case "anyOf", "oneOf", "allOf":
			list, ok := value.([]any)
			if !ok || len(list) == 0 {
				return fmt.Errorf("%s must be a non-empty list of schema objects", keyword)
			}
			if err := validateSchemaList(list, depth, keyword); err != nil {
				return err
			}
		default:
			return fmt.Errorf("schema keyword %q is not supported by this connector slice", keyword)
		}
	}
	return nil
}

func validateSchemaType(value any) error {
	switch typed := value.(type) {
	case string:
		if !isJSONSchemaType(typed) {
			return fmt.Errorf("type %q is not a JSON Schema type", typed)
		}
		return nil
	case []any:
		if len(typed) == 0 {
			return errors.New("type must not be an empty list")
		}
		seen := make(map[string]struct{}, len(typed))
		for _, entry := range typed {
			name, ok := entry.(string)
			if !ok || !isJSONSchemaType(name) {
				return errors.New("type must be a JSON Schema type name or list of names")
			}
			if _, duplicate := seen[name]; duplicate {
				return fmt.Errorf("type list contains duplicate %q", name)
			}
			seen[name] = struct{}{}
		}
		return nil
	default:
		return errors.New("type must be a JSON Schema type name or list of names")
	}
}

func validateStringList(value any, allowEmpty bool) error {
	list, ok := value.([]any)
	if !ok {
		return errors.New("must be a list of strings")
	}
	if len(list) == 0 && !allowEmpty {
		return errors.New("must not be empty")
	}
	seen := make(map[string]struct{}, len(list))
	for _, entry := range list {
		name, ok := entry.(string)
		if !ok {
			return errors.New("must be a list of strings")
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("duplicate entry %q", name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

func validateSchemaMap(value any, depth int) error {
	mapping, ok := value.(map[string]any)
	if !ok {
		return errors.New("must be an object of schema objects")
	}
	for key, entry := range mapping {
		nested, ok := entry.(map[string]any)
		if !ok {
			return fmt.Errorf("%q must be a schema object", key)
		}
		if err := validateSchema(nested, depth+1); err != nil {
			return fmt.Errorf("%q: %w", key, err)
		}
	}
	return nil
}

func validateSchemaList(list []any, depth int, keyword string) error {
	for i, entry := range list {
		nested, ok := entry.(map[string]any)
		if !ok {
			return fmt.Errorf("%s[%d] must be a schema object", keyword, i)
		}
		if err := validateSchema(nested, depth+1); err != nil {
			return fmt.Errorf("%s[%d]: %w", keyword, i, err)
		}
	}
	return nil
}

func schemaNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		return parsed, true
	}
	return 0, false
}

func isJSONNumber(value any) bool {
	switch value.(type) {
	case float64, float32, int, int64, json.Number:
		return true
	}
	return false
}

func schemaInteger(value any) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		if typed != float64(int64(typed)) {
			return 0, false
		}
		return int64(typed), true
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, false
		}
		return parsed, true
	}
	return 0, false
}

func isJSONSchemaType(value string) bool {
	switch value {
	case "object", "array", "string", "number", "integer", "boolean", "null":
		return true
	}
	return false
}

// shortToolName derives the deterministic MCP tool name from the original. A
// reversible name keeps the original only when every character is MCP-safe and
// the model-visible name fits the protocol bound; otherwise invalid characters
// become "_", the value is truncated and suffixed with an underscore plus ten
// hex characters of the SHA-256 of the full original UTF-8 name.
func shortToolName(original string) string {
	maxShort := MaxNativeToolNameBytes - len(MCPPrefix)
	if isMCPName(original) && len(original) <= maxShort {
		return original
	}
	normalized := normalizeMCPName(original)
	sum := sha256.Sum256([]byte(original))
	suffix := hex.EncodeToString(sum[:])[:nameHashBytes]
	keep := maxShort - 1 - nameHashBytes
	keep = max(keep, 1)
	if len(normalized) > keep {
		normalized = normalized[:keep]
	}
	if normalized == "" {
		normalized = "tool"
	}
	return normalized + "_" + suffix
}

func isMCPName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}

func normalizeMCPName(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

func catalogDigest(specs []ToolSpec) [32]byte {
	h := sha256.New()
	for _, spec := range specs {
		writeDigestField(h, spec.Name)
		writeDigestField(h, spec.Description)
		writeDigestField(h, string(spec.Parameters))
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}
