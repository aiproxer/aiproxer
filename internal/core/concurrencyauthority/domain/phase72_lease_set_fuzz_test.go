package domain_test

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/domain"
)

// Phase 7.2: lease-set state-machine fuzz (requirements 13.3, 13.4, 13.7).

func FuzzLeaseSet_OccupiesCapacity(f *testing.F) {
	f.Add(uint8(0), int64(60), int64(0))
	f.Add(uint8(1), int64(0), int64(120))
	f.Add(uint8(2), int64(-30), int64(30))
	f.Add(uint8(3), int64(1), int64(-1))
	f.Add(uint8(4), int64(3600), int64(3600))
	f.Fuzz(func(t *testing.T, stateIdx uint8, expiresOffsetSec, nowOffsetSec int64) {
		states := []domain.LeaseSetState{
			domain.LeaseSetStateActive,
			domain.LeaseSetStateUncertain,
			domain.LeaseSetStateExpiring,
			domain.LeaseSetStateReleased,
			domain.LeaseSetStateFailed,
			domain.LeaseSetState(""),
			domain.LeaseSetState("unknown"),
		}
		base := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
		set := domain.LeaseSet{
			SetID: "fuzz-set", RequestID: "fuzz-req", Generation: 1,
			State:     states[int(stateIdx)%len(states)],
			ExpiresAt: base.Add(time.Duration(expiresOffsetSec) * time.Second),
			Members:   []domain.Lease{{LeaseID: "L1", RuleID: "r1", State: domain.LeaseStateActive}},
		}
		now := base.Add(time.Duration(nowOffsetSec) * time.Second)
		occupied := set.OccupiesCapacity(now)
		switch set.State {
		case domain.LeaseSetStateReleased, domain.LeaseSetStateFailed:
			if occupied {
				t.Fatalf("released/failed must not occupy: state=%s", set.State)
			}
		case domain.LeaseSetStateUncertain:
			if !occupied {
				t.Fatal("uncertain must occupy regardless of wall clock")
			}
		}
	})
}
