package runtime

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/snapshotgen"
	"github.com/aiproxer/aiproxer/internal/core/terminalwork"
	terminalworkapp "github.com/aiproxer/aiproxer/internal/core/terminalwork/app"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

type requestAuthorityKey struct{}

// requestAuthorityState holds the once-per-request coordinator result for settle/release.
type requestAuthorityState struct {
	Decision authoritycoord.CompositeDecision
	// Coordinator is the immutable evaluator actually used for admission. It
	// may be candidate-local when a generation-scoped provider (such as the
	// nonfinancial quota gate) is configured; ExecutableGen still pins the
	// process snapshot used for existing providers and version evidence.
	Coordinator              *authoritycoord.RequestCoordinator
	RequestID                string
	AttemptID                string
	TraceID                  string
	Settled                  bool
	Released                 bool
	LeaseID                  string // primary lease (backward compat / aux parent)
	LeaseSetID               string
	LeaseIDs                 []string
	LeaseTargets             []leaseRenewTarget
	LeaseGeneration          int64
	LeaseExpiresAt           time.Time
	RenewBefore              time.Duration
	LeaseTTL                 time.Duration
	FailureBehavior          authority.FailureBehavior
	heartbeat                *leaseHeartbeat
	ExecutableGen            *snapshotgen.ExecutableGeneration
	cancelRequest            context.CancelFunc
	LeaseSetReleaseAcceptErr error
	LeaseSetUncertainErr     error
	// Workload is trusted content-free billing classification captured before
	// terminal paths may receive a bare caller context.
	Workload         usageevidence.WorkloadIdentity
	workloadMu       sync.RWMutex
	workloadByALegID map[string]usageevidence.WorkloadIdentity
}

// leaseRenewTarget is one occupancy the heartbeat renews until settle/release.
type leaseRenewTarget struct {
	LeaseID         string
	Generation      int64
	ExpiresAt       time.Time
	RenewBefore     time.Duration
	TTL             time.Duration
	RuleID          string
	FailureBehavior authority.FailureBehavior
}

func withRequestAuthority(ctx context.Context, st *requestAuthorityState) context.Context {
	if st == nil {
		return ctx
	}
	if ctx == nil {
		ctx = context.TODO()
	}
	return context.WithValue(ctx, requestAuthorityKey{}, st)
}

func requestAuthorityFrom(ctx context.Context) *requestAuthorityState {
	if ctx == nil {
		return nil
	}
	st, _ := ctx.Value(requestAuthorityKey{}).(*requestAuthorityState)
	return st
}

func (st *requestAuthorityState) setWorkloadForALeg(aLegID string, workload usageevidence.WorkloadIdentity) {
	if st == nil || workload.IsZero() {
		return
	}
	aLegID = strings.TrimSpace(aLegID)
	if aLegID == "" {
		return
	}
	st.workloadMu.Lock()
	if st.workloadByALegID == nil {
		st.workloadByALegID = make(map[string]usageevidence.WorkloadIdentity)
	}
	st.workloadByALegID[aLegID] = workload
	st.workloadMu.Unlock()
}

func (st *requestAuthorityState) workloadForALeg(aLegID string) (usageevidence.WorkloadIdentity, bool) {
	if st == nil {
		return usageevidence.WorkloadIdentity{}, false
	}
	aLegID = strings.TrimSpace(aLegID)
	if aLegID == "" {
		return usageevidence.WorkloadIdentity{}, false
	}
	st.workloadMu.RLock()
	workload, ok := st.workloadByALegID[aLegID]
	st.workloadMu.RUnlock()
	return workload, ok
}

