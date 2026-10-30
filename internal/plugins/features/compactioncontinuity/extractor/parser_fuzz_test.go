package extractor

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/capsule"
)

func FuzzParseResultNeverPanics(f *testing.F) {
	f.Add([]byte(`{"schema_version":1,"base_revision":0,"facts":[],"plan_updates":[],"decision_updates":[],"remove_or_supersede":[]}`))
	f.Add([]byte(`{"schema_version":1,"base_revision":0,"facts":[{"kind":"constraint","id":"c","statement":"x","status":"active","rationale":"","source_ref":"item-1"}],"plan_updates":[],"decision_updates":[],"remove_or_supersede":[]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = ParseResult(data, ParseOptions{Limits: Limits{MaxBytes: 32 << 10}})
	})
}

func TestResultDeltaDoesNotEscalateSemanticAuthority(t *testing.T) {
	t.Parallel()
	result := Result{
		SchemaVersion: 1,
		BaseRevision:  1,
		Decisions: []DecisionUpdate{{
			ID: "new", ConflictKey: "product.mode", Statement: "Use the bounded mode.", Status: capsule.DecisionActive,
			Authority: capsule.AuthorityUserExplicit, Source: capsule.SourceUserExplicit,
		}},
	}
	delta := result.Delta("sha256:"+strings.Repeat("a", 64), "watermark")
	if len(delta.Decisions) != 1 || delta.Decisions[0].Authority != capsule.AuthoritySemantic || delta.Decisions[0].SourceRef != "" {
		t.Fatalf("delta=%#v", delta)
	}
}
