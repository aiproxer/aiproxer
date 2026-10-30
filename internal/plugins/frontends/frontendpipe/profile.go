package frontendpipe

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/routeselect"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// FrontendProfile is the optional internal contract implemented by certified
// frontends to support the large-payload fast path (Task 7.1, Requirements 4, 8, 9, 14, 16, 18).
//
// A profile owns:
//   - profile identity (ProfileID)
//   - protocol semantic proof compilation over captured replay bytes
//   - canonical semantic identity digest calculation
//   - normalized client-turn shape derivation (ClientTurnShape)
//   - session precedence facts (SessionInput)
//   - body mode and rewrite semantics (BodyMode, RewriteSemantics, model Span)
//   - response-state seeds for post-execution frontend response building
//
// Invariants (Task 7.1, Requirements 8.5, 18.8):
//   - No backend selection or provider network I/O inside the profile.
//   - Carries bounded facts only; exceeding bounds declines to canonical.
//   - Nil profile or missing executor capability => canonical with zero spool allocation.
type FrontendProfile interface {
	// ProfileID returns the static identifier for this certified profile
	// (e.g., "openai_responses_v1", "openai_chat_v1", "openresponses_v1").
	ProfileID() string

	// CompileProof compiles protocol proof and response seeds from the
	// captured request replay under decode admission. If the request shape
	// cannot be certified or exceeds semantic-fact bounds, it returns an error
	// to select canonical fallback under the held permit (Requirements 4, 16.7).
	CompileProof(ctx context.Context, in ProofInput) (ProofOutput, error)
}

// ProofInput supplies request inputs and replay source to the profile proof compiler.
type ProofInput struct {
	Ctx                  context.Context
	Headers              http.Header
	URLPath              string
	Path                 PathMatch
	RouteSelector        string
	RoutePrefixes        routeselect.PrefixSet
	DefaultRouteSelector string
	RouteFromBodyModel   bool
	Source               largebody.Source
	BodyBytes            int64
	AnthropicVersion     string
}

// CandidateProofResult carries the outcome of candidate protocol proof under decode admission (Task 7.5).
type CandidateProofResult struct {
	// Output is the compiled proof output (valid when Err is nil).
	Output ProofOutput
	// PermitHeld indicates that the decode-admission permit was held during proof compilation.
	PermitHeld bool
	// Err is any proof compilation or validation error.
	Err error
}

// CandidateAssessmentResult carries the outcome of candidate proof assessment under decode admission (Task 11.9).
type CandidateAssessmentResult struct {
	// Assessment is the outcome returned by LargeBodyAssessor.AssessLargeBody.
	Assessment largebody.Assessment
	// PermitHeld reports whether the decode-admission permit was held during assessment.
	PermitHeld bool
	// Err is any error returned by AssessLargeBody or assessor resolution.
	Err error
}

// WireCommitResult carries the outcome of wire commit execution after decode admission permit release (Task 11.9).
type WireCommitResult struct {
	// Assessment is the accepted assessment that authorized the wire commit.
	Assessment largebody.Assessment
	// Result is the outcome returned by LargeBodyWireExecutor.ExecuteLargeBody.
	Result largebody.ExecutionResult
	// ResponseContext is the bounded frontend response context constructed on wire execution (Task 14.1).
	ResponseContext ResponseContext
	// PermitHeld reports whether the decode-admission permit was held during ExecuteLargeBody (must be false).
	PermitHeld bool
	// Err is any error returned by ExecuteLargeBody.
	Err error
}