// admitRequestAuthorityOnce runs the logical-request coordinator after FE ingress
// (requirements 4.5, 8.1, 9.3, 10.4). Nil coordinator still persists FE ingress when
// a MeteringRecorder is configured (task 3.3), then returns.
func (e *Executor) admitRequestAuthorityOnce(ctx context.Context, requestID, aLegID, traceID string, sc scope.PrincipalScopeView) (context.Context, error) {
	if e == nil {
		return ctx, nil
	}
	workload, err := billingWorkloadIdentityForALeg(ctx, aLegID)
	if err != nil {
		return ctx, fmt.Errorf("executor: billing workload identity: %w", err)
	}
	if err := e.enrichFrontendIngressQuantities(ctx); err != nil {
		return ctx, fmt.Errorf("executor: frontend ingress counting: %w", err)
	}
	holder := meteringHolderFrom(ctx)
	if _, err := e.persistFrontendIngressFact(ctx, holder); err != nil {
		return ctx, fmt.Errorf("executor: metering frontend ingress fact: %w", err)
	}
	if e.RequestCoordinator == nil {
		exec := (*snapshotgen.ExecutableGeneration)(nil)
		if e.SnapshotGeneration != nil {
			exec = e.SnapshotGeneration.CurrentExecutable()
		}
		if exec == nil || exec.RequestCoord == nil {
			if !workload.IsZero() {
				return withRequestAuthority(ctx, &requestAuthorityState{Workload: workload}), nil
			}
			return ctx, nil
		}
	}
	lifecycle := metering.LifecycleLogicalRequest
	parentLeaseID := ""
	auxPolicy := ""
	if parent := requestAuthorityFrom(ctx); parent != nil {
		policy := strings.ToLower(strings.TrimSpace(e.ConcurrencyAuxiliaryLeasePolicy))
		if policy == "" || policy == "inherit" || execctx.AuxiliaryDepth(ctx) == 0 {
			// Default: auxiliary Execute reuses parent occupancy (requirement 10.10).
			if workload.IsZero() {
				return ctx, nil
			}
			parent.setWorkloadForALeg(aLegID, workload)
			return ctx, nil
		}
		// acquire_own: continue into Admit with auxiliary lifecycle below.
		lifecycle = metering.LifecycleAuxiliaryRequest
		parentLeaseID = parent.LeaseID
		auxPolicy = "acquire_own"
	} else if execctx.AuxiliaryDepth(ctx) > 0 {
		lifecycle = metering.LifecycleAuxiliaryRequest
		policy := strings.ToLower(strings.TrimSpace(e.ConcurrencyAuxiliaryLeasePolicy))
		if policy == "acquire_own" {
			auxPolicy = "acquire_own"
		}
	}
	admitRequestID := strings.TrimSpace(requestID)
	idempotencyKey := "req:" + admitRequestID
	if auxPolicy == "acquire_own" {
		// Distinct logical identity so acquire_own does not replay the parent lease.
		suffix := strings.TrimSpace(aLegID)
		if suffix == "" {
			suffix = "aux"
		}
		admitRequestID = admitRequestID + ":aux:" + suffix
		idempotencyKey = "req-aux:" + admitRequestID
	}
	admitScope := trustedFrontendIngressScope(ctx, sc)
	in := authority.RequestAdmission{
		RequestID:      admitRequestID,
		ALegID:         strings.TrimSpace(aLegID),
		TraceID:        strings.TrimSpace(traceID),
		Perspective:    metering.PerspectiveCustomer,
		Lifecycle:      lifecycle,
		Scope:          admitScope,
		IdempotencyKey: idempotencyKey,
		ParentLeaseID:  parentLeaseID,
		AuxPolicy:      auxPolicy,
		Exposure: economics.ExposureBasis{
			Perspective: metering.PerspectiveCustomer,
			Boundary:    metering.BoundaryFrontendIngress,
			Lifecycle:   lifecycle,
		},
	}
	if holder != nil && holder.FrontendIngress != nil {
		in.Exposure.Quantities = append([]metering.Quantity(nil), holder.FrontendIngress.Public.Quantities...)
	}
	var boundGen *snapshotgen.ExecutableGeneration
	if e.SnapshotGeneration != nil {
		if exec := e.SnapshotGeneration.CurrentExecutable(); exec != nil {
			exec.Retain()
			boundGen = exec
		}
	}
	coord := e.RequestCoordinator
	if boundGen != nil && boundGen.RequestCoord != nil {
		coord = boundGen.RequestCoord
		if e.RequestCoordinator != nil && e.RequestCoordinator.HasGenerationScopedRequestProviders() {
			coord = e.RequestCoordinator
		}
	}
	if coord == nil {
		if boundGen != nil {
			boundGen.Release()
		}
		if !workload.IsZero() {
			return withRequestAuthority(ctx, &requestAuthorityState{Workload: workload}), nil
		}
		return ctx, nil
	}
	d, err := coord.Admit(ctx, in)
	if err != nil {
		if boundGen != nil {
			boundGen.Release()
		}
		return ctx, mapRequestAuthorityError(err)
	}
	e.mergeGenerationBoundVersionsFrom(boundGen, &d)
	targets := leaseTargetsFromDecision(d.Lease)
	leaseIDs := make([]string, 0, len(targets))
	for _, t := range targets {
		leaseIDs = append(leaseIDs, t.LeaseID)
	}
	primaryID := d.Lease.LeaseID
	primaryGen := d.Lease.Generation
	primaryExp := d.Lease.ExpiresAt
	if primaryID == "" && len(targets) > 0 {
		primaryID = targets[0].LeaseID
		primaryGen = targets[0].Generation
		primaryExp = targets[0].ExpiresAt
	}
	hbCtx, hbCancel := context.WithCancel(ctx)
	st := &requestAuthorityState{
		Decision:        d,
		Coordinator:     coord,
		RequestID:       in.RequestID,
		AttemptID:       strings.TrimSpace(aLegID),
		TraceID:         strings.TrimSpace(traceID),
		LeaseID:         primaryID,
		LeaseSetID:      strings.TrimSpace(d.Lease.SetID),
		LeaseIDs:        leaseIDs,
		LeaseTargets:    targets,
		LeaseGeneration: primaryGen,
		LeaseExpiresAt:  primaryExp,
		RenewBefore:     d.Lease.RenewBefore,
		LeaseTTL:        d.Lease.TTL,
		FailureBehavior: d.Lease.FailureBehavior,
		ExecutableGen:   boundGen,
		cancelRequest:   hbCancel,
		Workload:        workload,
	}
	outCtx := withRequestAuthority(hbCtx, st)
	e.startLeaseHeartbeat(outCtx, st)
	return outCtx, nil
}

