package authoritycoord

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

const (
	defaultQuotaReaderPageSize = 128
	// Permit one terminal empty page after the maximum bounded observation
	// count (a one-observation-per-page reader is still complete).
	maxQuotaReaderPages = authority.MaxQuotaObservations + 1
	maxQuotaCursorBytes = 4096
)

var (
	// ErrQuotaReader identifies a bounded failure while reading the immutable
	// account-window evidence source. The underlying cause is available through
	// errors.Is/As, while Error deliberately contains no provider payload.
	ErrQuotaReader = errors.New("authoritycoord: quota observation reader failed")
	// ErrQuotaReaderProtocol identifies a reader that violates pagination or
	// bounded-result requirements. It is separate from store availability.
	ErrQuotaReaderProtocol = errors.New("authoritycoord: invalid quota observation reader response")
)

// QuotaReaderError is returned when an account-window reader cannot provide a
// complete immutable history. It is typed so a host can classify the result as
// unavailable without parsing provider text; the bounded Error string never
// includes the underlying provider error.
type QuotaReaderError struct {
	Operation string
	Cause     error
}

func (e *QuotaReaderError) Error() string {
	if e == nil {
		return ErrQuotaReader.Error()
	}
	return ErrQuotaReader.Error()
}

func (e *QuotaReaderError) Unwrap() []error {
	if e == nil || e.Cause == nil {
		return []error{ErrQuotaReader}
	}
	return []error{ErrQuotaReader, e.Cause}
}

// QuotaRequestProviderConfig binds one immutable policy to one account-window
// reader. Policy and all identity values are generation-scoped: rebuilding a
// host for a config reload creates a new provider and therefore freezes the
// policy reference for requests admitted by that generation.
type QuotaRequestProviderConfig struct {
	ID       string
	Policy   *authority.QuotaPolicy
	Store    coremetering.AccountWindowStore
	Now      func() time.Time
	PageSize int
}

// QuotaRequestProvider adapts nonfinancial provider allowance gauges to the
// generic request authority contract. It never emits reservations, exposure,
// customer credit, provider debit, or settlement money.
type QuotaRequestProvider struct {
	id       string
	policy   *authority.QuotaPolicy
	store    coremetering.AccountWindowStore
	now      func() time.Time
	pageSize int
}

// GenerationScopedRequestProvider marks quota evaluation as candidate-local.
// The runtime uses this marker to keep a reloaded quota policy active even
// when the request also binds an older process-level executable snapshot.
func (*QuotaRequestProvider) GenerationScopedRequestProvider() {}

