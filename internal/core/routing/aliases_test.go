package routing

import (
	"errors"
	"strings"
	"testing"
)

func TestAliasResolver_exactMatch(t *testing.T) {
	t.Parallel()
	r, err := NewAliasResolver([]ModelAliasRule{
		{Pattern: `^gpt-4$`, Replacement: "openai-responses:gpt-4o-mini"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := r.Resolve("gpt-4")
	if got != "openai-responses:gpt-4o-mini" {
		t.Fatalf("got %q", got)
	}
}

func TestAliasResolver_aliasPrefixWhenConfigured(t *testing.T) {
	t.Parallel()
	r, err := NewAliasResolver([]ModelAliasRule{
		{Pattern: `^alias:foo$`, Replacement: "stub:bar"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Resolve("alias:foo"); got != "stub:bar" {
		t.Fatalf("got %q", got)
	}
}

func TestAliasResolver_noMatchPassthrough(t *testing.T) {
	t.Parallel()
	r, err := NewAliasResolver([]ModelAliasRule{
		{Pattern: `^other$`, Replacement: "stub:x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Resolve("keep-me"); got != "keep-me" {
		t.Fatalf("got %q", got)
	}
}

func TestAliasResolver_firstMatchWins(t *testing.T) {
	t.Parallel()
	r, err := NewAliasResolver([]ModelAliasRule{
		{Pattern: `^uni.*`, Replacement: "stub:first"},
		{Pattern: `^uni.*`, Replacement: "stub:second"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Resolve("unified"); got != "stub:first" {
		t.Fatalf("got %q", got)
	}
}

func TestAliasResolver_captureReplacement(t *testing.T) {
	t.Parallel()
	r, err := NewAliasResolver([]ModelAliasRule{
		{Pattern: `^pre-(.+)$`, Replacement: `stub:${1}`},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Resolve("pre-abc"); got != "stub:abc" {
		t.Fatalf("got %q", got)
	}
}

func TestAliasResolver_captureReplacement_invalidAfterExpansion_parseFails(t *testing.T) {
	t.Parallel()
	r, err := NewAliasResolver([]ModelAliasRule{
		{Pattern: `^x:(.*)$`, Replacement: `stub:$1`},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := r.Resolve("x:")
	if got != "stub:" {
		t.Fatalf("Resolve: got %q want stub:", got)
	}
	_, perr := Parse(got)
	if perr == nil {
		t.Fatal("Parse: expected error for post-rewrite selector")
	}
	if !errors.Is(perr, ErrInvalidSelector) {
		t.Fatalf("Parse: want ErrInvalidSelector, got %v", perr)
	}
}

func TestAliasResolver_trimsInput(t *testing.T) {
	t.Parallel()
	r, err := NewAliasResolver([]ModelAliasRule{
		{Pattern: `^x$`, Replacement: "stub:y"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Resolve("  x  "); got != "stub:y" {
		t.Fatalf("got %q", got)
	}
}

func TestValidateModelAliases_invalidRegexp(t *testing.T) {
	t.Parallel()
	err := ValidateModelAliases([]ModelAliasRule{
		{Pattern: `(`, Replacement: "stub:x"},
	})
	if err == nil || !strings.Contains(err.Error(), "pattern") {
		t.Fatalf("want pattern error, got %v", err)
	}
}

func TestValidateModelAliases_invalidReplacementSelector(t *testing.T) {
	t.Parallel()
	err := ValidateModelAliases([]ModelAliasRule{
		{Pattern: `^x$`, Replacement: "|bad"},
	})
	if err == nil || !strings.Contains(err.Error(), "replacement") {
		t.Fatalf("want replacement error, got %v", err)
	}
}

func TestValidateModelAliases_emptyPattern(t *testing.T) {
	t.Parallel()
	err := ValidateModelAliases([]ModelAliasRule{
		{Pattern: "  ", Replacement: "stub:x"},
	})
	if err == nil || !strings.Contains(err.Error(), "empty pattern") {
		t.Fatalf("got %v", err)
	}
}

func TestValidateModelAliases_emptyReplacement(t *testing.T) {
	t.Parallel()
	err := ValidateModelAliases([]ModelAliasRule{
		{Pattern: `^x$`, Replacement: "  "},
	})
	if err == nil || !strings.Contains(err.Error(), "empty replacement") {
		t.Fatalf("got %v", err)
	}
}

func TestNewAliasResolver_nilRules(t *testing.T) {
	t.Parallel()
	r, err := NewAliasResolver(nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Resolve("  z  ") != "z" {
		t.Fatal("expected trim passthrough")
	}
}
