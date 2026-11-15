package powershellheredoc_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/powershellheredoc"
	"gopkg.in/yaml.v3"
)

func TestFeatureID(t *testing.T) {
	t.Parallel()
	if powershellheredoc.ID != "powershell-heredoc-steering" {
		t.Fatalf("ID=%q, want powershell-heredoc-steering", powershellheredoc.ID)
	}
}

func TestDecodeConfig_AcceptsConfiguredTools(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
		want map[string]powershellheredoc.ToolRule
	}{
		{
			name: "powershell tool",
			raw:  "tools:\n  shell_command: { arg: command, shell: powershell }\n",
			want: map[string]powershellheredoc.ToolRule{
				"shell_command": {Arg: "command", Shell: powershellheredoc.ShellPowerShell},
			},
		},
		{
			name: "auto tool",
			raw:  "tools:\n  Bash: { arg: command, shell: auto }\n",
			want: map[string]powershellheredoc.ToolRule{
				"Bash": {Arg: "command", Shell: powershellheredoc.ShellAuto},
			},
		},
		{
			name: "block style with both shells",
			raw:  "tools:\n  shell_command:\n    arg: cmd\n    shell: powershell\n  Bash:\n    arg: command\n    shell: auto\n",
			want: map[string]powershellheredoc.ToolRule{
				"shell_command": {Arg: "cmd", Shell: powershellheredoc.ShellPowerShell},
				"Bash":          {Arg: "command", Shell: powershellheredoc.ShellAuto},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := decodeConfig(t, tc.raw)
			if err != nil {
				t.Fatalf("DecodeConfig: %v", err)
			}
			if len(cfg.Tools) != len(tc.want) {
				t.Fatalf("tools=%v, want %v", cfg.Tools, tc.want)
			}
			for name, want := range tc.want {
				if got := cfg.Tools[name]; got != want {
					t.Fatalf("tools[%q]=%+v, want %+v", name, got, want)
				}
			}
		})
	}
}

func TestDecodeConfig_RejectsInvalidConfig(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
	}{
		{name: "null config", raw: "null\n"},
		{name: "empty document", raw: ""},
		{name: "empty mapping", raw: "{}\n"},
		{name: "comment only", raw: "# no tools\n"},
		{name: "tools null", raw: "tools: null\n"},
		{name: "tools empty mapping", raw: "tools: {}\n"},
		{name: "tools scalar", raw: "tools: shell_command\n"},
		{name: "tools sequence", raw: "tools: [shell_command]\n"},
		{name: "unknown top-level key", raw: "tools:\n  shell_command: { arg: command, shell: powershell }\nextra: 1\n"},
		{name: "unknown tool key", raw: "tools:\n  shell_command: { arg: command, shell: powershell, default: x }\n"},
		{name: "tool entry not a mapping", raw: "tools:\n  shell_command: command\n"},
		{name: "missing arg", raw: "tools:\n  shell_command: { shell: powershell }\n"},
		{name: "empty arg", raw: "tools:\n  shell_command: { arg: \"\", shell: powershell }\n"},
		{name: "wildcard arg", raw: "tools:\n  shell_command: { arg: \"cmd*\", shell: powershell }\n"},
		{name: "missing shell", raw: "tools:\n  shell_command: { arg: command }\n"},
		{name: "null shell", raw: "tools:\n  shell_command: { arg: command, shell: null }\n"},
		{name: "unknown shell", raw: "tools:\n  shell_command: { arg: command, shell: cmd }\n"},
		{name: "shell case differs", raw: "tools:\n  shell_command: { arg: command, shell: PowerShell }\n"},
		{name: "empty tool name", raw: "tools:\n  \"\": { arg: command, shell: powershell }\n"},
		{name: "wildcard tool name", raw: "tools:\n  \"*\": { arg: command, shell: powershell }\n"},
		{name: "prefix wildcard tool name", raw: "tools:\n  \"mcp__*\": { arg: command, shell: auto }\n"},
		{name: "character class tool name", raw: "tools:\n  \"shell[0]\": { arg: command, shell: auto }\n"},
		{name: "padded tool name", raw: "tools:\n  \" shell \": { arg: command, shell: auto }\n"},
		{name: "invalid yaml", raw: "tools: [\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := decodeConfig(t, tc.raw); err == nil {
				t.Fatalf("DecodeConfig(%q) accepted invalid config", tc.raw)
			}
		})
	}
}

func TestDecodeConfig_ToolCountBound(t *testing.T) {
	t.Parallel()
	if _, err := decodeConfig(t, toolsYAML(16)); err != nil {
		t.Fatalf("16 tools: %v", err)
	}
	if _, err := decodeConfig(t, toolsYAML(17)); err == nil {
		t.Fatal("17 tools accepted, want rejection")
	}
}

func TestDecodeConfig_ErrorNamesFeature(t *testing.T) {
	t.Parallel()
	_, err := decodeConfig(t, "{}\n")
	if err == nil || !strings.Contains(err.Error(), powershellheredoc.ID) {
		t.Fatalf("error %v should name %q", err, powershellheredoc.ID)
	}
}

// toolsYAML returns a config with n distinct tools.
func toolsYAML(n int) string {
	var b strings.Builder
	b.WriteString("tools:\n")
	for i := range n {
		fmt.Fprintf(&b, "  tool_%d: { arg: command, shell: powershell }\n", i)
	}
	return b.String()
}

func decodeConfig(t *testing.T, raw string) (powershellheredoc.Config, error) {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
		// Invalid YAML is rejected by the decoder; surface it as a config error.
		return powershellheredoc.Config{}, err
	}
	return powershellheredoc.DecodeConfig(n)
}
