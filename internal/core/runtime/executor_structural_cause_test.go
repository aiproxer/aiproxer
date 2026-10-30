package runtime_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
)

// structuralCauseTransform stands for a proxy-owned transformation that
// changed the request, such as a compaction rewrite. The core does not
// know the feature: it only carries the label the transformation recorded.
type structuralCauseTransform struct{}

func (structuralCauseTransform) ID() string                        { return "test-structural-cause" }
func (structuralCauseTransform) Order() int                        { return 900 }
func (structuralCauseTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (structuralCauseTransform) Handle(_ context.Context, call *aipapi.Call, _ request.RequestMeta, _ request.Services) error {
	call.StructuralCause = "compaction"
	return nil
}

// TestExecutor_structuralCauseReachesUsageEvent proves the plumbing: the
// structural cause a committed request carries reaches the usage sample of the
// leg that request produced, so an observer attributes the change to exactly
// the request that carried it.
func TestExecutor_structuralCauseReachesUsageEvent(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	obs := &captureUsage{}
	bus := hooks.New(hooks.Config{})
	planes := aipfeature.NewContributionSet()
	if err := aipfeature.Contribute(planes, aipfeature.PlaneRequestTransforms, "test-structural-cause", []request.Transform{structuralCauseTransform{}}); err != nil {
		t.Fatal(err)
	}
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{UsageObserver: obs, FeaturePlanes: planes.Freeze()})
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	var opened aipapi.Call
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opened = call
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventUsageDelta, InputTokens: 3, OutputTokens: 5, TotalTokens: 8},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = aipapi.Collect(context.Background(), stream)
	if len(obs.events) != 1 {
		t.Fatalf("usage events %d want 1", len(obs.events))
	}
	if got := obs.events[0].StructuralCause; got != "compaction" {
		t.Fatalf("structural cause = %q, want compaction", got)
	}
	if got := opened.StructuralCause; got != "compaction" {
		t.Fatalf("provider-bound structural cause = %q, want compaction", got)
	}
}

// undoStructuralChangeHook undoes the change the cause describes: it restores the
// original tool result and clears the label. A transformation that reverses a
// proxy-owned change must clear the cause that named it.
type undoStructuralChangeHook struct{ original string }

func (h undoStructuralChangeHook) ID() string                        { return "test-undo-structural-change" }
func (h undoStructuralChangeHook) Order() int                        { return 0 }
func (h undoStructuralChangeHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (h undoStructuralChangeHook) HandleRequestParts(_ context.Context, call *aipapi.Call, _ sdkhooks.PartMeta) error {
	call.StructuralCause = ""
	for i := range call.Messages {
		for j := range call.Messages[i].Parts {
			if call.Messages[i].Parts[j].Text == "[proxy omitted duplicate tool output]" {
				call.Messages[i].Parts[j].Text = h.original
			}
		}
	}
	return nil
}

// TestExecutor_structuralCauseClearedByLateMutation proves the cause is
// captured at the final backend-open boundary: a request-part hook that undoes
// the change the cause names clears the label, and neither the provider-bound
// request nor the usage sample carries the cause afterwards.
func TestExecutor_structuralCauseClearedByLateMutation(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	obs := &captureUsage{}
	bus := hooks.New(hooks.Config{RequestPartHooks: []sdkhooks.RequestPartHook{undoStructuralChangeHook{original: "original payload"}}})
	planes := aipfeature.NewContributionSet()
	if err := aipfeature.Contribute(planes, aipfeature.PlaneRequestTransforms, "test-structural-cause", []request.Transform{structuralCauseTransform{}}); err != nil {
		t.Fatal(err)
	}
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{UsageObserver: obs, FeaturePlanes: planes.Freeze()})
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	var opened aipapi.Call
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
			Open: func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opened = call
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventUsageDelta, InputTokens: 3, OutputTokens: 5, TotalTokens: 8},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "call_1", ToolName: "read", Text: "[proxy omitted duplicate tool output]"}}},
		},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = aipapi.Collect(context.Background(), stream)
	if opened.StructuralCause != "" {
		t.Fatalf("provider-bound structural cause = %q, want empty after the mutation", opened.StructuralCause)
	}
	if len(obs.events) != 1 {
		t.Fatalf("usage events %d want 1", len(obs.events))
	}
	if got := obs.events[0].StructuralCause; got != "" {
		t.Fatalf("usage structural cause = %q, want empty after the mutation", got)
	}
}

