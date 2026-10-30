package selfdefense

import (
	"net/url"
	"strings"
	"testing"
)

func TestImpossiblePathRejectsAuditedProbeSet(t *testing.T) {
	t.Parallel()

	for _, target := range []string{
		"/.env",
		"/.env.local",
		"/.env/production",
		"/.ENV",
		"/.envrc",
		"/v1/.env",
		"/.git/config",
		"/.git/HEAD",
		"/.git/objects/info/packs",
		"/.gitignore",
		"/v1/.gitignore",
		"/.svn/entries",
		"/.hg/requires",
		"/.htaccess",
		"/.htpasswd",
		"/.aws/credentials",
		"/v1/.aws/credentials",
		"/.ssh/id_rsa",
		"/.docker/config.json",
		"/.dockerignore",
		"/.DS_Store",
		"/v1/.DS_Store",
		"/.idea/workspace.xml",
		"/.editorconfig",
		"/.npmrc",
		"/.kube/config",
		"/wp-admin",
		"/wp-admin/",
		"/wp-admin/install.php",
		"/wp-login.php",
		"/wp-cron.php",
		"/xmlrpc.php",
		"/wp-content/uploads/2020/shell.php",
		"/wp-includes/version.php",
		"/phpmyadmin",
		"/phpmyadmin/index.php",
		"/pma",
		"/pma/index.php",
		"/adminer.php",
		"/adminer",
		"/adminer/index.php",
		"/vendor/phpunit/phpunit/src/Util/PHP/eval-stdin.php",
		"/phpunit.xml",
		"/phpunit.xml.dist",
		"/cgi-bin",
		"/cgi-bin/test-cgi",
		"/composer.json",
		"/composer.lock",
		"/server-status",
		"/server-info",
		"/info.php",
		"/phpinfo.php",
		"/test.php",
		"/web.config",
		"//.env",
		"///wp-admin/install.php",
	} {
		if !ImpossiblePath(target) {
			t.Errorf("ImpossiblePath(%q) = false, want true", target)
		}
	}
}

func TestImpossiblePathRejectsTraversalStructure(t *testing.T) {
	t.Parallel()

	for _, target := range []string{
		"/../etc/passwd",
		"/..",
		"/v1/../../etc/passwd",
		"/v1/..",
		"/v1/../responses",
		"/a/b/../../../etc/shadow",
		`/v1/..\..\windows\win.ini`,
		`/\..\..\etc\passwd`,
		"/v1/responses\\..\\..\\admin",
	} {
		if !ImpossiblePath(target) {
			t.Errorf("ImpossiblePath(%q) = false, want true", target)
		}
	}
}

func TestImpossiblePathAllowsStandardDataPlaneSurface(t *testing.T) {
	t.Parallel()

	for _, target := range []string{
		"/",
		"/healthz",
		"/metrics",
		"/v1",
		"/v1/models",
		"/v1/chat/completions",
		"/v1/responses",
		"/v1/responses/resp_123/cancel",
		"/v1/messages",
		"/v1beta",
		"/v1beta/models/gemini-2.5-pro:generateContent",
		"/v1beta1/models/gemini-2.5-pro:streamGenerateContent",
		"/openresponses/v1/responses",
		"/openresponses/v1/responses/compact",
		"/aip/v1/a-legs/leg-1/cancel",
		"/admin/config/reload",
		"/admin/config/status",
		"/admin/billing/account",
		"/admin/keepwarm",
		"/admin/session-features/sess-1/feat-1",
		"/environment",
		"/v1/environment",
		"/v1/git/config",
		"/v1/settings",
		"/v1/htaccess-guide",
		"/v1/responses/",
		"/v2/chat/completions",
	} {
		if ImpossiblePath(target) {
			t.Errorf("ImpossiblePath(%q) = true, want false", target)
		}
	}
}

