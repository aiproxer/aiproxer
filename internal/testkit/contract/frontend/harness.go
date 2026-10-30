package frontend

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"time"

	stdhttpauth "github.com/aiproxer/aiproxer/internal/stdhttp/auth"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"

	"github.com/aiproxer/aiproxer/internal/testkit/contract/semantic"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
)

// EventScript contains scripted canonical events to feed back to a frontend under test.
type EventScript struct {
	Events []aipapi.Event
	Err    error
	Stream func(context.Context) aipapi.EventStream
}

// FrontendViewDecorator lets protocol-specific contract tests add probes without
// making the generic harness own protocol or transport orchestration.
type FrontendViewDecorator func(FrontendView) FrontendView

// CapturingExecutor intercepts canonical calls and supplies scripted event streams.
type CapturingExecutor struct {
	Calls  []*aipapi.Call
	Script EventScript
}

// Execute captures the canonical call and returns a scripted stream.
func (e *CapturingExecutor) Execute(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
	if call != nil {
		e.Calls = append(e.Calls, call)
	}
	if e.Script.Err != nil {
		return nil, e.Script.Err
	}
	if e.Script.Stream != nil {
		return e.Script.Stream(ctx), nil
	}
	return aipapi.NewFixedEventStream(e.Script.Events), nil
}

func (e *CapturingExecutor) CancelALeg(context.Context, aipapi.ALegCancelRequest) error { return nil }

func (e *CapturingExecutor) WallClock() func() time.Time { return time.Now }

// MountedHarness adapts any frontend mount to the common TCK without importing
// a concrete frontend or backend. Request paths and bodies are supplied by the
// frontend contribution, keeping protocol-owned wire details at the edge.
type MountedHarness struct {
	Descriptor           semantic.SubjectDescriptor
	Mount                aipsdk.FrontendMount
	Path                 func(semantic.ScenarioDescriptor) string
	Body                 func(semantic.ScenarioDescriptor) []byte
	NegativeBody         func(semantic.ScenarioDescriptor) []byte
	Decorate             FrontendViewDecorator
	ExecutorBoundary     *CapturingExecutor
	ContinuationStore    aipcont.Store
	ContinuationResolver aipcont.Resolver
	// AuthProvider is applied as the real outer transport middleware. This keeps
	// protocol frontends that do not own auth behind the same boundary as the
	// standard distribution, while OpenResponses also receives the authenticated
	// context when its direct mount requires auth.
	AuthProvider httpauth.Provider
	Mux          *http.ServeMux
	Handler      http.Handler
}

func (h *MountedHarness) Subject() semantic.SubjectDescriptor { return h.Descriptor }
func (h *MountedHarness) Executor() *CapturingExecutor        { return h.ExecutorBoundary }
func (h *MountedHarness) Reset() error {
	if h.ExecutorBoundary == nil {
		h.ExecutorBoundary = &CapturingExecutor{}
	}
	h.ExecutorBoundary.Calls = nil
	h.Mux = http.NewServeMux()
	if h.Mount == nil {
		return fmt.Errorf("frontend TCK: nil mount")
	}
	defaultRoute := "stub"
	if h.Descriptor.ID == "openresponses" {
		defaultRoute = "stub:gpt-4o-mini"
	}
	if err := h.Mount(h.Mux, aipsdk.FrontendMountOptions{
		Exec:                 h.ExecutorBoundary,
		DefaultRoute:         defaultRoute,
		AllowUnauthenticated: h.AuthProvider == nil,
		ContinuationStore:    h.ContinuationStore,
		ContinuationResolver: h.ContinuationResolver,
	}); err != nil {
		return err
	}
	if h.AuthProvider != nil {
		h.Handler = stdhttpauth.Middleware(nil, []httpauth.Provider{h.AuthProvider}, h.Mux)
	} else {
		h.Handler = h.Mux
	}
	return nil
}

func (h *MountedHarness) Frontend(context.Context) (FrontendView, error) {
	if h.Mux == nil {
		return nil, fmt.Errorf("frontend TCK: mount not reset")
	}
	view := mountedView{
		mux:          h.Mux,
		handler:      h.Handler,
		subject:      h.Descriptor,
		path:         h.Path,
		body:         h.Body,
		negativeBody: h.NegativeBody,
	}
	if h.Decorate != nil {
		return h.Decorate(view), nil
	}
	return view, nil
}

type mountedView struct {
	mux          *http.ServeMux
	subject      semantic.SubjectDescriptor
	path         func(semantic.ScenarioDescriptor) string
	body         func(semantic.ScenarioDescriptor) []byte
	negativeBody func(semantic.ScenarioDescriptor) []byte
	handler      http.Handler
}

func (v mountedView) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if v.handler != nil {
		v.handler.ServeHTTP(w, r)
		return
	}
	v.mux.ServeHTTP(w, r)
}

func (v mountedView) Subject() semantic.SubjectDescriptor { return v.subject }

