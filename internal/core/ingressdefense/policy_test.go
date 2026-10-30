package ingressdefense

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// validPolicy returns a policy that satisfies every domain invariant so each
// test can vary exactly one value.
func validPolicy() Policy {
	return Policy{
		Enabled:           true,
		AuthFailures:      5,
		FailureWindow:     time.Minute,
		InitialQuarantine: time.Minute,
		MaxQuarantine:     2 * time.Hour,
	}
}

func validStateLimits() StateLimits {
	return StateLimits{MaxEntries: 100000, StateTTL: 24 * time.Hour}
}

func TestPolicyValidateAcceptsCompiledValues(t *testing.T) {
	t.Parallel()

	if err := validPolicy().Validate(); err != nil {
		t.Fatalf("valid policy rejected: %v", err)
	}
}

func TestPolicyValidateRejectsBrokenBackoffInvariants(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		mutate func(*Policy)
		want   string
	}{
		{name: "zero auth failures", mutate: func(p *Policy) { p.AuthFailures = 0 }, want: "auth_failures"},
		{name: "negative auth failures", mutate: func(p *Policy) { p.AuthFailures = -1 }, want: "auth_failures"},
		{name: "zero failure window", mutate: func(p *Policy) { p.FailureWindow = 0 }, want: "window"},
		{name: "negative failure window", mutate: func(p *Policy) { p.FailureWindow = -time.Second }, want: "window"},
		{name: "zero initial quarantine", mutate: func(p *Policy) { p.InitialQuarantine = 0 }, want: "initial_quarantine"},
		{name: "zero max quarantine", mutate: func(p *Policy) { p.MaxQuarantine = 0 }, want: "max_quarantine"},
		{name: "max below initial", mutate: func(p *Policy) { p.MaxQuarantine = 30 * time.Second }, want: "max_quarantine"},
		{name: "invalid exempt prefix", mutate: func(p *Policy) {
			p.AdaptiveExemptCIDRs = []netip.Prefix{{}}
		}, want: "exempt_cidrs"},
		{name: "unmasked exempt prefix", mutate: func(p *Policy) {
			p.AdaptiveExemptCIDRs = []netip.Prefix{netip.PrefixFrom(netip.MustParseAddr("192.0.2.5"), 24)}
		}, want: "exempt_cidrs"},
		{name: "mapped exempt prefix", mutate: func(p *Policy) {
			p.AdaptiveExemptCIDRs = []netip.Prefix{netip.PrefixFrom(netip.MustParseAddr("::ffff:192.0.2.0"), 120)}
		}, want: "exempt_cidrs"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			policy := validPolicy()
			tc.mutate(&policy)
			err := policy.Validate()
			if err == nil {
				t.Fatal("Validate succeeded, want domain invariant error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q must name %q", err, tc.want)
			}
		})
	}
}

func TestStateLimitsValidateRejectsUnboundedValues(t *testing.T) {
	t.Parallel()

	if err := validStateLimits().Validate(); err != nil {
		t.Fatalf("valid state limits rejected: %v", err)
	}
	for _, tc := range []struct {
		name  string
		limit StateLimits
		want  string
	}{
		{name: "zero capacity", limit: StateLimits{StateTTL: time.Hour}, want: "max_entries"},
		{name: "negative capacity", limit: StateLimits{MaxEntries: -1, StateTTL: time.Hour}, want: "max_entries"},
		{name: "zero ttl", limit: StateLimits{MaxEntries: 10}, want: "state_ttl"},
		{name: "negative ttl", limit: StateLimits{MaxEntries: 10, StateTTL: -time.Hour}, want: "state_ttl"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if err := tc.limit.Validate(); err == nil {
				t.Fatal("Validate succeeded, want domain invariant error")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q must name %q", err, tc.want)
			}
		})
	}
}

