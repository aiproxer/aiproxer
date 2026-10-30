package siwc

import (
	"errors"
	"fmt"
)

// The refusal of a callback whose authority cannot be established.
//
// The sentinel and the typed error live together, and they live apart from
// the listener that produces them: the listener's PARSING is the part that
// changes whenever the protocol's edge cases are re-read, and the error
// contract is the part that callers classify against.

// ErrAmbiguousCallback reports a callback that carried a protocol parameter
// more than once.
//
// The sentinel classifies the outcome. A callback that repeats a parameter
// is refused, and the refusal is a decision of the connector rather than a
// failure the provider reported.
var ErrAmbiguousCallback = errors.New("siwc: the callback carried an ambiguous parameter")

// AmbiguousCallbackError is the refusal of an ambiguous callback.
//
// The text is CONNECTOR-OWNED: it names the protocol parameter that arrived
// more than once and the classified provider code, and it carries no
// provider text at all.
//
// The provider's description is NOT reused for it. CallbackError.Description
// is the provider's description by contract, and a connector-owned message
// must not travel under the provider's name.
//
// The refusal exists because the ambiguity HIDES a value. url.Values.Get
// answers the first value only, so a callback that repeats a parameter is
// read as if it carried one value: the values behind the first one - which
// may be a credential the provider echoed - are invisible to every check
// that follows, and absent from the redaction set.
type AmbiguousCallbackError struct {
	// Parameter is the protocol parameter that arrived more than once.
	Parameter string
	// ProviderCode is the CLASSIFIED provider error code of the callback.
	//
	// It is EMPTY when the callback carried no error code. An absent code
	// is not an error code: classifyProviderErrorCode answers
	// "provider_error" for an empty value, and that class describes an
	// UNKNOWN error, not the absence of one.
	ProviderCode string
}

// Error renders the refusal without provider text.
func (e *AmbiguousCallbackError) Error() string {
	if e.ProviderCode == "" {
		return fmt.Sprintf("siwc: the callback carried %q more than once", e.Parameter)
	}
	return fmt.Sprintf("siwc: the callback carried %q more than once: %s", e.Parameter, e.ProviderCode)
}

// Is reports the sentinel, so that a caller classifies the outcome with
// errors.Is instead of matching on message text.
func (e *AmbiguousCallbackError) Is(target error) bool { return target == ErrAmbiguousCallback }
