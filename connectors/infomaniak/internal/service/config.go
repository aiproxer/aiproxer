package service

import (
	"bytes"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/openaicompat"
	"gopkg.in/yaml.v3"
)

const (
	DefaultHTTPTimeout = 60 * time.Second
	DefaultOrigin      = "https://api.infomaniak.com"
)

// EnvAPIKeyRoot is the canonical nonsecret host environment namespace for
// Infomaniak credentials: INFOMANIAK_API_KEY[_N]. Secrets never appear in
// connector YAML; the host delivers ordered slots via Configure secrets.
const EnvAPIKeyRoot = "INFOMANIAK_API_KEY"

// QuotaDomain is the explicit nonsecret quota scope for Infomaniak
// credential selection: rotation decisions never alias an unrelated
// provider or project cooldown to this domain.
const QuotaDomain = "infomaniak-ai"

type Config struct {
	ProductID   string
	APIOrigin   string
	HTTPTimeout string
}

type rawConfig struct {
	ProductID   any    `yaml:"product_id"`
	APIOrigin   string `yaml:"api_origin"`
	HTTPTimeout string `yaml:"http_timeout"`
}

func ParseConfigYAML(raw []byte) (Config, error) {
	if err := openaicompat.RejectSecretFields("infomaniak-ai", EnvAPIKeyRoot, raw); err != nil {
		return Config{}, err
	}
	var rc rawConfig
	if len(bytes.TrimSpace(raw)) > 0 {
		dec := yaml.NewDecoder(bytes.NewReader(raw))
		dec.KnownFields(true)
		if err := dec.Decode(&rc); err != nil {
			return Config{}, fmt.Errorf("infomaniak-ai: config: %w", err)
		}
	}

	productID, err := parseProductID(rc.ProductID)
	if err != nil {
		return Config{}, err
	}

	apiOrigin := strings.TrimSpace(rc.APIOrigin)
	if strings.Contains(strings.ToLower(apiOrigin), "/compat") {
		return Config{}, fmt.Errorf("infomaniak-ai: /compat endpoint is forbidden")
	}

	return Config{
		ProductID:   productID,
		APIOrigin:   apiOrigin,
		HTTPTimeout: strings.TrimSpace(rc.HTTPTimeout),
	}, nil
}

func parseProductID(v any) (string, error) {
	if v == nil {
		return "", fmt.Errorf("infomaniak-ai: product_id is required")
	}
	var s string
	switch val := v.(type) {
	case int:
		if val <= 0 {
			return "", fmt.Errorf("infomaniak-ai: product_id must be positive, got %d", val)
		}
		s = strconv.Itoa(val)
	case int64:
		if val <= 0 {
			return "", fmt.Errorf("infomaniak-ai: product_id must be positive, got %d", val)
		}
		s = strconv.FormatInt(val, 10)
	case uint64:
		if val == 0 {
			return "", fmt.Errorf("infomaniak-ai: product_id must be positive, got 0")
		}
		s = strconv.FormatUint(val, 10)
	case string:
		s = strings.TrimSpace(val)
		if s == "" {
			return "", fmt.Errorf("infomaniak-ai: product_id is required")
		}
		for _, r := range s {
			if r < '0' || r > '9' {
				return "", fmt.Errorf("infomaniak-ai: product_id must contain only digits, got %q", s)
			}
		}
		num, err := strconv.ParseInt(s, 10, 64)
		if err != nil || num <= 0 {
			return "", fmt.Errorf("infomaniak-ai: product_id must be positive, got %q", s)
		}
	default:
		return "", fmt.Errorf("infomaniak-ai: product_id must be an integer or string of digits")
	}
	return s, nil
}

func (c Config) BaseURL() string {
	if c.ProductID == "" {
		return ""
	}
	origin := strings.TrimRight(strings.TrimSpace(c.APIOrigin), "/")
	if origin == "" {
		origin = DefaultOrigin
	}
	return fmt.Sprintf("%s/2/ai/%s/openai/v1", origin, c.ProductID)
}

func (c Config) HTTPClient() (*http.Client, error) {
	d := DefaultHTTPTimeout
	if c.HTTPTimeout != "" {
		parsed, err := time.ParseDuration(c.HTTPTimeout)
		if err != nil {
			return nil, fmt.Errorf("infomaniak-ai: http_timeout: %w", err)
		}
		d = parsed
	}
	return &http.Client{Timeout: d}, nil
}
