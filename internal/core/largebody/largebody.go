package largebody

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"

	"github.com/aiproxer/aiproxer/internal/capabilityfacts"
	"github.com/aiproxer/aiproxer/internal/compactionfacts"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
)

// redactedSensitive is the only rendering of a sensitive value outside its
// owning frontend boundary.
const redactedSensitive = "[redacted]"

// Source is an immutable completed replay capture (design section 5).
// After EOF the source exposes independent offset-zero readers; each
// provider/credential retry opens and closes a fresh reader. The concrete
// spool implementation (Task 4) lives elsewhere; this is the contract seam.
type Source interface {
	// Size reports the exact captured body size in bytes.
	Size() int64
	// Open returns an independent offset-zero reader over the capture.
	Open() (io.ReadCloser, error)
	// Close releases the root capture; it is idempotent.
	Close() error
}

// Span is an exact raw byte range inside the captured body, used for
// certified rewrite splices such as the top-level model token
// (Requirements 4, 9).
type Span struct {
	Offset int64
	Length int64
}

// Validate rejects negative bounds and checked-int64 overflow.
func (s Span) Validate() error {
	if s.Offset < 0 {
		return fmt.Errorf("largebody: span offset must be >= 0, got %d", s.Offset)
	}
	if s.Length < 0 {
		return fmt.Errorf("largebody: span length must be >= 0, got %d", s.Length)
	}
	if _, err := s.End(); err != nil {
		return err
	}
	return nil
}

// End returns the exclusive end offset using checked int64 math.
func (s Span) End() (int64, error) {
	if s.Offset > math.MaxInt64-s.Length {
		return 0, fmt.Errorf("largebody: span end overflows int64 (offset %d length %d)", s.Offset, s.Length)
	}
	return s.Offset + s.Length, nil
}

// CheckedSpliceLength returns prefixLen+replacementLen+suffixLen using
// checked int64 math before provider open (Requirement 9.4).
func CheckedSpliceLength(prefixLen, replacementLen, suffixLen int64) (int64, error) {
	for name, v := range map[string]int64{"prefix": prefixLen, "replacement": replacementLen, "suffix": suffixLen} {
		if v < 0 {
			return 0, fmt.Errorf("largebody: splice %s length must be >= 0, got %d", name, v)
		}
	}
	partial, err := checkedAdd(prefixLen, replacementLen)
	if err != nil {
		return 0, err
	}
	return checkedAdd(partial, suffixLen)
}

func checkedAdd(a, b int64) (int64, error) {
	if b > 0 && a > math.MaxInt64-b {
		return 0, fmt.Errorf("largebody: length addition overflows int64 (%d + %d)", a, b)
	}
	return a + b, nil
}

// BodyMode names the certified body representation (design sections 6, 9).
// Wave 1 supports identity JSON only; decoded/compressed modes need a
// separate certification wave and are rejected here.
type BodyMode string

const (
	// BodyModeUnknown is the zero value and never validates.
	BodyModeUnknown BodyMode = ""
	// BodyModeIdentityJSON is an uncompressed identity JSON body.
	BodyModeIdentityJSON BodyMode = "identity_json"
)

// String returns a bounded static label for metrics/diagnostics.
func (m BodyMode) String() string {
	if m == BodyModeIdentityJSON {
		return string(m)
	}
	return "unknown"
}

// Validate accepts only certified body modes.
func (m BodyMode) Validate() error {
	if m != BodyModeIdentityJSON {
		return fmt.Errorf("largebody: unsupported body mode %q", string(m))
	}
	return nil
}

// RewriteKind classifies the certified rewrite applied to one attempt.
type RewriteKind uint8

const (
	// RewriteKindNone means the captured bytes are forwarded unchanged.
	RewriteKindNone RewriteKind = iota
	// RewriteKindModelToken means the scanner-recorded top-level model token
	// span is spliced with a JSON-encoded replacement model.
	RewriteKindModelToken
)

// String returns a bounded static label for metrics/diagnostics.
func (k RewriteKind) String() string {
	switch k {
	case RewriteKindNone:
		return "none"
	case RewriteKindModelToken:
		return "model_token"
	default:
		return "unknown"
	}
}

// RewriteSemantics is the immutable certified rewrite contract passed to
// every backend exact/domain resolver (design section 9). A resolver may
// declare NeedsModelRewrite only when the profile certified it here; the
// flag can never grant an uncertified transformation.
type RewriteSemantics struct {
	kind RewriteKind
	span Span
}

// NewNoRewrite returns the immutable no-rewrite contract.
func NewNoRewrite() RewriteSemantics {
	return RewriteSemantics{kind: RewriteKindNone}
}

// NewModelTokenRewrite returns the immutable model-token splice contract
// for an exact, non-empty scanner-recorded span.
func NewModelTokenRewrite(span Span) (RewriteSemantics, error) {
	if err := span.Validate(); err != nil {
		return RewriteSemantics{}, fmt.Errorf("largebody: model rewrite: %w", err)
	}
	if span.Length == 0 {
		return RewriteSemantics{}, fmt.Errorf("largebody: model rewrite span must be non-empty")
	}
	return RewriteSemantics{kind: RewriteKindModelToken, span: span}, nil
}

// Kind reports the certified rewrite kind.
func (r RewriteSemantics) Kind() RewriteKind { return r.kind }

// Span returns the certified rewrite span (meaningful for model-token rewrites).
func (r RewriteSemantics) Span() Span { return r.span }

// NeedsModelRewrite reports whether attempts must splice a replacement model.
func (r RewriteSemantics) NeedsModelRewrite() bool { return r.kind == RewriteKindModelToken }

