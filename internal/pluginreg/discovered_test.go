package pluginreg_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/pluginreg"
)

func TestResolveEnabledFactoryIDs_UnknownKindFailsClosed(t *testing.T) {
	t.Parallel()
	err := pluginreg.ResolveEnabledFactoryIDs(
		[]string{"openai-responses", "synthetic-unknown-kind-xyz"},
		[]string{"openai-responses", "anthropic"},
	)
	if err == nil {
		t.Fatal("expected unknown kind error")
	}
	if !strings.Contains(err.Error(), "synthetic-unknown-kind-xyz") {
		t.Fatalf("error = %v, want unknown kind named", err)
	}
}

func TestResolveEnabledFactoryIDs_UnionOfEssentialAndDiscovered(t *testing.T) {
	t.Parallel()
	err := pluginreg.ResolveEnabledFactoryIDs(
		[]string{"openai-responses", "synthetic-unknown-kind-xyz"},
		[]string{"openai-responses", "anthropic", "synthetic-unknown-kind-xyz"},
	)
	if err != nil {
		t.Fatal(err)
	}
}

func TestResolveEnabledFactoryIDs_IgnoresEmptyEnabled(t *testing.T) {
	t.Parallel()
	if err := pluginreg.ResolveEnabledFactoryIDs(nil, []string{"openai-responses"}); err != nil {
		t.Fatal(err)
	}
}