func TestPolicyAdaptiveExemptMatchesAllowlistOnly(t *testing.T) {
	t.Parallel()

	policy := validPolicy()
	policy.AdaptiveExemptCIDRs = []netip.Prefix{
		netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("2001:db8::/32"),
	}
	for _, tc := range []struct {
		addr string
		want bool
	}{
		{addr: "192.0.2.7", want: true},
		{addr: "::ffff:192.0.2.7", want: true},
		{addr: "2001:db8:1234::1", want: true},
		{addr: "198.51.100.9", want: false},
		{addr: "2001:db9::1", want: false},
	} {
		addr := netip.MustParseAddr(tc.addr)
		if got := policy.AdaptiveExempt(addr); got != tc.want {
			t.Errorf("AdaptiveExempt(%s) = %v, want %v", tc.addr, got, tc.want)
		}
	}
	if policy.AdaptiveExempt(netip.Addr{}) {
		t.Error("invalid address must never be exempt")
	}
	empty := validPolicy()
	if empty.AdaptiveExempt(netip.MustParseAddr("192.0.2.7")) {
		t.Error("empty allowlist must exempt nothing")
	}
}

func TestReasonSetIsClosedAndBounded(t *testing.T) {
	t.Parallel()

	want := []Reason{
		ReasonImpossiblePath,
		ReasonAuthFailureThreshold,
		ReasonActiveQuarantine,
		ReasonClientIPError,
	}
	wantStrings := []string{
		"impossible_path",
		"auth_failure_threshold",
		"active_quarantine",
		"client_ip_error",
	}
	got := AllReasons()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AllReasons() = %v, want %v", got, want)
	}
	if len(got) != len(wantStrings) {
		t.Fatalf("reason cardinality = %d, want %d", len(got), len(wantStrings))
	}
	seen := make(map[Reason]bool, len(got))
	for i, reason := range got {
		if reason == "" {
			t.Fatalf("AllReasons()[%d] is empty", i)
		}
		if string(reason) != wantStrings[i] {
			t.Errorf("AllReasons()[%d] = %q, want %q", i, reason, wantStrings[i])
		}
		if seen[reason] {
			t.Errorf("duplicate reason %q", reason)
		}
		seen[reason] = true
	}
}

func TestReasonConstantSetIsClosed(t *testing.T) {
	t.Parallel()

	want := map[string]string{
		"ReasonImpossiblePath":       "impossible_path",
		"ReasonAuthFailureThreshold": "auth_failure_threshold",
		"ReasonActiveQuarantine":     "active_quarantine",
		"ReasonClientIPError":        "client_ip_error",
	}
	found := declaredReasonConstants(t, ".")
	if len(found) != len(want) {
		t.Fatalf("declared reason constants = %d %v, want exactly %d",
			len(found), slices.Sorted(maps.Keys(found)), len(want))
	}
	for name, value := range want {
		got, ok := found[name]
		if !ok {
			t.Errorf("missing reason constant %s", name)
			continue
		}
		if got != value {
			t.Errorf("%s = %q, want %q", name, got, value)
		}
	}
	for name, value := range found {
		if value == "" {
			t.Errorf("%s must be a plain literal reason string; an untyped or derived reason cannot extend the closed set", name)
		}
	}
}

func TestReasonConstantGateRejectsUntypedAndAliasedReasons(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		src        string
		wantNames  int
		probe      string
		probeValue string
	}{
		{
			name:       "untyped literal constant",
			src:        "package p\n\nconst ReasonSneaky = \"sneaky\"\n",
			wantNames:  1,
			probe:      "ReasonSneaky",
			probeValue: "sneaky",
		},
		{
			name:       "alias of an admitted reason",
			src:        "package p\n\ntype Reason string\n\nconst ReasonAlias = ReasonImpossiblePath\n",
			wantNames:  1,
			probe:      "ReasonAlias",
			probeValue: "",
		},
		{
			name:       "untyped constant inside a block",
			src:        "package p\n\nconst (\n\tReasonOne = \"one\"\n\tReasonTwo = \"two\"\n)\n",
			wantNames:  2,
			probe:      "ReasonTwo",
			probeValue: "two",
		},
		{
			name:       "derived expression value",
			src:        "package p\n\nconst ReasonConcat = \"im\" + \"possible_path\"\n",
			wantNames:  1,
			probe:      "ReasonConcat",
			probeValue: "",
		},
		{
			name:       "repeated name without its own value",
			src:        "package p\n\nconst (\n\tReasonOmitted = \"omitted\"\n\tReasonOmitted\n)\n",
			wantNames:  1,
			probe:      "ReasonOmitted",
			probeValue: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			file, err := parser.ParseFile(token.NewFileSet(), "synthetic.go", tc.src, 0)
			if err != nil {
				t.Fatal(err)
			}
			found := reasonConstants(file)
			if len(found) != tc.wantNames {
				t.Fatalf("collected %d reason constants %v, want %d", len(found), found, tc.wantNames)
			}
			if value, ok := found[tc.probe]; !ok || value != tc.probeValue {
				t.Fatalf("%s collected as %q (present=%v), want %q; the closed-set gate would miss it", tc.probe, value, ok, tc.probeValue)
			}
		})
	}
}

