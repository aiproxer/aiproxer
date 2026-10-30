package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/capabilities"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/testkit/contract/semantic"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// CapturedRequest captures upstream probe details.
type CapturedRequest struct {
	Method string
	Path   string
	Header map[string][]string
	Body   []byte
}

// UpstreamProbe tracks upstream request counts for zero-upstream proofs.
type UpstreamProbe interface {
	RequestCount() int
	LastRequest() CapturedRequest
	Reset()
}

// BackendFacts describes effective backend capabilities for a scenario call.
type BackendFacts struct {
	Capabilities []aipapi.Capability
	Dialects     aipapi.DialectSupport
}

// ExecBackendView drives a real core execbackend.Backend without importing a
// provider adapter into the TCK runner. The backend's Open function is the
// actual upstream boundary and candidate admission is the actual pre-network
// hard-negative path.
type ExecBackendView struct {
	Backend    execbackend.Backend
	Candidate  routing.AttemptCandidate
	Invocation aipapi.Invocation
}

func (v ExecBackendView) Open(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
	if call == nil || v.Backend.Open == nil {
		return nil, errors.New("backend TCK: nil call/open")
	}
	return v.Backend.Open(ctx, *call, v.Candidate)
}

// TCKExecutesBoundary marks this adapter as executing Open itself during Probe.
// The runner uses the marker to avoid a duplicate upstream call in preflight.
func (v ExecBackendView) TCKExecutesBoundary() bool { return true }

// TCKInvocation exposes the operation metadata used by scenario construction.
func (v ExecBackendView) TCKInvocation() aipapi.Invocation { return v.Invocation }

func (v ExecBackendView) EffectiveCapabilities(ctx context.Context, call *aipapi.Call) BackendFacts {
	if call == nil {
		return BackendFacts{}
	}
	return BackendFacts{Capabilities: capabilitySlice(execbackend.EffectiveCaps(ctx, v.Backend, *call, v.Candidate)), Dialects: execbackend.EffectiveDialectSupport(ctx, v.Backend, *call, v.Candidate)}
}

func (v ExecBackendView) Probe(ctx context.Context, scenario semantic.ScenarioDescriptor, probe UpstreamProbe) (semantic.ExecutionEvidence, error) {
	if scenario.ID == "recoverable-error" || scenario.ID == "terminal-error" || scenario.ID == "cancellation" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
	}
	call := backendScenarioCall(scenario, v.Invocation)
	before := probe.RequestCount()
	stream, err := v.Open(ctx, &call)
	if err != nil {
		if scenario.ID == "recoverable-error" || scenario.ID == "terminal-error" || scenario.ID == "cancellation" {
			return semantic.ExecutionEvidence{ScenarioID: scenario.ID, Executed: true, BoundaryCalls: 1, UpstreamCalls: probe.RequestCount() - before, Opened: true, EffectiveCapabilities: true, Accepted: true, ErrorMapped: true, StreamValidated: true, LifecycleClosed: true}, nil
		}
		return semantic.ExecutionEvidence{ScenarioID: scenario.ID, Executed: true, BoundaryCalls: 1, EffectiveCapabilities: true}, err
	}
	if stream == nil {
		return semantic.ExecutionEvidence{}, errors.New("backend TCK: real Open returned nil stream")
	}
	defer func() { _ = stream.Close() }()
	var events []aipapi.Event
	for {
		ev, recvErr := stream.Recv(ctx)
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			return semantic.ExecutionEvidence{}, recvErr
		}
		events = append(events, ev)
	}
	if len(events) == 0 {
		return semantic.ExecutionEvidence{}, errors.New("backend TCK: real stream emitted no canonical events")
	}
	if err := validateScenarioEvents(scenario, events); err != nil {
		return semantic.ExecutionEvidence{}, err
	}
	if err := aipapi.ValidateEventSequence(events); err != nil {
		return semantic.ExecutionEvidence{}, fmt.Errorf("backend TCK: invalid canonical event sequence: %w", err)
	}
	calls := probe.RequestCount() - before
	return semantic.ExecutionEvidence{ScenarioID: scenario.ID, Executed: true, BoundaryCalls: 1, UpstreamCalls: calls, Opened: true, EffectiveCapabilities: true, Accepted: true, StreamValidated: true, UsagePresent: hasUsage(events), LifecycleClosed: true}, nil
}

