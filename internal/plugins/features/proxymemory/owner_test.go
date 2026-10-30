package proxymemory

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

func TestCheckEligible(t *testing.T) {
	t.Parallel()

	allowAlice := Config{Principals: []string{"alice"}}
	allowWildcard := Config{Principals: []string{"*"}}
	allowLocalAlice := Config{Principals: []string{"alice"}, AllowLocalPrincipal: true}

	cases := []struct {
		name       string
		cfg        Config
		sc         scope.PrincipalScopeView
		wantOK     bool
		wantReason Ineligible
		want       Principal
	}{
		{
			name:       "an unknown principal is refused",
			cfg:        allowAlice,
			sc:         scope.PrincipalScopeView{},
			wantReason: IneligibleUnknownPrincipal,
		},
		{
			name:       "a known but blank principal is refused",
			cfg:        allowAlice,
			sc:         scope.PrincipalScopeView{PrincipalID: scope.Known("   ")},
			wantReason: IneligibleUnknownPrincipal,
		},
		{
			name:       "an internal origin is refused",
			cfg:        allowAlice,
			sc:         scope.PrincipalScopeView{PrincipalID: scope.Known("alice"), Origin: scope.OriginInternal},
			wantReason: IneligibleInternal,
		},
		{
			name:       "the local fallback principal is refused even under the wildcard",
			cfg:        allowWildcard,
			sc:         scope.PrincipalScopeView{PrincipalID: scope.Known(LocalFallbackPrincipalID), SubjectKind: scope.SubjectLocal},
			wantReason: IneligibleFallback,
		},
		{
			name:       "the local subject is refused by default",
			cfg:        allowAlice,
			sc:         scope.PrincipalScopeView{PrincipalID: scope.Known("alice"), SubjectKind: scope.SubjectLocal},
			wantReason: IneligibleLocal,
		},
		{
			name:       "the local subject refusal wins over the allowlist check",
			cfg:        allowAlice,
			sc:         scope.PrincipalScopeView{PrincipalID: scope.Known("bob"), SubjectKind: scope.SubjectLocal},
			wantReason: IneligibleLocal,
		},
		{
			name:   "the local subject is eligible when allowed and allowlisted",
			cfg:    allowLocalAlice,
			sc:     scope.PrincipalScopeView{PrincipalID: scope.Known("alice"), SubjectKind: scope.SubjectLocal},
			wantOK: true,
			want:   Principal{PrincipalID: "alice"},
		},
		{
			name:       "a principal outside the allowlist is refused",
			cfg:        allowAlice,
			sc:         scope.PrincipalScopeView{PrincipalID: scope.Known("bob")},
			wantReason: IneligibleNotAllowlisted,
		},
		{
			name:       "the allowlist match is case-sensitive",
			cfg:        allowAlice,
			sc:         scope.PrincipalScopeView{PrincipalID: scope.Known("Alice")},
			wantReason: IneligibleNotAllowlisted,
		},
		{
			name:   "an allowlisted principal is eligible",
			cfg:    allowAlice,
			sc:     scope.PrincipalScopeView{PrincipalID: scope.Known("alice")},
			wantOK: true,
			want:   Principal{PrincipalID: "alice"},
		},
		{
			name:   "the wildcard admits any client principal",
			cfg:    allowWildcard,
			sc:     scope.PrincipalScopeView{PrincipalID: scope.Known("bob")},
			wantOK: true,
			want:   Principal{PrincipalID: "bob"},
		},
		{
			name:       "the wildcard still refuses the local subject by default",
			cfg:        allowWildcard,
			sc:         scope.PrincipalScopeView{PrincipalID: scope.Known("bob"), SubjectKind: scope.SubjectLocal},
			wantReason: IneligibleLocal,
		},
		{
			name:   "a known tenant maps to TenantID",
			cfg:    allowAlice,
			sc:     scope.PrincipalScopeView{PrincipalID: scope.Known("alice"), TenantID: scope.Known("x")},
			wantOK: true,
			want:   Principal{TenantID: "x", PrincipalID: "alice"},
		},
		{
			name:   "an unknown tenant maps to an empty TenantID",
			cfg:    allowAlice,
			sc:     scope.PrincipalScopeView{PrincipalID: scope.Known("alice")},
			wantOK: true,
			want:   Principal{PrincipalID: "alice"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, reason, ok := CheckEligible(tc.cfg, tc.sc)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v (reason %q), want %v", ok, reason, tc.wantOK)
			}
			if tc.wantOK {
				if got != tc.want {
					t.Fatalf("principal = %+v, want %+v", got, tc.want)
				}
				if reason != "" {
					t.Fatalf("reason = %q, want empty on an eligible request", reason)
				}
				return
			}
			if reason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", reason, tc.wantReason)
			}
			if got != (Principal{}) {
				t.Fatalf("principal = %+v, want the zero principal on refusal", got)
			}
		})
	}
}

func TestCheckEligibleHasSession(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		view session.SessionView
		want bool
	}{
		{
			name: "an empty view has no session",
			view: session.SessionView{},
			want: false,
		},
		{
			name: "an authoritative session id alone counts",
			view: session.SessionView{AuthoritativeSessionID: "sess-1"},
			want: true,
		},
		{
			name: "a client session hint alone counts",
			view: session.SessionView{ClientSessionHint: "hint-1"},
			want: true,
		},
		{
			name: "a whitespace-only authoritative id does not count",
			view: session.SessionView{AuthoritativeSessionID: "   "},
			want: false,
		},
		{
			name: "a whitespace-only client hint does not count",
			view: session.SessionView{ClientSessionHint: "\t"},
			want: false,
		},
		{
			name: "whitespace-only values on both fields do not count",
			view: session.SessionView{AuthoritativeSessionID: " ", ClientSessionHint: "\n"},
			want: false,
		},
		{
			name: "a padded value still counts",
			view: session.SessionView{ClientSessionHint: " hint "},
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := HasSession(tc.view); got != tc.want {
				t.Fatalf("HasSession(%+v) = %v, want %v", tc.view, got, tc.want)
			}
		})
	}
}
