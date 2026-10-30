package stdhttp

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/diag"
)

func TestMergeInventoryExtrasForDiagnosticsCopiesSecretGuardPosture(t *testing.T) {
	t.Parallel()
	categories := []string{"request_credential", "betterleaks"}
	source := &diag.InventoryExtras{
		SecretGuardCatalogEntryCount:      7,
		SecretGuardSourceCategories:       categories,
		SecretGuardAccessMode:             "multi_user",
		SecretGuardAction:                 "block",
		SecretGuardLocalAutoDiscovery:     true,
		SecretGuardBetterLeaksEnabled:     true,
		SecretGuardBetterLeaksVersion:     "v2",
		SecretGuardBetterLeaksConfigHash:  "hash",
		SecretGuardBetterLeaksRuleCount:   0,
		SecretGuardBetterLeaksConfidence:  "medium",
		SecretGuardBetterLeaksDecodeDepth: 2,
		SecretGuardBetterLeaksWorkers:     1,
		SecretGuardDiscoveryDetectorCount: 3,
	}

	got := mergeInventoryExtrasForDiagnostics(nil, nil, source)
	if got == source {
		t.Fatal("diagnostic merge must return an independent inventory value")
	}
	if got.SecretGuardCatalogEntryCount != source.SecretGuardCatalogEntryCount ||
		got.SecretGuardBetterLeaksEnabled != source.SecretGuardBetterLeaksEnabled ||
		got.SecretGuardBetterLeaksRuleCount != 0 || got.SecretGuardDiscoveryDetectorCount != 3 {
		t.Fatalf("secret-guard posture was not copied: %#v", got)
	}
	got.SecretGuardSourceCategories[0] = "mutated"
	if source.SecretGuardSourceCategories[0] != "request_credential" {
		t.Fatalf("secret-guard categories must not alias the source inventory: got %q", source.SecretGuardSourceCategories[0])
	}
}
