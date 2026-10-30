package standardplugins

import (
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"gopkg.in/yaml.v3"
)

func TestValidatePrefixSyntax_rejectsEmpty(t *testing.T) {
	t.Parallel()
	if err := pluginreg.ValidatePrefixSyntax(""); err == nil {
		t.Fatal("expected error for empty backend_prefix")
	}
	if err := pluginreg.ValidatePrefixSyntax("   "); err == nil {
		t.Fatal("expected error for whitespace-only backend_prefix")
	}
}

func TestValidatePrefixSyntax_rejectsInvalidCharacters(t *testing.T) {
	t.Parallel()
	for _, prefix := range []string{"acme/prod", "acme:prod", "ac/me", "a:cme"} {
		if err := pluginreg.ValidatePrefixSyntax(prefix); err == nil {
			t.Fatalf("expected error for backend_prefix %q", prefix)
		}
	}
}

func TestValidateCompatibleManifestOwnership_rejectsReservedBuiltInPrefixes(t *testing.T) {
	t.Parallel()
	for _, owner := range CollectBuiltInBackendOwners(nil) {
		prefix := owner.Prefix
		err := ValidateCompatibleManifestOwnership([]config.PluginConfig{
			mustCompatibleRow(t, "nv-copy", prefix),
		}, nil)
		if err == nil {
			t.Fatalf("expected error for reserved backend_prefix %q", prefix)
		}
		var coll *pluginreg.OwnershipCollisionError
		if !errors.As(err, &coll) {
			t.Fatalf("error for %q = %v, want OwnershipCollisionError", prefix, err)
		}
		if coll.Key != prefix {
			t.Fatalf("collision key = %q, want %q", coll.Key, prefix)
		}
	}
}

func TestValidateCompatibleManifestOwnership_acceptsValidPrefix(t *testing.T) {
	t.Parallel()
	if err := ValidateCompatibleManifestOwnership([]config.PluginConfig{
		mustCompatibleRow(t, "ok", "my-provider"),
	}, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateCompatibleManifestOwnership_rejectsDuplicateEnabledPrefixes(t *testing.T) {
	t.Parallel()
	err := ValidateCompatibleManifestOwnership([]config.PluginConfig{
		mustCompatibleRow(t, "a", "same"),
		mustCompatibleRow(t, "b", "same"),
	}, nil)
	if err == nil {
		t.Fatal("expected duplicate backend_prefix error")
	}
	var coll *pluginreg.OwnershipCollisionError
	if !errors.As(err, &coll) {
		t.Fatalf("error = %v, want OwnershipCollisionError", err)
	}
	if coll.Key != "same" {
		t.Fatalf("collision key = %q, want same", coll.Key)
	}
	msg := coll.Error()
	if !strings.Contains(msg, "a") || !strings.Contains(msg, "b") {
		t.Fatalf("error %q must name both instances", msg)
	}
}

func TestValidateCompatibleManifestOwnership_ignoresDisabledRows(t *testing.T) {
	t.Parallel()
	disabled := mustCompatibleRow(t, "disabled", "same")
	disabled.Enabled = false
	err := ValidateCompatibleManifestOwnership([]config.PluginConfig{
		disabled,
		mustCompatibleRow(t, "enabled", "same"),
	}, nil)
	if err != nil {
		t.Fatalf("disabled rows must not participate in ownership: %v", err)
	}
}

func TestValidateCompatibleManifestOwnership_rejectsReservedAmongEnabled(t *testing.T) {
	t.Parallel()
	err := ValidateCompatibleManifestOwnership([]config.PluginConfig{
		mustCompatibleRow(t, "nv-copy", "anthropic"),
	}, nil)
	if err == nil {
		t.Fatal("expected reserved backend_prefix error")
	}
	if _, ok := errors.AsType[*pluginreg.OwnershipCollisionError](err); !ok {
		t.Fatalf("error = %v, want OwnershipCollisionError", err)
	}
}

func mustCompatibleRow(t *testing.T, id, prefix string) config.PluginConfig {
	t.Helper()
	var n yaml.Node
	raw := "backend_prefix: " + prefix + "\nbase_url: http://127.0.0.1:9/v1\n"
	if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatal(err)
	}
	for n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		n = *n.Content[0]
	}
	return config.PluginConfig{
		Kind:    CustomOpenAILegacyCompatibleID,
		ID:      id,
		Enabled: true,
		Config:  n,
	}
}
