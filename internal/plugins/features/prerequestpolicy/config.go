package prerequestpolicy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ID is the bundled feature plugin id for LLM-backed pre-request admission policy.
const ID = "pre-request-policy"

const (
	PolicyDenyOnPattern  = "deny_on_pattern"
	PolicyAllowOnPattern = "allow_on_pattern"
)

const defaultDenyMessage = "request denied"

// Config is the plugin-level configuration for chained pre-request policy handlers.
type Config struct {
	PromptDir string          `yaml:"prompt_dir"`
	Handlers  []HandlerConfig `yaml:"handlers"`
}

// HandlerConfig configures one pre-request policy model call.
type HandlerConfig struct {
	ID                 string        `yaml:"id"`
	Priority           int           `yaml:"priority"`
	PromptFilename     string        `yaml:"prompt_filename"`
	Prompt             string        `yaml:"-"`
	ModelRoutingString string        `yaml:"model_routing_string"`
	AllowPattern       string        `yaml:"allow_pattern"`
	DenyPattern        string        `yaml:"deny_pattern"`
	Policy             string        `yaml:"policy"`
	DenyMessage        string        `yaml:"deny_message"`
	Timeout            time.Duration `yaml:"-"`
	TimeoutRaw         string        `yaml:"timeout"`
}

// DecodeConfig parses YAML and validates file-local prompt references.
func DecodeConfig(n yaml.Node) (Config, error) {
	root := n
	switch root.Kind {
	case 0:
		return Config{}, nil
	case yaml.DocumentNode:
		if len(root.Content) == 0 {
			return Config{}, nil
		}
		root = *root.Content[0]
	}
	switch root.Kind {
	case 0, yaml.ScalarNode:
		if root.Kind == yaml.ScalarNode && (root.Tag == "!!null" || root.Value == "" || root.Value == "null") {
			return Config{}, nil
		}
		if root.Kind == 0 {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("%s: config must be a mapping or null", ID)
	case yaml.MappingNode:
		var cfg Config
		if err := root.Decode(&cfg); err != nil {
			return Config{}, fmt.Errorf("%s: %w", ID, err)
		}
		if strings.TrimSpace(cfg.PromptDir) == "" {
			cfg.PromptDir = "config/prompts/pre_request"
		}
		for i := range cfg.Handlers {
			if err := normalizeHandlerConfig(&cfg.Handlers[i], i); err != nil {
				return Config{}, err
			}
		}
		return cfg, nil
	default:
		return Config{}, fmt.Errorf("%s: config must be a mapping or null", ID)
	}
}

func normalizeHandlerConfig(h *HandlerConfig, i int) error {
	if strings.TrimSpace(h.ID) == "" {
		h.ID = fmt.Sprintf("%s-%d", ID, i+1)
	}
	h.ModelRoutingString = strings.TrimSpace(h.ModelRoutingString)
	if h.ModelRoutingString == "" {
		return fmt.Errorf("%s: handlers[%d].model_routing_string is required", ID, i)
	}
	h.Policy = strings.TrimSpace(h.Policy)
	if h.Policy == "" {
		h.Policy = PolicyDenyOnPattern
	}
	switch h.Policy {
	case PolicyDenyOnPattern:
		if strings.TrimSpace(h.DenyPattern) == "" {
			return fmt.Errorf("%s: handlers[%d].deny_pattern is required for %s", ID, i, PolicyDenyOnPattern)
		}
	case PolicyAllowOnPattern:
		if strings.TrimSpace(h.AllowPattern) == "" {
			return fmt.Errorf("%s: handlers[%d].allow_pattern is required for %s", ID, i, PolicyAllowOnPattern)
		}
	default:
		return fmt.Errorf("%s: handlers[%d].policy must be %q or %q", ID, i, PolicyDenyOnPattern, PolicyAllowOnPattern)
	}
	if strings.TrimSpace(h.DenyMessage) == "" {
		h.DenyMessage = defaultDenyMessage
	}
	if h.TimeoutRaw != "" {
		d, err := time.ParseDuration(h.TimeoutRaw)
		if err != nil {
			return fmt.Errorf("%s: handlers[%d].timeout: %w", ID, i, err)
		}
		if d <= 0 {
			return fmt.Errorf("%s: handlers[%d].timeout must be positive", ID, i)
		}
		h.Timeout = d
	}
	if h.Timeout < 0 {
		return fmt.Errorf("%s: handlers[%d].timeout must be positive", ID, i)
	}
	if h.Timeout == 0 {
		h.Timeout = 30 * time.Second
	}
	if strings.TrimSpace(h.Prompt) == "" {
		return validatePromptFilename(h.PromptFilename)
	}
	return nil
}

func validatePromptFilename(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("%s: prompt_filename is required", ID)
	}
	if filepath.Base(name) != name || strings.Contains(name, "/") || strings.Contains(name, `\`) || strings.Contains(name, "..") {
		return fmt.Errorf("%s: prompt_filename must be a filename, got %q", ID, name)
	}
	return nil
}

func loadPrompt(dir, name string) (string, error) {
	cleanPath := filepath.Clean(filepath.Join(dir, name))
	rel, err := filepath.Rel(dir, cleanPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == "." {
		return "", fmt.Errorf("%s: invalid path: path traversal detected", ID)
	}
	b, err := os.ReadFile(cleanPath)
	if err != nil {
		return "", fmt.Errorf("%s: prompt_filename %q: %w", ID, name, err)
	}
	prompt := strings.TrimSpace(string(b))
	if prompt == "" {
		return "", fmt.Errorf("%s: prompt_filename %q is empty", ID, name)
	}
	return prompt, nil
}
