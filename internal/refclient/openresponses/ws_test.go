package openresponses

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// wsTestServer upgrades connections on /responses and delegates to handle.
func wsTestServer(t *testing.T, handle func(t *testing.T, conn *websocket.Conn)) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
			t.Errorf("ws authorization: %q", got)
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		handle(t, conn)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func wsConn(t *testing.T, srv *httptest.Server) *WSSession {
	t.Helper()
	sess, err := Dial(context.Background(), WSDialOptions{BaseURL: srv.URL, APIKey: "sk-test"})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

func wsWrite(t *testing.T, conn *websocket.Conn, payload map[string]any) {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Errorf("marshal ws payload: %v", err)
		return
	}
	if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
		t.Errorf("ws write: %v", err)
	}
}

// wsReadCreateEnvelope reads and validates a response.create turn envelope.
func wsReadCreateEnvelope(t *testing.T, conn *websocket.Conn) CreateParams {
	t.Helper()
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Errorf("read create: %v", err)
		return CreateParams{}
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(msg, &m); err != nil {
		t.Errorf("unmarshal create envelope: %v", err)
		return CreateParams{}
	}
	rawType := m["type"]
	if string(rawType) != `"response.create"` {
		t.Errorf("turn envelope type: %s", rawType)
	}
	delete(m, "type")
	rest, err := json.Marshal(m)
	if err != nil {
		t.Errorf("remarshal create envelope: %v", err)
		return CreateParams{}
	}
	var params CreateParams
	if err := json.Unmarshal(rest, &params); err != nil {
		t.Errorf("parse create params: %v", err)
	}
	return params
}

// wsEvent builds a raw wire event.
func wsEvent(typeName string, fields map[string]any) map[string]any {
	m := map[string]any{"type": typeName, "sequence_number": 0}
	maps.Copy(m, fields)
	return m
}

