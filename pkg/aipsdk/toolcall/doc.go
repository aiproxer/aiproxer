// Package toolcall declares the completed-call finalizer SDK seam (ADR 0007 / issue #152).
//
// Core owns exact original event replay and must pass Finalize a defensive copy of
// ArgsJSON; finalizers must treat CompletedCall as immutable input.
//
// A generation may share each finalizer across concurrent requests and racing
// attempts. Implementations must synchronize mutable state, including reporters;
// immutable construction state needs no locking. Inputs must not be mutated or
// retained without copying. Finalize runs synchronously on the receive owner:
// it must honor ctx cancellation, keep CPU work bounded, and join any work it
// starts before returning. Panics are isolated, but panic recovery is not a
// timeout and cannot interrupt a blocked implementation.
//
// Phase 5 adapter contract: when a repair engine / finalizer returns ActionPass (or an
// engine OutcomePass) with nil ArgsJSON, core must replay the exact original buffered
// argument bytes and lifecycle events. Nil means “unchanged originals”, never empty args.
//
// A finalizer may also return ActionSteer with a SteerRequest to ask core to withhold
// the completed call and continue the turn once, hidden. Core may decline, and an
// unusable steer takes the same unusable-result fallback as any other result core cannot
// use; see Result.UsableSteer.
//
// A finalizer that must receive the complete assembled arguments before the call may be
// released declares that through the separate optional BufferingRequirement capability,
// read with a type assertion. Finalizer itself is unchanged, and a finalizer that declares
// nothing keeps its existing buffering behavior exactly.
package toolcall
