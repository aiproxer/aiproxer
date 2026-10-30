package controlplane

import (
	"context"
	"time"
)

// AccountingAuthorityState reports the operator-visible readiness posture for
// accounting authority queries and live limit state.
type AccountingAuthorityState string

const (
	AccountingAuthorityDisabled     AccountingAuthorityState = "disabled"
	AccountingAuthorityReady        AccountingAuthorityState = "ready"
	AccountingAuthorityDegraded     AccountingAuthorityState = "degraded"
	AccountingAuthorityUnavailable  AccountingAuthorityState = "unavailable"
	AccountingAuthorityAdvisoryOnly AccountingAuthorityState = "advisory_only"
)

// IsKnown reports whether s is one of the documented accounting authority states.
func (s AccountingAuthorityState) IsKnown() bool {
	switch s {
	case AccountingAuthorityDisabled, AccountingAuthorityReady, AccountingAuthorityDegraded, AccountingAuthorityUnavailable, AccountingAuthorityAdvisoryOnly:
		return true
	}
	return false
}

// AccountingOutcome is the safe accounting decision vocabulary used by
// accounting authority detail and query rows.
type AccountingOutcome string

const (
	AccountingOutcomeAllow       AccountingOutcome = "allow"
	AccountingOutcomeDeny        AccountingOutcome = "deny"
	AccountingOutcomeAdvisory    AccountingOutcome = "advisory"
	AccountingOutcomeClamp       AccountingOutcome = "clamp"
	AccountingOutcomeReserve     AccountingOutcome = "reserve"
	AccountingOutcomeReconcile   AccountingOutcome = "reconcile"
	AccountingOutcomeUnavailable AccountingOutcome = "unavailable"
	AccountingOutcomeError       AccountingOutcome = "error"
)

// IsKnown reports whether o is one of the documented accounting outcomes.
func (o AccountingOutcome) IsKnown() bool {
	switch o {
	case AccountingOutcomeAllow, AccountingOutcomeDeny, AccountingOutcomeAdvisory, AccountingOutcomeClamp,
		AccountingOutcomeReserve, AccountingOutcomeReconcile, AccountingOutcomeUnavailable, AccountingOutcomeError:
		return true
	}
	return false
}

// AccountingAuthoritySource identifies the authority posture that produced the
// detail or row.
type AccountingAuthoritySource string

const (
	AccountingAuthoritySourceEstimated     AccountingAuthoritySource = "estimated"
	AccountingAuthoritySourceAuthoritative AccountingAuthoritySource = "authoritative"
	AccountingAuthoritySourceReserved      AccountingAuthoritySource = "reserved"
	AccountingAuthoritySourceReconciled    AccountingAuthoritySource = "reconciled"
	AccountingAuthoritySourceAdvisory      AccountingAuthoritySource = "advisory"
	AccountingAuthoritySourceUnavailable   AccountingAuthoritySource = "unavailable"
)

// IsKnown reports whether s is one of the documented accounting authority sources.
func (s AccountingAuthoritySource) IsKnown() bool {
	switch s {
	case AccountingAuthoritySourceEstimated, AccountingAuthoritySourceAuthoritative, AccountingAuthoritySourceReserved,
		AccountingAuthoritySourceReconciled, AccountingAuthoritySourceAdvisory, AccountingAuthoritySourceUnavailable:
		return true
	}
	return false
}

// AccountingSettlementState reports the settlement posture attached to a
// reservation or reconciliation record.
type AccountingSettlementState string

const (
	AccountingSettlementPending     AccountingSettlementState = "pending"
	AccountingSettlementSettled     AccountingSettlementState = "settled"
	AccountingSettlementReleased    AccountingSettlementState = "released"
	AccountingSettlementAdjusted    AccountingSettlementState = "adjusted"
	AccountingSettlementOverage     AccountingSettlementState = "overage"
	AccountingSettlementUnavailable AccountingSettlementState = "unavailable"
)

