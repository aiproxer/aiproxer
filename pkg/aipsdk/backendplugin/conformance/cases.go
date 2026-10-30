package conformance

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// Run executes the advertised-capability conformance suite against svc with
// default fake-plugin configure YAML and strict execute proofs.
func Run(ctx context.Context, svc backendplugin.Service) Report {
	return RunWith(ctx, svc, Options{})
}

// RunWith executes the suite with connector-specific options.
func RunWith(ctx context.Context, svc backendplugin.Service, opts Options) Report {
	var rep Report
	if ctx == nil {
		ctx = context.Background()
	}
	rep.Results = append(rep.Results, caseLifecycle(ctx, svc, opts))
	rep.Results = append(rep.Results, caseCapabilityHonesty(ctx, svc, opts))
	rep.Results = append(rep.Results, caseProfileInventory(ctx, svc, opts))
	rep.Results = append(rep.Results, caseInventoryProvenance(ctx, svc, opts))
	rep.Results = append(rep.Results, caseEconomicV2Contract())
	if !opts.SkipExecute {
		rep.Results = append(rep.Results, caseExecuteOrdering(ctx, svc, opts))
	}
	rep.Results = append(rep.Results, caseCountBilling(ctx, svc, opts))
	rep.Results = append(rep.Results, caseClose(ctx, svc, opts))
	return rep
}

func caseLifecycle(ctx context.Context, svc backendplugin.Service, opts Options) Result {
	const name = "lifecycle.negotiate_describe_configure"
	inst, desc, err := configure(ctx, svc, "c-life", opts)
	if err != nil {
		return fail(name, err.Error(), "lifecycle")
	}
	_ = inst
	if desc.PluginID == "" || len(desc.Factories) == 0 {
		return fail(name, "empty descriptor", "lifecycle")
	}
	return pass(name)
}

func caseCapabilityHonesty(ctx context.Context, svc backendplugin.Service, opts Options) Result {
	const name = "capabilities.honesty"
	inst, desc, err := configure(ctx, svc, "c-caps", opts)
	if err != nil {
		return fail(name, err.Error(), "capabilities")
	}
	fac, err := advertisedFactoryKind(desc, opts.FactoryKind)
	if err != nil {
		return fail(name, err.Error(), "capabilities")
	}
	profile, err := inst.Resolve(ctx, nil)
	if err != nil {
		return fail(name, err.Error(), "capabilities")
	}
	if fac.StaticCapabilities.Streaming && !profile.Capabilities.Streaming {
		return fail(name, "streaming advertised but profile denies", "capabilities")
	}
	if fac.SupportsCountTokens != profile.SupportsCountTokens {
		return fail(name, "count_tokens mismatch", "capabilities")
	}
	if fac.SupportsFinalizeBilling != profile.SupportsFinalizeBilling {
		return fail(name, "finalize_billing mismatch", "capabilities")
	}
	return pass(name)
}

func caseProfileInventory(ctx context.Context, svc backendplugin.Service, opts Options) Result {
	const name = "inventory.bounds"
	inst, desc, err := configure(ctx, svc, "c-inv", opts)
	if err != nil {
		return fail(name, err.Error(), "inventory")
	}
	fac, err := advertisedFactoryKind(desc, opts.FactoryKind)
	if err != nil {
		return fail(name, err.Error(), "inventory")
	}
	if !fac.SupportsDynamicInventory {
		return pass(name)
	}
	out, err := inst.ListModels(ctx, 1)
	if err != nil {
		return fail(name, err.Error(), "inventory")
	}
	if err := out.Validate(1); err != nil {
		return fail(name, err.Error(), "inventory")
	}
	return pass(name)
}

// caseInventoryProvenance closes the static-list blind spot behind a dynamic
// claim: a factory advertising SupportsDynamicInventory must label where its
// models came from and when they were fetched. A hardcoded list served as
// "dynamic" inventory typically carries neither.
func caseInventoryProvenance(ctx context.Context, svc backendplugin.Service, opts Options) Result {
	const name = "inventory.provenance"
	inst, desc, err := configure(ctx, svc, "c-inv-prov", opts)
	if err != nil {
		return fail(name, err.Error(), "inventory")
	}
	fac, err := advertisedFactoryKind(desc, opts.FactoryKind)
	if err != nil {
		return fail(name, err.Error(), "inventory")
	}
	if !fac.SupportsDynamicInventory {
		return pass(name)
	}
	out, err := inst.ListModels(ctx, 0)
	if err != nil {
		return fail(name, err.Error(), "inventory")
	}
	if strings.TrimSpace(out.InventorySource) == "" {
		return fail(name, "dynamic inventory without InventorySource", "inventory")
	}
	if out.FetchedUnixMS <= 0 {
		return fail(name, "dynamic inventory without FetchedUnixMS", "inventory")
	}
	return pass(name)
}