// wsSendTextTurn emits a full assistant message lifecycle and terminal response.
func wsSendTextTurn(t *testing.T, conn *websocket.Conn, responseID, model, text string) {
	t.Helper()
	seq := int64(0)
	send := func(m map[string]any) {
		m["sequence_number"] = seq
		seq++
		wsWrite(t, conn, m)
	}
	item := map[string]any{
		"id": "msg_ws_1", "type": "message", "status": "in_progress", "role": "assistant", "content": []any{},
	}
	send(wsEvent("response.created", map[string]any{
		"response": map[string]any{"id": responseID, "object": "response", "created_at": 1719901000, "status": "in_progress", "model": model},
	}))
	send(wsEvent("response.output_item.added", map[string]any{"output_index": 0, "item": item}))
	send(wsEvent("response.content_part.added", map[string]any{"item_id": "msg_ws_1", "output_index": 0, "content_index": 0, "part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}}}))
	send(wsEvent("response.output_text.delta", map[string]any{"item_id": "msg_ws_1", "output_index": 0, "content_index": 0, "delta": text}))
	send(wsEvent("response.output_text.done", map[string]any{"item_id": "msg_ws_1", "output_index": 0, "content_index": 0, "text": text}))
	send(wsEvent("response.content_part.done", map[string]any{"item_id": "msg_ws_1", "output_index": 0, "content_index": 0, "part": map[string]any{"type": "output_text", "text": text, "annotations": []any{}}}))
	send(wsEvent("response.output_item.done", map[string]any{"output_index": 0, "item": map[string]any{
		"id": "msg_ws_1", "type": "message", "status": "completed", "role": "assistant",
		"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}},
	}}))
	send(wsEvent("response.completed", map[string]any{"response": wsFullResponse(responseID, model, text)}))
}

// wsFullResponse builds a complete response resource with required presence.
func wsFullResponse(responseID, model, text string) map[string]any {
	return map[string]any{
		"id": responseID, "object": "response", "created_at": 1719901000, "status": "completed",
		"model": model,
		"output": []any{map[string]any{
			"id": "msg_ws_1", "type": "message", "status": "completed", "role": "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}},
		}},
		"parallel_tool_calls": false, "reasoning": nil, "store": true, "background": false,
		"temperature": 1, "text": map[string]any{}, "tool_choice": "auto", "tools": []any{},
		"top_p": 1, "truncation": "disabled",
		"usage": map[string]any{
			"input_tokens": 1, "input_tokens_details": map[string]any{"cached_tokens": 0},
			"output_tokens": 1, "output_tokens_details": map[string]any{"reasoning_tokens": 0},
			"total_tokens": 2,
		},
		"metadata": map[string]any{}, "service_tier": "default", "max_output_tokens": nil,
		"max_tool_calls": nil, "instructions": nil, "previous_response_id": nil,
		"error": nil, "incomplete_details": nil,
	}
}

func wsErrorTurn(t *testing.T, conn *websocket.Conn, code, param string) {
	t.Helper()
	wsWrite(t, conn, map[string]any{
		"type":   "error",
		"status": 400,
		"error":  map[string]any{"code": code, "message": "previous response not found", "param": param},
	})
}

// wsHold keeps the server connection alive until the client disconnects, so a
// server that already emitted its terminal does not race the client's last reads.
func wsHold(conn *websocket.Conn) {
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

// wsScenarioCases registers the WebSocket scenario cases.
func wsScenarioCases() []scenarioCase {
	return []scenarioCase{
		{
			id:          "scenario-ws-basic",
			kind:        ScenarioWebSocket,
			fixture:     "",
			description: "A single WebSocket turn returns a terminal response.",
			run: func(t *testing.T) {
				t.Helper()
				srv := wsTestServer(t, func(t *testing.T, conn *websocket.Conn) {
					t.Helper()
					params := wsReadCreateEnvelope(t, conn)
					if params.Model != "gpt-openresponses-1" {
						t.Errorf("model: %q", params.Model)
					}
					wsSendTextTurn(t, conn, "resp_ws_1", "gpt-openresponses-1", "hello")
					wsHold(conn)
				})
				sess := wsConn(t, srv)
				turn, err := sess.Turn(context.Background(), CreateParams{Model: "gpt-openresponses-1", Input: Input{Text: "hi"}, Store: new(false)})
				if err != nil {
					t.Fatalf("Turn: %v", err)
				}
				if turn.Response == nil || turn.Response.ID != "resp_ws_1" || turn.Response.Status != "completed" {
					t.Fatalf("turn response: %+v", turn.Response)
				}
				if len(turn.Events) != 8 {
					t.Fatalf("event count: %d", len(turn.Events))
				}
				if turn.Error != nil {
					t.Fatalf("unexpected error: %+v", turn.Error)
				}
			},
		},
		{
			id:          "scenario-ws-sequential",
			kind:        ScenarioWebSocket,
			fixture:     "",
			description: "Sequential turns on one connection produce two terminal responses.",
			run: func(t *testing.T) {
				t.Helper()
				srv := wsTestServer(t, func(t *testing.T, conn *websocket.Conn) {
					t.Helper()
					for i := range 2 {
						_ = wsReadCreateEnvelope(t, conn)
						wsSendTextTurn(t, conn, fmt.Sprintf("resp_seq_%d", i+1), "m", "turn")
					}
				})
				sess := wsConn(t, srv)
				for i := 1; i <= 2; i++ {
					turn, err := sess.Turn(context.Background(), CreateParams{Model: "m", Input: Input{Text: fmt.Sprintf("turn %d", i)}})
					if err != nil {
						t.Fatalf("Turn %d: %v", i, err)
					}
					if turn.Response == nil || turn.Response.ID != fmt.Sprintf("resp_seq_%d", i) {
						t.Fatalf("turn %d response: %+v", i, turn.Response)
					}
				}
			},
		},
		{
			id:          "scenario-ws-continuation",
			kind:        ScenarioContinuation,
			fixture:     "",
			description: "A follow-up turn continues from previous_response_id with only new input.",
			run: func(t *testing.T) {
				t.Helper()
				srv := wsTestServer(t, func(t *testing.T, conn *websocket.Conn) {
					t.Helper()
					_ = wsReadCreateEnvelope(t, conn)
					wsSendTextTurn(t, conn, "resp_cont_1", "m", "remembered")
					second := wsReadCreateEnvelope(t, conn)
					if second.PreviousResponseID == nil || *second.PreviousResponseID != "resp_cont_1" {
						t.Errorf("continuation previous_response_id: %+v", second.PreviousResponseID)
					}
					if second.Input.Text != "What is the code word?" {
						t.Errorf("continuation input: %+v", second.Input)
					}
					wsSendTextTurn(t, conn, "resp_cont_2", "m", "cobalt")
				})
				sess := wsConn(t, srv)
				first, err := sess.Turn(context.Background(), CreateParams{Model: "m", Input: Input{Text: "Remember cobalt. Reply OK."}, Store: new(false)})
				if err != nil {
					t.Fatalf("first turn: %v", err)
				}
				prev := first.Response.ID
				second, err := sess.Turn(context.Background(), CreateParams{
					Model:              "m",
					Input:              Input{Text: "What is the code word?"},
					Store:              new(false),
					PreviousResponseID: &prev,
				})
				if err != nil {
					t.Fatalf("second turn: %v", err)
				}
				if second.Response == nil || second.Response.ID != "resp_cont_2" {
					t.Fatalf("continuation response: %+v", second.Response)
				}
			},
		},
		{
			id:          "scenario-ws-previous-not-found",
			kind:        ScenarioNegative,
			fixture:     "",
			description: "Missing local previous response yields previous_response_not_found error code.",
			run: func(t *testing.T) {
				t.Helper()
				srv := wsTestServer(t, func(t *testing.T, conn *websocket.Conn) {
					t.Helper()
					_ = wsReadCreateEnvelope(t, conn)
					wsErrorTurn(t, conn, "previous_response_not_found", "previous_response_id")
				})
				sess := wsConn(t, srv)
				missing := "resp_openresponses_missing"
				turn, err := sess.Turn(context.Background(), CreateParams{
					Model:              "m",
					Input:              Input{Text: "continue"},
					Store:              new(false),
					PreviousResponseID: &missing,
				})
				if err != nil {
					t.Fatalf("Turn: %v", err)
				}
				if turn.ErrorCode != "previous_response_not_found" {
					t.Fatalf("error code: %q", turn.ErrorCode)
				}
				if turn.Error == nil || turn.Error.Param != "previous_response_id" {
					t.Fatalf("error: %+v", turn.Error)
				}
			},
		},
		{
			id:          "scenario-ws-compact-new-chain",
			kind:        ScenarioCompaction,
			fixture:     "",
			description: "Standalone compact output seeds a new WS chain without previous_response_id.",
			run: func(t *testing.T) {
				t.Helper()
				compactData := mustReadScenario(t, "compact_resource.json")
				srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
					if strings.HasSuffix(r.URL.Path, "/compact") {
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write(compactData)
						return
					}
					http.NotFound(w, r)
				})
				cli := newTestClient(t, srv.URL)
				compact, err := cli.Compact(context.Background(), CompactParams{Model: "m", Input: Input{Items: []Item{NewMessageItem("user", "input_text", "compress")}}})
				if err != nil {
					t.Fatalf("Compact: %v", err)
				}

				wsSrv := wsTestServer(t, func(t *testing.T, conn *websocket.Conn) {
					t.Helper()
					params := wsReadCreateEnvelope(t, conn)
					if params.PreviousResponseID != nil {
						t.Errorf("compact new chain must omit previous_response_id: %+v", params.PreviousResponseID)
					}
					if len(params.Input.Items) == 0 {
						t.Fatal("compact new chain must seed compacted input")
					}
					wsSendTextTurn(t, conn, "resp_compact_ws", "m", "compacted")
					wsHold(conn)
				})
				sess := wsConn(t, wsSrv)
				input := compact.Output
				input = append(input, NewMessageItem("user", "input_text", "Continue from here."))
				turn, err := sess.Turn(context.Background(), CreateParams{Model: "m", Input: Input{Items: input}, Store: new(false)})
				if err != nil {
					t.Fatalf("Turn: %v", err)
				}
				if turn.Response == nil || turn.Response.ID != "resp_compact_ws" {
					t.Fatalf("compact new chain response: %+v", turn.Response)
				}
			},
		},
		{
			id:          "scenario-ws-cancellation",
			kind:        ScenarioNegative,
			fixture:     "",
			description: "Cancelled turn context aborts before a terminal arrives.",
			run: func(t *testing.T) {
				t.Helper()
				srv := wsTestServer(t, func(t *testing.T, conn *websocket.Conn) {
					t.Helper()
					_ = wsReadCreateEnvelope(t, conn)
					// Server stalls forever; client must cancel the turn.
					for {
						if _, _, err := conn.ReadMessage(); err != nil {
							return
						}
					}
				})
				sess := wsConn(t, srv)
				ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
				defer cancel()
				if _, err := sess.Turn(ctx, CreateParams{Model: "m", Input: Input{Text: "hi"}}); err == nil {
					t.Fatal("expected cancellation error")
				}
			},
		},
	}
}

// TestWSTurn_RejectsClosedSession ensures turns fail cleanly after Close.
func TestWSTurn_RejectsClosedSession(t *testing.T) {
	t.Parallel()
	srv := wsTestServer(t, func(t *testing.T, conn *websocket.Conn) {
		t.Helper()
		_ = wsReadCreateEnvelope(t, conn)
		wsSendTextTurn(t, conn, "resp_1", "m", "ok")
		wsHold(conn)
	})
	sess := wsConn(t, srv)
	if _, err := sess.Turn(context.Background(), CreateParams{Model: "m", Input: Input{Text: "hi"}}); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := sess.Turn(context.Background(), CreateParams{Model: "m", Input: Input{Text: "again"}}); err == nil {
		t.Fatal("expected error on closed session")
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("double Close must be idempotent: %v", err)
	}
}
