package auth

import (
	"fmt"
	"strings"
	"testing"
)

func TestInboundCallMeta_String_redactsSensitiveFields(t *testing.T) {
	t.Parallel()
	secret := "bearer-hunter2-secret"
	hint := "session-resume-TOKEN"
	m := InboundCallMeta{
		TraceID: "tr", Frontend: "fe", Method: "GET", Path: "/x",
		ClientAddr: "127.0.0.1:9", AuthorizationBearer: secret, SessionHint: hint,
	}
	s := m.String()
	if strings.Contains(s, secret) {
		t.Fatalf("String() leaked AuthorizationBearer: %q", s)
	}
	if strings.Contains(s, hint) {
		t.Fatalf("String() leaked SessionHint: %q", s)
	}
	gs := fmt.Sprintf("%#v", m)
	if strings.Contains(gs, secret) || strings.Contains(gs, hint) {
		t.Fatalf("GoString leaked secret material: %q", gs)
	}
}

func TestInboundCallMeta_String_emptyOptionalFieldsOmitted(t *testing.T) {
	t.Parallel()
	m := InboundCallMeta{Method: "GET", Path: "/v1", TraceID: "t1"}
	s := m.String()
	if !strings.Contains(s, "GET") {
		t.Fatalf("expected method: %q", s)
	}
}
