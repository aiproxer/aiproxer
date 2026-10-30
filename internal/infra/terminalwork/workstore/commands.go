package workstore

import (
	"fmt"
	"strings"
	"time"

	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

func normalizeClaimDueCommand(cmd *ClaimDueCommand, nowFn func() time.Time) error {
	if cmd == nil {
		return fmt.Errorf("%w: nil claim command", sdk.ErrInvalid)
	}
	if strings.TrimSpace(cmd.OwnerID) == "" {
		return fmt.Errorf("%w: empty claim owner", sdk.ErrInvalid)
	}
	if cmd.TTL <= 0 {
		return fmt.Errorf("%w: non-positive claim ttl", sdk.ErrInvalid)
	}
	if cmd.Limit < 0 {
		return fmt.Errorf("%w: negative claim limit", sdk.ErrInvalid)
	}
	if cmd.Now.IsZero() {
		if nowFn == nil {
			nowFn = time.Now
		}
		cmd.Now = nowFn().UTC()
	} else {
		cmd.Now = cmd.Now.UTC()
	}
	if cmd.Limit == 0 {
		cmd.Limit = 1
	}
	return nil
}

func normalizeRenewClaimCommand(cmd *RenewClaimCommand, nowFn func() time.Time) error {
	if cmd == nil {
		return fmt.Errorf("%w: nil renew command", sdk.ErrInvalid)
	}
	if strings.TrimSpace(cmd.WorkID) == "" {
		return fmt.Errorf("%w: empty work id", sdk.ErrInvalid)
	}
	if strings.TrimSpace(cmd.OwnerID) == "" {
		return fmt.Errorf("%w: empty claim owner", sdk.ErrInvalid)
	}
	if cmd.TTL <= 0 {
		return fmt.Errorf("%w: non-positive claim ttl", sdk.ErrInvalid)
	}
	if cmd.Now.IsZero() {
		if nowFn == nil {
			nowFn = time.Now
		}
		cmd.Now = nowFn().UTC()
	} else {
		cmd.Now = cmd.Now.UTC()
	}
	return nil
}