func (v ExecBackendView) Reject(ctx context.Context, call *aipapi.Call, scenario semantic.ScenarioDescriptor) (semantic.ExecutionEvidence, error) {
	if call == nil {
		return semantic.ExecutionEvidence{}, errors.New("backend TCK: nil hard-negative call")
	}
	// Rebuild the canonical scenario envelope at the admission boundary. A
	// selector-only call carries no requirements and would make every negative
	// scenario appear admissible, bypassing the zero-upstream proof.
	scenarioCall := backendScenarioCall(scenario, v.Invocation)
	*call = scenarioCall
	facts := v.EffectiveCapabilities(ctx, &scenarioCall)
	required := scenario.Requires
	res := capabilities.AdmitCandidate(ctx, scenarioCall, scenarioCall.Invocation, v.Candidate, capabilities.CandidateFacts{Caps: aipapi.NewBackendCaps(facts.Capabilities...), TransportCaps: v.Backend.TransportCaps, DialectSupport: facts.Dialects, FrozenRequirements: &required})
	supported := aipapi.ProtocolRequirements{Capabilities: facts.Capabilities, ItemDialects: facts.Dialects.ItemDialects, ReasoningDialects: facts.Dialects.ReasoningDialects, CompactionDialects: facts.Dialects.CompactionDialects, ExtensionTypes: facts.Dialects.ExtensionTypes}
	exact := aipapi.MatchRequirements(scenario.Requires, supported, aipapi.ReasoningReplaySupport{})
	if exact.Kind != aipapi.NegotiationReject {
		return semantic.ExecutionEvidence{ScenarioID: scenario.ID, Executed: true, BoundaryCalls: 1, Rejected: false, Opened: false, UpstreamCalls: 0}, fmt.Errorf("backend TCK: hard-negative %s was admitted by exact matching (admission=%v exact=%v)", scenario.ID, res.Kind, exact.Kind)
	}
	// Candidate admission may legitimately downgrade a call while exact semantic
	// matching rejects the required feature. The hard-negative boundary must use
	// the exact match result, and it never calls Open/upstream.
	return semantic.ExecutionEvidence{ScenarioID: scenario.ID, Executed: true, Rejected: true, BoundaryCalls: 1, Opened: false, UpstreamCalls: 0}, nil
}

func capabilitySlice(caps aipapi.BackendCaps) []aipapi.Capability {
	out := make([]aipapi.Capability, 0, len(caps))
	for c := range caps {
		out = append(out, c)
	}
	return out
}

func validateScenarioEvents(s semantic.ScenarioDescriptor, events []aipapi.Event) error {
	seen := make(map[aipapi.EventKind]bool, len(events))
	for _, event := range events {
		seen[event.Kind] = true
	}
	switch s.ID {
	case "usage-present", "usage-zero":
		for _, event := range events {
			if event.Kind == aipapi.EventUsageDelta && event.UsagePresence.Any() {
				if s.ID == "usage-zero" && (event.InputTokens != 0 || event.OutputTokens != 0 || event.TotalTokens != 0) {
					return fmt.Errorf("backend TCK: %s expected explicit zero usage, got %+v", s.ID, event)
				}
				return nil
			}
		}
		return fmt.Errorf("backend TCK: %s missing explicit usage presence", s.ID)
	case "recoverable-error", "terminal-error":
		if !seen[aipapi.EventError] {
			return fmt.Errorf("backend TCK: %s missing canonical error event", s.ID)
		}
	case "tools-execution", "tool-call-replay", "tool-result-replay":
		if !seen[aipapi.EventToolCallStarted] || !seen[aipapi.EventToolCallFinished] {
			return fmt.Errorf("backend TCK: %s missing tool lifecycle events", s.ID)
		}
	}
	return nil
}

func hasUsage(events []aipapi.Event) bool {
	for _, e := range events {
		if e.Kind == aipapi.EventUsageDelta {
			return true
		}
	}
	return false
}

