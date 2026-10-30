package metering_test

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestPhase2ReviewLiftedObservationCanReplay(t *testing.T) {
	fact := metering.Fact{
		FactID: "legacy-fact", StreamID: "legacy-stream", Sequence: 1,
		Kind: metering.FactKindDelta, Perspective: metering.PerspectiveCustomer,
		Boundary: metering.BoundaryFrontendIngress, Lifecycle: metering.LifecycleLogicalRequest,
		Correlation: metering.Correlation{RequestID: "request-1"},
		Source:      metering.SourceProviderReported, Authority: metering.AuthorityAuthoritative,
		Presence: metering.PresencePresent, RecordedAt: time.Unix(1700000000, 0).UTC(),
		Quantities: []metering.Quantity{{Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: 12, Present: true}},
	}
	observation, err := metering.ObservationFromFact(fact)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	var generic metering.Observation
	if err := json.Unmarshal(wire, &generic); err != nil {
		t.Fatalf("generic observation decoding must remain available for inspection: %v", err)
	}
	if err := generic.Validate(); err == nil {
		t.Fatal("generic JSON decoding must not grant legacy provider authority")
	}

	replay, err := metering.ReadLegacyV1Observation(wire)
	if err != nil {
		t.Fatalf("trusted legacy reader must replay a selected historical record: %v", err)
	}
	if replay.Fingerprint() != observation.Fingerprint() {
		t.Fatal("replay changed observation fingerprint")
	}
	for _, mutate := range []func(*metering.Observation){
		func(o *metering.Observation) { o.Subject.StoreID = "new-v2-store" },
		func(o *metering.Observation) { o.Correlation.StoreID = "new-v2-store" },
		func(o *metering.Observation) {
			o.Subject.StoreID = "new-v2-store"
			o.Correlation.StoreID = "new-v2-store"
		},
	} {
		forgedTrusted := replay
		mutate(&forgedTrusted)
		if err := forgedTrusted.Validate(); err == nil {
			t.Fatal("trusted legacy exception must require both immutable legacy store identities")
		}
	}

	forgedWire := bytes.ReplaceAll(wire, []byte(`"store_id":"legacy_v1"`), []byte(`"store_id":"new-v2-store"`))
	var forged metering.Observation
	if err := json.Unmarshal(forgedWire, &forged); err != nil {
		t.Fatalf("forged observation should still be inspectable as generic JSON: %v", err)
	}
	if err := forged.Validate(); err == nil {
		t.Fatal("wire-controlled legacy markers bypass B-leg ownership in a non-legacy store")
	}
	if _, err := metering.ReadLegacyV1Observation(forgedWire); err == nil {
		t.Fatal("trusted legacy reader must reject a record outside the legacy store scope")
	}
	for _, tc := range []struct {
		name string
		from []byte
		to   []byte
	}{
		{name: "mapping", from: []byte(`"mapping_ref":"legacy_v1_fact"`), to: []byte(`"mapping_ref":"provider:v2"`)},
		{name: "acquisition", from: []byte(`"acquisition":"legacy_v1_fact"`), to: []byte(`"acquisition":"provider_response"`)},
		{name: "origin", from: []byte(`"origin":"provider"`), to: []byte(`"origin":"statement"`)},
		{name: "subject", from: []byte(`"kind":"logical_request"`), to: []byte(`"kind":"b_leg"`)},
	} {
		forged := bytes.Replace(wire, tc.from, tc.to, 1)
		if bytes.Equal(forged, wire) {
			t.Fatalf("%s fixture mutation did not change the durable representation", tc.name)
		}
		if _, err := metering.ReadLegacyV1Observation(forged); err == nil {
			t.Fatalf("trusted legacy reader accepted forged %s marker", tc.name)
		}
	}
}
