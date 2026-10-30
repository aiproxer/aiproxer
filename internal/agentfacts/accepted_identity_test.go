package agentfacts

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/identity"
)

func TestMatchIdentityUsesAcceptedBoundedUserAgent(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		raw        string
		wantAccept bool
		wantFamily Family
	}{
		{name: "odd ASCII casing is normalized", raw: "  CoDeX_CLi_Rs/1.2.3  ", wantAccept: true, wantFamily: FamilyCodex},
		{name: "Unicode letter remains part of adjacent identity token", raw: "écodex_cli_rs/1.2.3", wantAccept: true},
		{name: "Unicode separator allows a token boundary", raw: "é CODEX_CLI_RS/1.2.3", wantAccept: true, wantFamily: FamilyCodex},
		{name: "oversized identity is rejected before matching", raw: strings.Repeat("x", identity.MaxUserAgentBytes+1) + "codex_cli_rs/1.2.3"},
		{name: "embedded control identity is rejected before matching", raw: "codex_cli_rs/\x001.2.3"},
		{name: "Unicode control identity is rejected before matching", raw: "codex_cli_rs/1.2.3\u0085noise"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			accepted, ok := identity.AcceptClientUserAgent(tc.raw)
			if ok != tc.wantAccept {
				t.Fatalf("AcceptClientUserAgent(%q) accepted=%t, want %t", tc.raw, ok, tc.wantAccept)
			}
			if !ok {
				if accepted != "" {
					t.Fatalf("rejected User-Agent returned nonempty value %q", accepted)
				}
				// Rejected raw bytes are deliberately never passed to MatchIdentity.
				return
			}
			if len(accepted) > identity.MaxUserAgentBytes {
				t.Fatalf("accepted User-Agent is %d bytes, over the capture bound", len(accepted))
			}
			match, matched := MatchIdentity(accepted)
			if tc.wantFamily == "" {
				if matched {
					t.Fatalf("MatchIdentity(%q) = %+v, true; want no stable family match", accepted, match)
				}
				return
			}
			if !matched || match.Family != tc.wantFamily || match.Confidence != ConfidenceHigh {
				t.Fatalf("MatchIdentity(%q) = (%+v, %t), want high-confidence %q", accepted, match, matched, tc.wantFamily)
			}
		})
	}
}
