package b2bua

import (
	"errors"
	"testing"
	"time"
)

func TestNewMemoryStore_defaultNowUsesWallClock(t *testing.T) {
	t.Parallel()
	s, err := NewMemoryStore(MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	got := s.nowTime()
	after := time.Now()
	if got.Before(before.Add(-time.Second)) || got.After(after.Add(time.Second)) {
		t.Fatalf("nowTime() = %v, want between %v and %v", got, before, after)
	}
}

func TestNewMemoryStore_rejectsNegativeMaxLegs(t *testing.T) {
	t.Parallel()
	_, err := NewMemoryStore(MemoryStoreOptions{MaxLegs: -1})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrInvalidMaxLegs) {
		t.Fatalf("got %v want %v", err, ErrInvalidMaxLegs)
	}
}

func TestNewMemoryStore_acceptsPositiveMaxLegs(t *testing.T) {
	t.Parallel()
	s, err := NewMemoryStore(MemoryStoreOptions{TTL: 0, MaxLegs: 7, Now: func() time.Time { return time.Unix(1, 0).UTC() }})
	if err != nil {
		t.Fatal(err)
	}
	if s.maxLegs != 7 {
		t.Fatalf("maxLegs: got %d want 7", s.maxLegs)
	}
}

func TestNewMemoryStore_TTL_modeGetsDefaultMaxLegsCap(t *testing.T) {
	t.Parallel()
	s, err := NewMemoryStore(MemoryStoreOptions{TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if s.maxLegs != DefaultMemoryStoreMaxLegsWithoutTTL {
		t.Fatalf("maxLegs: got %d want default %d", s.maxLegs, DefaultMemoryStoreMaxLegsWithoutTTL)
	}
}
