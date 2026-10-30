package routing_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
)

func TestRejectPlaceholderModel_refusesPlaceholderLeaf(t *testing.T) {
	t.Parallel()
	err := routing.RejectPlaceholderModel("claude-acp:model")
	if err == nil {
		t.Fatal("expected a refusal for the placeholder model")
	}
	if !strings.Contains(err.Error(), "placeholder") {
		t.Fatalf("error: %v", err)
	}
}

// TestRejectPlaceholderModel_refusesNestedParallelLeaf proves the check
// reaches a leaf nested in a parallel group embedded in a weighted branch: the
// thinker hybrid form is the only selector shape that carries one.
func TestRejectPlaceholderModel_refusesNestedParallelLeaf(t *testing.T) {
	t.Parallel()
	const route = "[thinker]thinker:tm^claude-acp:model!other:m"
	if _, err := routing.Parse(route); err != nil {
		t.Fatalf("the fixture must parse: %v", err)
	}
	err := routing.RejectPlaceholderModel(route)
	if err == nil {
		t.Fatal("expected a refusal for the placeholder model in a nested parallel branch")
	}
	if !strings.Contains(err.Error(), "placeholder") {
		t.Fatalf("error: %v", err)
	}
}

func TestRejectPlaceholderModel_acceptsRealModel(t *testing.T) {
	t.Parallel()
	if err := routing.RejectPlaceholderModel("claude-acp:anthropic/claude-sonnet-5.5"); err != nil {
		t.Fatalf("a real model must be accepted: %v", err)
	}
}

// TestCompileSelector_emptySelectorStaysEmpty proves a catch-all alias rule
// cannot turn an absent selector into a route: an absent route stays absent.
func TestCompileSelector_emptySelectorStaysEmpty(t *testing.T) {
	t.Parallel()
	aliases, err := routing.NewAliasResolver([]routing.ModelAliasRule{
		{Pattern: `^.*$`, Replacement: "claude-acp:anthropic/claude-sonnet-5.5"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = routing.CompileSelector("", aliases, "")
	if !errors.Is(err, routing.ErrNoRouteSelector) {
		t.Fatalf("got %v want ErrNoRouteSelector", err)
	}
}

// TestCompileSelector_aliasResolvesRealSelector proves the alias still applies
// to a selector that exists.
func TestCompileSelector_aliasResolvesRealSelector(t *testing.T) {
	t.Parallel()
	aliases, err := routing.NewAliasResolver([]routing.ModelAliasRule{
		{Pattern: `^cheap$`, Replacement: "openai-responses:gpt-4o-mini"},
	})
	if err != nil {
		t.Fatal(err)
	}
	sel, err := routing.CompileSelector("cheap", aliases, "")
	if err != nil {
		t.Fatalf("CompileSelector: %v", err)
	}
	if len(sel.Alternatives) != 1 || sel.Alternatives[0].Primary == nil {
		t.Fatalf("selector: %+v", sel)
	}
	if got := sel.Alternatives[0].Primary.Model; got != "gpt-4o-mini" {
		t.Fatalf("model = %q want gpt-4o-mini", got)
	}
}
