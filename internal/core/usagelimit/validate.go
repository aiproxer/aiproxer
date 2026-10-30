package usagelimit

import (
	"errors"
	"fmt"
	"strings"
)

var (
	ErrInvalidAmount            = errors.New("invalid amount")
	ErrRetiredMonetaryAuthority = errors.New("retired monetary usage authority rule; migration required")
)

func (a Amount) Validate() error {
	if strings.EqualFold(string(a.Unit), "money_nano") || strings.EqualFold(string(a.Unit), "money-nano") || strings.EqualFold(string(a.Unit), "moneynano") {
		return fmt.Errorf("%w: unit %q", ErrRetiredMonetaryAuthority, a.Unit)
	}
	if !a.Unit.IsKnown() {
		return fmt.Errorf("%w: unsupported unit %q", ErrInvalidAmount, a.Unit)
	}
	if a.Value < 0 {
		return fmt.Errorf("%w: negative value %d", ErrInvalidAmount, a.Value)
	}
	return nil
}
