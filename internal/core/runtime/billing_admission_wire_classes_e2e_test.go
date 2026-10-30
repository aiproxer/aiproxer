package runtime_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/billingport"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/infra/billingbinding"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/execerr"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkbilling "github.com/aiproxer/aiproxer/pkg/aipsdk/billing"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// This D1 regression drives a real request through the real executor with the
// real public binding adapter whose atomic admission denies, and classifies the
// returned executor error with the real frontend classifier. It pins the wire
// contract: the three billing-port denial sentinels reach 429/429/503 (with the
// sentinel identity intact through the adapter wrap), SDK validation classes
// and unclassified admission failures stay terminal 500, and no case opens a
// provider.

const wireClassesStore = "store-wire-classes"

type wireClassesQuoter struct{}

func (*wireClassesQuoter) Quote(_ context.Context, in economics.QuoteInput) (economics.ExposureQuote, error) {
	return economics.ExposureQuote{
		ID:           "quote-wire-classes",
		Version:      1,
		Subject:      in.Subject.Clone(),
		Maximum:      economics.Money{NanoUnits: 7, Currency: "USD", Present: true},
		Tariff:       in.Tariff,
		Policy:       in.Policy,
		Completeness: economics.CompletenessComplete,
	}, nil
}

type wireClassesAdmitter struct {
	err error
}

func (a *wireClassesAdmitter) Admit(_ context.Context, in sdkbilling.ExposureAdmissionInput) (sdkbilling.ExposureHandle, error) {
	if err := in.Validate(); err != nil {
		return sdkbilling.ExposureHandle{}, err
	}
	if a.err != nil {
		return sdkbilling.ExposureHandle{}, a.err
	}
	return sdkbilling.ExposureHandle{
		StoreID: in.Subject.StoreID, BillingCallID: in.Subject.BillingCallID,
		ExposureID: "exp-wire-classes", QuoteID: in.Quote.ID,
	}, nil
}

// wireClassesCounters records the binding side effects a refused admission
// must never reach.
type wireClassesCounters struct {
	quotes atomic.Int32
	admits atomic.Int32
}

type wireClassesCountingQuoter struct{ counters *wireClassesCounters }

func (q *wireClassesCountingQuoter) Quote(ctx context.Context, in economics.QuoteInput) (economics.ExposureQuote, error) {
	q.counters.quotes.Add(1)
	return (&wireClassesQuoter{}).Quote(ctx, in)
}

type wireClassesCountingAdmitter struct{ counters *wireClassesCounters }

func (a *wireClassesCountingAdmitter) Admit(ctx context.Context, in sdkbilling.ExposureAdmissionInput) (sdkbilling.ExposureHandle, error) {
	a.counters.admits.Add(1)
	return (&wireClassesAdmitter{}).Admit(ctx, in)
}

type wireClassesScreen struct{}

func (wireClassesScreen) Check(_ context.Context, in sdkbilling.CreditScreenInput) (sdkbilling.CreditScreenResult, error) {
	if err := in.Validate(); err != nil {
		return sdkbilling.CreditScreenResult{}, err
	}
	return sdkbilling.CreditScreenResult{Decision: sdkbilling.CreditAllow, StoreID: in.StoreID, AccountID: in.AccountID}, nil
}

// newWireClassesExecutor builds the real executor around one real public
// binding adapter. It returns the adapter so a test can override the identity
// bundle the executor resolves, and the counted provider opens.
func newWireClassesExecutor(t *testing.T, binding sdkbilling.Binding) (*runtime.Executor, *billingbinding.Adapter, *atomic.Int32) {
	t.Helper()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := billingbinding.NewAdapter(binding)
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	var opens atomic.Int32
	executor := runtime.NewExecutor(runtime.ExecutorConfig{
		Core: runtime.CoreRuntime{
			Store: store,
			Backends: map[string]execbackend.Backend{
				"backend": {
					Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
					Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
						opens.Add(1)
						return aipapi.NewFixedEventStream([]aipapi.Event{
							{Kind: aipapi.EventResponseStarted},
							{Kind: aipapi.EventMessageStarted},
							{Kind: aipapi.EventResponseFinished},
						}), nil
					},
				},
			},
			Rand: routing.NewSeededRng(17),
		},
		Billing: runtime.BillingRuntime{
			BillingCreditGate:        adapter,
			BillingExposureAdmission: adapter,
			TerminalUsageSink:        adapter,
			BillingIdentity:          adapter.Identity(),
		},
		Extension: runtime.ExtensionRuntime{Bus: hooks.New(hooks.Config{})},
	})
	return executor, adapter, &opens
}

