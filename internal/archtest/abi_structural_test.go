package archtest

import (
	"fmt"
	"strconv"
	"strings"
)

// LegacyProtoField is the exact v1.3 exception vocabulary.
type LegacyProtoField struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Number  int    `json:"number"`
	Type    string `json:"type"`
	Label   string `json:"label"`
	Options string `json:"options"`
}

var legacyProtoFields = map[string]LegacyProtoField{
	"Invocation.prompt_cache_key":                       {Path: "Invocation", Name: "prompt_cache_key", Number: 19, Type: "string"},
	"InvocationReasoningItem.summary":                   {Path: "InvocationReasoningItem", Name: "summary", Number: 5, Type: "RawJSONValue"},
	"InvocationReasoningItem.content":                   {Path: "InvocationReasoningItem", Name: "content", Number: 6, Type: "RawJSONValue"},
	"InvocationReasoningItem.encrypted_content":         {Path: "InvocationReasoningItem", Name: "encrypted_content", Number: 7, Type: "RawJSONValue"},
	"InvocationCompactionItem.encrypted_content":        {Path: "InvocationCompactionItem", Name: "encrypted_content", Number: 5, Type: "string", Label: "optional"},
	"InvocationContentPart.file_data":                   {Path: "InvocationContentPart", Name: "file_data", Number: 19, Type: "string", Label: "optional"},
	"InvocationContentPart.extension_type":              {Path: "InvocationContentPart", Name: "extension_type", Number: 20, Type: "string", Label: "optional"},
	"InvocationContentPart.extension_data":              {Path: "InvocationContentPart", Name: "extension_data", Number: 21, Type: "RawJSONValue"},
	"InvocationContentPart.reasoning_summary":           {Path: "InvocationContentPart", Name: "reasoning_summary", Number: 22, Type: "RawJSONValue"},
	"InvocationContentPart.reasoning_content":           {Path: "InvocationContentPart", Name: "reasoning_content", Number: 23, Type: "RawJSONValue"},
	"InvocationContentPart.reasoning_encrypted_content": {Path: "InvocationContentPart", Name: "reasoning_encrypted_content", Number: 24, Type: "RawJSONValue"},
	"InvocationContentPart.extension_namespace":         {Path: "InvocationContentPart", Name: "extension_namespace", Number: 25, Type: "string", Label: "optional"},
	"InvocationContentPart.extension_implementor":       {Path: "InvocationContentPart", Name: "extension_implementor", Number: 26, Type: "string", Label: "optional"},
	"Part.reasoning_summary":                            {Path: "Part", Name: "reasoning_summary", Number: 11, Type: "RawJSONValue"},
	"Part.reasoning_content":                            {Path: "Part", Name: "reasoning_content", Number: 12, Type: "RawJSONValue"},
	"Part.reasoning_encrypted_content":                  {Path: "Part", Name: "reasoning_encrypted_content", Number: 13, Type: "RawJSONValue"},
	"CanonicalEvent.reasoning_summary":                  {Path: "CanonicalEvent", Name: "reasoning_summary", Number: 15, Type: "RawJSONValue"},
	"CanonicalEvent.reasoning_content":                  {Path: "CanonicalEvent", Name: "reasoning_content", Number: 16, Type: "RawJSONValue"},
	"CanonicalEvent.reasoning_encrypted_content":        {Path: "CanonicalEvent", Name: "reasoning_encrypted_content", Number: 17, Type: "RawJSONValue"},
}

// economicV2ProtoMessages are the approved additive host-only message family
// introduced after the frozen v1.3 protocol ABI. Their provider identity field
// names must not be mistaken for provider-specific protocol extensions.
var economicV2ProtoMessages = map[string]struct{}{
	"AccountingEvidenceV2": {}, "EconomicDecimalV2": {}, "EconomicDimensionV2": {},
	"EconomicComponentKeyV2": {}, "EconomicMeasureV2": {}, "EconomicSubjectV2": {},
	"EconomicCorrelationV2": {}, "EconomicScopeValueV2": {}, "EconomicScopeV2": {},
	"EconomicChargeRefV2": {}, "EconomicCoverageV2": {}, "EconomicPaymentPartyV2": {},
	"EconomicChargeV2": {}, "EconomicObservationRefV2": {}, "EconomicEvidenceFieldV2": {},
	"EconomicObservationV2": {},
}

func isEconomicV2ProtoPath(path string) bool {
	for part := range strings.SplitSeq(path, ".") {
		if _, ok := economicV2ProtoMessages[part]; ok {
			return true
		}
	}
	return false
}

func protoTokens(source string) []string {
	var out []string
	for i := 0; i < len(source); {
		if i+1 < len(source) && source[i:i+2] == "//" {
			for i < len(source) && source[i] != '\n' {
				i++
			}
			continue
		}
		if i+1 < len(source) && source[i:i+2] == "/*" {
			i += 2
			for i+1 < len(source) && source[i:i+2] != "*/" {
				i++
			}
			if i+1 < len(source) {
				i += 2
			}
			continue
		}
		if strings.ContainsRune("{}=;(),[]<>:", rune(source[i])) {
			out = append(out, source[i:i+1])
			i++
			continue
		}
		if source[i] == '"' {
			j := i + 1
			for j < len(source) {
				if source[j] == '\\' {
					j += 2
					continue
				}
				if source[j] == '"' {
					j++
					break
				}
				j++
			}
			out = append(out, source[i:j])
			i = j
			continue
		}
		if (source[i] >= 'a' && source[i] <= 'z') || (source[i] >= 'A' && source[i] <= 'Z') || source[i] == '_' || (source[i] >= '0' && source[i] <= '9') || source[i] == '.' {
			j := i + 1
			for j < len(source) && ((source[j] >= 'a' && source[j] <= 'z') || (source[j] >= 'A' && source[j] <= 'Z') || (source[j] >= '0' && source[j] <= '9') || source[j] == '_' || source[j] == '.') {
				j++
			}
			out = append(out, source[i:j])
			i = j
			continue
		}
		i++
	}
	return out
}

