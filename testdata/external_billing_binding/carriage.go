package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/billing"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
)

// This file proves the version-2 carriage contract from a separate module
// using only public packages: a sink declares RecordCarrier and persists the
// carried record bytes verbatim, and a quoter declares RouteTariffCarrier and
// ReferenceFacts to receive the ordered planned-route occurrence list and
// return frozen per-route bindings.

// ErrCarrierReplayConflict is the module-local typed conflict for a same-key
// replay whose carried bytes differ from the durable record.
var ErrCarrierReplayConflict = errors.New("external carrier: conflicting carried record")

// carryingTerminalLog is the public-only version-2 sink: it validates the
// envelope through the public contract, persists the exact carried bytes, and
// answers each immutable record's durable generation. Per the contract, the
// revision is 1 for the first durable immutable append of a record and stays 1
// on an idempotent replay — a per-record generation, never a moving log offset.
// Identical replays are idempotent.
type carryingTerminalLog struct {
	mu           sync.Mutex
	keys         []string
	bytes        [][]byte
	fingerprints []string
}

func (*carryingTerminalLog) CarriesUsageRecords() {}

func (l *carryingTerminalLog) AppendTerminal(_ context.Context, env billing.TerminalEnvelope) (billing.TerminalAck, error) {
	if err := env.Validate(); err != nil {
		return billing.TerminalAck{}, err
	}
	if env.Record == nil {
		return billing.TerminalAck{}, fmt.Errorf("carrier sink requires a version-2 record payload")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, key := range l.keys {
		if key != env.Record.Key {
			continue
		}
		if l.fingerprints[i] != env.Record.Fingerprint || string(l.bytes[i]) != string(env.Record.JSON) {
			return billing.TerminalAck{}, fmt.Errorf("%w: key %q", ErrCarrierReplayConflict, key)
		}
		ack := billing.TerminalAck{StoreID: env.StoreID, EnvelopeID: env.EnvelopeID, Revision: 1}
		return ack, ack.Validate()
	}
	l.keys = append(l.keys, env.Record.Key)
	l.fingerprints = append(l.fingerprints, env.Record.Fingerprint)
	l.bytes = append(l.bytes, append([]byte(nil), env.Record.JSON...))
	ack := billing.TerminalAck{StoreID: env.StoreID, EnvelopeID: env.EnvelopeID, Revision: 1}
	if err := ack.Validate(); err != nil {
		return billing.TerminalAck{}, err
	}
	return ack, nil
}

// stored returns the verbatim payload bytes of the i-th durable record.
func (l *carryingTerminalLog) stored(i int) (string, string, []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.keys[i], l.fingerprints[i], append([]byte(nil), l.bytes[i]...)
}

// carryingQuoter declares RouteTariffCarrier and ReferenceFacts: it receives
// the ordered planned-route occurrence list and returns frozen bindings, one
// per distinct canonical route key, with a per-occurrence maximum.
type carryingQuoter struct{}

func (carryingQuoter) CarriesRouteTariffs() {}

func (carryingQuoter) CustomerPricingRef(context.Context) economics.VersionRef {
	return economics.VersionRef{ID: "customer-pricing-external", Version: "v1"}
}

func (carryingQuoter) ChargePolicyRef(context.Context) economics.VersionRef {
	return economics.VersionRef{ID: "charge-policy-external", Version: "v1"}
}

func (carryingQuoter) OperatorRateRef(_ context.Context, backendID, modelID string) economics.VersionRef {
	if backendID == "" && modelID == "" {
		return economics.VersionRef{}
	}
	return economics.VersionRef{ID: "operator-rate-" + economics.RouteTariffKey(backendID, modelID), Version: "v1"}
}

func (q carryingQuoter) Quote(_ context.Context, in economics.QuoteInput) (economics.ExposureQuote, error) {
	if err := in.Validate(); err != nil {
		return economics.ExposureQuote{}, err
	}
	type seen struct{}
	distinct := map[string]struct{}{}
	var bindings []economics.RouteTariffBinding
	for _, route := range in.Routes {
		key := economics.RouteTariffKey(route.Backend, route.Model)
		if _, ok := distinct[key]; ok {
			continue
		}
		distinct[key] = seen{}
		sum := sha256.Sum256([]byte("external-tariff:" + key))
		bindings = append(bindings, economics.RouteTariffBinding{
			RouteID: key, TariffID: "tariff-" + key, TariffVersion: "v1",
			ContentHash: hex.EncodeToString(sum[:]),
		})
	}
	mode := economics.RouteTariffModeFrozen
	if len(bindings) == 0 {
		mode = economics.RouteTariffModeNone
	}
	quote := economics.ExposureQuote{
		ID:                   "quote-carrier-1",
		Version:              1,
		Basis:                economics.BasisCustomerPolicy,
		Subject:              in.Subject.Clone(),
		Maximum:              economics.Money{NanoUnits: submissionFeeNano * int64(len(in.Routes)), Currency: testCurrency, Present: true},
		Tariff:               offerScope().Tariff,
		TariffContent:        testContentRefPtr("tariff"),
		Policy:               offerScope().Policy,
		PolicyContent:        testContentRefPtr("policy"),
		InputSetHash:         hashHex("carrier-quote-inputs"),
		QualifierSnapshotRef: testContentRefPtr("qualifiers"),
		Completeness:         economics.CompletenessComplete,
		RouteTariffMode:      mode,
		RouteTariffs:         bindings,
	}
	if err := quote.Validate(); err != nil {
		return economics.ExposureQuote{}, err
	}
	return quote, nil
}

// carrierQuoteInput mirrors the shared offer quote input with the planned
// route occurrences under test.
func carrierQuoteInput(routes []economics.RouteCandidate) economics.QuoteInput {
	in := quoteInput(10)
	in.Routes = routes
	return in
}

var (
	_ billing.RecordCarrier        = (*carryingTerminalLog)(nil)
	_ economics.RouteTariffCarrier = carryingQuoter{}
	_ economics.ReferenceFacts     = carryingQuoter{}
)