// TestExecutor_withoutStructuralCauseCarriesNone proves the label is empty
// when no transformation changed the request.
func TestExecutor_withoutStructuralCauseCarriesNone(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	obs := &captureUsage{}
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{UsageObserver: obs})
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventUsageDelta, InputTokens: 3, OutputTokens: 5, TotalTokens: 8},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = aipapi.Collect(context.Background(), stream)
	if len(obs.events) != 1 {
		t.Fatalf("usage events %d want 1", len(obs.events))
	}
	if got := obs.events[0].StructuralCause; got != "" {
		t.Fatalf("structural cause = %q, want empty", got)
	}
}

var _ usage.Observer = (*captureUsage)(nil)

// TestExecutor_structuralCausePublishedPerRequestExecution proves the
// publication claim belongs to the request execution: two request executions
// that each carried a structural change each publish their own cause, even
// when they share the backend and the model.
func TestExecutor_structuralCausePublishedPerRequestExecution(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	obs := &captureUsage{}
	bus := hooks.New(hooks.Config{})
	planes := aipfeature.NewContributionSet()
	if err := aipfeature.Contribute(planes, aipfeature.PlaneRequestTransforms, "test-structural-cause", []request.Transform{structuralCauseTransform{}}); err != nil {
		t.Fatal(err)
	}
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{UsageObserver: obs, FeaturePlanes: planes.Freeze()})
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventUsageDelta, InputTokens: 3, OutputTokens: 5, TotalTokens: 8},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)
	for i := range 2 {
		call := &aipapi.Call{
			Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
			Messages: []aipapi.Message{{
				Role:  aipapi.RoleUser,
				Parts: []aipapi.Part{aipapi.TextPart("hi")},
			}},
		}
		stream, err := ex.Execute(context.Background(), call)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = aipapi.Collect(context.Background(), stream)
		if len(obs.events) != i+1 {
			t.Fatalf("usage events %d want %d", len(obs.events), i+1)
		}
		if got := obs.events[i].StructuralCause; got != "compaction" {
			t.Fatalf("request %d structural cause = %q, want compaction", i, got)
		}
	}
}

// capturedSwitchEvidence is the content-free switch identity one usage sample
// published for its attempt.
type capturedSwitchEvidence struct {
	core            [32]byte
	cacheTarget     [32]byte
	openCredential  [32]byte
	providerAccount [32]byte
}

func switchExecutorTestKey() runtime.SeamDigestKey {
	var key runtime.SeamDigestKey
	for i := range key {
		key[i] = byte(i + 1)
	}
	return key
}

func executedSwitchIdentity(t *testing.T, ex *runtime.Executor, obs *captureUsage, call *aipapi.Call) capturedSwitchEvidence {
	t.Helper()
	before := len(obs.events)
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(obs.events) != before+1 {
		t.Fatalf("usage events %d, want one more than %d", len(obs.events), before)
	}
	ev := obs.events[before]
	return capturedSwitchEvidence{
		core:            ev.CacheIdentityDigest,
		cacheTarget:     ev.CacheTargetDigest,
		openCredential:  ev.CredentialIdentityDigest,
		providerAccount: ev.ProviderAccountDigest,
	}
}