func wireClassesBinding(quoter economics.Quoter, admission sdkbilling.ExposureAdmitter) sdkbilling.Binding {
	return sdkbilling.Binding{
		ID:      "binding-wire-classes",
		Version: sdkbilling.BindingVersionV1,
		Scope: sdkbilling.BindingScope{
			StoreID: wireClassesStore,
			Tariff:  economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "tariff-wire-classes", Version: "v1"}},
			Policy:  economics.PolicySnapshotRef{VersionRef: economics.VersionRef{ID: "policy-wire-classes", Version: "v1"}, PolicyID: "policy-wire-classes-v1"},
		},
		CreditScreen: wireClassesScreen{},
		Quoter:       quoter,
		Admission:    admission,
		Terminal:     &factsE2ESink{},
	}
}

func wireClassesCall(id string) *aipapi.Call {
	maxOut := 2048
	return &aipapi.Call{
		ID:       id,
		Route:    aipapi.RouteIntent{Selector: "backend:model"},
		Options:  aipapi.GenerationOptions{MaxOutputTokens: &maxOut},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
	}
}

// wireClassesExecute runs one real executor request against a binding whose
// admission denies with admitErr and returns the counted provider opens plus the
// executor error.
func wireClassesExecute(t *testing.T, admitErr error) (int32, error) {
	t.Helper()
	executor, _, opens := newWireClassesExecutor(t, wireClassesBinding(&wireClassesQuoter{}, &wireClassesAdmitter{err: admitErr}))
	ctx := scope.WithScope(context.Background(), factsE2EScope())
	stream, err := executor.Execute(ctx, wireClassesCall("call-wire-classes"))
	if err == nil {
		drainFactsE2E(t, stream)
		_ = stream.Close()
		t.Fatal("denied admission must fail the request before any provider open")
	}
	if stream != nil {
		_ = stream.Close()
		t.Fatal("Execute returned a stream; admission must deny before upstream")
	}
	return opens.Load(), err
}

// TestBillingAdmissionTrustedStoreMismatchRefusesBeforeAdmissionSideEffects
// pins the OSS carriage of the frozen trusted store identity: the executor
// resolves the trusted store once and carries it into the admission input, so
// the real binding adapter refuses a request whose trusted store differs from
// the binding's authoritative store with the typed sdkbilling.ErrStoreMismatch
// before the quote, the admission port, the provider open and any durable
// financial side effect. A dropped StoreID carriage would let the foreign
// store reach the quote and admit against the binding store instead.
func TestBillingAdmissionTrustedStoreMismatchRefusesBeforeAdmissionSideEffects(t *testing.T) {
	t.Parallel()
	counters := &wireClassesCounters{}
	binding := wireClassesBinding(&wireClassesCountingQuoter{counters: counters}, &wireClassesCountingAdmitter{counters: counters})
	executor, adapter, opens := newWireClassesExecutor(t, binding)
	identity := adapter.Identity()
	identity.StoreID = func(context.Context) string { return "store-foreign" }
	executor.BillingIdentity = identity

	ctx := scope.WithScope(context.Background(), factsE2EScope())
	stream, err := executor.Execute(ctx, wireClassesCall("call-wire-store-mismatch"))
	if stream != nil {
		_ = stream.Close()
		t.Fatal("a foreign trusted store must refuse before returning a stream")
	}
	if !errors.Is(err, runtime.ErrBillingAdmissionDenied) {
		t.Fatalf("err=%v, want runtime.ErrBillingAdmissionDenied in the chain", err)
	}
	if !errors.Is(err, sdkbilling.ErrStoreMismatch) {
		t.Fatalf("err=%v, want the typed sdkbilling.ErrStoreMismatch retained through the adapter wrap", err)
	}
	if got := opens.Load(); got != 0 {
		t.Fatalf("provider opens = %d, want 0 (a foreign store is refused before upstream)", got)
	}
	if got := counters.quotes.Load(); got != 0 {
		t.Fatalf("binding quotes = %d, want 0 (a foreign store is refused before the quote)", got)
	}
	if got := counters.admits.Load(); got != 0 {
		t.Fatalf("binding admissions = %d, want 0 (a foreign store is refused before the admission port)", got)
	}
	outcome := execerr.ClassifyExecute(err)
	if outcome.Status != 500 || outcome.Kind != execerr.KindInternalError || outcome.Message != execerr.InternalWireMessage {
		t.Fatalf("classification = %d/%v/%q, want the unchanged 500 internal fallthrough", outcome.Status, outcome.Kind, outcome.Message)
	}
}

