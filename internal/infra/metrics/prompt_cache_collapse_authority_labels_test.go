package metrics

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity"
	"github.com/aiproxer/aiproxer/internal/plugins/features/modelsystemprompt"
)

// The authority packages own their fixed tokens and the diagnostic owns the
// vocabulary, so a drift on either side would silently turn an attribution
// into `unexplained`.
func TestPromptCacheCollapse_AuthorityLabelsAreThePublishedVocabulary(t *testing.T) {
	for _, tc := range []struct {
		name  string
		label string
		want  string
	}{
		{"compaction", compactioncontinuity.CauseCompaction, "compaction"},
		{"system prompt", modelsystemprompt.CauseSystemPrompt, "system-prompt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.label != tc.want {
				t.Fatalf("authority label = %q, want %q", tc.label, tc.want)
			}
			if !publishableCollapseCause(CollapseCause(tc.label)) {
				t.Fatalf("authority label %q is not publishable", tc.label)
			}
		})
	}
}
