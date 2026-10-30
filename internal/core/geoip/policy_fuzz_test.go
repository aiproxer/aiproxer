package geoip

import (
	"net/netip"
	"testing"
)

func FuzzCompileAndEvaluate(f *testing.F) {
	f.Add("deny_allow", "RU", "192.0.2.0/24", "192.0.2.1")
	f.Add("allow_deny", "US", "2001:db8::/32", "::ffff:192.0.2.1")
	f.Fuzz(func(t *testing.T, order, country, cidr, address string) {
		policy, err := Compile(CompileInput{
			Order: Order(order),
			Allow: RuleConfig{Countries: []string{country}, CIDRs: []string{cidr}},
		})
		if err != nil {
			return
		}
		addr, err := netip.ParseAddr(address)
		if err != nil {
			return
		}
		_ = policy.Evaluate(addr, nil)
	})
}
