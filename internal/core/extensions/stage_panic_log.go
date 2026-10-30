package extensions

import (
	"context"
	"errors"
	"log/slog"

	"github.com/aiproxer/aiproxer/internal/core/lineage"
	"github.com/aiproxer/aiproxer/internal/core/safety"
)

// logFailOpenExtensionPanic emits one ERROR line when a fail-open extension stage path swallowed
// an isolated panic (converted to *safety.PanicError). When log is nil, logging is skipped.
func logFailOpenExtensionPanic(ctx context.Context, log *slog.Logger, stage, pluginID string, err error) {
	var pe *safety.PanicError
	if err == nil || !errors.As(err, &pe) || pe == nil || log == nil {
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
	attrs = append(attrs, slog.String("extension_stage", stage), slog.String("plugin_id", pluginID))
	log.LogAttrs(ctx, slog.LevelError, "extensions: isolated panic in fail-open extension stage", attrs...)
}
