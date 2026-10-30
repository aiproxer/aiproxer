// Package frontendpipe provides a shared HTTP create pipeline for wire frontends.
package frontendpipe

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/sessionwire"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// OpenAICancellationPrefix is the prefix used for carrier-bound OpenAI Responses IDs (Requirement 18.6).
const OpenAICancellationPrefix = "resp_aip_"

type openAICancellationCarrier struct {
	ALegID    string `json:"a"`
	SessionID string `json:"s,omitempty"`
}

// FormatOpenAICancellationCarrier encodes authoritative A-leg and session identifiers
// into an OpenAI Responses cancellation carrier string (Requirement 18.6).
func FormatOpenAICancellationCarrier(aLegID, sessionID string) string {
	aLegID = strings.TrimSpace(aLegID)
	if aLegID == "" {
		return ""
	}
	carrier := openAICancellationCarrier{
		ALegID:    aLegID,
		SessionID: strings.TrimSpace(sessionID),
	}
	raw, err := json.Marshal(carrier)
	if err != nil {
		return ""
	}
	return OpenAICancellationPrefix + base64.RawURLEncoding.EncodeToString(raw)
}

// ParseOpenAICancellationCarrier parses an OpenAI Responses cancellation carrier string
// and extracts the authoritative A-leg and session identifiers (Requirement 18.6).
func ParseOpenAICancellationCarrier(carrierID string) (aLegID, sessionID string, ok bool) {
	encoded, hasPrefix := strings.CutPrefix(strings.TrimSpace(carrierID), OpenAICancellationPrefix)
	if !hasPrefix || encoded == "" {
		return "", "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", "", false
	}
	var carrier openAICancellationCarrier
	if err := json.Unmarshal(raw, &carrier); err == nil {
		aLegID := strings.TrimSpace(carrier.ALegID)
		if aLegID == "" {
			return "", "", false
		}
		return aLegID, strings.TrimSpace(carrier.SessionID), true
	}
	aLegID = strings.TrimSpace(string(raw))
	if aLegID == "" {
		return "", "", false
	}
	return aLegID, "", true
}

// ResponseContext is the bounded shared frontend response context (Task 14.1, Requirement 18).
// It couples frontend-owned proof state (FrontendWireState) with ExecutionResult (ResponseFacts,
// sensitive SessionResponseCarrier, and canonical EventStream) to supply stream wrapping and
// response writers without fabricating a partial or fake aipapi.Call.
// Core contracts never import this type (Requirement 18.8).
type ResponseContext struct {
	// State is the frontend-owned proof state including response seeds and extra protocol state.
	State FrontendWireState
	// ResponseFacts contains bounded provider-neutral facts returned by ExecuteLargeBody.
	ResponseFacts largebody.ResponseFacts
	// Session carries sensitive session response carriers returned by ExecuteLargeBody.
	Session largebody.SessionResponseCarrier
	// Stream is the canonical event stream returned by ExecuteLargeBody.
	Stream aipapi.EventStream
}

// NewResponseContext creates an initialized ResponseContext from frontend wire state and wire execution result.
func NewResponseContext(state FrontendWireState, res largebody.ExecutionResult) ResponseContext {
	return ResponseContext{
		State:         state,
		ResponseFacts: res.Facts,
		Session:       res.Session,
		Stream:        res.Stream,
	}
}

// ProfileID returns the profile ID identifying the protocol-specific handler.
func (c ResponseContext) ProfileID() string {
	return c.State.ProfileID
}

// Proof returns the underlying provider-neutral largebody.Proof.
func (c ResponseContext) Proof() largebody.Proof {
	return c.State.Proof
}

// Seeds returns the underlying bounded ResponseStateSeeds.
func (c ResponseContext) Seeds() ResponseStateSeeds {
	return c.State.Seeds
}

// Extra returns any frontend-owned per-request protocol state.
func (c ResponseContext) Extra() any {
	return c.State.Extra
}

// Facts returns the bounded provider-neutral response facts.
func (c ResponseContext) Facts() largebody.ResponseFacts {
	return c.ResponseFacts
}

// SessionCarrier returns the sensitive session response carrier.
func (c ResponseContext) SessionCarrier() largebody.SessionResponseCarrier {
	return c.Session
}

// CallID returns the effective request / call ID: explicit request ID if present,
// otherwise the deterministic call ID derived from canonical semantic identity.
func (c ResponseContext) CallID() string {
	if c.ResponseFacts.RequestID != "" {
		return c.ResponseFacts.RequestID
	}
	return c.State.Seeds.EffectiveCallID()
}

// DeterministicCallID returns the path-stable call ID derived from canonical semantic identity.
func (c ResponseContext) DeterministicCallID() string {
	return c.State.Seeds.DeterministicCallID
}

// DeterministicTimestamp returns the Unix timestamp derived from canonical semantic identity.
func (c ResponseContext) DeterministicTimestamp() int64 {
	return c.State.Seeds.DeterministicTimestamp
}

// ExplicitRequestID returns any caller-provided or header-supplied request ID.
func (c ResponseContext) ExplicitRequestID() string {
	return c.State.Seeds.ExplicitRequestID
}

