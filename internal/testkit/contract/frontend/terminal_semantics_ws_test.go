package frontend

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	frontopenresponses "github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/gorilla/websocket"
)

// wsTerminalTimeout bounds one WS terminal drive. It is a read deadline, not
// synchronization: frames arrive via the session pump, and the deadline only
// fails a wedged turn.
const wsTerminalTimeout = 5 * time.Second

// wsTerminalType is terminal when the turn is over on the WS wire: one of the
// three response terminals or a classified error envelope.
func wsTerminalType(typ string) bool {
	switch typ {
	case "response.completed", "response.incomplete", "response.failed", "error":
		return true
	}
	return false
}

// driveTerminalWS sends one response.create turn to the openresponses
// WebSocket transport with a scripted canonical stream and returns every WS
// text frame observed until the terminal frame, joined by newlines. It drives
// the production WebSocketHandler + SessionRunner over a real httptest server
// and a real gorilla dial, mirroring websocket_turn_test.go; the only fake is
// the scripted executor. No sleeps: reads are bounded by a read deadline and
// the frame count is capped.
func driveTerminalWS(t *testing.T, events []aipapi.Event) string {
	t.Helper()
	executor := &CapturingExecutor{Script: EventScript{Events: events}}
	runner := frontopenresponses.NewSessionRunner(frontopenresponses.SessionRunnerConfig{
		Executor: executor,
	})
	handler := frontopenresponses.NewWebSocketHandler(frontopenresponses.WebSocketHandlerConfig{
		AllowUnauthenticated: true,
		Config: frontopenresponses.Config{
			Profile:  frontopenresponses.DefaultProfile,
			BasePath: frontopenresponses.DefaultBasePath,
			WebSocket: frontopenresponses.WebSocketConfig{
				Enabled:          true,
				MaxConnectionAge: frontopenresponses.DefaultMaxConnectionAge,
				IdleTimeout:      frontopenresponses.DefaultIdleTimeout,
				MaxQueuedTurns:   frontopenresponses.DefaultMaxQueuedTurns,
			},
		},
		Runner: runner,
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	conn, _, err := (&websocket.Dialer{HandshakeTimeout: wsTerminalTimeout}).Dial(
		"ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("openresponses ws dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	if err := conn.WriteMessage(websocket.TextMessage,
		[]byte(`{"type":"response.create","model":"m","input":"hi"}`)); err != nil {
		t.Fatalf("openresponses ws write response.create: %v", err)
	}
	var raws []string
	for range 128 {
		_ = conn.SetReadDeadline(time.Now().Add(wsTerminalTimeout))
		mt, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("openresponses ws read: %v (frames=%d)", err, len(raws))
		}
		if mt != websocket.TextMessage {
			continue
		}
		var frame struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &frame); err != nil {
			t.Fatalf("openresponses ws frame is not a JSON object: %s", data)
		}
		raws = append(raws, string(data))
		if wsTerminalType(frame.Type) {
			return strings.Join(raws, "\n")
		}
	}
	t.Fatalf("openresponses ws turn did not terminate within 128 frames")
	return ""
}