// String returns a bounded description of the certified rewrite semantics.
func (r RewriteSemantics) String() string {
	if r.kind == RewriteKindModelToken {
		return fmt.Sprintf("model_token(offset=%d,len=%d)", r.span.Offset, r.span.Length)
	}
	return r.kind.String()
}

// Validate rejects unknown kinds and invalid spans.
func (r RewriteSemantics) Validate() error {
	switch r.kind {
	case RewriteKindNone:
		return nil
	case RewriteKindModelToken:
		if err := r.span.Validate(); err != nil {
			return fmt.Errorf("largebody: model rewrite: %w", err)
		}
		if r.span.Length == 0 {
			return fmt.Errorf("largebody: model rewrite span must be non-empty")
		}
		return nil
	default:
		return fmt.Errorf("largebody: unknown rewrite kind %d", uint8(r.kind))
	}
}

// SensitiveString is an opaque bearer value (resume tokens) that must never
// reach backends, telemetry, logs, or metrics (Requirements 14, 22). Every
// default rendering is redacted; the owning frontend extracts the value via
// Reveal to emit authoritative session/resume headers.
type SensitiveString struct {
	value string
}

// NewSensitiveString wraps a bearer value without exposing it.
func NewSensitiveString(value string) SensitiveString {
	return SensitiveString{value: value}
}

// Reveal returns the wrapped value for the owning frontend boundary.
func (s SensitiveString) Reveal() string { return s.value }

// IsZero reports whether no value is wrapped.
func (s SensitiveString) IsZero() bool { return s.value == "" }

// ByteLen returns the length of the sensitive string in bytes without formatting or revealing it.
func (s SensitiveString) ByteLen() int { return len(s.value) }

// String redacts the value.
func (s SensitiveString) String() string { return redactedSensitive }

// GoString redacts the value.
func (s SensitiveString) GoString() string { return redactedSensitive }

// Format redacts the value for every verb.
func (s SensitiveString) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, redactedSensitive)
}

// MarshalJSON redacts the value.
func (s SensitiveString) MarshalJSON() ([]byte, error) {
	return json.Marshal(redactedSensitive)
}

// MarshalText redacts the value.
func (s SensitiveString) MarshalText() ([]byte, error) {
	return []byte(redactedSensitive), nil
}

// ProtocolFacts carries profile-certified protocol requirements as bounded
// scalar facts (design section 6). RequirementsID is a bounded static
// profile-defined requirement-set label (never user/model/session text);
// ControlCount counts certified control carriers, never prompt content.
type ProtocolFacts struct {
	RequirementsID string
	ControlCount   int64
}

// Validate enforces scalar bounds under the semantic-fact budget.
func (f ProtocolFacts) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	if int64(len(f.RequirementsID)) > maxFactBytes {
		return fmt.Errorf("largebody: protocol requirements id exceeds %d bytes", maxFactBytes)
	}
	if f.ControlCount < 0 {
		return fmt.Errorf("largebody: protocol control count must be >= 0, got %d", f.ControlCount)
	}
	return nil
}

// SessionInput carries the exact bounded client/session authority inputs
// that canonical BeginTurn receives, sourced through normal
// frontend/header/body precedence (Requirement 14.1). The resume token is
// sensitive: it never reaches backends or telemetry.
type SessionInput struct {
	AuthoritativeSessionID string
	ClientSessionID        string
	ALegID                 string
	ResumeToken            SensitiveString
	NewSessionRequested    bool
}

// Validate enforces scalar bounds under the semantic-fact budget.
func (s SessionInput) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	for name, v := range map[string]string{
		"authoritative session id": s.AuthoritativeSessionID,
		"client session id":        s.ClientSessionID,
		"a-leg id":                 s.ALegID,
	} {
		if int64(len(v)) > maxFactBytes {
			return fmt.Errorf("largebody: session %s exceeds %d bytes", name, maxFactBytes)
		}
	}
	if int64(len(s.ResumeToken.Reveal())) > maxFactBytes {
		return fmt.Errorf("largebody: session resume token exceeds %d bytes", maxFactBytes)
	}
	return nil
}

// MarshalJSON redacts the resume token while preserving presence signals.
func (s SessionInput) MarshalJSON() ([]byte, error) {
	token := ""
	if !s.ResumeToken.IsZero() {
		token = redactedSensitive
	}
	return json.Marshal(struct {
		AuthoritativeSessionID string `json:"authoritative_session_id,omitempty"`
		ClientSessionID        string `json:"client_session_id,omitempty"`
		ALegID                 string `json:"aleg_id,omitempty"`
		ResumeToken            string `json:"resume_token,omitempty"`
		NewSessionRequested    bool   `json:"new_session_requested,omitempty"`
	}{
		AuthoritativeSessionID: s.AuthoritativeSessionID,
		ClientSessionID:        s.ClientSessionID,
		ALegID:                 s.ALegID,
		ResumeToken:            token,
		NewSessionRequested:    s.NewSessionRequested,
	})
}

// ClientTurnPartShape describes one normalized content part by kind and
// attributed byte size only. Prompt text is never materialized for the
// recorder (Requirement 14.5).
type (
	ClientTurnPartShape = capabilityfacts.TurnPartShape
	ClientTurnItemShape = capabilityfacts.TurnItemShape
	ClientTurnShape     = capabilityfacts.TurnShape
	ControlRequirements = capabilityfacts.ControlRequirements
)

var (
	CapabilitiesDigest         = capabilityfacts.CapabilitiesDigest
	DeriveRequiredCapabilities = capabilityfacts.DeriveRequiredCapabilities
)

// IdentityDigest is the exact canonical semantic identity digest equivalent
// to the post-frontend-decode/pre-core canonical Call identity for the
// supported subset, derived without retaining prompt content
// (Requirement 16). It is a distinct type from SourceDigest so a raw-body
// hash can never substitute for canonical economic/request identity
// (Requirement 16.5).
type IdentityDigest struct {
	sum [32]byte
}

