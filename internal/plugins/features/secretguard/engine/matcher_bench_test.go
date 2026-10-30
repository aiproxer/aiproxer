package engine_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/secretguard/engine"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

func BenchmarkMatcher_ScanNoMatchLargeInput(b *testing.B) {
	cat := benchCatalog(b, 64)
	m := engine.NewMatcher(cat)
	input := []byte(strings.Repeat("abcdefghijklmnopqrstuvwxyz0123456789", 4096))
	b.ReportAllocs()
	b.SetBytes(int64(len(input)))
	b.ResetTimer()
	for b.Loop() {
		_ = m.ScanBytes(input)
	}
}

func BenchmarkMatcher_ScanManyPatterns(b *testing.B) {
	cat := benchCatalog(b, 512)
	m := engine.NewMatcher(cat)
	input := []byte(strings.Repeat("nomatch-payload-block-", 256))
	b.ReportAllocs()
	b.SetBytes(int64(len(input)))
	b.ResetTimer()
	for b.Loop() {
		_ = m.ScanBytes(input)
	}
}

func BenchmarkMatcher_ScanManyMatches(b *testing.B) {
	const n = 64
	inputs := make([]engine.CatalogInput, 0, n)
	var bld strings.Builder
	for i := range n {
		val := fmt.Sprintf("sg-bench-match-value-%04d-xxxx", i)
		inputs = append(inputs, engine.CatalogInput{
			Name:           fmt.Sprintf("BENCH_MATCH_%04d", i),
			Value:          val,
			SourceCategory: sdk.SourceCategoryProxyEnv,
		})
		bld.WriteString(val)
		bld.WriteByte('|')
	}
	cat, err := engine.BuildCatalog(inputs, 8)
	if err != nil {
		b.Fatal(err)
	}
	m := engine.NewMatcher(cat)
	input := []byte(bld.String())
	b.ReportAllocs()
	b.SetBytes(int64(len(input)))
	b.ResetTimer()
	for b.Loop() {
		_ = m.ScanBytes(input)
	}
}

func BenchmarkMatcher_ScanLargeToolResult(b *testing.B) {
	cat := benchCatalog(b, 64)
	m := engine.NewMatcher(cat)
	// Large tool_result-shaped payload with no matches.
	payload := []byte(`{"result":"` + strings.Repeat("tool-output-chunk-", 8192) + `"}`)
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for b.Loop() {
		_ = m.ScanBytes(payload)
	}
}

func benchCatalog(b *testing.B, n int) *engine.Catalog {
	b.Helper()
	inputs := make([]engine.CatalogInput, 0, n)
	for i := range n {
		inputs = append(inputs, engine.CatalogInput{
			Name:           fmt.Sprintf("BENCH_SECRET_%04d", i),
			Value:          fmt.Sprintf("sg-bench-pattern-value-%04d-xxxx", i),
			SourceCategory: sdk.SourceCategoryProxyEnv,
		})
	}
	cat, err := engine.BuildCatalog(inputs, 8)
	if err != nil {
		b.Fatal(err)
	}
	return cat
}
