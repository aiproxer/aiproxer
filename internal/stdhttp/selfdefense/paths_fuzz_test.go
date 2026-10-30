package selfdefense

import (
	"net/url"
	"strings"
	"testing"
)

// fuzzImpossiblePathSeeds seed the fuzzer with the audited probe families, the
// near-miss legitimate data-plane surface, the escaped-question-mark shape and
// the hostile structures the matcher scans for, so the engine starts inside and
// immediately around every audited decision.
var fuzzImpossiblePathSeeds = []string{
	"",
	"/",
	"/.env",
	"/.ENV",
	"/.envrc",
	"/v1/.env",
	"/.git/config",
	"/.htaccess",
	"/.ssh/id_rsa",
	"/.DS_Store",
	"/wp-admin",
	"/wp-admin/install.php",
	"/wp-login.php",
	"/xmlrpc.php",
	"/phpmyadmin/index.php",
	"/pma",
	"/adminer.php",
	"/cgi-bin/test-cgi",
	"/vendor/phpunit/phpunit/src/Util/PHP/eval-stdin.php",
	"/composer.json",
	"/server-status",
	"/info.php",
	"/phpinfo.php",
	"/web.config",
	"/../etc/passwd",
	"/..",
	"/v1/../../etc/passwd",
	`/v1/..\..\windows\win.ini`,
	"//.env",
	"///wp-admin/install.php",
	"/v1/chat/completions",
	"/v1/responses/resp_123/cancel",
	"/v1/messages",
	"/v1beta/models/gemini-2.5-pro:generateContent",
	"/openresponses/v1/responses",
	"/aip/v1/a-legs/leg-1/cancel",
	"/admin/config/reload",
	"/environment",
	"/v1/environment",
	"/v1/git/config",
	"/v1/settings",
	"/v1/htaccess-guide",
	"/v2/chat/completions",
	"/v1/chat/completions?path=../../../../etc/passwd",
	"/v1/chat/completions%3Fpath=../../../../etc/passwd",
	"/v1/chat/completions%3Fnext=/.env",
	"/%3F/../etc/passwd",
	"/v1/models%3Fnext=/info.php",
	"/.env?x=1",
	"/%2e%2e/etc/passwd",
	"/%2e%2e%2f%2e%2e%2fetc/shadow",
	"/..%2f..%2fetc/hosts",
	"/\x00/.env",
	"/%ff%fe/.env",
}

// FuzzImpossiblePath drives the hand-indexed byte scanner with attacker-controlled
// request targets. The matcher indexes raw bytes, looks for a '?' boundary and a
// '..' segment boundary, and slices the input, so a no-panic property over
// arbitrary bytes is the cheap minimum, and the query-independence property of
// requirement 3.2 and 9.4 is asserted for every generated target instead of only
// for the hand-picked fixtures.
func FuzzImpossiblePath(f *testing.F) {
	for _, seed := range fuzzImpossiblePathSeeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		// The raw bytes as an attacker would present them, including a target that
		// is not a valid URL at all.
		_ = ImpossiblePath(raw)

		parsed, err := url.Parse(raw)
		if err != nil {
			return
		}
		// The decoded path net/http would hand to the gate.
		decoded := parsed.Path
		decision := ImpossiblePath(decoded)

		// The decision must never depend on anything from the first question mark
		// onwards, so discarding the query text is decision-preserving. Without the
		// structural cut in normalizeTarget this property fails for any generated
		// target whose query smuggles a traversal or a dotfile marker.
		head, _, found := strings.Cut(decoded, "?")
		if found && ImpossiblePath(head) != decision {
			t.Fatalf("query text changed the decision: ImpossiblePath(%q) = %v, ImpossiblePath(%q) = %v", decoded, decision, head, ImpossiblePath(head))
		}
		// A target that reduces to the empty or root path is never a probe, whatever
		// the query carried.
		if found && decision && (head == "" || head == "/") {
			t.Fatalf("a target whose path is %q must never be an impossible path, query %q", head, decoded)
		}
	})
}
