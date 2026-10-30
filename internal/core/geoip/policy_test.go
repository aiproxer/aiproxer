package geoip

import (
	"errors"
	"net/netip"
	"testing"
)

type testLookup struct {
	country string
	found   bool
	err     error
	calls   int
}

func (l *testLookup) LookupCountry(netip.Addr) (string, bool, error) {
	l.calls++
	return l.country, l.found, l.err
}

func TestCompileEvaluateOrderTruthTable(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		order Order
		allow bool
		deny  bool
		want  bool
	}{
		{name: "deny_allow neither", order: OrderDenyAllow, want: true},
		{name: "deny_allow allow only", order: OrderDenyAllow, allow: true, want: true},
		{name: "deny_allow deny only", order: OrderDenyAllow, deny: true, want: false},
		{name: "deny_allow both", order: OrderDenyAllow, allow: true, deny: true, want: true},
		{name: "allow_deny neither", order: OrderAllowDeny, want: false},
		{name: "allow_deny allow only", order: OrderAllowDeny, allow: true, want: true},
		{name: "allow_deny deny only", order: OrderAllowDeny, deny: true, want: false},
		{name: "allow_deny both", order: OrderAllowDeny, allow: true, deny: true, want: false},
	}
	addr := netip.MustParseAddr("192.0.2.10")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			input := CompileInput{Order: tc.order}
			if tc.allow {
				input.Allow.CIDRs = []string{"192.0.2.0/24"}
			}
			if tc.deny {
				input.Deny.CIDRs = []string{"192.0.2.0/24"}
			}
			p, err := Compile(input)
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			got := p.Evaluate(addr, nil)
			if got.Allow != tc.want {
				t.Fatalf("Allow = %v, want %v (decision=%+v)", got.Allow, tc.want, got)
			}
		})
	}
}

func TestPolicyCountryAndCIDRException(t *testing.T) {
	t.Parallel()

	p, err := Compile(CompileInput{
		Order: OrderDenyAllow,
		Deny:  RuleConfig{Countries: []string{"ru"}},
		Allow: RuleConfig{CIDRs: []string{"198.51.100.64/27"}},
	})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	lookup := &testLookup{country: "RU", found: true}
	allowed := p.Evaluate(netip.MustParseAddr("198.51.100.70"), lookup)
	if !allowed.Allow || allowed.Reason != ReasonCIDRAllow {
		t.Fatalf("office decision = %+v, want CIDR allow", allowed)
	}
	blocked := p.Evaluate(netip.MustParseAddr("198.51.100.10"), lookup)
	if blocked.Allow || blocked.Reason != ReasonCountryDeny {
		t.Fatalf("country decision = %+v, want country deny", blocked)
	}
}

func TestPolicyNoCountryAndLookupError(t *testing.T) {
	t.Parallel()

	p, err := Compile(CompileInput{
		Order: OrderDenyAllow,
		Deny:  RuleConfig{Countries: []string{"RU"}},
	})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	noCountry := p.Evaluate(netip.MustParseAddr("203.0.113.10"), &testLookup{})
	if !noCountry.Allow || noCountry.Reason != ReasonDefaultAllow {
		t.Fatalf("no-country decision = %+v, want default allow", noCountry)
	}
	lookupErr := p.Evaluate(netip.MustParseAddr("203.0.113.10"), &testLookup{err: errors.New("corrupt reader")})
	if lookupErr.Allow || lookupErr.Reason != ReasonLookupError {
		t.Fatalf("lookup-error decision = %+v, want fail-closed lookup error", lookupErr)
	}
}

func TestPolicyNormalizesMappedIPv4AndOwnsInputs(t *testing.T) {
	t.Parallel()

	countries := []string{"RU"}
	cidrs := []string{"192.0.2.10/24"}
	p, err := Compile(CompileInput{
		Order: OrderDenyAllow,
		Deny:  RuleConfig{Countries: countries, CIDRs: cidrs},
	})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	countries[0] = "US"
	cidrs[0] = "198.51.100.0/24"

	lookup := &testLookup{}
	decision := p.Evaluate(netip.MustParseAddr("::ffff:192.0.2.10"), lookup)
	if decision.Allow || decision.Reason != ReasonCIDRDeny {
		t.Fatalf("mapped address decision = %+v, want CIDR deny", decision)
	}
}

func TestPolicyNormalizesMappedIPv6Prefixes(t *testing.T) {
	t.Parallel()

	p, err := Compile(CompileInput{
		Order: OrderDenyAllow,
		Deny:  RuleConfig{CIDRs: []string{"::ffff:192.0.2.0/120"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision := p.Evaluate(netip.MustParseAddr("192.0.2.10"), nil); decision.Allow || decision.Reason != ReasonCIDRDeny {
		t.Fatalf("mapped-prefix decision = %+v, want CIDR deny", decision)
	}
}

func TestPolicySkipsLookupWhenCIDRFinalizesDecision(t *testing.T) {
	t.Parallel()

	p, err := Compile(CompileInput{
		Order: OrderDenyAllow,
		Deny:  RuleConfig{Countries: []string{"RU"}},
		Allow: RuleConfig{CIDRs: []string{"192.0.2.0/24"}},
	})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	lookup := &testLookup{country: "RU", found: true}
	decision := p.Evaluate(netip.MustParseAddr("192.0.2.10"), lookup)
	if !decision.Allow || lookup.calls != 0 {
		t.Fatalf("decision=%+v lookup calls=%d, want final CIDR allow with no lookup", decision, lookup.calls)
	}
}

func TestCompileRejectsInvalidRules(t *testing.T) {
	t.Parallel()

	for _, input := range []CompileInput{
		{Order: "unknown"},
		{Order: OrderDenyAllow, Allow: RuleConfig{Countries: []string{"RUS"}}},
		{Order: OrderDenyAllow, Deny: RuleConfig{Countries: []string{"ZZ"}}},
		{Order: OrderDenyAllow, Allow: RuleConfig{CIDRs: []string{"example.com"}}},
		{Order: OrderDenyAllow, Deny: RuleConfig{CIDRs: []string{"192.0.2.1/99"}}},
	} {
		if _, err := Compile(input); err == nil {
			t.Errorf("Compile(%+v) succeeded, want error", input)
		}
	}
}
