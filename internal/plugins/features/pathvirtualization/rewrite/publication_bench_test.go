package rewrite_test

import (
	"encoding/json"
	"testing"
)

// The source call is immutable: every iteration must see real paths, not the
// previous iteration's already-virtualized output. Parallel workers share only
// the immutable rewriter and source, never the package-level benchmark sinks.
func BenchmarkRewriteCall_WrappedArguments(b *testing.B) {
	fixture := benchRootFixtures[0]
	rewriter := benchBuiltinRewriter(b, fixture.root)
	call := benchRequestCall(b, fixture, 100, "read_file", nil)
	for i := range call.Items {
		wrapped, err := json.Marshal(string(call.Items[i].ToolCall.Arguments))
		if err != nil {
			b.Fatal(err)
		}
		call.Items[i].ToolCall.Arguments = wrapped
	}
	b.Run("serial", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			out, stats, err := rewriter.RewriteCall(call)
			if err != nil || out == call || stats.Rewritten != 100 {
				b.Fatalf("rewrite failed: err=%v replacements=%d", err, stats.Rewritten)
			}
		}
	})
	b.Run("concurrent_sessions", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				out, stats, err := rewriter.RewriteCall(call)
				if err != nil || out == call || stats.Rewritten != 100 {
					b.Errorf("rewrite failed: err=%v replacements=%d", err, stats.Rewritten)
					return
				}
			}
		})
	})
}
