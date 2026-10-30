package largebody

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
)

// ErrBodySessionMetadataRejected indicates that the profile rejected body-carried
// AIProxer session metadata (Requirement 14.2, Requirement 17.5).
var ErrBodySessionMetadataRejected = errors.New("largebody: body-carried AIProxer session metadata rejected by profile")

// SessionInputSource carries raw candidate session values from inbound HTTP headers
// and body metadata before precedence and bounds enforcement.
type SessionInputSource struct {
	HeaderAuthoritativeSessionID string
	HeaderResumeToken            string
	HeaderALegID                 string
	HeaderClientSessionHint      string

	BodyAuthoritativeSessionID string
	BodyResumeToken            string
	HasBodySessionMetadata     bool

	RejectBodyMetadata bool
}

// BuildSessionInput builds an exact bounded SessionInput from raw source inputs.
// It enforces:
//   - Header-over-body precedence for authoritative session ID and resume token (Requirement 14.1);
//   - Rejection of body-carried AIProxer session metadata when RejectBodyMetadata is true (Requirements 14.2, 17.5);
//   - Bounded sizes under aipapi envelope caps and semantic-fact budget;
//   - SensitiveString wrapping for resume tokens so they never leak into telemetry or backends (Requirements 14.1, 22);
//   - Accurate NewSessionRequested determination (true when both authoritative session ID and resume token are empty).
func BuildSessionInput(src SessionInputSource, maxFactBytes int64) (SessionInput, error) {
	if err := checkBudget(maxFactBytes); err != nil {
		return SessionInput{}, err
	}

	if src.RejectBodyMetadata {
		if src.HasBodySessionMetadata || strings.TrimSpace(src.BodyAuthoritativeSessionID) != "" || strings.TrimSpace(src.BodyResumeToken) != "" {
			return SessionInput{}, fmt.Errorf("%w: request contains body-carried session metadata", ErrBodySessionMetadataRejected)
		}
	}

	authSessionID := strings.TrimSpace(src.BodyAuthoritativeSessionID)
	if hdr := strings.TrimSpace(src.HeaderAuthoritativeSessionID); hdr != "" {
		authSessionID = hdr
	}

	resumeToken := strings.TrimSpace(src.BodyResumeToken)
	if hdr := strings.TrimSpace(src.HeaderResumeToken); hdr != "" {
		resumeToken = hdr
	}

	aLegID := strings.TrimSpace(src.HeaderALegID)
	clientSessionID := strings.TrimSpace(src.HeaderClientSessionHint)

	if len(authSessionID) > aipapi.MaxAuthoritativeSessionIDBytes {
		return SessionInput{}, fmt.Errorf("largebody: session authoritative session id exceeds %d bytes", aipapi.MaxAuthoritativeSessionIDBytes)
	}
	if len(resumeToken) > aipapi.MaxResumeTokenBytes {
		return SessionInput{}, fmt.Errorf("largebody: session resume token exceeds %d bytes", aipapi.MaxResumeTokenBytes)
	}
	if len(aLegID) > aipapi.MaxALegIDBytes {
		return SessionInput{}, fmt.Errorf("largebody: session a-leg id exceeds %d bytes", aipapi.MaxALegIDBytes)
	}
	if len(clientSessionID) > aipapi.MaxClientSessionIDBytes {
		return SessionInput{}, fmt.Errorf("largebody: session client session id exceeds %d bytes", aipapi.MaxClientSessionIDBytes)
	}

	newSession := (authSessionID == "" && resumeToken == "")

	res := SessionInput{
		AuthoritativeSessionID: authSessionID,
		ClientSessionID:        clientSessionID,
		ALegID:                 aLegID,
		ResumeToken:            NewSensitiveString(resumeToken),
		NewSessionRequested:    newSession,
	}

	if err := res.Validate(maxFactBytes); err != nil {
		return SessionInput{}, err
	}

	return res, nil
}

