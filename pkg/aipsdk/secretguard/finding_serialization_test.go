package secretguard_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

func TestFindingJSONContainsOnlySafeBoundedMetadata(t *testing.T) {
	t.Parallel()
	finding := secretguard.Finding{
		SourceCategory:  secretguard.SourceCategoryUnknown,
		Location:        "messages[0].parts[0]",
		OccurrenceCount: 1,
		DetectorID:      secretguard.DetectorIDBetterLeaks,
		RuleID:          "github-pat",
		Confidence:      secretguard.ConfidenceMedium,
	}
	raw, err := json.Marshal(finding)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(raw)
	for _, forbidden := range []string{
		`"secret"`,
		"capture",
		"component",
		"context",
		"fingerprint",
		"fragment",
		"match",
	} {
		if strings.Contains(strings.ToLower(encoded), forbidden) {
			t.Fatalf("safe finding serialization contains forbidden field %q: %s", forbidden, encoded)
		}
	}
	for _, want := range []string{"DetectorID", "RuleID", "Confidence", "Location", "OccurrenceCount"} {
		if !strings.Contains(encoded, want) {
			t.Fatalf("safe finding serialization omitted %q: %s", want, encoded)
		}
	}
}