// IsKnown reports whether s is one of the documented settlement states.
func (s AccountingSettlementState) IsKnown() bool {
	switch s {
	case AccountingSettlementPending, AccountingSettlementSettled, AccountingSettlementReleased,
		AccountingSettlementAdjusted, AccountingSettlementOverage, AccountingSettlementUnavailable:
		return true
	}
	return false
}

// AccountingAuthorityStatus is the operator-visible live posture snapshot for
// accounting authority. It remains distinct from historical usage aggregates.
type AccountingAuthorityStatus struct {
	State          AccountingAuthorityState `json:"state"`
	Reason         ReasonCode               `json:"reason,omitempty"`
	LastUpdatedAt  time.Time                `json:"last_updated_at,omitzero"`
	EvidenceState  EvidenceState            `json:"evidence_state"`
	RedactionState RedactionState           `json:"redaction_state"`
}

// AccountingLimitStatusQuery requests bounded live accounting limit rows.
type AccountingLimitStatusQuery struct {
	Common          CommonFilters             `json:"common,omitzero"`
	RuleID          string                    `json:"rule_id,omitempty"`
	Unit            string                    `json:"unit,omitempty"`
	Currency        string                    `json:"currency,omitempty"`
	Authority       AccountingAuthoritySource `json:"authority,omitempty"`
	SettlementState AccountingSettlementState `json:"settlement_state,omitempty"`
	EvidenceState   EvidenceState             `json:"evidence_state,omitempty"`
	RedactionState  RedactionState            `json:"redaction_state,omitempty"`
	Perspective     UsagePerspective          `json:"perspective,omitempty"`
	LifecycleScope  UsageLifecycleScope       `json:"lifecycle_scope,omitempty"`
	Basis           string                    `json:"basis,omitempty"`
	Class           QueryClass                `json:"class,omitempty"`
	Limit           int                       `json:"limit,omitempty"`
	Cursor          Cursor                    `json:"cursor,omitzero"`
	Visibility      Visibility                `json:"visibility,omitempty"`
}

// AccountingDecisionQuery requests bounded accounting decision rows.
type AccountingDecisionQuery struct {
	Common          CommonFilters             `json:"common,omitzero"`
	RuleID          string                    `json:"rule_id,omitempty"`
	Unit            string                    `json:"unit,omitempty"`
	Currency        string                    `json:"currency,omitempty"`
	Authority       AccountingAuthoritySource `json:"authority,omitempty"`
	SettlementState AccountingSettlementState `json:"settlement_state,omitempty"`
	EvidenceState   EvidenceState             `json:"evidence_state,omitempty"`
	RedactionState  RedactionState            `json:"redaction_state,omitempty"`
	Perspective     UsagePerspective          `json:"perspective,omitempty"`
	LifecycleScope  UsageLifecycleScope       `json:"lifecycle_scope,omitempty"`
	Basis           string                    `json:"basis,omitempty"`
	Class           QueryClass                `json:"class,omitempty"`
	Limit           int                       `json:"limit,omitempty"`
	Cursor          Cursor                    `json:"cursor,omitzero"`
	Visibility      Visibility                `json:"visibility,omitempty"`
}

