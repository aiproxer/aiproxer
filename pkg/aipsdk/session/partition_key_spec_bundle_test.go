package session_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

func TestSessionView_PartitionKey_prefersAuthoritativeSessionID(t *testing.T) {
	t.Parallel()
	v := session.SessionView{
		AuthoritativeSessionID: "auth-1",
		ClientSessionHint:      "hint-9",
	}
	if got := v.PartitionKey(); got != "auth-1" {
		t.Fatalf("got %q", got)
	}
}

func TestSessionView_PartitionKey_fallsBackToTrimmedHint(t *testing.T) {
	t.Parallel()
	v := session.SessionView{ClientSessionHint: "  convo-x  "}
	if got := v.PartitionKey(); got != "convo-x" {
		t.Fatalf("got %q", got)
	}
}

func TestSessionView_PartitionKey_emptyWhenUnset(t *testing.T) {
	t.Parallel()
	var v session.SessionView
	if v.PartitionKey() != "" {
		t.Fatalf("got %q", v.PartitionKey())
	}
}
