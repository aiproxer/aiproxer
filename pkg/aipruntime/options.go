package aipruntime

import (
	"io"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/featurehost"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
	"github.com/prometheus/client_golang/prometheus"
)

// Options configures public production composition (requirement 12.3, 12.4).
//
// Provider injection uses descriptor-bound registrations only:
// RequestRegistrations, AttemptRegistrations, and ConcurrencyRegistration.
// FeatureHostRegistrations carries typed startup-only host-feature bindings.
type Options struct {
	// ConfigPath is the YAML runtime config path (required).
	ConfigPath string
	// LogWriter receives bootstrap logger output; nil means discard for library use.
	LogWriter io.Writer

	// MeteringRecorder overrides the config-built metering journal recorder.
	MeteringRecorder metering.Recorder
	// MeteringObservationSink is the optional borrowed atomic observation sink.
	// It is the second half of the explicit external observation-writer pair.
	MeteringObservationSink metering.AtomicObservationSink
	// MeteringObservationWriterSelection declares the process observation writer.
	MeteringObservationWriterSelection ObservationWriterSelection

	// RequestRegistrations bind descriptor, priority, and request authority.
	RequestRegistrations []authority.RequestRegistration
	// AttemptRegistrations bind descriptor, priority, and attempt authority.
	AttemptRegistrations    []authority.AttemptRegistration // ConcurrencyRegistration binds descriptor and concurrency authority.
	ConcurrencyRegistration *authority.ConcurrencyRegistration

	// UsageSnapshotSource supplies the usage-authority source-fetch metadata
	// view for publication and RefreshSnapshots. It is not enforcement evidence;
	// executable generations bind descriptor-bound registrations instead.
	UsageSnapshotSource economics.RuleSnapshotSource
	// ConcurrencySnapshotSource supplies the concurrency source-fetch metadata
	// view for publication and RefreshSnapshots.
	ConcurrencySnapshotSource economics.RuleSnapshotSource
	// RatingSnapshotSource supplies the rating/catalog source-fetch metadata
	// view for publication and RefreshSnapshots.
	RatingSnapshotSource economics.RatingSnapshotSource
	// EvidenceSink projects authority decisions into policy and control-plane evidence.
	EvidenceSink authority.EvidenceSink
	// MeteringQuerier is the enterprise metering query mount (requirement 12.1).
	// Bounded control-plane query filter expansion remains Phase 11.
	MeteringQuerier metering.Querier
	// TrafficObservers are merged with feature-bundle traffic observers.
	TrafficObservers []traffic.Observer
	// UsageObservers are merged with feature-bundle usage observers.
	UsageObservers []usage.Observer
	// PolicyObservers are chained into policy-decision evidence (additive to EvidenceSink).
	PolicyObservers []policydecision.Observer
	// FeatureHostRegistrations carries startup-only host-feature bindings
	// (Requirement 9, Task 8.4).
	FeatureHostRegistrations []featurehost.Registration

	// EnterpriseCapabilities declares provided capabilities; nil fails closed
	// for Enterprise-only accounting keys. BuildWithBilling declares billing.
	EnterpriseCapabilities []EnterpriseCapability
	// UsageLimits is the caller-owned usage-limit implementation (admission,
	// settlement, release, usage application). The host captures the port once
	// at process build and every reloaded generation reuses it; a nil port keeps
	// the runtime's admit-all behavior, and a typed-nil port is rejected before
	// any I/O.
	//
	// Borrowed-attachment contract. One mutable instance is bound to exactly one
	// host build attempt, not merely one successful host: do not share it across
	// concurrent hosts or reuse it after a failed build or a closed host, and
	// distinct wrappers around one mutable service do not isolate it. Before
	// passing the port its owner must stop and join any external calls or
	// background producers. The runtime never closes the port or its store and
	// never registers it as an owned resource. Startup setters (the only
	// reconfiguration surface, probed from the optional method sets below) run
	// once before any authority operation and are never re-run during service or
	// reload; a retained callback becomes unusable after failed assembly or host
	// teardown and the owner retires the instance. A setter-less port is not
	// restricted by this rule; its own storage/scope contract decides sharing.
	//
	// Optional surfaces, each publicly nameable by a closed module:
	//   - controlplane.AccountingStatusReader: readiness status component;
	//   - Limits(context.Context, controlplane.AccountingLimitStatusQuery) and
	//     Decisions(...) returning UsageLimitStatusResult /
	//     UsageDecisionHistoryResult: protected admin query routes (absent =>
	//     routes stay disabled);
	//   - SetStageMetrics(UsageStageMetrics): process stage metrics;
	//   - SetEvidenceSink(aipsdk/authority.EvidenceSink): process evidence.
	UsageLimits UsageLimits
	// AdminMounts are protected operator handlers (mounted by task 4.2).
	AdminMounts []AdminMount
	// RegisterStartupMetrics optionally registers Enterprise-owned Prometheus
	// collectors once at startup; an error aborts the build before serving.
	RegisterStartupMetrics func(prometheus.Registerer) error
}
