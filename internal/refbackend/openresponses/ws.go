package openresponses

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gorilla/websocket"
)

// wsTurnSeq bounds the number of sequential turns served on one connection.
const wsTurnSeq = 256

// serveWS upgrades an authenticated GET /responses connection and serves
// sequential response.create turns against the active script.
func (s *Server) serveWS(w http.ResponseWriter, r *http.Request) {
	script, err := s.ActiveScript()
	if err != nil {
		s.writeError(w, http.StatusServiceUnavailable, ErrorObject{Type: "server_error", Code: "no_active_script", Message: "no script selected"})
		return
	}
	if code, eo := s.authorize(r); eo != nil {
		s.writeError(w, code, *eo)
		return
	}
	s.cap.RecordHandshake(r)

	if script.Expected.Auth == AuthNone {
		s.mismatches.Add(1)
		s.writeError(w, http.StatusBadRequest, ErrorObject{Type: "invalid_request", Code: "expectation_mismatch", Message: "authorization: must be absent"})
		return
	}

	upgrader := websocket.Upgrader{}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Warn("refbackend/openresponses: websocket upgrade failed", "error", err)
		return
	}
	defer func() { _ = conn.Close() }()

	ctx := context.Background()
	for range wsTurnSeq {
		mt, msg, rerr := conn.ReadMessage()
		if rerr != nil {
			return
		}
		if mt != websocket.TextMessage && mt != websocket.BinaryMessage {
			s.writeWSError(conn, 400, "invalid_request", "invalid_frame", "only text/binary frames supported", "")
			continue
		}
		s.cap.RecordFrame("/responses", nil, msg)

		req, perr := parseWSTurn(msg)
		if perr != nil {
			s.writeWSError(conn, 400, "invalid_request", "invalid_envelope", perr.Error(), "type")
			continue
		}
		if fails := checkExpected(script.Expected, r, req, msg); len(fails) > 0 {
			s.mismatches.Add(1)
			s.writeWSError(conn, 400, "invalid_request", "expectation_mismatch", strings.Join(fails, "; "), "")
			continue
		}
		if script.Error != nil {
			eo := errorObjectFromStep(script.Error)
			s.writeWSError(conn, errorStatus(script.Error), eo.Type, eo.Code, eo.Message, eo.Param)
			continue
		}
		if err := s.writeWSTurn(ctx, conn, script); err != nil {
			s.writeErrs.Add(1)
			return
		}
	}
}

// writeWSTurn writes the script's stream as text frames. A terminal response
// event ends the turn; no trailing [DONE] frame is sent so sequential turns on
// one connection do not leave a stray frame in the client's receive buffer.
func (s *Server) writeWSTurn(ctx context.Context, conn *websocket.Conn, script *Script) error {
	events := s.eventsFor(script)
	for i, ev := range events {
		if err := s.delay(ctx, script, i); err != nil {
			s.cancelled.Add(1)
			return nil
		}
		payload, err := ev.renderPayload()
		if err != nil {
			return err
		}
		if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) writeWSError(conn *websocket.Conn, status int, typ, code, message, param string) {
	payload, err := json.Marshal(map[string]any{
		"type":   "error",
		"status": status,
		"error":  ErrorObject{Type: typ, Code: code, Message: message, Param: param},
	})
	if err != nil {
		return
	}
	_ = conn.WriteMessage(websocket.TextMessage, payload)
}

// parseWSTurn parses a response.create turn envelope into a CreateRequest.
func parseWSTurn(msg []byte) (*CreateRequest, error) {
	m, err := decodeObject(msg)
	if err != nil {
		return nil, err
	}
	rawType, ok := m["type"]
	if !ok {
		return nil, malformedf("turn envelope missing type discriminator")
	}
	if strings.TrimSpace(string(rawType)) != `"response.create"` {
		return nil, malformedf("turn envelope type must be response.create")
	}
	delete(m, "type")
	delete(m, "sequence_number")
	return parseCreateRequestObject(m)
}
