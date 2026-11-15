package powershellheredoc

import (
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// ID is the canonical feature identifier in plugins.features.
const ID = "powershell-heredoc-steering"

// maxTools bounds the number of mapped tools.
const maxTools = 16

// Shell states which shell a mapped tool runs.
type Shell string

const (
	// ShellPowerShell asserts that the tool runs PowerShell: every heredoc match
	// is steered.
	ShellPowerShell Shell = "powershell"
	// ShellAuto marks a tool whose shell is unknown or mixed: a match is steered
	// only when the command also has an unquoted Windows path before the "<<".
	ShellAuto Shell = "auto"
)

// ToolRule maps one offered tool to its command argument and shell mode.
type ToolRule struct {
	// Arg is the top-level JSON string key that holds the command.
	Arg string
	// Shell is the shell mode. It has no default.
	Shell Shell
}

// Config is the validated powershell-heredoc-steering configuration. Tools is
// keyed by the exact offered tool name.
type Config struct {
	Tools map[string]ToolRule
}

type rawConfig struct {
	Tools map[string]rawTool `yaml:"tools"`
}

type rawTool struct {
	Arg   string `yaml:"arg"`
	Shell string `yaml:"shell"`
}

// DecodeConfig decodes one feature-private YAML subtree. The feature has no
// defaults: "tools" is required and must name at least one tool. It rejects
// unknown keys at both levels, a shell other than powershell or auto, an empty
// or wildcard name or argument key, and more than [maxTools] tools. Errors name
// the offending field.
func DecodeConfig(n yaml.Node) (Config, error) {
	root := n
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return Config{}, missingToolsError()
		}
		root = *root.Content[0]
	}
	switch root.Kind {
	case 0:
		return Config{}, missingToolsError()
	case yaml.ScalarNode:
		if root.Tag == "!!null" || root.Value == "" || root.Value == "null" {
			return Config{}, missingToolsError()
		}
		return Config{}, fmt.Errorf("%s: config must be a mapping", ID)
	case yaml.MappingNode:
		if err := checkKeys(&root, "config", "tools"); err != nil {
			return Config{}, err
		}
		if tools := mappingValue(&root, "tools"); tools != nil && tools.Kind == yaml.MappingNode {
			for i := 1; i < len(tools.Content); i += 2 {
				if tools.Content[i].Kind == yaml.MappingNode {
					if err := checkKeys(tools.Content[i], "tool", "arg", "shell"); err != nil {
						return Config{}, fmt.Errorf("%s: tools[%q]: %w", ID, tools.Content[i-1].Value, err)
					}
				}
			}
		}
		var raw rawConfig
		if err := root.Decode(&raw); err != nil {
			return Config{}, fmt.Errorf("%s: %w", ID, err)
		}
		return normalizeConfig(raw)
	default:
		return Config{}, fmt.Errorf("%s: config must be a mapping", ID)
	}
}

func missingToolsError() error {
	return fmt.Errorf("%s: tools is required and must name at least one tool", ID)
}

// checkKeys rejects any mapping key outside allowed. The first name in allowed
// labels the error and is skipped; the rest are the permitted keys.
func checkKeys(n *yaml.Node, allowed ...string) error {
	label, keys := allowed[0], allowed[1:]
	for i := 0; i < len(n.Content); i += 2 {
		k := n.Content[i].Value
		if !slices.Contains(keys, k) {
			return fmt.Errorf("%s: unknown %s key %q", ID, label, k)
		}
	}
	return nil
}

func mappingValue(n *yaml.Node, key string) *yaml.Node {
	for i := 0; i < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func normalizeConfig(raw rawConfig) (Config, error) {
	tools := make(map[string]ToolRule, len(raw.Tools))
	for name, t := range raw.Tools {
		tools[name] = ToolRule{Arg: t.Arg, Shell: Shell(t.Shell)}
	}
	if err := validateTools(tools); err != nil {
		return Config{}, err
	}
	return Config{Tools: tools}, nil
}

// validateTools checks the feature's tool table. It is shared by DecodeConfig
// and newFinalizer, because the bundle is reachable from Go callers that never
// ran DecodeConfig.
func validateTools(tools map[string]ToolRule) error {
	if len(tools) == 0 {
		return missingToolsError()
	}
	if len(tools) > maxTools {
		return fmt.Errorf("%s: tools must have at most %d entries", ID, maxTools)
	}
	names := make([]string, 0, len(tools))
	for name := range tools {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if !validName(name) {
			return fmt.Errorf("%s: tool name %q must be a non-empty exact name without wildcards or padding", ID, name)
		}
		rule := tools[name]
		if !validName(rule.Arg) {
			return fmt.Errorf("%s: tools[%q].arg %q must be a non-empty exact key without wildcards or padding", ID, name, rule.Arg)
		}
		switch rule.Shell {
		case ShellPowerShell, ShellAuto:
		default:
			return fmt.Errorf("%s: tools[%q].shell %q must be %q or %q", ID, name, rule.Shell, ShellPowerShell, ShellAuto)
		}
	}
	return nil
}

// validName reports whether s is a usable exact name: non-empty, not padded with
// whitespace, and free of glob metacharacters.
func validName(s string) bool {
	return s != "" && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "*?[]")
}
