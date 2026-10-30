package backendplugin

import (
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const (
	toolChoiceRequiredPrefix = "required:"
	toolChoiceAllowedPrefix  = "allowed:"
)

// ValidateToolChoiceABI reports whether the canonical tool choice can be
// represented by the plugin ABI optional string without semantic loss. The
// allowed_tools subset is encoded as "allowed:<mode>:<n1>,<n2>", so a tool name
// containing a comma cannot round-trip and is rejected explicitly. Mode
// required combined with a subset is rejected too: the ABI decoder accepts only
// auto/none/any as the allowed mode, so the encoder could otherwise emit an
// "allowed:required:..." string the decoder rejects.
func ValidateToolChoiceABI(tc aipapi.ToolChoice) error {
	if tc.Mode == aipapi.ToolChoiceRequired && len(tc.AllowedTools) > 0 {
		return fmt.Errorf("%w: tool_choice mode required is incompatible with allowed tools", ErrInvalidInvocation)
	}
	for _, name := range tc.AllowedTools {
		if strings.Contains(name, ",") {
			return fmt.Errorf("%w: allowed tool name %q contains a comma and cannot be represented by the plugin ABI", ErrInvalidInvocation, name)
		}
	}
	return nil
}

// ToolChoiceToWire maps canonical tool choice into the plugin ABI optional string field.
// Default auto with no named tool is represented as absent (nil). The
// allowed_tools subset is encoded as "allowed:<mode>:<n1>,<n2>".
func ToolChoiceToWire(tc aipapi.ToolChoice) *string {
	mode := tc.Mode
	if mode == "" {
		mode = aipapi.ToolChoiceAuto
	}
	if len(tc.AllowedTools) > 0 {
		s := toolChoiceAllowedPrefix + string(mode) + ":" + strings.Join(tc.AllowedTools, ",")
		return &s
	}
	switch mode {
	case aipapi.ToolChoiceAuto:
		return nil
	case aipapi.ToolChoiceNone:
		s := string(aipapi.ToolChoiceNone)
		return &s
	case aipapi.ToolChoiceAny:
		s := string(aipapi.ToolChoiceAny)
		return &s
	case aipapi.ToolChoiceRequired:
		if tc.Name != "" {
			s := toolChoiceRequiredPrefix + tc.Name
			return &s
		}
		s := string(aipapi.ToolChoiceRequired)
		return &s
	default:
		s := string(mode)
		return &s
	}
}

// ToolChoiceFromWire decodes the plugin ABI tool choice string into canonical form.
func ToolChoiceFromWire(s *string) (aipapi.ToolChoice, error) {
	if s == nil {
		return aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto}, nil
	}
	raw := *s
	if raw == "" {
		return aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto}, nil
	}
	if raw != strings.TrimSpace(raw) {
		return aipapi.ToolChoice{}, fmt.Errorf("%w: tool_choice must not contain leading or trailing whitespace", ErrInvalidInvocation)
	}
	switch raw {
	case string(aipapi.ToolChoiceAuto):
		return aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto}, nil
	case string(aipapi.ToolChoiceNone):
		return aipapi.ToolChoice{Mode: aipapi.ToolChoiceNone}, nil
	case string(aipapi.ToolChoiceAny):
		return aipapi.ToolChoice{Mode: aipapi.ToolChoiceAny}, nil
	case string(aipapi.ToolChoiceRequired):
		return aipapi.ToolChoice{Mode: aipapi.ToolChoiceRequired}, nil
	}
	if after, ok := strings.CutPrefix(raw, toolChoiceRequiredPrefix); ok {
		name := after
		if name == "" {
			return aipapi.ToolChoice{}, fmt.Errorf("%w: tool_choice required name is empty", ErrInvalidInvocation)
		}
		if name != strings.TrimSpace(name) {
			return aipapi.ToolChoice{}, fmt.Errorf("%w: tool_choice required name must not contain leading or trailing whitespace", ErrInvalidInvocation)
		}
		return aipapi.ToolChoice{Mode: aipapi.ToolChoiceRequired, Name: name}, nil
	}
	if strings.HasPrefix(raw, toolChoiceAllowedPrefix) {
		return toolChoiceFromAllowedWire(raw)
	}
	if strings.HasPrefix(raw, "auto:") {
		return aipapi.ToolChoice{}, fmt.Errorf("%w: tool_choice auto name is not supported", ErrInvalidInvocation)
	}
	return aipapi.ToolChoice{}, fmt.Errorf("%w: unknown tool_choice %q", ErrInvalidInvocation, raw)
}

// toolChoiceFromAllowedWire decodes "allowed:<mode>:<n1>,<n2>" into canonical
// form. Mode must be one of the canonical auto/none/any values; names must be
// non-empty and whitespace-free. Colons inside a name are preserved (the first
// two segments are mode and the comma-joined name list).
func toolChoiceFromAllowedWire(raw string) (aipapi.ToolChoice, error) {
	body := strings.TrimPrefix(raw, toolChoiceAllowedPrefix)
	parts := strings.SplitN(body, ":", 2)
	if len(parts) != 2 {
		return aipapi.ToolChoice{}, fmt.Errorf("%w: malformed tool_choice %q", ErrInvalidInvocation, raw)
	}
	var mode aipapi.ToolChoiceMode
	switch parts[0] {
	case string(aipapi.ToolChoiceAuto):
		mode = aipapi.ToolChoiceAuto
	case string(aipapi.ToolChoiceNone):
		mode = aipapi.ToolChoiceNone
	case string(aipapi.ToolChoiceAny):
		mode = aipapi.ToolChoiceAny
	default:
		return aipapi.ToolChoice{}, fmt.Errorf("%w: unknown tool_choice allowed mode %q", ErrInvalidInvocation, parts[0])
	}
	namesStr := parts[1]
	if namesStr == "" || namesStr != strings.TrimSpace(namesStr) {
		return aipapi.ToolChoice{}, fmt.Errorf("%w: tool_choice allowed tools must not be empty or whitespace-padded", ErrInvalidInvocation)
	}
	names := strings.Split(namesStr, ",")
	allowed := make([]string, 0, len(names))
	for _, name := range names {
		if name == "" || name != strings.TrimSpace(name) {
			return aipapi.ToolChoice{}, fmt.Errorf("%w: tool_choice allowed tool name is empty or whitespace-padded", ErrInvalidInvocation)
		}
		allowed = append(allowed, name)
	}
	return aipapi.ToolChoice{Mode: mode, AllowedTools: allowed}, nil
}

func validateToolChoiceWire(s *string) error {
	_, err := ToolChoiceFromWire(s)
	return err
}