func (v mountedView) Probe(ctx context.Context, scenario semantic.ScenarioDescriptor, executor *CapturingExecutor) (semantic.ExecutionEvidence, error) {
	if v.path == nil || v.body == nil {
		return semantic.ExecutionEvidence{}, fmt.Errorf("frontend TCK: missing request fixture")
	}
	body := v.body(scenario)
	positiveIDs, _ := semantic.SelectScenariosForSubject(v.subject, semantic.BaselineScenarioCorpus())
	positive := containsScenario(positiveIDs, scenario.ID)
	if !positive {
		if v.negativeBody == nil {
			return semantic.ExecutionEvidence{}, fmt.Errorf("frontend TCK: missing protocol negative fixture for %s", scenario.ID)
		}
		body = v.negativeBody(scenario)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return semantic.ExecutionEvidence{}, fmt.Errorf("frontend TCK: %s has an empty request fixture", scenario.ID)
	}
	beforeCalls := len(executor.Calls)
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, v.path(scenario), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer frontend-tck")
	req.Header.Set("X-AIP-Session-ID", "frontend-tck-session")
	rec := httptest.NewRecorder()
	v.ServeHTTP(rec, req)
	if rec.Code >= 500 {
		return semantic.ExecutionEvidence{}, fmt.Errorf("frontend TCK: server returned %d body=%q", rec.Code, rec.Body.String())
	}
	deltaCalls := len(executor.Calls) - beforeCalls
	if positive && deltaCalls == 0 {
		return semantic.ExecutionEvidence{}, fmt.Errorf("frontend TCK: %s accepted valid request without canonical call (status=%d body=%q path=%s)", scenario.ID, rec.Code, rec.Body.String(), v.path(scenario))
	}
	if !positive && deltaCalls != 0 {
		return semantic.ExecutionEvidence{}, fmt.Errorf("frontend TCK: negative scenario %s reached executor", scenario.ID)
	}
	if positive && executor.Script.Err != nil {
		return semantic.ExecutionEvidence{}, fmt.Errorf("frontend TCK: %s script failed: %w", scenario.ID, executor.Script.Err)
	}
	if !positive {
		if rec.Code < 400 || rec.Code >= 500 {
			return semantic.ExecutionEvidence{}, fmt.Errorf("frontend TCK: negative scenario %s was not rejected with a 4xx (got %d)", scenario.ID, rec.Code)
		}
		if len(strings.TrimSpace(rec.Body.String())) == 0 {
			return semantic.ExecutionEvidence{}, fmt.Errorf("frontend TCK: negative scenario %s returned empty error mapping", scenario.ID)
		}
	}
	if positive {
		if err := validateCanonicalCall(scenario, executor.Calls[len(executor.Calls)-1]); err != nil {
			return semantic.ExecutionEvidence{}, err
		}
		wire, err := io.ReadAll(rec.Result().Body)
		if err != nil || len(strings.TrimSpace(string(wire))) == 0 {
			return semantic.ExecutionEvidence{}, fmt.Errorf("frontend TCK: %s produced empty wire response", scenario.ID)
		}
		if rec.Code >= 400 {
			return semantic.ExecutionEvidence{}, fmt.Errorf("frontend TCK: valid scenario %s returned HTTP %d", scenario.ID, rec.Code)
		}
		if scenario.Transport == semantic.TransportStreaming && !bytes.Contains(wire, []byte("data:")) {
			return semantic.ExecutionEvidence{}, fmt.Errorf("frontend TCK: streaming scenario %s did not emit SSE wire output", scenario.ID)
		}
	}
	return semantic.ExecutionEvidence{
		ScenarioID: scenario.ID, Executed: true, BoundaryCalls: max(deltaCalls, 1),
		Accepted: positive, Rejected: !positive, CanonicalCall: positive,
		WireResponse: positive, ErrorMapped: !positive || rec.Code < 400,
		StreamValidated: positive && scenario.Transport == semantic.TransportStreaming,
	}, nil
}

func callHasPart(call *aipapi.Call, kind aipapi.PartKind) bool {
	for _, message := range call.Messages {
		for _, part := range message.Parts {
			if part.Kind == kind {
				return true
			}
		}
	}
	for _, item := range call.Items {
		for _, part := range item.Content {
			if part.Kind == aipapi.ContentPartKind(kind) {
				return true
			}
		}
	}
	return false
}

func containsScenario(ids []semantic.ScenarioID, id semantic.ScenarioID) bool {
	return slices.Contains(ids, id)
}

