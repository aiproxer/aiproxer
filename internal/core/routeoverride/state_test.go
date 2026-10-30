package routeoverride

import (
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestNormalizeSelector_trimsWhitespace(t *testing.T) {
	t.Parallel()
	if got := NormalizeSelector("  openai:gpt-4\n"); got != "openai:gpt-4" {
		t.Fatalf("got %q", got)
	}
}

func TestInactive_revisionZeroInvariants(t *testing.T) {
	t.Parallel()
	s := Inactive("a_1")
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if s.Active || s.Selector != "" || s.Revision != 0 || !s.UpdatedAt.IsZero() {
		t.Fatalf("inactive zero: %+v", s)
	}
}

func TestStateValidate_table(t *testing.T) {
	t.Parallel()
	now := time.Unix(1700000000, 0).UTC()
	cases := []struct {
		name    string
		state   State
		wantErr bool
	}{
		{name: "revision0", state: Inactive("a"), wantErr: false},
		{
			name:    "active",
			state:   State{ALegID: "a", Active: true, Selector: "openai:gpt-4", Revision: 1, UpdatedAt: now},
			wantErr: false,
		},
		{
			name:    "inactiveTombstone",
			state:   State{ALegID: "a", Active: false, Revision: 2, UpdatedAt: now},
			wantErr: false,
		},
		{
			name:    "revision0Active",
			state:   State{ALegID: "a", Active: true, Selector: "x", Revision: 0},
			wantErr: true,
		},
		{
			name:    "activeEmptySelector",
			state:   State{ALegID: "a", Active: true, Revision: 1, UpdatedAt: now},
			wantErr: true,
		},
		{
			name:    "inactiveKeepsSelector",
			state:   State{ALegID: "a", Selector: "stale", Revision: 1, UpdatedAt: now},
			wantErr: true,
		},
		{
			name: "selectorTooLarge",
			state: State{
				ALegID:    "a",
				Active:    true,
				Selector:  strings.Repeat("a", aipapi.MaxRouteSelectorBytes+1),
				Revision:  1,
				UpdatedAt: now,
			},
			wantErr: true,
		},
		{
			name:    "missingUpdatedAt",
			state:   State{ALegID: "a", Active: true, Selector: "x", Revision: 1},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.state.Validate()
			if tc.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !tc.wantErr && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestStateClone_isValueCopy(t *testing.T) {
	t.Parallel()
	orig := State{ALegID: "a", Active: true, Selector: "openai:gpt-4", Revision: 1, UpdatedAt: time.Unix(1, 0)}
	cp := orig.Clone()
	cp.Selector = "mutated"
	if orig.Selector != "openai:gpt-4" {
		t.Fatal("clone must not alias selector storage")
	}
}
