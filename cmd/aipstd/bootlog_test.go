package main

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/accessmode"
	"github.com/aiproxer/aiproxer/internal/core/config"
)

type recordSink struct {
	records []slog.Record
}

func (s *recordSink) Enabled(context.Context, slog.Level) bool { return true }

func (s *recordSink) Handle(_ context.Context, r slog.Record) error {
	s.records = append(s.records, r)
	return nil
}

func (s *recordSink) WithAttrs([]slog.Attr) slog.Handler { return s }

func (s *recordSink) WithGroup(string) slog.Handler { return s }

func TestLogBootstrapAccessAuth_emitsInfoRecord(t *testing.T) {
	t.Parallel()
	var sink recordSink
	log := slog.New(&sink)
	cfg := &config.Config{
		Server: config.ServerConfig{Address: "127.0.0.1:9090"},
		Access: config.AccessConfig{Mode: "single_user"},
	}
	if err := logBootstrapAccessAuth(context.Background(), log, cfg); err != nil {
		t.Fatal(err)
	}
	var saw bool
	for _, r := range sink.records {
		if r.Level != slog.LevelInfo || r.Message != "aipstd: effective access and authentication" {
			continue
		}
		saw = true
		var mode, addr, handler, level string
		r.Attrs(func(a slog.Attr) bool {
			switch a.Key {
			case "access_mode":
				mode = a.Value.String()
			case "listen_address":
				addr = a.Value.String()
			case "auth_handler":
				handler = a.Value.String()
			case "auth_required_level":
				level = a.Value.String()
			}
			return true
		})
		if mode != string(accessmode.ModeSingleUser) {
			t.Fatalf("access_mode: %q", mode)
		}
		if addr != "127.0.0.1:9090" {
			t.Fatalf("listen_address: %q", addr)
		}
		if handler != "local_noop" || level != "none" {
			t.Fatalf("auth fields: handler=%q level=%q", handler, level)
		}
	}
	if !saw {
		t.Fatalf("expected info record, got %#v", sink.records)
	}
}

func TestLogBootstrapAccessAuth_propagatesAccessModeError(t *testing.T) {
	t.Parallel()
	var sink recordSink
	log := slog.New(&sink)
	cfg := &config.Config{
		Server: config.ServerConfig{Address: "127.0.0.1:1"},
		Access: config.AccessConfig{Mode: "not-a-mode"},
	}
	err := logBootstrapAccessAuth(context.Background(), log, cfg)
	if err == nil || !errors.Is(err, accessmode.ErrUnknownAccessMode) {
		t.Fatalf("want %v, got %v", accessmode.ErrUnknownAccessMode, err)
	}
	if len(sink.records) != 0 {
		t.Fatalf("expected no log records from logBootstrapAccessAuth on error path, got %#v", sink.records)
	}
}
