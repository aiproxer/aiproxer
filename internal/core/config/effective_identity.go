package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"gopkg.in/yaml.v3"
)

// EffectiveIdentity is the private raw/effective identity used for no-op
// decisions (requirements 3.6–3.8). PrivateDigest must never appear in logs,
// APIs, or public status. PublicFingerprint is secret-safe generation metadata.
type EffectiveIdentity struct {
	PrivateDigest     [32]byte
	PublicFingerprint string
}

// ComputeEffectiveIdentity returns the private digest over the full effective
// config and a secret-safe public fingerprint over a redacted projection.
func ComputeEffectiveIdentity(cfg *Config) (EffectiveIdentity, error) {
	if cfg == nil {
		return EffectiveIdentity{}, fmt.Errorf("config: nil config")
	}
	privateRaw, err := yaml.Marshal(cfg)
	if err != nil {
		return EffectiveIdentity{}, fmt.Errorf("config: marshal private identity: %w", err)
	}
	redacted := redactSecretsForFingerprint(cfg)
	publicRaw, err := yaml.Marshal(redacted)
	if err != nil {
		return EffectiveIdentity{}, fmt.Errorf("config: marshal public fingerprint: %w", err)
	}
	priv := sha256.Sum256(privateRaw)
	pub := sha256.Sum256(publicRaw)
	return EffectiveIdentity{
		PrivateDigest:     priv,
		PublicFingerprint: "cfg_" + hex.EncodeToString(pub[:8]),
	}, nil
}

func redactSecretsForFingerprint(cfg *Config) *Config {
	cp := *cfg
	if len(cfg.Auth.LocalAPIKeys) == 0 {
		return &cp
	}
	keys := make([]AuthLocalAPIKeyRecord, len(cfg.Auth.LocalAPIKeys))
	copy(keys, cfg.Auth.LocalAPIKeys)
	for i := range keys {
		if keys[i].Key != "" {
			keys[i].Key = "[redacted]"
		}
	}
	cp.Auth.LocalAPIKeys = keys
	return &cp
}
