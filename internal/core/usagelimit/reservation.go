package usagelimit

import (
	"strconv"
	"strings"
)

type ReservationKey struct {
	LogicalRequestID string
	ALegID           string
	BLegID           string
	AttemptID        string
	RuleID           string
	Sequence         int
	// Namespace isolates dual-plane reservations. Empty keeps the pre-Phase-7
	// key string format for legacy compatibility.
	Namespace string
}

func (k ReservationKey) String() string {
	base := k.LogicalRequestID + "|" + k.ALegID + "|" + k.BLegID + "|" + k.AttemptID + "|" + k.RuleID + "|" + strconv.Itoa(k.Sequence)
	if ns := strings.TrimSpace(k.Namespace); ns != "" {
		return ns + "|" + base
	}
	return base
}

type SettlementKey struct {
	ReservationKey ReservationKey
	Sequence       int
}

func (k SettlementKey) String() string {
	return k.ReservationKey.String() + "|settle|" + strconv.Itoa(k.Sequence)
}

type ReleaseKey struct {
	ReservationKey ReservationKey
	Sequence       int
}

func (k ReleaseKey) String() string {
	return k.ReservationKey.String() + "|release|" + strconv.Itoa(k.Sequence)
}
