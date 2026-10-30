package legacyfeatureconfig_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/standardplugins/legacyfeatureconfig"
)

const trailingBaseConfig = `
server:
  address: "127.0.0.1:0"
logging:
  level: "info"
plugins:
  backends:
    - id: "test-backend"
      enabled: true
`

// TestNormalizeYAML_PreservesMalformedTrailingContent is a BUG F2 regression
// test: a legacy alias followed by malformed trailing YAML must be returned
// unchanged so StrictDecode can reject it, never silently normalized to just
// the first document.
func TestNormalizeYAML_PreservesMalformedTrailingContent(t *testing.T) {
	t.Parallel()

	raw := []byte(trailingBaseConfig + "interleaved:\n  enabled: true\n---\nbroken: [")

	out, err := legacyfeatureconfig.NormalizeYAML(raw)
	if err != nil {
		t.Fatalf("NormalizeYAML: %v", err)
	}
	if !bytes.Equal(out, raw) {
		t.Fatalf("NormalizeYAML discarded malformed trailing content: got %q", string(out))
	}
}

// TestNormalizeYAML_PreservesSecondDocument is a BUG F2 regression test: a
// legacy alias followed by a well-formed second document must be returned
// unchanged so StrictDecode can reject it as multiple documents.
func TestNormalizeYAML_PreservesSecondDocument(t *testing.T) {
	t.Parallel()

	raw := []byte(trailingBaseConfig + "interleaved:\n  enabled: true\n---\nfoo: bar\n")

	out, err := legacyfeatureconfig.NormalizeYAML(raw)
	if err != nil {
		t.Fatalf("NormalizeYAML: %v", err)
	}
	if !bytes.Equal(out, raw) {
		t.Fatalf("NormalizeYAML discarded second document: got %q", string(out))
	}
}

// TestLoadEffective_LegacyAliasPlusMalformedTrailingContentFailsFast is the
// end-to-end BUG F2 regression test shared by the bootstrap and reload paths
// (both funnel raw bytes through NormalizeYAML into LoadEffective): the load
// must fail fast instead of succeeding on the silently truncated first doc.
// Note: with the raw bytes preserved, StrictDecode rejects the un-normalized
// legacy key before reaching the trailing-content check, so the surfaced
// category is UnknownCoreField — the fail-fast requirement is that load
// fails through the typed *config.LoadError path, never succeeds.
func TestLoadEffective_LegacyAliasPlusMalformedTrailingContentFailsFast(t *testing.T) {
	t.Parallel()

	raw := []byte(trailingBaseConfig + "interleaved:\n  enabled: true\n---\nbroken: [")

	_, err := config.LoadEffective(context.Background(), raw, config.LoadEffectiveOptions{
		NormalizeYAML: legacyfeatureconfig.NormalizeYAML,
	})
	if err == nil {
		t.Fatal("expected LoadEffective to fail fast on malformed trailing content, got nil error (tail silently discarded)")
	}
	if _, ok := errors.AsType[*config.LoadError](err); !ok {
		t.Fatalf("expected *config.LoadError, got %T: %v", err, err)
	}
}

// TestLoadEffective_LegacyAliasPlusSecondDocumentFailsFast is the end-to-end
// companion: legacy alias plus a well-formed second document must not load
// silently from the first document alone.
func TestLoadEffective_LegacyAliasPlusSecondDocumentFailsFast(t *testing.T) {
	t.Parallel()

	raw := []byte(trailingBaseConfig + "interleaved:\n  enabled: true\n---\nfoo: bar\n")

	_, err := config.LoadEffective(context.Background(), raw, config.LoadEffectiveOptions{
		NormalizeYAML: legacyfeatureconfig.NormalizeYAML,
	})
	if err == nil {
		t.Fatal("expected LoadEffective to fail fast on a second document, got nil error (second doc silently discarded)")
	}
	if _, ok := errors.AsType[*config.LoadError](err); !ok {
		t.Fatalf("expected *config.LoadError, got %T: %v", err, err)
	}
}
