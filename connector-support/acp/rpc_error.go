package acp

import (
	"encoding/json"
	"strings"
	"unicode"
)

// RPCError is a JSON-RPC error object returned by an ACP agent. Error() stays
// stable for aggregation. Message is operator-facing and may include a concise
// detail extracted from the response's data field. ErrorKind carries the
// adapter's categorical error kind (for example the Claude adapter's
// data.errorKind) for local classification; it is empty when absent or
// malformed and never holds free-form text.
type RPCError struct {
	Method    string
	Code      int64
	Message   string
	ErrorKind string
}

func (e *RPCError) Error() string {
	if e == nil {
		return "acp: json-rpc error"
	}
	if m := e.Method; m != "" {
		return "acp: " + m + ": json-rpc error"
	}
	return "acp: json-rpc error"
}

func rpcErrFromBody(method string, body *rpcErrorBody) error {
	if body == nil {
		return nil
	}
	return &RPCError{
		Method:    method,
		Code:      int64(body.Code),
		Message:   formatACPErrorBody(body),
		ErrorKind: errorKindFromBody(body),
	}
}

// maxRPCErrorKindBytes bounds the retained categorical error kind.
const maxRPCErrorKindBytes = 64

// ErrorKindFromData extracts the adapter's categorical error kind from a raw
// JSON-RPC error body. It never fails: unparseable or missing kinds yield "".
func ErrorKindFromData(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return ""
	}
	return errorKindFromData(env.Data)
}

// errorKindFromData extracts the kind from a raw data envelope with the same
// bounds as errorKindFromBody.
func errorKindFromData(data json.RawMessage) string {
	if len(data) == 0 || string(data) == "null" {
		return ""
	}
	return errorKindFromBody(&rpcErrorBody{Data: data})
}

// errorKindFromBody extracts the adapter's categorical error kind from the
// response data envelope. Only short single-token strings qualify; anything
// else drops to empty so free-form text can never ride the kind field.
func errorKindFromBody(body *rpcErrorBody) string {
	if body == nil || len(body.Data) == 0 || string(body.Data) == "null" {
		return ""
	}
	var data struct {
		ErrorKind string `json:"errorKind"`
	}
	if err := json.Unmarshal(body.Data, &data); err != nil {
		return ""
	}
	kind := strings.TrimSpace(data.ErrorKind)
	if kind == "" || len(kind) > maxRPCErrorKindBytes {
		return ""
	}
	for _, c := range kind {
		if c <= ' ' || c == '"' || c == '\\' || unicode.IsControl(c) {
			return ""
		}
	}
	return kind
}

func formatACPErrorBody(body *rpcErrorBody) string {
	message := strings.TrimSpace(body.Message)
	if message == "" {
		message = "unknown error"
	}
	if len(body.Data) == 0 || string(body.Data) == "null" {
		return message
	}

	var data any
	if err := json.Unmarshal(body.Data, &data); err != nil {
		return message
	}
	return formatACPError(map[string]any{"message": message, "data": data})
}
