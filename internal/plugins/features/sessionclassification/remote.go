package sessionclassification

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/aiproxer/aiproxer/internal/agentfacts"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	sdkclassification "github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
)

// The remote decision contract. Everything in this file is provider-neutral
// and derived: the feature policy depends only on the RemoteDecider port, its
// bounded input, and its bounded decision, so a vendor HTTP client stays entirely
// inside the adapter package that implements the port. No TypeSafe/Jev wire DTO,
// endpoint, request/response shape, or auth format is declared, named, or
// inferred here (requirements 6.1, 6.7, 7.1, 7.2, 7.5, 11.5; design.md
// "Provider-neutral port" and "Architecture and QA").

var (
	// ErrRemoteNotConfigured reports that a generation's policy does not permit a
	// remote decision, or that its remote settings are absent or unsafe. The
	// owning composition step turns this into a candidate-generation failure
	// rather than publishing a partially configured remote mode (requirements
	// 6.10, 8.3, 8.4).
	ErrRemoteNotConfigured = errors.New("session classification: remote classification is not configured")
	// ErrInvalidRemoteInput reports derived evidence that leaves the bounded
	// remote contract. The attempt is refused before any egress instead of
	// forwarding an unbounded or unrecognised fact (requirements 6.7, 7.1, 7.2).
	ErrInvalidRemoteInput = errors.New("session classification: invalid remote input")
	// ErrInvalidRemoteDecision reports a remote result that cannot be thresholded
	// safely. It degrades to unknown and never fails the user request
	// (requirements 4.4, 6.9).
	ErrInvalidRemoteDecision = errors.New("session classification: invalid remote decision")
)

// WorkspaceClass is the bounded, content-free summary of the resolved workspace
// for one remote decision. It records only whether a recognized project marker
// was present in the bounded marker inspection window; it is never the workspace
// root or any path (requirements 7.2, 7.3).
type WorkspaceClass string

const (
	// WorkspaceClassUnmarked reports that no recognized project marker was seen in
	// the bounded inspection window. It is the zero value.
	WorkspaceClassUnmarked WorkspaceClass = ""
	// WorkspaceClassProjectMarker reports a recognized project marker, the single
	// workspace fact local policy can corroborate.
	WorkspaceClassProjectMarker WorkspaceClass = "project_marker"
)

// RemoteInput is the derived, content-free evidence handed to one remote
// decision. Every field is a closed enumeration or a fixed-width scalar, so the
// struct cannot hold request content: it has no field for the raw client
// User-Agent, the workspace root, a prompt, a transcript, tool names or tool
// arguments, a session or A-leg identifier, a credential, or a vendor
// request/response value (requirements 7.1, 7.2, 7.3, 7.7).
type RemoteInput struct {
	// Operation is the bounded protocol operation already recorded for the turn.
	Operation aipapi.Operation
	// ClientFamily is the normalized high-confidence client family, or the empty
	// value when no supported coding-harness identity was recognized.
	ClientFamily agentfacts.Family
	// HasAmbiguousClient reports an accepted client identity that is not a
	// high-confidence coding-harness identity, such as a generic or underlying-SDK
	// User-Agent. It is never the identity value itself (requirements 3.2, 7.7).
	HasAmbiguousClient bool
	// ToolCategories is the fixed-width presence bitset of canonical tool-name
	// categories. Tool names are not retained (requirement 3.9).
	ToolCategories sdkclassification.ToolCategorySet
	// WorkspaceClass is the bounded workspace summary.
	WorkspaceClass WorkspaceClass
	// LocalEvidenceCode is the decisive local evidence code, or the empty value
	// when local evaluation found nothing decisive. A remote-decision code is
	// never valid here, because a remote input describes local evaluation only.
	LocalEvidenceCode session.EvidenceCode
}

