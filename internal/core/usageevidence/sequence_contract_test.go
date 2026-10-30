package usageevidence

import (
	"encoding/json"
	"errors"
	"testing"
)

// Phase 1.1: sequence-aware CallLegUsageRecord contract.
//
// New records carry the authoritative positive b2bua.BLegRecord.Seq in
// AttemptSeq and participate in a v2 semantic fingerprint. Legacy v1 rows have
// no sequence (AttemptSeq == 0, durable attempt_seq NULL) and must keep the
// historical v1 fingerprint so brownfield replay never appears corrupt.

func TestCallLegUsageReplayConflictsWhenAttemptSequenceChanges(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)

	first, err := testCallLegUsageRecord(callID, "b-1").Seal()
	if err != nil {
		t.Fatal(err)
	}
	first.AttemptSeq = 1
	first, err = first.Seal()
	if err != nil {
		t.Fatal(err)
	}

	replayed, err := testCallLegUsageRecord(callID, "b-1").Seal()
	if err != nil {
		t.Fatal(err)
	}
	replayed.AttemptSeq = 2
	replayed, err = replayed.Seal()
	if err != nil {
		t.Fatal(err)
	}

	if err := CheckCallLegUsageReplay(first, replayed); !errors.Is(err, ErrReplayConflict) {
		t.Fatalf("same-key replay with changed AttemptSeq = %v, want ErrReplayConflict", err)
	}

	identical, err := testCallLegUsageRecord(callID, "b-1").Seal()
	if err != nil {
		t.Fatal(err)
	}
	identical.AttemptSeq = 1
	identical, err = identical.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckCallLegUsageReplay(first, identical); err != nil {
		t.Fatalf("identical v2 replay must be a no-op: %v", err)
	}
}

func TestCallLegUsageAttemptSequenceParticipatesInFingerprint(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)

	seq1 := testCallLegUsageRecord(callID, "b-1")
	seq1.AttemptSeq = 1
	seq2 := testCallLegUsageRecord(callID, "b-1")
	seq2.AttemptSeq = 2

	a, err := seq1.Seal()
	if err != nil {
		t.Fatal(err)
	}
	b, err := seq2.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if a.Fingerprint == b.Fingerprint {
		t.Fatal("v2 fingerprint must include AttemptSeq; seq 1 and seq 2 produced the same fingerprint")
	}
	if a.Key != b.Key {
		t.Fatalf("key = %q vs %q, want same (BillingCallID + BLegID)", a.Key, b.Key)
	}
}

func TestCallLegUsageLegacyMissingSequenceKeepsV1Fingerprint(t *testing.T) {
	t.Parallel()
	callID := BillingCallID("bc_00000000000000000000000000000000")

	legacy := testCallLegUsageRecord(callID, "b-1") // AttemptSeq zero = pre-fix/legacy row
	sealed, err := legacy.Seal()
	if err != nil {
		t.Fatalf("legacy v1 row must still seal: %v", err)
	}
	if err := CheckCallLegUsageReplay(sealed, sealed); err != nil {
		t.Fatalf("legacy v1 row must validate against its own fingerprint: %v", err)
	}
	if got, want := sealed.Fingerprint, "56877481b2f322e821e15935ca117e43e4a5ef2004a611407d62d84f01a9aa8e"; got != want {
		t.Fatalf("legacy v1 fingerprint = %s, want %s", got, want)
	}

	// A legacy row replayed with a known sequence is a different (v2) record.
	known, err := testCallLegUsageRecord(callID, "b-1").Seal()
	if err != nil {
		t.Fatal(err)
	}
	known.AttemptSeq = 1
	known, err = known.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckCallLegUsageReplay(sealed, known); !errors.Is(err, ErrReplayConflict) {
		t.Fatalf("legacy v1 vs v2 same-key replay = %v, want ErrReplayConflict", err)
	}
}

func TestCallLegUsageRejectsNegativeAttemptSequence(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	src := testCallLegUsageRecord(callID, "b-1")
	src.AttemptSeq = -1
	if _, err := src.Seal(); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("negative AttemptSeq seal = %v, want ErrInvalidRecord", err)
	}
}

func TestCallLegUsageSequencePresenceRoundTripsThroughJSON(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)

	// Legacy payload: JSON historically did not carry any sequence member.
	legacy := testCallLegUsageRecord(callID, "b-1")
	legacyJSON, err := json.Marshal(stripField(legacy, "AttemptSeq"))
	if err != nil {
		t.Fatal(err)
	}
	var decodedLegacy CallLegUsageRecord
	if err := json.Unmarshal(legacyJSON, &decodedLegacy); err != nil {
		t.Fatal(err)
	}
	if decodedLegacy.AttemptSeq != 0 {
		t.Fatalf("legacy payload decoded AttemptSeq = %d, want 0 (unknown)", decodedLegacy.AttemptSeq)
	}
	sealedLegacy, err := decodedLegacy.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckCallLegUsageReplay(sealedLegacy, sealedLegacy); err != nil {
		t.Fatalf("legacy JSON round-trip must remain v1-readable: %v", err)
	}

	// New v2 payload carries and restores the sequence explicitly.
	newRecord := testCallLegUsageRecord(callID, "b-2")
	newRecord.AttemptSeq = 3
	sealedNew, err := newRecord.Seal()
	if err != nil {
		t.Fatal(err)
	}
	newJSON, err := json.Marshal(sealedNew)
	if err != nil {
		t.Fatal(err)
	}
	var decodedNew CallLegUsageRecord
	if err := json.Unmarshal(newJSON, &decodedNew); err != nil {
		t.Fatal(err)
	}
	if decodedNew.AttemptSeq != 3 {
		t.Fatalf("new payload decoded AttemptSeq = %d, want 3", decodedNew.AttemptSeq)
	}
	if err := CheckCallLegUsageReplay(sealedNew, decodedNew); err != nil {
		t.Fatalf("v2 JSON round-trip replay: %v", err)
	}
}

// stripField marshals v as JSON without the named field, simulating a
// pre-fix payload that predates sequence persistence.
func stripField(v any, field string) any {
	return structWithoutField{v: v, field: field}
}

type structWithoutField struct {
	v     any
	field string
}

func (s structWithoutField) MarshalJSON() ([]byte, error) {
	raw, err := json.Marshal(s.v)
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	delete(m, s.field)
	return json.Marshal(m)
}