func caseExecuteOrdering(ctx context.Context, svc backendplugin.Service, opts Options) Result {
	const name = "execute.one_terminal_ordering"
	inst, desc, err := configure(ctx, svc, "c-exec", opts)
	if err != nil {
		return fail(name, err.Error(), "execute")
	}
	fac, err := advertisedFactoryKind(desc, opts.FactoryKind)
	if err != nil {
		return fail(name, err.Error(), "execute")
	}
	inv := sampleInvocation(opts.SampleModel)
	frames, err := runExecute(ctx, inst, backendplugin.ClientFrame{
		Kind: backendplugin.ClientFrameStart, InstanceID: "c-exec", Invocation: &inv,
	})
	if err != nil {
		return fail(name, err.Error(), "execute")
	}
	if err := validateServerStream(frames); err != nil {
		return fail(name, err.Error(), "execute")
	}
	seen := map[backendplugin.EventKind]bool{}
	var sawTerminal, sawUsage bool
	for _, f := range frames {
		if f.Kind == backendplugin.ServerFrameTerminal {
			sawTerminal = true
		}
		if f.Kind == backendplugin.ServerFrameEvent && f.Event != nil {
			seen[f.Event.Kind] = true
			if f.Event.Kind == backendplugin.EventUsageDelta {
				sawUsage = true
			}
		}
	}
	if !sawTerminal {
		return fail(name, "missing terminal", "execute")
	}
	if !opts.DisableUsageRequirement && !sawUsage {
		return fail(name, "missing usage event", "execute")
	}
	if fac.StaticCapabilities.Streaming && !seen[backendplugin.EventTextDelta] {
		return fail(name, "advertised streaming missing text delta", "execute")
	}
	if fac.StaticCapabilities.Reasoning && !seen[backendplugin.EventReasoningDelta] {
		return fail(name, "advertised reasoning missing reasoning delta", "execute")
	}
	if fac.StaticCapabilities.Tools && !seen[backendplugin.EventToolCallStarted] {
		return fail(name, "advertised tools missing tool_call_started", "execute")
	}
	if fac.StaticCapabilities.Vision && !opts.VisionInputOnly && !seen[backendplugin.EventAssistantImageRef] {
		return fail(name, "advertised vision missing assistant image ref", "execute")
	}
	return pass(name)
}

func caseCountBilling(ctx context.Context, svc backendplugin.Service, opts Options) Result {
	const name = "accounting.count_finalize"
	inst, desc, err := configure(ctx, svc, "c-acct", opts)
	if err != nil {
		return fail(name, err.Error(), "accounting")
	}
	fac, err := advertisedFactoryKind(desc, opts.FactoryKind)
	if err != nil {
		return fail(name, err.Error(), "accounting")
	}
	if fac.SupportsCountTokens {
		counter, ok := inst.(backendplugin.TokenCounter)
		if !ok {
			return fail(name, "count advertised but interface missing", "accounting")
		}
		if _, err := counter.CountTokens(ctx, backendplugin.CountTokensRequest{
			InstanceID: "c-acct", ModelID: opts.SampleModel, Invocation: sampleInvocation(opts.SampleModel),
		}); err != nil {
			return fail(name, err.Error(), "accounting")
		}
	}
	if fac.SupportsFinalizeBilling {
		fin, ok := inst.(backendplugin.BillingFinalizer)
		if !ok {
			return fail(name, "finalize advertised but interface missing", "accounting")
		}
		if _, err := fin.FinalizeBilling(ctx, backendplugin.FinalizeBillingRequest{
			InstanceID: "c-acct", ALegID: "a", BLegID: "b", ModelID: "fake-model", IdempotencyKey: "k1",
		}); err != nil {
			return fail(name, err.Error(), "accounting")
		}
	}
	return pass(name)
}

func caseClose(ctx context.Context, svc backendplugin.Service, opts Options) Result {
	const name = "lifecycle.close"
	inst, _, err := configure(ctx, svc, "c-close", opts)
	if err != nil {
		return fail(name, err.Error(), "close")
	}
	if err := inst.Close(ctx); err != nil {
		return fail(name, err.Error(), "close")
	}
	return pass(name)
}

// RunBrokenMode checks that a broken fake mode fails with an exact ModeError.Code.
func RunBrokenMode(ctx context.Context, svc backendplugin.Service, wantCode string) Result {
	const name = "broken.mode"
	if ctx == nil {
		ctx = context.Background()
	}
	opts := Options{}
	inst, _, err := configure(ctx, svc, "c-broken", opts)
	if err != nil {
		var me backendplugin.ModeError
		if errors.As(err, &me) && me.Code == wantCode {
			return pass(name)
		}
		return fail(name, err.Error(), wantCode)
	}
	inv := sampleInvocation("")
	inbox := []backendplugin.ClientFrame{
		{Kind: backendplugin.ClientFrameStart, InstanceID: "c-broken", Invocation: &inv},
		{Kind: backendplugin.ClientFrameCancel, CancelReason: backendplugin.CancelReasonHost},
	}
	_, execErr := runExecute(ctx, inst, inbox...)
	if execErr == nil {
		return fail(name, "expected failure", wantCode)
	}
	var me backendplugin.ModeError
	if !errors.As(execErr, &me) || me.Code != wantCode {
		return fail(name, execErr.Error(), wantCode)
	}
	return pass(name)
}

// RunSlowModeDeadline verifies ModeSlowOutput fails with DiagSlowOutput under a short deadline.
func RunSlowModeDeadline(ctx context.Context, svc backendplugin.Service, wantCode string, deadline time.Duration) Result {
	const name = "broken.slow_deadline"
	opts := Options{}
	inst, _, err := configure(ctx, svc, "c-slow", opts)
	if err != nil {
		return fail(name, err.Error(), wantCode)
	}
	runCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	inv := sampleInvocation("")
	ms := &memStream{ctx: runCtx, inbox: []backendplugin.ClientFrame{
		{Kind: backendplugin.ClientFrameStart, InstanceID: "c-slow", Invocation: &inv},
	}}
	execErr := inst.Execute(ms)
	if execErr == nil {
		return fail(name, "expected deadline failure", wantCode)
	}
	var me backendplugin.ModeError
	if !errors.As(execErr, &me) || me.Code != wantCode {
		return fail(name, execErr.Error(), wantCode)
	}
	return pass(name)
}
