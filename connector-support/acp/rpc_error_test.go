package acp

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestRPCError_Error_stable(t *testing.T) {
	t.Parallel()
	e := &RPCError{Method: "initialize", Code: 42, Message: "dynamic upstream text"}
	if got := e.Error(); got != "acp: initialize: json-rpc error" {
		t.Fatalf("Error() = %q", got)
	}
}

func TestRPCError_Error_nilReceiver(t *testing.T) {
	t.Parallel()
	var e *RPCError
	if got := e.Error(); got != "acp: json-rpc error" {
		t.Fatalf("nil Error() = %q", got)
	}
}

func TestRPCErrFromBody(t *testing.T) {
	t.Parallel()
	if err := rpcErrFromBody("authenticate", nil); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	err := rpcErrFromBody("session/new", &rpcErrorBody{Code: 1, Message: "x"})
	var re *RPCError
	if !errors.As(err, &re) {
		t.Fatalf("want *RPCError, got %T", err)
	}
	if re.Method != "session/new" || re.Code != 1 || re.Message != "x" {
		t.Fatalf("fields: %+v", re)
	}
}

func TestRPCErrFromBody_includesStructuredDetail(t *testing.T) {
	t.Parallel()
	err := rpcErrFromBody("initialize", &rpcErrorBody{
		Code:    -32603,
		Message: "Internal error",
		Data:    json.RawMessage(`{"error":"Gemini quota exhausted for this account"}`),
	})
	var re *RPCError
	if !errors.As(err, &re) {
		t.Fatalf("want *RPCError, got %T", err)
	}
	if got, want := re.Message, "Internal error: Gemini quota exhausted for this account"; got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}
}
