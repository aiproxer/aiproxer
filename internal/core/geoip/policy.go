// Package geoip contains protocol-neutral GeoIP ingress policy semantics.
package geoip

import (
	"fmt"
	"net/netip"
	"strings"
)

// Order controls the Apache-style rule-class precedence.
type Order string

const (
	// OrderDenyAllow evaluates deny first and allow second; allow wins ties and
	// the default is allow.
	OrderDenyAllow Order = "deny_allow"
	// OrderAllowDeny evaluates allow first and deny second; deny wins ties and
	// the default is deny.
	OrderAllowDeny Order = "allow_deny"
)

// Reason is a finite, bounded decision classification suitable for metrics.
type Reason string

const (
	ReasonCIDRAllow     Reason = "cidr_allow"
	ReasonCIDRDeny      Reason = "cidr_deny"
	ReasonCountryAllow  Reason = "country_allow"
	ReasonCountryDeny   Reason = "country_deny"
	ReasonDefaultAllow  Reason = "default_allow"
	ReasonDefaultDeny   Reason = "default_deny"
	ReasonClientIPError Reason = "client_ip_error"
	ReasonLookupError   Reason = "lookup_error"
)

// Decision is the result of evaluating one normalized address.
type Decision struct {
	Allow  bool
	Reason Reason
}

// CountryLookup is the only database capability required by policy evaluation.
type CountryLookup interface {
	LookupCountry(netip.Addr) (country string, found bool, err error)
}

// RuleConfig is a source representation for one rule class. Compile takes
// ownership of copies; callers may safely reuse or mutate their inputs later.
type RuleConfig struct {
	Countries []string
	CIDRs     []string
}

// CompileInput describes one immutable policy.
type CompileInput struct {
	Order Order
	Allow RuleConfig
	Deny  RuleConfig
}

type ruleClass struct {
	countries map[string]struct{}
	prefixes  []netip.Prefix
}

// Policy is immutable after Compile returns. Its maps and slices are private,
// and no accessor exposes mutable backing storage.
type Policy struct {
	order              Order
	allow              ruleClass
	deny               ruleClass
	needsCountryLookup bool
}

// Compile validates and compiles a policy without retaining mutable caller data.
func Compile(in CompileInput) (*Policy, error) {
	if in.Order != OrderDenyAllow && in.Order != OrderAllowDeny {
		return nil, fmt.Errorf("geoip: invalid order %q", in.Order)
	}
	allow, err := compileClass("allow", in.Allow)
	if err != nil {
		return nil, err
	}
	deny, err := compileClass("deny", in.Deny)
	if err != nil {
		return nil, err
	}
	return &Policy{
		order:              in.Order,
		allow:              allow,
		deny:               deny,
		needsCountryLookup: len(allow.countries) > 0 || len(deny.countries) > 0,
	}, nil
}

func compileClass(name string, in RuleConfig) (ruleClass, error) {
	countries := make(map[string]struct{}, len(in.Countries))
	for i, raw := range in.Countries {
		country := strings.ToUpper(strings.TrimSpace(raw))
		if !isISOAlpha2(country) {
			return ruleClass{}, fmt.Errorf("geoip: %s.countries[%d]: invalid ISO-3166 alpha-2 code %q", name, i, raw)
		}
		countries[country] = struct{}{}
	}
	prefixes := make([]netip.Prefix, 0, len(in.CIDRs))
	for i, raw := range in.CIDRs {
		value := strings.TrimSpace(raw)
		if value == "" {
			return ruleClass{}, fmt.Errorf("geoip: %s.cidrs[%d]: empty address", name, i)
		}
		var prefix netip.Prefix
		if strings.Contains(value, "/") {
			parsed, err := netip.ParsePrefix(value)
			if err != nil {
				return ruleClass{}, fmt.Errorf("geoip: %s.cidrs[%d]: invalid CIDR %q: %w", name, i, raw, err)
			}
			prefix, err = normalizePrefix(parsed)
			if err != nil {
				return ruleClass{}, fmt.Errorf("geoip: %s.cidrs[%d]: invalid CIDR %q: %w", name, i, raw, err)
			}
		} else {
			addr, err := netip.ParseAddr(value)
			if err != nil {
				return ruleClass{}, fmt.Errorf("geoip: %s.cidrs[%d]: invalid address %q: %w", name, i, raw, err)
			}
			addr = addr.Unmap()
			bits := 128
			if addr.Is4() {
				bits = 32
			}
			prefix = netip.PrefixFrom(addr, bits)
		}
		prefixes = append(prefixes, prefix)
	}
	return ruleClass{countries: countries, prefixes: prefixes}, nil
}

// NeedsCountryLookup reports whether some address decisions may require a country lookup.
func (p *Policy) NeedsCountryLookup() bool {
	return p != nil && p.needsCountryLookup
}

// Order reports the immutable policy order.
func (p *Policy) Order() Order {
	if p == nil {
		return ""
	}
	return p.order
}

