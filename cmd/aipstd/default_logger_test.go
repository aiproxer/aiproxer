package main

import (
	"io"
	"log/slog"
	"testing"
)

// TestSetProcessDefaultLogger proves the standard distribution publishes the
// operator's configured logger as the process default, so packages that fall
// back to slog.Default() report through the configured level and format
// instead of the standard library default.
func TestSetProcessDefaultLogger(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	setProcessDefaultLogger(log)
	if slog.Default() != log {
		t.Fatal("the configured logger must become the process default")
	}
}

// TestSetProcessDefaultLoggerNilKeepsDefault proves a nil logger leaves the
// process default untouched instead of panicking.
func TestSetProcessDefaultLoggerNilKeepsDefault(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })

	setProcessDefaultLogger(nil)
	if slog.Default() != previous {
		t.Fatal("a nil logger must leave the process default untouched")
	}
}