// NewIdentityDigest wraps an already-computed canonical sum.
func NewIdentityDigest(sum [32]byte) IdentityDigest {
	return IdentityDigest{sum: sum}
}

// Sum returns the wrapped canonical sum.
func (d IdentityDigest) Sum() [32]byte { return d.sum }

// IsZero reports whether no sum was provided.
func (d IdentityDigest) IsZero() bool { return d.sum == [32]byte{} }

// String renders the sum as hex for diagnostics.
func (d IdentityDigest) String() string { return hex.EncodeToString(d.sum[:]) }

// SourceDigest is the replay/attempt source-integrity evidence. It is
// explicitly not a substitute for canonical identity (Requirement 16.5,
// design section 6).
type SourceDigest struct {
	sum [32]byte
}

// NewSourceDigest wraps an already-computed source integrity sum.
func NewSourceDigest(sum [32]byte) SourceDigest {
	return SourceDigest{sum: sum}
}

// Sum returns the wrapped source sum.
func (d SourceDigest) Sum() [32]byte { return d.sum }

// IsZero reports whether no sum was provided.
func (d SourceDigest) IsZero() bool { return d.sum == [32]byte{} }

// String renders the sum as hex for diagnostics.
func (d SourceDigest) String() string { return hex.EncodeToString(d.sum[:]) }

// Proof is the bounded frontend profile proof over the validated replay
// stream (design section 6). Every field has a named downstream consumer;
// this must never grow into a shadow aipapi.Call (design section 11).
type Proof struct {
	ProfileID     string
	Operation     aipapi.Operation
	Delivery      aipapi.DeliveryMode
	RouteSelector string
	// RouteOrigin names why the route was chosen: the client named it, the
	// proxy chose its default route, or a continuation parent's lineage
	// restored it. Only the default origin may keep the generation's frozen
	// model binding.
	RouteOrigin     routing.RouteOrigin
	ClientModel     string
	MaxOutputTokens int64
	Facts           ProtocolFacts
	Mode            BodyMode
	Rewrite         RewriteSemantics
	ModelSpan       Span
	Identity        IdentityDigest
	Turn            ClientTurnShape
	Session         SessionInput
	Source          SourceDigest
	BodyBytes       int64

	// ClassificationEvidence is the bounded classification summary for this
	// turn: the already-accepted client identity plus fixed tool-category bits.
	//
	// It exists so a classification-enabled large request stays wire-eligible
	// instead of forcing canonical materialization. It deliberately carries no
	// header bag, tool list, transcript, prompt text, path or shadow
	// aipapi.Call; absent evidence is legal and simply keeps the session
	// unknown (requirements 5.1, 5.2, 5.4, 5.5).
	//
	// Named consumer: the wire-path classification stage.
	ClassificationEvidence sessionclassification.Evidence

	CompactionFacts    compactionfacts.RequestFacts
	CompactionComplete bool

	RequiredCapabilities []aipapi.Capability
}

// CompactionDigest returns the deterministic digest of the compaction facts and completeness.
func (p Proof) CompactionDigest() [32]byte {
	return compactionfacts.Digest(p.CompactionFacts, p.CompactionComplete)
}

// AggregateFactBytes returns the aggregate in-memory byte size of proof metadata and compaction facts.
func (p Proof) AggregateFactBytes() int64 {
	total := int64(len(p.ProfileID) + len(p.Operation) + len(p.RouteSelector) + len(p.ClientModel) + len(p.Facts.RequirementsID))
	total += p.Turn.MetadataBytes() + int64(len(p.Session.AuthoritativeSessionID)+len(p.Session.ClientSessionID)+len(p.Session.ALegID)+p.Session.ResumeToken.ByteLen())
	total += int64(len(p.CompactionFacts.ItemHashes)*compactionfacts.ItemHashSizeBytes + len(p.CompactionFacts.StartRuleID))
	for _, c := range p.RequiredCapabilities {
		total += int64(len(c))
	}
	// Bounded classification evidence is charged to the large-payload cost
	// model: the accepted client identity length plus the fixed category
	// bitset. The bits never grow with tool count or body size, so a huge
	// request cannot inflate proof metadata through classification
	// (requirements 5.1, 5.2, 12.7).
	total += p.ClassificationEvidence.MetadataBytes()
	return total
}

