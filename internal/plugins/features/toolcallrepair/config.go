package toolcallrepair

import (
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"gopkg.in/yaml.v3"
)

const ID = "tool-call-repair"

const (
	ModeConservative          = "conservative"
	OnUnrepairablePassThrough = "pass_through"
	OnUnrepairableError       = "error"
	DefaultMaxArgsBytes       = 64 * 1024 // locked equal to core by TestDefaultMaxArgsBytesMatchCore
	DefaultFinalizerOrder     = 40
)

// Default schema limits mirror repair.DefaultSchemaLimits;
// locked by TestDefaultSchemaLimitsMatchCore.
const (
	defaultMaxSchemaBytes   = 256 * 1024
	defaultMaxNestingDepth  = 32
	defaultMaxNodes         = 4096
	defaultMaxProperties    = 1024
	defaultMaxLocalRefDepth = 32
	defaultMaxCacheEntries  = 64
	defaultMaxCacheBytes    = 4 * 1024 * 1024
)

const (
	MaxSchemaBytesCap   = aipapi.MaxToolParametersBytes
	MaxNestingDepthCap  = 256
	MaxNodesCap         = 65_536
	MaxPropertiesCap    = 16_384
	MaxLocalRefDepthCap = 256
	MaxCacheEntriesCap  = 4_096
	MaxCacheBytesCap    = 64 * 1024 * 1024
)

type SchemaConfig struct {
	MaxSchemaBytes   int `yaml:"max_schema_bytes"`
	MaxNestingDepth  int `yaml:"max_nesting_depth"`
	MaxNodes         int `yaml:"max_nodes"`
	MaxProperties    int `yaml:"max_properties"`
	MaxLocalRefDepth int `yaml:"max_local_ref_depth"`
	MaxCacheEntries  int `yaml:"max_cache_entries"`
	MaxCacheBytes    int `yaml:"max_cache_bytes"`
}

type Config struct {
	Mode           string       `yaml:"mode"`
	MaxArgsBytes   int          `yaml:"max_args_bytes"`
	OnUnrepairable string       `yaml:"on_unrepairable"`
	Order          *int         `yaml:"order"`
	Schema         SchemaConfig `yaml:"schema"`
}

func DecodeConfig(n yaml.Node) (Config, error) {
	root := n
	switch root.Kind {
	case 0:
		return defaultConfig(), nil
	case yaml.DocumentNode:
		if len(root.Content) == 0 {
			return defaultConfig(), nil
		}
		root = *root.Content[0]
	}
	switch root.Kind {
	case 0:
		return defaultConfig(), nil
	case yaml.ScalarNode:
		if root.Tag == "!!null" || root.Value == "" || root.Value == "null" {
			return defaultConfig(), nil
		}
		return Config{}, fmt.Errorf("%s: config must be a mapping or null", ID)
	case yaml.MappingNode:
		hasMaxArgsBytes := false
		for i := 0; i < len(root.Content); i += 2 {
			k := root.Content[i].Value
			if !allowedConfigKey(k) {
				return Config{}, fmt.Errorf("%s: unknown config key %q", ID, k)
			}
			if k == "max_args_bytes" {
				hasMaxArgsBytes = true
			}
			if k == "schema" {
				if err := validateSchemaKeys(root.Content[i+1]); err != nil {
					return Config{}, err
				}
			}
		}
		var cfg Config
		if err := root.Decode(&cfg); err != nil {
			return Config{}, fmt.Errorf("%s: %w", ID, err)
		}
		return normalizeConfig(cfg, hasMaxArgsBytes)
	default:
		return Config{}, fmt.Errorf("%s: config must be a mapping or null", ID)
	}
}

func allowedConfigKey(k string) bool {
	switch k {
	case "mode", "max_args_bytes", "on_unrepairable", "order", "schema":
		return true
	default:
		return false
	}
}

func validateSchemaKeys(n *yaml.Node) error {
	if n == nil {
		return nil
	}
	node := n
	if node.Kind == yaml.DocumentNode {
		if len(node.Content) == 0 {
			return nil
		}
		node = node.Content[0]
	}
	switch node.Kind {
	case 0:
		return nil
	case yaml.ScalarNode:
		if node.Tag == "!!null" || node.Value == "" || node.Value == "null" {
			return nil
		}
		return fmt.Errorf("%s: schema must be a mapping or null", ID)
	case yaml.MappingNode:
		for i := 0; i < len(node.Content); i += 2 {
			k := node.Content[i].Value
			switch k {
			case "max_schema_bytes", "max_nesting_depth", "max_nodes", "max_properties",
				"max_local_ref_depth", "max_cache_entries", "max_cache_bytes":
			default:
				return fmt.Errorf("%s: unknown config key %q", ID, "schema."+k)
			}
		}
		return nil
	default:
		return fmt.Errorf("%s: schema must be a mapping or null", ID)
	}
}

