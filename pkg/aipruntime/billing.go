package aipruntime

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/aiproxer/aiproxer/internal/infra/billingbinding"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	sdkbilling "github.com/aiproxer/aiproxer/pkg/aipsdk/billing"
)

// BuildWithBilling constructs a production runtime with one explicit typed
// monetary binding. Ordinary Options stay non-money; the binding carries the
// cheap credit screen, quote/admission, terminal handoff, and lifecycle
// ports. BuildWithBilling delegates to the same host construction as Build:
// one BuildHost, one Host, one Manager, one reload path.
func BuildWithBilling(ctx context.Context, opts Options, binding sdkbilling.Binding) (*Runtime, error) {
	if err := sdkbilling.ValidateBindings([]sdkbilling.Binding{binding}); err != nil {
		return nil, fmt.Errorf("aipruntime: %w", err)
	}
	adapter, err := billingbinding.NewAdapter(binding)
	if err != nil {
		return nil, fmt.Errorf("aipruntime: %w", err)
	}
	// Attaching a monetary binding is an explicit capability request: the shared
	// assembly declares billing implicitly, without mutating the caller's slice.
	return buildCommon(ctx, opts, true, func(prod *runtimebundle.ProductionOptions) error {
		gate, admission, sink, identity := adapter.Chokepoints()
		prod.BillingCreditGate = gate
		prod.BillingExposureAdmission = admission
		prod.BillingTerminalUsageSink = sink
		prod.BillingIdentity = identity
		// The binding's optional maintenance attachment feeds the existing
		// production maintenance port unchanged (same type identity, no
		// conversion). A binding without the attachment leaves the port at its
		// stock nil value: no accounting and no error.
		if maintenance := adapter.MaintenanceObserver(); maintenance != nil {
			prod.MaintenanceAccounting = maintenance
		}
		return nil
	})
}

// buildCommon is the single shared assembly behind Build and BuildWithBilling.
// Both bind one complete Host via runtimebundle.BuildHost; only the
// production-port mutation (nil for stock non-money builds) and the implicit
// billing capability differ.
func buildCommon(ctx context.Context, opts Options, implyBilling bool, apply func(*runtimebundle.ProductionOptions) error) (*Runtime, error) {
	if ctx == nil {
		return nil, fmt.Errorf("aipruntime: nil context")
	}
	path := strings.TrimSpace(opts.ConfigPath)
	if path == "" {
		return nil, fmt.Errorf("aipruntime: empty config path")
	}
	norm, err := normalizeCanonicalOptions(opts, implyBilling)
	if err != nil {
		return nil, err
	}
	if err := validateObservationWriterDeclaration(opts); err != nil {
		return nil, err
	}
	logOut := opts.LogWriter
	if logOut == nil {
		logOut = io.Discard
	}
	prod := stockProduction(opts, norm)
	if apply != nil {
		if err := apply(&prod); err != nil {
			return nil, err
		}
	}
	host, err := runtimebundle.BuildHost(ctx, runtimebundle.BuildHostInput{
		ConfigPath:             path,
		Mandatory:              aipsdk.StandardDistributionRequirements(),
		LogWriter:              logOut,
		Production:             prod,
		HandlerComposer:        stdhttp.ComposeStandardHTTP,
		EnterpriseCapabilities: norm.EnterpriseCapabilities,
	})
	if err != nil {
		return nil, err
	}
	api, err := adaptHost(ctx, host)
	if err != nil {
		return nil, err
	}
	return newRuntime(api, host), nil
}

// stockProduction carries the non-money descriptor-bound registrations and
// observer seams shared by every build. Monetary ports stay unset here.
func stockProduction(opts Options, norm normalizedProduction) runtimebundle.ProductionOptions {
	return runtimebundle.ProductionOptions{
		MeteringRecorder:                   opts.MeteringRecorder,
		MeteringObservationSink:            opts.MeteringObservationSink,
		MeteringObservationWriterSelection: opts.MeteringObservationWriterSelection,
		RequestRegistrations:               norm.RequestRegistrations,
		AttemptRegistrations:               norm.AttemptRegistrations,
		ConcurrencyRegistration:            norm.ConcurrencyRegistration,
		UsageSnapshotSource:                opts.UsageSnapshotSource,
		ConcurrencySnapshotSource:          opts.ConcurrencySnapshotSource,
		RatingSnapshotSource:               opts.RatingSnapshotSource,
		EvidenceSink:                       opts.EvidenceSink,
		MeteringQuerier:                    opts.MeteringQuerier,
		TrafficObservers:                   opts.TrafficObservers,
		UsageObservers:                     opts.UsageObservers,
		PolicyObservers:                    opts.PolicyObservers,
		FeatureHostRegistrations:           norm.FeatureHostRegistrations,
		UsageLimits:                        opts.UsageLimits,
		AdminMounts:                        norm.AdminMounts,
		RegisterStartupMetrics:             opts.RegisterStartupMetrics,
	}
}
