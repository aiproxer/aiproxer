package service

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestEngine_SidecarErrorMatrixIsTypedAndTerminalOnce(t *testing.T) {
	t.Parallel()
	cases := []struct {
		mode      string
		code      backendplugin.ErrorCode
		message   string
		forbidden string
	}{
		{mode: "auth", code: backendplugin.ErrorCodeUnauthenticated},
		{mode: "rate_limit", code: backendplugin.ErrorCodeResourceExhausted},
		{mode: "plan_exhausted", code: backendplugin.ErrorCodeResourceExhausted},
		{
			mode:      "third_party_extra_usage",
			code:      backendplugin.ErrorCodePermissionDenied,
			message:   "Claude requires third-party extra usage for this request",
			forbidden: "raw provider billing detail",
		},
		{mode: "org_policy", code: backendplugin.ErrorCodeUnauthenticated},
		{mode: "mcp_disconnect", code: backendplugin.ErrorCodeUnavailable},
		{mode: "malformed_frame", code: backendplugin.ErrorCodeProtocolViolation},
		{mode: "superseded", code: backendplugin.ErrorCodeFailedPrecondition},
		{
			mode:      "native_tools_exposed",
			code:      backendplugin.ErrorCodeFailedPrecondition,
			message:   "tools outside the admitted caller catalogue",
			forbidden: "raw sidecar native-tools detail",
		},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			t.Parallel()
			eng := testEngine(t, tc.mode)
			defer func() { _ = eng.Close(context.Background()) }()
			stream, err := eng.Open(context.Background(), textCall("anthropic/sonnet"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = stream.Close() }()
			var terminalErr error
			for {
				if _, recvErr := stream.Recv(context.Background()); recvErr != nil {
					terminalErr = recvErr
					break
				}
			}
			var terminal backendplugin.TerminalError
			if !errors.As(terminalErr, &terminal) {
				t.Fatalf("err=%v", terminalErr)
			}
			if got := terminal.TerminalPluginError().Code; got != tc.code {
				t.Fatalf("code=%v want=%v", got, tc.code)
			}
			if terminal.TerminalPluginError().Retryable {
				t.Fatal("retryable=true want=false")
			}
			if tc.message != "" || tc.forbidden != "" {
				message := terminal.TerminalPluginError().Message
				if tc.message != "" && !strings.Contains(message, tc.message) {
					t.Fatalf("message=%q want substring %q", message, tc.message)
				}
				if tc.forbidden != "" && strings.Contains(message, tc.forbidden) {
					t.Fatalf("raw sidecar detail leaked: %q", message)
				}
			}
			if _, err := stream.Recv(context.Background()); !errors.Is(err, io.EOF) {
				t.Fatalf("second terminal=%v", err)
			}
		})
	}
}

func TestEngine_FallbackWarningStaysVisible(t *testing.T) {
	t.Parallel()
	eng := testEngine(t, "fallback_warning")
	defer func() { _ = eng.Close(context.Background()) }()
	stream, err := eng.Open(context.Background(), textCall("anthropic/sonnet"))
	if err != nil {
		t.Fatal(err)
	}
	events, err := collect(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Kind == aipapi.EventWarning && event.WarningCode == "model_refusal_fallback" {
			found = true
		}
	}
	if !found {
		t.Fatalf("events=%v", events)
	}
}

func TestEngine_TimeoutIsTheCallerDeadline(t *testing.T) {
	t.Parallel()
	eng := testEngine(t, "slow")
	defer func() { _ = eng.Close(context.Background()) }()
	stream, err := eng.Open(context.Background(), textCall("anthropic/sonnet"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = stream.Recv(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
}

func TestEngine_TerminalMessagesRemainBounded(t *testing.T) {
	t.Parallel()
	eng := testEngine(t, "error_prose_credential")
	defer func() { _ = eng.Close(context.Background()) }()
	stream, err := eng.Open(context.Background(), textCall("anthropic/sonnet"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	_, err = stream.Recv(context.Background())
	var terminal backendplugin.TerminalError
	if !errors.As(err, &terminal) {
		t.Fatalf("err=%v", err)
	}
	if message := terminal.TerminalPluginError().Message; len(message) > 512 || strings.Contains(message, "sk-ant") {
		t.Fatalf("unbounded terminal message: %q", message)
	}
}