// TestImpossiblePathIgnoresAnEscapedQuestionMarkAndEverythingAfterIt makes the
// query-independence property of requirement 3.2 and 9.4 structural instead of
// contractual. net/http percent-decodes the request target exactly once, so an
// escaped "%3F" becomes a literal "?" inside [url.URL.Path] together with all the
// attacker's remaining text. The matcher must cut the decoded path itself, so no
// caller obligation is needed and no rule family can reintroduce the leak.
func TestImpossiblePathIgnoresAnEscapedQuestionMarkAndEverythingAfterIt(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		target string
	}{
		{name: "escaped question mark then traversal", target: "/v1/chat/completions%3Fpath=../../../../etc/passwd"},
		{name: "escaped question mark then dotfile", target: "/v1/chat/completions%3Fnext=/.env"},
		{name: "escaped question mark then backslash traversal", target: `/v1/messages%3Fx=..\..\etc\passwd`},
		{name: "escaped question mark alone", target: "/v1/responses%3Fkey=../../secret"},
		{name: "leading escaped question mark", target: "/%3F/../etc/passwd"},
		{name: "escaped question mark in the first segment", target: "/v1%3F.env/credentials"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			parsed, err := url.Parse(tc.target)
			if err != nil {
				t.Fatalf("url.Parse(%q): %v", tc.target, err)
			}
			if parsed.RawQuery != "" {
				t.Fatalf("fixture must carry no real query string, got %q", parsed.RawQuery)
			}
			if !strings.Contains(parsed.Path, "?") {
				t.Fatalf("fixture must decode to a literal question mark inside the path, got %q", parsed.Path)
			}
			// Non-vacuity: the same bytes with the question mark spelled as a real
			// separator are a genuine probe, so the decoded target above is safe only
			// because of the cut.
			if !ImpossiblePath(strings.Replace(parsed.Path, "?", "/", 1)) {
				t.Fatalf("fixture is vacuous: %q must match once the question mark is a separator", parsed.Path)
			}
			if ImpossiblePath(parsed.Path) {
				t.Errorf("ImpossiblePath(%q) = true, want false: the matcher must cut at the first question mark", parsed.Path)
			}
		})
	}

	// A literal question mark handed to the matcher directly is the same
	// structural obligation, so it is checked on the same terms.
	for _, decoded := range []string{
		"/v1/chat/completions?path=../../../../etc/passwd",
		"/v1/models?next=/info.php",
		"/?/../etc/passwd",
		"?/../etc/passwd",
	} {
		if !strings.Contains(decoded, "?") {
			t.Fatalf("fixture must carry a literal question mark, got %q", decoded)
		}
		if ImpossiblePath(decoded) {
			t.Errorf("ImpossiblePath(%q) = true, want false", decoded)
		}
	}
}

func TestImpossiblePathNeverSeesAQueryValue(t *testing.T) {
	t.Parallel()

	// The matcher receives the decoded request-target path only
	// ([url.URL.Path]); a query string is never part of its input, so a hostile
	// query value is structurally unreachable (requirement 3.2 / 9.4).
	query := "?file=/etc/passwd&path=../../../../etc/passwd&x=<script>alert(1)</script>&q='+OR+1%3D1--+"
	target, _, found := strings.Cut("/v1/chat/completions"+query, "?")
	if !found {
		t.Fatal("fixture must carry a query string")
	}
	if ImpossiblePath(target) {
		t.Fatalf("ImpossiblePath(%q) = true, want false", target)
	}
	if strings.Contains(target, "?") {
		t.Fatal("the matcher input must be the decoded path, never the request target with its query")
	}
}

// frozenRule is one audited entry of the frozen rule tables, carrying the probe
// targets that must decide it. The families are listed in the order
// [ImpossiblePath] evaluates them: traversal structure first, then the exact,
// prefix and segment tables in table order.
type frozenRule struct {
	family string
	rule   string
	probes []string
}

// auditedRuleSet flattens the three frozen tables into evaluation order. It uses
// the production matcher primitives, never a reimplementation, so the audit
// cannot drift from the implementation it audits.
func auditedRuleSet() []frozenRule {
	rules := make([]frozenRule, 0, maxAuditedImpossiblePathRules)
	for _, rule := range impossibleExactPaths {
		rules = append(rules, frozenRule{
			family: "exact",
			rule:   rule,
			probes: []string{rule, "/" + strings.ToUpper(rule[1:])},
		})
	}
	for _, rule := range impossiblePrefixPaths {
		rules = append(rules, frozenRule{
			family: "prefix",
			rule:   rule,
			probes: []string{rule, rule + "/nested"},
		})
	}
	for _, rule := range impossibleSegmentPrefixes {
		rules = append(rules, frozenRule{
			family: "segment",
			rule:   rule,
			probes: []string{"/" + rule, "/v1/" + rule + ".bak"},
		})
	}
	return rules
}

