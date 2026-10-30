package controlplane

import (
	"context"
	"fmt"
	"time"

	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

// RetentionControllerConfig configures a [RetentionController] (requirement
// 6.1, 6.2, 6.6).
type RetentionControllerConfig struct {
	// Profile is the retention/redaction profile applied to evidence outside
	// the retained window.
	Profile RetentionProfile
	// Window is the retention duration. The controller derives the cutoff
	// from Clock.Now() - Window when the caller does not supply an explicit
	// cutoff.
	Window time.Duration
	// Clock supplies the current time for cutoff derivation and last-failure
	// timestamps. Defaults to SystemClock when nil.
	Clock Clock
}

// RetentionController applies configured retention and redaction commands
// through the store with safe status updates, without mutating routing,
// policy, usage, or session outcomes for active requests (requirements 6.1,
// 6.2, 6.3, 6.4, 6.5, 6.6, 7.2, 10.7).
//
// It runs only from runtime-owned lifecycle paths, startup maintenance, or
// explicit operator action. It does not start hidden goroutines. Detailed
// records, summaries, privileged evidence, and aggregate usage views remain
// distinct after retention or redaction changes visibility: the controller
// delegates all data-layer mutation to the store, which preserves allowed
// safe correlation metadata while withholding redacted fields (requirement
// 6.2, 6.4).
type RetentionController struct {
	store   RetentionApplier
	status  *Status
	profile RetentionProfile
	window  time.Duration
	clock   Clock
}

// NewRetentionController constructs a RetentionController bound to the
// supplied store and status. status may be nil.
func NewRetentionController(store RetentionApplier, status *Status, cfg RetentionControllerConfig) *RetentionController {
	clock := cfg.Clock
	if clock == nil {
		clock = SystemClock{}
	}
	return &RetentionController{store: store, status: status, profile: cfg.Profile, window: cfg.Window, clock: clock}
}

// Apply runs one retention/redaction pass against the store. If cutoff is zero,
// the controller derives it from Clock.Now() - Window (requirement 6.1).
// visibility selects the query visibility profile applied to redacted rows.
// On store failure the controller degrades status with a bounded
// [cp.ReasonRetentionFailure] reason and returns a wrapped [ErrDegraded]
// error without leaking raw infrastructure details (requirement 7.2, 7.3).
func (c *RetentionController) Apply(ctx context.Context, cutoff time.Time, visibility cp.Visibility) (RetentionResult, error) {
	if !c.profile.IsKnown() {
		return RetentionResult{}, fmt.Errorf("%w: unknown retention profile %q", ErrInvalidQuery, c.profile)
	}
	if visibility == "" {
		visibility = cp.VisibilityDefault
	}
	if !visibility.IsKnown() {
		return RetentionResult{}, fmt.Errorf("%w: unknown visibility %q", ErrInvalidQuery, visibility)
	}
	if cutoff.IsZero() {
		if c.window <= 0 {
			return RetentionResult{}, fmt.Errorf("%w: retention window is required when cutoff is omitted", ErrInvalidQuery)
		}
		cutoff = c.clock.Now().Add(-c.window)
	}
	cmd := RetentionCommand{
		Cutoff:     cutoff,
		Profile:    c.profile,
		Visibility: visibility,
	}
	res, err := c.store.ApplyRetention(ctx, cmd)
	if err != nil {
		c.degrade(cp.ReasonRetentionFailure)
		return RetentionResult{}, fmt.Errorf("%w: retention apply failed", ErrDegraded)
	}
	// The store guarantees idempotency at the data layer: repeated runs after
	// the same cutoff produce no additional visible records (requirement 6.1,
	// 6.6, design "Idempotency"). The controller does not invent additional
	// marks on top of the store result.
	if c.status != nil {
		res.Status = c.status.Snapshot()
	}
	return res, nil
}

// degrade records a bounded failure reason on the capability status.
func (c *RetentionController) degrade(reason cp.ReasonCode) {
	if c.status == nil {
		return
	}
	c.status.RecordFailure(reason, c.clock.Now())
}