// Validate enforces bounds and cross-field consistency: a recorded model
// span requires certified model-rewrite semantics (Requirement 9.5
// duplicate/ambiguous forms stay canonical unless certified).
func (p Proof) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	if int64(len(p.CompactionFacts.ItemHashes)*compactionfacts.ItemHashSizeBytes) > maxFactBytes {
		return fmt.Errorf("largebody: proof compaction fact hashes exceed %d bytes", maxFactBytes)
	}
	if int64(len(p.CompactionFacts.StartRuleID)) > maxFactBytes {
		return fmt.Errorf("largebody: proof compaction start rule id exceeds %d bytes", maxFactBytes)
	}
	if err := capabilityfacts.ValidateCapabilities(p.RequiredCapabilities, maxFactBytes); err != nil {
		return err
	}
	if p.Delivery == aipapi.DeliveryModeStreaming && !slices.Contains(p.RequiredCapabilities, aipapi.CapabilityStreaming) {
		return fmt.Errorf("largebody: proof delivery streaming requires CapabilityStreaming in RequiredCapabilities")
	}
	if strings.TrimSpace(p.ProfileID) == "" {
		return fmt.Errorf("largebody: proof profile id must not be empty")
	}
	if int64(len(p.ProfileID)) > maxFactBytes {
		return fmt.Errorf("largebody: proof profile id exceeds %d bytes", maxFactBytes)
	}
	if strings.TrimSpace(string(p.Operation)) == "" {
		return fmt.Errorf("largebody: proof operation must not be empty")
	}
	if int64(len(p.Operation)) > maxFactBytes {
		return fmt.Errorf("largebody: proof operation exceeds %d bytes", maxFactBytes)
	}
	switch p.Delivery {
	case aipapi.DeliveryModeStreaming, aipapi.DeliveryModeNonStreaming:
	default:
		return fmt.Errorf("largebody: proof delivery mode %q is unknown", string(p.Delivery))
	}
	if int64(len(p.RouteSelector)) > maxFactBytes {
		return fmt.Errorf("largebody: proof route selector exceeds %d bytes", maxFactBytes)
	}
	if int64(len(p.ClientModel)) > maxFactBytes {
		return fmt.Errorf("largebody: proof client model exceeds %d bytes", maxFactBytes)
	}
	if p.MaxOutputTokens < 0 {
		return fmt.Errorf("largebody: proof max output tokens must be >= 0, got %d", p.MaxOutputTokens)
	}
	if err := p.Facts.Validate(maxFactBytes); err != nil {
		return err
	}
	if err := p.Mode.Validate(); err != nil {
		return err
	}
	if err := p.Rewrite.Validate(); err != nil {
		return err
	}
	if err := p.ModelSpan.Validate(); err != nil {
		return err
	}
	if p.Rewrite.NeedsModelRewrite() && p.ModelSpan.Length == 0 {
		return fmt.Errorf("largebody: proof rewrite requires a non-empty model span")
	}
	if !p.Rewrite.NeedsModelRewrite() && p.ModelSpan != (Span{}) {
		return fmt.Errorf("largebody: proof model span requires certified rewrite semantics")
	}
	if p.Identity.IsZero() {
		return fmt.Errorf("largebody: proof canonical identity digest must not be zero")
	}
	if err := p.Turn.Validate(maxFactBytes); err != nil {
		return err
	}
	if err := p.Session.Validate(maxFactBytes); err != nil {
		return err
	}
	if err := p.ClassificationEvidence.Validate(maxFactBytes); err != nil {
		return fmt.Errorf("largebody: proof classification evidence: %w", err)
	}
	// Present evidence must describe the same turn the proof describes:
	// compiling evidence from a different operation would let the wire path
	// classify a request it never observed (requirements 5.2, 5.5).
	if !p.ClassificationEvidence.IsZero() && p.ClassificationEvidence.Operation != p.Operation {
		return fmt.Errorf("largebody: proof classification evidence operation %q does not match proof operation %q",
			string(p.ClassificationEvidence.Operation), string(p.Operation))
	}
	if p.Source.IsZero() {
		return fmt.Errorf("largebody: proof source digest must not be zero")
	}
	if p.BodyBytes <= 0 {
		return fmt.Errorf("largebody: proof body bytes must be > 0, got %d", p.BodyBytes)
	}
	return nil
}

// AssessmentRequest carries proof plus immutable generation facts only
// (design section 8). It never carries a body reader: assessment inspects
// bounded facts, not prompt content (Requirement 6.2).
type AssessmentRequest struct {
	Proof        Proof
	GenerationID string
}

// Validate enforces the generation binding and proof bounds.
func (r AssessmentRequest) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	if strings.TrimSpace(r.GenerationID) == "" {
		return fmt.Errorf("largebody: assessment generation binding must not be empty")
	}
	if int64(len(r.GenerationID)) > maxFactBytes {
		return fmt.Errorf("largebody: assessment generation binding exceeds %d bytes", maxFactBytes)
	}
	if err := r.Proof.Validate(maxFactBytes); err != nil {
		return err
	}
	return nil
}

// AssessmentDecision is the bounded assessment outcome.
type AssessmentDecision uint8

const (
	// AssessmentDecisionUnknown is the zero value and never validates.
	AssessmentDecisionUnknown AssessmentDecision = iota
	// AssessmentDecisionDecline selects canonical decode under the held permit.
	AssessmentDecisionDecline
	// AssessmentDecisionAccept crosses the one-way wire commit.
	AssessmentDecisionAccept
)

// String returns a bounded static label for metrics/diagnostics.
func (d AssessmentDecision) String() string {
	switch d {
	case AssessmentDecisionDecline:
		return "decline"
	case AssessmentDecisionAccept:
		return "accept"
	default:
		return "unknown"
	}
}

// DeclineReason is the bounded assessment-decline taxonomy for diagnostics.
// Labels are static; backend/model/session/user IDs never appear here
// (Requirement 22.2).
type DeclineReason uint16

const (
	// DeclineReasonNone accompanies accept decisions only.
	DeclineReasonNone DeclineReason = iota
	// DeclineReasonProofUncertain means profile proof could not certify the shape.
	DeclineReasonProofUncertain
	// DeclineReasonAuthorityBlocker means a frozen authority blocks wire mode.
	DeclineReasonAuthorityBlocker
	// DeclineReasonRouteIncompatible means the route domain is not wire-compatible.
	DeclineReasonRouteIncompatible
	// DeclineReasonBackendIncompatible means a candidate lacks exact/domain proof.
	DeclineReasonBackendIncompatible
	// DeclineReasonRewriteUnsupported means the rewrite has no certified contract.
	DeclineReasonRewriteUnsupported
	// DeclineReasonSessionUnsupported means session facts lack a wire view.
	DeclineReasonSessionUnsupported
	// DeclineReasonMeteringUnsupported means metering lacks a wire-native path.
	DeclineReasonMeteringUnsupported
	// DeclineReasonCountingUnsupported means token counting lacks an exact wire contract.
	DeclineReasonCountingUnsupported
	// DeclineReasonGenerationMismatch means execution disagrees with the stamp.
	DeclineReasonGenerationMismatch
	// DeclineReasonCanceled means assessment observed a canceled context.
	DeclineReasonCanceled
)

