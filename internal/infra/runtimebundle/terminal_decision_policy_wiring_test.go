package runtimebundle_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/sessionpolicy"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

func TestTerminalDecisionPolicyStoreIsProcessOwnedAcrossGenerationReload(t *testing.T) {
	t.Parallel()
	fixture := newTerminalDecisionFeatureFixture(t)
	store := fixture.process.StandardFeatures.TerminalDecisionPolicy()
	if store == nil {
		t.Fatal("process services standard features did not construct terminal decision policy store")
	}
	key := sessionpolicy.Key{
		SecureSessionIncarnation: "session-policy-lifecycle",
		ALegID:                   "a-leg-policy-lifecycle",
		FeatureID:                "terminal-decision",
	}
	authority := sessionpolicy.Authority{
		SecureSessionIncarnation: key.SecureSessionIncarnation,
		ALegID:                   key.ALegID,
		Authorized:               true,
	}
	setSnapshot, err := store.Set(context.Background(), authority, key, sessionpolicy.ActorClient, sessionpolicy.TriStateDisabled)
	if err != nil {
		t.Fatalf("Set: %v", err)
	}

	first := compileTerminalDecisionGeneration(t, fixture.process, terminalDecisionCandidate(t, fixture.firstFactory, true))
	if err := first.Close(); err != nil {
		t.Fatalf("close first generation: %v", err)
	}
	second := compileTerminalDecisionGeneration(t, fixture.process, terminalDecisionCandidate(t, "", false))
	if err := second.Close(); err != nil {
		t.Fatalf("close disabled generation: %v", err)
	}
	reenabled := compileTerminalDecisionGeneration(t, fixture.process, terminalDecisionCandidate(t, fixture.firstFactory, true))
	if err := reenabled.Close(); err != nil {
		t.Fatalf("close re-enabled generation: %v", err)
	}

	got, err := store.Snapshot(context.Background(), authority, key, true)
	if err != nil {
		t.Fatalf("Snapshot after reload/disable: %v", err)
	}
	if got.Revision != setSnapshot.Revision || got.EffectiveEnabled {
		t.Fatalf("policy store was not retained across generations: got=%+v set=%+v", got, setSnapshot)
	}

	if err := fixture.process.Close(); err != nil {
		t.Fatalf("close process: %v", err)
	}
	if err := fixture.process.Close(); err != nil {
		t.Fatalf("idempotent process close: %v", err)
	}
	if _, err := store.Set(context.Background(), authority, key, sessionpolicy.ActorClient, sessionpolicy.TriStateEnabled); !errors.Is(err, sessionpolicy.ErrClosed) {
		t.Fatalf("Set after process close error=%v want ErrClosed", err)
	}
}

func TestTerminalDecisionAdmissionSuppliesFrozenCompleteProviderInput(t *testing.T) {
	t.Parallel()
	fixture := newTerminalDecisionFeatureFixture(t)
	fixture.first.inputs = make(chan terminaldecision.Input, 4)
	m := runtimehost.NewManager(4, nil)
	bundle := compileTerminalDecisionGeneration(t, fixture.process, terminalDecisionCandidate(t, fixture.firstFactory, true))
	publishTerminalDecisionBundle(t, m, "terminal-policy-input", bundle)
	lease, ok := m.Acquire()
	if !ok {
		t.Fatal("Acquire generation")
	}
	postResponses(t, lease.Handler(), "stub-default")

	var in terminaldecision.Input
	select {
	case in = <-fixture.first.inputs:
	default:
		t.Fatal("terminal decision provider was not called")
	}
	if err := in.Validate(); err != nil {
		t.Fatalf("provider input is incomplete: %v; input=%+v", err, in)
	}
	if in.Policy.Revision == "" || in.Request.RequestID == "" || in.Candidate.Reference == "" || !in.Deadline.After(time.Now()) {
		t.Fatalf("provider input lacks immutable admission facts: %+v", in)
	}

	lease.Release()
	if err := m.ShutdownDetached(context.Background()); err != nil {
		t.Fatalf("ShutdownDetached: %v", err)
	}
}
