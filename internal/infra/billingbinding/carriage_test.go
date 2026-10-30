package billingbinding_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/billingport"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	runtimecore "github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/infra/billingbinding"
	sdkbilling "github.com/aiproxer/aiproxer/pkg/aipsdk/billing"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
)

// carriageCall is a well-formed BillingCallID: sealed record fixtures must
// satisfy the usage-evidence identity contract.
const (
	carriageCall = "bc_00112233445566778899aabbccddeeff"
	carriageBLeg = "bleg-1"
)

// carriageTerminal is the optional version-2 terminal sink: it declares
// RecordCarrier and records exactly the envelope the adapter sends.
type carriageTerminal struct {
	mu     sync.Mutex
	calls  int
	last   sdkbilling.TerminalEnvelope
	ackFor func(sdkbilling.TerminalEnvelope) (sdkbilling.TerminalAck, error)
}

func (c *carriageTerminal) CarriesUsageRecords() {}

func (c *carriageTerminal) AppendTerminal(_ context.Context, env sdkbilling.TerminalEnvelope) (sdkbilling.TerminalAck, error) {
	c.mu.Lock()
	c.calls++
	c.last = env.Clone()
	ackFn := c.ackFor
	c.mu.Unlock()
	if ackFn != nil {
		return ackFn(env)
	}
	return sdkbilling.TerminalAck{StoreID: env.StoreID, EnvelopeID: env.EnvelopeID, Revision: 1}, nil
}

func (c *carriageTerminal) envelope() sdkbilling.TerminalEnvelope {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}

func (c *carriageTerminal) callsValue() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// carriageQuoter declares RouteTariffCarrier and ReferenceFacts: it receives
// the ordered planned-route occurrence list and returns the quote the test
// scenario dictates.
type carriageQuoter struct {
	mu      sync.Mutex
	calls   int
	last    economics.QuoteInput
	respond func(economics.QuoteInput) (economics.ExposureQuote, error)
}

func (q *carriageQuoter) CarriesRouteTariffs() {}

func (q *carriageQuoter) CustomerPricingRef(context.Context) economics.VersionRef {
	return economics.VersionRef{ID: "customer-pricing", Version: "v7"}
}

func (q *carriageQuoter) ChargePolicyRef(context.Context) economics.VersionRef {
	return economics.VersionRef{ID: "charge-policy", Version: "v3"}
}

func (q *carriageQuoter) OperatorRateRef(_ context.Context, backendID, modelID string) economics.VersionRef {
	switch backendID + ":" + modelID {
	case "backend-1:model-1":
		return economics.VersionRef{ID: "operator-rate-1", Version: "v1"}
	case "backend-2:model-2":
		return economics.VersionRef{ID: "operator-rate-2", Version: "v2"}
	default:
		return economics.VersionRef{}
	}
}

func (q *carriageQuoter) Quote(_ context.Context, in economics.QuoteInput) (economics.ExposureQuote, error) {
	q.mu.Lock()
	q.calls++
	q.last = in.Clone()
	respond := q.respond
	q.mu.Unlock()
	if respond != nil {
		return respond(in)
	}
	return carriageQuote(in, economics.RouteTariffModeNone, nil, 0), nil
}

func (q *carriageQuoter) callsValue() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.calls
}

func (q *carriageQuoter) lastInput() economics.QuoteInput {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.last
}

// carriageQuote builds a valid quote that mirrors the bound fixture identity
// and carries the requested route-tariff state.
func carriageQuote(in economics.QuoteInput, mode economics.RouteTariffMode, bindings []economics.RouteTariffBinding, maximum int64) economics.ExposureQuote {
	return economics.ExposureQuote{
		ID:              "q-1",
		Version:         1,
		Subject:         in.Subject.Clone(),
		Tariff:          in.Tariff,
		Policy:          in.Policy,
		Maximum:         economics.Money{NanoUnits: maximum, Currency: "USD", Present: true},
		Completeness:    economics.CompletenessComplete,
		RouteTariffMode: mode,
		RouteTariffs:    append([]economics.RouteTariffBinding(nil), bindings...),
	}
}

// admitInputWithRoute returns the shared admission fixture with a parsed
// route plan attached.
func admitInputWithRoute(t *testing.T, selector string) runtimecore.BillingExposureAdmissionInput {
	t.Helper()
	in := admitInput()
	sel, err := routing.Parse(selector)
	if err != nil {
		t.Fatalf("parse selector %q: %v", selector, err)
	}
	in.Route = sel
	return in
}

// frozenBindingsFor builds one binding per distinct planned route key with a
// deterministic content hash, echoing the reference producer's distinctness.
func frozenBindingsFor(routes []economics.RouteCandidate) []economics.RouteTariffBinding {
	seen := map[string]struct{}{}
	var out []economics.RouteTariffBinding
	for _, route := range routes {
		key := economics.RouteTariffKey(route.Backend, route.Model)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		sum := sha256.Sum256([]byte("tariff:" + key))
		out = append(out, economics.RouteTariffBinding{
			RouteID: key, TariffID: "tariff-" + key, TariffVersion: "v1",
			ContentHash: hex.EncodeToString(sum[:]),
		})
	}
	return out
}

