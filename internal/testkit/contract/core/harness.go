package core

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/aiproxer/aiproxer/internal/testkit/contract/semantic"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// RequirementDerivationView tests canonical core requirement derivation and matching without real providers.
type RequirementDerivationView interface {
	DeriveRequirements(call *aipapi.Call) (aipapi.ProtocolRequirements, error)
	MatchRequirements(reqs aipapi.ProtocolRequirements, caps []aipapi.Capability) bool
	Probe(ctx context.Context, scenario semantic.ScenarioDescriptor) (semantic.ExecutionEvidence, error)
}

// CoreHarness provides test interfaces for canonical-core certification.
type CoreHarness interface {
	Subject() semantic.SubjectDescriptor
	Derivation(ctx context.Context) (RequirementDerivationView, error)
}

// SemanticInvariantHooks is the dependency-neutral seam used by the core TCK.
// The default hooks call canonical aipapi/core operations; tests can replace one
// hook with a mutation double and prove that the corresponding invariant turns RED.
// No frontend or backend adapter is involved in this suite.
type SemanticInvariantHooks struct {
	Derive        func(aipapi.Call) aipapi.ProtocolRequirements
	Match         func(aipapi.ProtocolRequirements, aipapi.ProtocolRequirements) bool
	ProjectItems  func(aipapi.Call, aipapi.LegacyProjectionTarget) (aipapi.LegacyProjectionResult, error)
	ProjectLegacy func(aipapi.Call, aipapi.OrderedItemProjectionTarget) ([]aipapi.Item, aipapi.ProtocolRequirements, error)
	Admit         func(aipapi.CandidateAdmissionInput) aipapi.CandidateAdmissionResult
	Validate      func([]aipapi.Event) error
	OutputPolicy  func(committed, retry bool) error
	StickyCleanup func(affinityID string, admitted bool) bool
}

func DefaultSemanticInvariantHooks() SemanticInvariantHooks {
	return SemanticInvariantHooks{
		Derive: aipapi.DeriveProtocolRequirements,
		Match: func(req, supported aipapi.ProtocolRequirements) bool {
			return aipapi.MatchRequirements(req, supported, aipapi.ReasoningReplaySupport{}).Kind != aipapi.NegotiationReject
		},
		ProjectItems:  aipapi.ProjectItemsToLegacyView,
		ProjectLegacy: aipapi.ProjectLegacyToOrderedItems,
		Admit:         aipapi.AdmitCandidate,
		Validate:      aipapi.ValidateEventSequence,
		OutputPolicy: func(committed, retry bool) error {
			if committed && retry {
				return errors.New("output committed: retry/failover is forbidden")
			}
			return nil
		},
		StickyCleanup: func(affinityID string, admitted bool) bool {
			return !admitted
		},
	}
}

// RunSemanticInvariantSuite exercises the complete canonical semantic boundary.
// It intentionally validates presence-bearing projectors, transport admission,
// frozen failover obligations, output commitment, and terminal sequence rules.
func RunSemanticInvariantSuite(h SemanticInvariantHooks) error {
	if h.Derive == nil || h.Match == nil || h.ProjectItems == nil || h.ProjectLegacy == nil || h.Admit == nil || h.Validate == nil || h.OutputPolicy == nil {
		return errors.New("core TCK: incomplete semantic invariant hooks")
	}
	text := "hello"
	call := aipapi.Call{
		Items:      []aipapi.Item{{Kind: aipapi.ItemKindMessage, ID: "m1", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: text}}}},
		Messages:   []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: text}}}},
		Tools:      []aipapi.ToolDef{{Name: "weather", Parameters: []byte(`{"type":"object"}`)}},
		Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions, DeliveryMode: aipapi.DeliveryModeStreaming, TransportMode: aipapi.TransportModeStreaming},
	}
	req := h.Derive(call)
	if !slices.Contains(req.Capabilities, aipapi.CapabilityOrderedItems) || !slices.Contains(req.Capabilities, aipapi.CapabilityTools) {
		return fmt.Errorf("core TCK: derivation lost required capabilities: %v", req.Capabilities)
	}
	if !h.Match(req, aipapi.ProtocolRequirements{Capabilities: append([]aipapi.Capability(nil), req.Capabilities...)}) {
		return errors.New("core TCK: exact requirement matching rejected supported capabilities")
	}
	if h.Match(req, aipapi.ProtocolRequirements{Capabilities: []aipapi.Capability{aipapi.CapabilityOrderedItems}}) {
		return errors.New("core TCK: exact matching accepted missing tools capability")
	}

	itemCall := aipapi.Call{Items: []aipapi.Item{{Kind: aipapi.ItemKindMessage, ID: "presence", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: text}}}}}
	legacy, err := h.ProjectItems(itemCall, aipapi.DefaultLegacyProjectionTarget(aipapi.NewBackendCaps(aipapi.CapabilityStreaming), aipapi.ReasoningReplaySupport{}))
	if err != nil || len(legacy.Messages) != 1 || legacy.Messages[0].Parts[0].Text != text {
		return fmt.Errorf("core TCK: item->legacy projection lost presence/value: %v", err)
	}
	ordered, _, err := h.ProjectLegacy(legacyCall(legacy.Messages), aipapi.DefaultOrderedItemProjectionTarget())
	if err != nil || len(ordered) != 1 || ordered[0].Content[0].Text != text {
		return fmt.Errorf("core TCK: legacy->item projection lost presence/value: %v", err)
	}

	transport := aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{Operation: call.Invocation.Operation, Modes: []aipapi.TransportMode{aipapi.TransportModeStreaming}})
	admission := h.Admit(aipapi.CandidateAdmissionInput{Call: call, Invocation: call.Invocation, BackendCaps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools, aipapi.CapabilityOrderedItems), TransportCaps: transport})
	if admission.Kind == aipapi.NegotiationReject {
		return fmt.Errorf("core TCK: transport admission rejected compatible candidate: %v", admission.Err())
	}
	badTransport := h.Admit(aipapi.CandidateAdmissionInput{Call: call, Invocation: call.Invocation, BackendCaps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools, aipapi.CapabilityOrderedItems), TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{Operation: call.Invocation.Operation, Modes: []aipapi.TransportMode{aipapi.TransportModeNonStreaming}})})
	if badTransport.Kind != aipapi.NegotiationReject {
		return errors.New("core TCK: transport admission accepted undeclared streaming mode")
	}
	if h.StickyCleanup != nil && !h.StickyCleanup("session-affinity-id", false) {
		return errors.New("core TCK: sticky affinity state was retained when candidate admission was rejected")
	}

	frozen := h.Derive(call)
	frozen.Capabilities = append(frozen.Capabilities, aipapi.CapabilityVision)
	frozen = aipapi.NormalizeProtocolRequirements(frozen)
	failoverCaps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools, aipapi.CapabilityOrderedItems)
	failover := h.Admit(aipapi.CandidateAdmissionInput{Call: call, Invocation: call.Invocation, BackendCaps: failoverCaps, TransportCaps: transport, FrozenRequirements: &frozen})
	if failover.Kind != aipapi.NegotiationReject {
		return errors.New("core TCK: failover candidate weakened frozen requirement union")
	}

	if err := h.OutputPolicy(false, true); err != nil {
		return fmt.Errorf("core TCK: pre-output retry unexpectedly forbidden: %w", err)
	}
	if err := h.OutputPolicy(true, true); err == nil {
		return errors.New("core TCK: output commitment allowed retry/failover")
	}
	events := []aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventMessageStarted}, {Kind: aipapi.EventTextDelta, Delta: text}, {Kind: aipapi.EventResponseFinished}}
	if err := h.Validate(events); err != nil {
		return fmt.Errorf("core TCK: valid terminal sequence rejected: %w", err)
	}
	broken := append([]aipapi.Event(nil), events...)
	broken[2], broken[1] = broken[1], broken[2]
	if err := h.Validate(broken); err == nil {
		return errors.New("core TCK: terminal validator accepted delta before message start")
	}
	return nil
}

