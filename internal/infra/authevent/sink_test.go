package authevent

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
)

func TestNewSlogEventSink_nilLogger(t *testing.T) {
	t.Parallel()
	if _, err := NewSlogEventSink(nil); err == nil {
		t.Fatal("expected error for nil logger")
	}
}

func TestSlogEventSink_OnAuthDecision_nilReceiverOrLogger_noPanic(t *testing.T) {
	t.Parallel()
	var nilSink *SlogEventSink
	if err := nilSink.OnAuthDecision(context.Background(), sdkauth.AuthDecisionEvent{}); err != nil {
		t.Fatalf("nil receiver: %v", err)
	}
	bad := &SlogEventSink{log: nil}
	if err := bad.OnAuthDecision(context.Background(), sdkauth.AuthDecisionEvent{}); err != nil {
		t.Fatalf("nil logger: %v", err)
	}
	if err := bad.OnSessionStart(context.Background(), sdkauth.SessionStartEvent{}); err != nil {
		t.Fatalf("OnSessionStart: %v", err)
	}
	if err := nilSink.OnSessionStart(context.Background(), sdkauth.SessionStartEvent{}); err != nil {
		t.Fatalf("nil receiver session: %v", err)
	}
}

func TestSlogEventSink_OnAuthDecision_emitsJSONRecord(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	sink, err := NewSlogEventSink(log)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ev := sdkauth.AuthDecisionEvent{
		Time:       time.Unix(1700000000, 0).UTC(),
		TraceID:    "tr-1",
		AccessMode: sdkauth.AccessSingleUser,
		Outcome:    sdkauth.OutcomeAllow,
		Frontend:   "openai",
		ReasonCode: "ok",
	}
	if err := sink.OnAuthDecision(ctx, ev); err != nil {
		t.Fatalf("OnAuthDecision: %v", err)
	}
	line := buf.String()
	if !strings.Contains(line, msgAuthDecision) {
		t.Fatalf("missing message: %q", line)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &obj); err != nil {
		t.Fatalf("json: %v\nline=%s", err, line)
	}
	if obj["msg"] != msgAuthDecision {
		t.Fatalf("msg: got %v", obj["msg"])
	}
	if obj[attrAIPRequestTraceID] != "tr-1" {
		t.Fatalf("%s: got %v", attrAIPRequestTraceID, obj[attrAIPRequestTraceID])
	}
}

func TestSlogEventSink_OnSessionStart_emitsJSONRecord(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	sink, err := NewSlogEventSink(log)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ev := sdkauth.SessionStartEvent{
		Time:       time.Unix(1700000001, 0).UTC(),
		TraceID:    "tr-2",
		SessionID:  "sid-9",
		AccessMode: sdkauth.AccessMultiUser,
		Frontend:   "anthropic",
	}
	if err := sink.OnSessionStart(ctx, ev); err != nil {
		t.Fatalf("OnSessionStart: %v", err)
	}
	line := buf.String()
	if !strings.Contains(line, msgSessionStart) {
		t.Fatalf("missing message: %q", line)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &obj); err != nil {
		t.Fatalf("json: %v\nline=%s", err, line)
	}
	if obj[attrAIPRequestTraceID] != "tr-2" {
		t.Fatalf("%s: got %v", attrAIPRequestTraceID, obj[attrAIPRequestTraceID])
	}
}

func TestSlogEventSink_principalSafeClaims_logsKeysOnly(t *testing.T) {
	t.Parallel()
	secretLike := "sk-fixture-NEVER_EMIT_THIS_VALUE_zzzz"
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	sink, err := NewSlogEventSink(log)
	if err != nil {
		t.Fatal(err)
	}
	ev := sdkauth.AuthDecisionEvent{
		Time:    time.Unix(1, 0).UTC(),
		TraceID: "t",
		Outcome: sdkauth.OutcomeDeny,
		PrincipalSafeClaims: map[string]string{
			"org": secretLike,
		},
	}
	if err := sink.OnAuthDecision(context.Background(), ev); err != nil {
		t.Fatalf("OnAuthDecision: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, secretLike) {
		t.Fatalf("secret value leaked into log: %s", out)
	}
	if !strings.Contains(out, "org") {
		t.Fatalf("expected claim key org in log keys attr: %s", out)
	}
}
