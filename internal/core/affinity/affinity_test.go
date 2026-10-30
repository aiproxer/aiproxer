package affinity

import (
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

func TestResolveKey(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		in      IdentityInput
		want    Key
		wantOK  bool
		wantErr error
	}{
		{
			name: "session authoritative first",
			in: IdentityInput{
				Mode:    routing.AffinitySession,
				Session: session.SessionView{AuthoritativeSessionID: "auth", ClientSessionHint: "hint"},
			},
			want:   Key{Scope: ScopeSession, ID: "auth"},
			wantOK: true,
		},
		{
			name:   "session hint fallback",
			in:     IdentityInput{Mode: routing.AffinitySession, Session: session.SessionView{ClientSessionHint: "hint"}},
			want:   Key{Scope: ScopeSession, ID: "hint"},
			wantOK: true,
		},
		{
			name:   "client principal",
			in:     IdentityInput{Mode: routing.AffinityClient, Principal: execview.PrincipalView{ID: "user-1"}},
			want:   Key{Scope: ScopeClient, ID: "user-1"},
			wantOK: true,
		},
		{
			name:   "none",
			in:     IdentityInput{Mode: routing.AffinityNone},
			wantOK: false,
		},
		{
			name:    "missing fail closed",
			in:      IdentityInput{Mode: routing.AffinityClient, MissingIdentityPolicy: MissingIdentityFailClosed},
			wantErr: ErrIdentityRequired,
		},
		{
			name:   "missing ignore",
			in:     IdentityInput{Mode: routing.AffinityClient, MissingIdentityPolicy: MissingIdentityIgnore},
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok, err := ResolveKey(tc.in)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err got %v want %v", err, tc.wantErr)
			}
			if ok != tc.wantOK || got != tc.want {
				t.Fatalf("got (%+v, %v) want (%+v, %v)", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}