// declaredReasonConstants reads every reason constant of the package so an
// unbounded reason vocabulary cannot be added without failing the closed set. Any
// constant whose name begins with Reason is collected whether or not it carries
// the Reason type identifier, so an untyped literal or an alias of an existing
// reason cannot slip past the closed set.
func declaredReasonConstants(t *testing.T, dir string) map[string]string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]string{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		maps.Copy(found, reasonConstants(parseSource(t, filepath.Join(dir, name))))
	}
	return found
}

// reasonConstants maps every constant name prefixed Reason in one parsed file to
// its string value. A name whose value is not a plain string literal maps to the
// empty string so the caller rejects it: an untyped alias, a derived expression
// or a repeated const spec can never add a fifth reason.
func reasonConstants(file *ast.File) map[string]string {
	found := map[string]string{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, ident := range value.Names {
				if !strings.HasPrefix(ident.Name, "Reason") {
					continue
				}
				if i >= len(value.Values) {
					found[ident.Name] = ""
					continue
				}
				literal, ok := value.Values[i].(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					found[ident.Name] = ""
					continue
				}
				found[ident.Name] = strings.Trim(literal.Value, `"`)
			}
		}
	}
	return found
}

func parseSource(t *testing.T, path string) *ast.File {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	return file
}

func TestPolicyValueSurfaceRejectsAttackerControlledData(t *testing.T) {
	t.Parallel()

	allowed := map[string][]string{
		"ingressdefense.Policy": {
			"Enabled", "AuthFailures", "FailureWindow", "InitialQuarantine", "MaxQuarantine", "AdaptiveExemptCIDRs",
		},
		"ingressdefense.StateLimits": {"MaxEntries", "StateTTL"},
		"ingressdefense.Transition":  {"QuarantineStarted", "QuarantineUntil", "Reason", "EntryCount"},
	}
	for _, value := range []any{Policy{}, StateLimits{}, Transition{}} {
		typ := reflect.TypeOf(value)
		want := allowed[typ.String()]
		if want == nil {
			t.Fatalf("%s is not an admitted policy value type", typ)
		}
		if typ.NumField() != len(want) {
			t.Errorf("%s has %d fields, want exactly %d", typ, typ.NumField(), len(want))
		}
		for field := range typ.Fields() {
			if !slices.Contains(want, field.Name) {
				t.Errorf("%s.%s is not part of the closed policy surface", typ, field.Name)
			}
			if err := checkBoundedValueField(typ, field); err != nil {
				t.Error(err)
			}
		}
	}
}

func checkBoundedValueField(owner reflect.Type, field reflect.StructField) error {
	prefixType := reflect.TypeFor[netip.Prefix]()
	addrType := reflect.TypeFor[netip.Addr]()
	switch field.Type {
	case reflect.TypeFor[bool](), reflect.TypeFor[int](), reflect.TypeFor[time.Duration](), reflect.TypeFor[time.Time]():
		return nil
	case reflect.TypeFor[Reason]():
		if field.Name != "Reason" {
			return fmt.Errorf("%s.%s: only the closed Reason enum may be string-kind", owner, field.Name)
		}
		return nil
	}
	if field.Type == reflect.SliceOf(prefixType) {
		if owner.String() != "ingressdefense.Policy" || field.Name != "AdaptiveExemptCIDRs" {
			return fmt.Errorf("%s.%s: prefixes are allowed only as the adaptive exemption allowlist", owner, field.Name)
		}
		return nil
	}
	if field.Type == prefixType {
		return fmt.Errorf("%s.%s: no single prefix may be stored; exemptions are an allowlist", owner, field.Name)
	}
	if field.Type == addrType {
		return fmt.Errorf("%s.%s: source identity belongs in exact-address state keys, not policy values", owner, field.Name)
	}
	return fmt.Errorf("%s.%s: unsupported value field type %s", owner, field.Name, field.Type)
}