// frozenRuleDecides reports whether one audited entry decides the probe target.
func frozenRuleDecides(family, rule, probe string) bool {
	target := normalizeTarget(probe)
	switch family {
	case "exact":
		return matchesRootPath(target, rule)
	case "prefix":
		return matchesRootPrefix(target, rule)
	case "segment":
		return hasSegmentPrefix(target, rule)
	default:
		return false
	}
}

func TestImpossiblePathRuleSetIsSmallAndAuditable(t *testing.T) {
	t.Parallel()

	rules := auditedRuleSet()
	total := len(rules)
	if total == 0 {
		t.Fatal("the audited v1 impossible-path set must not be empty")
	}
	if total > maxAuditedImpossiblePathRules {
		t.Fatalf("audited rules = %d, want at most %d: the v1 set stays small and static", total, maxAuditedImpossiblePathRules)
	}
	// A rule repeated inside or across the frozen tables can never change a
	// decision, so a duplicate is silent dead weight in the audited set.
	owners := make(map[string]string, total)
	for _, entry := range rules {
		if !strings.HasPrefix(entry.rule, "/") && !strings.HasPrefix(entry.rule, ".") {
			t.Errorf("rule %q must be an absolute path or a dot-prefixed segment marker", entry.rule)
		}
		if entry.rule != strings.ToLower(entry.rule) {
			t.Errorf("rule %q must be stored lowercase; matching folds ASCII case", entry.rule)
		}
		if strings.Contains(entry.rule, "//") {
			t.Errorf("rule %q must not contain a double slash", entry.rule)
		}
		if owner, duplicate := owners[entry.rule]; duplicate {
			t.Errorf("rule %q is a duplicate: the %s and %s families both carry it, so one is dead weight", entry.rule, owner, entry.family)
			continue
		}
		owners[entry.rule] = entry.family
	}
	// Dead-rule audit: a frozen entry an earlier-evaluated rule already decides can
	// never change a decision either, so it is dead weight too. Traversal is
	// evaluated before every table, so a probe it already decides would make this
	// fixture invalid rather than dead.
	for i, entry := range rules {
		for _, probe := range entry.probes {
			if hasTraversal(normalizeTarget(probe)) {
				t.Fatalf("probe %q of %s rule %q is already decided by traversal, so the audit fixture is invalid", probe, entry.family, entry.rule)
			}
			for _, earlier := range rules[:i] {
				if frozenRuleDecides(earlier.family, earlier.rule, probe) {
					t.Errorf("%s rule %q is dead weight: the earlier %s rule %q already decides its probe %q", entry.family, entry.rule, earlier.family, earlier.rule, probe)
				}
			}
			if !ImpossiblePath(probe) {
				t.Errorf("%s rule %q does not decide its own probe %q", entry.family, entry.rule, probe)
			}
		}
	}
}

func TestImpossiblePathIsDeterministicAndSideEffectFree(t *testing.T) {
	t.Parallel()

	targets := []string{"/.env", "/v1/chat/completions", "/../etc/passwd", "/", "/wp-admin/install.php"}
	for _, target := range targets {
		first := ImpossiblePath(target)
		for range 4 {
			if again := ImpossiblePath(target); again != first {
				t.Fatalf("ImpossiblePath(%q) is not deterministic: %v then %v", target, first, again)
			}
		}
	}
}

func TestImpossiblePathIsAllocationFreeForStandardSurface(t *testing.T) {
	targets := []string{"/v1/chat/completions", "/v1/responses/resp_1/cancel", "/openresponses/v1/responses"}
	got := testing.AllocsPerRun(200, func() {
		for _, target := range targets {
			_ = ImpossiblePath(target)
		}
	})
	if got != 0 {
		t.Fatalf("allocations per run = %v, want 0 on the standard data-plane surface", got)
	}
}
