package diag

import (
	"context"
	"log/slog"

	"github.com/aiproxer/aiproxer/internal/core/safety"
)

// CrashAttrOpts extends [AttrOpts] with an optional attempt sequence; fields must stay bounded
// (static IDs, not unbounded user content).
type CrashAttrOpts struct {
	AttrOpts
	AttemptSeq int
}

// IsolatedCrashAttrs returns bounded slog attributes for an isolated application panic, merged
// with request/lineage context from ctx and [AttrOpts]. It never includes stack traces or raw
// panic text; use [AppendIsolatedCrashStack] to append a server-only stack field for loggers.
// When pe is nil, only correlation attributes from ctx and o are returned.
func IsolatedCrashAttrs(ctx context.Context, pe *safety.PanicError, o CrashAttrOpts) []slog.Attr {
	out := make([]slog.Attr, 0, 10)
	out = append(out, Attrs(ctx, o.AttrOpts)...)
	if o.AttemptSeq > 0 {
		out = append(out, slog.Int("attempt_seq", o.AttemptSeq))
	}
	if pe != nil {
		out = append(out, safety.PanicSlogFieldAttrs(pe)...)
	}
	return out
}

// AppendIsolatedCrashStack appends a "panic_stack" attribute when pe has stack bytes. Intended
// for structured server logs only; do not add these attributes to client responses, metrics
// labels, or high-cardinality dimensions.
func AppendIsolatedCrashStack(attrs []slog.Attr, pe *safety.PanicError) []slog.Attr {
	return safety.AppendPanicStackAttr(attrs, pe)
}
