package authoritycoord

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
)

// Provider invoke helpers isolate panics and validate decision shape (req 15.9).

func invokeAdmitRequest(ctx context.Context, p authority.RequestProvider, in authority.RequestAdmission) (d authority.Decision, err error) {
	if p == nil {
		return authority.Decision{Kind: authority.DecisionAllow}, nil
	}
	defer isolateProviderPanic(&d, &err)
	return p.AdmitRequest(ctx, in)
}

func invokeAdmitAttempt(ctx context.Context, p authority.AttemptProvider, in authority.AttemptAdmission) (d authority.Decision, err error) {
	if p == nil {
		return authority.Decision{Kind: authority.DecisionAllow}, nil
	}
	defer isolateProviderPanic(&d, &err)
	return p.AdmitAttempt(ctx, in)
}

func invokePreviewAttempt(ctx context.Context, p authority.AttemptClampPreviewer, in authority.AttemptAdmission) (d authority.Decision, err error) {
	if p == nil {
		return authority.Decision{Kind: authority.DecisionAllow}, nil
	}
	defer isolateProviderPanic(&d, &err)
	d, err = p.PreviewAttempt(ctx, in)
	if err != nil {
		return d, err
	}
	if vErr := d.Validate(); vErr != nil {
		return d, vErr
	}
	if len(d.Reservations) > 0 || strings.TrimSpace(d.CompensationHandle) != "" {
		return d, fmt.Errorf("preview returned holds")
	}
	for _, clamp := range d.Clamps {
		if cErr := validatePreviewClamp(clamp); cErr != nil {
			return d, cErr
		}
	}
	return d, nil
}

func invokeSettleRequest(ctx context.Context, p authority.RequestProvider, in authority.RequestSettlement) (s authority.Settlement, err error) {
	if p == nil {
		return authority.OwnedFinalSettlement(in.Handles), nil
	}
	defer func() {
		if recover() != nil {
			s = authority.Settlement{}
			err = errProviderPanic
		}
	}()
	return p.SettleRequest(ctx, in)
}

func invokeSettleAttempt(ctx context.Context, p authority.AttemptProvider, in authority.AttemptSettlement) (s authority.Settlement, err error) {
	if p == nil {
		return authority.OwnedFinalSettlement(in.Handles), nil
	}
	defer func() {
		if recover() != nil {
			s = authority.Settlement{}
			err = errProviderPanic
		}
	}()
	return p.SettleAttempt(ctx, in)
}

func invokeAdmitLease(ctx context.Context, p authority.ConcurrencyProvider, in authority.LeaseAdmission, now time.Time, reg authority.ProviderDescriptor) (d authority.LeaseDecision, err error) {
	if p == nil {
		return authority.LeaseDecision{Kind: authority.LeaseAllow}, nil
	}
	defer func() {
		if recover() != nil {
			d, err = authority.LeaseDecision{}, errConcurrencyPanic
		}
	}()
	d, err = p.AdmitLease(ctx, in)
	if err != nil {
		return d, err
	}
	if vErr := validateCoordinatorLease(d, in, reg, now); vErr != nil {
		// Keep d so callers can reverse-compensate any claimed leases (req 15.9).
		return d, vErr
	}
	return d, nil
}

func invokeRenewLease(ctx context.Context, p authority.ConcurrencyProvider, in authority.LeaseRenew, now time.Time, reg authority.ProviderDescriptor) (d authority.LeaseDecision, err error) {
	if p == nil {
		return authority.LeaseDecision{}, fmt.Errorf("authoritycoord: nil concurrency provider")
	}
	defer func() {
		if recover() != nil {
			d, err = authority.LeaseDecision{}, errConcurrencyPanic
		}
	}()
	d, err = p.RenewLease(ctx, in)
	if err != nil {
		return d, err
	}
	if vErr := validateCoordinatorLeaseRenewal(d, in, reg, now); vErr != nil {
		return d, vErr
	}
	return d, nil
}

func invokeCompensate(ctx context.Context, fn Compensator) (err error) {
	if fn == nil {
		return nil
	}
	defer func() {
		if recover() != nil {
			err = errCompensatePanic
		}
	}()
	return fn(ctx)
}

func isolateProviderPanic(d *authority.Decision, err *error) {
	if recover() != nil {
		*d = authority.Decision{}
		*err = errProviderPanic
	}
}

// Safe client-facing panic markers (D14 / req 13.3): no raw panic payload.
var (
	errProviderPanic    = fmt.Errorf("authoritycoord: provider panic")
	errConcurrencyPanic = fmt.Errorf("authoritycoord: concurrency provider panic")
	errCompensatePanic  = fmt.Errorf("authoritycoord: compensate panic")
)
