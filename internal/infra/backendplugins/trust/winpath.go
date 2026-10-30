package trust

import (
	"strings"
)

// normalizeWindowsPath strips Win32/NT path prefixes, unifies separators, and
// lowercases for case-insensitive volume path comparisons. It is pure and safe
// to call from any GOOS (helpers used by Windows containment checks).
func normalizeWindowsPath(p string) string {
	if p == "" {
		return ""
	}
	p = strings.ReplaceAll(p, `/`, `\`)
	upper := strings.ToUpper(p)
	switch {
	case strings.HasPrefix(upper, `\\?\UNC\`):
		p = `\\` + p[len(`\\?\UNC\`):]
	case strings.HasPrefix(p, `\\?\`):
		p = p[len(`\\?\`):]
	case strings.HasPrefix(upper, `\??\UNC\`):
		p = `\\` + p[len(`\??\UNC\`):]
	case strings.HasPrefix(p, `\??\`):
		p = p[len(`\??\`):]
	}
	p = strings.ToLower(p)
	return strings.TrimRight(p, `\`)
}

// windowsPathContained reports whether candidate is a path strictly under root
// after Windows path normalization. Equality with root is rejected (files must
// be nested). Sibling prefix matches (C:\trust vs C:\trusted) are rejected.
func windowsPathContained(root, candidate string) bool {
	r := normalizeWindowsPath(root)
	c := normalizeWindowsPath(candidate)
	if r == "" || c == "" {
		return false
	}
	if c == r {
		return false
	}
	return strings.HasPrefix(c, r+`\`)
}