// boundParts assembles a binding with the supplied ports and the shared
// fixture identity. The caller keeps the port handles for assertions.
func boundParts(t *testing.T, quoter economics.Quoter, terminal sdkbilling.TerminalSink) (sdkbilling.Binding, *fakeScreen, *fakeAdmitter) {
	t.Helper()
	screen := &fakeScreen{result: sdkbilling.CreditScreenResult{Decision: sdkbilling.CreditAllow, StoreID: testStore, AccountID: "acct-1"}}
	admitter := &fakeAdmitter{handle: sdkbilling.ExposureHandle{StoreID: testStore, BillingCallID: testCall, ExposureID: "exp-1", QuoteID: "q-1"}}
	binding := sdkbilling.Binding{
		ID:           "binding-carriage",
		Version:      sdkbilling.BindingVersionV1,
		Scope:        bindingScope(),
		CreditScreen: screen,
		Quoter:       quoter,
		Admission:    admitter,
		Terminal:     terminal,
	}
	return binding, screen, admitter
}

func newCarriageAdapter(t *testing.T, quoter economics.Quoter, terminal sdkbilling.TerminalSink) (*billingbinding.Adapter, *fakeScreen, *fakeAdmitter) {
	t.Helper()
	binding, screen, admitter := boundParts(t, quoter, terminal)
	a, err := billingbinding.NewAdapter(binding)
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	return a, screen, admitter
}

// sealedLegRecord builds one valid sealed leg record whose facts the version-1
// projection drops (backend/provider/model, timings, surfaced state, operator
// ref) so carriage can be proven end to end.
func sealedLegRecord(t *testing.T) usageevidence.CallLegUsageRecord {
	t.Helper()
	now := time.Now().UTC()
	record := usageevidence.CallLegUsageRecord{
		CallID:     usageevidence.BillingCallID(carriageCall),
		ALegID:     "aleg-1",
		BLegID:     carriageBLeg,
		AttemptSeq: 3,
		BackendID:  "backend-1",
		ProviderID: "provider-1",
		ModelID:    "model-1",
		StartedAt:  now,
		FinishedAt: now.Add(time.Second),
		Outcome:    usageevidence.LegOutcomeWinner,
		Surfaced:   usageevidence.SurfacedYes,
		Evidence: usageevidence.FinalBillingEvidence{
			InputTokens: usageevidence.Quantity{Value: 10, Present: true},
			Source:      usageevidence.EvidenceSourceProviderReported,
			Authority:   usageevidence.EvidenceAuthorityAuthoritative,
			DedupeKey:   "dedupe-1",
		},
		OperatorRateRef: usageevidence.VersionRef{ID: "operator-rate-1", Version: "v1"},
	}
	sealed, err := record.Seal()
	if err != nil {
		t.Fatalf("seal leg fixture: %v", err)
	}
	return sealed
}

func sealedCallRecord(t *testing.T) usageevidence.CallUsageRecord {
	t.Helper()
	now := time.Now().UTC()
	record := usageevidence.CallUsageRecord{
		SchemaVersion:      usageevidence.CurrentRecordSchemaVersion,
		CallID:             usageevidence.BillingCallID(carriageCall),
		AccountID:          "acct-1",
		ALegID:             "aleg-1",
		SessionID:          "sess-1",
		StartedAt:          now,
		FinishedAt:         now.Add(time.Second),
		Outcome:            usageevidence.TurnOutcomeCompleted,
		ExpectedBLegIDs:    []string{carriageBLeg},
		CustomerPricingRef: usageevidence.VersionRef{ID: "customer-pricing", Version: "v7"},
		ChargePolicyRef:    usageevidence.VersionRef{ID: "charge-policy", Version: "v3"},
	}
	sealed, err := record.Seal()
	if err != nil {
		t.Fatalf("seal call fixture: %v", err)
	}
	return sealed
}

