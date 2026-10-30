package proxymemory

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// The feature-owned pre-open invariant port (design §2).
//
// The feature cannot see the executor, so the composition root binds this
// port and the core calls it at the provider-open chokepoint: after every
// mutating request transform, after the post-hook re-derive and its
// negotiated downgrade, after the route-parameter merge and after the
// projection adaptation, and before any provider I/O.
//
// The inputs are content-free by construction: a target, the frozen
// expectation and a role label. The feature never receives a prompt, a
// transcript, a memory or a canonical call.

// InstallationState is the deployment fact about one (backend, model) pair.
//
// It is a fact about the deployment, never about the operator's policy: the
// allowed inventory constrains what an owner may choose, and it is never
// presented as installation (design §3; Requirement 1.4, U7).
type InstallationState uint8

const (
	// InstallationUnknown means the publication does not prove whether the
	// backend carries the model. It is the zero value and the safe default.
	InstallationUnknown InstallationState = iota
	// InstallationPresent means the publication names the model for the
	// backend.
	InstallationPresent
	// InstallationAbsent means the publication proves the backend does not
	// carry the model.
	InstallationAbsent
)

// EffortSupportState is the provider-owned answer for one reasoning-effort
// value on one candidate's model.
//
// It is a query result, not a vocabulary: the provider states what it
// carries, and the root never carries a global list of effort values. The
// three states never collapse: "the provider published no fact" is not "the
// provider supports nothing" (design §2; Requirement 1.4, U8).
type EffortSupportState uint8

const (
	// EffortSupportUnknown means the provider published no graded fact for
	// this value and model. It is the zero value and the safe default.
	EffortSupportUnknown EffortSupportState = iota
	// EffortSupportSupported means the provider states it carries this value
	// for this model as a graded level.
	EffortSupportSupported
	// EffortSupportUnsupported means the provider states it does not carry
	// this value for this model.
	EffortSupportUnsupported
)

// AttemptGuard is the feature-owned pre-open invariant port.
//
// A bound guard that refuses refuses the whole operation: the phase fails
// explicitly instead of silently serving a changed target.
type AttemptGuard interface {
	// CheckAttempt validates the attempt that is about to open. A non-nil
	// error refuses the attempt before any provider I/O.
	CheckAttempt(ctx context.Context, in AttemptGuardInput) error
}

// NegotiatedDowngradeGuard is the optional extension of the pre-open guard
// that runs at the two places where the runtime applies a negotiated
// downgrade: the pre-open candidate admission and the post-request transform.
//
// The runtime consults it before it strips the downgraded capabilities, so a
// governed phase that carried a requested reasoning effort is refused instead
// of being silently served with the effort negotiated away. A guard that does
// not implement this extension keeps the runtime's generic behaviour, and an
// implementation refuses only the roles it governs.
type NegotiatedDowngradeGuard interface {
	// CheckNegotiatedDowngrade refuses one negotiated downgrade before the
	// runtime applies it. A non-nil error refuses the whole operation.
	CheckNegotiatedDowngrade(ctx context.Context, in NegotiatedDowngradeInput) error
}

// NegotiatedDowngradeInput is the content-free content of one pre-apply
// downgrade check.
//
// It carries the opaque role label, the candidate identity and the effort the
// runtime is about to strip. It never carries a prompt, a transcript or a
// memory.
type NegotiatedDowngradeInput struct {
	// Role is the opaque auxiliary role of the call, or "".
	Role string
	// CandidateKey is the runtime's opaque identity of the candidate.
	CandidateKey string
	// Backend and Model are the target the runtime is about to open.
	Backend string
	Model   string
	// Downgraded are the capabilities the negotiation classified as soft
	// downgrades, in the runtime's own order.
	Downgraded []aipapi.Capability
	// ReasoningEffort is the effort the attempt carries before the strip.
	ReasoningEffort string
	// ExpectedReasoningEffort is the effort the served call must carry: the
	// frozen baseline merged with the candidate's route parameters. A strip
	// of a different effort does not change what the call serves.
	ExpectedReasoningEffort string
}