func legacyCall(messages []aipapi.Message) aipapi.Call {
	return aipapi.Call{Messages: append([]aipapi.Message(nil), messages...)}
}

// CertifyCore selects and records scenarios for one canonical-core harness.
func CertifyCore(ctx context.Context, h CoreHarness) (semantic.Certification, error) {
	if h == nil {
		return semantic.Certification{}, fmt.Errorf("core TCK: nil harness")
	}
	subject := h.Subject()
	if subject.Kind != semantic.KindCanonicalCore {
		return semantic.Certification{}, fmt.Errorf("core TCK: subject %q must use canonical_core kind, got %q", subject.ID, subject.Kind)
	}
	positive, negative := semantic.SelectScenariosForSubject(subject, semantic.BaselineScenarioCorpus())
	view, err := h.Derivation(ctx)
	if err != nil || view == nil {
		if err == nil {
			err = fmt.Errorf("derivation returned nil")
		}
		return semantic.Certification{}, fmt.Errorf("core TCK: derivation boundary failed: %w", err)
	}
	call := &aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser}}}
	derived, err := view.DeriveRequirements(call)
	if err != nil {
		return semantic.Certification{}, fmt.Errorf("core TCK: derive requirements: %w", err)
	}
	if !view.MatchRequirements(derived, derived.Capabilities) {
		return semantic.Certification{}, fmt.Errorf("core TCK: exact matching rejected derived requirements")
	}
	corpus := semantic.BaselineScenarioCorpus()
	selected := append(append([]semantic.ScenarioID(nil), positive...), negative...)
	for _, id := range selected {
		for _, scenario := range corpus {
			if scenario.ID == id {
				evidence, err := view.Probe(ctx, scenario)
				if err != nil {
					return semantic.Certification{}, fmt.Errorf("core TCK: scenario %s: %w", id, err)
				}
				isPositive := slices.Contains(positive, id)
				if !evidence.Executed || evidence.ScenarioID != id || evidence.BoundaryCalls == 0 || !evidence.Derived || !evidence.ExactMatch {
					return semantic.Certification{}, fmt.Errorf("core TCK: scenario %s lacks derive/exact-match evidence", id)
				}
				if isPositive && !evidence.Accepted {
					return semantic.Certification{}, fmt.Errorf("core TCK: positive scenario %s lacks acceptance evidence", id)
				}
				if !isPositive && (!evidence.Rejected || evidence.Accepted) {
					return semantic.Certification{}, fmt.Errorf("core TCK: negative scenario %s lacks rejection evidence", id)
				}
				break
			}
		}
	}
	return semantic.Certification{SubjectID: subject.ID, SubjectKind: subject.Kind, Profile: subject.Profile, Capabilities: subject.Capabilities, Dialects: subject.Dialects, Transports: subject.Transports, Passed: positive, Negative: negative, Executed: true, ExecutedScenarioIDs: selected, HarnessCalls: 3 + len(selected)}, nil
}
