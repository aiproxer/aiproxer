package usagelimit

// DeriveSettleScalars copies aggregate fields from the first reservation descriptor
// when callers supply only the descriptor set (runtime lifecycle path).
func DeriveSettleScalars(in SettleInput) SettleInput {
	if len(in.Reservations) == 0 {
		return in
	}
	first := in.Reservations[0]
	if in.ReservationKey.LogicalRequestID == "" {
		in.ReservationKey = first.Reservation.ReservationKey
	}
	if in.ReservationID == "" {
		in.ReservationID = first.Reservation.ReservationID
	}
	if in.RuleID == "" {
		in.RuleID = first.Reservation.RuleID
	}
	if in.Authority == "" {
		in.Authority = first.Authority
	}
	if in.MeasurementAuthority.Usage == "" {
		in.MeasurementAuthority = first.MeasurementAuthority
	}
	if in.FinalUsage.Unit == "" {
		in.FinalUsage = first.FinalUsage
	}
	if in.ReservedUsage.Unit == "" {
		in.ReservedUsage = first.Reservation.Amount
	}
	if in.EstimatedUsage.Unit == "" {
		in.EstimatedUsage = first.EstimatedUsage
	}
	return in
}

// DeriveReleaseScalars copies aggregate fields from the first reservation descriptor
// when callers supply only the descriptor set (runtime lifecycle path).
func DeriveReleaseScalars(in ReleaseInput) ReleaseInput {
	if len(in.Reservations) == 0 {
		return in
	}
	first := in.Reservations[0].Reservation
	if in.ReservationKey.LogicalRequestID == "" {
		in.ReservationKey = first.ReservationKey
	}
	if in.ReservationID == "" {
		in.ReservationID = first.ReservationID
	}
	if in.RuleID == "" {
		in.RuleID = first.RuleID
	}
	if in.Amount.Unit == "" {
		in.Amount = first.Amount
	}
	return in
}
