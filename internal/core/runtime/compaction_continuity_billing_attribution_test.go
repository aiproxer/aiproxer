package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/billingport"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

type compactionContinuityBillingCapture struct {
	mu    sync.Mutex
	calls []usageevidence.CallUsageRecord
	legs  []usageevidence.CallLegUsageRecord
	opens []compactionContinuityOpen
}

type compactionContinuityOpen struct {
	call      aipapi.Call
	auxiliary bool
	scope     scope.PrincipalScopeView
}

func (c *compactionContinuityBillingCapture) appendCall(_ context.Context, record usageevidence.CallUsageRecord) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, record)
	return nil
}

func (c *compactionContinuityBillingCapture) appendLeg(_ context.Context, record usageevidence.CallLegUsageRecord) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.legs = append(c.legs, record)
	return nil
}

func (c *compactionContinuityBillingCapture) snapshot() (calls []usageevidence.CallUsageRecord, legs []usageevidence.CallLegUsageRecord, opens []compactionContinuityOpen) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]usageevidence.CallUsageRecord(nil), c.calls...), append([]usageevidence.CallLegUsageRecord(nil), c.legs...), append([]compactionContinuityOpen(nil), c.opens...)
}

func TestCompactionContinuityBillingAttributesDetachedChildToOriginatingAccount(t *testing.T) {
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	capture := &compactionContinuityBillingCapture{}
	principalID := "principal-compaction"
	accountID := "acct:" + principalID

	ex := TestExecutor()
	ex.Store = store
	ex.Bus = hooks.New(hooks.Config{})
	ex.SecureSession = testSecureManager(t, memory.New(memory.Options{SimulateDurable: true}), store)
	ex.SyntheticLocalPrincipal = true
	ex.MaxAttempts = 2
	ex.Now = func() time.Time { return time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC) }
	ex.BillingIdentity = BillingIdentity{
		AccountID: func(ctx context.Context, _ aipapi.Call) string {
			got, ok := scope.ScopeFromContext(ctx)
			if !ok {
				return ""
			}
			return "acct:" + got.PrincipalID.String()
		},
		CustomerPricingRef: func(context.Context, aipapi.Call) usageevidence.VersionRef {
			return usageevidence.VersionRef{ID: "pricing:compaction", Version: "1"}
		},
		ChargePolicyRef: func(context.Context, aipapi.Call) usageevidence.VersionRef {
			return usageevidence.VersionRef{ID: "policy:compaction", Version: "1"}
		},
		OperatorRateRef: func(context.Context, string, string) usageevidence.VersionRef {
			return usageevidence.VersionRef{ID: "operator:compaction", Version: "1"}
		},
	}
	ex.BillingCreditGate = creditGateFunc(func(_ context.Context, got string) error {
		if got != accountID {
			return errors.New("unexpected account at credit screen: " + got)
		}
		return nil
	})
	ex.BillingExposureAdmission = exposureAdmissionFunc(func(_ context.Context, in BillingExposureAdmissionInput) (billingport.CallExposure, error) {
		if in.CallID == "" || in.ALegID == "" {
			return billingport.CallExposure{}, errors.New("missing exposure identity")
		}
		return billingport.CallExposure{
			AccountID:       accountID,
			CallID:          in.CallID,
			PricingRef:      usageevidence.VersionRef{ID: "pricing:compaction", Version: "1"},
			ChargePolicyRef: usageevidence.VersionRef{ID: "policy:compaction", Version: "1"},
			Status:          billingport.ExposureOpen,
		}, nil
	})
	ex.TerminalUsageSink = testTerminalSink{appendCall: capture.appendCall, appendLeg: capture.appendLeg}
	ex.Backends = map[string]execbackend.Backend{
		"backend": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(ctx context.Context, call aipapi.Call, candidate routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				gotScope, _ := scope.ScopeFromContext(ctx)
				isAux := gotScope.Origin == scope.OriginInternal
				capture.mu.Lock()
				capture.opens = append(capture.opens, compactionContinuityOpen{
					call:      aipapi.CloneCall(call),
					auxiliary: isAux,
					scope:     gotScope,
				})
				capture.mu.Unlock()

				// Force one pre-output auxiliary failure. The replacement B-leg must
				// retain its own positive sequence and terminal evidence.
				if isAux && candidate.Primary.Model == "extractor" {
					return nil, aipapi.RecoverablePreOutputError(errors.New("extractor route unavailable"))
				}
				input, output, total, cost, dedupe := 11, 5, 16, int64(101), "primary-provider-charge"
				if isAux {
					input, output, total, cost, dedupe = 3, 2, 5, 37, "aux-provider-charge"
				}
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventTextDelta, Delta: strings.Repeat("x", output)},
					{
						Kind: aipapi.EventUsageDelta, InputTokens: input, OutputTokens: output, TotalTokens: total,
						CostNanoUnits: cost, Currency: "USD", CostPresent: true,
						UsagePresence: aipapi.UsagePresence{InputTokens: true, OutputTokens: true, TotalTokens: true},
						Accounting: aipapi.UsageAccountingMetadata{
							Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported,
							Authority: aipapi.UsageAuthorityAuthoritative, DedupeKey: dedupe,
						},
					},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}

	ctx := scope.WithScope(context.Background(), scope.PrincipalScopeView{
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known(principalID),
		Origin:      scope.OriginClient,
	})
	primaryCall := &aipapi.Call{
		Session: aipapi.SessionRef{ClientSessionID: "client-compaction", ContinuityKey: "parent-branch"},
		Route:   aipapi.RouteIntent{Selector: "backend:primary"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{
			aipapi.TextPart("primary request"),
		}}},
	}
	primaryStream, err := ex.Execute(ctx, primaryCall)
	if err != nil {
		t.Fatal("primary Execute:", err)
	}
	primary, err := aipapi.Collect(ctx, primaryStream)
	if err != nil {
		t.Fatal("primary collect:", err)
	}

	childReq := auxiliary.Request{
		Call: &aipapi.Call{
			Route: aipapi.RouteIntent{Selector: "backend:extractor|backend:fallback"},
			Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{
				aipapi.TextPart("bounded continuity extraction input"),
			}}},
		},
		Role:          "compaction_continuity_extractor",
		Visibility:    "private",
		ParentTraceID: "primary-trace",
		ParentALegID:  primaryCall.Session.ALegID,
		SessionMode:   auxiliary.SessionModeDetached,
	}
	childStream, err := (&auxiliaryClientForExecutor{executor: ex}).Stream(ctx, childReq)
	if err != nil {
		t.Fatal("detached child Stream:", err)
	}
	child, err := aipapi.Collect(ctx, childStream)
	if err != nil {
		t.Fatal("child collect:", err)
	}

	if primary.InputTokens != 11 || primary.OutputTokens != 5 || primary.TotalTokens != 16 {
		t.Fatalf("primary protocol usage was changed by child: %+v", primary)
	}
	if child.InputTokens != 3 || child.OutputTokens != 2 || child.TotalTokens != 5 {
		t.Fatalf("child usage: %+v", child)
	}

	calls, legs, opens := capture.snapshot()
	if len(calls) != 2 {
		t.Fatalf("call closures = %d, want primary + auxiliary", len(calls))
	}
	if len(legs) < 3 {
		t.Fatalf("B-leg records = %d, want primary plus two child failover legs", len(legs))
	}
	childALeg := ""
	var primaryALeg string
	for _, open := range opens {
		if open.auxiliary {
			childALeg = open.call.Session.ALegID
			if open.scope.PrincipalID.String() != principalID || open.scope.Origin != scope.OriginInternal {
				t.Fatalf("auxiliary scope attribution: %+v", open.scope)
			}
			var lineage struct {
				Role          string `json:"role"`
				Visibility    string `json:"visibility"`
				ParentALegID  string `json:"parent_a_leg_id"`
				ParentTraceID string `json:"parent_trace_id"`
			}
			raw := open.call.Extensions["aip.aux.lineage.v1"]
			if err := json.Unmarshal(raw, &lineage); err != nil {
				t.Fatalf("auxiliary lineage: %v", err)
			}
			if lineage.Role != "compaction_continuity_extractor" || lineage.Visibility != "private" || lineage.ParentALegID != primaryCall.Session.ALegID || lineage.ParentTraceID != "primary-trace" {
				t.Fatalf("auxiliary workload lineage: %+v", lineage)
			}
		} else {
			primaryALeg = open.call.Session.ALegID
		}
	}
	if primaryALeg == "" || childALeg == "" || primaryALeg == childALeg {
		t.Fatalf("primary/child A-leg identity: primary=%q child=%q", primaryALeg, childALeg)
	}

	var primaryCallID, childCallID usageevidence.BillingCallID
	for _, call := range calls {
		if call.AccountID != accountID {
			t.Fatalf("call account = %q, want originating account %q", call.AccountID, accountID)
		}
		switch call.ALegID {
		case primaryALeg:
			primaryCallID = call.CallID
		case childALeg:
			childCallID = call.CallID
		default:
			t.Fatalf("unexpected call-closure A-leg %q", call.ALegID)
		}
	}
	if primaryCallID == "" || childCallID == "" || primaryCallID == childCallID {
		t.Fatalf("primary/child BillingCallID: primary=%q child=%q", primaryCallID, childCallID)
	}

	var primaryLegs, childLegs []usageevidence.CallLegUsageRecord
	for _, leg := range legs {
		switch leg.CallID {
		case primaryCallID:
			primaryLegs = append(primaryLegs, leg)
		case childCallID:
			childLegs = append(childLegs, leg)
		default:
			t.Fatalf("leg has unknown BillingCallID %q", leg.CallID)
		}
		if leg.AttemptSeq <= 0 {
			t.Fatalf("independently accounted leg has non-positive AttemptSeq: %+v", leg)
		}
		if leg.Evidence.Source == "" || leg.Evidence.Authority == "" || strings.TrimSpace(leg.Evidence.DedupeKey) == "" {
			t.Fatalf("independently accounted leg lost billing evidence: %+v", leg)
		}
	}
	if len(primaryLegs) != 1 || primaryLegs[0].AttemptSeq != 1 {
		t.Fatalf("primary B-leg sequence: %+v", primaryLegs)
	}
	sort.Slice(childLegs, func(i, j int) bool { return childLegs[i].AttemptSeq < childLegs[j].AttemptSeq })
	if len(childLegs) != 2 || childLegs[0].AttemptSeq != 1 || childLegs[1].AttemptSeq != 2 {
		t.Fatalf("auxiliary failover B-leg sequences: %+v", childLegs)
	}
	if childLegs[1].Evidence.OutputTokens.Value != 2 || !childLegs[1].Evidence.OutputTokens.Present || childLegs[1].Evidence.Cost.NanoUnits != 37 || !childLegs[1].Evidence.Cost.Present {
		t.Fatalf("auxiliary provider evidence: %+v", childLegs[1].Evidence)
	}
	if childLegs[1].Evidence.Source != usageevidence.EvidenceSourceProviderReported || childLegs[1].Evidence.Authority != usageevidence.EvidenceAuthorityAuthoritative || childLegs[1].Evidence.DedupeKey != "aux-provider-charge" {
		t.Fatalf("auxiliary provenance: %+v", childLegs[1].Evidence)
	}
	if childLegs[0].Evidence.Source != usageevidence.EvidenceSourceUnavailable || childLegs[0].Evidence.Authority != usageevidence.EvidenceAuthorityUnavailable {
		t.Fatalf("failed auxiliary leg must carry explicit unavailable evidence: %+v", childLegs[0].Evidence)
	}

	var aggregateOutput int64
	for _, leg := range legs {
		if leg.Evidence.OutputTokens.Present {
			aggregateOutput += leg.Evidence.OutputTokens.Value
		}
	}
	if aggregateOutput != int64(primary.OutputTokens+child.OutputTokens) {
		t.Fatalf("account/operator aggregate output=%d, want primary+auxiliary=%d", aggregateOutput, primary.OutputTokens+child.OutputTokens)
	}
}

