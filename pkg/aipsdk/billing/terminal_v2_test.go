package billing_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/billing"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// recordCarrierSink pins the optional version-2 sink declaration name.
type recordCarrierSink struct{}

func (recordCarrierSink) CarriesUsageRecords() {}

func (recordCarrierSink) AppendTerminal(context.Context, billing.TerminalEnvelope) (billing.TerminalAck, error) {
	return billing.TerminalAck{}, nil
}

var _ billing.RecordCarrier = recordCarrierSink{}

// goldenEnvelopeV1 is the frozen version-1 byte-identity fixture. Its
// marshaled JSON was captured from the pre-carriage contract and must never
// change: version 1 keeps every field, the payload-hash identity, and the
// exact wire bytes.
func goldenEnvelopeV1() billing.TerminalEnvelope {
	return billing.TerminalEnvelope{
		Version:    billing.TerminalEnvelopeVersionV1,
		EnvelopeID: "call-1:bleg-1",
		StoreID:    "store-test",
		Scope:      knownTestScope(),
		Subject: metering.SubjectRef{
			Kind: metering.SubjectBLeg, StoreID: "store-test",
			BillingCallID: "call-1", BLegID: "bleg-1", AttemptSeq: 3,
			ALegID: "aleg-1", SubmissionID: "sub-1",
		},
		Correlation: metering.CorrelationV2{
			StoreID: "store-test", BillingCallID: "call-1", BLegID: "bleg-1",
			AttemptSeq: 3, ALegID: "aleg-1", SubmissionID: "sub-1",
		},
		Outcome:      billing.TerminalCompleted,
		AttemptSeq:   3,
		ALegID:       "aleg-1",
		SubmissionID: "sub-1",
		Workload:     &billing.TerminalWorkload{Class: "auxiliary", Role: "test-role"},
		Tariff:       economics.RatingSnapshotRef{VersionRef: economics.VersionRef{ID: "tariff-snap", Version: "v1"}},
		Policy:       economics.PolicySnapshotRef{VersionRef: economics.VersionRef{ID: "policy-snap", Version: "v1"}, PolicyID: "policy-1"},
		PayloadHash:  strings.Repeat("a", 64),
	}
}

const goldenEnvelopeV1JSON = `{"version":1,"envelope_id":"call-1:bleg-1","store_id":"store-test","scope":{"subject_kind":"human","principal_id":{"known":true,"value":"user-1"},"display_name":{"known":false,"value":""},"auth_method":{"known":false,"value":""},"credential_id":{"known":false,"value":""},"tenant_id":{"known":true,"value":"tenant-1"},"organization_id":{"known":false,"value":""},"workspace_id":{"known":false,"value":""},"project_id":{"known":false,"value":""},"department_id":{"known":false,"value":""},"cost_center_id":{"known":false,"value":""},"origin":"client","parent_trace_id":{"known":false,"value":""}},"subject":{"kind":"b_leg","store_id":"store-test","a_leg_id":"aleg-1","billing_call_id":"call-1","b_leg_id":"bleg-1","attempt_seq":3,"submission_id":"sub-1"},"correlation":{"store_id":"store-test","billing_call_id":"call-1","a_leg_id":"aleg-1","b_leg_id":"bleg-1","attempt_seq":3,"submission_id":"sub-1"},"outcome":"completed","attempt_seq":3,"a_leg_id":"aleg-1","submission_id":"sub-1","workload":{"class":"auxiliary","role":"test-role"},"tariff":{"id":"tariff-snap","version":"v1"},"policy":{"id":"policy-snap","version":"v1","policy_id":"policy-1"},"payload_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`

