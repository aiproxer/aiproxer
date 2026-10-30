package runtimebundle

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// isNilUsageLimitPort reports untyped nil and typed-nil authority ports so a
// boxed nil port is rejected at the build boundary instead of panicking later.
func isNilUsageLimitPort(port usagelimit.Authority) bool {
	if port == nil {
		return true
	}
	rv := reflect.ValueOf(port)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}

// validateRequiredAuthorityEvidenceWiring protects callers that assemble a
// runtime bundle without first running config.Validate. A required pre-work
// accounting-authority category is meaningless without both the authority
// capability, a live control-plane recorder, and an injected usage-limit port
// that can receive the required evidence.
func validateRequiredAuthorityEvidenceWiring(cfg *config.Config, port usagelimit.Authority) error {
	if cfg == nil || !strings.EqualFold(strings.TrimSpace(cfg.ControlPlane.RecordingPolicy), "required_pre_work") {
		return nil
	}
	required := slices.ContainsFunc(cfg.ControlPlane.RequiredCategories, func(category string) bool {
		return strings.EqualFold(strings.TrimSpace(category), "accounting_authority")
	})
	if !required {
		return nil
	}
	if !cfg.ControlPlane.Enabled {
		return fmt.Errorf("runtimebundle: control_plane.enabled: must be true when accounting_authority is required under required_pre_work")
	}
	if !cfg.Accounting.Authority.Enabled {
		return fmt.Errorf("runtimebundle: accounting.authority.enabled: must be true when accounting_authority is required under required_pre_work")
	}
	if port == nil {
		return fmt.Errorf("runtimebundle: usage_limits: accounting_authority evidence is required under required_pre_work but no usage-limit port is injected; this requires AIProxer Enterprise (capability %q)", config.CapabilityUsageLimits)
	}
	if _, ok := port.(usagelimit.EvidenceSinkSetter); !ok {
		return fmt.Errorf("runtimebundle: usage_limits: the injected usage-limit port cannot receive required accounting_authority evidence (missing SetEvidenceSink)")
	}
	return nil
}

func disposeClosers(closers []func() error) error {
	var out error
	for _, closer := range slices.Backward(closers) {
		if err := closer(); err != nil {
			out = errors.Join(out, fmt.Errorf("runtimebundle: dispose closer: %w", err))
		}
	}
	return out
}

// withDisposedClosers disposes closers and returns err unchanged on successful
// disposal, or errors.Join(err, derr) when disposal fails. It treats a nil err
// defensively so callers can pass build errors without a separate nil check.
func withDisposedClosers(err error, closers []func() error) error {
	if derr := disposeClosers(closers); derr != nil {
		return errors.Join(err, derr)
	}
	return err
}

// RegisterPluginBuildCleanup appends an idempotent plugin BuildResult cleanup
// immediately during assembly so later inventory/accounting/server failures
// reverse-dispose process/pipe/staging resources (Phase 3 composition seam).
func RegisterPluginBuildCleanup(closers []func() error, cleanup func() error) []func() error {
	if cleanup == nil {
		return closers
	}
	var once sync.Once
	var err error
	return append(closers, func() error {
		once.Do(func() {
			err = normalizePluginCleanupErr(cleanup())
		})
		return err
	})
}

// normalizePluginCleanupErr treats transport death after process-host reap as
// successful cleanup completion. Generation-owned plugin cleanup may run after
// a process-owned host closer already closed the gRPC conn; that must not
// surface as a ShutdownDetached/ledger failure (cleanup still ran at most once).
//
// Only the transport-closure component is dropped. A composite cleanup error
// can carry independent causes next to the transport failure — the connector
// output-budget terminal cause among them — and dropping the whole error
// because one constituent is a transport death would swallow them. The whole
// error tree is traversed, so a wrapper around a joined error keeps its
// independent causes too.
func normalizePluginCleanupErr(err error) error {
	if err == nil {
		return nil
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		kept := make([]error, 0, len(joined.Unwrap()))
		for _, child := range joined.Unwrap() {
			if normalized := normalizePluginCleanupErr(child); normalized != nil {
				kept = append(kept, normalized)
			}
		}
		if len(kept) == 1 {
			return kept[0]
		}
		return errors.Join(kept...)
	}
	if inner := errors.Unwrap(err); inner != nil {
		// A single wrapper defers to the error it wraps: a wrapper around the
		// transport death alone stays harmless, and a wrapper around a
		// composite cause keeps only the normalized causes. The wrapper's own
		// identity goes with the components it carried, so a discarded
		// transport classification can never survive next to the cause that
		// actually ended the generation.
		return normalizePluginCleanupErr(inner)
	}
	if st, ok := status.FromError(err); ok && st.Code() == codes.Unavailable {
		return nil
	}
	// errors.Join and dial wrappers may not expose a bare status code.
	msg := err.Error()
	if strings.Contains(msg, "use of closed network connection") ||
		strings.Contains(msg, "code = Unavailable") {
		return nil
	}
	return err
}
