package policydecision

// AccountingReasonCode is the bounded reason taxonomy used when projecting
// accounting decisions into policy-compatible evidence.
type AccountingReasonCode string

const (
	AccountingReasonAllowed           AccountingReasonCode = "allowed"
	AccountingReasonAdvisory          AccountingReasonCode = "advisory"
	AccountingReasonClamped           AccountingReasonCode = "clamped"
	AccountingReasonReserved          AccountingReasonCode = "reserved"
	AccountingReasonReconciled        AccountingReasonCode = "reconciled"
	AccountingReasonQuotaExceeded     AccountingReasonCode = "quota_exceeded"
	AccountingReasonRateLimited       AccountingReasonCode = "rate_limited"
	AccountingReasonBudgetExceeded    AccountingReasonCode = "budget_exceeded"
	AccountingReasonReservationFailed AccountingReasonCode = "reservation_failed"
	AccountingReasonUnavailable       AccountingReasonCode = "unavailable"
	AccountingReasonError             AccountingReasonCode = "error"
)

// IsKnown reports whether r is one of the documented accounting reason codes.
func (r AccountingReasonCode) IsKnown() bool {
	switch r {
	case AccountingReasonAllowed, AccountingReasonAdvisory, AccountingReasonClamped, AccountingReasonReserved,
		AccountingReasonReconciled, AccountingReasonQuotaExceeded, AccountingReasonRateLimited,
		AccountingReasonBudgetExceeded, AccountingReasonReservationFailed, AccountingReasonUnavailable,
		AccountingReasonError:
		return true
	}
	return false
}

// AccountingProjection carries safe accounting metadata that may be projected
// into a policydecision.Record without changing the base policy outcome/effect.
type AccountingProjection struct {
	ReasonCode       AccountingReasonCode
	RuleID           string
	Authority        string
	ReservationID    string
	SettlementStatus string
}

// ProjectAccountingRecord attaches bounded accounting annotations to record
// while preserving the base decision fields. It returns ok=false when a caller
// supplies an unknown accounting reason code or when the resulting record is not
// a legal policydecision record.
func ProjectAccountingRecord(record Record, projection AccountingProjection) (Record, bool) {
	if projection.ReasonCode != "" && !projection.ReasonCode.IsKnown() {
		return Record{}, false
	}

	out := record.Clone()
	if projection.ReasonCode != "" {
		out.ReasonCode = string(projection.ReasonCode)
	}
	if out.ClientCategory == "" {
		out.ClientCategory = accountingClientCategory(out)
	}

	setAnnotation := func(key, val string) {
		if val == "" {
			return
		}
		if out.Annotations == nil {
			out.Annotations = make(map[string]string)
		}
		out.Annotations[key] = val
	}

	setAnnotation("accounting.rule_id", projection.RuleID)
	setAnnotation("accounting.reason", out.ReasonCode)
	setAnnotation("accounting.authority", projection.Authority)
	setAnnotation("accounting.reservation_id", projection.ReservationID)
	setAnnotation("accounting.settlement_status", projection.SettlementStatus)

	out = NormalizeRecord(out)
	if err := ValidateRecord(out); err != nil {
		return Record{}, false
	}
	return out, true
}

func accountingClientCategory(record Record) string {
	switch record.Outcome {
	case OutcomeDeny:
		return CategoryDenied
	case OutcomeError:
		return CategoryFailure
	case OutcomeSkip:
		return CategorySkipped
	default:
		return CategoryAllowed
	}
}