// TestAppendLegCarryingSinkReceivesExactSealedRecord proves version-2 carriage:
// the payload is byte-identical to the sealed record's canonical JSON, the
// payload hash covers those bytes, and the dropped-by-version-1 facts survive.
func TestAppendLegCarryingSinkReceivesExactSealedRecord(t *testing.T) {
	t.Parallel()
	terminal := &carriageTerminal{}
	a, _, _ := newCarriageAdapter(t, &carriageQuoter{}, terminal)
	record := sealedLegRecord(t)
	if err := a.AppendLeg(scopedCtx(validScope()), record); err != nil {
		t.Fatalf("AppendLeg: %v", err)
	}
	env := terminal.envelope()
	if env.Version != sdkbilling.TerminalEnvelopeVersionV2 || env.Record == nil {
		t.Fatalf("envelope = version %d record %v, want version 2 with record", env.Version, env.Record != nil)
	}
	wantJSON, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(env.Record.JSON, wantJSON) {
		t.Fatalf("carried payload is not the exact sealed record bytes:\n got %s\nwant %s", env.Record.JSON, wantJSON)
	}
	if env.Record.Key != env.EnvelopeID || env.Record.Key != record.Key {
		t.Fatalf("carried identity key %q envelope %q want sealed %q", env.Record.Key, env.EnvelopeID, record.Key)
	}
	if env.Record.Fingerprint != record.Fingerprint {
		t.Fatalf("carried fingerprint %q want %q", env.Record.Fingerprint, record.Fingerprint)
	}
	sum := sha256.Sum256(env.Record.JSON)
	if env.PayloadHash != hex.EncodeToString(sum[:]) {
		t.Fatalf("payload hash %q does not cover the carried bytes", env.PayloadHash)
	}
	var decoded usageevidence.CallLegUsageRecord
	if err := json.Unmarshal(env.Record.JSON, &decoded); err != nil {
		t.Fatalf("decode carried record: %v", err)
	}
	if !reflect.DeepEqual(decoded, record) {
		t.Fatalf("decoded record differs from the sealed record:\n got %+v\nwant %+v", decoded, record)
	}
	resealed, err := decoded.Seal()
	if err != nil {
		t.Fatalf("re-seal carried record: %v", err)
	}
	if resealed.Key != env.Record.Key || resealed.Fingerprint != env.Record.Fingerprint {
		t.Fatalf("re-sealed identity %q/%q differs from carried %q/%q", resealed.Key, resealed.Fingerprint, env.Record.Key, env.Record.Fingerprint)
	}
	if decoded.BackendID != "backend-1" || decoded.ProviderID != "provider-1" || decoded.ModelID != "model-1" ||
		decoded.Surfaced != usageevidence.SurfacedYes || decoded.OperatorRateRef.ID != "operator-rate-1" {
		t.Fatalf("carried record lost dropped-by-v1 facts: %+v", decoded)
	}
	if env.Outcome != sdkbilling.TerminalCompleted {
		t.Fatalf("outcome = %q, want completed", env.Outcome)
	}
}

