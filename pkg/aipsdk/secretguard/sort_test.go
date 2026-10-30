package secretguard_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

type ordGuard struct {
	id  string
	ord int
}

func (g ordGuard) ID() string                           { return g.id }
func (g ordGuard) Order() int                           { return g.ord }
func (g ordGuard) FailureMode() secretguard.FailureMode { return secretguard.FailClosed }
func (ordGuard) Evaluate(context.Context, *aipapi.Call, secretguard.Meta, secretguard.Services) (secretguard.Decision, error) {
	return secretguard.Decision{Outcome: secretguard.OutcomePass}, nil
}

func TestMaterializeSorted_orderThenID(t *testing.T) {
	t.Parallel()
	in := []secretguard.Guard{
		ordGuard{id: "b", ord: 2},
		ordGuard{id: "a", ord: 1},
		ordGuard{id: "c", ord: 1},
	}
	got := secretguard.MaterializeSorted(in)
	if len(got) != 3 {
		t.Fatalf("len=%d", len(got))
	}
	if got[0].ID() != "a" || got[1].ID() != "c" || got[2].ID() != "b" {
		t.Fatalf("order: %q %q %q", got[0].ID(), got[1].ID(), got[2].ID())
	}
	in[0] = ordGuard{id: "mut", ord: 99}
	if got[2].ID() != "b" {
		t.Fatal("MaterializeSorted must clone")
	}
}

type ptrGuard struct {
	id  string
	ord int
}

func (g *ptrGuard) ID() string                        { return g.id }
func (g *ptrGuard) Order() int                        { return g.ord }
func (ptrGuard) FailureMode() secretguard.FailureMode { return secretguard.FailClosed }
func (ptrGuard) Evaluate(context.Context, *aipapi.Call, secretguard.Meta, secretguard.Services) (secretguard.Decision, error) {
	return secretguard.Decision{Outcome: secretguard.OutcomePass}, nil
}

func TestMaterializeSorted_skipsTypedNil(t *testing.T) {
	t.Parallel()
	var typedNil *ptrGuard
	got := secretguard.MaterializeSorted([]secretguard.Guard{
		typedNil,
		ordGuard{id: "a", ord: 1},
	})
	if len(got) != 1 {
		t.Fatalf("len=%d", len(got))
	}
	if got[0].ID() != "a" {
		t.Fatalf("got[0]=%q", got[0].ID())
	}
}