// ProvesFreshALeg reports whether the session input definitely proves a fresh, unresumed A-leg.
// Returns true only when NewSessionRequested is true, AuthoritativeSessionID is empty,
// ALegID is empty, and ResumeToken is zero or empty.
func (s SessionInput) ProvesFreshALeg() bool {
	return s.NewSessionRequested &&
		strings.TrimSpace(s.AuthoritativeSessionID) == "" &&
		strings.TrimSpace(s.ALegID) == "" &&
		(s.ResumeToken.IsZero() || strings.TrimSpace(s.ResumeToken.Reveal()) == "")
}

// CorrelationID returns a stable identifier for diagnostics and traffic capture:
// authoritative session ID when set, otherwise client session ID.
func (s SessionInput) CorrelationID() string {
	if x := strings.TrimSpace(s.AuthoritativeSessionID); x != "" {
		return x
	}
	return strings.TrimSpace(s.ClientSessionID)
}

// String implements fmt.Stringer to ensure resume tokens are never formatted.
func (s SessionInput) String() string {
	token := ""
	if !s.ResumeToken.IsZero() {
		token = redactedSensitive
	}
	return fmt.Sprintf("SessionInput{AuthoritativeSessionID:%s ClientSessionID:%s ALegID:%s ResumeToken:%s NewSessionRequested:%t}",
		s.AuthoritativeSessionID, s.ClientSessionID, s.ALegID, token, s.NewSessionRequested)
}

// GoString implements fmt.GoStringer.
func (s SessionInput) GoString() string {
	return s.String()
}

// Format ensures that even under custom fmt verbs, the resume token is never printed.
func (s SessionInput) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, s.String())
}

// LogValue implements slog.LogValuer to ensure resume tokens are never logged.
func (s SessionInput) LogValue() slog.Value {
	token := ""
	if !s.ResumeToken.IsZero() {
		token = redactedSensitive
	}
	return slog.GroupValue(
		slog.String("authoritative_session_id", s.AuthoritativeSessionID),
		slog.String("client_session_id", s.ClientSessionID),
		slog.String("aleg_id", s.ALegID),
		slog.String("resume_token", token),
		slog.Bool("new_session_requested", s.NewSessionRequested),
	)
}

// SessionRef converts the bounded SessionInput into a canonical aipapi.SessionRef.
func (s SessionInput) SessionRef() aipapi.SessionRef {
	return aipapi.SessionRef{
		AuthoritativeSessionID: s.AuthoritativeSessionID,
		ClientSessionID:        s.ClientSessionID,
		ALegID:                 s.ALegID,
		ResumeToken:            s.ResumeToken.Reveal(),
	}
}

// SessionInputFromRef constructs a SessionInput from a canonical aipapi.SessionRef.
func SessionInputFromRef(ref aipapi.SessionRef) SessionInput {
	authID := strings.TrimSpace(ref.AuthoritativeSessionID)
	resume := strings.TrimSpace(ref.ResumeToken)
	return SessionInput{
		AuthoritativeSessionID: authID,
		ClientSessionID:        strings.TrimSpace(ref.ClientSessionID),
		ALegID:                 strings.TrimSpace(ref.ALegID),
		ResumeToken:            NewSensitiveString(resume),
		NewSessionRequested:    authID == "" && resume == "",
	}
}

type (
	wireSessionCtxKey        struct{}
	wireTurnShapeCtxKey      struct{}
	wireIdentityCtxKey       struct{}
	wireClassificationCtxKey struct{}
)

// WireIdentity carries logical RequestID and TraceID for wire execution.
type WireIdentity struct {
	RequestID string
	TraceID   string
}

// WithWireIdentity attaches a WireIdentity to ctx for wire execution.
func WithWireIdentity(ctx context.Context, requestID, traceID string) context.Context {
	return context.WithValue(ctx, wireIdentityCtxKey{}, WireIdentity{
		RequestID: requestID,
		TraceID:   traceID,
	})
}

