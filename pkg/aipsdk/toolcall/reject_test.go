package toolcall_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

func TestRejectError_noRawPayload(t *testing.T) {
	t.Parallel()
	err := &toolcall.RejectError{ReasonCode: toolcall.ReasonUnrepairable, ToolCallID: "c1"}
	msg := err.Error()
	if !strings.Contains(msg, toolcall.ReasonUnrepairable) {
		t.Fatalf("msg=%q", msg)
	}
	if strings.Contains(msg, "{") || strings.Contains(msg, "args") {
		t.Fatalf("must not include payload: %q", msg)
	}
	if _, ok := errors.AsType[*toolcall.RejectError](err); !ok {
		t.Fatal("errors.As")
	}
}
