package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/safety"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/submission"
)

func stampBillingCallID(prep *preparedRequest) error {
	if prep == nil {
		return fmt.Errorf("%w: prepared request is required", usageevidence.ErrBillingCallIDInvalid)
	}
	if prep.billingCallID != "" {
		if prep.submission.Trusted() &&
			(prep.submission.Authority.Kind == submission.KindToolContinuation || prep.submission.Authority.Kind == submission.KindHistoricalReplay || prep.submission.Authority.Kind == submission.KindTransportRetry) &&
			strings.TrimSpace(prep.submission.Authority.BillingCallID) != "" &&
			strings.TrimSpace(prep.submission.Authority.BillingCallID) != prep.billingCallID.String() {
			return fmt.Errorf("%w: continuation cannot change billing call identity", submission.ErrScopeMismatch)
		}
		if prep.submission.Trusted() &&
			(prep.submission.Authority.Kind == submission.KindNewSubmission || prep.submission.Authority.Kind == submission.KindFollowUp) &&
			strings.TrimSpace(prep.submission.Authority.BillingCallID) != prep.billingCallID.String() {
			return fmt.Errorf("%w: new submission cannot reuse billing call identity", submission.ErrLifecycleConflict)
		}
		if err := prep.billingCallID.Validate(); err != nil {
			return err
		}
		if prep.billingCallState == nil {
			prep.billingCallState = newBillingCallState(prep.billingCallID)
		}
		if prep.submission.Billable() {
			if err := prep.billingCallState.ensureSubmissionID(prep.submission.Authority.SubmissionID); err != nil {
				return err
			}
		}
		return nil
	}
	if prep.submission.Trusted() && strings.TrimSpace(prep.submission.Authority.BillingCallID) != "" {
		id := usageevidence.BillingCallID(strings.TrimSpace(prep.submission.Authority.BillingCallID))
		if err := id.Validate(); err != nil {
			return fmt.Errorf("%w: trusted continuation billing call id: %v", usageevidence.ErrBillingCallIDInvalid, err)
		}
		prep.billingCallID = id
		prep.billingCallState = newBillingCallState(id)
		if prep.submission.Billable() {
			if err := prep.billingCallState.ensureSubmissionID(prep.submission.Authority.SubmissionID); err != nil {
				return err
			}
		}
		return nil
	}
	id, err := usageevidence.NewBillingCallID()
	if err == nil {
		prep.billingCallID = id
		prep.billingCallState = newBillingCallState(id)
		if prep.submission.Billable() {
			if err := prep.billingCallState.ensureSubmissionID(prep.submission.Authority.SubmissionID); err != nil {
				return err
			}
		}
	}
	return err
}

// resolveBillingStoreID reads the trusted durable-store lineage through the
// existing backend safety boundary. A panicking resolver fails closed with
// an error; an empty result stays an explicit absence, never a fabricated
// store identity.
func (e *Executor) resolveBillingStoreID(ctx context.Context) (string, error) {
	if e == nil || e.BillingIdentity.StoreID == nil {
		return "", nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	storeID, err := safety.CallValue(safety.BoundaryBackend, "billing_store_identity", func() (string, error) {
		return strings.TrimSpace(e.BillingIdentity.StoreID(ctx)), nil
	})
	if err != nil {
		return "", err
	}
	return storeID, nil
}

// stampBillingStoreID freezes the trusted durable-store lineage alongside the
// request BillingCallID. An empty resolver result is intentionally retained as
// an absent fact; terminal V1 compatibility evidence may still be recorded, but
// V2 observations must not be assigned a fabricated store identity. The resolver
// runs through the existing backend safety boundary so a panicking callback
// fails closed with an error instead of leaking the request-authority guard.
func (e *Executor) stampBillingStoreID(ctx context.Context, request *preparedRequest) error {
	if e == nil || request == nil || request.billingStoreIDStamped {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	storeID, err := e.resolveBillingStoreID(ctx)
	if err != nil {
		return err
	}
	request.billingStoreID = storeID
	request.billingStoreIDStamped = true
	return nil
}