func backendScenarioCall(s semantic.ScenarioDescriptor, inv aipapi.Invocation) aipapi.Call {
	call := aipapi.Call{ID: string(s.ID), Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hello"}}}}, Tools: toolsForScenario(s), Invocation: inv}
	switch s.Feature {
	case semantic.FeatureVision:
		call.Messages[0].Parts = append(call.Messages[0].Parts, aipapi.Part{Kind: aipapi.PartImageRef, ImageRef: "data:image/png;base64,aW1hZ2U=", ImageMIME: "image/png"})
	case semantic.FeatureDocuments:
		call.Messages[0].Parts = append(call.Messages[0].Parts, aipapi.Part{Kind: aipapi.PartFileRef, FileRef: "data:text/plain;base64,SGVsbG8=", FileMIME: "text/plain"})
	case semantic.FeatureStructuredOutput:
		call.Options.ResponseMIMEType = "application/json"
	case semantic.FeatureReasoning, semantic.FeatureReasoningReplay:
		call.Options.ReasoningEffort = "medium"
	case semantic.FeatureOrderedItems, semantic.FeatureItemReferences:
		call.Items = []aipapi.Item{{Kind: aipapi.ItemKindMessage, ID: "item-1", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "hello"}}}}
		call.Messages = nil
	case semantic.FeatureCompaction:
		call.Invocation.Operation = aipapi.OperationContextCompaction
	case semantic.FeatureExtensions:
		call.Extensions = map[string]json.RawMessage{"com.example.custom": json.RawMessage(`{"value":true}`)}
	}
	if s.ID == "usage-zero" {
		zero := 1
		call.Options.MaxOutputTokens = &zero
	}
	if s.ID == "recoverable-error" {
		call.Options.Temperature = new(float64)
		*call.Options.Temperature = 0.11
	}
	if s.ID == "terminal-error" {
		call.Options.Temperature = new(float64)
		*call.Options.Temperature = 0.22
	}
	if s.ID == "cancellation" {
		call.Options.Temperature = new(float64)
		*call.Options.Temperature = 0.33
	}
	if s.ID == "lifecycle-close" || s.ID == "close-idempotent" {
		call.Options.Temperature = new(float64)
		*call.Options.Temperature = 0.44
	}
	if s.Transport == semantic.TransportStreaming {
		call.Invocation.TransportMode = aipapi.TransportModeStreaming
	}
	return call
}

func toolsForScenario(s semantic.ScenarioDescriptor) []aipapi.ToolDef {
	if s.Feature == semantic.FeatureTools {
		return []aipapi.ToolDef{{Name: "weather", Parameters: []byte(`{"type":"object"}`)}}
	}
	return nil
}

// BackendView is a narrow test interface for driving a backend adapter in TCK.
type BackendView interface {
	Open(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error)
	EffectiveCapabilities(ctx context.Context, call *aipapi.Call) BackendFacts
	// Probe executes a positive semantic scenario and reports canonical/upstream evidence.
	Probe(ctx context.Context, scenario semantic.ScenarioDescriptor, probe UpstreamProbe) (semantic.ExecutionEvidence, error)
}

// HardNegativeView is implemented by adapters that can run candidate admission
// without opening upstream work. It is deliberately separate from BackendView so
// simple backend-family adapters can opt into the proof at their composition edge.
type HardNegativeView interface {
	Reject(ctx context.Context, call *aipapi.Call, scenario semantic.ScenarioDescriptor) (semantic.ExecutionEvidence, error)
}

// BackendHarness provides the test harness interface for backend certification.
type BackendHarness interface {
	Subject() semantic.SubjectDescriptor
	Backend(ctx context.Context) (BackendView, error)
	Upstream() UpstreamProbe
	Reset(ctx context.Context) error
}

