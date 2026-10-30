package runtimebundle_test

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// selfDefenseDocMarker is the operator-documentation marker the canonical
// example config must carry for ingress self-defense.
const selfDefenseDocMarker = "# --- Ingress self-defense ---"

// selfDefenseDocumentedDurations is every duration literal this feature
// documents. time.ParseDuration has no "d" unit, so an example or guide that
// printed the design prose "7d" would never load; the state_ttl upper bound is
// therefore proven to be loadable as 168h and unparseable as 7d.
var selfDefenseDocumentedDurations = []struct {
	field string
	value string
	want  time.Duration
}{
	{field: "window", value: "1m", want: time.Minute},
	{field: "initial_quarantine", value: "1m", want: time.Minute},
	{field: "max_quarantine", value: "2h", want: 2 * time.Hour},
	{field: "state_ttl", value: "24h", want: 24 * time.Hour},
	{field: "documented state_ttl upper bound", value: "168h", want: 7 * 24 * time.Hour},
}

// selfDefenseRequiredDocPhrases pins the deliberately small v1 surface and its
// safety trade-offs. Every phrase must appear in docs/ingress-self-defense.md.
var selfDefenseRequiredDocPhrases = map[string]string{
	"default-on posture":            "default-on",
	"explicit opt-out":              "enabled: false",
	"shared client-address trust":   "access.geoip.client_ip",
	"adaptive exemptions":           "adaptive.exempt_cidrs",
	"reload classification":         "reload",
	"restart classification":        "restart",
	"generic impossible-path reply": "generic `404`",
	"generic quarantine reply":      "generic `429`",
	"fixed policy stays in geoip":   "access.geoip",
	"success resets not trusts":     "reset",
	"credential traffic reaches au": "reach authentication",
	"shared-address safety":         "shared public ip",
	"no external feeds":             "reputation feed",
	"no waf or body inspection":     "waf",
	"no persistent state":           "process-local",
	"no subnet escalation":          "subnet",
	"logging bounded":               "logging",
}

var selfDefenseExampleKeyPattern = func(key string) *regexp.Regexp {
	// A documented line may carry a trailing explanatory comment, so the value
	// is the first whitespace-delimited token after the key.
	return regexp.MustCompile(`(?m)^[ \t]*#?[ \t]*` + regexp.QuoteMeta(key) + `:[ \t]*(\S+)`)
}

var docDurationLiteralPattern = regexp.MustCompile(`\b\d+(?:\.\d+)?(?:ns|us|ms|s|m|h)\b`)

// selfDefenseDocBlock returns the documented access.self_defense region of the
// canonical example: the top-level block that follows the operator marker, up to
// the next top-level key. Scoping the lookup to that block keeps the assertion
// from silently matching an unrelated "enabled:" row elsewhere in the file.
func selfDefenseDocBlock(t *testing.T, text string) string {
	t.Helper()
	start := strings.Index(text, selfDefenseDocMarker)
	if start < 0 {
		t.Fatalf("config/config.yaml must document ingress self-defense with the %q marker", selfDefenseDocMarker)
	}
	rest := text[start:]
	begin, end := -1, len(rest)
	consumed := 0
	for _, line := range strings.SplitAfter(rest, "\n") {
		if len(line) >= 2 && line[0] != ' ' && line[0] != '\t' && line[0] != '#' && line[0] != '\n' {
			if begin < 0 {
				begin = consumed
			} else {
				end = consumed
				break
			}
		}
		consumed += len(line)
	}
	if begin < 0 {
		t.Fatalf("config/config.yaml must document a top-level access block for ingress self-defense:\n%s", rest)
	}
	return rest[begin:end]
}

func selfDefenseExampleValue(t *testing.T, block, key string) string {
	t.Helper()
	match := selfDefenseExampleKeyPattern(key).FindStringSubmatch(block)
	if match == nil {
		t.Fatalf("the documented self-defense block does not set %q:\n%s", key, block)
	}
	return match[1]
}

func selfDefenseExamplePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(repoRootFromRuntimebundleTest(t), "config", "config.yaml")
}