// String returns a bounded static label for metrics/diagnostics.
func (r DeclineReason) String() string {
	switch r {
	case DeclineReasonNone:
		return "none"
	case DeclineReasonProofUncertain:
		return "proof_uncertain"
	case DeclineReasonAuthorityBlocker:
		return "authority_blocker"
	case DeclineReasonRouteIncompatible:
		return "route_incompatible"
	case DeclineReasonBackendIncompatible:
		return "backend_incompatible"
	case DeclineReasonRewriteUnsupported:
		return "rewrite_unsupported"
	case DeclineReasonSessionUnsupported:
		return "session_unsupported"
	case DeclineReasonMeteringUnsupported:
		return "metering_unsupported"
	case DeclineReasonCountingUnsupported:
		return "counting_unsupported"
	case DeclineReasonGenerationMismatch:
		return "generation_mismatch"
	case DeclineReasonCanceled:
		return "canceled"
	default:
		return "unknown"
	}
}

// AssessmentStamp is the opaque generation/proof-bound acceptance stamp
// (design sections 8, 11.8). Execution revalidates it and treats
// disagreement as an invariant failure, never fallback (Requirement 6.7).
type AssessmentStamp struct {
	generationID              string
	profileID                 string
	source                    SourceDigest
	bodyBytes                 int64
	mode                      BodyMode
	rewrite                   RewriteSemantics
	identity                  IdentityDigest
	candidateDomainGeneration string
	compactionDigest          [32]byte
	requiredCapabilities      []aipapi.Capability
	requiredCapsDigest        [32]byte
}

// CompactionDigest returns the bound compaction facts digest.
func (s AssessmentStamp) CompactionDigest() [32]byte { return s.compactionDigest }

// RequiredCapabilities returns a copy of the bound required capabilities.
func (s AssessmentStamp) RequiredCapabilities() []aipapi.Capability {
	return append([]aipapi.Capability(nil), s.requiredCapabilities...)
}

// RequiredCapabilitiesDigest returns the bound required capabilities digest.
func (s AssessmentStamp) RequiredCapabilitiesDigest() [32]byte { return s.requiredCapsDigest }

// NewAssessmentStamp binds generation identity, profile/proof identity,
// source digest/size, the body/rewrite contract, and candidate/domain proof generation.
// Budget-bounded length checks run in Validate; the constructor enforces structural binding only.
// If candidateDomainGen is omitted or empty, it defaults to generationID.
func NewAssessmentStamp(
	generationID, profileID string,
	source SourceDigest,
	bodyBytes int64,
	mode BodyMode,
	rewrite RewriteSemantics,
	identity IdentityDigest,
	candidateDomainGen ...string,
) (AssessmentStamp, error) {
	cGen := generationID
	if len(candidateDomainGen) > 0 && strings.TrimSpace(candidateDomainGen[0]) != "" {
		cGen = candidateDomainGen[0]
	}
	stamp := AssessmentStamp{
		generationID:              generationID,
		profileID:                 profileID,
		source:                    source,
		bodyBytes:                 bodyBytes,
		mode:                      mode,
		rewrite:                   rewrite,
		identity:                  identity,
		candidateDomainGeneration: cGen,
		requiredCapsDigest:        CapabilitiesDigest(nil),
	}
	if err := stamp.validateStructure(); err != nil {
		return AssessmentStamp{}, err
	}
	return stamp, nil
}

// GenerationID returns the bound generation identity.
func (s AssessmentStamp) GenerationID() string { return s.generationID }

// ProfileID returns the bound profile identity.
func (s AssessmentStamp) ProfileID() string { return s.profileID }

// SourceDigest returns the bound source evidence.
func (s AssessmentStamp) SourceDigest() SourceDigest { return s.source }

// BodyBytes returns the bound body size.
func (s AssessmentStamp) BodyBytes() int64 { return s.bodyBytes }

// BodyMode returns the bound body contract.
func (s AssessmentStamp) BodyMode() BodyMode { return s.mode }

// Rewrite returns the bound rewrite contract.
func (s AssessmentStamp) Rewrite() RewriteSemantics { return s.rewrite }

// IdentityDigest returns the bound canonical identity.
func (s AssessmentStamp) IdentityDigest() IdentityDigest { return s.identity }

// CandidateDomainGeneration returns the bound candidate/domain proof generation.
func (s AssessmentStamp) CandidateDomainGeneration() string { return s.candidateDomainGeneration }

// IsZero reports whether the stamp is the zero value.
func (s AssessmentStamp) IsZero() bool {
	return s.generationID == "" && s.profileID == "" && s.bodyBytes == 0 && s.source.IsZero()
}

// Validate enforces the stamp binding under the semantic-fact budget.
func (s AssessmentStamp) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	if err := s.validateStructure(); err != nil {
		return err
	}
	if int64(len(s.generationID)) > maxFactBytes || int64(len(s.profileID)) > maxFactBytes || int64(len(s.candidateDomainGeneration)) > maxFactBytes {
		return fmt.Errorf("largebody: stamp binding exceeds %d bytes", maxFactBytes)
	}
	return nil
}

func (s AssessmentStamp) validateStructure() error {
	if strings.TrimSpace(s.generationID) == "" {
		return fmt.Errorf("largebody: stamp generation binding must not be empty")
	}
	if strings.TrimSpace(s.profileID) == "" {
		return fmt.Errorf("largebody: stamp profile binding must not be empty")
	}
	if s.source.IsZero() {
		return fmt.Errorf("largebody: stamp source digest must not be zero")
	}
	if s.bodyBytes <= 0 {
		return fmt.Errorf("largebody: stamp body bytes must be > 0, got %d", s.bodyBytes)
	}
	if err := s.mode.Validate(); err != nil {
		return err
	}
	if err := s.rewrite.Validate(); err != nil {
		return err
	}
	if s.identity.IsZero() {
		return fmt.Errorf("largebody: stamp canonical identity digest must not be zero")
	}
	if strings.TrimSpace(s.candidateDomainGeneration) == "" {
		return fmt.Errorf("largebody: stamp candidate/domain proof generation binding must not be empty")
	}
	return nil
}

