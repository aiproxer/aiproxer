package config_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/configsource"
)

// FuzzEffectiveCanonicalization exercises StrictDecode → ComputeEffectiveIdentity.
// It proves deterministic digests for the same decoded config and that public
// fingerprints never echo seeded secrets (req 3.6-3.8, 15.5, 16.9).
func FuzzEffectiveCanonicalization(f *testing.F) {
	f.Add([]byte("server:\n  address: \"127.0.0.1:0\"\n"))
	f.Add([]byte("server:\n  address: \"127.0.0.1:0\"\nrouting:\n  default_route: stub:a\n"))
	secret := []byte("server:\n  address: \"127.0.0.1:0\"\nauth:\n  local_api_keys:\n    - key_id: k\n      principal_id: p\n      key: fuzz-eff-secret-do-not-echo-99\n")
	f.Add(secret)
	f.Add([]byte("server: [\n"))
	f.Add([]byte(""))

	const secretMarker = "fuzz-eff-secret-do-not-echo-99"

	f.Fuzz(func(t *testing.T, raw []byte) {
		if int64(len(raw)) > configsource.DefaultMaxBytes+4096 {
			raw = raw[:configsource.DefaultMaxBytes+4096]
		}
		cfg, cat, err := config.StrictDecode(raw)
		if err != nil || cfg == nil {
			if err != nil {
				msg := err.Error()
				if strings.Contains(msg, secretMarker) {
					t.Fatalf("load error echoed secret: cat=%s", cat)
				}
			}
			return
		}
		if cat != configsource.CategoryOK {
			t.Fatalf("nil error with category %q", cat)
		}
		id1, err := config.ComputeEffectiveIdentity(cfg)
		if err != nil {
			t.Fatalf("identity1: %v", err)
		}
		id2, err := config.ComputeEffectiveIdentity(cfg)
		if err != nil {
			t.Fatalf("identity2: %v", err)
		}
		if id1.PrivateDigest != id2.PrivateDigest {
			t.Fatal("private digest not deterministic")
		}
		if id1.PublicFingerprint != id2.PublicFingerprint {
			t.Fatal("public fingerprint not deterministic")
		}
		if id1.PublicFingerprint == "" || !strings.HasPrefix(id1.PublicFingerprint, "cfg_") {
			t.Fatalf("public fingerprint shape: %q", id1.PublicFingerprint)
		}
		if strings.Contains(id1.PublicFingerprint, secretMarker) {
			t.Fatalf("public fingerprint leaked secret: %q", id1.PublicFingerprint)
		}
		if bytes.Contains(raw, []byte(secretMarker)) && strings.Contains(id1.PublicFingerprint, "fuzz-eff-secret") {
			t.Fatalf("fingerprint contains secret fragment: %q", id1.PublicFingerprint)
		}
	})
}