// RouteSelector returns the effective routing selector.
func (c ResponseContext) RouteSelector() string {
	if c.State.Seeds.RouteSelector != "" {
		return c.State.Seeds.RouteSelector
	}
	return c.State.Proof.RouteSelector
}

// ClientModel returns the client-requested model name.
func (c ResponseContext) ClientModel() string {
	if c.State.Seeds.ClientModel != "" {
		return c.State.Seeds.ClientModel
	}
	return c.State.Proof.ClientModel
}

// EffectiveModel returns the candidate/provider effective model if resolved, otherwise the client model.
func (c ResponseContext) EffectiveModel() string {
	if c.ResponseFacts.EffectiveModel != "" {
		return c.ResponseFacts.EffectiveModel
	}
	return c.ClientModel()
}

// IsStream reports whether streaming delivery was requested.
func (c ResponseContext) IsStream() bool {
	if c.ResponseFacts.Delivery != "" {
		return c.ResponseFacts.Delivery == aipapi.DeliveryModeStreaming
	}
	return c.State.Seeds.Stream || c.State.Proof.Delivery == aipapi.DeliveryModeStreaming
}

// DeterministicToken returns the 16-hex-character token derived from canonical semantic identity (Requirement 16.4).
func (c ResponseContext) DeterministicToken() string {
	if !c.State.Proof.Identity.IsZero() {
		return c.State.Proof.Identity.Token()
	}
	if c.State.Seeds.DeterministicToken != "" {
		return c.State.Seeds.DeterministicToken
	}
	if after, ok := strings.CutPrefix(c.State.Seeds.DeterministicCallID, "call_"); ok {
		return after
	}
	return ""
}

// DeterministicTime returns the UTC time derived from canonical semantic identity (Requirement 16.4).
func (c ResponseContext) DeterministicTime() time.Time {
	return time.Unix(c.DeterministicTimestamp(), 0).UTC()
}

// ResponseID returns prefix followed by the deterministic token (Requirement 18.7).
func (c ResponseContext) ResponseID(prefix string) string {
	return prefix + c.DeterministicToken()
}

// OpenAIResponseID returns the OpenAI Responses response ID:
// if an authoritative A-leg ID is present, it returns the cancellation carrier ID bound
// to that A-leg and session (Requirement 18.6);
// otherwise, it falls back to the deterministic "resp_" + DeterministicToken() (Requirement 18.7).
func (c ResponseContext) OpenAIResponseID() string {
	if aLegID := c.ALegID(); aLegID != "" {
		if carrier := FormatOpenAICancellationCarrier(aLegID, c.SessionID()); carrier != "" {
			return carrier
		}
	}
	return "resp_" + c.DeterministicToken()
}

// OpenAIMessageID returns the OpenAI Responses message ID ("msg_" + OpenAIResponseID()).
func (c ResponseContext) OpenAIMessageID() string {
	return "msg_" + c.OpenAIResponseID()
}

// OpenAIChatCompletionID returns the OpenAI Chat completion ID ("chatcmpl_" + DeterministicToken()).
func (c ResponseContext) OpenAIChatCompletionID() string {
	return "chatcmpl_" + c.DeterministicToken()
}

// AnthropicMessageID returns the Anthropic message ID ("msg_" + DeterministicToken()).
func (c ResponseContext) AnthropicMessageID() string {
	return "msg_" + c.DeterministicToken()
}

// CancellationID returns the authoritative cancellation ID.
// If an authoritative A-leg ID is present, it returns the carrier-bound cancellation ID (Requirement 18.6).
// Otherwise, it falls back to any explicit seed cancellation ID, or OpenAIChatCompletionID() for
// Chat completions, or OpenAIResponseID().
func (c ResponseContext) CancellationID() string {
	if aLegID := c.ALegID(); aLegID != "" {
		if carrier := FormatOpenAICancellationCarrier(aLegID, c.SessionID()); carrier != "" {
			return carrier
		}
	}
	if c.State.Seeds.CancellationID != "" {
		return c.State.Seeds.CancellationID
	}
	if c.State.Proof.Operation == aipapi.OperationOpenAIChatCompletions {
		return c.OpenAIChatCompletionID()
	}
	return c.OpenAIResponseID()
}

// SessionID returns the authoritative session identifier.
func (c ResponseContext) SessionID() string {
	if c.ResponseFacts.SessionID != "" {
		return c.ResponseFacts.SessionID
	}
	if c.Session.AuthoritativeSessionID != "" {
		return c.Session.AuthoritativeSessionID
	}
	return c.State.Seeds.SessionID
}

// ALegID returns the authoritative A-leg identifier.
func (c ResponseContext) ALegID() string {
	if c.ResponseFacts.ALegID != "" {
		return c.ResponseFacts.ALegID
	}
	if c.Session.ALegID != "" {
		return c.Session.ALegID
	}
	return c.State.Seeds.ALegID
}

// ResumeToken returns the sensitive resume token carrier.
func (c ResponseContext) ResumeToken() largebody.SensitiveString {
	return c.Session.ResumeToken
}

