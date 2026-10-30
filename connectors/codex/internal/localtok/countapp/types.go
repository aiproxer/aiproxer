package countapp

import (
	"errors"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

var ErrLocalUnavailable = errors.New("local token counting unavailable")

type CountCallInput struct {
	Backend string
	Model   string
	CallID  string
	Call    aipapi.Call
}

type CountOutputInput struct {
	Backend string
	Model   string
	CallID  string
	Text    string
	Events  []aipapi.Event
}

type CountTextInput struct {
	Backend string
	Model   string
	CallID  string
	Text    string
}

type CountResult struct {
	InputTokens        int
	OutputTokens       int
	CacheReadTokens    int
	CacheWriteTokens   int
	ReasoningTokens    int
	TotalTokens        int
	TotalTokensPresent bool
	Accounting         aipapi.UsageAccountingMetadata
	Fallbacks          []Fallback
}

type FallbackReason string

const FallbackReasonLocalDefaultEncoding FallbackReason = "local_default_encoding"

type Fallback struct {
	Reason  FallbackReason
	Message string
	Err     error
}
