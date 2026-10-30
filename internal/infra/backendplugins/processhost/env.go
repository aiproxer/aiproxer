package processhost

import (
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

var forbiddenEnvExact = map[string]struct{}{
	"PLUGIN_CLIENT_CERT":  {},
	"PLUGIN_MAGIC_COOKIE": {},
	"PLUGIN_MIN_PORT":     {},
	"PLUGIN_MAX_PORT":     {},
	"AIP_PLUGIN_SECRET":   {},
	"AIP_BOOTSTRAP_KEY":   {},
}

func osLookupEnv(name string) (string, bool) {
	return os.LookupEnv(name)
}

func isForbiddenEnvKey(name string) bool {
	u := strings.ToUpper(strings.TrimSpace(name))
	if _, ok := forbiddenEnvExact[u]; ok {
		return true
	}
	if strings.Contains(u, "SECRET") || strings.Contains(u, "PASSWORD") ||
		strings.Contains(u, "TOKEN") || strings.Contains(u, "BOOTSTRAP") ||
		strings.Contains(u, "MAGIC_COOKIE") || strings.Contains(u, "CLIENT_CERT") {
		return true
	}
	return false
}

// buildLaunchEnv returns a non-nil minimal environment. Empty allowlist yields
// a non-nil empty slice (os/exec will not inherit parent env).
func buildLaunchEnv(allow []string, channelFD int, extras []string) ([]string, error) {
	out := make([]string, 0, len(allow)+1+len(extras))
	for _, name := range allow {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if isForbiddenEnvKey(name) {
			return nil, ReasonEnvBootstrapRejected
		}
		if v, ok := osLookupEnv(name); ok {
			out = append(out, name+"="+v)
		}
	}
	if channelFD > 0 {
		out = append(out, backendplugin.EnvChannelFD+"="+itoa(channelFD))
	}
	for _, e := range extras {
		if e == "" {
			continue
		}
		key, _, _ := strings.Cut(e, "=")
		if isForbiddenEnvKey(key) {
			return nil, ReasonEnvBootstrapRejected
		}
		out = append(out, e)
	}
	if out == nil {
		out = []string{}
	}
	return out, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// appendActivationEnv accepts frozen non-secret values, never host inheritance.
func appendActivationEnv(base []string, values map[string]string) ([]string, error) {
	if len(values) > 16 {
		return nil, ReasonEnvBootstrapRejected
	}
	seen := map[string]bool{}
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		seen[strings.ToUpper(key)] = true
	}
	out := append([]string{}, base...)
	total := 0
	for _, name := range slices.Sorted(maps.Keys(values)) {
		value := values[name]
		total += len(value)
		if !config.BackendExecutionEnvName(name) || seen[strings.ToUpper(name)] || strings.ContainsRune(value, 0) || len(value) > 16<<10 || total > 64<<10 {
			return nil, ReasonEnvBootstrapRejected
		}
		out = append(out, name+"="+value)
	}
	return out, nil
}
