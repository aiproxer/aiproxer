package openresponses

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func wsDialURL(ts *httptest.Server) string {
	u, _ := url.Parse(ts.URL)
	u.Scheme = "ws"
	u.Path = "/responses"
	return u.String()
}

func dialWS(t *testing.T, ts *httptest.Server, apiKey string) *websocket.Conn {
	t.Helper()
	header := http.Header{}
	if apiKey != "" {
		header.Set("Authorization", "Bearer "+apiKey)
	}
	header.Set("Content-Type", "application/json")
	conn, _, err := websocket.DefaultDialer.Dial(wsDialURL(ts), header)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func wsSendCreate(t *testing.T, conn *websocket.Conn, model string) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"type": "response.create", "model": model, "input": "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		t.Fatal(err)
	}
}

func wsReadTurn(t *testing.T, conn *websocket.Conn) ([]string, string) {
	t.Helper()
	var types []string
	var terminalID string
	for {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("ws read: %v", err)
		}
		text := string(msg)
		if strings.TrimSpace(text) == "[DONE]" {
			return types, terminalID
		}
		var evt struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(msg, &evt); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		types = append(types, evt.Type)
		if evt.Type == "response.completed" {
			var full struct {
				Response struct {
					ID string `json:"id"`
				} `json:"response"`
			}
			_ = json.Unmarshal(msg, &full)
			terminalID = full.Response.ID
			return types, terminalID
		}
		if evt.Type == "error" {
			return types, terminalID
		}
	}
}

func TestWS_BasicTurn(t *testing.T) {
	t.Parallel()
	srv, ts := startServer(t, Options{}, &Script{
		ID: "scenario-ws-basic", Description: "basic ws turn", Mode: ModeWebSocket,
		Expected: ExpectedRequest{Model: "m"},
		Resource: NewResource("resp_ws_1", "m", 1719901000, []Item{NewMessagePartsItem("assistant", "", NewTextPart("ws ok"))}),
	})
	conn := dialWS(t, ts, "sk-test")
	wsSendCreate(t, conn, "m")
	types, terminalID := wsReadTurn(t, conn)
	if terminalID != "resp_ws_1" {
		t.Fatalf("terminal id: %q", terminalID)
	}
	if types[len(types)-1] != "response.completed" {
		t.Fatalf("last event: %v", types)
	}
	if srv.Capture().Total() != 1 {
		t.Fatalf("turn count: %d (handshake must not count)", srv.Capture().Total())
	}
}

func TestWS_SequentialTurns(t *testing.T) {
	t.Parallel()
	_, ts := startServer(t, Options{}, &Script{
		ID: "scenario-ws-seq", Description: "sequential ws turns", Mode: ModeWebSocket,
		Expected: ExpectedRequest{Model: "m"},
		Resource: NewResource("resp_ws", "m", 1719901000, []Item{NewMessagePartsItem("assistant", "", NewTextPart("turn"))}),
	})
	conn := dialWS(t, ts, "sk-test")
	for i := range 2 {
		wsSendCreate(t, conn, "m")
		_, terminalID := wsReadTurn(t, conn)
		if terminalID != "resp_ws" {
			t.Fatalf("turn %d terminal: %q", i, terminalID)
		}
	}
}

func TestWS_ContinuationPreviousResponseID(t *testing.T) {
	t.Parallel()
	srv, ts := startServer(t, Options{}, &Script{
		ID: "scenario-ws-cont", Description: "ws continuation", Mode: ModeWebSocket,
		Expected: ExpectedRequest{Model: "m", Contains: []string{`"previous_response_id":"resp_ws_1"`}},
		Resource: NewResource("resp_ws_2", "m", 1719901000, []Item{NewMessagePartsItem("assistant", "", NewTextPart("continued"))}),
	})
	conn := dialWS(t, ts, "sk-test")
	payload, _ := json.Marshal(map[string]any{
		"type": "response.create", "model": "m", "input": "what next?",
		"previous_response_id": "resp_ws_1", "store": false,
	})
	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		t.Fatal(err)
	}
	_, terminalID := wsReadTurn(t, conn)
	if terminalID != "resp_ws_2" {
		t.Fatalf("terminal: %q", terminalID)
	}
	if srv.MismatchCount() != 0 {
		t.Fatalf("mismatch: %d", srv.MismatchCount())
	}
	obs, ok := srv.Capture().Last()
	if !ok || !strings.Contains(string(obs.Body), "resp_ws_1") {
		t.Fatalf("continuation not captured: %+v", obs)
	}
}

