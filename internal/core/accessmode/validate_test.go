package accessmode

import (
	"errors"
	"sort"
	"testing"
)

func TestValidatePosture_singleUserLoopbackOnly(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"127.0.0.1:1", "127.0.0.2:1", "[::1]:1", "localhost:1"} {
		t.Run("allows_"+raw, func(t *testing.T) {
			t.Parallel()
			if err := ValidatePosture(PostureInput{
				Mode:   ModeSingleUser,
				Listen: ListenClassification{Raw: raw, Surface: SurfaceLoopback},
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, tc := range []struct {
		name string
		l    ListenClassification
		want error
	}{
		{"broad_ipv4", ListenClassification{Raw: "0.0.0.0:8080", Surface: SurfaceBroad}, ErrSingleUserBroadBind},
		{"bare_port", ListenClassification{Raw: ":8080", Surface: SurfaceBroad}, ErrSingleUserBroadBind},
		{"non_loopback", ListenClassification{Raw: "192.168.0.1:1", Surface: SurfaceNonLoopback}, ErrSingleUserNonLoopback},
		{"malformed", ListenClassification{Raw: "oops", Surface: SurfaceMalformed}, ErrSingleUserMalformedAddress},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidatePosture(PostureInput{Mode: ModeSingleUser, Listen: tc.l})
			if err == nil || !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
}

func TestValidatePosture_multiUserRequiresStrongAuth(t *testing.T) {
	t.Parallel()
	base := PostureInput{
		Mode:           ModeMultiUser,
		Listen:         ListenClassification{Raw: "0.0.0.0:8080", Surface: SurfaceBroad},
		LegacyAuthMode: "external",
	}
	if err := ValidatePosture(PostureInput{
		Mode:           base.Mode,
		Listen:         base.Listen,
		Handler:        "remote",
		RequiredLevel:  "api_key",
		LegacyAuthMode: "external",
	}); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePosture(PostureInput{
		Mode:           base.Mode,
		Listen:         base.Listen,
		Handler:        "local_api_key",
		RequiredLevel:  "api_key_sso",
		LegacyAuthMode: "external",
	}); err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		in   PostureInput
		want error
	}{
		"noop": {
			in: PostureInput{
				Mode: ModeMultiUser, Listen: base.Listen, Handler: "local_noop", RequiredLevel: "api_key",
				LegacyAuthMode: "external",
			},
			want: ErrMultiUserLocalNoopDisallowed,
		},
		"missing_handler": {
			in: PostureInput{
				Mode: ModeMultiUser, Listen: base.Listen, Handler: "", RequiredLevel: "api_key",
				LegacyAuthMode: "external",
			},
			want: ErrMultiUserHandlerRequired,
		},
		"none_level": {
			in: PostureInput{
				Mode: ModeMultiUser, Listen: base.Listen, Handler: "local_api_key", RequiredLevel: "none",
				LegacyAuthMode: "external",
			},
			want: ErrMultiUserRequiredLevelTooWeak,
		},
		"legacy_no_auth": {
			in: PostureInput{
				Mode: ModeMultiUser, Listen: base.Listen, Handler: "remote", RequiredLevel: "api_key",
				LegacyAuthMode: "no_auth",
			},
			want: ErrMultiUserIncompatibleNoAuth,
		},
	}
	names := make([]string, 0, len(cases))
	for n := range cases {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		tc := cases[name]
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := ValidatePosture(tc.in); err == nil || !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
}