func leaseTargetsFromDecision(ld authority.LeaseDecision) []leaseRenewTarget {
	if len(ld.Leases) > 0 {
		out := make([]leaseRenewTarget, 0, len(ld.Leases))
		seen := make(map[string]struct{}, len(ld.Leases))
		for _, occ := range ld.Leases {
			id := strings.TrimSpace(occ.LeaseID)
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			rb := occ.RenewBefore
			if rb <= 0 {
				rb = ld.RenewBefore
			}
			ttl := occ.TTL
			if ttl <= 0 {
				ttl = ld.TTL
			}
			fb := occ.FailureBehavior
			if fb == "" {
				fb = ld.FailureBehavior
			}
			exp := occ.ExpiresAt
			if exp.IsZero() {
				exp = ld.ExpiresAt
			}
			out = append(out, leaseRenewTarget{
				LeaseID:         id,
				Generation:      occ.Generation,
				ExpiresAt:       exp,
				RenewBefore:     rb,
				TTL:             ttl,
				RuleID:          strings.TrimSpace(occ.RuleID),
				FailureBehavior: fb,
			})
		}
		if len(out) > 0 {
			return out
		}
	}
	if id := strings.TrimSpace(ld.LeaseID); id != "" {
		return []leaseRenewTarget{{
			LeaseID:         id,
			Generation:      ld.Generation,
			ExpiresAt:       ld.ExpiresAt,
			RenewBefore:     ld.RenewBefore,
			TTL:             ld.TTL,
			FailureBehavior: ld.FailureBehavior,
		}}
	}
	return nil
}

