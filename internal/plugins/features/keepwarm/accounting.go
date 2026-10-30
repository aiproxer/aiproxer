package keepwarm

import (
	"context"
	"time"
)

const maxAccountingAttempts = 3

func (m *Manager) deliverAccounting(parent context.Context, timeout time.Duration, record RenewalRecord) error {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	var err error
	for range maxAccountingAttempts {
		err = m.hooks.Accounting(ctx, record)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	return err
}
