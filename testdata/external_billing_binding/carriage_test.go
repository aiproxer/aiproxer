package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/billing"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
)

// TestPublicCarrierPersistsVersion2BytesVerbatim proves the version-2 carriage
// contract end to end from a separate public-only module: the envelope
// validates through the public contract, the sink persists the carried bytes
// verbatim, an identical replay is idempotent, and a conflicting replay for
// the same key fails closed.
func TestPublicCarrierPersistsVersion2BytesVerbatim(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"external":"record-bytes","revision":1}`)
	sum := sha256.Sum256(payload)
	env, err := terminalEnvelopeFixture("env-carrier-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	env.Version = billing.TerminalEnvelopeVersionV2
	env.Record = &billing.TerminalUsageRecord{
		Key:         env.EnvelopeID,
		Fingerprint: hashHex("carrier-fingerprint-1"),
		JSON:        payload,
	}
	env.PayloadHash = hex.EncodeToString(sum[:])
	sink := &carryingTerminalLog{}
	ack, err := sink.AppendTerminal(context.Background(), env)
	if err != nil {
		t.Fatalf("carrier append: %v", err)
	}
	if ack.Revision != 1 || !ack.MatchesEnvelope(env) {
		t.Fatalf("carrier ack = %+v, want durable revision 1", ack)
	}
	key, fingerprint, stored := sink.stored(0)
	if key != env.EnvelopeID || fingerprint != env.Record.Fingerprint || !bytes.Equal(stored, payload) {
		t.Fatalf("stored record = %q/%q/%s, want verbatim carried bytes", key, fingerprint, stored)
	}
	replay, err := sink.AppendTerminal(context.Background(), env)
	if err != nil || replay.Revision != ack.Revision {
		t.Fatalf("identical replay = %+v/%v, want the durable revision", replay, err)
	}
	conflicting := env
	conflicting.Record = &billing.TerminalUsageRecord{
		Key:         env.EnvelopeID,
		Fingerprint: env.Record.Fingerprint,
		JSON:        []byte(`{"external":"edited"}`),
	}
	edited := sha256.Sum256(conflicting.Record.JSON)
	conflicting.PayloadHash = hex.EncodeToString(edited[:])
	if _, err := sink.AppendTerminal(context.Background(), conflicting); !errors.Is(err, ErrCarrierReplayConflict) {
		t.Fatalf("conflicting replay err = %v, want typed conflict", err)
	}
	// Version 1 keeps rejecting a carried record through the public contract.
	legacy, err := terminalEnvelopeFixture("env-carrier-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	legacy.Record = env.Record
	if err := legacy.Validate(); err == nil {
		t.Fatal("version 1 with a carried record must fail closed")
	}
}

// TestPublicRouteCarrierCarriesOrderedOccurrences proves the route carriage
// surface from the separate module: duplicates survive as ordered occurrences,
// the quoter returns distinct frozen bindings, and the frozen quote validates
// through the public admission input.
func TestPublicRouteCarrierCarriesOrderedOccurrences(t *testing.T) {
	t.Parallel()
	quoter := carryingQuoter{}
	in := carrierQuoteInput([]economics.RouteCandidate{
		{Backend: "a", Model: "m1"},
		{Backend: "a", Model: "m1"},
		{Backend: "b", Model: "m2"},
	})
	quote, err := quoter.Quote(context.Background(), in)
	if err != nil {
		t.Fatalf("carrier quote: %v", err)
	}
	if quote.RouteTariffMode != economics.RouteTariffModeFrozen || len(quote.RouteTariffs) != 2 {
		t.Fatalf("quote carriage = %q/%+v, want two distinct frozen bindings", quote.RouteTariffMode, quote.RouteTariffs)
	}
	if quote.Maximum.NanoUnits != 3*submissionFeeNano {
		t.Fatalf("quote maximum = %d, want per-occurrence pricing (duplicate counted twice)", quote.Maximum.NanoUnits)
	}
	admission := billing.ExposureAdmissionInput{
		Subject:         in.Subject,
		Scope:           validScope(),
		AccountID:       testAccount,
		Quote:           quote,
		ExecutionLimits: quoteLimits(10, 2048),
	}
	if err := admission.Validate(); err != nil {
		t.Fatalf("frozen carrier quote must validate for public admission: %v", err)
	}
}

// TestCarrierReplayRevisionStableAcrossOtherAppends locks the fixture's
// immutable-ack promise: a durable record keeps its original generation when it
// is replayed after an unrelated append, exactly like a per-record immutable
// revision rather than a moving log offset.
func TestCarrierReplayRevisionStableAcrossOtherAppends(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"external":"immutable-record"}`)
	sum := sha256.Sum256(payload)
	env, err := terminalEnvelopeFixture("review-a", nil)
	if err != nil {
		t.Fatal(err)
	}
	env.Version = billing.TerminalEnvelopeVersionV2
	env.Record = &billing.TerminalUsageRecord{Key: env.EnvelopeID, Fingerprint: hashHex("review-a"), JSON: payload}
	env.PayloadHash = hex.EncodeToString(sum[:])
	sink := &carryingTerminalLog{}
	first, err := sink.AppendTerminal(context.Background(), env)
	if err != nil {
		t.Fatalf("first append: %v", err)
	}
	other := env.Clone()
	other.EnvelopeID = "review-b"
	other.Record.Key = other.EnvelopeID
	if _, err := sink.AppendTerminal(context.Background(), other); err != nil {
		t.Fatalf("unrelated append: %v", err)
	}
	replay, err := sink.AppendTerminal(context.Background(), env)
	if err != nil {
		t.Fatalf("replay after unrelated append: %v", err)
	}
	if replay.Revision != first.Revision {
		t.Fatalf("immutable record replay revision changed after unrelated append: first=%d replay=%d", first.Revision, replay.Revision)
	}
}