type parsedProto struct {
	Messages  map[string]struct{}
	Enums     map[string]struct{}
	Fields    map[string]LegacyProtoField
	AllFields map[string]LegacyProtoField
}

func parseProtoSchema(source string) (parsedProto, error) {
	t := protoTokens(source)
	p := parsedProto{Messages: map[string]struct{}{}, Enums: map[string]struct{}{}, Fields: map[string]LegacyProtoField{}, AllFields: map[string]LegacyProtoField{}}
	var stack []string
	for i := 0; i < len(t); i++ {
		switch t[i] {
		case "message", "enum":
			if i+2 >= len(t) {
				return p, fmt.Errorf("declaration missing name")
			}
			name := t[i+1]
			path := name
			if len(stack) > 0 {
				path = strings.Join(append(append([]string(nil), stack...), name), ".")
			}
			if t[i] == "message" {
				p.Messages[path] = struct{}{}
			} else {
				p.Enums[path] = struct{}{}
			}
			stack = append(stack, path)
			i++
		case "}":
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		default:
			if len(stack) == 0 || i+4 >= len(t) {
				continue
			}
			start := i
			label := ""
			if t[i] == "optional" || t[i] == "repeated" || t[i] == "required" {
				label = t[i]
				i++
			}
			if i+4 >= len(t) || t[i+2] != "=" {
				i = start
				continue
			}
			number, err := strconv.Atoi(t[i+3])
			if err != nil {
				i = start
				continue
			}
			name, typ := t[i+1], t[i]
			if name == "" || typ == "" {
				i = start
				continue
			}
			field := LegacyProtoField{Path: stack[len(stack)-1], Name: name, Number: number, Type: typ, Label: label}
			if i+4 < len(t) && t[i+4] == "[" {
				var opts []string
				j := i + 4
				for j < len(t) && t[j] != ";" {
					opts = append(opts, t[j])
					j++
				}
				field.Options = strings.Join(opts, " ")
			}
			key := field.Path + "." + field.Name
			p.AllFields[key] = field
			if _, protected := legacyProtoFields[key]; protected {
				p.Fields[key] = field
			}
		}
	}
	return p, nil
}

func protoFieldEqual(a, b LegacyProtoField) bool {
	return a.Path == b.Path && a.Name == b.Name && a.Number == b.Number && a.Type == b.Type && a.Label == b.Label && a.Options == b.Options
}

// ValidateProtoSchema performs structural ABI validation.
func ValidateProtoSchema(source string) error {
	parsed, err := parseProtoSchema(source)
	if err != nil {
		return err
	}
	for key, want := range legacyProtoFields {
		got, ok := parsed.Fields[key]
		if !ok {
			return fmt.Errorf("protected legacy declaration %s is missing", key)
		}
		// Existing declarations may omit an explicit proto3 label; normalize that
		// representation to the baseline's empty label.
		if !protoFieldEqual(got, want) {
			return fmt.Errorf("protected declaration %s changed: got %+v want %+v", key, got, want)
		}
	}
	for path := range parsed.Messages {
		if protocolName(path) && !isEconomicV2ProtoPath(path) {
			return fmt.Errorf("protocol-specific message %q is not in v1.3 allowlist", path)
		}
	}
	for path := range parsed.Enums {
		if protocolName(path) && !isEconomicV2ProtoPath(path) {
			return fmt.Errorf("protocol-specific enum %q is not in v1.3 allowlist", path)
		}
	}
	for key, field := range parsed.AllFields {
		if _, enum := parsed.Enums[field.Path]; enum {
			continue
		}
		if protocolName(key) && !isEconomicV2ProtoPath(key) {
			if _, allowed := legacyProtoFields[key]; !allowed {
				return fmt.Errorf("protocol-specific field %q is not in v1.3 allowlist", key)
			}
		}
	}
	return nil
}

func protocolName(value string) bool {
	v := strings.ToLower(value)
	for _, marker := range []string{"openai", "openresponses", "anthropic", "gemini", "bedrock", "codex", "acp", "claude", "vendor", "provider"} {
		if strings.Contains(v, marker) {
			return true
		}
	}
	words := identifierWords(value)
	if len(words) < 2 {
		return false
	}
	// Neutral vocabulary is deliberately open-ended by semantic role, while a
	// non-neutral qualifier attached to a protocol-shaped message/field is not.
	if neutralABITerms[words[0]] {
		return false
	}
	for _, word := range words[1:] {
		switch word {
		case "invocation", "request", "response", "event", "message", "field", "fields", "schema", "feature", "features", "capability", "capabilities", "dialect", "extension", "extensions", "payload", "carrier", "row", "projector":
			return true
		}
	}
	return providerSpecificABIIdentifier(value)
}

// ValidateProtoLine is a mutation-test seam for protocol tokens.
func ValidateProtoLine(line string) error {
	return validateProtoMutationLine(line)
}

func validateProtoMutationLine(line string) error {
	clean := strings.TrimSpace(line)
	if clean == "" || strings.HasPrefix(clean, "//") || strings.HasPrefix(clean, "/*") {
		return nil
	}
	for _, part := range protoTokens(clean) {
		if protocolName(part) {
			return fmt.Errorf("protocol-specific proto token %q is not in the v1.3 allowlist", part)
		}
	}
	return nil
}