// RemoteDecision is the bounded numeric result of one remote decision. It
// carries only the facts the feature threshold policy needs; a vendor response
// DTO never crosses the port (requirements 6.8, 9.3, 9.4, 11.5).
//
// The zero value is a valid below-threshold answer: it can never promote a
// session, so an adapter that decoded nothing usable fails safe rather than
// failing the request (requirements 4.4, 6.9).
type RemoteDecision struct {
	// CodingProbability is the bounded probability that the session is a
	// coding-agent session. It must be finite and within [0, 1].
	CodingProbability float64
	// Confidence is the bounded confidence the remote service reported for that
	// probability. It must be finite and within [0, 1]. It is evidence, not a
	// threshold input: the configured positive_threshold alone decides promotion,
	// so no unmeasured confidence floor is invented here (requirement 6.8).
	Confidence float64
}

// RemoteDecider is the provider-neutral remote classification port. Exactly one
// vendor adapter implements it, and that adapter owns every wire detail: the
// endpoint, the request/response mapping, the credential, the body bounds, and
// the mapping of transport failures onto the bounded RemoteOutcome vocabulary
// through the optional RemoteFailure interface.
//
// Contract, in full:
//
//   - Decide receives only derived bounded evidence and returns only bounded
//     numbers, or an error;
//   - a decision returned together with an error is never a positive answer;
//   - an error is a bounded remote failure that leaves an unknown session
//     unknown and never fails the user request (requirements 4.4, 6.9);
//   - absence of a positive is never a negative classification (requirement 6.8).
type RemoteDecider interface {
	Decide(ctx context.Context, in RemoteInput) (RemoteDecision, error)
}

// The closed vocabularies a remote input may name. They are bounded by
// construction: every value that is not a member below has no representation in
// the port, so an unrecognised fact cannot become egress payload.
var (
	remoteOperations = []aipapi.Operation{
		aipapi.OperationOpenAIChatCompletions,
		aipapi.OperationOpenAIResponses,
		aipapi.OperationOpenResponsesCreate,
		aipapi.OperationAnthropicMessages,
		aipapi.OperationGeminiGenerateContent,
		aipapi.OperationContextCompaction,
	}
	remoteClientFamilies = []agentfacts.Family{
		agentfacts.FamilyCodex,
		agentfacts.FamilyRoo,
		agentfacts.FamilyOpenCode,
		agentfacts.FamilyPi,
		agentfacts.FamilyDroid,
		agentfacts.FamilyHermes,
	}
	remoteWorkspaceClasses = []WorkspaceClass{WorkspaceClassProjectMarker}
	// remoteLocalEvidenceCodes is every decisive code local evaluation can
	// produce. EvidenceCodeRemoteAboveThreshold is deliberately absent: a remote
	// input describes local evaluation, so a remote code there would be a
	// fabricated fact. A family added to agentfacts must be given a code here in
	// the same change that adds it to clientFamilyEvidenceCode.
	remoteLocalEvidenceCodes = []session.EvidenceCode{
		EvidenceCodeCodex,
		EvidenceCodeDroid,
		EvidenceCodeHermes,
		EvidenceCodeOpenCode,
		EvidenceCodePi,
		EvidenceCodeRoo,
		EvidenceCodeDistinctCluster,
		EvidenceCodeProjectMarkerCluster,
	}
)

// RemotePolicy is the validated remote posture of one generation. It exists so a
// generation can obtain a remote posture only through validation: there is no
// other way to name a provider, a credential reference, or a threshold, and a
// heuristic-mode configuration cannot produce one at all (requirements 6.1, 6.2,
// 6.10, 8.4).
//
// The zero RemotePolicy is inert: it names no mode, no provider, no credential
// reference, and no threshold, so it cannot authorise a remote attempt.
type RemotePolicy struct {
	mode   Mode
	remote RemoteConfig
}

