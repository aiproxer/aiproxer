package tokenizers_test

import (
	"context"
	"strings"
	"testing"

	accountingapp "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	"github.com/aiproxer/aiproxer/internal/infra/tokenizers"
)

func TestResolveCompatibleID_omissionUsesDefault(t *testing.T) {
	t.Parallel()
	counter, id, err := tokenizers.ResolveCompatibleID("")
	if err != nil {
		t.Fatal(err)
	}
	if counter != nil || id != "" {
		t.Fatalf("counter=%v id=%q want nil/empty default", counter, id)
	}
}

func TestResolveCompatibleID_validOverrides(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		raw    string
		wantID string
	}{
		{"cl100k_base", "cl100k_base"},
		{"o200k_base", "o200k_base"},
		{" CL100K_BASE ", "cl100k_base"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			t.Parallel()
			counter, id, err := tokenizers.ResolveCompatibleID(tc.raw)
			if err != nil {
				t.Fatal(err)
			}
			if id != tc.wantID {
				t.Fatalf("id=%q want %q", id, tc.wantID)
			}
			if counter == nil {
				t.Fatal("expected local counter")
			}
			res, err := counter.CountText(context.Background(), accountingapp.CountTextInput{Text: "hello"})
			if err != nil {
				t.Fatal(err)
			}
			if res.Accounting.Tokenizer.ID != tc.wantID {
				t.Fatalf("tokenizer ref id=%q want %q", res.Accounting.Tokenizer.ID, tc.wantID)
			}
		})
	}
}

func TestResolveCompatibleID_rejectsUnknown(t *testing.T) {
	t.Parallel()
	_, _, err := tokenizers.ResolveCompatibleID("unknown-encoding")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "unknown compatible tokenizer") {
		t.Fatalf("err=%v", err)
	}
}

func TestResolveCompatibleID_sameKindInstancesIndependentCounters(t *testing.T) {
	t.Parallel()
	a, idA, err := tokenizers.ResolveCompatibleID("cl100k_base")
	if err != nil {
		t.Fatal(err)
	}
	b, idB, err := tokenizers.ResolveCompatibleID("o200k_base")
	if err != nil {
		t.Fatal(err)
	}
	if idA == idB {
		t.Fatal("fixture ids must differ")
	}
	if a == b {
		t.Fatal("expected distinct counter instances")
	}
}
