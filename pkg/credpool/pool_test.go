package credpool_test

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/backends/credpool"
)

func TestNew_rejectsEmptySecret(t *testing.T) {
	t.Parallel()
	_, err := credpool.New([]credpool.Credential{{ID: "x", Secret: ""}})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestNew_rejectsEmptyCredentialList(t *testing.T) {
	t.Parallel()
	_, err := credpool.New(nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestNew_preservesConfiguredCredentialIDs(t *testing.T) {
	t.Parallel()
	p, err := credpool.New([]credpool.Credential{
		{ID: "primary", Secret: "a"},
		{ID: "secondary", Secret: "b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Acquire(time.Unix(1, 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "primary" || got.Secret != "a" {
		t.Fatalf("credential: %+v", got)
	}
	p.MarkAuthInvalid("primary")
	got, err = p.Acquire(time.Unix(1, 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "secondary" {
		t.Fatalf("credential id: got %q want secondary", got.ID)
	}
}

func TestNew_rejectsDuplicateConfiguredCredentialIDs(t *testing.T) {
	t.Parallel()
	_, err := credpool.New([]credpool.Credential{
		{ID: "dup", Secret: "a"},
		{ID: "dup", Secret: "b"},
	})
	if err == nil {
		t.Fatal("expected duplicate id error")
	}
}

func TestAcquire_orderedSkipsCooldownAndAuthInvalid(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	p, err := credpool.New([]credpool.Credential{
		{Secret: "alpha"},
		{Secret: "beta"},
		{Secret: "gamma"},
	})
	if err != nil {
		t.Fatal(err)
	}
	s0, err := p.Acquire(base, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s0.Secret != "alpha" {
		t.Fatalf("first acquire want alpha secret, got %q", s0.Secret)
	}
	p.MarkRateLimited(s0.ID, base.Add(30*time.Second))
	s1, err := p.Acquire(base, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s1.Secret != "beta" {
		t.Fatalf("after c0 cooldown want beta, got %q", s1.Secret)
	}
	p.MarkAuthInvalid(s1.ID)
	s2, err := p.Acquire(base, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s2.Secret != "gamma" {
		t.Fatalf("after c1 invalid want gamma, got %q", s2.Secret)
	}
	p.MarkAuthInvalid(s2.ID)
	// c0 cooldown, c1+c2 invalid
	_, err = p.Acquire(base, nil)
	if !errors.Is(err, credpool.ErrNoUsableCredential) {
		t.Fatalf("expected exhaustion, got %v", err)
	}
	// advance: c0 usable again first in order; c1 and c2 stay invalid
	after := base.Add(31 * time.Second)
	sx, err := p.Acquire(after, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sx.Secret != "alpha" {
		t.Fatalf("after cooldown expiry want alpha first, got %q", sx.Secret)
	}
}

func TestAcquire_excludeSkipsCredential(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	p, err := credpool.New([]credpool.Credential{
		{Secret: "a"},
		{Secret: "b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := p.Acquire(base, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.Acquire(base, map[string]struct{}{first.ID: {}})
	if err != nil {
		t.Fatal(err)
	}
	if second.Secret != "b" {
		t.Fatalf("want b with first excluded, got %q", second.Secret)
	}
}

func TestAcquire_poolLocalIDsNotSecrets(t *testing.T) {
	t.Parallel()
	secretA := "sk-test-unique-alpha-12345"
	secretB := "sk-test-unique-beta-67890"
	p, err := credpool.New([]credpool.Credential{
		{Secret: secretA},
		{Secret: secretB},
	})
	if err != nil {
		t.Fatal(err)
	}
	ca, err := p.Acquire(time.Unix(0, 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	cb, err := p.Acquire(time.Unix(0, 0), map[string]struct{}{ca.ID: {}})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{ca.ID, cb.ID} {
		if id == "" {
			t.Fatal("empty id")
		}
		if strings.Contains(id, secretA) || strings.Contains(id, secretB) {
			t.Fatalf("id %q must not embed secret material", id)
		}
	}
	if ca.ID == cb.ID {
		t.Fatal("ids must differ")
	}
}

func TestAcquire_exhaustionWhenNoneUsable(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	p, err := credpool.New([]credpool.Credential{
		{Secret: "x"},
		{Secret: "y"},
	})
	if err != nil {
		t.Fatal(err)
	}
	c0, _ := p.Acquire(base, nil)
	p.MarkAuthInvalid(c0.ID)
	c1, errA := p.Acquire(base, nil)
	if errA != nil {
		t.Fatal(errA)
	}
	p.MarkAuthInvalid(c1.ID)
	_, err = p.Acquire(base, nil)
	if !errors.Is(err, credpool.ErrNoUsableCredential) {
		t.Fatalf("want ErrNoUsableCredential, got %v", err)
	}
}

func TestAcquire_concurrentAcquireAndMark(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	p, err := credpool.New([]credpool.Credential{
		{Secret: "k0"}, {Secret: "k1"}, {Secret: "k2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			for j := range 50 {
				c, err := p.Acquire(base, nil)
				if err != nil {
					continue
				}
				switch j % 3 {
				case 0:
					p.MarkRateLimited(c.ID, base.Add(time.Second))
				case 1:
					p.MarkAuthInvalid(c.ID)
				}
				_ = c
			}
		})
	}
	wg.Wait()
}

func TestAcquireByID_usableCredential(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	p, err := credpool.New([]credpool.Credential{
		{ID: "alpha", Secret: "sk-a"},
		{ID: "beta", Secret: "sk-b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.AcquireByID(base, "beta")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "beta" || got.Secret != "sk-b" {
		t.Fatalf("credential: %+v", got)
	}
}

func TestAcquireByID_authInvalidReturnsErrNoUsableCredential(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	p, err := credpool.New([]credpool.Credential{{ID: "x", Secret: "sk-x"}})
	if err != nil {
		t.Fatal(err)
	}
	p.MarkAuthInvalid("x")
	_, err = p.AcquireByID(base, "x")
	if !errors.Is(err, credpool.ErrNoUsableCredential) {
		t.Fatalf("want ErrNoUsableCredential, got %v", err)
	}
}

func TestAcquireByID_cooldownReturnsErrNoUsableCredential(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	p, err := credpool.New([]credpool.Credential{{ID: "x", Secret: "sk-x"}})
	if err != nil {
		t.Fatal(err)
	}
	p.MarkRateLimited("x", base.Add(time.Minute))
	_, err = p.AcquireByID(base, "x")
	if !errors.Is(err, credpool.ErrNoUsableCredential) {
		t.Fatalf("want ErrNoUsableCredential, got %v", err)
	}
}

func TestAcquireByID_missingIDReturnsErrNoUsableCredential(t *testing.T) {
	t.Parallel()
	p, err := credpool.New([]credpool.Credential{{ID: "x", Secret: "sk-x"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.AcquireByID(time.Unix(0, 0), "missing")
	if !errors.Is(err, credpool.ErrNoUsableCredential) {
		t.Fatalf("want ErrNoUsableCredential, got %v", err)
	}
}

func TestMarkRateLimited_keepsLaterCooldownDeadline(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	p, err := credpool.New([]credpool.Credential{{Secret: "a"}})
	if err != nil {
		t.Fatal(err)
	}
	c0, err := p.Acquire(base, nil)
	if err != nil {
		t.Fatal(err)
	}
	long := base.Add(10 * time.Minute)
	short := base.Add(30 * time.Second)
	p.MarkRateLimited(c0.ID, long)
	p.MarkRateLimited(c0.ID, short)
	snap := p.Snapshot(base)
	if len(snap) != 1 {
		t.Fatalf("snap: %#v", snap)
	}
	if !snap[0].CooldownUntil.Equal(long) {
		t.Fatalf("expected longer cooldown preserved, got %v want %v", snap[0].CooldownUntil, long)
	}
}
