package hooks

import (
	"context"
	"errors"
	"log/slog"

	"github.com/aiproxer/aiproxer/internal/core/lineage"
	"github.com/aiproxer/aiproxer/internal/core/safety"
)

type diagnosticsLoggerKey struct{}

// WithDiagnosticsLogger attaches log for isolated fail-open hook panic diagnostics. Nil log is a no-op.
// Composition roots that run hooks (for example the executor) should attach the server logger when
// available so [logFailOpenHookPanic] can emit structured lines; otherwise fail-open panic logs are skipped.
func WithDiagnosticsLogger(ctx context.Context, log *slog.Logger) context.Context {
	if ctx == nil || log == nil {
		return ctx
	}
	return context.WithValue(ctx, diagnosticsLoggerKey{}, log)
}

// diagnosticsLoggerFromContext returns the logger set by [WithDiagnosticsLogger], or nil when none
// was attached (including nil context). It does not fall back to [slog.Default] so operators are not
// surprised by hook panic lines on an unrelated global handler.
func diagnosticsLoggerFromContext(ctx context.Context) *slog.Logger {
	if ctx == nil {
		return nil
	}
	if v, ok := ctx.Value(diagnosticsLoggerKey{}).(*slog.Logger); ok && v != nil {
		return v
	}
	return nil
}

// logFailOpenHookPanic emits one ERROR line when a fail-open hook path swallowed an isolated panic.
func logFailOpenHookPanic(ctx context.Context, phase, hookID string, err error) {
	var pe *safety.PanicError
	if err == nil || !errors.As(err, &pe) || pe == nil {
		return
	}
	log := diagnosticsLoggerFromContext(ctx)
	if log == nil {
		return
	}
	attrs := make([]slog.Attr, 0, 12)
	if t := lineage.TraceID(ctx); t != "" {
		attrs = append(attrs, slog.String("trace_id", t))
	}
	if a := lineage.ALegID(ctx); a != "" {
		attrs = append(attrs, slog.String("a_leg_id", a))
	}
	attrs = append(attrs, safety.PanicSlogFieldAttrs(pe)...)
	attrs = safety.AppendPanicStackAttr(attrs, pe)
	attrs = append(attrs, slog.String("hook_phase", phase), slog.String("hook_id", hookID))
	log.LogAttrs(ctx, slog.LevelError, "hooks: isolated panic in fail-open hook", attrs...)
}
