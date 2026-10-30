package hooks

import (
	"fmt"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// ValidateCallAfterRequestHooks re-validates the canonical call after request-part hooks.
func ValidateCallAfterRequestHooks(hookID string, call *aipapi.Call) error {
	if call == nil {
		return &aipapi.HookMutationError{HookID: hookID, Details: "nil call"}
	}
	if err := call.Validate(); err != nil {
		return &aipapi.HookMutationError{
			HookID:  hookID,
			Details: "call failed validation after request part hook",
			Cause:   err,
		}
	}
	return nil
}

// ValidateEventAfterResponseHook checks a single event remains structurally legal after a response hook.
func ValidateEventAfterResponseHook(hookID string, ev *aipapi.Event) error {
	if ev == nil {
		return &aipapi.HookMutationError{HookID: hookID, Details: "nil event"}
	}
	switch ev.Kind {
	case aipapi.EventResponseStarted, aipapi.EventMessageStarted,
		aipapi.EventTextDelta, aipapi.EventReasoningDelta, aipapi.EventReasoningSignatureDelta, aipapi.EventReasoningOpaqueDelta,
		aipapi.EventReasoningPart,
		aipapi.EventToolCallStarted, aipapi.EventToolCallArgsDelta, aipapi.EventToolCallFinished,
		aipapi.EventAssistantImageRef, aipapi.EventAssistantFileRef,
		aipapi.EventUsageDelta, aipapi.EventWarning, aipapi.EventError, aipapi.EventResponseFinished:
	default:
		return &aipapi.HookMutationError{HookID: hookID, Details: fmt.Sprintf("unknown event kind %q", ev.Kind)}
	}
	switch ev.Kind {
	case aipapi.EventToolCallStarted, aipapi.EventToolCallArgsDelta, aipapi.EventToolCallFinished:
		if ev.ToolCallID == "" {
			return &aipapi.HookMutationError{
				HookID:  hookID,
				Details: fmt.Sprintf("%s requires a non-empty tool call id", ev.Kind),
			}
		}
	}
	if err := aipapi.ValidateEventEnvelope(ev); err != nil {
		return &aipapi.HookMutationError{HookID: hookID, Details: err.Error(), Cause: err}
	}
	return nil
}

// ValidateToolEventAfterPolicy checks a tool-policy decision has not produced an illegal tool event.
func ValidateToolEventAfterPolicy(policyID string, te *aipapi.ToolEvent) error {
	if te == nil {
		return &aipapi.HookMutationError{HookID: policyID, Details: "nil tool event"}
	}
	switch te.Kind {
	case aipapi.ToolEventStarted, aipapi.ToolEventArgsDelta, aipapi.ToolEventFinished:
	default:
		return &aipapi.HookMutationError{HookID: policyID, Details: fmt.Sprintf("unknown tool event kind %q", te.Kind)}
	}
	if te.ToolCallID == "" {
		return &aipapi.HookMutationError{
			HookID:  policyID,
			Details: fmt.Sprintf("%s requires a non-empty tool call id", te.Kind),
		}
	}
	env := &aipapi.Event{
		Kind:       eventKindFromToolEventKind(te.Kind),
		ToolCallID: te.ToolCallID,
		ToolName:   te.ToolName,
		Delta:      te.ArgsDelta,
	}
	if err := aipapi.ValidateEventEnvelope(env); err != nil {
		return &aipapi.HookMutationError{HookID: policyID, Details: err.Error(), Cause: err}
	}
	return nil
}

func eventKindFromToolEventKind(k aipapi.ToolEventKind) aipapi.EventKind {
	switch k {
	case aipapi.ToolEventStarted:
		return aipapi.EventToolCallStarted
	case aipapi.ToolEventArgsDelta:
		return aipapi.EventToolCallArgsDelta
	case aipapi.ToolEventFinished:
		return aipapi.EventToolCallFinished
	default:
		return aipapi.EventKind(k)
	}
}
