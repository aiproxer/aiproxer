package credpool_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/backends/credpool"
)

func TestErrNoUsableCredential_errorStringNeverContainsSecret(t *testing.T) {
	t.Parallel()
	secret := "sk-leak-test-UNIQUE-STRING-XYZZY-12345"
	base := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	p, err := credpool.New([]credpool.Credential{
		{Secret: secret},
	})
	if err != nil {
		t.Fatal(err)
	}
	c0, err := p.Acquire(base, nil)
	if err != nil {
		t.Fatal(err)
	}
	p.MarkAuthInvalid(c0.ID)
	_, err = p.Acquire(base, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, credpool.ErrNoUsableCredential) {
		t.Fatalf("want errors.Is ErrNoUsableCredential, got %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error must not echo API key: %q", err.Error())
	}
}

func TestSnapshot_secretSafeAndStates(t *testing.T) {
	t.Parallel()
	secret := "sk-snapshot-NEVER-SHOW-abcdef"
	base := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	until := base.Add(2 * time.Minute)
	p, err := credpool.New([]credpool.Credential{
		{Secret: secret},
		{Secret: "other-key-material"},
	})
	if err != nil {
		t.Fatal(err)
	}
	c0, err := p.Acquire(base, nil)
	if err != nil {
		t.Fatal(err)
	}
	p.MarkRateLimited(c0.ID, until)
	c1, err := p.Acquire(base, nil)
	if err != nil {
		t.Fatal(err)
	}
	p.MarkAuthInvalid(c1.ID)

	snap := p.Snapshot(base)
	if len(snap) != 2 {
		t.Fatalf("want 2 entries, got %d", len(snap))
	}
	joined := fmt.Sprintf("%+v", snap)
	if strings.Contains(joined, secret) || strings.Contains(joined, "other-key-material") {
		t.Fatalf("snapshot repr must not contain secrets: %s", joined)
	}
	if snap[0].ID != c0.ID || snap[0].State != credpool.StateCooldown {
		t.Fatalf("entry0: %+v", snap[0])
	}
	if !snap[0].CooldownUntil.Equal(until) {
		t.Fatalf("want cooldown until %v, got %v", until, snap[0].CooldownUntil)
	}
	if snap[1].ID != c1.ID || snap[1].State != credpool.StateAuthInvalid {
		t.Fatalf("entry1: %+v", snap[1])
	}
	if !snap[1].CooldownUntil.IsZero() {
		t.Fatal("auth_invalid should not set CooldownUntil")
	}
}
