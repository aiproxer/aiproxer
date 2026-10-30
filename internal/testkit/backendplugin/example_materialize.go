package backendplugin

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	reLocalStubKind   = regexp.MustCompile(`(?m)^[ \t]*-[ \t]*kind:[ \t]*local-stub\b`)
	reMultiUserAccess = regexp.MustCompile(`(?m)^[ \t]*mode:[ \t]*multi_user\b`)
)

// MaterializeExampleConfig copies an example YAML and rewrites plugins.backend_discovery
// to a freshly staged connectors/localstub root. Configs that enable kind local-stub
// without discovery get a discovery block injected.
func MaterializeExampleConfig(tb testing.TB, srcPath string) string {
	tb.Helper()
	raw, err := os.ReadFile(srcPath)
	if err != nil {
		tb.Fatal(err)
	}
	body := string(raw)
	if !reLocalStubKind.MatchString(body) {
		return srcPath
	}
	pluginRoot := StageLocalStub(tb)
	devMode := "true"
	if reMultiUserAccess.MatchString(body) {
		devMode = "false"
	}
	discovery := "  backend_discovery:\n" +
		"    enabled: true\n" +
		"    development_mode: " + devMode + "\n" +
		"    paths:\n" +
		"      - " + filepath.ToSlash(pluginRoot) + "\n"
	if rewritten, ok := replaceBackendDiscovery(body, discovery); ok {
		body = rewritten
	} else {
		idx := strings.Index(body, "\nplugins:\n")
		if idx < 0 {
			idx = strings.Index(body, "\nplugins:\r\n")
		}
		if idx < 0 {
			tb.Fatalf("example %s uses local-stub but has no plugins: block", srcPath)
		}
		insertAt := idx + len("\nplugins:\n")
		if strings.HasPrefix(body[idx:], "\nplugins:\r\n") {
			insertAt = idx + len("\nplugins:\r\n")
		}
		body = body[:insertAt] + discovery + body[insertAt:]
	}
	dst := filepath.Join(tb.TempDir(), filepath.Base(srcPath))
	if err := os.WriteFile(dst, []byte(body), 0o600); err != nil {
		tb.Fatal(err)
	}
	return dst
}

// replaceBackendDiscovery replaces only the backend_discovery mapping and its
// nested keys, preserving sibling keys under plugins.
func replaceBackendDiscovery(body, discovery string) (string, bool) {
	lines := strings.SplitAfter(body, "\n")
	start := -1
	baseIndent := ""
	for i, line := range lines {
		trim := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trim, "backend_discovery:") {
			start = i
			baseIndent = line[:len(line)-len(trim)]
			break
		}
	}
	if start < 0 {
		return body, false
	}
	end := start + 1
	for end < len(lines) {
		line := lines[end]
		if strings.TrimSpace(line) == "" {
			end++
			continue
		}
		if !strings.HasPrefix(line, baseIndent) {
			break
		}
		rest := line[len(baseIndent):]
		if rest == "" || rest[0] == ' ' || rest[0] == '\t' {
			end++
			continue
		}
		break
	}
	var b strings.Builder
	for _, line := range lines[:start] {
		b.WriteString(line)
	}
	b.WriteString(discovery)
	for _, line := range lines[end:] {
		b.WriteString(line)
	}
	return b.String(), true
}