func (e *Executor) settleRequestAuthority(ctx context.Context, facts []metering.Fact) error {
	if e == nil {
		return nil
	}
	st := requestAuthorityFrom(ctx)
	if st == nil || st.Settled || st.Released {
		return nil
	}
	coord := e.requestCoordinatorFor(st)
	if coord == nil {
		return nil
	}
	err := coord.Settle(ctx, st.Decision.Stack, authority.RequestSettlement{
		RequestID:     st.RequestID,
		Handles:       st.Decision.Stack.Handles(),
		Facts:         facts,
		BoundVersions: st.Decision.BoundVersions,
	})
	if err != nil {
		// Post-output settlement failure must retain reservation evidence and stay
		// retryable; do not mark settled/released or release the lease (15.5).
		// Accept durable intents for each unfinished provider (Phase 4.5 / D9).
		return e.acceptSettleDurableIntents(ctx, st)
	}
	// RequestCoordinator.Settle does not release concurrency occupancy (10.5).
	e.stopLeaseHeartbeat(st)
	if setID := strings.TrimSpace(st.LeaseSetID); setID != "" {
		if err := coord.ReleaseLeaseSet(ctx, setID, st.LeaseID, st.RequestID, "settled"); err != nil {
			acceptErr := e.acceptLeaseSetReleaseIntent(ctx, st, "settle_release_failure")
			if acceptErr != nil {
				return errors.Join(err, acceptErr)
			}
			return errors.Join(err, terminalworkapp.ErrDurablePending)
		}
	} else {
		ids := st.LeaseIDs
		if len(ids) == 0 && st.LeaseID != "" {
			ids = []string{st.LeaseID}
		}
		if err := coord.ReleaseLeases(ctx, ids, st.RequestID, "settled"); err != nil {
			return err
		}
	}
	st.Settled = true
	st.Released = true
	if st.ExecutableGen != nil {
		st.ExecutableGen.Release()
		st.ExecutableGen = nil
	}
	return nil
}

func (e *Executor) releaseRequestAuthority(ctx context.Context) error {
	if e == nil {
		return nil
	}
	st := requestAuthorityFrom(ctx)
	if st == nil || st.Settled || st.Released {
		return nil
	}
	coord := e.requestCoordinatorFor(st)
	if coord == nil {
		return nil
	}
	e.stopLeaseHeartbeat(st)
	if setID := strings.TrimSpace(st.LeaseSetID); setID != "" {
		if err := coord.ReleaseLeaseSet(ctx, setID, st.LeaseID, st.RequestID, "released"); err != nil {
			acceptErr := e.acceptLeaseSetReleaseIntent(ctx, st, "release_failure")
			if acceptErr != nil {
				return errors.Join(err, acceptErr)
			}
			return errors.Join(err, terminalworkapp.ErrDurablePending)
		}
	}
	fails := coord.Release(ctx, st.Decision.Stack)
	if len(fails) > 0 {
		// Live release incomplete: keep Released=false and accept durable intents.
		return e.acceptReleaseDurableIntents(ctx, st, fails)
	}
	st.Released = true
	if st.ExecutableGen != nil {
		st.ExecutableGen.Release()
		st.ExecutableGen = nil
	}
	return nil
}

func (e *Executor) requestCoordinatorFor(st *requestAuthorityState) *authoritycoord.RequestCoordinator {
	if st != nil && st.Coordinator != nil {
		return st.Coordinator
	}
	if st != nil && st.ExecutableGen != nil && st.ExecutableGen.RequestCoord != nil {
		return st.ExecutableGen.RequestCoord
	}
	if e != nil {
		return e.RequestCoordinator
	}
	return nil
}

