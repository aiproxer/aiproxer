package configsource

import (
	"errors"
	"fmt"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

// IntegrityError is a secret-safe source-integrity or pre-decode failure.
// Error strings carry only the category (and bounded limits), never raw bytes.
type IntegrityError struct {
	Category Category
	Limit    int64 // set for oversize; otherwise 0
	reason   string
}

func (e *IntegrityError) Error() string {
	if e == nil {
		return "configsource: unknown"
	}
	if e.Category == CategoryOversize && e.Limit > 0 {
		return fmt.Sprintf("configsource: %s (limit %d bytes)", e.Category, e.Limit)
	}
	return fmt.Sprintf("configsource: %s", e.Category)
}

// CategoryOf returns the integrity/decode category when err wraps or is an
// IntegrityError or a config.LoadError from the shared effective-load pipeline.
func CategoryOf(err error) (Category, bool) {
	if err == nil {
		return "", false
	}
	var ie *IntegrityError
	if errors.As(err, &ie) && ie != nil && ie.Category != "" {
		return ie.Category, true
	}
	var le *config.LoadError
	if errors.As(err, &le) && le != nil && le.Category != "" {
		return le.Category, true
	}
	return "", false
}

func integrityErr(cat Category) error {
	return &IntegrityError{Category: cat}
}

func oversizeErr(limit int64) error {
	return &IntegrityError{Category: CategoryOversize, Limit: limit}
}

func leaseIntegrityErr(reason string) error {
	switch reason {
	case "lease_unavailable", "lease_closed", "lease_provenance", "lease_device":
	default:
		reason = "lease_unavailable"
	}
	return &IntegrityError{Category: CategoryNonAtomicUpdate, reason: reason}
}

// SafeReasonOf returns only the bounded private reason attached to source
// lease integrity failures. It never returns operating-system error text.
func SafeReasonOf(err error) string {
	var ie *IntegrityError
	if !errors.As(err, &ie) || ie == nil {
		return ""
	}
	switch ie.reason {
	case "lease_unavailable", "lease_closed", "lease_provenance", "lease_device":
		return ie.reason
	default:
		return ""
	}
}
