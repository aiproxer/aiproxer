package bunstore

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
)

// LegacyInterleavedStateRowDTO is the persistence compatibility DTO used to verify
// backward-compatible serialization when legacy database rows combine cycle and memo_ref state.

func TestInterleavedState_DurableCompatibility_OldAndCurrent(t *testing.T) {
	t.Parallel()

	// 1. Legacy row containing both cycle and memo_ref.
	legacyJSON := `{"cycle":{"selector_key":"sel-1","sequence":[{"key":"k1","role":"thinker"},{"key":"k2","role":"executor"}],"next_index":1},"memo_ref":{"key":"memo-99","version":4}}`

	var legacyRow LegacyInterleavedStateRowDTO
	if err := json.Unmarshal([]byte(legacyJSON), &legacyRow); err != nil {
		t.Fatalf("unmarshal legacy row: %v", err)
	}
	if legacyRow.Cycle.SelectorKey != "sel-1" {
		t.Errorf("selector key mismatch: got %q, want sel-1", legacyRow.Cycle.SelectorKey)
	}
	if len(legacyRow.Cycle.Sequence) != 2 {
		t.Fatalf("sequence len mismatch: got %d, want 2", len(legacyRow.Cycle.Sequence))
	}
	if legacyRow.Cycle.NextIndex != 1 {
		t.Errorf("next index mismatch: got %d, want 1", legacyRow.Cycle.NextIndex)
	}
	if len(legacyRow.MemoRef) == 0 {
		t.Error("expected non-empty legacy memo_ref payload preserved in compatibility DTO")
	}

	// 2. Decode legacy JSON through adapter DecodeInterleavedStateRow - projects only cycle into core state and memo values outward.
	adapterState, memoOut, err := DecodeInterleavedStateRow(legacyJSON)
	if err != nil {
		t.Fatalf("DecodeInterleavedStateRow legacyJSON: %v", err)
	}
	if adapterState.Cycle.SelectorKey != "sel-1" || adapterState.Cycle.NextIndex != 1 {
		t.Errorf("DecodeInterleavedStateRow decoded cycle mismatch: %+v", adapterState.Cycle)
	}
	if string(memoOut) != `{"key":"memo-99","version":4}` {
		t.Errorf("DecodeInterleavedStateRow memo outward mismatch: got %s", string(memoOut))
	}

	// 3. Decode legacy JSON through core UnmarshalStateText - must succeed and load Cycle without requiring MemoRef.
	state, err := interleavedstate.UnmarshalStateText(legacyJSON)
	if err != nil {
		t.Fatalf("UnmarshalStateText legacyJSON: %v", err)
	}
	if state.Cycle.SelectorKey != "sel-1" || state.Cycle.NextIndex != 1 {
		t.Errorf("UnmarshalStateText decoded cycle mismatch: %+v", state.Cycle)
	}

	// 4. Current row without memo_ref.
	currentJSON := `{"cycle":{"selector_key":"sel-1","sequence":[{"key":"k1","role":"thinker"},{"key":"k2","role":"executor"}],"next_index":1}}`
	currentState, err := interleavedstate.UnmarshalStateText(currentJSON)
	if err != nil {
		t.Fatalf("UnmarshalStateText currentJSON: %v", err)
	}
	if !currentState.Cycle.Equal(state.Cycle) {
		t.Errorf("cycle states must match: got %+v, want %+v", currentState.Cycle, state.Cycle)
	}

	// 5. Marshal and round-trip current state via core text codec (cycle-only).
	encoded, err := interleavedstate.MarshalStateText(currentState)
	if err != nil {
		t.Fatalf("MarshalStateText: %v", err)
	}
	roundTripState, err := interleavedstate.UnmarshalStateText(encoded)
	if err != nil {
		t.Fatalf("UnmarshalStateText roundtrip: %v", err)
	}
	if !roundTripState.Cycle.Equal(currentState.Cycle) {
		t.Errorf("roundtrip mismatch: got %+v, want %+v", roundTripState.Cycle, currentState.Cycle)
	}
}

