package toolcall

import "fmt"

type RejectError struct {
	ReasonCode string
	ToolCallID string
}

func (e *RejectError) Error() string {
	if e == nil {
		return "toolcall: rejected"
	}
	if e.ReasonCode == "" {
		return "toolcall: rejected"
	}
	return fmt.Sprintf("toolcall: rejected (%s)", e.ReasonCode)
}
