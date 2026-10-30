package logging_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/logging"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestNewLogger_JSONContainsMessageAndLevel(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log, err := logging.NewLogger(config.LoggingConfig{
		Level:  "info",
		Format: "json",
	}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	log.Info("hello", "k", "v")
	line := strings.TrimSpace(buf.String())
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("json: %v\nline: %s", err, line)
	}
	if m["msg"] != "hello" {
		t.Fatalf("msg: %v", m["msg"])
	}
	if m["level"] != "INFO" && m["level"] != "info" {
		t.Fatalf("level: %v", m["level"])
	}
}

func TestNewLogger_recordsErrorField(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log, err := logging.NewLogger(config.LoggingConfig{
		Level:  "info",
		Format: "json",
	}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	log.Error("boom", "error", sampleError{})
	line := strings.TrimSpace(buf.String())
	if !strings.Contains(line, `"msg":"boom"`) && !strings.Contains(line, `"msg": "boom"`) {
		t.Fatalf("expected msg in line: %s", line)
	}
	if !strings.Contains(line, "error") {
		t.Fatalf("expected error key in line: %s", line)
	}
}

// An error attribute is logged as its message only: no stack trace (which would
// carry build-host paths and cost a runtime.Callers walk per record).
func TestNewLogger_errorFieldIsPlainMessage(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log, err := logging.NewLogger(config.LoggingConfig{Level: "info", Format: "json"}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	log.Error("boom", "error", sampleError{})
	var m map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &m); err != nil {
		t.Fatalf("json: %v\nline: %s", err, buf.String())
	}
	if m["error"] != "sample" {
		t.Fatalf("error field = %#v, want %q", m["error"], "sample")
	}
}

type sampleError struct{}

func (sampleError) Error() string { return "sample" }

func TestNewLogger_withOTELTraceAttrs(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log, err := logging.NewLogger(config.LoggingConfig{Level: "info", Format: "json"}, &buf,
		logging.WithOTELTraceAttrs(true))
	if err != nil {
		t.Fatal(err)
	}
	tp := sdktrace.NewTracerProvider()
	tr := tp.Tracer("t")
	ctx, span := tr.Start(context.Background(), "op")
	defer span.End()
	log.InfoContext(ctx, "otel")
	line := strings.TrimSpace(buf.String())
	wantT := `"trace_id"`
	wantS := `"span_id"`
	if !strings.Contains(line, wantT) || !strings.Contains(line, wantS) {
		t.Fatalf("missing trace correlation in log line: %s", line)
	}
}