func continuedStructuralCall(first *aipapi.Call, selector string, instructions []aipapi.Message) *aipapi.Call {
	return &aipapi.Call{
		Route:        aipapi.RouteIntent{Selector: selector},
		Instructions: instructions,
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("continue")},
		}},
		Session: aipapi.SessionRef{
			AuthoritativeSessionID: first.Session.AuthoritativeSessionID,
			ContinuityKey:          first.Session.ContinuityKey,
			ResumeToken:            first.Session.ResumeToken,
			ALegID:                 first.Session.ALegID,
		},
	}
}

func newSwitchAuthorityExecutor(t *testing.T, obs *captureUsage, open func(context.Context, aipapi.Call, routing.AttemptCandidate, func(execbackend.AttemptIdentity)) (aipapi.ManagedEventStream, error)) *runtime.Executor {
	t.Helper()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{UsageObserver: obs})
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.SystemPromptDigestEnabled = true
	ex.SystemPromptDigestKey = switchExecutorTestKey()
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return open(ctx, call, cand, func(identity execbackend.AttemptIdentity) {
					execbackend.ReportAttemptIdentity(ctx, identity)
				})
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)
	return ex
}

func fixedUsageStream() aipapi.ManagedEventStream {
	return aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventUsageDelta, InputTokens: 3, OutputTokens: 5, TotalTokens: 8},
		{Kind: aipapi.EventResponseFinished},
	})
}

func fixedUsageStreamWithAccount(account string) aipapi.ManagedEventStream {
	return aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{
			Kind:         aipapi.EventUsageDelta,
			InputTokens:  3,
			OutputTokens: 5,
			TotalTokens:  8,
			Accounting:   aipapi.UsageAccountingMetadata{ProviderAccountKey: account},
		},
		{Kind: aipapi.EventResponseFinished},
	})
}

func TestExecutor_SwitchIdentityReachesUsageEvent(t *testing.T) {
	t.Parallel()
	obs := &captureUsage{}
	ex := newSwitchAuthorityExecutor(t, obs, func(_ context.Context, _ aipapi.Call, _ routing.AttemptCandidate, report func(execbackend.AttemptIdentity)) (aipapi.ManagedEventStream, error) {
		report(execbackend.AttemptIdentity{Cache: "cache-a", Credential: "cred-a"})
		return fixedUsageStream(), nil
	})
	call := &aipapi.Call{
		Route:    aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	}
	got := executedSwitchIdentity(t, ex, obs, call)
	var none [32]byte
	if got.core == none || got.cacheTarget == none || got.openCredential == none {
		t.Fatalf("switch identity = %#v, want core, target and open credential", got)
	}
	if got.providerAccount != none {
		t.Fatal("an accounting sample without an account key must not publish one")
	}
}

func TestExecutor_NoAdapterIdentityStillCarriesAffinity(t *testing.T) {
	t.Parallel()
	obs := &captureUsage{}
	ex := newSwitchAuthorityExecutor(t, obs, func(_ context.Context, _ aipapi.Call, _ routing.AttemptCandidate, _ func(execbackend.AttemptIdentity)) (aipapi.ManagedEventStream, error) {
		return fixedUsageStream(), nil
	})
	call := &aipapi.Call{
		Route:    aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	}
	got := executedSwitchIdentity(t, ex, obs, call)
	var none [32]byte
	if got.core == none {
		t.Fatal("the core-visible provider identity must always be present")
	}
	if got.cacheTarget != none || got.openCredential != none || got.providerAccount != none {
		t.Fatalf("unreported adapter evidence = %#v, want zero components", got)
	}
}