// AccountingAuthorityDetail is the safe, queryable accounting authority
// evidence block for decisions, reservations, settlements, adjustments, and
// unavailable states.
//
// "No window" semantic for WindowStart/End/ResetAt: a projector that emits
// this detail may not have access to the rule's window metadata (the rule
// snapshot is the authoritative source for window bounds). In that case the
// projector MUST leave all three fields as the time.Time zero value. SDK
// consumers MUST treat WindowStart.IsZero(), WindowEnd.IsZero(), and
// WindowResetAt.IsZero() as "not applicable" rather than as a zero-length
// or "reset now" window. To look up actual window bounds, query the
// accounting limit status (AccountingLimitStatusRow.WindowStart/End/ResetAt)
// which is always populated by the authority store. Event.Validate does not
// reject zero window fields precisely so this no-window projection is a
// valid contract.
type AccountingAuthorityDetail struct {
	Correlation     Correlation               `json:"correlation"`
	Scope           ScopeSnapshot             `json:"scope"`
	RuleID          string                    `json:"rule_id,omitempty"`
	RuleType        string                    `json:"rule_type,omitempty"`
	Outcome         AccountingOutcome         `json:"outcome"`
	ReasonCode      string                    `json:"reason_code,omitempty"`
	Authority       AccountingAuthoritySource `json:"authority,omitempty"`
	ReservationID   string                    `json:"reservation_id,omitempty"`
	SettlementState AccountingSettlementState `json:"settlement_state,omitempty"`
	Unit            string                    `json:"unit,omitempty"`
	Currency        string                    `json:"currency,omitempty"`
	Limit           int64                     `json:"limit,omitempty"`
	Consumed        int64                     `json:"consumed,omitempty"`
	Reserved        int64                     `json:"reserved,omitempty"`
	Remaining       int64                     `json:"remaining,omitempty"`
	Adjustment      int64                     `json:"adjustment,omitempty"`
	WindowStart     time.Time                 `json:"window_start,omitzero"`
	WindowEnd       time.Time                 `json:"window_end,omitzero"`
	WindowResetAt   time.Time                 `json:"window_reset_at,omitzero"`
	EvidenceState   EvidenceState             `json:"evidence_state"`
	RedactionState  RedactionState            `json:"redaction_state"`
	// Dual-plane identity (requirements 1.6, 14.3, 17.4).
	AuthorityNamespace string              `json:"authority_namespace,omitempty"`
	Perspective        UsagePerspective    `json:"perspective,omitempty"`
	LifecycleScope     UsageLifecycleScope `json:"lifecycle_scope,omitempty"`
	Basis              string              `json:"basis,omitempty"`
	RuleVersion        string              `json:"rule_version,omitempty"`
	Surfaced           UsageSurfaced       `json:"surfaced,omitempty"`
	ReservationType    AuthorityHandleType `json:"reservation_type,omitempty"`
	ParentRequestID    string              `json:"parent_request_id,omitempty"`
	BoundPolicyVersion VersionRef          `json:"bound_policy_version,omitzero"`
	BoundRatingVersion VersionRef          `json:"bound_rating_version,omitzero"`
}

// AccountingLimitStatusRow is the live accounting limit view distinct from
// historical usage aggregates.
//
// WindowStart/End/ResetAt are populated by the authority store from the
// rule's window bounds at append time. They are set only when the rule has
// a window definition (Window.Algorithm, Size, or Anchor); rules without
// a window definition leave these fields as the zero time.Time. SDK
// consumers SHOULD treat zero values on these fields as "no window" —
// same contract as AccountingAuthorityDetail. To check whether the
// authority is enabled and queryable, use AccountingAuthorityStatus
// before reading limit rows.
type AccountingLimitStatusRow struct {
	Correlation    Correlation               `json:"correlation"`
	Scope          ScopeSnapshot             `json:"scope"`
	RuleID         string                    `json:"rule_id,omitempty"`
	RuleType       string                    `json:"rule_type,omitempty"`
	Unit           string                    `json:"unit,omitempty"`
	Currency       string                    `json:"currency,omitempty"`
	Limit          int64                     `json:"limit,omitempty"`
	Consumed       int64                     `json:"consumed,omitempty"`
	Reserved       int64                     `json:"reserved,omitempty"`
	Remaining      int64                     `json:"remaining,omitempty"`
	Adjustment     int64                     `json:"adjustment,omitempty"`
	WindowStart    time.Time                 `json:"window_start,omitzero"`
	WindowEnd      time.Time                 `json:"window_end,omitzero"`
	WindowResetAt  time.Time                 `json:"window_reset_at,omitzero"`
	Authority      AccountingAuthoritySource `json:"authority,omitempty"`
	EvidenceState  EvidenceState             `json:"evidence_state"`
	RedactionState RedactionState            `json:"redaction_state"`
	// Dual-plane identity (Phase 7). Empty AuthorityNamespace means legacy.
	AuthorityNamespace string `json:"authority_namespace,omitempty"`
	Perspective        string `json:"perspective,omitempty"`
	LifecycleScope     string `json:"lifecycle_scope,omitempty"`
	Basis              string `json:"basis,omitempty"`
	RuleVersion        string `json:"rule_version,omitempty"`
}

