package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/b2bualineage"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
)

func TestManager_Quarantine_andAssertActive_signatures(t *testing.T) {
	t.Parallel()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	st := memory.New(memory.Options{SimulateDurable: true})
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 9)
	}
	m, err := app.NewManager(st, app.NewRandGenerator(key), b2bualineage.New(b2), app.ManagerConfig{
		FingerprintKey: key,
		StoreDurable:   true,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	owner := domain.PrincipalRef{ID: "u"}
	got, err := m.BeginTurn(ctx, app.BeginInput{
		Now:       time.Unix(1, 0).UTC(),
		Principal: owner,
		Session:   app.SessionWire{},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := m.AssertActive(ctx, got.Record.SessionID); err != nil {
		t.Fatalf("AssertActive on active session: %v", err)
	}

	err = m.Quarantine(ctx, domain.QuarantineInput{
		SessionID:  got.Record.SessionID,
		TurnID:     "turn-1",
		ReasonCode: "secret_guard_block",
		EventID:    "evt-1",
		At:         time.Unix(2, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("Quarantine: %v", err)
	}

	if err := m.AssertActive(ctx, got.Record.SessionID); !errors.Is(err, domain.ErrSessionQuarantined) {
		t.Fatalf("AssertActive after quarantine: got %v want ErrSessionQuarantined", err)
	}

	_, err = m.BeginTurn(ctx, app.BeginInput{
		Now:       time.Unix(3, 0).UTC(),
		Principal: owner,
		Session: app.SessionWire{
			SessionID:   got.Response.SessionID,
			ResumeToken: string(got.Response.ResumeToken),
		},
	})
	if !errors.Is(err, domain.ErrSessionQuarantined) {
		t.Fatalf("BeginTurn resume after quarantine: got %v want ErrSessionQuarantined", err)
	}
}

func TestManager_Quarantine_rejectsInvalidInput(t *testing.T) {
	t.Parallel()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	st := memory.New(memory.Options{SimulateDurable: true})
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 9)
	}
	m, err := app.NewManager(st, app.NewRandGenerator(key), b2bualineage.New(b2), app.ManagerConfig{
		FingerprintKey: key,
		StoreDurable:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Quarantine(context.Background(), domain.QuarantineInput{}); !errors.Is(err, domain.ErrInvalidQuarantineInput) {
		t.Fatalf("Quarantine invalid input = %v want ErrInvalidQuarantineInput", err)
	}
}

func TestStore_includesQuarantineMethod(t *testing.T) {
	t.Parallel()
	// Compile-time: app.Store requires Quarantine.
	var s app.Store = memory.New(memory.Options{})
	_ = s.Quarantine
}
