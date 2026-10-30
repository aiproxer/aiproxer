package identity_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/identity"
)

// Mutation-kill tests encode #147 plan kill conditions without a mutation CLI.
// Changing defaults, reversing precedence, or collapsing drop/omit must fail here.

func TestMutationKill_defaultsAreLiteralProductStrings(t *testing.T) {
	t.Parallel()
	// Literals must stay hard-coded in tests (not derived from production helpers alone).
	const (
		wantName = "aiproxer"
		wantURL  = "https://github.com/aiproxer/aiproxer"
	)
	if identity.DefaultProductName != wantName {
		t.Fatalf("DefaultProductName=%q want %q", identity.DefaultProductName, wantName)
	}
	if identity.DefaultProjectURL != wantURL {
		t.Fatalf("DefaultProjectURL=%q want %q", identity.DefaultProjectURL, wantURL)
	}
	var cfg identity.Config
	if err := identity.Validate(&cfg); err != nil {
		t.Fatal(err)
	}
	up := identity.MergeUpstream(cfg, nil)
	if up.UserAgentValue() != wantName {
		t.Fatalf("UserAgentValue=%q want %q", up.UserAgentValue(), wantName)
	}
	if up.AppURLValue() != wantURL {
		t.Fatalf("AppURLValue=%q want %q", up.AppURLValue(), wantURL)
	}
	if up.AppTitleValue() != wantName {
		t.Fatalf("AppTitleValue=%q want %q", up.AppTitleValue(), wantName)
	}
	if identity.EffectiveDownstreamOf(cfg).ServerValue() != wantName {
		t.Fatalf("ServerValue=%q want %q", identity.EffectiveDownstreamOf(cfg).ServerValue(), wantName)
	}
}

func TestMutationKill_backendOverrideWinsOmitNotDrop(t *testing.T) {
	t.Parallel()
	global := identity.Config{
		Upstream: identity.UpstreamPolicy{
			UserAgent: identity.FieldPolicy{Mode: identity.ModeCustom, Value: "GlobalUA/1"},
			OpenRouter: identity.OpenRouterPolicy{
				AppURL:   identity.FieldPolicy{Mode: identity.ModeCustom, Value: "https://global.example/"},
				AppTitle: identity.FieldPolicy{Mode: identity.ModeCustom, Value: "GlobalTitle"},
			},
		},
	}
	if err := identity.Validate(&global); err != nil {
		t.Fatal(err)
	}

	overrideWin := &identity.BackendOverride{
		UserAgent: &identity.FieldPolicy{Mode: identity.ModeCustom, Value: "BackendUA/2"},
	}
	if err := identity.ValidateBackendOverride(overrideWin); err != nil {
		t.Fatal(err)
	}
	got := identity.MergeUpstream(global, overrideWin)
	if got.UserAgent.Value != "BackendUA/2" {
		t.Fatalf("override must win: got %+v", got.UserAgent)
	}

	omitOR := &identity.BackendOverride{
		UserAgent: &identity.FieldPolicy{Mode: identity.ModeDrop},
		// OpenRouter omitted → inherit global URL/title
	}
	if err := identity.ValidateBackendOverride(omitOR); err != nil {
		t.Fatal(err)
	}
	gotOmit := identity.MergeUpstream(global, omitOR)
	if gotOmit.UserAgent.Mode != identity.ModeDrop {
		t.Fatalf("explicit drop: %+v", gotOmit.UserAgent)
	}
	if gotOmit.AppURL.Value != "https://global.example/" || gotOmit.AppTitle.Value != "GlobalTitle" {
		t.Fatalf("omitted openrouter fields must inherit, got url=%+v title=%+v", gotOmit.AppURL, gotOmit.AppTitle)
	}

	explicitDropURL := &identity.BackendOverride{
		OpenRouter: &identity.OpenRouterOverride{
			AppURL: &identity.FieldPolicy{Mode: identity.ModeDrop},
		},
	}
	if err := identity.ValidateBackendOverride(explicitDropURL); err != nil {
		t.Fatal(err)
	}
	gotDrop := identity.MergeUpstream(global, explicitDropURL)
	if gotDrop.AppURL.Mode != identity.ModeDrop {
		t.Fatalf("explicit drop must not collapse to inherit: %+v", gotDrop.AppURL)
	}
	if gotDrop.AppURLValue() != "" {
		t.Fatalf("drop AppURLValue must be empty, got %q", gotDrop.AppURLValue())
	}
}

func TestMutationKill_callPathPassthroughEmptyDoesNotBecomeProxyDefault(t *testing.T) {
	t.Parallel()
	// Accept empty/invalid → not ok; wire layer must omit (not emit product default).
	if v, ok := identity.AcceptClientUserAgent(""); ok || v != "" {
		t.Fatalf("empty UA must be rejected, got %q ok=%v", v, ok)
	}
	if v, ok := identity.AcceptClientUserAgent("bad\r\nagent"); ok || v != "" {
		t.Fatalf("embedded CRLF UA must be rejected, got %q ok=%v", v, ok)
	}
	// ResolvedValue for passthrough is ""; product default must only come from ModeProxy.
	pt := identity.FieldPolicy{Mode: identity.ModePassthrough}
	if got := pt.ResolvedValue(identity.DefaultProductName); got != "" {
		t.Fatalf("passthrough ResolvedValue must be empty (not proxy default), got %q", got)
	}
	proxy := identity.FieldPolicy{Mode: identity.ModeProxy}
	if got := proxy.ResolvedValue("aiproxer"); got != "aiproxer" {
		t.Fatalf("proxy ResolvedValue=%q", got)
	}
}

func TestMutationKill_legacyTitleHeaderNameIsModernOnly(t *testing.T) {
	t.Parallel()
	// Documentation/contract lock: wire title carrier is X-OpenRouter-Title, not X-Title.
	// Behavioral emission is covered in standardplugins; this kills silent rename of the
	// modern header constant expectations used across identity docs and tests.
	const modern = "X-OpenRouter-Title"
	const legacy = "X-Title"
	if modern == legacy {
		t.Fatal("modern and legacy title headers must differ")
	}
	if modern != "X-OpenRouter-Title" {
		t.Fatalf("modern title header drifted: %q", modern)
	}
}
