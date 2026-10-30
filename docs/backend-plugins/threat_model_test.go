package backendplugins_docs_test

import (
	"strings"
	"testing"
)

func TestThreatModel_CoversRequiredControls(t *testing.T) {
	t.Parallel()
	body := read(t, "threat-model.md")
	for _, want := range []string{
		"Trust equivalence",
		"not a malicious-code sandbox",
		"trust-equivalent",
		"TM-01",
		"TM-02",
		"TM-03",
		"TM-04",
		"TM-05",
		"TM-06",
		"TM-07",
		"TM-08",
		"TM-09",
		"TM-10",
		"SO_PEERCRED",
		"named pipe",
		"Fail closed",
		"stale-generation",
		"development mode",
		"backend-plugin-security-checks",
		"digest-bound",
		"symlink",
		"stderr",
		"canonical events",
		"diagredact",
		"[redacted]",
		"ADR 0008",
	} {
		if !strings.Contains(body, want) && !strings.Contains(strings.ToLower(body), strings.ToLower(want)) {
			t.Errorf("threat-model.md missing %q", want)
		}
	}
}

func TestThreatModel_DoesNotClaimSandbox(t *testing.T) {
	t.Parallel()
	body := strings.ToLower(read(t, "threat-model.md"))
	if strings.Contains(body, "malicious plugins are sandboxed") {
		t.Fatal("threat model must not claim malicious-plugin sandboxing")
	}
	if !strings.Contains(body, "not") || !strings.Contains(body, "sandbox") {
		t.Fatal("must explicitly deny sandbox equivalence")
	}
}