// NewRemotePolicy validates one generation's configuration and returns the remote
// posture it may serve. It fails for a mode that must never reach the network
// (including heuristic mode and an unknown mode), for remote modes without
// explicit remote settings, and for any remote setting outside its safe finite
// bounds, so an unservable remote posture is rejected before publication
// (requirements 6.1, 6.2, 6.7, 6.10, 8.3, 8.4).
func NewRemotePolicy(cfg Config) (RemotePolicy, error) {
	switch cfg.Mode {
	case ModeJev, ModeHybrid:
	default:
		return RemotePolicy{}, fmt.Errorf("%w: mode %q never constructs or calls a remote decider", ErrRemoteNotConfigured, cfg.Mode)
	}
	if cfg.Remote == nil {
		return RemotePolicy{}, fmt.Errorf("%w: remote mode requires explicit remote settings", ErrRemoteNotConfigured)
	}
	if err := cfg.Validate(); err != nil {
		return RemotePolicy{}, fmt.Errorf("%w: %w", ErrRemoteNotConfigured, err)
	}
	// The validated remote settings are copied, so a later in-place edit of the
	// decoded configuration cannot change an already-validated posture.
	return RemotePolicy{mode: cfg.Mode, remote: *cfg.Remote}, nil
}

// Mode returns the generation mode this posture was derived from.
func (p RemotePolicy) Mode() Mode { return p.mode }

// Threshold returns the validated positive threshold used for remote promotion.
func (p RemotePolicy) Threshold() float64 { return p.remote.PositiveThreshold }

// CredentialReference returns the validated environment-variable name that holds
// the remote credential. It is a reference, never a credential value: this package
// does not read, store, log, or echo the referenced value (requirements 7.1, 7.5,
// 8.3).
func (p RemotePolicy) CredentialReference() string { return p.remote.APIKeyEnv }

// Config returns a copy of the validated remote settings, including the provider,
// hard timeout, attempt budget, lease TTL, retry backoff, and threshold the
// generation must enforce.
func (p RemotePolicy) Config() RemoteConfig { return p.remote }

// BuildRemoteInput normalizes one bounded metadata snapshot and the local
// decision that was reached for it into remote input. It is the only place where
// accepted client identity is read, and it keeps nothing but the normalized
// family and ambiguity bit: the raw User-Agent, the workspace root, and the
// marker names are reduced here and never stored (requirements 7.1, 7.2, 7.3).
//
// Normalization reduces a value outside a closed vocabulary to that vocabulary's
// absent value, so an unrecognised fact is never forwarded as egress payload. The
// operation is the one exception: it has no safe absent value, so it is forwarded
// unchanged and ValidateRemoteInput refuses an operation outside the closed
// vocabulary instead of the builder hiding a carrier defect.
func BuildRemoteInput(input sdkclassification.Input, decision LocalDecision) RemoteInput {
	family, ambiguous := normalizedClientIdentity(input.Evidence.ClientUserAgent)
	return RemoteInput{
		Operation:          input.Evidence.Operation,
		ClientFamily:       family,
		HasAmbiguousClient: ambiguous,
		ToolCategories:     input.Evidence.ToolCategories & sdkclassification.DefinedToolCategoryBits,
		WorkspaceClass:     boundedWorkspaceClass(input.Workspace.Markers),
		LocalEvidenceCode:  boundedLocalEvidenceCode(decision.EvidenceCode),
	}
}

// ValidateRemoteInput checks that in names only closed vocabularies and
// fixed-width bits, and that it is internally consistent. It is the gate an
// adapter or caller must satisfy before Decide: an input with no operation, an
// unrecognised family, an undefined tool bit, or a code outside the local
// vocabulary is refused instead of being sent (requirements 6.7, 7.1, 7.2).
func ValidateRemoteInput(in RemoteInput) error {
	if !remoteOperationAllowed(in.Operation) {
		return fmt.Errorf("%w: operation is outside the closed operation vocabulary", ErrInvalidRemoteInput)
	}
	if !remoteClientFamilyAllowed(in.ClientFamily) {
		return fmt.Errorf("%w: client family is outside the closed family vocabulary", ErrInvalidRemoteInput)
	}
	if in.ClientFamily != "" && in.HasAmbiguousClient {
		return fmt.Errorf("%w: a recognized client family cannot also be an ambiguous identity", ErrInvalidRemoteInput)
	}
	if in.ToolCategories&^sdkclassification.DefinedToolCategoryBits != 0 {
		return fmt.Errorf("%w: tool categories contain undefined bits", ErrInvalidRemoteInput)
	}
	if !workspaceClassAllowed(in.WorkspaceClass) {
		return fmt.Errorf("%w: workspace class is outside the closed workspace vocabulary", ErrInvalidRemoteInput)
	}
	if !localEvidenceCodeAllowed(in.LocalEvidenceCode) {
		return fmt.Errorf("%w: local evidence code is outside the closed local vocabulary", ErrInvalidRemoteInput)
	}
	return nil
}