// auxiliaryClientForExecutor keeps this test on the same detached execution
// adapter as the production feature path without composing a second runner.
type auxiliaryClientForExecutor struct{ executor *Executor }

func (c *auxiliaryClientForExecutor) Stream(ctx context.Context, req auxiliary.Request) (aipapi.EventStream, error) {
	if c == nil || c.executor == nil {
		return nil, auxiliary.ErrNotConfigured
	}
	return auxreq.NewClient(func() auxreq.ExecutorRunner { return c.executor }).Stream(ctx, req)
}

func TestCompactionContinuityBillingTerminalFallbackDedupeUsesStateCallID(t *testing.T) {
	var legs []usageevidence.CallLegUsageRecord
	ex := TestExecutor()
	ex.TerminalUsageSink = testTerminalSink{appendLeg: func(_ context.Context, leg usageevidence.CallLegUsageRecord) error {
		legs = append(legs, leg)
		return nil
	}}
	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	state := newBillingCallState(callID)
	ex.appendIndependentTerminalLeg(context.Background(), state, "aux-child-a", b2bua.BLegRecord{
		ALegID: "aux-child-a", BLegID: "aux-child-b", Seq: 1,
	}, routing.Primary{Backend: "backend", Model: "extractor"}, time.Unix(1, 0), time.Unix(2, 0), usageevidence.LegOutcomeNeverStarted)
	if len(legs) != 1 {
		t.Fatalf("terminal leg appends = %d, want 1", len(legs))
	}
	dedupeKey := legs[0].Evidence.DedupeKey
	if strings.Contains(dedupeKey, "unknown-call") || !strings.Contains(dedupeKey, callID.String()) {
		t.Fatalf("fallback DedupeKey = %q, want actual BillingCallID and no unknown-call", dedupeKey)
	}
}

func TestCompactionContinuityBillingRejectsNonPositiveAttemptSeq(t *testing.T) {
	var legs []usageevidence.CallLegUsageRecord
	ex := TestExecutor()
	ex.TerminalUsageSink = testTerminalSink{appendLeg: func(_ context.Context, leg usageevidence.CallLegUsageRecord) error {
		legs = append(legs, leg)
		return nil
	}}
	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	for _, seq := range []int{0, -1} {
		ex.appendIndependentCallLeg(context.Background(), callID, usageevidence.CallLegUsageRecord{
			CallID: callID, ALegID: "aux-child-a", BLegID: "aux-b-leg", AttemptSeq: seq,
			BackendID: "backend", ProviderID: "provider", ModelID: "extractor",
			StartedAt: time.Unix(1, 0), FinishedAt: time.Unix(2, 0), Outcome: usageevidence.LegOutcomeFailed,
			Surfaced: usageevidence.SurfacedNo,
		})
	}
	if len(legs) != 0 {
		t.Fatalf("non-positive AttemptSeq was silently appended: %#v", legs)
	}
}
