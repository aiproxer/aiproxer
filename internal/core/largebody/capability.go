package largebody

import (
	"context"

	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// LargeBodyAssessor evaluates frontend proof and returns an Assessment
// containing an opaque generation/proof-bound stamp and bounded facts only (Task 11.1).
// The frontend supplies proof only; it cannot synthesize route/backend internals
// (Requirements 6, 22).
type LargeBodyAssessor interface {
	// AssessLargeBody is side-effect-free proof assessment over bounded facts.
	AssessLargeBody(ctx context.Context, proof Proof) (Assessment, error)
}

// LargeBodyWireExecutor runs an accepted wire turn from the bound assessment
// and immutable replay source (Task 11.1, Requirements 6, 22).
//
// Source-transfer contract (large-payload-lifecycle-correctness Requirements
// 1.1-1.3; internal contract, not a public API/ABI):
//   - On usable success (a nil error with a non-nil Stream), the returned
//     ExecutionResult lifetime owns source cleanup: the caller closes the
//     returned Stream, never the root Source. The production runtime releases
//     the root after receive teardown, closing the execution stream before the
//     replay source and releasing that source at most once.
//   - The production runtime preserves pre-return error/panic rollback: when
//     ExecuteLargeBody returns an error (or panics), no usable lifetime is
//     transferred and the untransferred root is closed before returning.
//   - Failed results and nil-success results (a nil error with a nil Stream)
//     transfer no usable lifetime. The caller keeps root ownership, closes the
//     untransferred root, and reports the existing safe error shape. Callers
//     must never fall back to canonical execution after this commitment, and
//     must not retry after downstream content commitment.
type LargeBodyWireExecutor interface {
	// ExecuteLargeBody runs the accepted wire turn from the bound assessment and
	// immutable replay source. It is reachable only after assessment accepts.
	ExecuteLargeBody(ctx context.Context, accepted Assessment, src Source) (ExecutionResult, error)
}

// LargeBodyStaticDispositionProvider provides an O(1) static wire disposition for a given profile.
type LargeBodyStaticDispositionProvider interface {
	LargeBodyStaticDisposition(profileID string) (StaticWireDisposition, StaticWireReason)
}

// LargeBodyExecutor is the internal optional large-body capability for the
// large-payload streaming fast path (design section 8, assessor/executor
// interface, Task 11.1).
//
// It is deliberately NOT part of the public aipsdk.ExecutorView contract
// (Requirements 1, 22): existing public ExecutorView remains supported and
// unchanged, and external/manual frontends/executors stay source-compatible
// and canonical-only. The standard bundled frontend path type-asserts this
// capability from its aipsdk.ExecutorView; absence selects the canonical path
// with no behavior change.
//
// Ownership (steering structure SDK zones, product core/plugin ownership):
// provider-neutral DTOs and this seam live in internal core; provider/frontend
// semantics stay in adapters/plugins. Core never imports concrete plugins or
// provider SDKs here (stdlib plus pkg/aipsdk only).
type LargeBodyExecutor interface {
	LargeBodyAssessor
	LargeBodyWireExecutor
}

// AsLargeBodyAssessor safely probes the executor for the internal large-body
// assessor capability. A nil executor or an executor without the capability
// reports ok=false so the caller continues on the canonical path.
func AsLargeBodyAssessor(exec aipsdk.ExecutorView) (LargeBodyAssessor, bool) {
	if exec == nil {
		return nil, false
	}
	assessor, ok := exec.(LargeBodyAssessor)
	if !ok || assessor == nil {
		return nil, false
	}
	return assessor, true
}

// AsLargeBodyExecutor safely probes the standard frontend executor for the
// internal large-body capability. A nil executor or an executor without the
// capability reports ok=false so the caller continues on the canonical path.
func AsLargeBodyExecutor(exec aipsdk.ExecutorView) (LargeBodyExecutor, bool) {
	if exec == nil {
		return nil, false
	}
	capability, ok := exec.(LargeBodyExecutor)
	if !ok || capability == nil {
		return nil, false
	}
	return capability, true
}
