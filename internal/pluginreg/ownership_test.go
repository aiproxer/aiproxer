package pluginreg_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/pluginreg"
)

func TestValidatePrefixSyntax_contract(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		prefix  string
		wantErr bool
		wantSub string
	}{
		{name: "valid", prefix: "provider-a", wantErr: false},
		{name: "empty", prefix: "", wantErr: true, wantSub: "backend_prefix"},
		{name: "whitespace", prefix: "  ", wantErr: true, wantSub: "backend_prefix"},
		{name: "contains_slash", prefix: "a/b", wantErr: true, wantSub: "/"},
		{name: "contains_colon", prefix: "a:b", wantErr: true, wantSub: ":"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := pluginreg.ValidatePrefixSyntax(tc.prefix)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				if !strings.Contains(err.Error(), tc.wantSub) {
					t.Fatalf("error %q does not mention %q", err, tc.wantSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestOwnershipCollision_builtinAndGeneric(t *testing.T) {
	t.Parallel()

	builtinOpenAI := pluginreg.BackendOwner{
		Origin:      pluginreg.OriginBuiltIn,
		FactoryKind: "openai-legacy",
		Prefix:      "openai",
		SourceID:    "essential:openai-legacy",
	}
	genericA := pluginreg.BackendOwner{
		Origin:      pluginreg.OriginBuiltInCompatible,
		FactoryKind: "custom-openai-legacy-compatible",
		InstanceID:  "compat-a",
		Prefix:      "provider-a",
		SourceID:    "plugins.backends.compat-a",
	}
	genericDup := pluginreg.BackendOwner{
		Origin:      pluginreg.OriginBuiltInCompatible,
		FactoryKind: "custom-openai-responses-compatible",
		InstanceID:  "compat-b",
		Prefix:      "provider-a",
		SourceID:    "plugins.backends.compat-b",
	}
	genericVsBuiltin := pluginreg.BackendOwner{
		Origin:      pluginreg.OriginBuiltInCompatible,
		FactoryKind: "custom-anthropic-compatible",
		InstanceID:  "compat-openai-prefix",
		Prefix:      "openai",
		SourceID:    "plugins.backends.compat-openai-prefix",
	}

	t.Run("unique_prefixes_ok", func(t *testing.T) {
		t.Parallel()
		err := pluginreg.ValidateManifestOwnership(pluginreg.ManifestOwnershipInput{
			BuiltIns:       []pluginreg.BackendOwner{builtinOpenAI},
			GenericEnabled: []pluginreg.BackendOwner{genericA},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("generic_duplicate_prefix", func(t *testing.T) {
		t.Parallel()
		err := pluginreg.ValidateManifestOwnership(pluginreg.ManifestOwnershipInput{
			BuiltIns:       []pluginreg.BackendOwner{builtinOpenAI},
			GenericEnabled: []pluginreg.BackendOwner{genericA, genericDup},
		})
		assertCollision(t, err, "provider-a", genericA.InstanceID, genericDup.InstanceID)
	})

	t.Run("generic_vs_builtin_prefix", func(t *testing.T) {
		t.Parallel()
		err := pluginreg.ValidateManifestOwnership(pluginreg.ManifestOwnershipInput{
			BuiltIns:       []pluginreg.BackendOwner{builtinOpenAI},
			GenericEnabled: []pluginreg.BackendOwner{genericVsBuiltin},
		})
		assertCollision(t, err, "openai", "openai", genericVsBuiltin.InstanceID)
	})
}

func TestOwnershipCollision_externalFactoryKindWithoutActivation(t *testing.T) {
	t.Parallel()

	builtin := pluginreg.BackendOwner{
		Origin:      pluginreg.OriginBuiltIn,
		FactoryKind: "anthropic",
		Prefix:      "anthropic",
		SourceID:    "essential:anthropic",
	}
	manifestKind := pluginreg.BackendOwner{
		Origin:      pluginreg.OriginExternalManifest,
		FactoryKind: "openrouter",
		SourceID:    "manifest:openrouter@1",
	}
	genericVsKind := pluginreg.BackendOwner{
		Origin:      pluginreg.OriginBuiltInCompatible,
		FactoryKind: "custom-openai-legacy-compatible",
		InstanceID:  "compat-openrouter",
		Prefix:      "openrouter",
		SourceID:    "plugins.backends.compat-openrouter",
	}

	t.Run("manifest_kind_blocks_generic_prefix", func(t *testing.T) {
		t.Parallel()
		err := pluginreg.ValidateManifestOwnership(pluginreg.ManifestOwnershipInput{
			BuiltIns:       []pluginreg.BackendOwner{builtin},
			GenericEnabled: []pluginreg.BackendOwner{genericVsKind},
			ManifestKinds:  []pluginreg.BackendOwner{manifestKind},
		})
		assertCollision(t, err, "openrouter", "openrouter", genericVsKind.InstanceID)
	})

	t.Run("disabled_generic_rows_ignored", func(t *testing.T) {
		t.Parallel()
		// Contract: only enabled generic rows are supplied in GenericEnabled.
		// Disabled rows must not be present in the input (established enabled-row policy).
		err := pluginreg.ValidateManifestOwnership(pluginreg.ManifestOwnershipInput{
			BuiltIns:      []pluginreg.BackendOwner{builtin},
			ManifestKinds: []pluginreg.BackendOwner{manifestKind},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestOwnershipCollision_fakeResolvedProfileBeforePublication(t *testing.T) {
	t.Parallel()

	base := pluginreg.ManifestOwnershipInput{
		BuiltIns: []pluginreg.BackendOwner{{
			Origin:      pluginreg.OriginBuiltIn,
			FactoryKind: "openai-responses",
			Prefix:      "openai",
			SourceID:    "essential:openai-responses",
		}},
		GenericEnabled: []pluginreg.BackendOwner{{
			Origin:      pluginreg.OriginBuiltInCompatible,
			FactoryKind: "custom-openai-responses-compatible",
			InstanceID:  "compat-local",
			Prefix:      "local-openai",
			SourceID:    "plugins.backends.compat-local",
		}},
		ManifestKinds: []pluginreg.BackendOwner{{
			Origin:      pluginreg.OriginExternalManifest,
			FactoryKind: "vllm",
			SourceID:    "manifest:vllm@1",
		}},
	}
	resolvedPrefix := pluginreg.BackendOwner{
		Origin:      pluginreg.OriginExternalResolved,
		FactoryKind: "vllm",
		InstanceID:  "ext-vllm-1",
		Prefix:      "local-openai",
		SourceID:    "resolved:vllm/ext-vllm-1",
	}

	err := pluginreg.ValidateResolvedOwnership(pluginreg.ResolvedOwnershipInput{
		Base:             base,
		ResolvedPrefixes: []pluginreg.BackendOwner{resolvedPrefix},
	})
	assertCollision(t, err, "local-openai", "compat-local", "ext-vllm-1")
}

func TestOwnership_acceptsNonCollidingResolvedPrefix(t *testing.T) {
	t.Parallel()
	err := pluginreg.ValidateResolvedOwnership(pluginreg.ResolvedOwnershipInput{
		Base: pluginreg.ManifestOwnershipInput{
			GenericEnabled: []pluginreg.BackendOwner{{
				Origin:      pluginreg.OriginBuiltInCompatible,
				FactoryKind: "custom-anthropic-compatible",
				InstanceID:  "compat-a",
				Prefix:      "provider-a",
			}},
		},
		ResolvedPrefixes: []pluginreg.BackendOwner{{
			Origin:      pluginreg.OriginExternalResolved,
			FactoryKind: "ollama",
			InstanceID:  "ext-ollama",
			Prefix:      "ollama-local",
			SourceID:    "resolved:ollama/ext-ollama",
		}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func assertCollision(t *testing.T, err error, key string, ownerA, ownerB string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected ownership collision error")
	}
	var coll *pluginreg.OwnershipCollisionError
	if !errors.As(err, &coll) {
		t.Fatalf("error type %T (%v) is not OwnershipCollisionError", err, err)
	}
	if coll.Key != key {
		t.Fatalf("collision key = %q, want %q", coll.Key, key)
	}
	msg := coll.Error()
	if !strings.Contains(msg, ownerA) || !strings.Contains(msg, ownerB) {
		t.Fatalf("collision error %q must identify owners %q and %q", msg, ownerA, ownerB)
	}
}