// BindAssessmentStamp binds generation identity, profile/proof identity,
// source digest/size, body mode/rewrite contract, canonical identity, and candidate/domain
// proof generation from the proof and generation identifiers (Requirements 6.7, 8.5; Task 11.8).
func BindAssessmentStamp(generationID string, proof Proof, candidateDomainGen ...string) (AssessmentStamp, error) {
	cGen := generationID
	if len(candidateDomainGen) > 0 && strings.TrimSpace(candidateDomainGen[0]) != "" {
		cGen = candidateDomainGen[0]
	}
	stamp, err := NewAssessmentStamp(generationID, proof.ProfileID, proof.Source, proof.BodyBytes, proof.Mode, proof.Rewrite, proof.Identity, cGen)
	if err != nil {
		return AssessmentStamp{}, err
	}
	stamp.compactionDigest = proof.CompactionDigest()
	stamp.requiredCapabilities = append([]aipapi.Capability(nil), proof.RequiredCapabilities...)
	stamp.requiredCapsDigest = CapabilitiesDigest(proof.RequiredCapabilities)
	return stamp, nil
}

// Assessment is the bounded assessment outcome containing an opaque
// generation/proof-bound stamp and bounded facts only (design section 8, Task 11.1).
// The frontend supplies proof only; it cannot synthesize route/backend internals
// (Requirements 6, 22).
type Assessment struct {
	Decision           AssessmentDecision
	Reason             DeclineReason
	Stamp              AssessmentStamp
	WireRequest        WireRequestFacts
	WireDomain         WireDomainFacts
	CompactionFacts    compactionfacts.RequestFacts
	CompactionComplete bool
}

// WithCompactionFacts returns a copy of Assessment with the provided compaction facts.
func (a Assessment) WithCompactionFacts(facts compactionfacts.RequestFacts, complete bool) Assessment {
	a.CompactionFacts = facts.Clone()
	a.CompactionComplete = complete
	return a
}

// Accepted reports whether the assessment accepted the wire turn.
func (a Assessment) Accepted() bool {
	return a.Decision == AssessmentDecisionAccept
}

// Declined reports whether the assessment declined to canonical processing.
func (a Assessment) Declined() bool {
	return a.Decision == AssessmentDecisionDecline
}

// Validate enforces decision/reason/stamp consistency and bounds on facts.
func (a Assessment) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	switch a.Decision {
	case AssessmentDecisionDecline:
		if a.Reason == DeclineReasonNone || a.Reason.String() == "unknown" {
			return fmt.Errorf("largebody: decline requires a bounded reason")
		}
		if !a.Stamp.IsZero() {
			return fmt.Errorf("largebody: decline must not carry an assessment stamp")
		}
		return nil
	case AssessmentDecisionAccept:
		if a.Reason != DeclineReasonNone {
			return fmt.Errorf("largebody: accept must not carry a decline reason")
		}
		if a.Stamp.IsZero() {
			return fmt.Errorf("largebody: accept requires a valid assessment stamp")
		}
		if err := a.Stamp.Validate(maxFactBytes); err != nil {
			return fmt.Errorf("largebody: accept: %w", err)
		}
		if a.WireRequest.ProfileID != "" {
			if err := a.WireRequest.Validate(maxFactBytes); err != nil {
				return fmt.Errorf("largebody: accept wire request facts: %w", err)
			}
		}
		if a.WireDomain.ProfileID != "" {
			if err := a.WireDomain.Validate(maxFactBytes); err != nil {
				return fmt.Errorf("largebody: accept wire domain facts: %w", err)
			}
		}
		if a.CompactionComplete {
			if int64(len(a.CompactionFacts.ItemHashes)*compactionfacts.ItemHashSizeBytes) > maxFactBytes {
				return fmt.Errorf("largebody: accept compaction fact hashes exceed %d bytes", maxFactBytes)
			}
		}
		return nil
	default:
		return fmt.Errorf("largebody: unknown assessment decision %d", uint8(a.Decision))
	}
}

// NewDeclinedAssessment creates a declined Assessment with a bounded decline reason.
func NewDeclinedAssessment(reason DeclineReason) (Assessment, error) {
	if reason == DeclineReasonNone || reason.String() == "unknown" {
		return Assessment{}, fmt.Errorf("largebody: decline requires a valid bounded reason")
	}
	return Assessment{
		Decision: AssessmentDecisionDecline,
		Reason:   reason,
	}, nil
}

// NewAcceptedAssessment creates an accepted Assessment with an opaque stamp and bounded facts.
func NewAcceptedAssessment(stamp AssessmentStamp, wireReq WireRequestFacts, wireDomain WireDomainFacts) (Assessment, error) {
	if stamp.IsZero() {
		return Assessment{}, fmt.Errorf("largebody: accept requires a non-zero assessment stamp")
	}
	return Assessment{
		Decision:    AssessmentDecisionAccept,
		Reason:      DeclineReasonNone,
		Stamp:       stamp,
		WireRequest: wireReq,
		WireDomain:  wireDomain,
	}, nil
}

// WireRequestFacts is the immutable provider-neutral input to a backend
// exact wire proof (design section 9). Outbound header construction stays
// backend-owned; no client headers travel here.
type WireRequestFacts struct {
	ProfileID            string
	Operation            aipapi.Operation
	Delivery             aipapi.DeliveryMode
	BodyMode             BodyMode
	Rewrite              RewriteSemantics
	ClientModel          string
	CandidateModel       string
	MaxOutputTokens      int64
	RequiredCapabilities []aipapi.Capability
}