func TestWS_ErrorTurn(t *testing.T) {
	t.Parallel()
	srv, ts := startServer(t, Options{}, &Script{
		ID: "scenario-ws-err", Description: "ws error", Mode: ModeWebSocket,
		Expected: ExpectedRequest{Model: "m"},
		Resource: NewResource("r", "m", 1, nil),
		Error:    &ErrorStep{Status: 400, Type: "invalid_request", Code: "previous_response_not_found", Message: "missing", Param: "previous_response_id"},
	})
	conn := dialWS(t, ts, "sk-test")
	wsSendCreate(t, conn, "m")
	types, _ := wsReadTurn(t, conn)
	if types[len(types)-1] != "error" {
		t.Fatalf("expected error event, got %v", types)
	}
	if srv.MismatchCount() != 0 {
		t.Fatalf("mismatch: %d", srv.MismatchCount())
	}
}

func TestWS_MalformedEnvelope(t *testing.T) {
	t.Parallel()
	srv, ts := startServer(t, Options{}, &Script{
		ID: "scenario-ws-mal", Description: "malformed ws envelope", Mode: ModeWebSocket,
		Resource: NewResource("r", "m", 1, nil),
	})
	conn := dialWS(t, ts, "sk-test")
	// Missing type discriminator.
	_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"model":"m"}`))
	types, _ := wsReadTurn(t, conn)
	if types[len(types)-1] != "error" {
		t.Fatalf("expected error for malformed envelope, got %v", types)
	}
	// Wrong type.
	_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.cancel","model":"m"}`))
	types, _ = wsReadTurn(t, conn)
	if types[len(types)-1] != "error" {
		t.Fatalf("expected error for wrong type, got %v", types)
	}
	if srv.MismatchCount() != 0 {
		t.Fatalf("mismatch: %d", srv.MismatchCount())
	}
}

func TestWS_ExpectationMismatch(t *testing.T) {
	t.Parallel()
	srv, ts := startServer(t, Options{}, &Script{
		ID: "scenario-ws-mismatch", Description: "ws expectation mismatch", Mode: ModeWebSocket,
		Expected: ExpectedRequest{Model: "expected"},
		Resource: NewResource("r", "m", 1, nil),
	})
	conn := dialWS(t, ts, "sk-test")
	wsSendCreate(t, conn, "different-model")
	types, _ := wsReadTurn(t, conn)
	if types[len(types)-1] != "error" {
		t.Fatalf("expected error on mismatch, got %v", types)
	}
	if srv.MismatchCount() != 1 {
		t.Fatalf("mismatch count: %d", srv.MismatchCount())
	}
}

func TestWS_AuthRequiredBeforeUpgrade(t *testing.T) {
	t.Parallel()
	_, ts := startServer(t, Options{AllowMissingBearer: false}, &Script{
		ID: "scenario-ws-auth", Description: "ws auth", Mode: ModeWebSocket,
		Resource: NewResource("r", "m", 1, nil),
	})
	// No bearer: upgrade must be refused.
	_, resp, err := websocket.DefaultDialer.Dial(wsDialURL(ts), http.Header{})
	if err == nil {
		t.Fatal("expected dial failure without credentials")
	}
	if resp != nil && resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestWS_ServerObservesClientClose(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	_, ts := startServer(t, Options{}, &Script{
		ID: "scenario-ws-close", Description: "ws client close", Mode: ModeWebSocket,
		Delay:    DelayPlan{BetweenEvents: 40 * time.Millisecond},
		Resource: NewResource("r", "m", 1, []Item{NewMessagePartsItem("assistant", "", NewTextPart("x"))}),
	})
	header := http.Header{"Authorization": []string{"Bearer sk-test"}, "Content-Type": []string{"application/json"}}
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsDialURL(ts), header)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"m","input":"hi"}`))
	// Read a little then close abruptly.
	_, _, _ = conn.ReadMessage()
	_ = conn.Close()
	// The server's read loop must return cleanly (no panic/leak); nothing to assert
	// beyond the connection closing without error on our side.
}