// AttemptGuardInput is the content-free content of one pre-open check.
type AttemptGuardInput struct {
	// Role is the opaque auxiliary role of the attempt, or "".
	Role string
	// CandidateKey is the runtime's opaque identity of the candidate the
	// attempt is about to open. It is compared, never parsed.
	CandidateKey string
	// Backend and Model are the target the attempt is about to open.
	Backend string
	Model   string
	// BaselineRoute is the frozen dispatched route selector.
	BaselineRoute string
	// BaselineOptions are the frozen dispatched generation options.
	BaselineOptions aipapi.GenerationOptions
	// ExpectedOptions are the frozen baseline options merged with the actual
	// candidate's route parameters: the options the attempt must still carry.
	ExpectedOptions aipapi.GenerationOptions
	// AttemptOptions are the options the attempt carries at the open point.
	AttemptOptions aipapi.GenerationOptions
	// Installation is the deployment fact about the (backend, model) pair.
	Installation InstallationState
	// EffortSupport is the provider's own answer for the effort the attempt
	// carries.
	EffortSupport EffortSupportState
}

// attemptGuard is the feature's guard implementation. It holds the
// generation-bound policy and the generation-bound observability seams, keeps
// no per-call state and allocates no per-request goroutine.
type attemptGuard struct {
	cfg     Config
	metrics *Metrics
	log     *slog.Logger
}

// NewAttemptGuard returns the feature's pre-open guard for one generation.
//
// A configuration that declares no model policy keeps the legacy behaviour:
// the guard is inert and the phase dispatches with the operator's route.
func NewAttemptGuard(cfg Config, metrics *Metrics, log *slog.Logger) AttemptGuard {
	return attemptGuard{cfg: cfg, metrics: metrics, log: log}
}

// phaseForRole maps an auxiliary role label to the phase it names.
//
// The label is a label, not an authority: it says which request produced the
// attempt, never who may make it. A role that names no phase of this feature
// leaves the guard inert, so ordinary traffic is untouched.
func phaseForRole(role string) (Phase, bool) {
	switch role {
	case phase1Role:
		return PhaseExtraction, true
	case phase2Role:
		return PhaseConsolidation, true
	}
	return "", false
}

// GovernsRole reports whether the guard governs an attempt with the given
// auxiliary role. It reuses phaseForRole and the phase-policy selection, so
// it never duplicates role literals. The identical checks inside CheckAttempt
// remain as defense in depth.
func (g attemptGuard) GovernsRole(role string) bool {
	if !g.cfg.ModelsDeclared {
		return false
	}
	phase, ok := phaseForRole(role)
	if !ok {
		return false
	}
	_, ok = g.cfg.Models.For(phase)
	return ok
}

// CheckAttempt applies the phase's policy to the attempt that is about to
// open.
//
// The refusal is explicit and content-free: it names the phase and the field
// and never echoes a value, so it can be logged without carrying an
// operator-supplied or owner-supplied string.
func (g attemptGuard) CheckAttempt(ctx context.Context, in AttemptGuardInput) error {
	if !g.cfg.ModelsDeclared {
		return nil
	}
	phase, ok := phaseForRole(in.Role)
	if !ok {
		return nil
	}
	policy, ok := g.cfg.Models.For(phase)
	if !ok {
		return nil
	}
	// The frozen invariant: the attempt must still carry the reasoning
	// effort the frozen baseline resolved to for this candidate. A request
	// transform that rewrote the effort and the runtime's own negotiated
	// downgrade that stripped it both land here, and both are refused
	// instead of being served (design §2; Requirement 1.4, 1.5).
	if in.AttemptOptions.ReasoningEffort != in.ExpectedOptions.ReasoningEffort {
		return &PolicyError{
			Phase:  phase,
			Field:  FieldReasoningEffort,
			Reason: "the attempt no longer carries the frozen reasoning effort",
		}
	}
	// The inventory must cover the target that is about to open.
	if err := checkInventoryField(phase, FieldBackend, policy.Inventory.Backends, in.Backend); err != nil {
		return err
	}
	if err := checkInventoryField(phase, FieldModel, policy.Inventory.Models, in.Model); err != nil {
		return err
	}
	effort := in.AttemptOptions.ReasoningEffort
	if err := checkInventoryEffort(phase, policy.Inventory, in.Backend, in.Model, effort); err != nil {
		return err
	}
	// The deployment fact about the target. A confirmed absence is refused
	// before the provider open; an unknown state is served and counted,
	// unless the operator selected the strict policy.
	return g.cfg.factualRefusal(phase, in.Installation, in.EffortSupport, func(event, field string) {
		g.observe(ctx, event, phase, field)
	})
}