// TestExampleConfigDocumentsSelfDefenseDefaults is the task 6.2 config-parse
// gate. It proves three things about the shipped canonical example: it prints
// every documented access.self_defense default, it loads and validates through
// the real production configuration path, and every duration it prints parses
// with the loader that consumes it.
func TestExampleConfigDocumentsSelfDefenseDefaults(t *testing.T) {
	t.Parallel()

	path := selfDefenseExamplePath(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	block := selfDefenseDocBlock(t, text)
	for _, field := range []string{
		"enabled", "impossible_paths",
		"auth_failures", "window", "initial_quarantine",
		"max_quarantine", "state_ttl", "max_entries", "exempt_cidrs",
	} {
		_ = selfDefenseExampleValue(t, block, field)
	}

	// The printed values must be the documented v1 defaults.
	printed := map[string]string{
		"enabled":            "true",
		"impossible_paths":   "true",
		"auth_failures":      "5",
		"window":             "1m",
		"initial_quarantine": "1m",
		"max_quarantine":     "2h",
		"state_ttl":          "24h",
		"max_entries":        "100000",
	}
	for key, want := range printed {
		if got := selfDefenseExampleValue(t, block, key); got != want {
			t.Fatalf("documented %s = %q, want the documented default %q", key, got, want)
		}
	}
	if got := selfDefenseExampleValue(t, block, "exempt_cidrs"); got != "[]" {
		t.Fatalf("documented exempt_cidrs = %q, want the documented empty default []", got)
	}
	for _, tc := range selfDefenseDocumentedDurations {
		parsed, err := time.ParseDuration(tc.value)
		if err != nil {
			t.Fatalf("documented %s = %q does not parse with the loader's duration parser: %v", tc.field, tc.value, err)
		}
		if parsed != tc.want {
			t.Fatalf("documented %s = %q parses to %s, want %s", tc.field, tc.value, parsed, tc.want)
		}
	}

	// The real loader must accept the shipped file unchanged, and the effective
	// self-defense policy it compiles must be the documented default-on v1 policy.
	cfg, err := config.LoadFile(path)
	if err != nil {
		t.Fatalf("config/config.yaml must load with the production loader: %v", err)
	}
	compiled, err := config.CompileSelfDefense(cfg.Access.SelfDefense)
	if err != nil {
		t.Fatalf("the shipped example must compile: %v", err)
	}
	if !compiled.Enabled() || !compiled.ImpossiblePaths() {
		t.Fatal("the shipped example must document the default-on posture")
	}
	if compiled.AuthFailures() != 5 || compiled.FailureWindow() != time.Minute ||
		compiled.InitialQuarantine() != time.Minute || compiled.MaxQuarantine() != 2*time.Hour ||
		compiled.StateTTL() != 24*time.Hour || compiled.MaxEntries() != 100000 {
		t.Fatalf("shipped example effective policy = %d/%s/%s/%s/%s/%d, want 5/1m/1m/2h/24h/100000",
			compiled.AuthFailures(), compiled.FailureWindow(), compiled.InitialQuarantine(),
			compiled.MaxQuarantine(), compiled.StateTTL(), compiled.MaxEntries())
	}
	if len(compiled.AdaptiveExemptCIDRs()) != 0 {
		t.Fatalf("documented exempt_cidrs default must be empty, got %v", compiled.AdaptiveExemptCIDRs())
	}

	// The documented state_ttl upper bound must itself be loadable configuration:
	// an operator copying the documented range must be able to use its maximum.
	if _, err := config.CompileSelfDefense(config.SelfDefenseConfig{
		Adaptive: config.SelfDefenseAdaptiveConfig{StateTTL: "168h"},
	}); err != nil {
		t.Fatalf("the documented state_ttl upper bound 168h must be accepted by the loader: %v", err)
	}
	if _, err := config.CompileSelfDefense(config.SelfDefenseConfig{
		Adaptive: config.SelfDefenseAdaptiveConfig{StateTTL: "7d"},
	}); err == nil {
		t.Fatal("the documentation must not imply a loadable day unit: time.ParseDuration has no d unit")
	}

	// check-config semantics: structural validation of the shipped file, with no
	// listener bound and no network I/O.
	materialized := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(materialized, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimebundle.ValidateStructural(context.Background(), runtimebundle.ValidateStructuralInput{
		ConfigPath: materialized,
		Mandatory:  aipsdk.StandardDistributionRequirements(),
	}); err != nil {
		t.Fatalf("the shipped example must pass structural validation: %v", err)
	}
}

// TestSelfDefenseOperatorDocStatesTheSafetyTradeOffs proves the operator guide
// states every deliberately small v1 behaviour an operator needs before
// exposing the proxy, and that no duration it prints is unloadable.
func TestSelfDefenseOperatorDocStatesTheSafetyTradeOffs(t *testing.T) {
	t.Parallel()

	path := filepath.Join(repoRootFromRuntimebundleTest(t), "docs", "ingress-self-defense.md")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the canonical ingress self-defense operator guide must exist: %v", err)
	}
	text := strings.ToLower(string(raw))
	for label, phrase := range selfDefenseRequiredDocPhrases {
		if !strings.Contains(text, phrase) {
			t.Fatalf("docs/ingress-self-defense.md must state the %s trade-off (missing %q)", label, phrase)
		}
	}
	for _, field := range []string{"max_entries", "state_ttl"} {
		if !strings.Contains(text, field) {
			t.Fatalf("docs/ingress-self-defense.md must classify the restart-required field %q", field)
		}
	}
	for _, field := range []string{"auth_failures", "window", "initial_quarantine", "max_quarantine", "exempt_cidrs", "impossible_paths", "enabled"} {
		if !strings.Contains(text, field) {
			t.Fatalf("docs/ingress-self-defense.md must document the reloadable field %q", field)
		}
	}
	// Every Go duration literal the guide prints must parse with the loader.
	for _, literal := range docDurationLiterals(raw) {
		if _, err := time.ParseDuration(literal); err != nil {
			t.Fatalf("docs/ingress-self-defense.md prints %q, which the configuration loader cannot parse: %v", literal, err)
		}
	}
}

// docDurationLiterals returns the distinct Go duration literals the guide
// prints. The pattern requires the unit to be adjacent to the digits, so version
// numbers, ports and CIDR prefixes never match.
func docDurationLiterals(raw []byte) []string {
	seen := map[string]bool{}
	var out []string
	for _, literal := range docDurationLiteralPattern.FindAll(raw, -1) {
		text := string(literal)
		if seen[text] {
			continue
		}
		seen[text] = true
		out = append(out, text)
	}
	return out
}
