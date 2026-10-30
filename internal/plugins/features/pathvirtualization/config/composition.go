package config

// This file owns the ONE cross-feature constraint this feature has, and it is a
// constraint about composition rather than about a value inside this feature's
// own subtree.
//
// The gap it closes was characterized by the tool-call-repair composition review
// and is requirements.md 8.4's first clause: mandatory path expansion shall
// receive VALID COMPLETED JSON. That holds only while tool-call repair's
// effective finalizer order sorts strictly before this feature's expansion pass.
// The mechanism that makes it hold is ordinary numeric ordering - the runtime
// materializes the finalizer chain with toolcall.MaterializeSorted and then
// iterates it, and no core rule reorders finalizers - and the expansion pass
// declares its order above repair's shipped default for exactly that reason.
//
// Ordering alone is not sufficient, and the reason is an operator key.
// `tool_call_repair.order` is a real configuration value, its normalization
// rejects only negatives, and there is no upper bound and no cross-feature
// constraint anywhere in the tree. So a generation can configure repair to sort
// AT OR AFTER the expansion pass, and in that generation the expansion pass sees
// the RAW MALFORMED document for every malformed model tool call. Declaring a
// higher expansion order cannot fix it, because repair's order is unbounded
// above: there is no number this feature could declare that wins against every
// operator value.
//
// The expansion pass is not a passive bystander in that configuration either. It
// refuses closed on an unparseable alias-bearing document precisely because no
// selector layer can run there, so it degrades from "expand" to "refuse" for
// those calls. That is the safe direction, which is why the ordering gap is a
// correctness and availability problem rather than an alias leak. It is still a
// requirement 8.4 violation, and it is still refused at generation compilation
// rather than shipped.
//
// WHY THIS LIVES HERE AND NOT IN THE SUBTREE. The condition spans two
// registrations: this feature's subtree has no key for repair's order, and
// repair's subtree has no key for this feature's existence. A subtree-scoped
// decoder therefore CANNOT see it, which is why the guard takes the whole
// registration list. There is an established precedent for that shape inside a
// feature: secrets-guard exports a composition entry point that validates the
// full registration list precisely because some of its rules are cross-feature.
//
// WHY IT IS NOT A CORE RULE. A core-side "mandatory finalizers run last" rule was
// already tried for this specification and rejected on review: it has no present
// value (no shipped finalizer declares the capability beyond this one), it
// overrides the authoritative numeric Order contract the SDK publishes, and it
// closes nothing at the assembler chokepoint. The rule therefore stays in the
// feature that owns the declared order, and the composition root calls it.
//
// THE COMPARISON, stated once. Refuse publication when this feature is enabled
// AND a tool-call-repair feature registration is enabled AND the repair
// registration's EFFECTIVE finalizer order is greater than or equal to
// expansion.FinalizerOrder. "Effective" means the operator's `order` when the key
// is present and repair's shipped default otherwise, and it is read through
// repair's own decode and its own accessor rather than re-derived, so this guard
// cannot drift from the semantics it defends.
//
// Fail-closed decisions follow the same rule as the rest of this package. An
// unreadable repair subtree, an unreadable own subtree, and more than one enabled
// registration of either feature are all REFUSALS rather than assumptions, because
// an effective order this build cannot determine cannot be proven to sort before
// the expansion pass.

