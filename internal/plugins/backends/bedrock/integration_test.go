package bedrock_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream/eventstreamapi"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	backend "github.com/aiproxer/aiproxer/internal/plugins/backends/bedrock"
	refbackend "github.com/aiproxer/aiproxer/internal/refbackend/bedrock"
	"github.com/aiproxer/aiproxer/internal/refclient/refclienttest"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func newTestBedrock(t *testing.T, cfg backend.Config) execbackend.Backend {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), backend.DefaultLoadConfigTimeout)
	defer cancel()
	return backend.NewWithContext(ctx, cfg)
}

func TestIntegration_refbackendStreamingText(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	be := newTestBedrock(t, backend.Config{
		Region:          "us-east-1",
		AccessKeyID:     "AKID",
		SecretAccessKey: "SECRET",
		BaseEndpoint:    srv.URL,
		DisableHTTPS:    true,
		HTTPClient:      srv.Client(),
	})
	call := aipapi.Call{
		ID: "int1",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: backend.ID, Model: "anthropic.claude-3-haiku-20240307-v1:0"},
	}
	es, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatal(err)
	}
	col, err := aipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(col.Text.String(), "stream-ok") {
		t.Fatalf("text: %q", col.Text.String())
	}
}

func TestIntegration_refbackendStreamUsage(t *testing.T) {
	t.Parallel()
	streamBody := appendConverseMetadataEvent(t, minimalStreamTextOnly(t))
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		StreamEvents: streamBody,
	}))
	t.Cleanup(srv.Close)

	be := newTestBedrock(t, backend.Config{
		Region:          "us-east-1",
		AccessKeyID:     "AKID",
		SecretAccessKey: "SECRET",
		BaseEndpoint:    srv.URL,
		DisableHTTPS:    true,
		HTTPClient:      srv.Client(),
	})
	call := aipapi.Call{
		ID: "int2",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("ping")},
		}},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: backend.ID, Model: "m"},
	}
	es, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatal(err)
	}
	col, err := aipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	if col.InputTokens != 3 || col.OutputTokens != 7 {
		t.Fatalf("usage: in=%d out=%d", col.InputTokens, col.OutputTokens)
	}
	if col.Text.String() != "stream-ok" {
		t.Fatalf("text: %q", col.Text.String())
	}
}

func minimalStreamTextOnly(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := eventstream.NewEncoder()
	events := []struct {
		eventType string
		payload   map[string]any
	}{
		{"messageStart", map[string]any{"role": "assistant"}},
		{"contentBlockDelta", map[string]any{
			"contentBlockIndex": 0,
			"delta":             map[string]any{"text": "stream-ok"},
		}},
		{"contentBlockStop", map[string]any{"contentBlockIndex": 0}},
		{"messageStop", map[string]any{"stopReason": "end_turn"}},
	}
	for _, ev := range events {
		appendEventFrame(t, &buf, enc, ev.eventType, ev.payload)
	}
	return buf.Bytes()
}

func appendConverseMetadataEvent(t *testing.T, prefix []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	_, _ = buf.Write(prefix)
	enc := eventstream.NewEncoder()
	payload := map[string]any{
		"metrics": map[string]any{"latencyMs": 1},
		"usage": map[string]any{
			"inputTokens":  3,
			"outputTokens": 7,
			"totalTokens":  10,
		},
	}
	appendEventFrame(t, &buf, enc, "metadata", payload)
	return buf.Bytes()
}

func appendEventFrame(t *testing.T, buf *bytes.Buffer, enc *eventstream.Encoder, typ string, payload map[string]any) {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	msg := eventstream.Message{
		Headers: []eventstream.Header{
			{Name: eventstreamapi.MessageTypeHeader, Value: eventstream.StringValue(eventstreamapi.EventMessageType)},
			{Name: eventstreamapi.EventTypeHeader, Value: eventstream.StringValue(typ)},
			{Name: eventstreamapi.ContentTypeHeader, Value: eventstream.StringValue("application/json")},
		},
		Payload: b,
	}
	if err := enc.Encode(buf, msg); err != nil {
		t.Fatal(err)
	}
}