// Validate enforces bounds under the semantic-fact budget.
func (f WireRequestFacts) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	if strings.TrimSpace(f.ProfileID) == "" {
		return fmt.Errorf("largebody: wire facts profile id must not be empty")
	}
	if strings.TrimSpace(string(f.Operation)) == "" {
		return fmt.Errorf("largebody: wire facts operation must not be empty")
	}
	switch f.Delivery {
	case aipapi.DeliveryModeStreaming, aipapi.DeliveryModeNonStreaming:
	default:
		return fmt.Errorf("largebody: wire facts delivery mode %q is unknown", string(f.Delivery))
	}
	if err := f.BodyMode.Validate(); err != nil {
		return err
	}
	if err := f.Rewrite.Validate(); err != nil {
		return err
	}
	for name, v := range map[string]string{
		"profile id":      f.ProfileID,
		"operation":       string(f.Operation),
		"client model":    f.ClientModel,
		"candidate model": f.CandidateModel,
	} {
		if int64(len(v)) > maxFactBytes {
			return fmt.Errorf("largebody: wire facts %s exceeds %d bytes", name, maxFactBytes)
		}
	}
	if err := capabilityfacts.ValidateCapabilities(f.RequiredCapabilities, maxFactBytes); err != nil {
		return err
	}
	if f.MaxOutputTokens < 0 {
		return fmt.Errorf("largebody: wire facts max output tokens must be >= 0, got %d", f.MaxOutputTokens)
	}
	return nil
}

// WireDomainFacts is the provider-neutral input to a backend late-route
// domain proof (design section 9, Requirement 7). A universal domain
// (AnyAcceptedModel) must not also enumerate models, and a finite domain
// must enumerate at least one proven model.
type WireDomainFacts struct {
	ProfileID            string
	Operation            aipapi.Operation
	Delivery             aipapi.DeliveryMode
	BodyMode             BodyMode
	Rewrite              RewriteSemantics
	UniversalModel       bool
	CandidateModels      []string
	RequiredCapabilities []aipapi.Capability
}

// Validate enforces universal/finite exclusivity and bounds.
func (f WireDomainFacts) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	if strings.TrimSpace(f.ProfileID) == "" {
		return fmt.Errorf("largebody: wire domain profile id must not be empty")
	}
	if strings.TrimSpace(string(f.Operation)) == "" {
		return fmt.Errorf("largebody: wire domain operation must not be empty")
	}
	switch f.Delivery {
	case aipapi.DeliveryModeStreaming, aipapi.DeliveryModeNonStreaming:
	default:
		return fmt.Errorf("largebody: wire domain delivery mode %q is unknown", string(f.Delivery))
	}
	if err := f.BodyMode.Validate(); err != nil {
		return err
	}
	if err := f.Rewrite.Validate(); err != nil {
		return err
	}
	if err := capabilityfacts.ValidateCapabilities(f.RequiredCapabilities, maxFactBytes); err != nil {
		return err
	}
	if int64(len(f.ProfileID)) > maxFactBytes || int64(len(f.Operation)) > maxFactBytes {
		return fmt.Errorf("largebody: wire domain identity exceeds %d bytes", maxFactBytes)
	}
	if f.UniversalModel && len(f.CandidateModels) > 0 {
		return fmt.Errorf("largebody: universal wire domain must not enumerate models")
	}
	if !f.UniversalModel && len(f.CandidateModels) == 0 {
		return fmt.Errorf("largebody: finite wire domain must enumerate at least one model")
	}
	if int64(len(f.CandidateModels)) > maxFactBytes {
		return fmt.Errorf("largebody: wire domain model count exceeds %d", maxFactBytes)
	}
	for i, model := range f.CandidateModels {
		if strings.TrimSpace(model) == "" {
			return fmt.Errorf("largebody: wire domain model %d must not be empty", i)
		}
		if int64(len(model)) > maxFactBytes {
			return fmt.Errorf("largebody: wire domain model %d exceeds %d bytes", i, maxFactBytes)
		}
	}
	return nil
}

// RewritePlan is the per-attempt certified splice: exact scanner span plus
// JSON-encoded replacement model plus the checked rewritten body length.
// A splice reader emits prefix + replacement + suffix without a second full
// body (Requirement 9.3).
type RewritePlan struct {
	Rewrite          RewriteSemantics
	ReplacementModel string
	RewrittenLength  int64
}

// Validate requires certified rewrite semantics, a bounded replacement
// model, and a positive rewritten length.
func (p RewritePlan) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	if err := p.Rewrite.Validate(); err != nil {
		return err
	}
	if !p.Rewrite.NeedsModelRewrite() {
		return fmt.Errorf("largebody: rewrite plan requires certified model-rewrite semantics")
	}
	if strings.TrimSpace(p.ReplacementModel) == "" {
		return fmt.Errorf("largebody: rewrite plan replacement model must not be empty")
	}
	if int64(len(p.ReplacementModel)) > maxFactBytes {
		return fmt.Errorf("largebody: rewrite plan replacement model exceeds %d bytes", maxFactBytes)
	}
	if p.RewrittenLength <= 0 {
		return fmt.Errorf("largebody: rewrite plan rewritten length must be > 0, got %d", p.RewrittenLength)
	}
	return nil
}

// ResponseFacts carries bounded provider-neutral response facts for
// frontend wrapping/encoding (Requirement 18.2): request/trace identity,
// A-leg/session facts, operation/delivery, and replay/rewrite evidence.
// The sensitive session-response carrier travels separately.
type ResponseFacts struct {
	RequestID       string
	TraceID         string
	ALegID          string
	SessionID       string
	Operation       aipapi.Operation
	Delivery        aipapi.DeliveryMode
	EffectiveModel  string
	Source          SourceDigest
	BodyBytes       int64
	RewrittenLength int64
}