func validateCanonicalCall(s semantic.ScenarioDescriptor, call *aipapi.Call) error {
	if call == nil {
		return fmt.Errorf("frontend TCK: %s captured nil canonical call", s.ID)
	}
	if err := call.Validate(); err != nil {
		return fmt.Errorf("frontend TCK: %s captured invalid canonical call: %w", s.ID, err)
	}
	switch s.Feature {
	case semantic.FeatureTools:
		if len(call.Tools) == 0 {
			return fmt.Errorf("frontend TCK: %s captured no tools", s.ID)
		}
	case semantic.FeatureVision:
		if !callHasPart(call, aipapi.PartImageRef) {
			return fmt.Errorf("frontend TCK: %s captured no image part", s.ID)
		}
	case semantic.FeatureDocuments:
		if !callHasPart(call, aipapi.PartFileRef) {
			return fmt.Errorf("frontend TCK: %s captured no document part", s.ID)
		}
	case semantic.FeatureStructuredOutput:
		if call.Options.ResponseMIMEType == "" {
			return fmt.Errorf("frontend TCK: %s captured no structured-output requirement", s.ID)
		}
	case semantic.FeatureReasoning:
		if call.Options.ReasoningEffort == "" {
			return fmt.Errorf("frontend TCK: %s captured no reasoning requirement", s.ID)
		}
	case semantic.FeatureOrderedItems:
		if len(call.Items) == 0 {
			return fmt.Errorf("frontend TCK: %s captured no ordered items", s.ID)
		}
	case semantic.FeatureCompaction:
		if call.Invocation.Operation != aipapi.OperationContextCompaction {
			return fmt.Errorf("frontend TCK: %s did not preserve compaction operation", s.ID)
		}
	case semantic.FeatureExtensions:
		hasExt := len(call.Extensions) > 0
		if !hasExt {
			for _, item := range call.Items {
				if item.Kind == aipapi.ItemKindExtension {
					hasExt = true
					break
				}
				for _, part := range item.Content {
					if part.Kind == aipapi.ContentPartExtension {
						hasExt = true
						break
					}
				}
			}
		}
		if !hasExt {
			return fmt.Errorf("frontend TCK: %s captured no extension payload", s.ID)
		}
	}
	if s.Feature == semantic.FeatureStreaming && s.Transport == semantic.TransportStreaming && call.Invocation.DeliveryMode != aipapi.DeliveryModeStreaming {
		return fmt.Errorf("frontend TCK: %s did not preserve streaming delivery mode", s.ID)
	}
	return nil
}

// FrontendView is a test interface for driving a frontend wire handler.
type FrontendView interface {
	ServeHTTP(w http.ResponseWriter, req *http.Request)
	Subject() semantic.SubjectDescriptor
	Probe(ctx context.Context, scenario semantic.ScenarioDescriptor, executor *CapturingExecutor) (semantic.ExecutionEvidence, error)
}

// FrontendHarness provides the test harness interface for frontend certification.
type FrontendHarness interface {
	Subject() semantic.SubjectDescriptor
	Frontend(ctx context.Context) (FrontendView, error)
	Executor() *CapturingExecutor
	Reset() error
}

// CertifyFrontend selects and records scenarios for one frontend harness.
func CertifyFrontend(ctx context.Context, h FrontendHarness) (semantic.Certification, error) {
	if h == nil {
		return semantic.Certification{}, fmt.Errorf("frontend TCK: nil harness")
	}
	subject := h.Subject()
	if subject.Kind != semantic.KindFrontend {
		return semantic.Certification{}, fmt.Errorf("frontend TCK: subject %q must use frontend kind, got %q", subject.ID, subject.Kind)
	}
	executor := h.Executor()
	if executor == nil {
		return semantic.Certification{}, fmt.Errorf("frontend TCK: harness has no executor boundary")
	}
	if err := h.Reset(); err != nil {
		return semantic.Certification{}, fmt.Errorf("frontend TCK: reset: %w", err)
	}
	view, err := h.Frontend(ctx)
	if err != nil || view == nil {
		if err == nil {
			err = fmt.Errorf("frontend construction returned nil")
		}
		return semantic.Certification{}, fmt.Errorf("frontend TCK: construct: %w", err)
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
		evidence, err := view.Probe(ctx, scenario, executor)
		if err != nil {
			return semantic.Certification{}, fmt.Errorf("frontend TCK: scenario %s: %w", id, err)
		}
		isPositive := containsScenario(positive, id)
		if !evidence.Executed || evidence.ScenarioID != id || evidence.BoundaryCalls == 0 {
			return semantic.Certification{}, fmt.Errorf("frontend TCK: scenario %s lacks captured boundary execution evidence", id)
		}
		if isPositive && scenario.IsCancellation() {
			if !evidence.Accepted || !evidence.CanonicalCall || !evidence.Cancelled {
				return semantic.Certification{}, fmt.Errorf("frontend TCK: cancellation scenario lacks in-flight cancellation evidence")
			}
		} else if isPositive && (!evidence.Accepted || !evidence.CanonicalCall || !evidence.WireResponse) {
			return semantic.Certification{}, fmt.Errorf("frontend TCK: positive scenario %s lacks canonical/wire acceptance evidence", id)
		}
		if !isPositive && (!evidence.Rejected || evidence.Accepted || evidence.WireResponse) {
			return semantic.Certification{}, fmt.Errorf("frontend TCK: negative scenario %s lacks actual hard-negative rejection evidence", id)
		}
	}
	return semantic.Certification{
		SubjectID: subject.ID, SubjectKind: subject.Kind, Profile: subject.Profile,
		Capabilities: subject.Capabilities, Dialects: subject.Dialects, Transports: subject.Transports,
		Passed: positive, Negative: negative, Executed: true, ExecutedScenarioIDs: selected,
		HarnessCalls: 1 + len(selected),
	}, nil
}