// WireIdentityFromContext extracts WireIdentity from ctx if attached.
func WireIdentityFromContext(ctx context.Context) (WireIdentity, bool) {
	if ctx == nil {
		return WireIdentity{}, false
	}
	id, ok := ctx.Value(wireIdentityCtxKey{}).(WireIdentity)
	return id, ok
}

// WithWireSessionInput attaches a bounded SessionInput to ctx for wire execution (Requirement 14).
func WithWireSessionInput(ctx context.Context, in SessionInput) context.Context {
	return context.WithValue(ctx, wireSessionCtxKey{}, in)
}

// WireSessionInputFromContext extracts SessionInput from ctx if attached.
func WireSessionInputFromContext(ctx context.Context) (SessionInput, bool) {
	if ctx == nil {
		return SessionInput{}, false
	}
	in, ok := ctx.Value(wireSessionCtxKey{}).(SessionInput)
	return in, ok
}

// WithWireClientTurnShape attaches a ClientTurnShape to ctx for wire execution (Requirement 14).
func WithWireClientTurnShape(ctx context.Context, shape ClientTurnShape) context.Context {
	return context.WithValue(ctx, wireTurnShapeCtxKey{}, shape)
}

// WireClientTurnShapeFromContext extracts ClientTurnShape from ctx if attached.
func WireClientTurnShapeFromContext(ctx context.Context) (ClientTurnShape, bool) {
	if ctx == nil {
		return ClientTurnShape{}, false
	}
	shape, ok := ctx.Value(wireTurnShapeCtxKey{}).(ClientTurnShape)
	return shape, ok
}

// WithWireClassificationEvidence attaches the bounded classification evidence a
// certified frontend proof compiled for this wire turn to ctx.
//
// The value is the provider-neutral SDK evidence carrier and nothing else: the
// already-accepted client identity, the canonical operation and the fixed
// tool-category bits. It carries no header bag, tool list, transcript, prompt
// text, local path or shadow canonical Call, so handing it to the wire
// classification stage never requires materializing a canonical request
// (Requirements 5.2, 5.3, 5.5).
func WithWireClassificationEvidence(ctx context.Context, evidence sessionclassification.Evidence) context.Context {
	return context.WithValue(ctx, wireClassificationCtxKey{}, evidence)
}

// WireClassificationEvidenceFromContext extracts the bounded classification
// evidence from ctx if attached. Absent evidence is legal and means "stay
// unknown": it is never read as a negative classification (Requirements 5.4).
func WireClassificationEvidenceFromContext(ctx context.Context) (sessionclassification.Evidence, bool) {
	if ctx == nil {
		return sessionclassification.Evidence{}, false
	}
	evidence, ok := ctx.Value(wireClassificationCtxKey{}).(sessionclassification.Evidence)
	return evidence, ok
}

// ContextWithWireProof enriches ctx with assessed proof Session, Turn, bounded
// classification evidence and canonical Request/Trace IDs in one pass so all
// frontend lanes cannot forget part of the handoff.
// RequestID derives from proof.Identity.CallID(explicitRequestID) matching
// diag.StableCallID (explicit ID or "call_"+token); TraceID equals RequestID under the
// canonical single-identity contract. Overwrites any prior Wire* values so a stale or
// conflicting caller ctx cannot replace assessed proof. No Call materialization, no
// payload decode, no new IDs: only already-derived bounded proof facts are carried.
// SessionInput strings are immutable; TurnShape shallow copy shares bounded read-only
// slices consumed without mutation by PrepareSecureSession/RecordClientTurnWithShape.
// ClassificationEvidence is a fixed-shape metadata value, so it is carried by value
// and consumed by the wire session-classification stage.
func ContextWithWireProof(ctx context.Context, proof Proof, explicitRequestID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	reqID := proof.Identity.CallID(strings.TrimSpace(explicitRequestID))
	ctx = WithWireSessionInput(ctx, proof.Session)
	ctx = WithWireClientTurnShape(ctx, proof.Turn)
	ctx = WithWireClassificationEvidence(ctx, proof.ClassificationEvidence)
	ctx = WithWireIdentity(ctx, reqID, reqID)
	return ctx
}
