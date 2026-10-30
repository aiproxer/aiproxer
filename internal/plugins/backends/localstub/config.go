package localstub

import (
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"gopkg.in/yaml.v3"
)

// Config is YAML configuration for the local-stub backend (see stage-five design).
type Config struct {
	Text         string `yaml:"text"`
	InputTokens  int    `yaml:"input_tokens"`
	OutputTokens int    `yaml:"output_tokens"`
	ToolName     string `yaml:"tool_name"`
	// ToolArgs is the raw tool-call arguments delta emitted when ToolName is set.
	// Empty defaults to "{}". May be intentionally truncated/malformed JSON for
	// dogfood of canonical tool-call repair (issue #152).
	ToolArgs string `yaml:"tool_args"`
	// StreamErrorAfterTextDelta, when true, yields the normal stream through the first assistant
	// text delta then returns a non-recoverable error (dogfood invariant tests; not provider-like).
	StreamErrorAfterTextDelta bool `yaml:"stream_error_after_text_delta"`
}

// DefaultToolArgs is emitted when tool_name is set and tool_args is omitted/empty.
const DefaultToolArgs = "{}"

// maxToolArgsBytes caps configured tool_args (aligned with default repair max_args_bytes).
const maxToolArgsBytes = 64 * 1024

// DefaultAssistantText is used when text is empty or whitespace-only after normalization.
const DefaultAssistantText = "[local-stub] deterministic assistant text"

// ParseConfig decodes and normalizes stub configuration from a plugin YAML node.
func ParseConfig(n yaml.Node) (Config, error) {
	// Env-only credential contract (requirements 2.1, 5.4): even the
	// secret-free stub rejects literal credential fields before decode.
	if err := config.RejectBackendStaticCredentials("local-stub backend config", "", n); err != nil {
		return Config{}, err
	}
	var raw Config
	if err := config.DecodeYAMLNode(n, &raw); err != nil {
		return Config{}, fmt.Errorf("local-stub: config: %w", err)
	}
	out, err := NormalizeConfig(raw)
	if err != nil {
		return Config{}, fmt.Errorf("local-stub: normalize config: %w", err)
	}
	return out, nil
}

// NormalizeConfig applies defaults and validates counts and tool name.
func NormalizeConfig(raw Config) (Config, error) {
	if raw.InputTokens < 0 {
		return Config{}, fmt.Errorf("local-stub: input_tokens must be non-negative")
	}
	if raw.OutputTokens < 0 {
		return Config{}, fmt.Errorf("local-stub: output_tokens must be non-negative")
	}
	tool := strings.TrimSpace(raw.ToolName)
	text := strings.TrimSpace(raw.Text)
	if text == "" {
		text = DefaultAssistantText
	}
	args := raw.ToolArgs
	if tool == "" {
		args = ""
	} else if args == "" {
		args = DefaultToolArgs
	}
	if len(args) > maxToolArgsBytes {
		return Config{}, fmt.Errorf("local-stub: tool_args exceeds %d bytes", maxToolArgsBytes)
	}
	return Config{
		Text:                      text,
		InputTokens:               raw.InputTokens,
		OutputTokens:              raw.OutputTokens,
		ToolName:                  tool,
		ToolArgs:                  args,
		StreamErrorAfterTextDelta: raw.StreamErrorAfterTextDelta,
	}, nil
}
