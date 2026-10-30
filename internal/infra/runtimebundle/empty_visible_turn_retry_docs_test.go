package runtimebundle_test

// Operator documentation for the empty-visible-turn corrective retry (spec task
// 9; requirements 1.1, 4.7): the commented disabled reference in
// config/config.yaml must match the example's documented snippet, load as valid
// canonical configuration, and resolve to the disabled default.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/stretchr/testify/require"
)

func TestEmptyVisibleTurnRetryDocSnippetMatchesExample(t *testing.T) {
	t.Parallel()
	root := repoRootFromRuntimebundleTest(t)
	fixed, err := os.ReadFile(filepath.Join(root, "config", "config.yaml"))
	require.NoError(t, err)
	example, err := os.ReadFile(filepath.Join(root, "config", "examples", "empty-visible-turn-retry.yaml"))
	require.NoError(t, err)

	const key = "empty_visible_turn_retry:"
	fixedSnippet := commentedConfigSnippet(t, "config/config.yaml", string(fixed), key)
	exampleSnippet := commentedConfigSnippet(t, "config/examples/empty-visible-turn-retry.yaml", string(example), key)
	require.Equal(t, exampleSnippet, fixedSnippet, "documented snippet in config/config.yaml must match the example's documented form")
	require.Equal(t, key+"\n  enabled: false", fixedSnippet, "documented snippet must be the disabled reference")

	eff, err := config.LoadEffective(context.Background(), []byte(fixedSnippet), config.LoadEffectiveOptions{})
	require.NoError(t, err, "the documented snippet must load as canonical configuration")
	require.False(t, config.EffectiveEmptyVisibleTurnRetry(eff.Config).Enabled, "the documented snippet must resolve to the disabled default")
	require.Contains(t, string(example), key+"\n  enabled: true", "the example must show the explicit opt-in enabled form")
}

// commentedConfigSnippet returns the first commented two-line block starting
// with the named key as the uncommented YAML text, dedented to the block's own
// first-line indentation, so a documented block and an example's documented
// block compare as the same configuration.
func commentedConfigSnippet(t *testing.T, name, raw, key string) string {
	t.Helper()
	var snippet []string
	for line := range strings.SplitSeq(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "#") {
			continue
		}
		content := strings.TrimPrefix(strings.TrimPrefix(trimmed, "#"), " ")
		if len(snippet) == 0 {
			if strings.TrimSpace(content) == key {
				snippet = append(snippet, content)
			}
			continue
		}
		snippet = append(snippet, content)
		break
	}
	require.Lenf(t, snippet, 2, "%s: no commented two-line %q reference snippet found", name, key)
	first := snippet[0]
	indent := first[:len(first)-len(strings.TrimLeft(first, " "))]
	for i, line := range snippet {
		snippet[i] = strings.TrimPrefix(line, indent)
	}
	return strings.Join(snippet, "\n")
}