// TestAppendLegCarryingSinkMapsNonClosureOutcome proves the honest coarse
// mapping: a never-started disposition is carried verbatim in the record while
// the version-2 envelope carries the non-closure label instead of inventing a
// closure. The version-1 sink keeps failing closed.
func TestAppendLegCarryingSinkMapsNonClosureOutcome(t *testing.T) {
	t.Parallel()
	carrying := &carriageTerminal{}
	a, _, _ := newCarriageAdapter(t, &carriageQuoter{}, carrying)
	record := sealedLegRecord(t)
	record.Outcome = usageevidence.LegOutcomeNeverStarted
	sealed, err := record.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.AppendLeg(scopedCtx(validScope()), sealed); err != nil {
		t.Fatalf("never-started carriage: %v", err)
	}
	env := carrying.envelope()
	if env.Outcome != sdkbilling.TerminalUnresolved {
		t.Fatalf("outcome = %q, want %q", env.Outcome, sdkbilling.TerminalUnresolved)
	}
	var decoded usageevidence.CallLegUsageRecord
	if err := json.Unmarshal(env.Record.JSON, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Outcome != usageevidence.LegOutcomeNeverStarted {
		t.Fatalf("carried disposition = %q, want never_started", decoded.Outcome)
	}
	// A non-declaring sink keeps version-1 behavior: fail closed, no sink call.
	plain := newParts()
	aV1 := newAdapter(t, plain)
	if err := aV1.AppendLeg(scopedCtx(validScope()), sealed); err == nil {
		t.Fatal("version-1 sink must keep failing closed on a non-closure disposition")
	}
	if plain.terminal.calls != 0 {
		t.Fatal("version-1 sink must not be called for an unmappable disposition")
	}
}

// TestAppendLegCarryingSinkRejectsConflictingPresetIdentity proves a
// caller-set identity that disagrees with the sealed identity fails closed
// before the envelope is stamped, while the exact sealed identity is accepted.
func TestAppendLegCarryingSinkRejectsConflictingPresetIdentity(t *testing.T) {
	t.Parallel()
	record := sealedLegRecord(t)
	for name, mutate := range map[string]func(*usageevidence.CallLegUsageRecord){
		"foreign key":         func(r *usageevidence.CallLegUsageRecord) { r.Key = "call-foreign:bleg-1" },
		"foreign fingerprint": func(r *usageevidence.CallLegUsageRecord) { r.Fingerprint = strings.Repeat("f", 64) },
	} {
		terminal := &carriageTerminal{}
		a, _, _ := newCarriageAdapter(t, &carriageQuoter{}, terminal)
		rec := record
		mutate(&rec)
		if err := a.AppendLeg(scopedCtx(validScope()), rec); err == nil {
			t.Fatalf("%s: conflicting preset identity must fail closed", name)
		}
		if terminal.callsValue() != 0 {
			t.Fatalf("%s: sink must not be invoked", name)
		}
	}
	// The exact sealed identity (empty preset or matching preset) is accepted.
	for name, preset := range map[string]usageevidence.CallLegUsageRecord{
		"matching preset": record,
		"empty preset": func() usageevidence.CallLegUsageRecord {
			rec := record
			rec.Key = ""
			rec.Fingerprint = ""
			return rec
		}(),
	} {
		terminal := &carriageTerminal{}
		a, _, _ := newCarriageAdapter(t, &carriageQuoter{}, terminal)
		if err := a.AppendLeg(scopedCtx(validScope()), preset); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if terminal.callsValue() != 1 {
			t.Fatalf("%s: sink calls = %d, want 1", name, terminal.callsValue())
		}
	}
}

// TestAppendCallCarryingSinkReceivesExactSealedRecord proves call-closure
// carriage, including the unknown-turn non-closure label.
func TestAppendCallCarryingSinkReceivesExactSealedRecord(t *testing.T) {
	t.Parallel()
	terminal := &carriageTerminal{}
	a, _, _ := newCarriageAdapter(t, &carriageQuoter{}, terminal)
	record := sealedCallRecord(t)
	if err := a.AppendCall(scopedCtx(validScope()), record); err != nil {
		t.Fatalf("AppendCall: %v", err)
	}
	env := terminal.envelope()
	if env.Version != sdkbilling.TerminalEnvelopeVersionV2 || env.Record == nil {
		t.Fatalf("envelope = version %d record %v, want version 2", env.Version, env.Record != nil)
	}
	wantJSON, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(env.Record.JSON, wantJSON) {
		t.Fatalf("call payload is not the exact sealed bytes:\n got %s\nwant %s", env.Record.JSON, wantJSON)
	}
	if env.Outcome != sdkbilling.TerminalCompleted {
		t.Fatalf("outcome = %q, want completed", env.Outcome)
	}
	unknown := sealedCallRecord(t)
	unknown.Outcome = usageevidence.TurnOutcomeUnknown
	sealedUnknown, err := unknown.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.AppendCall(scopedCtx(validScope()), sealedUnknown); err != nil {
		t.Fatalf("unknown-turn carriage: %v", err)
	}
	if got := terminal.envelope().Outcome; got != sdkbilling.TerminalUnresolved {
		t.Fatalf("unknown-turn outcome = %q, want %q", got, sdkbilling.TerminalUnresolved)
	}
}

// TestNonDeclaringSinkKeepsVersion1Bytes locks the version-1 producer
// behavior: no record, the key+fingerprint payload-hash identity, and the same
// observable envelope shape as before the carriage addition.
func TestNonDeclaringSinkKeepsVersion1Bytes(t *testing.T) {
	t.Parallel()
	terminal := newParts().terminal
	a, _, _ := newCarriageAdapter(t, &fakeQuoter{}, terminal)
	record := usageevidence.CallLegUsageRecord{
		Key:         carriageCall + ":" + carriageBLeg,
		Fingerprint: strings.Repeat("a", 64),
		CallID:      usageevidence.BillingCallID(carriageCall),
		ALegID:      "aleg-1",
		BLegID:      carriageBLeg,
		AttemptSeq:  3,
		BackendID:   "backend-1",
		ProviderID:  "provider-1",
		ModelID:     "model-1",
		StartedAt:   time.Now().UTC(),
		FinishedAt:  time.Now().UTC().Add(time.Second),
		Outcome:     usageevidence.LegOutcomeWinner,
		Surfaced:    usageevidence.SurfacedYes,
	}
	if err := a.AppendLeg(scopedCtx(validScope()), record); err != nil {
		t.Fatalf("AppendLeg: %v", err)
	}
	env := terminal.last
	if env.Version != sdkbilling.TerminalEnvelopeVersionV1 || env.Record != nil {
		t.Fatalf("version-1 envelope = version %d record %v, want version 1 without record", env.Version, env.Record != nil)
	}
	sum := sha256.Sum256([]byte(record.Key + "\x00" + record.Fingerprint))
	if env.PayloadHash != hex.EncodeToString(sum[:]) {
		t.Fatalf("version-1 payload hash %q is not the key+fingerprint identity", env.PayloadHash)
	}
}

// TestAdmitCarrierProjectsOrderedRoutesAndStampsFrozenBindings proves the
// adapter receives the ordered planned-leaf occurrence list with duplicates
// (so a per-occurrence quote maximum stays 20, not 10), the route key matches
// the canonical identity, and the frozen bindings are stamped on the admitted
// exposure.
func TestAdmitCarrierProjectsOrderedRoutesAndStampsFrozenBindings(t *testing.T) {
	t.Parallel()
	quoter := &carriageQuoter{}
	quoter.respond = func(in economics.QuoteInput) (economics.ExposureQuote, error) {
		// Price every occurrence: 10 nano per planned leaf.
		return carriageQuote(in, economics.RouteTariffModeFrozen, frozenBindingsFor(in.Routes), int64(len(in.Routes))*10), nil
	}
	a, _, _ := newCarriageAdapter(t, quoter, &carriageTerminal{})
	exposure, err := a.Admit(scopedCtx(validScope()), admitInputWithRoute(t, "a:m1!a:m1"))
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	last := quoter.lastInput()
	wantRoutes := []economics.RouteCandidate{{Backend: "a", Model: "m1"}, {Backend: "a", Model: "m1"}}
	if !reflect.DeepEqual(last.Routes, wantRoutes) {
		t.Fatalf("quoter routes = %+v, want ordered occurrences %+v", last.Routes, wantRoutes)
	}
	if exposure.Max.Nano != 20 || exposure.Max.Currency != "USD" {
		t.Fatalf("exposure maximum = %+v, want 20 USD (duplicate leaves cost twice)", exposure.Max)
	}
	if len(exposure.RouteTariffs) != 1 || exposure.RouteTariffs[0].RouteID != "a:m1" {
		t.Fatalf("exposure route tariffs = %+v, want one a:m1 binding", exposure.RouteTariffs)
	}
	// Two distinct planned routes carry two sorted bindings.
	exposure, err = a.Admit(scopedCtx(validScope()), admitInputWithRoute(t, "b:m2|a:m1"))
	if err != nil {
		t.Fatalf("Admit two routes: %v", err)
	}
	if len(exposure.RouteTariffs) != 2 || exposure.RouteTariffs[0].RouteID != "a:m1" || exposure.RouteTariffs[1].RouteID != "b:m2" {
		t.Fatalf("route tariffs = %+v, want sorted distinct routes a:m1,b:m2", exposure.RouteTariffs)
	}
}

// TestAdmitCarrierModeNoneKeepsFallbackEmpty proves the intentional scalar and
// strict-ceiling-fallback quote: mode none accepts a duplicate plan with no
// bindings and stamps nothing.
func TestAdmitCarrierModeNoneKeepsFallbackEmpty(t *testing.T) {
	t.Parallel()
	quoter := &carriageQuoter{}
	quoter.respond = func(in economics.QuoteInput) (economics.ExposureQuote, error) {
		return carriageQuote(in, economics.RouteTariffModeNone, nil, 5), nil
	}
	a, _, _ := newCarriageAdapter(t, quoter, &carriageTerminal{})
	exposure, err := a.Admit(scopedCtx(validScope()), admitInputWithRoute(t, "a:m1!a:m1"))
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if len(exposure.RouteTariffs) != 0 {
		t.Fatalf("scalar quote must stamp no bindings, got %+v", exposure.RouteTariffs)
	}
	if exposure.Max.Nano != 5 {
		t.Fatalf("exposure maximum = %d, want the frozen scalar 5", exposure.Max.Nano)
	}
}

// TestAdmitCarrierRejectsIncompleteOrForeignCoverage locks frozen coverage:
// missing, extra, conflicting, and mode-less declarations fail closed before
// the admission port runs; equal duplicates collapse exactly like the
// reference producer.
func TestAdmitCarrierRejectsIncompleteOrForeignCoverage(t *testing.T) {
	t.Parallel()
	foreign := economics.RouteTariffBinding{
		RouteID: "z:foreign", TariffID: "t", TariffVersion: "v1", ContentHash: strings.Repeat("a", 64),
	}
	conflicting := func() []economics.RouteTariffBinding {
		first := frozenBindingsFor([]economics.RouteCandidate{{Backend: "a", Model: "m1"}})[0]
		second := first
		second.TariffVersion = "v2"
		return []economics.RouteTariffBinding{first, second}
	}
	cases := map[string]struct {
		selector string
		build    func(in economics.QuoteInput) (economics.ExposureQuote, error)
		// wantSDKCause marks the declaring-carrier case whose quote the SDK
		// itself rejects: its error must keep the SDK invalid-quote cause as
		// well as the admission and quote-mismatch classes.
		wantSDKCause bool
	}{
		"missing planned route": {
			selector: "a:m1|b:m2",
			build: func(in economics.QuoteInput) (economics.ExposureQuote, error) {
				return carriageQuote(in, economics.RouteTariffModeFrozen, frozenBindingsFor(in.Routes[:1]), 20), nil
			},
		},
		"extra foreign route": {
			selector: "a:m1",
			build: func(in economics.QuoteInput) (economics.ExposureQuote, error) {
				bindings := append(frozenBindingsFor(in.Routes), foreign)
				return carriageQuote(in, economics.RouteTariffModeFrozen, bindings, 20), nil
			},
		},
		"conflicting duplicate route": {
			selector: "a:m1",
			build: func(in economics.QuoteInput) (economics.ExposureQuote, error) {
				return carriageQuote(in, economics.RouteTariffModeFrozen, conflicting(), 20), nil
			},
		},
		"declaring quoter without mode": {
			selector: "a:m1",
			build: func(in economics.QuoteInput) (economics.ExposureQuote, error) {
				return carriageQuote(in, "", nil, 20), nil
			},
		},
		"frozen with empty bindings": {
			selector:     "a:m1",
			wantSDKCause: true,
			build: func(in economics.QuoteInput) (economics.ExposureQuote, error) {
				return carriageQuote(in, economics.RouteTariffModeFrozen, nil, 10), nil
			},
		},
		"frozen without planned routes": {
			selector: "",
			build: func(in economics.QuoteInput) (economics.ExposureQuote, error) {
				bindings := frozenBindingsFor([]economics.RouteCandidate{{Backend: "a", Model: "m1"}})
				return carriageQuote(in, economics.RouteTariffModeFrozen, bindings, 20), nil
			},
		},
	}
	for name, tc := range cases {
		quoter := &carriageQuoter{respond: tc.build}
		a, _, admitter := newCarriageAdapter(t, quoter, &carriageTerminal{})
		in := admitInput()
		if tc.selector != "" {
			sel, err := routing.Parse(tc.selector)
			if err != nil {
				t.Fatalf("%s: parse selector: %v", name, err)
			}
			in.Route = sel
		}
		if _, admitErr := a.Admit(scopedCtx(validScope()), in); admitErr == nil {
			t.Fatalf("%s: expected fail-closed rejection", name)
		} else {
			if !errors.Is(admitErr, billingbinding.ErrAdmission) {
				t.Fatalf("%s: error must keep the admission class: %v", name, admitErr)
			}
			if !errors.Is(admitErr, sdkbilling.ErrQuoteMismatch) {
				t.Fatalf("%s: error must be the typed quote mismatch: %v", name, admitErr)
			}
			// A malformed declaring-carrier quote is rejected by the SDK as
			// well; the SDK cause must survive alongside the mismatch class
			// (and never for the valid-quote coverage failures).
			if got := errors.Is(admitErr, economics.ErrInvalidQuote); got != tc.wantSDKCause {
				t.Fatalf("%s: SDK invalid-quote cause = %v, want %v: %v", name, got, tc.wantSDKCause, admitErr)
			}
		}
		if admitter.calls != 0 {
			t.Fatalf("%s: admission port must not run without exact coverage", name)
		}
	}
	// Equal duplicates collapse: echoing one binding per planned occurrence is
	// accepted and stamped exactly once, matching the reference producer.
	quoter := &carriageQuoter{}
	quoter.respond = func(in economics.QuoteInput) (economics.ExposureQuote, error) {
		bindings := frozenBindingsFor(in.Routes)
		echoed := append(append([]economics.RouteTariffBinding(nil), bindings...), bindings...)
		return carriageQuote(in, economics.RouteTariffModeFrozen, echoed, 20), nil
	}
	a, _, _ := newCarriageAdapter(t, quoter, &carriageTerminal{})
	exposure, err := a.Admit(scopedCtx(validScope()), admitInputWithRoute(t, "a:m1!a:m1"))
	if err != nil {
		t.Fatalf("equal duplicates must collapse: %v", err)
	}
	if len(exposure.RouteTariffs) != 1 {
		t.Fatalf("collapsed bindings = %+v, want exactly one", exposure.RouteTariffs)
	}
}

// TestAdmitNonCarrierKeepsVersion1Request locks the non-declaring quoter path:
// no route facts are sent, no mode or coverage is required, and no bindings
// are stamped even when the plan has route leaves.
func TestAdmitNonCarrierKeepsVersion1Request(t *testing.T) {
	t.Parallel()
	quoter := &fakeQuoter{quote: quoteFixture(t)}
	a, _, _ := newCarriageAdapter(t, quoter, &carriageTerminal{})
	if _, err := a.Admit(scopedCtx(validScope()), admitInputWithRoute(t, "a:m1!a:m1")); err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if len(quoter.last.Routes) != 0 {
		t.Fatalf("version-1 quoter must receive no route facts, got %+v", quoter.last.Routes)
	}
}

// TestIdentityUsesReferenceFactsWhenDeclared proves the optional facts port:
// absent facts stay absent, per-route facts stay per-route, and the
// non-declaring quoter keeps today's scope mapping.
func TestIdentityUsesReferenceFactsWhenDeclared(t *testing.T) {
	t.Parallel()
	quoter := &carriageQuoter{}
	a, _, _ := newCarriageAdapter(t, quoter, &carriageTerminal{})
	id := a.Identity()
	ctx := context.Background()
	if got := id.CustomerPricingRef(ctx, aipapiCall()); got.ID != "customer-pricing" || got.Version != "v7" {
		t.Fatalf("customer pricing ref = %+v, want the declared fact", got)
	}
	if got := id.ChargePolicyRef(ctx, aipapiCall()); got.ID != "charge-policy" || got.Version != "v3" {
		t.Fatalf("charge policy ref = %+v, want the declared fact", got)
	}
	if got := id.WireCustomerPricingRef(ctx); got.ID != "customer-pricing" {
		t.Fatalf("wire customer pricing ref = %+v, want the declared fact", got)
	}
	if got := id.WireChargePolicyRef(ctx); got.ID != "charge-policy" {
		t.Fatalf("wire charge policy ref = %+v, want the declared fact", got)
	}
	if got := id.OperatorRateRef(ctx, "backend-1", "model-1"); got.ID != "operator-rate-1" {
		t.Fatalf("operator rate ref = %+v, want per-route fact", got)
	}
	if got := id.OperatorRateRef(ctx, "backend-2", "model-2"); got.ID != "operator-rate-2" {
		t.Fatalf("second operator rate ref = %+v, want distinct per-route fact", got)
	}
	if got := id.OperatorRateRef(ctx, "unbound", "route"); got != (usageevidence.VersionRef{}) {
		t.Fatalf("absent operator rate ref = %+v, want the zero ref (never substituted)", got)
	}
	// Non-declaring quoter: today's exact scope mapping, operator rate equals
	// the customer pricing ref.
	plain, _, _ := newCarriageAdapter(t, &fakeQuoter{}, &carriageTerminal{})
	plainID := plain.Identity()
	if got := plainID.OperatorRateRef(ctx, "backend-1", "model-1"); got.ID != "tariff-snap" {
		t.Fatalf("non-opting operator rate ref = %+v, want today's scope mapping", got)
	}
	if got := plainID.CustomerPricingRef(ctx, aipapiCall()); got.ID != "tariff-snap" {
		t.Fatalf("non-opting customer pricing ref = %+v, want scope ref", got)
	}
}

// TestCarvedSentinelClassesStayDistinct locks the moved credit-screen
// sentinel's classification boundary: degraded is unavailable, never denied.
func TestCarvedSentinelClassesStayDistinct(t *testing.T) {
	t.Parallel()
	parts := newParts()
	parts.screen.result = sdkbilling.CreditScreenResult{Decision: sdkbilling.CreditDegraded, StoreID: testStore, AccountID: "acct-1"}
	a := newAdapter(t, parts)
	err := a.Check(scopedCtx(validScope()), "acct-1")
	if err == nil || errors.Is(err, billingport.ErrCreditScreenDenied) || !errors.Is(err, billingport.ErrCreditScreenUnavailable) {
		t.Fatalf("degraded classification = %v, want unavailable but not denied", err)
	}
}

// TestReferenceFactsProduceSealedRecordsThroughCarriage proves the producer
// path from real admission rather than a hand-built record: the admitted
// exposure carries the declared facts, the record is built from those stamped
// refs (exactly what the runtime's resolveExposureIdentity/stampExposureIdentity
// installs), sealing preserves them, and version-2 carriage carries them
// byte-exactly — one distinct operator ref per route, and an absent operator
// ref that stays absent.
func TestReferenceFactsProduceSealedRecordsThroughCarriage(t *testing.T) {
	t.Parallel()
	terminal := &carriageTerminal{}
	a, _, _ := newCarriageAdapter(t, &carriageQuoter{}, terminal)
	ctx := scopedCtx(validScope())
	now := time.Now().UTC()

	// The admission owns the frozen customer-pricing and charge-policy refs;
	// the runtime prefers these non-zero admitted refs over its fallbacks.
	exposure, err := a.Admit(ctx, admitInputWithRoute(t, "a:m1"))
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	call := usageevidence.CallUsageRecord{
		SchemaVersion:      usageevidence.CurrentRecordSchemaVersion,
		CallID:             usageevidence.BillingCallID(carriageCall),
		AccountID:          "acct-1",
		ALegID:             "aleg-1",
		SessionID:          "sess-1",
		StartedAt:          now,
		FinishedAt:         now.Add(time.Second),
		Outcome:            usageevidence.TurnOutcomeCompleted,
		CustomerPricingRef: exposure.PricingRef,
		ChargePolicyRef:    exposure.ChargePolicyRef,
	}
	sealedCall, err := call.Seal()
	if err != nil {
		t.Fatalf("seal generated call: %v", err)
	}
	if err := a.AppendCall(ctx, sealedCall); err != nil {
		t.Fatalf("AppendCall: %v", err)
	}
	var carriedCall usageevidence.CallUsageRecord
	if err := json.Unmarshal(terminal.envelope().Record.JSON, &carriedCall); err != nil {
		t.Fatal(err)
	}
	if carriedCall.CustomerPricingRef.ID != "customer-pricing" || carriedCall.ChargePolicyRef.ID != "charge-policy" {
		t.Fatalf("carried call refs = %+v/%+v, want the resolved facts", carriedCall.CustomerPricingRef, carriedCall.ChargePolicyRef)
	}
	if carriedCall.CustomerPricingRef != exposure.PricingRef || carriedCall.ChargePolicyRef != exposure.ChargePolicyRef {
		t.Fatalf("carried call refs differ from the admitted exposure refs: %+v/%+v vs %+v/%+v",
			carriedCall.CustomerPricingRef, carriedCall.ChargePolicyRef, exposure.PricingRef, exposure.ChargePolicyRef)
	}
	id := a.Identity()

	legFor := func(bLegID, backend, model string, seq int) usageevidence.CallLegUsageRecord {
		leg := usageevidence.CallLegUsageRecord{
			CallID:     usageevidence.BillingCallID(carriageCall),
			ALegID:     "aleg-1",
			BLegID:     bLegID,
			AttemptSeq: seq,
			BackendID:  backend,
			ProviderID: "provider-" + backend,
			ModelID:    model,
			StartedAt:  now,
			FinishedAt: now.Add(time.Second),
			Outcome:    usageevidence.LegOutcomeWinner,
			Surfaced:   usageevidence.SurfacedNo,
			Evidence: usageevidence.FinalBillingEvidence{
				InputTokens: usageevidence.Quantity{Value: 1, Present: true},
				Source:      usageevidence.EvidenceSourceProviderReported,
				Authority:   usageevidence.EvidenceAuthorityAuthoritative,
				DedupeKey:   "dedupe-" + bLegID,
			},
			OperatorRateRef: id.OperatorRateRef(ctx, backend, model),
		}
		sealed, err := leg.Seal()
		if err != nil {
			t.Fatalf("seal generated leg %s: %v", bLegID, err)
		}
		return sealed
	}
	for _, tc := range []struct {
		bLegID, backend, model, wantOperatorRef string
		seq                                     int
	}{
		{"bleg-1", "backend-1", "model-1", "operator-rate-1", 1},
		{"bleg-2", "backend-2", "model-2", "operator-rate-2", 2},
		{"bleg-3", "unbound", "route", "", 3},
	} {
		if err := a.AppendLeg(ctx, legFor(tc.bLegID, tc.backend, tc.model, tc.seq)); err != nil {
			t.Fatalf("AppendLeg %s: %v", tc.bLegID, err)
		}
		var carried usageevidence.CallLegUsageRecord
		if err := json.Unmarshal(terminal.envelope().Record.JSON, &carried); err != nil {
			t.Fatal(err)
		}
		if carried.OperatorRateRef.ID != tc.wantOperatorRef {
			t.Fatalf("%s carried operator ref = %+v, want ID %q", tc.bLegID, carried.OperatorRateRef, tc.wantOperatorRef)
		}
		if carried.OperatorRateRef.ID == "" && carried.OperatorRateRef != (usageevidence.VersionRef{}) {
			t.Fatalf("%s absent operator ref must stay the zero ref, got %+v", tc.bLegID, carried.OperatorRateRef)
		}
	}
}

// factsQuoter is the optional ReferenceFacts quoter with distinct, timestamped
// facts or absent facts. It counts the frozen-fact reads so admission can be
// proven to resolve them exactly once, before the atomic admission port runs.
type factsQuoter struct {
	*carriageQuoter
	absent           bool
	pricingEffective time.Time
	pricingFetched   time.Time
	policyEffective  time.Time
	policyFetched    time.Time
	mu               sync.Mutex
	pricingCalls     int
	policyCalls      int
}

func (q *factsQuoter) CustomerPricingRef(context.Context) economics.VersionRef {
	q.mu.Lock()
	q.pricingCalls++
	q.mu.Unlock()
	if q.absent {
		return economics.VersionRef{}
	}
	return economics.VersionRef{
		ID: "actual-pricing", Version: "actual-v1",
		EffectiveAt: q.pricingEffective, FetchedAt: q.pricingFetched,
	}
}

func (q *factsQuoter) ChargePolicyRef(context.Context) economics.VersionRef {
	q.mu.Lock()
	q.policyCalls++
	q.mu.Unlock()
	if q.absent {
		return economics.VersionRef{}
	}
	return economics.VersionRef{
		ID: "actual-policy", Version: "actual-v2",
		EffectiveAt: q.policyEffective, FetchedAt: q.policyFetched,
	}
}

func (q *factsQuoter) factsCalls() (pricing, policy int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.pricingCalls, q.policyCalls
}

// TestAdmitProjectsDeclaredReferenceFactsBeforeAdmission locks the F1 fix: the
// declared frozen facts are resolved exactly once, before the atomic admission
// port, and projected onto the admitted exposure — distinct facts keep their
// timestamps, an absent fact stays the zero ref, and the scope snapshots never
// replace either. The runtime prefers these non-zero admitted refs over its
// fallback resolvers, so the request's terminal facts stay exactly these.
func TestAdmitProjectsDeclaredReferenceFactsBeforeAdmission(t *testing.T) {
	t.Parallel()
	effective := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	fetched := effective.Add(time.Hour)
	for _, absent := range []bool{false, true} {
		q := &factsQuoter{
			carriageQuoter:   &carriageQuoter{},
			absent:           absent,
			pricingEffective: effective, pricingFetched: fetched,
			policyEffective: effective, policyFetched: fetched,
		}
		a, _, admitter := newCarriageAdapter(t, q, &carriageTerminal{})
		ctx := scopedCtx(validScope())
		exposure, err := a.Admit(ctx, admitInputWithRoute(t, "a:m1"))
		if err != nil {
			t.Fatalf("absent=%v: Admit: %v", absent, err)
		}
		pricingCalls, policyCalls := q.factsCalls()
		if pricingCalls != 1 || policyCalls != 1 {
			t.Fatalf("absent=%v: facts resolved %d/%d times, want exactly once before admission", absent, pricingCalls, policyCalls)
		}
		if admitter.calls != 1 {
			t.Fatalf("absent=%v: admission port calls = %d, want 1", absent, admitter.calls)
		}
		if absent {
			if exposure.PricingRef != (usageevidence.VersionRef{}) || exposure.ChargePolicyRef != (usageevidence.VersionRef{}) {
				t.Fatalf("absent facts must stay the zero ref, got %+v/%+v", exposure.PricingRef, exposure.ChargePolicyRef)
			}
			continue
		}
		wantPricing := usageevidence.VersionRef{ID: "actual-pricing", Version: "actual-v1", EffectiveAt: effective, FetchedAt: fetched}
		wantPolicy := usageevidence.VersionRef{ID: "actual-policy", Version: "actual-v2", EffectiveAt: effective, FetchedAt: fetched}
		if exposure.PricingRef != wantPricing || exposure.ChargePolicyRef != wantPolicy {
			t.Fatalf("admitted exposure refs = %+v/%+v, want the declared frozen facts %+v/%+v",
				exposure.PricingRef, exposure.ChargePolicyRef, wantPricing, wantPolicy)
		}
		// The Identity resolvers agree with the frozen admission projection.
		id := a.Identity()
		if got := id.CustomerPricingRef(ctx, aipapiCall()); got != exposure.PricingRef {
			t.Fatalf("identity pricing ref = %+v, want the admitted %+v", got, exposure.PricingRef)
		}
		if got := id.ChargePolicyRef(ctx, aipapiCall()); got != exposure.ChargePolicyRef {
			t.Fatalf("identity policy ref = %+v, want the admitted %+v", got, exposure.ChargePolicyRef)
		}
	}
}
