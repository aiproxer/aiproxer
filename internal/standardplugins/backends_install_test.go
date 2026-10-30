package standardplugins

import (
	"reflect"
	"testing"
)

func TestPrefixedModelIDsFromYAML_stripsNativePrefixAndFallsBackToCanonicalTail(t *testing.T) {
	t.Parallel()
	got, err := prefixedModelIDsFromYAML("openai-responses", modelInventoryYAML{Items: []modelInventoryItemYAML{
		{NativeID: "openai-responses/gpt-5.3-codex-spark"},
		{CanonicalID: "openai-responses/gpt-5.4"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := []prefixedModelYAML{{RawID: "gpt-5.3-codex-spark"}, {RawID: "gpt-5.4"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("models = %#v, want %#v", got, want)
	}
}

func TestFirstResolvedAPIKeyReturnsFirstEnvKey(t *testing.T) {
	t.Parallel()
	if got := firstResolvedAPIKey([]string{"env-1", "env-2"}); got != "env-1" {
		t.Fatalf("primary = %q, want env-1", got)
	}
	if got := firstResolvedAPIKey(nil); got != "" {
		t.Fatalf("empty pool primary = %q, want empty (no-auth)", got)
	}
}