import (
	"errors"
	"strings"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/expansion"
	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// ValidateGenerationComposition refuses a generation in which this feature's
// mandatory expansion could not receive valid completed JSON, and returns nil for
// every generation where that clause holds.
//
// It is the cross-feature half of requirements.md 7.5's "ambiguous configuration
// shall fail generation compilation", and it is the only exported symbol in this
// feature that needs to see more than its own subtree.
//
// A composition root that compiles a candidate generation calls it over the whole
// enabled registration list. The four shapes it refuses are:
//
//   - this feature is enabled, tool-call repair is enabled, and repair's effective
//     finalizer order is at or above expansion.FinalizerOrder
//     (ReasonRepairOrder);
//   - more than one enabled registration claims either guarded feature, so the
//     effective order of the composition is not a single fact
//     (ReasonAmbiguousRegistration);
//   - the repair subtree cannot be decoded, so its effective order is unknown
//     (ReasonRepairConfigUnreadable);
//   - this feature's own subtree cannot be decoded, which exists so the guard
//     cannot be used to route around this package's own validation
//     (ReasonSelfConfigUnreadable). The decoder's own verdict is JOINED onto that
//     reason rather than replaced by it: the guard runs before the factory, so a
//     discarded cause is a verdict produced nowhere, and the precise reason - an
//     unknown key, a missing mode, an unusable bound - is what an operator needs
//     from exactly the configuration that cannot be published. Both sides are
//     bounded: this package's *Error renders as a literal reason and a literal
//     location, so the join satisfies requirement 7.7.
//
// Every other generation returns nil, including the stock one: this feature
// disabled, repair disabled, no repair registration at all, and the shipped
// repair default, which sorts strictly below the expansion pass's declared order.
//
// It performs no I/O, holds no state, and is safe to call concurrently and more
// than once for the same generation.
func ValidateGenerationComposition(registrations []aipsdk.Registration) error {
	self, err := enabledRegistrations(registrations, ID)
	if err != nil {
		return err
	}
	if len(self) == 0 {
		// Nothing publishes an expansion pass, so there is nothing for a repair
		// order to be composed against.
		return nil
	}
	if len(self) > 1 {
		return reject(ReasonAmbiguousRegistration)
	}
	// The full compile runs rather than a shape-only decode, because the guard's
	// question is "does this generation publish an expansion pass", and a subtree
	// that decodes but refuses to compile publishes nothing. Reading the enabled
	// flag off a shape-only decode would answer yes for a configuration this
	// package itself cannot publish.
	resolved, cfgErr := Decode(self[0].Config.Node)
	if cfgErr != nil {
		return errors.Join(reject(ReasonSelfConfigUnreadable), cfgErr)
	}
	if !resolved.Enabled {
		return nil
	}

	repairs, err := enabledRegistrations(registrations, toolcallrepair.ID)
	if err != nil {
		return err
	}
	if len(repairs) == 0 {
		return nil
	}
	if len(repairs) > 1 {
		return reject(ReasonAmbiguousRegistration)
	}
	// Repair's own decode and its own accessor are the authority for its effective
	// order. Reading them rather than re-implementing its normalization is what
	// keeps this guard correct if repair's defaults or its `order` semantics ever
	// change, and it is why this guard needs no knowledge of repair's
	// configuration surface beyond the fact that it exists.
	repairCfg, repairErr := toolcallrepair.DecodeConfig(repairs[0].Config.Node)
	if repairErr != nil {
		// Repair errors can contain operator values; retain only the bounded verdict.
		return reject(ReasonRepairConfigUnreadable)
	}
	if repairCfg.FinalizerOrder() >= expansion.FinalizerOrder {
		return reject(ReasonRepairOrder)
	}
	return nil
}

// enabledRegistrations returns the enabled FEATURE registrations that a bundled
// factory would resolve to id.
//
// The match is on the registry factory key rather than the instance id, because
// that is what the composition root looks a factory up by, and it tolerates
// surrounding space and letter case in the same way every other feature surface
// in this tree does. A non-feature registration is skipped even when its id
// matches, because a backend or frontend row carrying the same text is not a
// finalizer participant.
func enabledRegistrations(registrations []aipsdk.Registration, id string) ([]aipsdk.Registration, error) {
	var matches []aipsdk.Registration
	for _, registration := range registrations {
		if registration.Kind != aipsdk.PluginKindFeature || !registration.Enabled {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(registration.RegistryFactoryKey()), id) {
			continue
		}
		matches = append(matches, registration)
		if len(matches) > 1 {
			// Refuse as soon as a second claim appears rather than collecting them
			// all: the caller needs the fact that the count is not one, and nothing
			// else about the extra rows.
			return nil, reject(ReasonAmbiguousRegistration)
		}
	}
	return matches, nil
}