// Evaluate applies the exact two-class truth table. Mapped IPv4 addresses are
// normalized before matching and lookup. A CIDR in the final precedence phase
// short-circuits country lookup because its outcome cannot be changed.
func (p *Policy) Evaluate(addr netip.Addr, lookup CountryLookup) Decision {
	if p == nil || !addr.IsValid() {
		return Decision{Allow: false, Reason: ReasonClientIPError}
	}
	addr = addr.Unmap()
	allowCIDR := p.allow.contains(addr)
	denyCIDR := p.deny.contains(addr)
	if p.order == OrderDenyAllow && allowCIDR {
		return Decision{Allow: true, Reason: ReasonCIDRAllow}
	}
	if p.order == OrderAllowDeny && denyCIDR {
		return Decision{Allow: false, Reason: ReasonCIDRDeny}
	}

	allowCountry, denyCountry := false, false
	if p.needsCountryLookup {
		if lookup == nil {
			return Decision{Allow: false, Reason: ReasonLookupError}
		}
		country, found, err := lookup.LookupCountry(addr)
		if err != nil {
			return Decision{Allow: false, Reason: ReasonLookupError}
		}
		if found {
			country = strings.ToUpper(strings.TrimSpace(country))
			_, allowCountry = p.allow.countries[country]
			_, denyCountry = p.deny.countries[country]
		}
	}
	allowMatch := allowCIDR || allowCountry
	denyMatch := denyCIDR || denyCountry
	if p.order == OrderDenyAllow {
		if allowMatch {
			return Decision{Allow: true, Reason: classReason(allowCIDR, allowCountry, true)}
		}
		if denyMatch {
			return Decision{Allow: false, Reason: classReason(denyCIDR, denyCountry, false)}
		}
		return Decision{Allow: true, Reason: ReasonDefaultAllow}
	}
	if denyMatch {
		return Decision{Allow: false, Reason: classReason(denyCIDR, denyCountry, false)}
	}
	if allowMatch {
		return Decision{Allow: true, Reason: classReason(allowCIDR, allowCountry, true)}
	}
	return Decision{Allow: false, Reason: ReasonDefaultDeny}
}

func classReason(cidr, country, allow bool) Reason {
	if allow {
		if cidr {
			return ReasonCIDRAllow
		}
		if country {
			return ReasonCountryAllow
		}
	} else {
		if cidr {
			return ReasonCIDRDeny
		}
		if country {
			return ReasonCountryDeny
		}
	}
	if allow {
		return ReasonDefaultAllow
	}
	return ReasonDefaultDeny
}

func normalizePrefix(prefix netip.Prefix) (netip.Prefix, error) {
	addr := prefix.Addr()
	bits := prefix.Bits()
	if addr.Is4In6() {
		addr = addr.Unmap()
		bits -= 96
		if bits < 0 {
			return netip.Prefix{}, fmt.Errorf("mapped IPv6 prefix has invalid length")
		}
	}
	return netip.PrefixFrom(addr.Unmap(), bits).Masked(), nil
}

func (r ruleClass) contains(addr netip.Addr) bool {
	for _, prefix := range r.prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// isISOAlpha2 is a maintained ISO-3166 alpha-2 allowlist, intentionally kept
// here so policy compilation is deterministic and does not depend on locale or
// network state.
func isISOAlpha2(code string) bool {
	_, ok := iso3166Alpha2[code]
	return ok
}

var iso3166Alpha2 = func() map[string]struct{} {
	const codes = "AD AE AF AG AI AL AM AO AQ AR AS AT AU AW AX AZ BA BB BD BE BF BG BH BI BJ BL BM BN BO BQ BR BS BT BV BW BY BZ CA CC CD CF CG CH CI CK CL CM CN CO CR CU CV CW CX CY CZ DE DJ DK DM DO DZ EC EE EG EH ER ES ET FI FJ FK FM FO FR GA GB GD GE GF GG GH GI GL GM GN GP GQ GR GS GT GU GW GY HK HM HN HR HT HU ID IE IL IM IN IO IQ IR IS IT JE JM JO JP KE KG KH KI KM KN KP KR KW KY KZ LA LB LC LI LK LR LS LT LU LV LY MA MC MD ME MF MG MH MK ML MM MN MO MP MQ MR MS MT MU MV MW MX MY MZ NA NC NE NF NG NI NL NO NP NR NU NZ OM PA PE PF PG PH PK PL PM PN PR PS PT PW PY QA RE RO RS RU RW SA SB SC SD SE SG SH SI SJ SK SL SM SN SO SR SS ST SV SX SY SZ TC TD TF TG TH TJ TK TL TM TN TO TR TT TV TW TZ UA UG UM US UY UZ VA VC VE VG VI VN VU WF WS YE YT ZA ZM ZW"
	out := make(map[string]struct{})
	for code := range strings.FieldsSeq(codes) {
		out[code] = struct{}{}
	}
	return out
}()
