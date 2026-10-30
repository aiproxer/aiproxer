package reasoningpreservation

import (
	"fmt"
	"reflect"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
)

// CompressionServices bundles generation-local capabilities required when
// compression is enabled. Capabilities are carried explicitly per construction;
// no global service locator, no DI container, no init() globals (Decision 6).
//
// Single-value pattern: a process-owned scheduler implements both
// auxiliary.BackgroundClient and auxiliary.BackgroundPoller. Caller passes the same
// value for both Client and Poller fields. Two-field representation is chosen for
// explicitness and to keep disabled mode zero-value clean without type assertions
// inside the feature.
type CompressionServices struct {
	// Client is the generation-bound BackgroundClient (Scheduler.BindRunner result).
	Client auxiliary.BackgroundClient
	// Poller is the optional non-blocking poll capability. When compression is
	// enabled Poller must be non-nil; when disabled it must be nil (no extra goroutines).
	Poller auxiliary.BackgroundPoller
	// EgressPolicy is the trusted egress decision authority for purpose
	// reasoning_semantic_compression. When compression is enabled it must be non-nil;
	// route string alone never approves (EvaluateEgress enforces).
	EgressPolicy EgressPolicy
	// Sanitizer is the trusted redaction authority reused for redact-then-allow.
	// When compression is enabled it must be non-nil so redact decisions can succeed;
	// deny remains possible via policy, but missing sanitizer would make redaction fail closed.
	Sanitizer TrustedTextSanitizer
}

func isNilCapability(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice, reflect.UnsafePointer:
		return rv.IsNil()
	default:
		return false
	}
}

func (s CompressionServices) validateFor(cfg Config) error {
	if !cfg.Compression.Enabled {
		return nil
	}
	if isNilCapability(s.Client) {
		return fmt.Errorf("%s: compression enabled requires BackgroundClient", ID)
	}
	if isNilCapability(s.Poller) {
		return fmt.Errorf("%s: compression enabled requires BackgroundPoller", ID)
	}
	if isNilCapability(s.EgressPolicy) {
		return fmt.Errorf("%s: compression enabled requires EgressPolicy", ID)
	}
	if isNilCapability(s.Sanitizer) {
		return fmt.Errorf("%s: compression enabled requires TrustedTextSanitizer", ID)
	}
	return nil
}