// TraceID returns the trace identifier.
func (c ResponseContext) TraceID() string {
	if c.ResponseFacts.TraceID != "" {
		return c.ResponseFacts.TraceID
	}
	return c.CallID()
}

// WriteSessionHeaders sets AIProxer session response headers on w from the sensitive session carrier
// (Requirements 14.6, 18.2).
func (c ResponseContext) WriteSessionHeaders(w http.ResponseWriter) {
	if w == nil {
		return
	}
	c.WriteSessionHeadersTo(w.Header())
}

// WriteSessionHeadersTo sets AIProxer session response headers on h from the sensitive session carrier.
func (c ResponseContext) WriteSessionHeadersTo(h http.Header) {
	if h == nil {
		return
	}
	sessionwire.WriteSessionResponseCarrierHeaders(h, c.Session)
}

// String returns a safe, non-sensitive string representation of ResponseContext (Requirements 14.7, 18.2, 22.3).
// Sensitive resume tokens are never exposed in string renderings.
func (c ResponseContext) String() string {
	return fmt.Sprintf("ResponseContext{ProfileID:%q CallID:%q RouteSelector:%q ClientModel:%q EffectiveModel:%q Stream:%t Session:%s}",
		c.ProfileID(), c.CallID(), c.RouteSelector(), c.ClientModel(), c.EffectiveModel(), c.IsStream(), c.Session)
}

// GoString returns a safe Go syntax representation of ResponseContext.
func (c ResponseContext) GoString() string {
	return c.String()
}

// Format formats the response context safely for all verbs (%v, %+v, %#v, %s, %q).
func (c ResponseContext) Format(f fmt.State, verb rune) {
	switch verb {
	case 'q':
		_, _ = io.WriteString(f, fmt.Sprintf("%q", c.String()))
	default:
		_, _ = io.WriteString(f, c.String())
	}
}

// LogValue implements slog.LogValuer to ensure structured logging never exposes sensitive tokens (Requirements 14.7, 22.3).
func (c ResponseContext) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("profile_id", c.ProfileID()),
		slog.String("call_id", c.CallID()),
		slog.String("route_selector", c.RouteSelector()),
		slog.String("client_model", c.ClientModel()),
		slog.String("effective_model", c.EffectiveModel()),
		slog.Bool("stream", c.IsStream()),
		slog.String("session", c.Session.String()),
	)
}

// Validate validates the response context under the given semantic-fact budget.
func (c ResponseContext) Validate(maxFactBytes int64) error {
	if maxFactBytes <= 0 {
		return fmt.Errorf("frontendpipe: fact budget must be > 0, got %d", maxFactBytes)
	}
	if err := c.State.Validate(maxFactBytes); err != nil {
		return fmt.Errorf("frontendpipe: response context wire state: %w", err)
	}

	// Validate ResponseFacts if populated
	if c.ResponseFacts.RequestID != "" {
		if err := c.ResponseFacts.Validate(maxFactBytes); err != nil {
			return fmt.Errorf("frontendpipe: response context facts: %w", err)
		}
		// Cross-consistency checks
		if c.State.Seeds.SessionID != "" && c.ResponseFacts.SessionID != "" && c.ResponseFacts.SessionID != c.State.Seeds.SessionID {
			return fmt.Errorf("frontendpipe: response context session ID %q disagrees with seeds session ID %q",
				c.ResponseFacts.SessionID, c.State.Seeds.SessionID)
		}
		if c.State.Seeds.ALegID != "" && c.ResponseFacts.ALegID != "" && c.ResponseFacts.ALegID != c.State.Seeds.ALegID {
			return fmt.Errorf("frontendpipe: response context A-leg ID %q disagrees with seeds A-leg ID %q",
				c.ResponseFacts.ALegID, c.State.Seeds.ALegID)
		}
		isStream := c.ResponseFacts.Delivery == aipapi.DeliveryModeStreaming
		if c.State.Seeds.Stream != isStream {
			return fmt.Errorf("frontendpipe: response context delivery mode %q disagrees with seeds stream flag %t",
				string(c.ResponseFacts.Delivery), c.State.Seeds.Stream)
		}
	}

	// Validate Session carrier if populated
	if c.Session.AuthoritativeSessionID != "" || c.Session.ALegID != "" || !c.Session.ResumeToken.IsZero() {
		if err := c.Session.Validate(maxFactBytes); err != nil {
			return fmt.Errorf("frontendpipe: response context session carrier: %w", err)
		}
		if c.State.Seeds.SessionID != "" && c.Session.AuthoritativeSessionID != "" && c.Session.AuthoritativeSessionID != c.State.Seeds.SessionID {
			return fmt.Errorf("frontendpipe: response context carrier session ID %q disagrees with seeds session ID %q",
				c.Session.AuthoritativeSessionID, c.State.Seeds.SessionID)
		}
		if c.State.Seeds.ALegID != "" && c.Session.ALegID != "" && c.Session.ALegID != c.State.Seeds.ALegID {
			return fmt.Errorf("frontendpipe: response context carrier A-leg ID %q disagrees with seeds A-leg ID %q",
				c.Session.ALegID, c.State.Seeds.ALegID)
		}
	}

	return nil
}
