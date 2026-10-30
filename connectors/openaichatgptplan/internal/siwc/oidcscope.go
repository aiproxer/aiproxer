package siwc

import (
	"errors"
	"strings"
)

// The granted-scope policy.
//
// The ChatGPT plan usage is authorized by a SCOPE, not by a token. The
// documentation is explicit about it: "A valid ID token alone does not
// authorize ChatGPT plan usage", and the sign-in flow must "Check the token
// response's granted scopes for chatgpt.tokens.use.direct before proceeding
// to inference".
//
// The two outcomes are DIFFERENT and both of them are real:
//
//   - The granted scopes carry the direct plan-use scope. The registration
//     may use the ChatGPT plan.
//
//   - The granted scopes do NOT carry it. The sign-in is RETAINED and the
//     plan usage is DISABLED. The account is authenticated, the
//     registration is kept, and the operator is told to enable the plan
//     usage or to configure another billing path.
//
// The second outcome is therefore NOT a rejection of the sign-in. It is
// the CLASSIFICATION of the plan-usage outcome, and it is reported with
// the class vocabulary of this package so that a caller can tell it apart
// from a rejected token.
//
// The check belongs to the FLOW and not to the ID-token validator: the
// scopes are a property of the TOKEN RESPONSE, which the flow owns, and a
// validator that the flow does not control must not be able to decide
// whether the plan usage is authorized.

// PlanUseScope is the scope that authorizes direct ChatGPT plan usage.
const PlanUseScope = "chatgpt.tokens.use.direct"

// ErrPlanUsageNotGranted reports that the granted scopes do not authorize
// direct ChatGPT plan usage.
//
// It is the cause of the scope-classified outcome. It is never a rejection
// of the sign-in.
var ErrPlanUsageNotGranted = errors.New(
	"siwc: the granted scopes do not authorize direct ChatGPT plan usage",
)

// PlanUsageEnabled reports whether the granted scopes authorize direct
// ChatGPT plan usage.
//
// The scopes are compared as a SET of names: a scope matches when it is
// the direct plan-use scope exactly. A scope that merely CONTAINS the
// name - "chatgpt.tokens.use.direct.other" - is another scope, and
// accepting it would authorize a plan usage the provider did not grant.
func PlanUsageEnabled(scopes []string) bool {
	for _, scope := range scopes {
		if strings.TrimSpace(scope) == PlanUseScope {
			return true
		}
	}
	return false
}

// CheckDirectPlanUsage checks the granted scopes for the direct plan-use
// scope.
//
// The outcome is CLASSIFIED: a scope set without the direct plan-use scope
// is reported as a *ValidatorError of class ValidatorClassScope, so that a
// caller classifies it by the class and never by matching on message text.
//
// The class is NOT a rejection of the sign-in. The documentation requires
// the sign-in to be RETAINED and the plan usage to be marked disabled, so
// the flow of this package HANDLES the class: it keeps the registration,
// it persists the granted scopes, and it reports the degraded plan usage
// to the operator.
//
// The error itself is never rendered. The rendering of a ValidatorError
// names the ID token, and the ID token was not rejected: a valid ID token
// without the direct plan-use scope is a legitimate state. A caller that
// reports the outcome reports the CLASS, and the class is the scope.
func CheckDirectPlanUsage(scopes []string) error {
	if PlanUsageEnabled(scopes) {
		return nil
	}
	return NewValidatorError(ValidatorClassScope, ErrPlanUsageNotGranted)
}

// IsPlanUsageNotGranted reports whether the failure is the classified
// "the granted scopes do not authorize direct ChatGPT plan usage" outcome.
//
// The comparison is by the class, so a caller classifies the outcome
// without matching on message text.
func IsPlanUsageNotGranted(err error) bool {
	var classified *ValidatorError
	if !errors.As(err, &classified) {
		return false
	}
	return classified.Class == ValidatorClassScope
}