func TestBillingAdmissionWireStatusesThroughRealExecutor(t *testing.T) {
	t.Parallel()
	// Out-of-scope classes stay at the legacy 500 fallthrough: the D1 repair
	// restores the three sentinel classes and must not widen the cheap-screen
	// deny/unavailable outcomes (their user-visible 500 behavior is pre-existing
	// and unchanged in both compositions).
	for _, outOfScope := range []error{billingport.ErrCreditScreenDenied, billingport.ErrCreditScreenUnavailable} {
		outcome := execerr.ClassifyExecute(fmt.Errorf("%w: %w", runtime.ErrBillingAdmissionDenied, outOfScope))
		if outcome.Status != 500 || outcome.Kind != execerr.KindInternalError {
			t.Fatalf("out-of-scope denial %v classified %d/%v, want the legacy 500 internal fallthrough", outOfScope, outcome.Status, outcome.Kind)
		}
	}
	cases := []struct {
		name       string
		admitErr   error
		wantClass  error
		wantStatus int
		wantKind   execerr.Kind
	}{
		{
			name:       "account not ready",
			admitErr:   fmt.Errorf("binding store: %w", billingport.ErrAccountNotReady),
			wantClass:  billingport.ErrAccountNotReady,
			wantStatus: 429,
			wantKind:   execerr.KindBillingDenied,
		},
		{
			name:       "insufficient spendable",
			admitErr:   fmt.Errorf("binding settle: %w", billingport.ErrInsufficientSpendable),
			wantClass:  billingport.ErrInsufficientSpendable,
			wantStatus: 429,
			wantKind:   execerr.KindBillingDenied,
		},
		{
			name:       "billing store unavailable",
			admitErr:   billingport.ErrBillingStoreUnavailable,
			wantClass:  billingport.ErrBillingStoreUnavailable,
			wantStatus: 503,
			wantKind:   execerr.KindBillingUnavailable,
		},
		{
			name:       "sdk quote mismatch stays terminal",
			admitErr:   sdkbilling.ErrQuoteMismatch,
			wantClass:  sdkbilling.ErrQuoteMismatch,
			wantStatus: 500,
			wantKind:   execerr.KindInternalError,
		},
		{
			name:       "sdk admission mismatch stays terminal",
			admitErr:   sdkbilling.ErrAdmissionMismatch,
			wantClass:  sdkbilling.ErrAdmissionMismatch,
			wantStatus: 500,
			wantKind:   execerr.KindInternalError,
		},
		{
			name:       "sdk invalid admission stays terminal",
			admitErr:   sdkbilling.ErrInvalidAdmission,
			wantClass:  sdkbilling.ErrInvalidAdmission,
			wantStatus: 500,
			wantKind:   execerr.KindInternalError,
		},
		{
			name:       "unclassified admission failure stays terminal",
			admitErr:   errors.New("adapter blew up"),
			wantClass:  nil,
			wantStatus: 500,
			wantKind:   execerr.KindInternalError,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			opens, err := wireClassesExecute(t, tc.admitErr)
			if opens != 0 {
				t.Fatalf("provider opens = %d, want 0 (denial is pre-open and terminal)", opens)
			}
			if !errors.Is(err, runtime.ErrBillingAdmissionDenied) {
				t.Fatalf("err=%v, want runtime.ErrBillingAdmissionDenied in the chain", err)
			}
			if tc.wantClass != nil && !errors.Is(err, tc.wantClass) {
				t.Fatalf("err=%v, want class %v retained through executor + adapter wraps", err, tc.wantClass)
			}
			outcome := execerr.ClassifyExecute(err)
			if outcome.Status != tc.wantStatus || outcome.Kind != tc.wantKind {
				t.Fatalf("classification = %d/%v, want %d/%v (err=%v)", outcome.Status, outcome.Kind, tc.wantStatus, tc.wantKind, err)
			}
			if tc.wantStatus == 429 && outcome.Message != execerr.InsufficientCreditWireMessage {
				t.Fatalf("429 message = %q, want %q", outcome.Message, execerr.InsufficientCreditWireMessage)
			}
			if tc.wantStatus == 503 && outcome.Message != execerr.BillingUnavailableWireMessage {
				t.Fatalf("503 message = %q, want %q", outcome.Message, execerr.BillingUnavailableWireMessage)
			}
			if tc.wantStatus == 500 && outcome.Message != execerr.InternalWireMessage {
				t.Fatalf("500 message = %q, want %q", outcome.Message, execerr.InternalWireMessage)
			}
		})
	}
}
