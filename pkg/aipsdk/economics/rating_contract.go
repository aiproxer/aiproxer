package economics

import (
	"errors"
	"fmt"
	"strings"
)

// MaxRatingRules bounds the immutable rule material accepted from a source
// or a host configuration. The bound keeps publication and replay work
// deterministic without imposing a provider-specific catalog.
const MaxRatingRules = 1024

var ErrInvalidTariffSnapshot = errors.New("economics: invalid tariff snapshot")

func NormalizeCurrencyRequired(currency string) error {
	if strings.TrimSpace(currency) == "" {
		return fmt.Errorf("currency required")
	}
	_, err := NormalizeCurrency(currency)
	return err
}
