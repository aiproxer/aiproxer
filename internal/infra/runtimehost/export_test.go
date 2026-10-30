package runtimehost

import "time"

// NewManualClock returns a controllable clock starting at t.
func NewManualClock(t time.Time) *ManualClock { return &ManualClock{t: t} }
