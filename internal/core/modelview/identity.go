package modelview

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

type identityKey struct{}

// Identity is the aggregate request model-view identity: config generation,
// registry generation, catalog generation, and a stable digest (req 9.6).
// It carries no secrets, raw config, or connection material.
type Identity struct {
	ConfigGeneration   int64
	ConfigFingerprint  string
	RegistryGeneration string
	CatalogGeneration  string
	Digest             string
}

// Derive builds a stable Identity from safe generation fields.
func Derive(configGen int64, configFP, registryGen, catalogGen string) Identity {
	id := Identity{
		ConfigGeneration:   configGen,
		ConfigFingerprint:  strings.TrimSpace(configFP),
		RegistryGeneration: strings.TrimSpace(registryGen),
		CatalogGeneration:  strings.TrimSpace(catalogGen),
	}
	id.Digest = digestOf(id)
	return id
}

func digestOf(id Identity) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "cfg=%d\nfp=%s\nreg=%s\ncat=%s\n",
		id.ConfigGeneration, id.ConfigFingerprint, id.RegistryGeneration, id.CatalogGeneration)
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:16])
}

// WithIdentity attaches the aggregate model-view identity to ctx.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, identityKey{}, id)
}

// FromContext returns the request-bound aggregate identity when present.
func FromContext(ctx context.Context) (Identity, bool) {
	if ctx == nil {
		return Identity{}, false
	}
	id, ok := ctx.Value(identityKey{}).(Identity)
	if !ok || id.Digest == "" {
		return Identity{}, false
	}
	return id, true
}

// QuotedETag returns a safely quoted ETag from the digest, or empty when unset.
func (id Identity) QuotedETag() string {
	d := strings.TrimSpace(id.Digest)
	if d == "" {
		return ""
	}
	return `"` + d + `"`
}

// SafeFields returns operator-safe diagnostic fields (no secrets).
func (id Identity) SafeFields() map[string]string {
	out := map[string]string{
		"model_view_digest":   strings.TrimSpace(id.Digest),
		"registry_generation": strings.TrimSpace(id.RegistryGeneration),
		"catalog_generation":  strings.TrimSpace(id.CatalogGeneration),
		"config_fingerprint":  strings.TrimSpace(id.ConfigFingerprint),
	}
	if id.ConfigGeneration > 0 {
		out["config_generation"] = strconv.FormatInt(id.ConfigGeneration, 10)
	}
	return out
}