func TestInterleavedState_AdapterRoundTrip_LegacyAndCurrent(t *testing.T) {
	t.Parallel()

	// Old row: contains both cycle and memo_ref
	legacyJSON := `{"cycle":{"selector_key":"sel-1","sequence":[{"key":"k1","role":"thinker"},{"key":"k2","role":"executor"}],"next_index":1},"memo_ref":{"key":"memo-99","version":4}}`
	coreState, memoRef, err := DecodeInterleavedStateRow(legacyJSON)
	if err != nil {
		t.Fatalf("DecodeInterleavedStateRow: %v", err)
	}
	if coreState.Cycle.SelectorKey != "sel-1" || coreState.Cycle.NextIndex != 1 {
		t.Fatalf("cycle mismatch from decode: %+v", coreState.Cycle)
	}
	if len(memoRef) == 0 {
		t.Fatal("expected memoRef preserved outward from decode")
	}

	// Re-encode at adapter level (old -> current projection -> old round-trip)
	reencoded, err := EncodeInterleavedStateRow(LegacyInterleavedStateRowDTO{
		Cycle:   coreState.Cycle,
		MemoRef: memoRef,
	})
	if err != nil {
		t.Fatalf("EncodeInterleavedStateRow: %v", err)
	}
	roundTripCore, roundTripMemo, err := DecodeInterleavedStateRow(reencoded)
	if err != nil {
		t.Fatalf("DecodeInterleavedStateRow roundtrip: %v", err)
	}
	if !roundTripCore.Cycle.Equal(coreState.Cycle) {
		t.Fatalf("roundtrip cycle mismatch: got %+v, want %+v", roundTripCore.Cycle, coreState.Cycle)
	}
	if string(roundTripMemo) != string(memoRef) {
		t.Fatalf("roundtrip memo mismatch: got %s, want %s", roundTripMemo, memoRef)
	}

	// Current row: cycle only
	currentJSON := `{"cycle":{"selector_key":"sel-1","sequence":[{"key":"k1","role":"thinker"},{"key":"k2","role":"executor"}],"next_index":1}}`
	curCore, curMemo, err := DecodeInterleavedStateRow(currentJSON)
	if err != nil {
		t.Fatalf("Decode current: %v", err)
	}
	if len(curMemo) != 0 {
		t.Fatalf("expected empty memo for current row, got %s", curMemo)
	}
	curEncoded, err := EncodeInterleavedStateRow(LegacyInterleavedStateRowDTO{
		Cycle: curCore.Cycle,
	})
	if err != nil {
		t.Fatalf("Encode current: %v", err)
	}
	curDecoded, _, err := DecodeInterleavedStateRow(curEncoded)
	if err != nil {
		t.Fatalf("Decode curEncoded: %v", err)
	}
	if !curDecoded.Cycle.Equal(curCore.Cycle) {
		t.Fatalf("current roundtrip cycle mismatch: got %+v, want %+v", curDecoded.Cycle, curCore.Cycle)
	}
}

func TestInterleavedState_CoreStateIsCycleOnly(t *testing.T) {
	t.Parallel()

	legacyJSON := `{"cycle":{"selector_key":"sel-1","sequence":[{"key":"k1","role":"thinker"},{"key":"k2","role":"executor"}],"next_index":1},"memo_ref":{"key":"memo-99","version":4}}`
	state, err := interleavedstate.UnmarshalStateText(legacyJSON)
	if err != nil {
		t.Fatalf("UnmarshalStateText: %v", err)
	}
	coreEncoded, err := interleavedstate.MarshalStateText(state)
	if err != nil {
		t.Fatalf("MarshalStateText: %v", err)
	}
	wantCoreJSON := `{"cycle":{"selector_key":"sel-1","sequence":[{"key":"k1","role":"thinker"},{"key":"k2","role":"executor"}],"next_index":1}}`
	if coreEncoded != wantCoreJSON {
		t.Fatalf("core state serialization must be cycle-only:\n got: %s\nwant: %s", coreEncoded, wantCoreJSON)
	}
}

