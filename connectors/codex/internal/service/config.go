package service

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/acp"
	"github.com/aiproxer/aiproxer/connector-support/openaicompat"
	"github.com/aiproxer/aiproxer/connectors/codex/internal/appserver"
	"github.com/aiproxer/aiproxer/connectors/codex/internal/catalog"
	"github.com/aiproxer/aiproxer/connectors/codex/internal/codex"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"gopkg.in/yaml.v3"
)

const DefaultHTTPTimeout = 60 * time.Second

// EnvAccessTokenRoot is the canonical nonsecret host environment namespace
// for the Codex HTTP static token: OPENAI_CODEX_ACCESS_TOKEN. The connector
// never reads process environment or files for credentials; the host
// delivers exactly one api_token slot via Configure secrets
// (static_nonrotatable/no_rotation: one subscription identity, never a
// pooled quota peer).
const EnvAccessTokenRoot = "OPENAI_CODEX_ACCESS_TOKEN"

// QuotaDomain is the explicit nonsecret quota scope for the Codex HTTP
// export: rotation/refresh decisions never alias an unrelated provider or
// project cooldown to this domain.
const QuotaDomain = "codex"

type Config struct {
	BaseURL string `yaml:"base_url"`
	// No credential fields: access_token, refresh_token and auth_json_path
	// YAML are rejected in ParseConfigYAML. The account identity is
	// nonsecret operator configuration; the token arrives via Configure
	// secrets only.
	AccountID                                string                     `yaml:"account_id"`
	Models                                   []string                   `yaml:"models"`
	Executable                               string                     `yaml:"executable"`
	Model                                    string                     `yaml:"model"`
	ExtraArgs                                []string                   `yaml:"extra_args"`
	DefaultWorkspace                         string                     `yaml:"default_workspace"`
	ConfigOverrides                          []string                   `yaml:"config_overrides"`
	DefaultVerbosity                         string                     `yaml:"default_verbosity"`
	IdleTimeoutS                             float64                    `yaml:"idle_timeout_seconds"`
	StaleKillDelayS                          float64                    `yaml:"stale_kill_delay_seconds"`
	CatalogEnabled                           *bool                      `yaml:"catalog_enabled"`
	CatalogFallbackPath                      string                     `yaml:"catalog_fallback_path"`
	CatalogCodexBinary                       string                     `yaml:"catalog_codex_binary_path"`
	CatalogTimeout                           string                     `yaml:"catalog_timeout"`
	HTTPTimeout                              string                     `yaml:"http_timeout"`
	Transport                                string                     `yaml:"transport"`
	ExperimentalWebSocket                    bool                       `yaml:"experimental_websocket"`
	NativeContext                            *codex.NativeContextConfig `yaml:"-"`
	DisableNativeCompactionWithoutAccounting bool                       `yaml:"-"`
}

type nativeContextYAML struct {
	Enabled                   *bool                         `yaml:"enabled"`
	RequestEncryptedReasoning *bool                         `yaml:"request_encrypted_reasoning"`
	ReasoningContinuity       *codex.ContinuityMode         `yaml:"reasoning_continuity"`
	Compaction                *codex.NativeCompactionConfig `yaml:"compaction"`
}

// rejectCodexSecretFields fails closed on connector-specific static
// credential fields the shared forbidden list intentionally does not cover:
// refresh_token is legitimate YAML for OAuth-family connectors, but the
// Codex HTTP export admits its subscription token only via host Configure
// secrets. The error names the field path and the approved environment
// root, never the rejected value.
func rejectCodexSecretFields(raw []byte) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var fields map[string]any
	if err := yaml.Unmarshal(raw, &fields); err != nil {
		return fmt.Errorf("codex connector: config: invalid YAML")
	}
	for name := range fields {
		normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(name), "-", "_"))
		if normalized == "refresh_token" || normalized == "refreshtoken" {
			return fmt.Errorf("codex connector: config field %q is forbidden; provide credentials via host environment %s[_N] delivered as Configure secrets", name, EnvAccessTokenRoot)
		}
	}
	return nil
}

type configYAML struct {
	Config        `yaml:",inline"`
	NativeContext *nativeContextYAML `yaml:"native_context"`
}

func ParseConfigYAML(kind string, raw []byte) (Config, error) {
	if err := openaicompat.RejectSecretFields("codex", EnvAccessTokenRoot, raw); err != nil {
		return Config{}, err
	}
	if err := rejectCodexSecretFields(raw); err != nil {
		return Config{}, err
	}
	var cfg Config
	if len(bytes.TrimSpace(raw)) > 0 {
		var decoded configYAML
		dec := yaml.NewDecoder(bytes.NewReader(raw))
		dec.KnownFields(true)
		if err := dec.Decode(&decoded); err != nil {
			return Config{}, fmt.Errorf("codex connector: config yaml: %w", err)
		}
		cfg = decoded.Config
		if decoded.NativeContext != nil {
			if kind == FactoryKindHTTP {
				defaulted := codex.DefaultNativeContextConfig()
				cfg.NativeContext = &defaulted
				cfg.NativeContext.Source = "explicit"
			} else {
				cfg.NativeContext = &codex.NativeContextConfig{}
			}
			if decoded.NativeContext.Enabled != nil {
				cfg.NativeContext.Enabled = *decoded.NativeContext.Enabled
				cfg.NativeContext.SetEnabledPresentForYAML()
			}
			if decoded.NativeContext.RequestEncryptedReasoning != nil {
				cfg.NativeContext.RequestEncryptedReasoning = *decoded.NativeContext.RequestEncryptedReasoning
				cfg.NativeContext.SetRequestEncryptedPresentForYAML()
			}
			if decoded.NativeContext.ReasoningContinuity != nil {
				cfg.NativeContext.ReasoningContinuity = *decoded.NativeContext.ReasoningContinuity
			}
			if decoded.NativeContext.Compaction != nil {
				cfg.NativeContext.Compaction = *decoded.NativeContext.Compaction
				cfg.NativeContext.SetCompactionPresentForYAML()
			}
		}
	}
	if kind != FactoryKindHTTP && kind != FactoryKindAppServer {
		return Config{}, fmt.Errorf("codex connector: unknown factory kind %q", kind)
	}
	if kind == FactoryKindHTTP && cfg.NativeContext == nil {
		defaulted := codex.DefaultNativeContextConfig()
		cfg.NativeContext = &defaulted
	}
	return cfg, nil
}