func (e *Executor) acceptSettleDurableIntents(ctx context.Context, st *requestAuthorityState) error {
	if st == nil {
		return fmt.Errorf("%w: missing request authority state", terminalworkapp.ErrDurableIntentRejected)
	}
	if e == nil || e.TerminalWork == nil {
		return fmt.Errorf("%w: terminal work not configured", terminalworkapp.ErrDurableIntentRejected)
	}
	handlesByProvider := handlesByProviderFromStack(st.Decision.Stack)
	providers := st.Decision.Stack.UnfinishedSettleProviders()
	if len(providers) == 0 {
		// Fall back to every provider with handles when tracker has no unfinished set.
		for id := range handlesByProvider {
			providers = append(providers, id)
		}
	}
	if len(providers) == 0 {
		return fmt.Errorf("%w: no unfinished settle providers", terminalworkapp.ErrDurableIntentRejected)
	}
	var acceptErrs []error
	accepted := 0
	for _, providerID := range providers {
		providerID = strings.TrimSpace(providerID)
		if providerID == "" {
			continue
		}
		if err := e.TerminalWork.AcceptSettleFailure(ctx, terminalworkapp.SettleFailureInput{
			RequestID:  st.RequestID,
			AttemptID:  st.AttemptID,
			TraceID:    st.TraceID,
			ProviderID: providerID,
			Handles:    handlesByProvider[providerID],
			Versions:   boundVersionsForProvider(st, providerID),
		}); err != nil {
			acceptErrs = append(acceptErrs, err)
			continue
		}
		accepted++
	}
	if accepted == 0 {
		return terminalworkapp.ErrDurableIntentRejected
	}
	if len(acceptErrs) > 0 {
		return errors.Join(
			terminalworkapp.ErrDurablePending,
			fmt.Errorf("%w: partial durable intent acceptance", terminalworkapp.ErrDurableIntentRejected),
		)
	}
	return terminalworkapp.ErrDurablePending
}

func (e *Executor) acceptLeaseSetReleaseIntent(ctx context.Context, st *requestAuthorityState, reason string) error {
	if st == nil {
		return fmt.Errorf("%w: missing request authority state", terminalworkapp.ErrDurableIntentRejected)
	}
	setID := strings.TrimSpace(st.LeaseSetID)
	if setID == "" {
		return fmt.Errorf("%w: missing lease set id", terminalworkapp.ErrDurableIntentRejected)
	}
	if e == nil || e.TerminalWork == nil {
		err := fmt.Errorf("%w: terminal work not configured", terminalworkapp.ErrDurableIntentRejected)
		st.LeaseSetReleaseAcceptErr = err
		return err
	}
	timeout := 2 * time.Second
	if coord := e.requestCoordinatorFor(st); coord != nil && coord.CleanupTimeout > 0 {
		timeout = coord.CleanupTimeout
	}
	base := ctx
	if base == nil {
		base = context.Background()
	}
	actx, cancel := context.WithTimeout(context.WithoutCancel(base), timeout)
	defer cancel()
	err := e.TerminalWork.AcceptLeaseSetRelease(actx, terminalworkapp.LeaseSetReleaseInput{
		RequestID:  st.RequestID,
		AttemptID:  st.AttemptID,
		TraceID:    st.TraceID,
		LeaseSetID: setID,
		Reason:     reason,
		Versions:   boundVersionsForProvider(st, "concurrency"),
	})
	if err != nil {
		st.LeaseSetReleaseAcceptErr = err
	}
	return err
}

type leaseSetUncertainMarker interface {
	MarkLeaseSetUncertain(ctx context.Context, setID string) error
}

func (e *Executor) markLeaseSetUncertain(ctx context.Context, st *requestAuthorityState) error {
	if st == nil {
		return nil
	}
	setID := strings.TrimSpace(st.LeaseSetID)
	if setID == "" {
		return nil
	}
	coord := e.requestCoordinatorFor(st)
	if coord == nil || coord.Concurrency == nil {
		return nil
	}
	if m, ok := coord.Concurrency.(leaseSetUncertainMarker); ok {
		base := ctx
		if base == nil {
			base = context.Background()
		}
		mctx, cancel := context.WithTimeout(context.WithoutCancel(base), 2*time.Second)
		defer cancel()
		return m.MarkLeaseSetUncertain(mctx, setID)
	}
	return nil
}

