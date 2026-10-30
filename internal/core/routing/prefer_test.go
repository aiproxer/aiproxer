package routing_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
)

func TestExpandFailover_preferredMovesEligibleEarlier(t *testing.T) {
	t.Parallel()
	sel, err := routing.Parse("bad:m|good:m")
	if err != nil {
		t.Fatal(err)
	}
	list, err := routing.ExpandFailover(sel, routing.PlanOptions{
		PreferredCandidateKeys: []string{"good:m"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 arms got %d", len(list))
	}
	if list[0].Key != "good:m" {
		t.Fatalf("want preference first got %q", list[0].Key)
	}
}

func TestExpandFailover_preferredIgnoredWhenUnhealthy(t *testing.T) {
	t.Parallel()
	sel, err := routing.Parse("bad:m|good:m")
	if err != nil {
		t.Fatal(err)
	}
	list, err := routing.ExpandFailover(sel, routing.PlanOptions{
		PreferredCandidateKeys: []string{"good:m"},
		Unhealthy:              map[string]struct{}{"good:m": {}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Key != "bad:m" {
		t.Fatalf("want only bad:m got %+v", list)
	}
}
