package extensions

import (
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
)

// ValidateDecisionRecord is a thin compatibility wrapper that delegates to
// [policydecision.ValidateRecord]. It is retained because existing tests and
// runtime integration call it directly; new callers should use the SDK
// validator (requirements 1.5, 3.6, 4.4, 6.6).
func ValidateDecisionRecord(record policydecision.Record) error {
	return policydecision.ValidateRecord(record)
}
