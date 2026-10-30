package config_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/configsource"
)

// FuzzReloadConfigSource exercises bounded source classification and production
// StrictDecode. Inputs are capped; failures must not panic or echo seed secrets
// into error strings.
func FuzzReloadConfigSource(f *testing.F) {
	f.Add([]byte(""))
	f.Add([]byte(" \n\t"))
	f.Add([]byte("server:\n  address: \"127.0.0.1:0\"\n"))
	f.Add([]byte("server: [\n"))
	f.Add([]byte("server:\n  address: \"127.0.0.1:0\"\n---\nx: 1\n"))
	f.Add([]byte("server:\n  address: \"127.0.0.1:0\"\nnot_a_core_field: true\n"))
	f.Add([]byte("plugins:\n  backends:\n    - id: stub\n      enabled: true\n      config:\n        k: v\n"))
	secretSeed := []byte("server:\n  address: \"127.0.0.1:0\"\nauth:\n  local_api_keys:\n    - key_id: k\n      principal_id: p\n      key: fuzz-secret-do-not-echo-012345\n")
	f.Add(secretSeed)
	f.Add(bytes.Repeat([]byte("a"), int(configsource.DefaultMaxBytes)+8))

	f.Fuzz(func(t *testing.T, raw []byte) {
		if int64(len(raw)) > configsource.DefaultMaxBytes+4096 {
			raw = raw[:configsource.DefaultMaxBytes+4096]
		}
		_, cat, err := config.StrictDecode(raw)
		if err != nil {
			msg := err.Error()
			if strings.Contains(msg, "\x00") {
				t.Fatalf("error contains NUL: %q", msg)
			}
			if bytes.Contains(raw, []byte("fuzz-secret-do-not-echo-012345")) && strings.Contains(msg, "fuzz-secret-do-not-echo-012345") {
				t.Fatalf("error echoed secret seed: cat=%s msg=%q", cat, msg)
			}
			if int64(len(raw)) > 128 && strings.Contains(msg, string(raw[:64])) {
				t.Fatalf("error echoed raw prefix: cat=%s", cat)
			}
			return
		}
		if cat != configsource.CategoryOK {
			t.Fatalf("nil error with category %q", cat)
		}
	})
}