func TestExecutor_GenerationParamsDoNotChangeSwitchIdentity(t *testing.T) {
	t.Parallel()
	obs := &captureUsage{}
	ex := newSwitchAuthorityExecutor(t, obs, func(_ context.Context, _ aipapi.Call, _ routing.AttemptCandidate, report func(execbackend.AttemptIdentity)) (aipapi.ManagedEventStream, error) {
		report(execbackend.AttemptIdentity{Cache: "cache-a", Credential: "cred-a"})
		return fixedUsageStreamWithAccount("account-a"), nil
	})
	first := &aipapi.Call{
		Route:    aipapi.RouteIntent{Selector: "openai:gpt-4?temperature=0.2&unused=one"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	}
	firstIdentity := executedSwitchIdentity(t, ex, obs, first)
	second := continuedStructuralCall(first, "openai:gpt-4?temperature=0.9&unused=two", nil)
	secondIdentity := executedSwitchIdentity(t, ex, obs, second)
	if firstIdentity != secondIdentity {
		t.Fatalf("generation options changed the switch identity: %#v vs %#v", firstIdentity, secondIdentity)
	}
}

func TestExecutor_CacheTargetChangesSwitchIdentity(t *testing.T) {
	t.Parallel()
	obs := &captureUsage{}
	target := "cache-a"
	ex := newSwitchAuthorityExecutor(t, obs, func(_ context.Context, _ aipapi.Call, _ routing.AttemptCandidate, report func(execbackend.AttemptIdentity)) (aipapi.ManagedEventStream, error) {
		report(execbackend.AttemptIdentity{Cache: target, Credential: "cred-a"})
		return fixedUsageStream(), nil
	})
	first := &aipapi.Call{
		Route:    aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	}
	firstIdentity := executedSwitchIdentity(t, ex, obs, first)
	target = "cache-b"
	second := continuedStructuralCall(first, "openai:gpt-4", nil)
	secondIdentity := executedSwitchIdentity(t, ex, obs, second)
	if firstIdentity.core != secondIdentity.core {
		t.Fatal("a cache-target change must not change the core provider identity")
	}
	if firstIdentity.cacheTarget == secondIdentity.cacheTarget {
		t.Fatal("a downstream cache-target change must change the target identity")
	}
	if firstIdentity.openCredential != secondIdentity.openCredential {
		t.Fatal("a cache-target change must not change the credential identity")
	}
}

func TestExecutor_CredentialChangesSwitchIdentity(t *testing.T) {
	t.Parallel()
	obs := &captureUsage{}
	credential := "cred-a"
	ex := newSwitchAuthorityExecutor(t, obs, func(_ context.Context, _ aipapi.Call, _ routing.AttemptCandidate, report func(execbackend.AttemptIdentity)) (aipapi.ManagedEventStream, error) {
		report(execbackend.AttemptIdentity{Cache: "cache-a", Credential: credential})
		return fixedUsageStream(), nil
	})
	first := &aipapi.Call{
		Route:    aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	}
	firstIdentity := executedSwitchIdentity(t, ex, obs, first)
	credential = "cred-b"
	second := continuedStructuralCall(first, "openai:gpt-4", nil)
	secondIdentity := executedSwitchIdentity(t, ex, obs, second)
	if firstIdentity.core != secondIdentity.core || firstIdentity.cacheTarget != secondIdentity.cacheTarget {
		t.Fatal("a credential change must not change the core or target identity")
	}
	if firstIdentity.openCredential == secondIdentity.openCredential {
		t.Fatal("a credential change must change the open credential identity")
	}
}

func TestExecutor_ProviderAccountChangesCredentialDigest(t *testing.T) {
	t.Parallel()
	obs := &captureUsage{}
	account := "account-a"
	ex := newSwitchAuthorityExecutor(t, obs, func(_ context.Context, _ aipapi.Call, _ routing.AttemptCandidate, _ func(execbackend.AttemptIdentity)) (aipapi.ManagedEventStream, error) {
		// No open-time credential: this is the connector-local path.
		return fixedUsageStreamWithAccount(account), nil
	})
	first := &aipapi.Call{
		Route:    aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	}
	firstIdentity := executedSwitchIdentity(t, ex, obs, first)
	account = "account-b"
	second := continuedStructuralCall(first, "openai:gpt-4", nil)
	secondIdentity := executedSwitchIdentity(t, ex, obs, second)
	var none [32]byte
	if firstIdentity.openCredential != none || secondIdentity.openCredential != none {
		t.Fatal("the fixture must not report an open credential")
	}
	if firstIdentity.providerAccount == none || secondIdentity.providerAccount == none {
		t.Fatal("accounting provider account identity must be published")
	}
	if firstIdentity.providerAccount == secondIdentity.providerAccount {
		t.Fatal("a provider-account change must change the account identity")
	}
	if firstIdentity.core != secondIdentity.core || firstIdentity.cacheTarget != secondIdentity.cacheTarget {
		t.Fatal("a provider-account change must not change core or target identity")
	}
}

// TestExecutor_DisabledSwitchIdentityFeedPublishesNothing proves the operator
// gate, not the key alone, arms the switch-identity feed: a process key that is
// present while the prompt-cache collapse diagnostic is disabled publishes no
// core, target, credential or provider-account identity. The assertion observes
// publication only; skipping the hashing itself is the gate's own property
// (ObservabilityRuntime.SystemPromptDigestEnabled), not something a usage
// observer can see.
func TestExecutor_DisabledSwitchIdentityFeedPublishesNothing(t *testing.T) {
	t.Parallel()
	obs := &captureUsage{}
	ex := newSwitchAuthorityExecutor(t, obs, func(_ context.Context, _ aipapi.Call, _ routing.AttemptCandidate, report func(execbackend.AttemptIdentity)) (aipapi.ManagedEventStream, error) {
		report(execbackend.AttemptIdentity{Cache: "cache-a", Credential: "cred-a"})
		return fixedUsageStreamWithAccount("account-a"), nil
	})
	// The fixture arms the feed; an operator-disabled diagnostic turns it off
	// while the process key stays configured.
	if ex.SystemPromptDigestKey.IsZero() {
		t.Fatal("fixture must keep the process key so the gate is the only suppressor")
	}
	newCall := func() *aipapi.Call {
		return &aipapi.Call{
			Route:    aipapi.RouteIntent{Selector: "openai:gpt-4"},
			Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
		}
	}
	// Positive control: the same fixture publishes every component while the
	// gate is armed, so the zero assertion below cannot pass vacuously.
	armed := executedSwitchIdentity(t, ex, obs, newCall())
	if armed.core == ([32]byte{}) || armed.cacheTarget == ([32]byte{}) || armed.openCredential == ([32]byte{}) || armed.providerAccount == ([32]byte{}) {
		t.Fatalf("armed feed switch identity: core=%x target=%x credential=%x account=%x, want every component published",
			armed.core, armed.cacheTarget, armed.openCredential, armed.providerAccount)
	}
	ex.SystemPromptDigestEnabled = false
	if got := executedSwitchIdentity(t, ex, obs, newCall()); got != (capturedSwitchEvidence{}) {
		t.Fatalf("disabled feed switch identity: core=%x target=%x credential=%x account=%x, want all zero",
			got.core, got.cacheTarget, got.openCredential, got.providerAccount)
	}
}

func TestExecutor_KeylessSwitchIdentityPublishesNothing(t *testing.T) {
	t.Parallel()
	obs := &captureUsage{}
	ex := newSwitchAuthorityExecutor(t, obs, func(_ context.Context, _ aipapi.Call, _ routing.AttemptCandidate, report func(execbackend.AttemptIdentity)) (aipapi.ManagedEventStream, error) {
		report(execbackend.AttemptIdentity{Cache: "cache-a", Credential: "cred-a"})
		return fixedUsageStreamWithAccount("account-a"), nil
	})
	// The feed is armed but keyless: no component may be published.
	ex.SystemPromptDigestEnabled = true
	ex.SystemPromptDigestKey = runtime.SeamDigestKey{}
	call := &aipapi.Call{
		Route:    aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	}
	got := executedSwitchIdentity(t, ex, obs, call)
	if got != (capturedSwitchEvidence{}) {
		t.Fatalf("keyless switch identity: core=%x target=%x credential=%x account=%x, want all zero",
			got.core, got.cacheTarget, got.openCredential, got.providerAccount)
	}
}
