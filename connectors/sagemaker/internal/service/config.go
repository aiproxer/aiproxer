package service

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	DefaultHTTPTimeout         = 60 * time.Second
	InferenceContractHFTextGen = "hf-text-generation"
)

type Config struct {
	Region                 string `yaml:"region"`
	EndpointName           string `yaml:"endpoint_name"`
	InferenceContract      string `yaml:"inference_contract"`
	APIOrigin              string `yaml:"api_origin"`
	TargetModel            string `yaml:"target_model"`
	InferenceComponentName string `yaml:"inference_component_name"`
	AWSProfile             string `yaml:"aws_profile"`
	HTTPTimeout            string `yaml:"http_timeout"`
	// No credential fields: signing material arrives via host Configure
	// secrets or the provider-managed AWS chain. Literal secret-bearing
	// YAML fields fail closed in rejectSecretFields below. The nonsecret
	// aws_profile chain selector stays valid YAML.
}

// forbiddenSecretFields are top-level YAML configuration keys that must
// never carry provider credential material in this connector. Comparison is
// case-insensitive with -/_ unified; values are never echoed. The set
// covers the shared static-key taxonomy plus AWS credential spellings.
var forbiddenSecretFields = map[string]struct{}{
	"api_key": {}, "api_keys": {}, "apikey": {}, "apikeys": {},
	"access_token": {}, "api_token": {}, "api_tokens": {},
	"auth_token": {}, "bearer_token": {}, "bearer": {},
	"token": {}, "tokens": {}, "iam_token": {}, "session_token": {},
	"credential": {}, "credentials": {},
	"secret": {}, "secrets": {}, "secret_key": {}, "secret_keys": {}, "secretkey": {}, "client_secret": {},
	"private_key": {}, "privatekey": {}, "pat": {}, "password": {},
	"auth_json": {}, "auth_json_path": {}, "authjson": {},
	"dotenv": {}, "env_file": {}, "credential_file": {}, "credentials_file": {},
	"key_file": {}, "secrets_file": {}, "token_file": {},
	"authorization":     {},
	"aws_access_key_id": {}, "access_key": {},
	"aws_secret_access_key": {}, "aws_session_token": {},
}

func ParseConfigYAML(raw []byte) (Config, error) {
	var cfg Config
	if len(raw) > 0 {
		if err := rejectSecretFields(raw); err != nil {
			return Config{}, err
		}
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return Config{}, fmt.Errorf("sagemaker: config: %w", err)
		}
	}

	cfg.Region = strings.TrimSpace(cfg.Region)
	cfg.EndpointName = strings.TrimSpace(cfg.EndpointName)
	cfg.InferenceContract = strings.TrimSpace(cfg.InferenceContract)
	cfg.APIOrigin = strings.TrimSpace(cfg.APIOrigin)
	cfg.TargetModel = strings.TrimSpace(cfg.TargetModel)
	cfg.InferenceComponentName = strings.TrimSpace(cfg.InferenceComponentName)
	cfg.AWSProfile = strings.TrimSpace(cfg.AWSProfile)
	cfg.HTTPTimeout = strings.TrimSpace(cfg.HTTPTimeout)

	if cfg.Region == "" {
		return Config{}, fmt.Errorf("sagemaker: region is required")
	}
	if cfg.EndpointName == "" {
		return Config{}, fmt.Errorf("sagemaker: endpoint_name is required")
	}
	if cfg.InferenceContract == "" {
		return Config{}, fmt.Errorf("sagemaker: inference_contract is required")
	}
	if cfg.InferenceContract != InferenceContractHFTextGen {
		return Config{}, fmt.Errorf("sagemaker: unsupported inference_contract %q; only %q is supported in v1", cfg.InferenceContract, InferenceContractHFTextGen)
	}

	return cfg, nil
}

// rejectSecretFields fails closed when raw connector YAML carries a
// secret-bearing top-level field. The error keeps the established
// secret-free shape, never the rejected value.
func rejectSecretFields(raw []byte) error {
	var rawMap map[string]any
	if err := yaml.Unmarshal(raw, &rawMap); err != nil {
		return fmt.Errorf("sagemaker: config: %w", err)
	}
	for k := range rawMap {
		normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(k), "-", "_"))
		if _, forbidden := forbiddenSecretFields[normalized]; forbidden {
			return fmt.Errorf("sagemaker: literal secrets in configuration YAML are forbidden; supply via ConfigureRequest.Secrets")
		}
	}
	return nil
}

func (c Config) HTTPClient() (*http.Client, error) {
	d := DefaultHTTPTimeout
	if c.HTTPTimeout != "" {
		parsed, err := time.ParseDuration(c.HTTPTimeout)
		if err != nil {
			return nil, fmt.Errorf("sagemaker: http_timeout: %w", err)
		}
		d = parsed
	}
	return &http.Client{Timeout: d}, nil
}