// NewQuotaRequestProvider validates the immutable binding and constructs a
// request-stage provider. A nil reader is retained as unavailable telemetry so
// the policy's explicit unavailable action is evaluated instead of silently
// allowing a request.
func NewQuotaRequestProvider(cfg QuotaRequestProviderConfig) (*QuotaRequestProvider, error) {
	if cfg.Policy == nil {
		return nil, fmt.Errorf("authoritycoord: quota policy required")
	}
	if err := cfg.Policy.PolicyRef().Validate(); err != nil {
		return nil, fmt.Errorf("authoritycoord: quota policy reference: %w", err)
	}
	id := strings.TrimSpace(cfg.ID)
	if id == "" {
		id = "provider-quota:" + cfg.Policy.ID()
	}
	if len(id) > 512 {
		return nil, fmt.Errorf("authoritycoord: quota provider id exceeds 512 bytes")
	}
	pageSize := cfg.PageSize
	if pageSize == 0 {
		pageSize = defaultQuotaReaderPageSize
	}
	if pageSize < 1 || pageSize > authority.MaxQuotaObservations {
		return nil, fmt.Errorf("authoritycoord: quota reader page size must be between 1 and %d", authority.MaxQuotaObservations)
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &QuotaRequestProvider{id: id, policy: cfg.Policy, store: cfg.Store, now: now, pageSize: pageSize}, nil
}

// PolicyRef returns the immutable policy identity used by this provider.
func (p *QuotaRequestProvider) PolicyRef() authority.QuotaPolicyRef {
	if p == nil || p.policy == nil {
		return authority.QuotaPolicyRef{}
	}
	return p.policy.PolicyRef()
}

// Describe declares a required fail-closed request-stage authority. A host may
// explicitly wrap this provider in an advisory registration, but no adapter
// default turns unavailable telemetry into an implicit allow.
func (p *QuotaRequestProvider) Describe() authority.ProviderDescriptor {
	id := "provider-quota"
	if p != nil && strings.TrimSpace(p.id) != "" {
		id = p.id
	}
	return authority.ProviderDescriptor{
		ID:   id,
		Kind: authority.ProviderKindAuthority,
		Postures: []authority.StagePosture{{
			Stage: authority.StageRequestAdmit, Strength: authority.StrengthRequired,
			FailureBehavior: authority.FailureFailClosed,
		}},
	}
}

// AdmitRequest reads every page of the bound account-window history and maps
// the pure quota result into the generic request authority vocabulary.
func (p *QuotaRequestProvider) AdmitRequest(ctx context.Context, _ authority.RequestAdmission) (authority.Decision, error) {
	if p == nil || p.policy == nil {
		return authority.Decision{}, fmt.Errorf("authoritycoord: quota provider is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	at := p.now().UTC()
	if at.IsZero() {
		return authority.Decision{}, fmt.Errorf("authoritycoord: quota admission time is zero")
	}
	if err := ctx.Err(); err != nil {
		decision, evalErr := p.unavailableDecision(at)
		if evalErr != nil {
			return authority.Decision{}, quotaReaderError("before_read", err)
		}
		return decision, quotaReaderError("before_read", err)
	}
	observations, err := p.readObservations(ctx, at)
	if err != nil {
		// Preserve the typed reader failure for the coordinator/host while
		// still returning the policy's explicit unavailable posture to direct
		// callers. The generic coordinator will fail closed on the error; the
		// decision carries the nonfinancial status/ref when inspected at the
		// adapter boundary.
		decision, evalErr := p.unavailableDecision(at)
		if evalErr != nil {
			return authority.Decision{}, err
		}
		return decision, err
	}
	input := authority.QuotaEvaluationInput{At: at, Observations: observations, TelemetryUnavailable: p.store == nil}
	quotaDecision, err := p.policy.Evaluate(input)
	if err != nil {
		return authority.Decision{}, fmt.Errorf("authoritycoord: quota evaluation: %w", err)
	}
	return p.mapDecision(quotaDecision)
}

func (p *QuotaRequestProvider) unavailableDecision(at time.Time) (authority.Decision, error) {
	if p == nil || p.policy == nil {
		return authority.Decision{}, fmt.Errorf("authoritycoord: quota provider is not configured")
	}
	quotaDecision, err := p.policy.Evaluate(authority.QuotaEvaluationInput{
		At: at, TelemetryUnavailable: true,
	})
	if err != nil {
		return authority.Decision{}, fmt.Errorf("authoritycoord: unavailable quota evaluation: %w", err)
	}
	return p.mapDecision(quotaDecision)
}

func (p *QuotaRequestProvider) mapDecision(quotaDecision authority.QuotaDecision) (authority.Decision, error) {
	if p == nil {
		return authority.Decision{}, fmt.Errorf("authoritycoord: quota provider is not configured")
	}
	if err := quotaDecision.Validate(); err != nil {
		return authority.Decision{}, fmt.Errorf("authoritycoord: quota decision: %w", err)
	}
	ref := quotaDecision.Policy
	d := authority.Decision{
		ProviderID: p.id,
		Stage:      authority.StageRequestAdmit,
		Readiness:  quotaReadiness(quotaDecision),
		BoundVersions: []economics.PolicySnapshotRef{{
			VersionRef: economics.VersionRef{ID: ref.ID, Version: ref.Version},
			PolicyID:   ref.ID,
		}},
		Evidence: authority.SafeEvidence{
			Category:       "provider_quota",
			Code:           string(quotaDecision.Reason),
			Message:        "provider quota admission decision",
			RuleID:         ref.ID,
			ProviderID:     p.id,
			QuotaPolicyRef: quotaPolicyRefCopy(ref),
			EvidenceRefs:   append([]metering.ObservationRef(nil), quotaDecision.EvidenceRefs...),
			Attrs: map[string]string{
				"policy_method":   ref.Method,
				"policy_id":       ref.ID,
				"policy_version":  ref.Version,
				"policy_hash":     ref.Hash,
				"quota_reason":    string(quotaDecision.Reason),
				"evidence_status": string(quotaDecision.EvidenceStatus),
			},
		},
	}
	switch quotaDecision.Kind {
	case authority.QuotaDecisionAllow:
		d.Kind = authority.DecisionAllow
	case authority.QuotaDecisionDeny, authority.QuotaDecisionIndeterminate:
		// Generic authority has no indeterminate kind. An indeterminate quota
		// result is therefore a deny; a host can only weaken it through its
		// explicit advisory registration posture.
		d.Kind = authority.DecisionDeny
	default:
		return authority.Decision{}, fmt.Errorf("authoritycoord: quota evaluation returned unknown kind %q", quotaDecision.Kind)
	}
	return d, nil
}

// SettleRequest is intentionally nonfinancial. Quota gauge observations do
// not create a hold, so settlement returns an explicit unavailable result and
// carries no handle, money, quantity, or payable evidence.
func (p *QuotaRequestProvider) SettleRequest(_ context.Context, _ authority.RequestSettlement) (authority.Settlement, error) {
	ref := p.PolicyRef()
	return authority.Settlement{
		Kind: authority.SettlementUnavailable,
		BoundVersions: []economics.PolicySnapshotRef{{
			VersionRef: economics.VersionRef{ID: ref.ID, Version: ref.Version}, PolicyID: ref.ID,
		}},
	}, nil
}

// ReleaseRequest is a no-op because the quota adapter never acquires a hold.
func (p *QuotaRequestProvider) ReleaseRequest(context.Context, authority.RequestRelease) error {
	return nil
}

func (p *QuotaRequestProvider) readObservations(ctx context.Context, at time.Time) ([]metering.Observation, error) {
	if p.store == nil {
		return nil, nil
	}
	binding := p.policy.Binding()
	reset := binding.ResetAt
	query := coremetering.AccountWindowQuery{
		StoreID: binding.StoreID, TenantID: binding.TenantID, ProviderAccountKey: binding.ProviderAccountKey,
		PoolID: binding.PoolID, WindowID: binding.WindowID, ResetAt: &reset, AsOf: at, Limit: p.pageSize,
	}
	observations := make([]metering.Observation, 0, p.pageSize)
	seenCursors := make(map[string]struct{})
	for pageNo := 0; ; pageNo++ {
		if err := ctx.Err(); err != nil {
			return nil, quotaReaderError("read", err)
		}
		if pageNo >= maxQuotaReaderPages {
			return nil, quotaReaderProtocolError("page limit exceeded")
		}
		page, err := p.store.ListAccountWindowObservations(ctx, query)
		if err != nil {
			return nil, quotaReaderError("read", err)
		}
		if err := ctx.Err(); err != nil {
			return nil, quotaReaderError("read", err)
		}
		if len(page.Observations) > authority.MaxQuotaObservations-len(observations) {
			return nil, quotaReaderProtocolError("observation limit exceeded")
		}
		for _, observation := range page.Observations {
			// The reader owns its page and may reuse backing storage on the
			// next call. Keep the evaluator input immutable for the complete
			// request decision.
			observations = append(observations, observation.Clone())
		}
		next := page.NextCursor
		if next == "" {
			return observations, nil
		}
		if len(next) > maxQuotaCursorBytes {
			return nil, quotaReaderProtocolError("cursor exceeds bounded size")
		}
		if _, exists := seenCursors[next]; exists {
			return nil, quotaReaderProtocolError("cursor repeated")
		}
		seenCursors[next] = struct{}{}
		query.Cursor = next
	}
}

func quotaReaderError(operation string, cause error) error {
	return &QuotaReaderError{Operation: operation, Cause: cause}
}

func quotaReaderProtocolError(reason string) error {
	return &QuotaReaderError{Operation: "protocol", Cause: fmt.Errorf("%w: %s", ErrQuotaReaderProtocol, reason)}
}

func quotaReadiness(decision authority.QuotaDecision) authority.Readiness {
	if decision.EvidenceStatus == authority.QuotaEvidenceComplete {
		return authority.ReadinessReady
	}
	return authority.ReadinessUnavailable
}

func quotaPolicyRefCopy(ref authority.QuotaPolicyRef) *authority.QuotaPolicyRef {
	out := ref
	return &out
}