// AccountingDecisionRow is the decision-history view for accounting authority
// evidence.
//
// WindowStart/End/ResetAt are populated by the authority store's
// appendDecision path from the live limit row. They are set only when the
// underlying rule has a window definition; rules without a window
// definition leave these fields as the zero time.Time. SDK consumers
// SHOULD treat zero values on these fields as "no window" — same
// contract as AccountingLimitStatusRow.
//
// Released/Overage/Adjustment carry the settlement/release delta for the
// mutation recorded by this row. Settle records released/overage/adjustment
// (adjustment = released - overage); release records released with adjustment
// mirroring released and no overage; reserve decisions (allow or deny) leave
// all three as zero. They use the ,omitzero JSON tag so reserve rows omit them
// entirely and consumers can distinguish "no settlement delta" from a real
// zero-valued adjustment.
type AccountingDecisionRow struct {
	Correlation     Correlation               `json:"correlation"`
	Scope           ScopeSnapshot             `json:"scope"`
	RuleID          string                    `json:"rule_id,omitempty"`
	Outcome         AccountingOutcome         `json:"outcome"`
	ReasonCode      string                    `json:"reason_code,omitempty"`
	Authority       AccountingAuthoritySource `json:"authority,omitempty"`
	ReservationID   string                    `json:"reservation_id,omitempty"`
	SettlementState AccountingSettlementState `json:"settlement_state,omitempty"`
	Unit            string                    `json:"unit,omitempty"`
	Currency        string                    `json:"currency,omitempty"`
	Limit           int64                     `json:"limit,omitempty"`
	Consumed        int64                     `json:"consumed,omitempty"`
	Reserved        int64                     `json:"reserved,omitempty"`
	Remaining       int64                     `json:"remaining,omitempty"`
	Released        int64                     `json:"released,omitzero"`
	Overage         int64                     `json:"overage,omitzero"`
	Adjustment      int64                     `json:"adjustment,omitzero"`
	WindowStart     time.Time                 `json:"window_start,omitzero"`
	WindowEnd       time.Time                 `json:"window_end,omitzero"`
	WindowResetAt   time.Time                 `json:"window_reset_at,omitzero"`
	EvidenceState   EvidenceState             `json:"evidence_state"`
	RedactionState  RedactionState            `json:"redaction_state"`
	// Dual-plane identity (requirements 1.6, 14.3, 17.4).
	AuthorityNamespace string              `json:"authority_namespace,omitempty"`
	Perspective        UsagePerspective    `json:"perspective,omitempty"`
	LifecycleScope     UsageLifecycleScope `json:"lifecycle_scope,omitempty"`
	Basis              string              `json:"basis,omitempty"`
	RuleVersion        string              `json:"rule_version,omitempty"`
	Surfaced           UsageSurfaced       `json:"surfaced,omitempty"`
	ReservationType    AuthorityHandleType `json:"reservation_type,omitempty"`
	ParentRequestID    string              `json:"parent_request_id,omitempty"`
	BoundPolicyVersion VersionRef          `json:"bound_policy_version,omitzero"`
	BoundRatingVersion VersionRef          `json:"bound_rating_version,omitzero"`
}

// AccountingStatusReader is the stable service contract for reading the live
// accounting authority posture.
type AccountingStatusReader interface {
	Status(ctx context.Context) (AccountingAuthorityStatus, error)
}

// AccountingPageQueries is the stable service contract for bounded accounting
// authority query views.
type AccountingPageQueries interface {
	LimitStatus(ctx context.Context, q AccountingLimitStatusQuery) (Page[AccountingLimitStatusRow], error)
	Decisions(ctx context.Context, q AccountingDecisionQuery) (Page[AccountingDecisionRow], error)
}

// AccountingQueries composes live status and bounded page queries for the
// dedicated accounting authority control-plane surface.
type AccountingQueries interface {
	AccountingStatusReader
	AccountingPageQueries
}