func (e *Executor) acceptReleaseDurableIntents(ctx context.Context, st *requestAuthorityState, fails []authoritycoord.CompensateFailed) error {
	if st == nil {
		return fmt.Errorf("%w: missing request authority state", terminalworkapp.ErrDurableIntentRejected)
	}
	if e == nil || e.TerminalWork == nil {
		return fmt.Errorf("%w: terminal work not configured", terminalworkapp.ErrDurableIntentRejected)
	}
	var acceptErrs []error
	accepted := 0
	for _, fail := range fails {
		providerID := strings.TrimSpace(fail.ProviderID)
		if providerID == "" || providerID == "concurrency" {
			continue
		}
		if err := e.TerminalWork.AcceptReleaseFailure(ctx, terminalworkapp.ReleaseFailureInput{
			RequestID:  st.RequestID,
			AttemptID:  st.AttemptID,
			TraceID:    st.TraceID,
			ProviderID: providerID,
			Handle:     fail.Handle,
			Versions:   boundVersionsForProvider(st, providerID),
		}); err != nil {
			acceptErrs = append(acceptErrs, err)
			continue
		}
		accepted++
	}
	if accepted == 0 {
		return terminalworkapp.ErrDurableIntentRejected
	}
	if len(acceptErrs) > 0 {
		return errors.Join(
			terminalworkapp.ErrDurablePending,
			fmt.Errorf("%w: partial durable intent acceptance", terminalworkapp.ErrDurableIntentRejected),
		)
	}
	return terminalworkapp.ErrDurablePending
}

func handlesByProviderFromStack(stack authoritycoord.CompensationStack) map[string][]string {
	out := make(map[string][]string)
	for _, e := range stack.Entries() {
		id := strings.TrimSpace(e.ProviderID)
		h := strings.TrimSpace(e.Handle)
		if id == "" || h == "" || id == "concurrency" {
			continue
		}
		out[id] = append(out[id], h)
	}
	return out
}

func boundVersionsForProvider(st *requestAuthorityState, providerID string) terminalwork.BoundVersions {
	ver := terminalwork.BoundVersions{ProviderID: providerID}
	if st == nil {
		return ver
	}
	if st.ExecutableGen != nil {
		ver.GenerationID = strconv.FormatInt(st.ExecutableGen.ID, 10)
		if v := strings.TrimSpace(st.ExecutableGen.Version); v != "" {
			ver.RatingID = v
		}
		return ver
	}
	for _, ref := range st.Decision.BoundVersions {
		id := strings.TrimSpace(ref.ID)
		if id == "" {
			id = strings.TrimSpace(ref.PolicyID)
		}
		if id == "" {
			continue
		}
		ver.GenerationID = id
		if v := strings.TrimSpace(ref.Version); v != "" {
			ver.RatingID = v
		}
		break
	}
	return ver
}

// mapRequestAuthorityError converts coordinator denials into client-safe policy
// errors. Concurrency denials use the stable concurrency_limit category and must
// not include internal lease IDs (requirements 10.11, 14.3).
func mapRequestAuthorityError(err error) error {
	var denied *authoritycoord.DeniedError
	if errors.As(err, &denied) && denied != nil && denied.ProviderID == "concurrency" {
		return aipapi.NewPolicyDeniedError(
			"request_authority",
			"",
			"concurrency_limit",
			"concurrency_limit",
			"active request limit reached",
			nil,
		)
	}
	var unavail *authoritycoord.UnavailableError
	if errors.As(err, &unavail) && unavail != nil && unavail.ProviderID == "concurrency" {
		return aipapi.NewPolicyFailureError(
			"request_authority",
			"",
			"concurrency_unavailable",
			"concurrency_limit",
			"concurrency authority unavailable",
			nil,
		)
	}
	return fmt.Errorf("executor: request authority: %w", err)
}
