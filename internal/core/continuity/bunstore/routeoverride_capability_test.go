package bunstore

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/routeoverride"
	"github.com/aiproxer/aiproxer/internal/core/routeoverride/storecontract"
)

func TestStoreImplementsRouteOverrideStore(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()
	if _, ok := routeoverride.AsStore(st); !ok {
		t.Fatal("continuity/bunstore.Store does not implement routeoverride.Store")
	}
	storecontract.RunAll(t, sqliteRouteOverrideContractEnv())
}

func sqliteRouteOverrideContractEnv() storecontract.ContractEnv {
	return storecontract.ContractEnv{
		New: func(t *testing.T) storecontract.ContractPair {
			t.Helper()
			s, done := newTestStore(t)
			t.Cleanup(done)
			ov, ok := routeoverride.AsStore(s)
			if !ok {
				t.Fatal("continuity/bunstore.Store does not implement routeoverride.Store")
			}
			return storecontract.ContractPair{Override: ov, Legs: s}
		},
		SeedRevision:    seedBunRevision,
		PeekLastSeenAt:  peekBunLastSeenAt,
		AdvanceClock:    advanceBunLastSeen,
		SeedStoredState: seedBunStoredState,
		Spawn:           func(fn func()) { go fn() },
	}
}

func bunStoreFromPair(t *testing.T, pair storecontract.ContractPair) *Store {
	t.Helper()
	s, ok := pair.Legs.(*Store)
	if !ok {
		t.Fatalf("Legs is %T, want *Store", pair.Legs)
	}
	return s
}

func seedBunRevision(t *testing.T, pair storecontract.ContractPair, aLegID string, revision int64) {
	t.Helper()
	seedBunStoredState(t, pair, aLegID, routeoverride.State{
		ALegID:    aLegID,
		Active:    true,
		Selector:  "seed:overflow",
		Revision:  revision,
		UpdatedAt: time.Unix(1, 0).UTC(),
	})
}

func seedBunStoredState(t *testing.T, pair storecontract.ContractPair, aLegID string, st routeoverride.State) {
	t.Helper()
	s := bunStoreFromPair(t, pair)
	active := 0
	if st.Active {
		active = 1
	}
	updated := int64(0)
	if !st.UpdatedAt.IsZero() {
		updated = st.UpdatedAt.UnixNano()
	}
	_, err := s.db.NewRaw(`
INSERT INTO a_leg_route_overrides(a_leg_id, active, selector, revision, updated_at_unix)
VALUES(?,?,?,?,?)
ON CONFLICT(a_leg_id) DO UPDATE SET
	active=excluded.active,
	selector=excluded.selector,
	revision=excluded.revision,
	updated_at_unix=excluded.updated_at_unix
`, aLegID, active, st.Selector, st.Revision, updated).Exec(context.Background())
	if err != nil {
		t.Fatalf("seed stored override: %v", err)
	}
}

func peekBunLastSeenAt(t *testing.T, pair storecontract.ContractPair, aLegID string) time.Time {
	t.Helper()
	s := bunStoreFromPair(t, pair)
	var ns int64
	err := s.db.NewRaw(`SELECT last_seen_at_unix FROM a_legs WHERE a_leg_id = ?`, aLegID).Scan(context.Background(), &ns)
	if err != nil {
		t.Fatalf("peek last_seen: %v", err)
	}
	return time.Unix(0, ns)
}

func advanceBunLastSeen(t *testing.T, pair storecontract.ContractPair, aLegID string) {
	t.Helper()
	s := bunStoreFromPair(t, pair)
	_, err := s.db.NewRaw(
		`UPDATE a_legs SET last_seen_at_unix = last_seen_at_unix - ? WHERE a_leg_id = ?`,
		int64(time.Second),
		aLegID,
	).Exec(context.Background())
	if err != nil {
		t.Fatalf("advance last_seen: %v", err)
	}
}