// CheckNegotiatedDowngrade converts the runtime's negotiated effort strip into
// an explicit refusal for the phases this guard governs.
//
// The refusal is feature-scoped: a call that names no phase of this feature
// (ordinary traffic, another auxiliary role), a negotiation that strips
// another capability, or a call that carries no requested effort is left to
// the runtime's existing behaviour. A governed call is refused only when the
// effort being stripped is the effort the call will serve — a strip of an
// effort the route parameters replace does not change the served call and is
// left to the existing pre-open check. The refusal happens before the
// downgrade is applied and before any provider I/O; the bounded event is the
// count, and the bounded error kind is the classification.
func (g attemptGuard) CheckNegotiatedDowngrade(ctx context.Context, in NegotiatedDowngradeInput) error {
	if !g.cfg.ModelsDeclared {
		return nil
	}
	phase, ok := phaseForRole(in.Role)
	if !ok {
		return nil
	}
	if _, ok := g.cfg.Models.For(phase); !ok {
		return nil
	}
	if in.ReasoningEffort == "" {
		return nil
	}
	if in.ReasoningEffort != in.ExpectedReasoningEffort {
		return nil
	}
	if !slices.Contains(in.Downgraded, aipapi.CapabilityReasoning) {
		return nil
	}
	if g.metrics != nil {
		g.metrics.Inc(EventModelPolicyRefusedNegotiatedStrip)
	}
	if g.log != nil {
		g.log.WarnContext(ctx, "proxy-memory model policy refused a negotiated effort strip",
			"event", EventModelPolicyRefusedNegotiatedStrip,
			"phase", string(phase),
			"field", FieldReasoningEffort,
		)
	}
	return fmt.Errorf("%w: %w", ErrNegotiatedEffortStrip, &PolicyError{
		Phase:  phase,
		Field:  FieldReasoningEffort,
		Reason: "the runtime negotiated the reasoning effort away",
	})
}

// factualRefusal applies the deployment's factual policy to one candidate.
//
// A known absent installation and a known unsupported effort value are refused
// explicitly. An unknown fact is refused only under the operator's strict
// policy; otherwise it is served, counted and logged through observe. A known
// present installation with a known supported effort always passes, also under
// the strict policy (design §3; Requirement 1.4).
//
// The decision lives here so the write path and the pre-open guard cannot
// drift: the write path applies it to the compiled actual target and the
// guard applies it to the facts the provider open observes.
func (c Config) factualRefusal(phase Phase, installation InstallationState, support EffortSupportState, observe func(event, field string)) error {
	switch installation {
	case InstallationAbsent:
		return &PolicyError{
			Phase:  phase,
			Field:  FieldModel,
			Reason: "the actual target is not installed for the backend",
		}
	case InstallationUnknown:
		if c.Models.Strict {
			return &PolicyError{
				Phase:  phase,
				Field:  FieldModel,
				Reason: "the installation of the actual target is not proven",
			}
		}
		observe(EventModelPolicyUnverifiedInstallation, FieldModel)
	}
	// The provider's own answer for the effort value. An unsupported value is
	// refused before the provider open; an unknown answer is served and
	// counted, unless the operator selected the strict policy.
	switch support {
	case EffortSupportUnsupported:
		return &PolicyError{
			Phase:  phase,
			Field:  FieldReasoningEffort,
			Reason: "the actual target does not support the reasoning effort",
		}
	case EffortSupportUnknown:
		if c.Models.Strict {
			return &PolicyError{
				Phase:  phase,
				Field:  FieldReasoningEffort,
				Reason: "the support of the reasoning effort is not proven",
			}
		}
		observe(EventModelPolicyUnverifiedEffort, FieldReasoningEffort)
	}
	return nil
}

// observe records one unverified fact: a bounded counter and a bounded log
// line that names the phase and the field and never a value.
func (g attemptGuard) observe(ctx context.Context, event string, phase Phase, field string) {
	if g.metrics != nil {
		g.metrics.Inc(event)
	}
	if g.log != nil {
		g.log.WarnContext(ctx, "proxy-memory model policy served an unverified fact",
			"event", event,
			"phase", string(phase),
			"field", field,
		)
	}
}
