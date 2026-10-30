package adapter_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/adapter"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

type effortStubSession struct {
	neg      backendplugin.Negotiation
	profile  backendplugin.ResolvedProfile
	err      error
	calls    int
	gotModel *string
}

func (s *effortStubSession) Resolve(ctx context.Context, modelID *string) (backendplugin.ResolvedProfile, error) {
	s.calls++
	s.gotModel = modelID
	if s.err != nil {
		return backendplugin.ResolvedProfile{}, s.err
	}
	return s.profile, nil
}

func (s *effortStubSession) ListModels(context.Context, uint32) (backendplugin.ListModelsResponse, error) {
	return backendplugin.ListModelsResponse{}, nil
}
func (s *effortStubSession) Execute(backendplugin.ExecuteStream) error { return nil }
func (s *effortStubSession) Close(context.Context) error               { return nil }
func (s *effortStubSession) Negotiation() backendplugin.Negotiation    { return s.neg }

func effortNeg() backendplugin.Negotiation {
	return backendplugin.Negotiation{
		Compatible: true, NegotiatedMinor: backendplugin.ProtocolMinorReasoningEffortValues,
		EnabledFeatures: []string{backendplugin.FeatureReasoningEffortValues},
	}
}

func effortCand(model string) routing.AttemptCandidate {
	return routing.AttemptCandidate{Primary: routing.Primary{Backend: "fake", Model: model}, Key: "fake:" + model}
}

func TestAdapterEffortSupportSupportedAndUnsupported(t *testing.T) {
	t.Parallel()
	sess := &effortStubSession{
		neg: effortNeg(),
		profile: backendplugin.ResolvedProfile{
			ReasoningEffortValues: &backendplugin.ReasoningEffortValuesV1{ModelID: "model-m", Supported: []string{"high"}, Exhaustive: true},
		},
	}
	br := adapter.Build(sess, backendplugin.ResolvedProfile{}, adapter.Options{InstanceID: "i1", Negotiation: effortNeg(), MetadataTimeout: time.Second})
	be := br.Backend
	if be.ResolveEffortSupport == nil {
		t.Fatalf("negotiated session must bind ResolveEffortSupport")
	}
	if got := be.ResolveEffortSupport(context.Background(), aipapi.Call{}, effortCand("model-m"), "high"); got != execbackend.EffortSupportSupported {
		t.Fatalf("high must be supported, got %d", got)
	}
	if got := be.ResolveEffortSupport(context.Background(), aipapi.Call{}, effortCand("model-m"), "none"); got != execbackend.EffortSupportUnsupported {
		t.Fatalf("none must be unsupported (exhaustive), got %d", got)
	}
	if sess.calls == 0 || sess.gotModel == nil || *sess.gotModel != "model-m" {
		t.Fatalf("must query the backend-facing model, calls=%d model=%v", sess.calls, sess.gotModel)
	}
}

func TestAdapterEffortSupportUnknownPaths(t *testing.T) {
	t.Parallel()
	// Unnegotiated session keeps nil callback.
	plain := &effortStubSession{neg: backendplugin.Negotiation{Compatible: true, NegotiatedMinor: 10}}
	br := adapter.Build(plain, backendplugin.ResolvedProfile{}, adapter.Options{InstanceID: "i1", Negotiation: plain.neg})
	if br.Backend.ResolveEffortSupport != nil {
		t.Fatalf("unnegotiated session must keep nil callback")
	}
	// Partial declaration omits value -> Unknown.
	partial := &effortStubSession{
		neg: effortNeg(),
		profile: backendplugin.ResolvedProfile{
			ReasoningEffortValues: &backendplugin.ReasoningEffortValuesV1{ModelID: "model-m", Supported: []string{"low"}},
		},
	}
	br = adapter.Build(partial, backendplugin.ResolvedProfile{}, adapter.Options{InstanceID: "i1", Negotiation: effortNeg(), MetadataTimeout: time.Second})
	if got := br.Backend.ResolveEffortSupport(context.Background(), aipapi.Call{}, effortCand("model-m"), "high"); got != execbackend.EffortSupportUnknown {
		t.Fatalf("partial omission must be unknown, got %d", got)
	}
	// Model mismatch never borrows another model's assertion.
	mismatch := &effortStubSession{
		neg: effortNeg(),
		profile: backendplugin.ResolvedProfile{
			ReasoningEffortValues: &backendplugin.ReasoningEffortValuesV1{ModelID: "other", Supported: []string{"high"}, Exhaustive: true},
		},
	}
	br = adapter.Build(mismatch, backendplugin.ResolvedProfile{}, adapter.Options{InstanceID: "i1", Negotiation: effortNeg(), MetadataTimeout: time.Second})
	if got := br.Backend.ResolveEffortSupport(context.Background(), aipapi.Call{}, effortCand("model-m"), "high"); got != execbackend.EffortSupportUnknown {
		t.Fatalf("mismatch must be unknown, got %d", got)
	}
	// RPC error, empty effort, empty model all yield Unknown.
	failing := &effortStubSession{neg: effortNeg(), err: errors.New("boom")}
	br = adapter.Build(failing, backendplugin.ResolvedProfile{}, adapter.Options{InstanceID: "i1", Negotiation: effortNeg(), MetadataTimeout: time.Second})
	if got := br.Backend.ResolveEffortSupport(context.Background(), aipapi.Call{}, effortCand("model-m"), "high"); got != execbackend.EffortSupportUnknown {
		t.Fatalf("RPC error must be unknown, got %d", got)
	}
	if got := br.Backend.ResolveEffortSupport(context.Background(), aipapi.Call{}, effortCand("model-m"), ""); got != execbackend.EffortSupportUnknown {
		t.Fatalf("empty effort must be unknown, got %d", got)
	}
	if got := br.Backend.ResolveEffortSupport(context.Background(), aipapi.Call{}, effortCand(""), "high"); got != execbackend.EffortSupportUnknown {
		t.Fatalf("empty model must be unknown, got %d", got)
	}
}