// ResponseStateSeeds carries bounded seed facts derived during protocol proof
// for constructing frontend response headers, IDs, timestamps, and session/cancellation
// carriers after wire execution (Task 7.1, Requirement 18).
type ResponseStateSeeds struct {
	// DeterministicCallID is the path-stable call ID derived from canonical semantic identity.
	DeterministicCallID string
	// DeterministicTimestamp is the Unix timestamp derived from canonical semantic identity.
	DeterministicTimestamp int64
	// DeterministicToken is the 16-hex-character token derived from canonical semantic identity.
	DeterministicToken string
	// ExplicitRequestID is any caller-provided or header-supplied request ID.
	ExplicitRequestID string
	// RouteSelector is the effective routing selector.
	RouteSelector string
	// ClientModel is the requested client model name.
	ClientModel string
	// Stream indicates whether streaming delivery was requested.
	Stream bool
	// CancellationID is the frontend cancellation identifier seed (e.g. for OpenAI Responses).
	CancellationID string
	// SessionID is the client/authoritative session identifier.
	SessionID string
	// ALegID is the authoritative A-leg identifier.
	ALegID string
}

// NewResponseStateSeeds creates initialized, bounded response-state seeds from
// canonical identity digest, session inputs, and route/model facts.
func NewResponseStateSeeds(
	digest largebody.IdentityDigest,
	explicitReqID string,
	routeSelector string,
	clientModel string,
	stream bool,
	session largebody.SessionInput,
	cancellationID string,
) ResponseStateSeeds {
	callID := digest.CallID(explicitReqID)
	ts := digest.Unix()
	tok := digest.Token()
	return ResponseStateSeeds{
		DeterministicCallID:    callID,
		DeterministicTimestamp: ts,
		DeterministicToken:     tok,
		ExplicitRequestID:      explicitReqID,
		RouteSelector:          routeSelector,
		ClientModel:            clientModel,
		Stream:                 stream,
		CancellationID:         cancellationID,
		SessionID:              session.AuthoritativeSessionID,
		ALegID:                 session.ALegID,
	}
}

// EffectiveCallID returns the explicit request ID if non-empty, otherwise the
// deterministic call ID derived from canonical semantic identity.
func (s ResponseStateSeeds) EffectiveCallID() string {
	if id := strings.TrimSpace(s.ExplicitRequestID); id != "" {
		return id
	}
	return s.DeterministicCallID
}

// EffectiveTimestamp returns the deterministic timestamp derived from canonical
// semantic identity.
func (s ResponseStateSeeds) EffectiveTimestamp() int64 {
	return s.DeterministicTimestamp
}

// Validate enforces bounds on all string facts under the semantic-fact budget (Requirement 4.2, 4.5).
func (s ResponseStateSeeds) Validate(maxFactBytes int64) error {
	if maxFactBytes <= 0 {
		return fmt.Errorf("frontendpipe: fact budget must be > 0, got %d", maxFactBytes)
	}
	for name, v := range map[string]string{
		"deterministic call id": s.DeterministicCallID,
		"deterministic token":   s.DeterministicToken,
		"explicit request id":   s.ExplicitRequestID,
		"route selector":        s.RouteSelector,
		"client model":          s.ClientModel,
		"cancellation id":       s.CancellationID,
		"session id":            s.SessionID,
		"a-leg id":              s.ALegID,
	} {
		if int64(len(v)) > maxFactBytes {
			return fmt.Errorf("frontendpipe: response seed %s exceeds %d bytes", name, maxFactBytes)
		}
	}
	return nil
}

// FrontendWireState is the bounded frontend-owned state for a wire request.
// It couples the provider-neutral Proof with frontend-owned response seeds
// and optional protocol-specific metadata (Requirement 18.8).
// Core contracts never import this type.
type FrontendWireState struct {
	ProfileID string
	Proof     largebody.Proof
	Seeds     ResponseStateSeeds
	Extra     any
}

