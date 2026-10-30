package runtimebundle

import (
	"time"

	"github.com/aiproxer/aiproxer/internal/core/billingport"
	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	runtimecore "github.com/aiproxer/aiproxer/internal/core/runtime"
	terminalworkapp "github.com/aiproxer/aiproxer/internal/core/terminalwork/app"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/featurehost"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
	"github.com/prometheus/client_golang/prometheus"
)

// ProductionOptions carries enterprise/production injection seams (reqs 12.1, 12.3, 12.4).
// Canonical host construction accepts descriptor-bound registrations only.
type ProductionOptions struct {
	BillingTerminalUsageSink billingport.TerminalUsageSink
	BillingIdentity          runtimecore.BillingIdentity
	// MaintenanceAccounting receives provider-authoritative maintenance usage on
	// behalf of background maintenance operations (such as prompt cache keepwarm).
	MaintenanceAccounting billingport.ProviderMaintenanceUsageObserver
	// BillingCreditGate is the required pre-route settled-credit screen for
	// authoritative billing. It is intentionally separate from detailed post-route
	// exposure admission.
	BillingCreditGate runtimecore.BillingCreditGate
	// BillingExposureAdmission is the authoritative post-route operational
	// exposure seam carried by the public binding.
	BillingExposureAdmission runtimecore.BillingExposureAdmission
	MeteringRecorder         metering.Recorder
	// MeteringObservationSink is the borrowed atomic observation sink.
	MeteringObservationSink metering.AtomicObservationSink
	// MeteringObservationWriterSelection is the carried writer declaration.
	MeteringObservationWriterSelection coremetering.ObservationWriterSelection
	// MeteringAccountWindowStore is the optional nonfinancial gauge query port
	// used by an explicitly configured provider quota policy. It is separate
	// from all billing and customer-unit seams.
	MeteringAccountWindowStore coremetering.AccountWindowStore
	RequestRegistrations       []authority.RequestRegistration
	AttemptRegistrations       []authority.AttemptRegistration
	ConcurrencyRegistration    *authority.ConcurrencyRegistration
	UsageSnapshotSource        economics.RuleSnapshotSource
	ConcurrencySnapshotSource  economics.RuleSnapshotSource
	RatingSnapshotSource       economics.RatingSnapshotSource
	EvidenceSink               authority.EvidenceSink
	MeteringQuerier            metering.Querier
	TrafficObservers           []traffic.Observer
	UsageObservers             []usage.Observer
	PolicyObservers            []policydecision.Observer
	// Terminal-work processor ownership (tasks 4.4–4.5). When TerminalWorkStore is
	// set, Build constructs processor/registry/intents, starts the processor, and
	// injects IntentService into the executor.
	//
	// EffectProviders are composed as: RequestRegistrations -> AuthorityRequestEffectProvider
	// adapters (by descriptor ID), then TerminalWorkProviders merged by ProviderID with
	// explicit entries overriding derived adapters for the same ID.
	TerminalWorkStore          terminalworkapp.RecoveryStore
	TerminalWorkProviders      []terminalworkapp.EffectProvider
	TerminalWorkOwnerID        string
	TerminalWorkClaimTTL       time.Duration
	TerminalWorkClaimLimit     int
	TerminalWorkGlobalMax      int
	TerminalWorkPerProviderMax int
	TerminalWorkTickInterval   time.Duration
	TerminalWorkRenewInterval  time.Duration
	// FeatureHostRegistrations carries startup-only host-feature bindings
	// (Task 8.3/8.4, Requirements 9.3-9.6).
	FeatureHostRegistrations []featurehost.Registration
	// UsageLimits is the injected usage-limit port (mirror of
	// pkg/aipruntime.Options.UsageLimits); captured once by NewProcessServices.
	UsageLimits usagelimit.Authority
	// AdminMounts are protected operator handlers mounted under the admin
	// prefix by the standard HTTP layer; the public face is
	// pkg/aipruntime.AdminMount (mounted by task 4.2).
	AdminMounts []AdminMount
	// RegisterStartupMetrics optionally registers Enterprise collectors on the
	// process registry once during construction; nil/disabled metrics skip it,
	// an error aborts startup, and reload never re-runs it.
	RegisterStartupMetrics func(prometheus.Registerer) error
}

// AdminMount is one operator handler for a protected admin path; the public
// face is pkg/aipruntime.AdminMount, and the mount contract lives in the
// cycle-neutral stdhttp contract package.
type AdminMount = httpcontract.AdminMount

// HasAuthorityOverrides reports whether production authority providers are set.
func (p ProductionOptions) HasAuthorityOverrides() bool {
	return len(p.RequestRegistrations) > 0 || len(p.AttemptRegistrations) > 0 ||
		p.ConcurrencyRegistration != nil
}