func TestStore_InterleavedState_DurableMemoPreservationRoundTrip(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()

	leg, err := st.CreateALeg(ctx, "memo-preservation-key")
	if err != nil {
		t.Fatalf("CreateALeg: %v", err)
	}

	legacyJSON := `{"cycle":{"selector_key":"sel-1","sequence":[{"key":"k1","role":"thinker"},{"key":"k2","role":"executor"}],"next_index":0},"memo_ref":{"key":"memo-99","version":4}}`
	if _, err := st.db.NewRaw(`UPDATE a_legs SET interleaved_state_json = ? WHERE a_leg_id = ?`, legacyJSON, leg.ALegID).Exec(ctx); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	// 1. Read through real Store - compatibility DTO row must surface memo_ref
	row, err := st.FetchInterleavedRow(ctx, leg.ALegID)
	if err != nil {
		t.Fatalf("FetchInterleavedRow: %v", err)
	}
	if string(row.MemoRef) != `{"key":"memo-99","version":4}` {
		t.Fatalf("initial read: expected memo_ref preserved, got %s", string(row.MemoRef))
	}

	// 2. Update cycle via SetInterleavedState (updates cycle cursor from index 0 to 1)
	updatedCycle := interleavedstate.State{
		Cycle: interleavedstate.CycleState{
			SelectorKey: "sel-1",
			Sequence: []interleavedstate.CycleEntry{
				{Key: "k1", Role: interleavedstate.RoleThinker},
				{Key: "k2", Role: interleavedstate.RoleExecutor},
			},
			NextIndex: 1,
		},
	}
	if err := st.SetInterleavedState(ctx, leg.ALegID, updatedCycle); err != nil {
		t.Fatalf("SetInterleavedState: %v", err)
	}

	// 3. Read back through real Store - memo_ref must remain INTACT with byte/field equality
	afterRow, err := st.FetchInterleavedRow(ctx, leg.ALegID)
	if err != nil {
		t.Fatalf("FetchInterleavedRow after update: %v", err)
	}
	if string(afterRow.MemoRef) != `{"key":"memo-99","version":4}` {
		t.Fatalf("memo_ref was NOT preserved after SetInterleavedState: got %q, want %q", string(afterRow.MemoRef), `{"key":"memo-99","version":4}`)
	}
	if afterRow.Cycle.NextIndex != 1 {
		t.Fatalf("cycle NextIndex was not updated: got %d, want 1", afterRow.Cycle.NextIndex)
	}

	// 4. Also verify via FetchInterleavedStateRow
	stateOut, memoOut, err := st.FetchInterleavedStateRow(ctx, leg.ALegID)
	if err != nil {
		t.Fatalf("FetchInterleavedStateRow: %v", err)
	}
	if string(memoOut) != `{"key":"memo-99","version":4}` {
		t.Fatalf("FetchInterleavedStateRow memo mismatch: got %s", string(memoOut))
	}
	if stateOut.Cycle.NextIndex != 1 {
		t.Fatalf("FetchInterleavedStateRow cycle mismatch: got %d", stateOut.Cycle.NextIndex)
	}

	// 5. Also assert at raw SQL level that the durable JSON retains memo_ref
	var dbRaw string
	if err := st.db.NewRaw(`SELECT interleaved_state_json FROM a_legs WHERE a_leg_id = ?`, leg.ALegID).Scan(ctx, &dbRaw); err != nil {
		t.Fatalf("raw select: %v", err)
	}
	var directDTO LegacyInterleavedStateRowDTO
	if err := json.Unmarshal([]byte(dbRaw), &directDTO); err != nil {
		t.Fatalf("unmarshal raw db row: %v", err)
	}
	if string(directDTO.MemoRef) != `{"key":"memo-99","version":4}` {
		t.Fatalf("raw DB memo_ref mismatch: got %s, want %s", string(directDTO.MemoRef), `{"key":"memo-99","version":4}`)
	}
}