// Validate enforces bounds and structural consistency between Proof, Seeds, and ProfileID.
func (s FrontendWireState) Validate(maxFactBytes int64) error {
	if maxFactBytes <= 0 {
		return fmt.Errorf("frontendpipe: fact budget must be > 0, got %d", maxFactBytes)
	}
	if strings.TrimSpace(s.ProfileID) == "" {
		return fmt.Errorf("frontendpipe: wire state profile id must not be empty")
	}
	if int64(len(s.ProfileID)) > maxFactBytes {
		return fmt.Errorf("frontendpipe: wire state profile id exceeds %d bytes", maxFactBytes)
	}
	if s.ProfileID != s.Proof.ProfileID {
		return fmt.Errorf("frontendpipe: wire state profile id %q disagrees with proof profile id %q", s.ProfileID, s.Proof.ProfileID)
	}
	if err := s.Proof.Validate(maxFactBytes); err != nil {
		return fmt.Errorf("frontendpipe: proof: %w", err)
	}
	if err := s.Seeds.Validate(maxFactBytes); err != nil {
		return fmt.Errorf("frontendpipe: seeds: %w", err)
	}

	// Consistency cross-checks between proof facts and response seeds
	if s.Seeds.RouteSelector != s.Proof.RouteSelector {
		return fmt.Errorf("frontendpipe: seeds route selector %q disagrees with proof %q", s.Seeds.RouteSelector, s.Proof.RouteSelector)
	}
	if s.Seeds.ClientModel != s.Proof.ClientModel {
		return fmt.Errorf("frontendpipe: seeds client model %q disagrees with proof %q", s.Seeds.ClientModel, s.Proof.ClientModel)
	}
	isStream := s.Proof.Delivery == aipapi.DeliveryModeStreaming
	if s.Seeds.Stream != isStream {
		return fmt.Errorf("frontendpipe: seeds stream flag %t disagrees with proof delivery %q", s.Seeds.Stream, string(s.Proof.Delivery))
	}
	if !s.Proof.Identity.IsZero() {
		expectedCallID := s.Proof.Identity.CallID(s.Seeds.ExplicitRequestID)
		if s.Seeds.DeterministicCallID != expectedCallID {
			return fmt.Errorf("frontendpipe: seeds deterministic call id %q disagrees with proof identity %q", s.Seeds.DeterministicCallID, expectedCallID)
		}
		expectedUnix := s.Proof.Identity.Unix()
		if s.Seeds.DeterministicTimestamp != expectedUnix {
			return fmt.Errorf("frontendpipe: seeds deterministic timestamp %d disagrees with proof identity %d", s.Seeds.DeterministicTimestamp, expectedUnix)
		}
	}
	if s.Proof.Session.AuthoritativeSessionID != "" && s.Seeds.SessionID != s.Proof.Session.AuthoritativeSessionID {
		return fmt.Errorf("frontendpipe: seeds session id %q disagrees with proof session %q", s.Seeds.SessionID, s.Proof.Session.AuthoritativeSessionID)
	}
	if s.Proof.Session.ALegID != "" && s.Seeds.ALegID != s.Proof.Session.ALegID {
		return fmt.Errorf("frontendpipe: seeds a-leg id %q disagrees with proof a-leg %q", s.Seeds.ALegID, s.Proof.Session.ALegID)
	}
	return nil
}

// ProofOutput is the result of profile proof compilation over the captured replay source.
type ProofOutput struct {
	State FrontendWireState
}

// Proof returns the underlying provider-neutral largebody.Proof.
func (o ProofOutput) Proof() largebody.Proof { return o.State.Proof }

// Seeds returns the underlying bounded ResponseStateSeeds.
func (o ProofOutput) Seeds() ResponseStateSeeds { return o.State.Seeds }

// Validate validates the underlying bounded FrontendWireState.
func (o ProofOutput) Validate(maxFactBytes int64) error {
	return o.State.Validate(maxFactBytes)
}

// CandidatePrerequisites evaluates the cheap pre-capture prerequisites for the large-body fast path (Task 7.1, 7.3).
// Returns the LargeBodyExecutor and true if and only if:
//   - spec != nil
//   - spec.Profile != nil
//   - spec.Exec implements largebody.LargeBodyExecutor
//
// Otherwise, returns (nil, false), directing the request immediately to the
// unchanged canonical path with zero spool allocation.
func CandidatePrerequisites[Opts any](spec *Spec[Opts]) (largebody.LargeBodyExecutor, bool) {
	if spec == nil || spec.Profile == nil {
		return nil, false
	}
	return largebody.AsLargeBodyExecutor(spec.Exec)
}
