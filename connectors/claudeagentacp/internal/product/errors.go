package product

import (
	"context"
	"errors"
	"strings"

	"github.com/aiproxer/aiproxer/connector-support/acp"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// ErrRuntimeFailed reports that the supervised runtime exited unsuccessfully.
// The supervisor confirms extinction before reporting it, so this is a runtime
// failure and not an unconfirmed cleanup: the process tree is gone, and the
// runtime's own outcome stays observable to the caller.
var ErrRuntimeFailed = errors.New("claudeagentacp: runtime exited unsuccessfully")

// SupervisorRuntimeFailedExit is the exit code the supervisor uses for a
// runtime that exited unsuccessfully. A supervisor that could not establish
// cleanup authority exits with 1, so the parent can separate a runtime
// failure from an unconfirmed cleanup instead of treating both as one.
const SupervisorRuntimeFailedExit = 2

// safeFailure exposes only connector-owned text, stable sentinels and copied
// RPC metadata. The original error and its untrusted wrappers are not retained.
type safeFailure struct {
	message string
	cause   error
	kind    error
}

func (e *safeFailure) Error() string    { return e.message }
func (e *safeFailure) GoString() string { return e.message }
func (e *safeFailure) Unwrap() []error {
	if e.cause == nil {
		if e.kind == nil {
			return nil
		}
		return []error{e.kind}
	}
	if e.kind == nil {
		return []error{e.cause}
	}
	return []error{e.kind, e.cause}
}

// classifyError consumes an ACP failure before any retry wrapper is applied.
// It adds eligibility, never retries; core and the shared stream owner enforce
// output commitment. RPC text (including details decoded from data) is untrusted.
func classifyError(err error, phase aipapi.OutputPhase, source string) error {
	if err == nil {
		return nil
	}
	failure := &safeFailure{message: "claudeagentacp: agent failed"}
	rpc, isRPC := errors.AsType[*acp.RPCError](err)
	if isRPC && rpc != nil {
		// Method is assigned by the local ACP caller, not decoded from the
		// provider response. Message includes untrusted response data; drop it.
		failure.cause = &acp.RPCError{Method: rpc.Method, Code: rpc.Code, ErrorKind: rpc.ErrorKind}
	}
	recoverable := false
	gatewaySource := source == "gateway"
	switch {
	case errors.Is(err, acp.ErrCleanupIncomplete):
		// Process-tree extinction is unconfirmed. The runtime keeps its state
		// and refuses reuse, so the lifecycle failure must stay observable
		// instead of being flattened into a generic recoverable failure.
		failure.message = "claudeagentacp: agent cleanup incomplete"
		failure.cause = acp.ErrCleanupIncomplete
	case errors.Is(err, context.Canceled):
		failure.message = "claudeagentacp: request canceled"
		failure.cause = context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		failure.message = "claudeagentacp: request deadline exceeded"
		failure.cause = context.DeadlineExceeded
	case isRPC && rpc != nil && rpc.Code == -32000: // ACP authentication_required
		failure.message = "claudeagentacp: selected credential is missing or expired; " + authRemediation(source)
		failure.kind = aipapi.NewStreamError("authentication_required", failure.message)
		recoverable = true
	case isRPC && rpc != nil && rpc.Method == "authenticate":
		// Handshake failures name the source remediation regardless of the
		// adapter's errorKind: the method is locally assigned, not provider
		// text, and the remediation stays the actionable one (requirement
		// 2.4). Gateway failures therefore never reach plan taxonomy here,
		// and first-party authenticate failures are not misread as usage
		// exhaustion either.
		if gatewaySource {
			failure.message = "claudeagentacp: gateway authentication failed; " + authRemediation(source)
		} else {
			failure.message = "claudeagentacp: agent authentication failed; " + authRemediation(source)
		}
	case isRPC && rpc != nil && isPlanExhaustion(rpc):
		if gatewaySource {
			// The gateway bills through the operator's endpoint: its quota
			// and billing failures belong to the generic upstream class and
			// never inherit Anthropic plan taxonomy (requirement 3.3).
			failure.message = "claudeagentacp: agent RPC failed"
			break
		}
		failure.message = "claudeagentacp: plan usage exhausted; " + planRemediation(source)
		failure.kind = aipapi.NewStreamError("plan_exhausted", failure.message)
		recoverable = true
	case errors.Is(err, aipapi.ErrCapabilityReject) || (isRPC && rpc != nil && rpc.Code == -32602 && rpc.Method == "session/set_config_option"):
		failure.message = "claudeagentacp: requested model or effort is not advertised by the agent"
		failure.kind = aipapi.ErrCapabilityReject
	case isRPC && rpc != nil && rpc.Method == "session/set_config_option":
		// A refused session-option exchange is a setup failure before any
		// output, not a terminal agent failure: a fresh spawn can clear it and
		// the host may select another candidate. The advertised-value refusal
		// above stays a capability rejection, which no fresh spawn clears.
		failure.message = "claudeagentacp: agent refused the requested model or effort"
		recoverable = true
	case isRPC:
		// Keep the shared unary RPC fallback terminal; do not infer capability
		// rejection or authentication from arbitrary provider message strings.
		failure.message = "claudeagentacp: agent RPC failed"
	default:
		// Match the shared transport/protocol pre-output fallback.
		recoverable = true
	}
	if phase == aipapi.PhasePostOutput && failure.kind == nil {
		failure.kind = aipapi.ErrStreamTerminal
	}
	if phase == aipapi.PhasePreOutput && recoverable {
		return aipapi.RecoverablePreOutputError(failure)
	}
	return failure
}

func authRemediation(source string) string {
	switch source {
	case "subscription_token":
		return "run claude setup-token and provision the claude_oauth_token secret"
	case "host_login":
		return "run claude auth login"
	case "gateway":
		return "verify the gateway endpoint and provisioned header secrets"
	default:
		return "provision the api_key secret"
	}
}

// planQuotaErrorKinds are the adapter's categorical SDK failures that mean
// the plan allocation is spent (never transient rate limiting). They mirror
// the pinned adapter 0.89.1 / SDK 0.3.293 failure taxonomy.
var planQuotaErrorKinds = map[string]bool{
	"billing_error":   true,
	"account_on_hold": true,
}

// usageLimitMessagePrefixes are the pinned SDK synthetic usage-limit texts.
// They arrive without a categorical errorKind (the data envelope is absent),
// so the exact vendor prefixes are the only signal. Structured errorKind
// wins over text: a transient "rate_limit" kind never matches here even if
// its human text starts with a similar phrase.
var usageLimitMessagePrefixes = []string{
	"You've hit your",
	"You've reached your",
	"You're out of usage credits",
	"Your org is out of usage",
	"Your seat type doesn't include usage",
	"Your seat type doesn't include extra usage",
	"Your usage allocation has been disabled by your admin",
	"Your group's usage limit is set to $0",
	"Fable 5 requires usage credits",
	"You're out of extra usage",
}

// isPlanExhaustion reports whether a retained RPC failure means the plan
// allocation is spent, as opposed to a transient rate limit or generic
// backend failure.
func isPlanExhaustion(rpc *acp.RPCError) bool {
	if rpc == nil {
		return false
	}
	if planQuotaErrorKinds[rpc.ErrorKind] {
		return true
	}
	if rpc.ErrorKind != "" {
		return false
	}
	for _, prefix := range usageLimitMessagePrefixes {
		if strings.HasPrefix(rpc.Message, prefix) {
			return true
		}
	}
	return false
}

func planRemediation(source string) string {
	switch source {
	case "subscription_token":
		return "the subscription plan allocation is spent; wait for reset or refill, then run claude setup-token again"
	case "host_login":
		return "the subscription plan allocation is spent; wait for reset or refill, then run claude auth login"
	default:
		return "check Anthropic billing and quota for the API key"
	}
}
