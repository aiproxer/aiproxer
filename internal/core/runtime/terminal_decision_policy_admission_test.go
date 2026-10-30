package runtime

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

func TestSnapshotTerminalDecisionPolicyUsesGenerationDefaultWithoutSecureScope(t *testing.T) {
	provider := admissionProviderStub{}
	executor := admissionExecutor(provider)

	policy, enabled, err := executor.snapshotTerminalDecisionPolicy(context.Background(), &identityBoundTurn{})
	if err != nil {
		t.Fatalf("snapshot policy: %v", err)
	}
	if !enabled {
		t.Fatal("provider must remain enabled when no secure-session scope is available")
	}
	if policy.Revision != "0" {
		t.Fatalf("generation-default policy revision = %q, want 0", policy.Revision)
	}
}

func TestSnapshotTerminalDecisionPolicyUsesSecureScopeOverride(t *testing.T) {
	provider := admissionProviderStub{}
	turn := secureAdmissionTurn()
	executor := admissionExecutor(provider)
	executor.TerminalPolicyReader = &fakeTerminalPolicyReader{
		snapshot: TerminalPolicySnapshot{
			EffectiveEnabled: false,
			Revision:         42,
		},
	}

	policy, enabled, err := executor.snapshotTerminalDecisionPolicy(context.Background(), turn)
	if err != nil {
		t.Fatalf("snapshot policy: %v", err)
	}
	if enabled {
		t.Fatal("secure-session disable override must disable the provider")
	}
	if policy.Revision != "42" {
		t.Fatalf("expected revision 42, got %q", policy.Revision)
	}
}

func TestSnapshotTerminalDecisionPolicyWithoutProviderIsDisabled(t *testing.T) {
	executor := admissionExecutor(nil)

	_, enabled, err := executor.snapshotTerminalDecisionPolicy(context.Background(), &identityBoundTurn{})
	if err != nil {
		t.Fatalf("snapshot policy: %v", err)
	}
	if enabled {
		t.Fatal("nil provider must disable terminal-decision evaluation")
	}
}

type admissionProviderStub struct{}

func (admissionProviderStub) ID() string { return "admission-test-provider" }

func (admissionProviderStub) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop}, nil
}

func admissionExecutor(provider terminaldecision.Provider) *Executor {
	return &Executor{
		ExtensionRuntime: ExtensionRuntime{
			RuntimeSnapshot: extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
				FeaturePlanes: freezeBundle(testFeatureBundle{
					TerminalDecisionProvider: provider,
				}),
			}),
		},
	}
}

func secureAdmissionTurn() *identityBoundTurn {
	return &identityBoundTurn{
		aLeg: b2bua.ALegRecord{ALegID: "a-leg-admission"},
		secureTurn: execctx.SecureSessionTurn{
			SessionID: domain.SessionID("session-admission"),
			TurnID:    domain.TurnID("turn-admission"),
		},
		secureTurnOK: true,
	}
}

type fakeTerminalPolicyReader struct {
	lastQuery TerminalPolicyQuery
	snapshot  TerminalPolicySnapshot
	err       error
}

func (f *fakeTerminalPolicyReader) Effective(_ context.Context, in TerminalPolicyQuery) (TerminalPolicySnapshot, error) {
	f.lastQuery = in
	return f.snapshot, f.err
}

func TestSnapshotTerminalDecisionPolicy_UsesTerminalPolicyReaderSeam(t *testing.T) {
	provider := admissionProviderStub{}
	reader := &fakeTerminalPolicyReader{
		snapshot: TerminalPolicySnapshot{
			EffectiveEnabled: false,
			Revision:         42,
		},
	}
	turn := secureAdmissionTurn()
	executor := admissionExecutor(provider)
	executor.TerminalPolicyReader = reader

	policy, enabled, err := executor.snapshotTerminalDecisionPolicy(context.Background(), turn)
	if err != nil {
		t.Fatalf("snapshot policy: %v", err)
	}
	if enabled {
		t.Fatal("reader returning disabled must disable terminal-decision")
	}
	if policy.Revision != "42" {
		t.Fatalf("policy revision = %q, want 42", policy.Revision)
	}
	if reader.lastQuery.SecureSessionIncarnation != "session-admission" {
		t.Fatalf("reader query session incarnation = %q, want session-admission", reader.lastQuery.SecureSessionIncarnation)
	}
	if reader.lastQuery.ALegID != "a-leg-admission" {
		t.Fatalf("reader query ALegID = %q, want a-leg-admission", reader.lastQuery.ALegID)
	}
	if reader.lastQuery.FeatureID != terminalDecisionFeatureID {
		t.Fatalf("reader query FeatureID = %q, want %q", reader.lastQuery.FeatureID, terminalDecisionFeatureID)
	}
	if !reader.lastQuery.GenerationDefault {
		t.Fatal("reader query GenerationDefault must be true when provider is present")
	}
}
