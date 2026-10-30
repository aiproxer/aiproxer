package jsonshape

import (
	"context"
	"errors"
	"fmt"
)

// Kind classifies guard failures with stable, payload-free labels.
type Kind string

const (
	KindTooLarge      Kind = "too_large"
	KindMalformed     Kind = "malformed"
	KindTooDeep       Kind = "too_deep"
	KindTooManyTokens Kind = "too_many_tokens"
	KindTooManyItems  Kind = "too_many_items"
	KindStringTooLong Kind = "string_too_long"
	KindKeyTooLong    Kind = "key_too_long"
	KindNumberTooLong Kind = "number_too_long"
	KindDuplicateName Kind = "duplicate_name"
	KindInvalidUTF8   Kind = "invalid_utf8"
	KindCanceled      Kind = "canceled"
)

// MalformedReason identifies the stable structural reason for malformed JSON.
type MalformedReason string

const (
	MalformedEmpty             MalformedReason = "empty"
	MalformedSyntax            MalformedReason = "syntax"
	MalformedMultipleValues    MalformedReason = "multiple_values"
	MalformedUnexpectedClosing MalformedReason = "unexpected_closing"
	MalformedIncomplete        MalformedReason = "incomplete"
	MalformedTrailingData      MalformedReason = "trailing_data"
	MalformedObjectValue       MalformedReason = "object_value_without_key"
)

// Error is a typed JSON shape guard failure. Msg must never include payload,
// keys, or values from the scanned input.
type Error struct {
	Kind   Kind
	Reason MalformedReason
	Limit  int
	Value  int
	Msg    string
	cause  error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Msg != "" {
		return "jsonshape: " + e.Msg
	}
	if e.Limit > 0 {
		return fmt.Sprintf("jsonshape: %s: value %d exceeds limit %d", e.Kind, e.Value, e.Limit)
	}
	return "jsonshape: " + string(e.Kind)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// Classify returns the guard kind for typed guard errors.
func Classify(err error) Kind {
	if guardErr, ok := errors.AsType[*Error](err); ok {
		return guardErr.Kind
	}
	return ""
}

func canceledError(err error) *Error {
	msg := "context canceled"
	if errors.Is(err, context.DeadlineExceeded) {
		msg = "context deadline exceeded"
	}
	return &Error{Kind: KindCanceled, Msg: msg, cause: err}
}
