package hooks

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func FuzzHookMutationValidators(f *testing.F) {
	f.Add([]byte("call\x00"))
	f.Add([]byte("ev\x01"))
	f.Add([]byte{0, 0, 0})

	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) == 0 {
			return
		}
		switch b[0] % 2 {
		case 0:
			txt := string(b[1:])
			if len(txt) > 1<<16 {
				txt = txt[:1<<16]
			}
			if txt == "" {
				txt = "x"
			}
			call := &aipapi.Call{
				Route:    aipapi.RouteIntent{Selector: "stub:m"},
				Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(txt)}}},
			}
			_ = ValidateCallAfterRequestHooks("fuzz", call)
		default:
			ev := eventFromFuzzBytes(b[1:])
			_ = ValidateEventAfterResponseHook("fuzz", ev)
		}
	})
}

func eventFromFuzzBytes(b []byte) *aipapi.Event {
	kinds := []aipapi.EventKind{
		aipapi.EventResponseStarted,
		aipapi.EventMessageStarted,
		aipapi.EventTextDelta,
		aipapi.EventReasoningDelta,
		aipapi.EventReasoningSignatureDelta,
		aipapi.EventReasoningOpaqueDelta,
		aipapi.EventReasoningPart,
		aipapi.EventToolCallStarted,
		aipapi.EventToolCallArgsDelta,
		aipapi.EventToolCallFinished,
		aipapi.EventUsageDelta,
		aipapi.EventWarning,
		aipapi.EventError,
		aipapi.EventResponseFinished,
		aipapi.EventKind("unknown_kind_from_fuzz"),
	}
	idx := 0
	if len(b) > 0 {
		idx = int(b[0]) % len(kinds)
	}
	k := kinds[idx]
	delta := string(b)
	if len(delta) > 4096 {
		delta = delta[:4096]
	}
	tid := "tool-id"
	if len(b) > 1 {
		tid = string(b[1:])
		if tid == "" {
			tid = "t"
		}
		if len(tid) > 512 {
			tid = tid[:512]
		}
	}
	return &aipapi.Event{
		Kind:         k,
		Delta:        delta,
		ToolCallID:   tid,
		ToolName:     delta,
		WarningCode:  delta,
		ErrorCode:    delta,
		ErrorMessage: delta,
	}
}
