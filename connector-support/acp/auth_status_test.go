package acp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func retainedOptions() SessionUpdateMapperOptions {
	return SessionUpdateMapperOptions{RetainRPCError: true}
}

func asRPCErrorTest(err error) (*RPCError, bool) {
	rpc, ok := errors.AsType[*RPCError](err)
	return rpc, ok && rpc != nil
}

func TestMapAuthStatusToEvent_RedactedAccount(t *testing.T) {
	t.Parallel()
	params := map[string]any{"authStatus": map[string]any{
		"kind":  "account",
		"label": "Claude Max",
		"account": map[string]any{
			"plan":         "max",
			"email":        "operator@example.com",
			"organization": "Example Org",
		},
	}}
	ev, ok := mapAuthStatusToEvent(params)
	if !ok || ev.Kind != aipapi.EventWarning {
		t.Fatalf("got %#v, %v", ev, ok)
	}
	if ev.WarningCode != "auth_status" {
		t.Fatalf("code = %q", ev.WarningCode)
	}
	for _, leaked := range []string{"operator@example.com", "Example Org"} {
		if strings.Contains(ev.WarningMessage, leaked) {
			t.Fatalf("account identity leaked in %q", ev.WarningMessage)
		}
	}
	for _, want := range []string{"account", "Claude Max"} {
		if !strings.Contains(ev.WarningMessage, want) {
			t.Fatalf("message %q must carry %q", ev.WarningMessage, want)
		}
	}
}

func TestMapAuthStatusToEvent_Kinds(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"api_key", "none", "external", "gateway"} {
		ev, ok := mapAuthStatusToEvent(map[string]any{"authStatus": map[string]any{"kind": kind}})
		if !ok || ev.Kind != aipapi.EventWarning {
			t.Fatalf("%s: got %#v, %v", kind, ev, ok)
		}
		if !strings.Contains(ev.WarningMessage, kind) {
			t.Fatalf("%s: message %q", kind, ev.WarningMessage)
		}
	}
}

func TestMapAuthStatusToEvent_DropsGarbage(t *testing.T) {
	t.Parallel()
	for _, params := range []map[string]any{
		nil,
		{},
		{"authStatus": "account"},
		{"authStatus": map[string]any{"kind": "root-admin"}},
		{"authStatus": map[string]any{"kind": 42}},
		{"authStatus": map[string]any{"kind": "account", "label": "x\x00y"}},
		{"authStatus": map[string]any{"kind": "account", "account": map[string]any{"plan": 42}}},
	} {
		if ev, ok := mapAuthStatusToEvent(params); ok {
			t.Fatalf("garbage must drop, got %#v", ev)
		}
	}
}

func TestParseNDJSONLine_AuthStatusNotification(t *testing.T) {
	t.Parallel()
	line := `{"jsonrpc":"2.0","method":"_auth/status_update","params":{"authStatus":{"kind":"api_key","label":"Anthropic API key"}}}`
	evs, err := parseNDJSONLine(context.Background(), mergeMapperOptions(Config{}), line, 99)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != aipapi.EventWarning || evs[0].WarningCode != "auth_status" {
		t.Fatalf("got %#v", evs)
	}
}

func TestParseNDJSONLine_RetainsErrorKind(t *testing.T) {
	t.Parallel()
	line := `{"jsonrpc":"2.0","id":7,"error":{"code":-32603,"message":"Credit balance is too low","data":{"errorKind":"billing_error"}}}`
	_, err := parseNDJSONLine(context.Background(), retainedOptions(), line, 7)
	rpc, ok := asRPCErrorTest(err)
	if !ok {
		t.Fatalf("want retained RPC error, got %#v", err)
	}
	if rpc.ErrorKind != "billing_error" {
		t.Fatalf("errorKind = %q", rpc.ErrorKind)
	}
}

func TestParseNDJSONLine_ErrorKindBounded(t *testing.T) {
	t.Parallel()
	line := `{"jsonrpc":"2.0","id":7,"error":{"code":-32603,"message":"x","data":{"errorKind":"` + strings.Repeat("k", 200) + `"}}}`
	_, err := parseNDJSONLine(context.Background(), retainedOptions(), line, 7)
	rpc, ok := asRPCErrorTest(err)
	if !ok {
		t.Fatalf("want retained RPC error, got %#v", err)
	}
	if rpc.ErrorKind != "" {
		t.Fatalf("overlong errorKind must drop, got %q", rpc.ErrorKind)
	}
}