type blockingEffortSession struct {
	neg             backendplugin.Negotiation
	observedHasDL   bool
	observedTimeout time.Duration
}

func (s *blockingEffortSession) Resolve(ctx context.Context, _ *string) (backendplugin.ResolvedProfile, error) {
	dl, ok := ctx.Deadline()
	s.observedHasDL = ok
	if ok {
		s.observedTimeout = time.Until(dl)
	}
	<-ctx.Done()
	return backendplugin.ResolvedProfile{}, ctx.Err()
}

func (s *blockingEffortSession) ListModels(context.Context, uint32) (backendplugin.ListModelsResponse, error) {
	return backendplugin.ListModelsResponse{}, nil
}
func (s *blockingEffortSession) Execute(backendplugin.ExecuteStream) error { return nil }
func (s *blockingEffortSession) Close(context.Context) error               { return nil }
func (s *blockingEffortSession) Negotiation() backendplugin.Negotiation {
	return s.neg
}

// TestAdapterEffortSupportRespectsMetadataTimeout proves the metadata RPC is
// always bounded by MetadataTimeout even when the caller allows longer.
// A parent deadline longer than the metadata budget must not extend the
// pre-open guard, and a shorter parent deadline must still win.
func TestAdapterEffortSupportRespectsMetadataTimeout(t *testing.T) {
	t.Parallel()
	// Long parent, short metadata: the metadata budget must win and the
	// answer must stay Unknown instead of waiting out the parent.
	blocking := &blockingEffortSession{neg: effortNeg()}
	br := adapter.Build(blocking, backendplugin.ResolvedProfile{}, adapter.Options{InstanceID: "i1", Negotiation: effortNeg(), MetadataTimeout: 50 * time.Millisecond})
	parent, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	got := br.Backend.ResolveEffortSupport(parent, aipapi.Call{}, effortCand("model-m"), "high")
	elapsed := time.Since(start)
	if got != execbackend.EffortSupportUnknown {
		t.Fatalf("timeout must yield unknown, got %d", got)
	}
	if elapsed > time.Second {
		t.Fatalf("metadata timeout bypassed: elapsed=%v with 50ms budget and 5s parent", elapsed)
	}
	if !blocking.observedHasDL {
		t.Fatalf("metadata RPC must carry a deadline")
	}
	// The observed RPC deadline must reflect the 50ms metadata budget, not
	// the 5s parent deadline (generous upper bound avoids flake).
	if blocking.observedTimeout > time.Second {
		t.Fatalf("metadata RPC inherited the long parent deadline: timeout=%v", blocking.observedTimeout)
	}
	// Short parent, long metadata: the parent deadline must win.
	blocking2 := &blockingEffortSession{neg: effortNeg()}
	br2 := adapter.Build(blocking2, backendplugin.ResolvedProfile{}, adapter.Options{InstanceID: "i1", Negotiation: effortNeg(), MetadataTimeout: 5 * time.Second})
	shortParent, shortCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer shortCancel()
	start = time.Now()
	got = br2.Backend.ResolveEffortSupport(shortParent, aipapi.Call{}, effortCand("model-m"), "high")
	elapsed = time.Since(start)
	if got != execbackend.EffortSupportUnknown {
		t.Fatalf("cancelled parent must yield unknown, got %d", got)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("short parent deadline not preserved: elapsed=%v", elapsed)
	}
}
