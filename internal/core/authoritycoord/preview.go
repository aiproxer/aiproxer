package authoritycoord

import (
	"context"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
)

func (c *AttemptCoordinator) PreviewClamps(ctx context.Context, in authority.AttemptAdmission) ([]authority.Clamp, error) {
	if c == nil {
		return nil, nil
	}
	if err := in.Validate(); err != nil {
		return nil, err
	}
	slots := attemptStageSlots(c.Slots)
	if err := validateAttemptPriorityClasses(c.Slots); err != nil {
		return nil, err
	}
	if err := validateStageSlots(slots, func(p authority.AttemptProvider) bool { return p == nil }); err != nil {
		return nil, err
	}
	var merged []authority.Clamp
	sortStageSlots(slots)
	for _, slot := range slots {
		previewer, ok := slot.provider.(authority.AttemptClampPreviewer)
		if !ok || previewer == nil {
			continue
		}
		id := strings.TrimSpace(slot.id)
		d, err := invokePreviewAttempt(ctx, previewer, in)
		if err != nil {
			strength, _ := resolveStagePosture(slot)
			if strength == authority.StrengthRequired {
				return nil, fmt.Errorf("authoritycoord: preview %s: %w", id, err)
			}
			continue
		}
		if len(d.Reservations) > 0 || strings.TrimSpace(d.CompensationHandle) != "" {
			return nil, fmt.Errorf("authoritycoord: preview %s returned holds (side-effect free preview forbids reservations)", id)
		}
		for _, clamp := range d.Clamps {
			if err := validatePreviewClamp(clamp); err != nil {
				return nil, fmt.Errorf("authoritycoord: preview %s: %w", id, err)
			}
		}
		var merr error
		merged, merr = mergeClampsNonWidening(merged, d.Clamps)
		if merr != nil {
			return nil, fmt.Errorf("authoritycoord: preview %s: %w", id, merr)
		}
	}
	return merged, nil
}
