package service

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	FactoryKind          = "local-stub"
	DefaultAssistantText = "[local-stub] deterministic assistant text"
	DefaultToolArgs      = "{}"
	maxToolArgsBytes     = 64 * 1024
	stubToolCallID       = "stub-tool-1"
)

type Config struct {
	Text                      string `yaml:"text"`
	InputTokens               int    `yaml:"input_tokens"`
	OutputTokens              int    `yaml:"output_tokens"`
	ToolName                  string `yaml:"tool_name"`
	ToolArgs                  string `yaml:"tool_args"`
	StreamErrorAfterTextDelta bool   `yaml:"stream_error_after_text_delta"`
}

func ParseConfigYAML(raw []byte) (Config, error) {
	var node yaml.Node
	if len(raw) == 0 {
		return NormalizeConfig(Config{})
	}
	if err := yaml.Unmarshal(raw, &node); err != nil {
		return Config{}, fmt.Errorf("local-stub: config yaml: %w", err)
	}
	var cfg Config
	if err := node.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("local-stub: config decode: %w", err)
	}
	return NormalizeConfig(cfg)
}

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
