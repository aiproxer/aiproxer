package discovery

import (
	"os"
	"path/filepath"
	"runtime"
)

// UpstreamDefaultRoot returns the OS-specific upstream machine default plugin root.
func UpstreamDefaultRoot() string {
	switch runtime.GOOS {
	case "windows":
		pf := os.Getenv("ProgramFiles")
		if pf == "" {
			pf = `C:\Program Files`
		}
		return filepath.Join(pf, "AIProxer", "plugins")
	case "darwin":
		return "/Library/Application Support/AIProxer/plugins"
	default:
		return "/opt/AIProxer/plugins"
	}
}

func (c Config) roots() ([]string, error) {
	if c.Development {
		if len(c.ExplicitPaths) == 0 {
			return nil, ErrDevelopmentPathsRequired
		}
		return uniquePreserve(c.ExplicitPaths), nil
	}
	var out []string
	out = append(out, c.ExplicitPaths...)
	out = append(out, c.PackagerRoots...)
	if c.IncludeUpstreamDefaults {
		out = append(out, UpstreamDefaultRoot())
	}
	return uniquePreserve(out), nil
}

func uniquePreserve(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, p := range in {
		if p == "" {
			continue
		}
		clean := filepath.Clean(p)
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	return out
}