// TestTerminalEnvelopeV1GoldenBytesUnchanged pins version-1 wire bytes: the
// version-2 carriage addition must not change any existing envelope byte.
func TestTerminalEnvelopeV1GoldenBytesUnchanged(t *testing.T) {
	t.Parallel()
	env := goldenEnvelopeV1()
	if err := env.Validate(); err != nil {
		t.Fatalf("golden fixture: %v", err)
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != goldenEnvelopeV1JSON {
		t.Fatalf("version-1 bytes changed:\n got %s\nwant %s", raw, goldenEnvelopeV1JSON)
	}
}

func validRecordCarriage(t *testing.T) (billing.TerminalUsageRecord, string) {
	t.Helper()
	payload := []byte(`{"call_id":"call-1","b_leg_id":"bleg-1"}`)
	sum := sha256.Sum256(payload)
	return billing.TerminalUsageRecord{
		Key:         "call-1:bleg-1",
		Fingerprint: strings.Repeat("b", 64),
		JSON:        payload,
	}, hex.EncodeToString(sum[:])
}

func goldenEnvelopeV2(t *testing.T) billing.TerminalEnvelope {
	t.Helper()
	record, hash := validRecordCarriage(t)
	env := goldenEnvelopeV1()
	env.Version = billing.TerminalEnvelopeVersionV2
	env.Record = &record
	env.PayloadHash = hash
	return env
}

// TestTerminalEnvelopeV2RequiresRecordIdentityHashAndBounds locks the
// version-2 carriage validation: exact record/envelope identity, a bounded
// valid-JSON payload, and a payload hash over the carried bytes.
func TestTerminalEnvelopeV2RequiresRecordIdentityHashAndBounds(t *testing.T) {
	t.Parallel()
	if err := goldenEnvelopeV2(t).Validate(); err != nil {
		t.Fatalf("valid version-2 envelope: %v", err)
	}
	oversize := make([]byte, billing.MaxTerminalRecordBytes+1)
	for i := range oversize {
		oversize[i] = 'x'
	}
	for name, mutate := range map[string]func(*billing.TerminalEnvelope){
		"missing record": func(e *billing.TerminalEnvelope) { e.Record = nil },
		"foreign record key": func(e *billing.TerminalEnvelope) {
			e.Record.Key = "call-foreign:bleg-1"
		},
		"empty record key": func(e *billing.TerminalEnvelope) {
			e.Record.Key = ""
		},
		"empty fingerprint": func(e *billing.TerminalEnvelope) { e.Record.Fingerprint = "" },
		"empty payload":     func(e *billing.TerminalEnvelope) { e.Record.JSON = nil },
		"invalid json":      func(e *billing.TerminalEnvelope) { e.Record.JSON = []byte("{not-json") },
		"oversize payload":  func(e *billing.TerminalEnvelope) { e.Record.JSON = oversize },
		"hash over foreign bytes": func(e *billing.TerminalEnvelope) {
			e.PayloadHash = strings.Repeat("c", 64)
		},
		"unbounded fingerprint": func(e *billing.TerminalEnvelope) {
			e.Record.Fingerprint = strings.Repeat("d", billing.MaxIdentityBytes+1)
		},
	} {
		env := goldenEnvelopeV2(t)
		mutate(&env)
		if err := env.Validate(); err == nil {
			t.Fatalf("%s: expected validation failure", name)
		}
	}
}

// TestTerminalEnvelopeVersionGatingFailsClosed locks the mixed-state rules:
// version 1 never carries a record, and the non-closure carriage label is only
// valid on version 2.
func TestTerminalEnvelopeVersionGatingFailsClosed(t *testing.T) {
	t.Parallel()
	record, _ := validRecordCarriage(t)
	env := goldenEnvelopeV1()
	env.Record = &record
	if err := env.Validate(); err == nil {
		t.Fatal("version 1 with a record payload must fail closed")
	}
	env = goldenEnvelopeV1()
	env.Outcome = billing.TerminalUnresolved
	if err := env.Validate(); err == nil {
		t.Fatal("version 1 must keep the execution-closure vocabulary")
	}
	v2 := goldenEnvelopeV2(t)
	v2.Outcome = billing.TerminalUnresolved
	if err := v2.Validate(); err != nil {
		t.Fatalf("version 2 must accept the non-closure carriage label: %v", err)
	}
	unknown := goldenEnvelopeV2(t)
	unknown.Outcome = billing.TerminalOutcome("not-a-disposition")
	if err := unknown.Validate(); err == nil {
		t.Fatal("unknown outcomes must stay rejected on version 2")
	}
}

// TestTerminalEnvelopeV2CloneDeepCopiesCarriedBytes locks the deep copy: a
// mutated clone must never change the source envelope or its payload bytes.
func TestTerminalEnvelopeV2CloneDeepCopiesCarriedBytes(t *testing.T) {
	t.Parallel()
	env := goldenEnvelopeV2(t)
	clone := env.Clone()
	if clone.Record == env.Record {
		t.Fatal("clone must carry an independent record value")
	}
	clone.Record.Key = "call-mutated:bleg-1"
	clone.Record.JSON[0] = '!'
	if env.Record.Key != "call-1:bleg-1" {
		t.Fatalf("source record key mutated to %q", env.Record.Key)
	}
	if env.Record.JSON[0] != '{' {
		t.Fatalf("source record bytes mutated: %q", env.Record.JSON)
	}
}
