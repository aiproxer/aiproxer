package routing_test

import (
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestCompileSelector_acceptedForms(t *testing.T) {
	t.Parallel()
	aliases, err := routing.NewAliasResolver([]routing.ModelAliasRule{
		{Pattern: `^cheap$`, Replacement: "openai:gpt-4"},
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name           string
		raw            string
		defaultBackend string
		aliases        *routing.AliasResolver
	}{
		{name: "direct", raw: "openai:gpt-4"},
		{name: "modelOnly", raw: "gpt-4", defaultBackend: "openai"},
		{name: "alias", raw: "cheap", aliases: aliases},
		{name: "failover", raw: "a:m|b:m"},
		{name: "weighted", raw: "[weight=1]a:m^[weight=1]b:m"},
		{name: "race", raw: "a:m!b:m"},
		{name: "ttft", raw: "{ttft_timeout=60}openai:gpt-4"},
		{name: "affinity", raw: "{affinity=session}backendA:model-x"},
		{name: "first", raw: "[first]cheap:m^[weight=100]expensive:m"},
		{name: "thinker", raw: "[thinker]a:m^b:m"},
		{name: "handicap", raw: "a:m!b:m[handicap=200]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sel, err := routing.CompileSelector(tc.raw, tc.aliases, tc.defaultBackend)
			if err != nil {
				t.Fatalf("CompileSelector(%q): %v", tc.raw, err)
			}
			if sel == nil {
				t.Fatal("expected compiled selector")
			}
			if routing.SelectorHasEmptyBackend(sel) {
				t.Fatalf("compiled selector still has empty backend: %+v", sel)
			}
		})
	}
}

func TestCompileSelector_malformedAndUnresolved(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name           string
		raw            string
		defaultBackend string
		want           error
	}{
		{name: "empty", raw: "  ", want: routing.ErrInvalidSelector},
		{name: "trailingFailover", raw: "a:m|", want: routing.ErrInvalidSelector},
		{name: "unclosedGlobal", raw: "{ttft_timeout=60", want: routing.ErrInvalidSelector},
		{name: "unresolvedModelOnly", raw: "gpt-4", want: aipapi.ErrUnresolvedModelOnlySelector},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := routing.CompileSelector(tc.raw, nil, tc.defaultBackend)
			if err == nil {
				t.Fatal("expected error")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
		})
	}
}

func TestRejectUnknownBackends(t *testing.T) {
	t.Parallel()
	known := map[string]struct{}{"openai": {}, "backup": {}}
	cases := []struct {
		name    string
		raw     string
		known   map[string]struct{}
		wantErr error
	}{
		{name: "knownDirect", raw: "openai:gpt-4", known: known},
		{name: "knownFailover", raw: "openai:m|backup:m", known: known},
		{name: "nilKnownSkips", raw: "typo-backend:model", known: nil},
		{name: "unknownDirect", raw: "typo-backend:model", known: known, wantErr: routing.ErrUnknownBackend},
		{name: "unknownFailoverArm", raw: "openai:m|typo-backend:m", known: known, wantErr: routing.ErrUnknownBackend},
		{name: "emptyKnownRejects", raw: "openai:gpt-4", known: map[string]struct{}{}, wantErr: routing.ErrUnknownBackend},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sel, err := routing.CompileSelector(tc.raw, nil, "openai")
			if err != nil {
				t.Fatalf("CompileSelector: %v", err)
			}
			err = routing.RejectUnknownBackends(sel, tc.known)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("RejectUnknownBackends: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("got %v want %v", err, tc.wantErr)
			}
		})
	}
}

func TestCompileSelector_matchesAliasParseDefaultOracle(t *testing.T) {
	t.Parallel()
	aliases, err := routing.NewAliasResolver([]routing.ModelAliasRule{
		{Pattern: `^alias$`, Replacement: "backendB:model-x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := "alias"
	got, err := routing.CompileSelector(raw, aliases, "backendA")
	if err != nil {
		t.Fatalf("CompileSelector: %v", err)
	}
	wantStr := aliases.Resolve(raw)
	want, err := routing.Parse(wantStr)
	if err != nil {
		t.Fatal(err)
	}
	routing.ApplyModelOnlyBackends(want, "backendA")
	if routing.SelectorHasEmptyBackend(want) {
		t.Fatal("oracle unresolved")
	}
	if primaryBackend(got) != primaryBackend(want) {
		t.Fatalf("shared helper drifted from alias/parse/default oracle: got backend %q want %q", primaryBackend(got), primaryBackend(want))
	}
}

func primaryBackend(sel *routing.Selector) string {
	if sel == nil || len(sel.Alternatives) == 0 || sel.Alternatives[0].Primary == nil {
		return ""
	}
	return sel.Alternatives[0].Primary.Backend
}