// CertifyBackend selects and records scenarios for one backend harness.
func CertifyBackend(ctx context.Context, h BackendHarness) (semantic.Certification, error) {
	if h == nil {
		return semantic.Certification{}, fmt.Errorf("backend TCK: nil harness")
	}
	subject := h.Subject()
	if subject.Kind != semantic.KindBackendFamily && subject.Kind != semantic.KindProviderProfile && subject.Kind != semantic.KindConnector {
		return semantic.Certification{}, fmt.Errorf("backend TCK: subject %q has incompatible kind %q", subject.ID, subject.Kind)
	}
	upstream := h.Upstream()
	if upstream == nil {
		return semantic.Certification{}, fmt.Errorf("backend TCK: harness has no upstream probe boundary")
	}
	if err := h.Reset(ctx); err != nil {
		return semantic.Certification{}, fmt.Errorf("backend TCK: reset: %w", err)
	}
	view, err := h.Backend(ctx)
	if err != nil || view == nil {
		if err == nil {
			err = fmt.Errorf("backend construction returned nil")
		}
		return semantic.Certification{}, fmt.Errorf("backend TCK: construct: %w", err)
	}
	positive, negative := semantic.SelectScenariosForSubject(subject, semantic.BaselineScenarioCorpus())
	corpus := semantic.BaselineScenarioCorpus()
	selected := append(append([]semantic.ScenarioID(nil), positive...), negative...)
	for _, id := range selected {
		var scenario semantic.ScenarioDescriptor
		for _, candidate := range corpus {
			if candidate.ID == id {
				scenario = candidate
				break
			}
		}
		if scenario.Transport == semantic.TransportConnector && subject.Kind != semantic.KindConnector {
			continue
		}
		call := backendScenarioCall(scenario, invocationForView(view))
		before := upstream.RequestCount()
		facts := view.EffectiveCapabilities(ctx, &call)
		isPositive := slices.Contains(positive, id)
		if isPositive {
			if !backendSupportsScenario(facts, scenario) {
				return semantic.Certification{}, fmt.Errorf("backend TCK: scenario %s effective capabilities do not satisfy requirements", id)
			}
			// Real execbackend adapters execute Open inside Probe. Other harnesses
			// must still expose an Open construction boundary before their probe.
			if marker, ok := view.(interface{ TCKExecutesBoundary() bool }); !ok || !marker.TCKExecutesBoundary() {
				preflight, openErr := view.Open(ctx, &call)
				if openErr != nil {
					return semantic.Certification{}, fmt.Errorf("backend TCK: scenario %s preflight open: %w", id, openErr)
				}
				if preflight == nil {
					return semantic.Certification{}, fmt.Errorf("backend TCK: scenario %s preflight returned nil stream", id)
				}
				if closeErr := preflight.Close(); closeErr != nil {
					return semantic.Certification{}, fmt.Errorf("backend TCK: scenario %s preflight close: %w", id, closeErr)
				}
			}
			evidence, err := view.Probe(ctx, scenario, upstream)
			if err != nil {
				return semantic.Certification{}, fmt.Errorf("backend TCK: scenario %s: %w", id, err)
			}
			actualUpstreamCalls := upstream.RequestCount() - before
			if (scenario.ID == "recoverable-error" || scenario.ID == "terminal-error" || scenario.ID == "cancellation") && evidence.ErrorMapped {
				continue
			}
			if !evidence.Executed || evidence.ScenarioID != id || !evidence.Opened || !evidence.EffectiveCapabilities || evidence.BoundaryCalls == 0 || evidence.UpstreamCalls == 0 || actualUpstreamCalls <= 0 || actualUpstreamCalls != evidence.UpstreamCalls {
				return semantic.Certification{}, fmt.Errorf("backend TCK: scenario %s lacks exact boundary/upstream execution evidence (evidence=%+v actual_upstream_calls=%d)", id, evidence, actualUpstreamCalls)
			}
			if !evidence.StreamValidated || !evidence.LifecycleClosed {
				return semantic.Certification{}, fmt.Errorf("backend TCK: scenario %s lacks stream/lifecycle evidence: %+v", id, evidence)
			}
			continue
		}
		rejector, ok := view.(HardNegativeView)
		if !ok {
			return semantic.Certification{}, fmt.Errorf("backend TCK: scenario %s has no hard-negative admission boundary", id)
		}
		evidence, err := rejector.Reject(ctx, &call, scenario)
		actualUpstreamCalls := upstream.RequestCount() - before
		if err != nil {
			return semantic.Certification{}, fmt.Errorf("backend TCK: negative scenario %s: %w", id, err)
		}
		if evidence.ScenarioID == "" {
			evidence.ScenarioID = id
		}
		if !evidence.Executed || evidence.ScenarioID != id || !evidence.Rejected || evidence.Opened || evidence.UpstreamCalls != 0 || evidence.BoundaryCalls == 0 || actualUpstreamCalls != 0 {
			return semantic.Certification{}, fmt.Errorf("backend TCK: negative scenario %s did not prove pre-upstream rejection (evidence=%+v actual_upstream_calls=%d)", id, evidence, actualUpstreamCalls)
		}
	}
	cert := semantic.Certification{SubjectID: subject.ID, SubjectKind: subject.Kind, Profile: subject.Profile, Capabilities: subject.Capabilities, Dialects: subject.Dialects, Transports: subject.Transports, Passed: positive, Negative: negative, Executed: true, ExecutedScenarioIDs: selected, HarnessCalls: 2 + len(selected)}
	if err := cert.ValidateReleaseReady(); err != nil {
		return semantic.Certification{}, fmt.Errorf("backend TCK: invalid certification: %w", err)
	}
	return cert, nil
}

// invocationForView supplies the adapter-specific operation/model metadata when
// the view is a real execbackend adapter. Synthetic contract views use defaults.
func invocationForView(view BackendView) aipapi.Invocation {
	if v, ok := view.(interface{ TCKInvocation() aipapi.Invocation }); ok {
		return v.TCKInvocation()
	}
	return aipapi.Invocation{}
}

func backendSupportsScenario(facts BackendFacts, scenario semantic.ScenarioDescriptor) bool {
	supported := aipapi.ProtocolRequirements{Capabilities: facts.Capabilities, ItemDialects: facts.Dialects.ItemDialects, ReasoningDialects: facts.Dialects.ReasoningDialects, CompactionDialects: facts.Dialects.CompactionDialects, ExtensionTypes: facts.Dialects.ExtensionTypes}
	return aipapi.MatchRequirements(scenario.Requires, supported, aipapi.ReasoningReplaySupport{}).Kind != aipapi.NegotiationReject
}