// Validate enforces identity presence and bounds.
func (f ResponseFacts) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	for name, v := range map[string]string{
		"request id":      f.RequestID,
		"trace id":        f.TraceID,
		"a-leg id":        f.ALegID,
		"session id":      f.SessionID,
		"operation":       string(f.Operation),
		"effective model": f.EffectiveModel,
	} {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("largebody: response facts %s must not be empty", name)
		}
		if int64(len(v)) > maxFactBytes {
			return fmt.Errorf("largebody: response facts %s exceeds %d bytes", name, maxFactBytes)
		}
	}
	switch f.Delivery {
	case aipapi.DeliveryModeStreaming, aipapi.DeliveryModeNonStreaming:
	default:
		return fmt.Errorf("largebody: response facts delivery mode %q is unknown", string(f.Delivery))
	}
	if f.Source.IsZero() {
		return fmt.Errorf("largebody: response facts source digest must not be zero")
	}
	if f.BodyBytes <= 0 {
		return fmt.Errorf("largebody: response facts body bytes must be > 0, got %d", f.BodyBytes)
	}
	if f.RewrittenLength < 0 {
		return fmt.Errorf("largebody: response facts rewritten length must be >= 0, got %d", f.RewrittenLength)
	}
	return nil
}

// SessionResponseCarrier returns new-session BeginTurn response values to
// the frontend: authoritative session ID, A-leg ID, and the raw resume
// token (design section 10, Requirement 14.6). Values are never metrics
// labels/log fields and are released with request lifetime
// (Requirements 14.7, 22.3): every default rendering is redacted and the
// frontend extracts values explicitly for session/resume headers.
type SessionResponseCarrier struct {
	AuthoritativeSessionID string
	ALegID                 string
	ResumeToken            SensitiveString
}

// Validate enforces bounds under the semantic-fact budget.
func (c SessionResponseCarrier) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	for name, v := range map[string]string{
		"authoritative session id": c.AuthoritativeSessionID,
		"a-leg id":                 c.ALegID,
	} {
		if int64(len(v)) > maxFactBytes {
			return fmt.Errorf("largebody: session carrier %s exceeds %d bytes", name, maxFactBytes)
		}
	}
	if int64(len(c.ResumeToken.Reveal())) > maxFactBytes {
		return fmt.Errorf("largebody: session carrier resume token exceeds %d bytes", maxFactBytes)
	}
	return nil
}

// String renders presence signals only; values stay redacted.
func (c SessionResponseCarrier) String() string {
	return fmt.Sprintf("SessionResponseCarrier{has_session_id:%t has_aleg_id:%t has_resume_token:%t}",
		c.AuthoritativeSessionID != "", c.ALegID != "", !c.ResumeToken.IsZero())
}

// GoString renders presence signals only; values stay redacted.
func (c SessionResponseCarrier) GoString() string { return c.String() }

// Format renders presence signals only for every verb.
func (c SessionResponseCarrier) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, c.String())
}

// MarshalJSON emits presence signals only; values stay redacted.
func (c SessionResponseCarrier) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		HasSessionID   bool `json:"has_session_id"`
		HasALegID      bool `json:"has_aleg_id"`
		HasResumeToken bool `json:"has_resume_token"`
	}{
		HasSessionID:   c.AuthoritativeSessionID != "",
		HasALegID:      c.ALegID != "",
		HasResumeToken: !c.ResumeToken.IsZero(),
	})
}

// ExecutionResult is the wire execution output (design section 13):
// the canonical event stream plus bounded response facts. An
// EventStream alone is insufficient (Requirement 18.1).
type ExecutionResult struct {
	Stream  aipapi.EventStream
	Facts   ResponseFacts
	Session SessionResponseCarrier
}

// ResponseFacts returns the bounded provider-neutral response facts (Requirement 18.2).
func (r ExecutionResult) ResponseFacts() ResponseFacts {
	return r.Facts
}

// Validate requires the canonical stream and valid bounded facts.
func (r ExecutionResult) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	if r.Stream == nil {
		return fmt.Errorf("largebody: execution result stream must not be nil")
	}
	if err := r.Facts.Validate(maxFactBytes); err != nil {
		return err
	}
	if err := r.Session.Validate(maxFactBytes); err != nil {
		return err
	}
	return nil
}

// DefaultMaxSemanticFactBytes is the default ceiling on profile-derived and
// wire-resolution semantic facts (256 KiB; Requirement 4, design section 3).
const DefaultMaxSemanticFactBytes int64 = 256 * 1024

type factBudgetCtxKey struct{}

// WithSemanticFactBudget attaches a configured semantic-fact budget to ctx.
func WithSemanticFactBudget(ctx context.Context, budget int64) context.Context {
	if budget <= 0 {
		return ctx
	}
	return context.WithValue(ctx, factBudgetCtxKey{}, budget)
}

// SemanticFactBudget returns the configured semantic-fact budget from ctx,
// or DefaultMaxSemanticFactBytes if not set or non-positive.
func SemanticFactBudget(ctx context.Context) int64 {
	if ctx != nil {
		if b, ok := ctx.Value(factBudgetCtxKey{}).(int64); ok && b > 0 {
			return b
		}
	}
	return DefaultMaxSemanticFactBytes
}

// checkBudget rejects a non-positive semantic-fact budget: validation
// without a budget cannot prove boundedness.
func checkBudget(maxFactBytes int64) error {
	if maxFactBytes <= 0 {
		return fmt.Errorf("largebody: fact budget must be > 0, got %d", maxFactBytes)
	}
	return nil
}
