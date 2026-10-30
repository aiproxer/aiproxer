package reftoolpolicy

import "strings"

type policy struct {
	exact  map[string]struct{}
	prefix []string
}

func newPolicy(cfg Config) policy {
	exact := map[string]struct{}{}
	for _, n := range cfg.BlockNames {
		if n == "" {
			continue
		}
		exact[n] = struct{}{}
	}
	prefix := make([]string, 0, len(cfg.BlockPrefixes))
	for _, p := range cfg.BlockPrefixes {
		if p == "" {
			continue
		}
		prefix = append(prefix, p)
	}
	return policy{exact: exact, prefix: prefix}
}

func (p policy) blocked(name string) bool {
	if name == "" {
		return false
	}
	if _, ok := p.exact[name]; ok {
		return true
	}
	for _, pre := range p.prefix {
		if strings.HasPrefix(name, pre) {
			return true
		}
	}
	return false
}
