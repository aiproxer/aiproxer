package reasoningpreservation_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"gopkg.in/yaml.v3"
)

func FuzzDecodeConfig(f *testing.F) {
	f.Add([]byte(validObserveYAML))
	f.Add([]byte(validRestoreYAML))
	f.Add([]byte("action: observe\n"))
	f.Add([]byte("action: restore\non_ambiguous: log_skip\n"))
	f.Add([]byte("not: a mapping"))
	f.Add([]byte(""))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 1<<16 {
			return
		}
		var n yaml.Node
		if err := yaml.Unmarshal(raw, &n); err != nil {
			return
		}
		cfg, err := reasoningpreservation.DecodeConfig(n)
		if err != nil {
			msg := err.Error()
			if strings.Contains(msg, "\x00") {
				t.Fatalf("error must not contain NUL: %q", msg)
			}
			return
		}
		if cfg.Action != reasoningpreservation.ActionObserve && cfg.Action != reasoningpreservation.ActionRestore {
			t.Fatalf("decoded action=%q", cfg.Action)
		}
		if cfg.OnAmbiguous != "" && cfg.OnAmbiguous != reasoningpreservation.PolicyLogSkip {
			t.Fatalf("on_ambiguous=%q", cfg.OnAmbiguous)
		}
	})
}
