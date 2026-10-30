package main

import "log/slog"

// setProcessDefaultLogger publishes the operator's logger as the process
// default, so packages that fall back to slog.Default() report through the
// configured level and format instead of the standard library default.
func setProcessDefaultLogger(log *slog.Logger) {
	if log != nil {
		slog.SetDefault(log)
	}
}