func TestIntegration_refbackendMultimodalRequestBody(t *testing.T) {
	t.Parallel()
	var captured string
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		OnRequestBody: func(b []byte) { captured = string(b) },
	}))
	t.Cleanup(srv.Close)

	png := refclienttest.ReadRefclientFixture(t, "tiny.png")
	pdf := refclienttest.ReadRefclientFixture(t, "minimal.pdf")
	pngB64 := base64.StdEncoding.EncodeToString(png)
	pdfB64 := base64.StdEncoding.EncodeToString(pdf)

	be := newTestBedrock(t, backend.Config{
		Region:          "us-east-1",
		AccessKeyID:     "AKID",
		SecretAccessKey: "SECRET",
		BaseEndpoint:    srv.URL,
		DisableHTTPS:    true,
		HTTPClient:      srv.Client(),
	})
	call := aipapi.Call{
		ID: "mm",
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{
				aipapi.TextPart("x"),
				{Kind: aipapi.PartImageRef, ImageRef: "data:image/png;base64," + pngB64},
				aipapi.FilePart("data:application/pdf;base64,"+pdfB64, "application/pdf", "f.pdf"),
			},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "anthropic.claude-3-haiku-20240307-v1:0"}}
	es, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aipapi.Collect(context.Background(), es); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(captured, pngB64) || !strings.Contains(captured, pdfB64) {
		t.Fatalf("expected multimodal base64 payloads in body: %s", captured)
	}
}

func TestIntegration_refbackendToolUseStream(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	enc := eventstream.NewEncoder()
	events := []struct {
		eventType string
		payload   map[string]any
	}{
		{"messageStart", map[string]any{"role": "assistant"}},
		{"contentBlockStart", map[string]any{
			"contentBlockIndex": 0,
			"start": map[string]any{
				"toolUse": map[string]any{
					"toolUseId": "tool_use_int_1",
					"name":      "get_weather",
					"type":      "tool_use",
				},
			},
		}},
		{"contentBlockDelta", map[string]any{
			"contentBlockIndex": 0,
			"delta": map[string]any{
				"toolUse": map[string]any{"input": `{"city":"NYC"}`},
			},
		}},
		{"contentBlockStop", map[string]any{"contentBlockIndex": 0}},
		{"messageStop", map[string]any{"stopReason": "tool_use"}},
	}
	for _, ev := range events {
		appendEventFrame(t, &buf, enc, ev.eventType, ev.payload)
	}
	streamBody := appendConverseMetadataEvent(t, buf.Bytes())

	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		StreamEvents: streamBody,
	}))
	t.Cleanup(srv.Close)

	be := newTestBedrock(t, backend.Config{
		Region:          "us-east-1",
		AccessKeyID:     "AKID",
		SecretAccessKey: "SECRET",
		BaseEndpoint:    srv.URL,
		DisableHTTPS:    true,
		HTTPClient:      srv.Client(),
	})
	schema := json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`)
	call := aipapi.Call{
		ID: "tool-int",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("weather?")},
		}},
		Tools: []aipapi.ToolDef{{
			Name:        "get_weather",
			Description: "Weather",
			Parameters:  schema,
		}},
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceRequired, Name: "get_weather"},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "anthropic.claude-3-haiku-20240307-v1:0"}}
	es, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatal(err)
	}
	col, err := aipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	tcs := col.OrderedToolCalls()
	if len(tcs) != 1 {
		t.Fatalf("tool calls: %+v", tcs)
	}
	if tcs[0].Name != "get_weather" || tcs[0].ID != "tool_use_int_1" {
		t.Fatalf("tool: %+v", tcs[0])
	}
	if tcs[0].Arguments != `{"city":"NYC"}` {
		t.Fatalf("args: %q", tcs[0].Arguments)
	}
}
