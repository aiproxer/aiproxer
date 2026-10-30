package testkit

import (
	"io"
	"log/slog"
)

// DiscardLogger returns a logger that discards all output. Use in tests that must
// satisfy non-nil logger requirements at the composition root.
func DiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
