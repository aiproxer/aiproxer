// Package jsonguard provides low-cost preflight checks for untrusted frontend JSON bodies.
// Shape scanning is delegated to internal/core/jsonshape; this package keeps the
// frontend HTTP read helpers and historical type/error surface.
//
// Frozen compat shim: callers depend on this package path for legacy imports and error
// types; do not extend behavior here—add new shape policy under jsonshape or wire frontends.
package jsonguard

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/aiproxer/aiproxer/internal/core/jsonshape"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/reqbody"
)

// Limits bounds JSON body shape before adapter-specific decode.
type Limits = jsonshape.Limits

// Result reports basic facts gathered during token-level scanning.
type Result = jsonshape.Result

// Kind classifies guard failures for frontend handler mapping.
type Kind = jsonshape.Kind

const (
	KindTooLarge      = jsonshape.KindTooLarge
	KindMalformed     = jsonshape.KindMalformed
	KindTooDeep       = jsonshape.KindTooDeep
	KindTooManyTokens = jsonshape.KindTooManyTokens
	KindTooManyItems  = jsonshape.KindTooManyItems
	KindStringTooLong = jsonshape.KindStringTooLong
	KindKeyTooLong    = jsonshape.KindKeyTooLong
	KindNumberTooLong = jsonshape.KindNumberTooLong
	KindDuplicateName = jsonshape.KindDuplicateName
	KindInvalidUTF8   = jsonshape.KindInvalidUTF8
	KindCanceled      = jsonshape.KindCanceled
)

// Error is a typed JSON guard failure with jsonguard-prefixed Error() text.
type Error struct {
	Kind  Kind
	Limit int
	Value int
	Msg   string
	cause error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Msg != "" {
		return "jsonguard: " + e.Msg
	}
	if e.Limit > 0 {
		return fmt.Sprintf("jsonguard: %s: value %d exceeds limit %d", e.Kind, e.Value, e.Limit)
	}
	return "jsonguard: " + string(e.Kind)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// DefaultLimits returns conservative non-zero defaults for frontend JSON bodies.
func DefaultLimits() Limits {
	return jsonshape.RequestEnvelopeLimits()
}

// NormalizeLimits fills zero or negative fields with defaults.
func NormalizeLimits(limits Limits) Limits {
	return jsonshape.NormalizeLimits(limits)
}

// Preflight validates JSON size and shape using streaming decoder tokens.
func Preflight(data []byte, limits Limits) (Result, error) {
	return PreflightWithContext(context.Background(), data, limits)
}

// PreflightWithContext is Preflight with request-cancellation checks at token boundaries.
func PreflightWithContext(ctx context.Context, data []byte, limits Limits) (Result, error) {
	result, err := jsonshape.PreflightWithContext(ctx, data, limits)
	return result, mapError(err)
}

// ReadAndPreflight reads a bounded request body and then applies PreflightWithContext.
func ReadAndPreflight(w http.ResponseWriter, r *http.Request, limits Limits) ([]byte, Result, error) {
	limits = NormalizeLimits(limits)
	data, err := reqbody.ReadAll(w, r, limits.MaxBytes)
	if err != nil {
		if reqbody.TooLarge(err) {
			return data, Result{Bytes: len(data)}, &Error{Kind: KindTooLarge, Limit: int(limits.MaxBytes), Value: len(data), Msg: err.Error()}
		}
		return data, Result{Bytes: len(data)}, err
	}
	result, err := PreflightWithContext(r.Context(), data, limits)
	return data, result, err
}

// Classify returns the guard kind for typed guard errors.
func Classify(err error) Kind {
	if guardErr, ok := errors.AsType[*Error](err); ok {
		return guardErr.Kind
	}
	return jsonshape.Classify(err)
}

// TooLarge reports whether err maps to a request-entity-too-large response.
func TooLarge(err error) bool {
	return Classify(err) == KindTooLarge || reqbody.TooLarge(err)
}

func mapError(err error) error {
	if err == nil {
		return nil
	}
	var shapeErr *jsonshape.Error
	if !errors.As(err, &shapeErr) {
		return err
	}
	return &Error{
		Kind:  shapeErr.Kind,
		Limit: shapeErr.Limit,
		Value: shapeErr.Value,
		Msg:   shapeErr.Msg,
		cause: shapeErr,
	}
}
