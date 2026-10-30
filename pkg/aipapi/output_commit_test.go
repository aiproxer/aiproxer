package aipapi_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestOutputCommitted(t *testing.T) {
	t.Parallel()
	if !aipapi.OutputCommitted(aipapi.Event{Kind: aipapi.EventTextDelta}) {
		t.Fatal("text delta commits")
	}
	if !aipapi.OutputCommitted(aipapi.Event{Kind: aipapi.EventToolCallStarted}) {
		t.Fatal("tool start commits")
	}
	if aipapi.OutputCommitted(aipapi.Event{Kind: aipapi.EventResponseStarted}) {
		t.Fatal("response_started does not commit for failover")
	}
	if aipapi.OutputCommitted(aipapi.Event{Kind: aipapi.EventMessageStarted}) {
		t.Fatal("message_started does not commit for failover")
	}
	if !aipapi.OutputCommitted(aipapi.Event{Kind: aipapi.EventAssistantImageRef}) {
		t.Fatal("assistant_image_ref commits")
	}
}
