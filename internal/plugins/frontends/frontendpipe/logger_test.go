package frontendpipe

import (
	"io"
	"log/slog"
	"testing"
)

// TestConfigLoggerNeverNil proves the shared pipeline always has a diagnostics
// logger. A frontend without one drops every internal failure record, so the
// pipeline falls back to the standard library default logger instead of
// staying silent.
func TestConfigLoggerNeverNil(t *testing.T) {
	t.Parallel()
	if (Config{}).Logger() == nil {
		t.Fatal("a pipeline without an explicit logger must fall back to the default logger")
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if (Config{Log: log}).Logger() != log {
		t.Fatal("an explicit logger must be used as is")
	}
}
