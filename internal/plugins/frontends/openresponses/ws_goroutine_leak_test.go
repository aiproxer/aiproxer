package openresponses_test

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/gorilla/websocket"
	"go.uber.org/goleak"
)

// TestWebSocketSession_NoGoroutineLeak verifies that the session read pump and
// pinger are owned and joined: after valid sessions and shutdown-driven closes,
// no goroutine escapes the frontend package. Servers are closed explicitly
// before the leak check because httptest cleanup runs after the test body.
func TestWebSocketSession_NoGoroutineLeak(t *testing.T) {
	defer goleak.VerifyNone(t)

	cfg := wsTestConfig(nil)
	var servers []*httptest.Server
	defer func() {
		for _, srv := range servers {
			srv.Close()
		}
	}()

	for range 5 {
		srv := httptest.NewServer(openresponses.NewWebSocketHandler(openresponses.WebSocketHandlerConfig{
			AllowUnauthenticated: true, Config: cfg,
		}))
		servers = append(servers, srv)
		conn := wsDial(t, srv, nil)
		if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","store":false}`)); err != nil {
			t.Fatal(err)
		}
		_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				break
			}
		}
		_ = conn.Close()
	}
	time.Sleep(100 * time.Millisecond)
}
