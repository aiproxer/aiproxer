package diag

import (
	"testing"
)

func BenchmarkWithCallDiag(b *testing.B) {
	ctx := b.Context()
	b.ReportAllocs()
	for b.Loop() {
		_ = WithCallDiag(ctx, "trace-1", "aleg-1")
	}
}

func BenchmarkWithTraceIDThenALeg(b *testing.B) {
	ctx := b.Context()
	b.ReportAllocs()
	for b.Loop() {
		c := WithTraceID(ctx, "trace-1")
		_ = WithALeg(c, "aleg-1")
	}
}

func BenchmarkEnsureCallDiag_hit(b *testing.B) {
	base := WithCallDiag(b.Context(), "trace-1", "aleg-1")
	b.ReportAllocs()
	for b.Loop() {
		_ = EnsureCallDiag(base, "trace-1", "aleg-1")
	}
}

func BenchmarkEnsureCallDiag_miss(b *testing.B) {
	ctx := b.Context()
	b.ReportAllocs()
	for b.Loop() {
		_ = EnsureCallDiag(ctx, "trace-1", "aleg-1")
	}
}