// ValidateRemoteDecision checks that both numbers are finite and inside the
// probability range a threshold can be applied to. A NaN, an infinity, a negative
// value, or a value above one is a bounded remote error: the session stays
// unknown and the user request continues (requirements 4.4, 6.9).
func ValidateRemoteDecision(decision RemoteDecision) error {
	if !remoteProbability(decision.CodingProbability) {
		return fmt.Errorf("%w: coding probability is not a finite value within zero and one", ErrInvalidRemoteDecision)
	}
	if !remoteProbability(decision.Confidence) {
		return fmt.Errorf("%w: confidence is not a finite value within zero and one", ErrInvalidRemoteDecision)
	}
	return nil
}

// Positive reports whether this validated decision meets the policy threshold.
// Only the configured positive_threshold decides promotion: a hostile threshold
// or an invalid decision can never yield a positive, and a below-threshold answer
// is an unknown session rather than a negative classification (requirements 6.8,
// 1.4, 1.5).
func (d RemoteDecision) Positive(threshold float64) bool {
	if !validRemoteThreshold(threshold) || ValidateRemoteDecision(d) != nil {
		return false
	}
	return d.CodingProbability >= threshold
}

// validRemoteThreshold reports whether threshold is a usable positive threshold.
// The range comparison is deliberate: NaN fails both comparisons and an infinity
// fails one, so no explicit NaN/Inf test is needed and none is missed.
func validRemoteThreshold(threshold float64) bool {
	return threshold > 0 && threshold <= 1
}

// remoteProbability reports whether value is a finite probability. NaN and both
// infinities fail the range comparison.
func remoteProbability(value float64) bool {
	return value >= 0 && value <= 1
}

// normalizedClientIdentity reduces an accepted client identity to the normalized
// family plus an ambiguity bit. An unrecognised identity is ambiguous evidence
// rather than identity authority: it is never promoted into proxy identity, and
// the value itself is not retained (requirements 3.2, 7.7).
func normalizedClientIdentity(clientUserAgent string) (agentfacts.Family, bool) {
	if clientUserAgent == "" {
		return "", false
	}
	if match, ok := agentfacts.MatchIdentity(clientUserAgent); ok && match.Confidence == agentfacts.ConfidenceHigh {
		return match.Family, false
	}
	return "", true
}

func remoteOperationAllowed(operation aipapi.Operation) bool {
	return slices.Contains(remoteOperations, operation)
}

func remoteClientFamilyAllowed(family agentfacts.Family) bool {
	return family == "" || slices.Contains(remoteClientFamilies, family)
}

func boundedWorkspaceClass(markers []string) WorkspaceClass {
	if hasRecognizedProjectMarker(markers) {
		return WorkspaceClassProjectMarker
	}
	return WorkspaceClassUnmarked
}

func workspaceClassAllowed(class WorkspaceClass) bool {
	return class == "" || slices.Contains(remoteWorkspaceClasses, class)
}

func boundedLocalEvidenceCode(code session.EvidenceCode) session.EvidenceCode {
	if localEvidenceCodeAllowed(code) {
		return code
	}
	return ""
}

func localEvidenceCodeAllowed(code session.EvidenceCode) bool {
	return code == "" || slices.Contains(remoteLocalEvidenceCodes, code)
}
