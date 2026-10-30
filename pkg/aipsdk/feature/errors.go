package feature

import (
	"errors"
	"fmt"
)

var (
	// ErrInvalidPlane indicates an invalid or incomplete plane declaration.
	ErrInvalidPlane = errors.New("feature: invalid plane declaration")

	// ErrInvalidContribution indicates an invalid contribution to a plane.
	ErrInvalidContribution = errors.New("feature: invalid contribution")

	// ErrUnsupportedSource indicates a contribution from a source not supported by the plane.
	ErrUnsupportedSource = errors.New("feature: unsupported contribution source")

	// ErrExclusiveConflict reports an attempted second contribution to an exclusive plane slot.
	ErrExclusiveConflict = errors.New("feature: exclusive slot conflict")

	// ErrTerminalDecisionProviderConflict reports a conflict when multiple terminal-decision providers are registered.
	ErrTerminalDecisionProviderConflict = errors.New("featurebundle: terminal-decision provider conflict")

	// ErrControlToolProviderConflict reports a conflict when multiple proxy control-tool providers are registered.
	ErrControlToolProviderConflict = errors.New("featurebundle: control-tool provider conflict")

	// ErrNilContribution reports a nil contribution rejected by plane nil policy.
	ErrNilContribution = errors.New("feature: nil contribution")

	// ErrUnsupportedReplaySource reports a source that all-plane frozen replay cannot apply safely.
	ErrUnsupportedReplaySource = errors.New("feature: unsupported frozen replay source")

	// ErrUngeneratedPlane indicates an attempt to contribute through an ungenerated or unbound plane.
	// In v1, the standard-plane catalog is closed; arbitrary dynamic planes are rejected with this error.
	ErrUngeneratedPlane = errors.New("feature: ungenerated plane")
)

// makeExclusiveConflictError creates a single *AttributedError for an exclusive plane conflict.
func makeExclusiveConflictError(contributorID, planeID string, compatErr error, existingID, incomingID string) *AttributedError {
	cause := error(ErrExclusiveConflict)
	if compatErr != nil {
		cause = errors.Join(cause, compatErr)
	}
	return &AttributedError{
		PluginID: contributorID,
		PlaneID:  planeID,
		Err:      fmt.Errorf("%w: %q and %q", cause, existingID, incomingID),
	}
}

// AttributedError wraps an underlying error with the plugin ID and plane ID
// where the contribution or validation failure occurred.
type AttributedError struct {
	PluginID string
	PlaneID  string
	Err      error
}

func (e *AttributedError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.PluginID != "" && e.PlaneID != "" {
		return fmt.Sprintf("feature: plugin %q plane %q: %v", e.PluginID, e.PlaneID, e.Err)
	}
	if e.PlaneID != "" {
		return fmt.Sprintf("feature: plane %q: %v", e.PlaneID, e.Err)
	}
	if e.PluginID != "" {
		return fmt.Sprintf("feature: plugin %q: %v", e.PluginID, e.Err)
	}
	return fmt.Sprintf("feature: %v", e.Err)
}

func (e *AttributedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *AttributedError) Is(target error) bool {
	if target == nil {
		return false
	}
	return errors.Is(e.Err, target)
}

// planeValidationError records a validation failure bound to a specific plane ID.
type planeValidationError struct {
	planeID string
	err     error
}

func (e *planeValidationError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s: %v", e.planeID, e.err)
}

func (e *planeValidationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func (e *planeValidationError) Is(target error) bool {
	if target == nil {
		return false
	}
	return errors.Is(e.err, target)
}

func newPlaneValidationError(planeID string, err error) error {
	if err == nil {
		return nil
	}
	return &planeValidationError{
		planeID: planeID,
		err:     err,
	}
}

func attributeReplayValidationError(err error, contributorID string) error {
	if err == nil {
		return nil
	}
	if pve, ok := errors.AsType[*planeValidationError](err); ok {
		return &AttributedError{
			PluginID: contributorID,
			PlaneID:  pve.planeID,
			Err:      fmt.Errorf("%w: %w", ErrInvalidContribution, pve.err),
		}
	}
	if _, ok := errors.AsType[*AttributedError](err); ok {
		return err
	}
	return &AttributedError{
		PluginID: contributorID,
		PlaneID:  "",
		Err:      fmt.Errorf("%w: %w", ErrInvalidContribution, err),
	}
}