func defaultConfig() Config {
	cfg, _ := normalizeConfig(Config{}, false)
	return cfg
}

func normalizeConfig(cfg Config, hasMaxArgsBytes bool) (Config, error) {
	cfg.Mode = strings.TrimSpace(cfg.Mode)
	if cfg.Mode == "" {
		cfg.Mode = ModeConservative
	}
	if cfg.Mode != ModeConservative {
		return Config{}, fmt.Errorf("%s: mode must be %q", ID, ModeConservative)
	}
	if !hasMaxArgsBytes {
		cfg.MaxArgsBytes = DefaultMaxArgsBytes
	} else if cfg.MaxArgsBytes < 1 || cfg.MaxArgsBytes > aipapi.MaxEventDeltaBytes {
		return Config{}, fmt.Errorf("%s: max_args_bytes must be in [1,%d]", ID, aipapi.MaxEventDeltaBytes)
	}
	cfg.OnUnrepairable = strings.TrimSpace(cfg.OnUnrepairable)
	if cfg.OnUnrepairable == "" {
		cfg.OnUnrepairable = OnUnrepairablePassThrough
	}
	switch cfg.OnUnrepairable {
	case OnUnrepairablePassThrough, OnUnrepairableError:
	default:
		return Config{}, fmt.Errorf("%s: on_unrepairable must be %q or %q", ID, OnUnrepairablePassThrough, OnUnrepairableError)
	}
	if cfg.Order != nil && *cfg.Order < 0 {
		return Config{}, fmt.Errorf("%s: order must be non-negative", ID)
	}
	cfg.Schema = normalizeSchemaConfig(cfg.Schema)
	if err := validateSchemaLimit("max_schema_bytes", cfg.Schema.MaxSchemaBytes, MaxSchemaBytesCap); err != nil {
		return Config{}, err
	}
	if err := validateSchemaLimit("schema.max_nesting_depth", cfg.Schema.MaxNestingDepth, MaxNestingDepthCap); err != nil {
		return Config{}, err
	}
	if err := validateSchemaLimit("schema.max_nodes", cfg.Schema.MaxNodes, MaxNodesCap); err != nil {
		return Config{}, err
	}
	if err := validateSchemaLimit("schema.max_properties", cfg.Schema.MaxProperties, MaxPropertiesCap); err != nil {
		return Config{}, err
	}
	if err := validateSchemaLimit("schema.max_local_ref_depth", cfg.Schema.MaxLocalRefDepth, MaxLocalRefDepthCap); err != nil {
		return Config{}, err
	}
	if err := validateSchemaLimit("schema.max_cache_entries", cfg.Schema.MaxCacheEntries, MaxCacheEntriesCap); err != nil {
		return Config{}, err
	}
	if err := validateSchemaLimit("schema.max_cache_bytes", cfg.Schema.MaxCacheBytes, MaxCacheBytesCap); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func normalizeSchemaConfig(in SchemaConfig) SchemaConfig {
	if in.MaxSchemaBytes == 0 {
		in.MaxSchemaBytes = defaultMaxSchemaBytes
	}
	if in.MaxNestingDepth == 0 {
		in.MaxNestingDepth = defaultMaxNestingDepth
	}
	if in.MaxNodes == 0 {
		in.MaxNodes = defaultMaxNodes
	}
	if in.MaxProperties == 0 {
		in.MaxProperties = defaultMaxProperties
	}
	if in.MaxLocalRefDepth == 0 {
		in.MaxLocalRefDepth = defaultMaxLocalRefDepth
	}
	if in.MaxCacheEntries == 0 {
		in.MaxCacheEntries = defaultMaxCacheEntries
	}
	if in.MaxCacheBytes == 0 {
		in.MaxCacheBytes = defaultMaxCacheBytes
	}
	return in
}

func validateSchemaLimit(name string, v, limit int) error {
	if v < 1 {
		return fmt.Errorf("%s: %s must be positive", ID, name)
	}
	if v > limit {
		return fmt.Errorf("%s: %s must be in [1,%d]", ID, name, limit)
	}
	return nil
}

func (c Config) FinalizerOrder() int {
	if c.Order != nil {
		return *c.Order
	}
	return DefaultFinalizerOrder
}
