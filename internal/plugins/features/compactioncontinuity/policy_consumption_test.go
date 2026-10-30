package compactioncontinuity

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/policy"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

func TestEffectiveConfigConsumesOnlyTrustedSessionPolicy(t *testing.T) {
	t.Parallel()
	cfg, err := (Config{Extractor: ExtractorConfig{Enabled: true, Route: "route:default"}}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	p := &Plugin{cfg: cfg}
	trusted := execctx.WithViews(context.Background(), execctx.Views{
		Scope:   scope.PrincipalScopeView{PrincipalID: scope.Known("principal-1")},
		Session: session.SessionView{AuthoritativeSessionID: "session-1"},
	})
	override := policy.Override{Route: "route:approved", RouteSet: true, Limits: policy.LimitOverride{MaxInputTokens: new(6000)}}
	got, ok := p.effectiveConfig(policy.WithTrustedOverride(trusted, override))
	if !ok || got.Extractor.Route != "route:approved" || got.Extractor.MaxInputTokens != 6000 {
		t.Fatalf("trusted policy was not consumed: ok=%v cfg=%+v", ok, got)
	}
	untrusted, ok := p.effectiveConfig(policy.WithTrustedOverride(context.Background(), override))
	if !ok || untrusted.Extractor.Route != "route:default" || untrusted.Extractor.MaxInputTokens == 6000 {
		t.Fatalf("untrusted policy changed feature config: ok=%v cfg=%+v", ok, untrusted)
	}
}

func TestEffectiveConfigTrustedDisableStopsContinuityWork(t *testing.T) {
	t.Parallel()
	cfg, err := (Config{Extractor: ExtractorConfig{Enabled: true, Route: "route:default"}}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	p := &Plugin{cfg: cfg}
	ctx := execctx.WithViews(context.Background(), execctx.Views{Session: session.SessionView{AuthoritativeSessionID: "session-1"}})
	ctx = policy.WithTrustedOverride(ctx, policy.Override{Enabled: new(false)})
	if _, ok := p.effectiveConfig(ctx); ok {
		t.Fatal("trusted session disable did not stop continuity work")
	}
}
