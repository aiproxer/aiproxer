package service

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func slotBundle(values map[string]string) backendplugin.SecretBundle {
	bundle := backendplugin.SecretBundle{Values: make(map[string][]byte, len(values))}
	for name, value := range values {
		bundle.Values[name] = []byte(value)
	}
	return bundle
}

func TestResolveAPIKeys_OrderedSecretsPerKind(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{FactoryKindGo, FactoryKindZen} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			keys, err := ResolveAPIKeys(kind, slotBundle(map[string]string{
				"api_key_2": "second-key",
				"api_key":   "first-key",
			}))
			if err != nil {
				t.Fatal(err)
			}
			if len(keys) != 2 || keys[0] != "first-key" || keys[1] != "second-key" {
				t.Fatalf("keys=%q", keys)
			}
		})
	}
}

func TestResolveAPIKeys_MissingFailsWithKindRoot(t *testing.T) {
	t.Parallel()
	_, err := ResolveAPIKeys(FactoryKindGo, backendplugin.SecretBundle{})
	if err == nil || !strings.Contains(err.Error(), EnvAPIKeyRootGo) {
		t.Fatalf("err=%v", err)
	}
	_, err = ResolveAPIKeys(FactoryKindZen, backendplugin.SecretBundle{})
	if err == nil || !strings.Contains(err.Error(), EnvAPIKeyRootZen) {
		t.Fatalf("err=%v", err)
	}
}

func TestResolveAPIKeys_ErrorsNeverContainSecrets(t *testing.T) {
	t.Parallel()
	_, err := ResolveAPIKeys(FactoryKindGo, slotBundle(map[string]string{
		"api_key": "top-secret-material",
		"bogus":   "other",
	}))
	if err == nil {
		t.Fatal("expected rejection of unexpected slot name")
	}
	if strings.Contains(err.Error(), "top-secret-material") {
		t.Fatalf("error leaks secret: %v", err)
	}
}
