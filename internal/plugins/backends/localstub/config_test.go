package localstub

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestParseConfig_emptyTextUsesDefault(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(`{}`), &n); err != nil {
		t.Fatal(err)
	}
	cfg, err := ParseConfig(n)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Text != DefaultAssistantText {
		t.Fatalf("text: got %q want %q", cfg.Text, DefaultAssistantText)
	}
}

func TestParseConfig_customText(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(`text: "hello"`), &n); err != nil {
		t.Fatal(err)
	}
	cfg, err := ParseConfig(n)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Text != "hello" {
		t.Fatalf("text %q", cfg.Text)
	}
}

func TestNormalizeConfig_negativeInputTokens(t *testing.T) {
	t.Parallel()
	_, err := NormalizeConfig(Config{InputTokens: -1})
	if err == nil || !strings.Contains(err.Error(), "input_tokens") {
		t.Fatalf("want input_tokens error, got %v", err)
	}
}

func TestNormalizeConfig_negativeOutputTokens(t *testing.T) {
	t.Parallel()
	_, err := NormalizeConfig(Config{OutputTokens: -3})
	if err == nil || !strings.Contains(err.Error(), "output_tokens") {
		t.Fatalf("want output_tokens error, got %v", err)
	}
}

func TestNormalizeConfig_whitespaceToolNameClears(t *testing.T) {
	t.Parallel()
	cfg, err := NormalizeConfig(Config{ToolName: "  \t  ", Text: "x", ToolArgs: `{"x":1`})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ToolName != "" {
		t.Fatalf("tool name: %q", cfg.ToolName)
	}
	if cfg.ToolArgs != "" {
		t.Fatalf("tool args should clear without tool name: %q", cfg.ToolArgs)
	}
}

func TestNormalizeConfig_toolNameDefaultsEmptyArgs(t *testing.T) {
	t.Parallel()
	cfg, err := NormalizeConfig(Config{ToolName: "echo", Text: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ToolArgs != DefaultToolArgs {
		t.Fatalf("tool args: got %q want %q", cfg.ToolArgs, DefaultToolArgs)
	}
}

func TestNormalizeConfig_toolArgsPreserved(t *testing.T) {
	t.Parallel()
	want := `{"location":"NYC"`
	cfg, err := NormalizeConfig(Config{ToolName: "get_weather", Text: "x", ToolArgs: want})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ToolArgs != want {
		t.Fatalf("tool args: got %q want %q", cfg.ToolArgs, want)
	}
}

func TestNormalizeConfig_toolArgsTooLarge(t *testing.T) {
	t.Parallel()
	_, err := NormalizeConfig(Config{
		ToolName: "echo",
		Text:     "x",
		ToolArgs: strings.Repeat("a", maxToolArgsBytes+1),
	})
	if err == nil || !strings.Contains(err.Error(), "tool_args") {
		t.Fatalf("want tool_args size error, got %v", err)
	}
}

func TestParseConfig_invalidTokenTypes(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(`input_tokens: "not-int"`), &n); err != nil {
		t.Fatal(err)
	}
	_, err := ParseConfig(n)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParseConfig_streamErrorAfterTextDelta(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(`stream_error_after_text_delta: true`), &n); err != nil {
		t.Fatal(err)
	}
	cfg, err := ParseConfig(n)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.StreamErrorAfterTextDelta {
		t.Fatal("expected StreamErrorAfterTextDelta")
	}
}

func TestNormalizeConfig_zeroTokensAllowed(t *testing.T) {
	t.Parallel()
	cfg, err := NormalizeConfig(Config{Text: "z", InputTokens: 0, OutputTokens: 0})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.InputTokens != 0 || cfg.OutputTokens != 0 {
		t.Fatalf("tokens %+v", cfg)
	}
}