func (c Config) catalogLoadOptions() catalog.LoadOptions {
	enabled := true
	if c.CatalogEnabled != nil {
		enabled = *c.CatalogEnabled
	}
	opts := catalog.LoadOptions{
		Enabled:         enabled,
		FallbackPath:    strings.TrimSpace(c.CatalogFallbackPath),
		CodexBinaryPath: strings.TrimSpace(firstNonEmpty(c.CatalogCodexBinary, c.Executable)),
	}
	if d := strings.TrimSpace(c.CatalogTimeout); d != "" {
		if parsed, err := time.ParseDuration(d); err == nil {
			opts.Timeout = parsed
		}
	}
	return opts
}

func (c Config) httpClient() (*http.Client, error) {
	d := DefaultHTTPTimeout
	if c.HTTPTimeout != "" {
		parsed, err := time.ParseDuration(c.HTTPTimeout)
		if err != nil {
			return nil, fmt.Errorf("codex connector: http_timeout: %w", err)
		}
		d = parsed
	}
	return &http.Client{Timeout: d}, nil
}

func (c Config) toCodexHTTP(cat *catalog.Catalog, accessToken string) (codex.Config, error) {
	native := c.NativeContext
	if native == nil {
		defaulted := codex.DefaultNativeContextConfig()
		native = &defaulted
	}
	nativeContext, err := normalizeNativeContext(native)
	if err != nil {
		return codex.Config{}, err
	}
	verbosity, err := aipapi.ParseVerbosityLevel(c.DefaultVerbosity)
	if err != nil {
		return codex.Config{}, fmt.Errorf("default_verbosity: %w", err)
	}
	hc, err := c.httpClient()
	if err != nil {
		return codex.Config{}, err
	}
	return codex.Config{
		BaseURL:                                  firstNonEmpty(strings.TrimSpace(c.BaseURL), codex.DefaultBaseURL),
		AccessToken:                              strings.TrimSpace(accessToken),
		AccountID:                                strings.TrimSpace(c.AccountID),
		HTTPClient:                               hc,
		Models:                                   c.Models,
		ModelCatalog:                             cat,
		CatalogLoadOpts:                          c.catalogLoadOptions(),
		DefaultVerbosity:                         verbosity,
		Transport:                                strings.TrimSpace(c.Transport),
		ExperimentalWebSocket:                    c.ExperimentalWebSocket,
		NativeContext:                            nativeContext,
		DisableNativeCompactionWithoutAccounting: c.DisableNativeCompactionWithoutAccounting,
	}, nil
}

func (c Config) toAppServer(cat *catalog.Catalog, src catalog.Source, cache *acp.ExecutableCache) (appserver.Config, error) {
	if c.NativeContext != nil {
		if _, err := normalizeNativeContext(c.NativeContext); err != nil {
			return appserver.Config{}, err
		}
		if c.NativeContext.HasNonDefaultSettings() {
			return appserver.Config{}, fmt.Errorf("codex connector: native_context is not supported for app-server connector")
		}
	}
	verbosity, err := aipapi.ParseVerbosityLevel(c.DefaultVerbosity)
	if err != nil {
		return appserver.Config{}, fmt.Errorf("default_verbosity: %w", err)
	}
	cfg := appserver.Config{
		ConnectorConfig: acp.ConnectorConfig{
			Executable:       strings.TrimSpace(c.Executable),
			Model:            strings.TrimSpace(c.Model),
			ExtraArgs:        c.ExtraArgs,
			DefaultWorkspace: strings.TrimSpace(c.DefaultWorkspace),
		},
		ConfigOverrides:    c.ConfigOverrides,
		ModelCatalog:       cat,
		ModelCatalogSource: src,
		CatalogLoadOpts:    c.catalogLoadOptions(),
		DefaultVerbosity:   verbosity,
		ExeCache:           cache,
	}
	if c.IdleTimeoutS > 0 {
		cfg.IdleTimeout = time.Duration(c.IdleTimeoutS * float64(time.Second))
	}
	if c.StaleKillDelayS > 0 {
		cfg.StaleKillDelay = time.Duration(c.StaleKillDelayS * float64(time.Second))
	}
	return cfg, nil
}

func normalizeNativeContext(cfg *codex.NativeContextConfig) (*codex.NativeContextConfig, error) {
	if cfg == nil {
		defaulted := codex.DefaultNativeContextConfig()
		cfg = &defaulted
	}
	norm, err := cfg.NormalizeAndValidate()
	if err != nil {
		return nil, fmt.Errorf("codex connector: native_context: %w", err)
	}
	return &norm, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
