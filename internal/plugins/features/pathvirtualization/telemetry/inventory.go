package telemetry

// This file is requirement 7.8's diagnostics inventory: the bounded projection of a
// compiled configuration, and the only value in this package that describes CONFIGURED
// state rather than TRAFFIC.
//
// The design rule is that the inventory reports the SHAPE of a configuration and never
// its content. Enablement, mode, whether schema-assisted inference is bound, the declared
// mandatory bound, and two counts are the whole surface. Every one of them is either a
// boolean, a closed-enum label, or an integer.
//
// The reason is not squeamishness about log formatting. config.Resolved deliberately
// carries no operator text at all - a JSON Pointer, a tool name, and a vocabulary key are
// all operator input and all potentially sensitive, so none of them ever reaches this
// package to leak in the first place. Reading config.Shape rather than re-reading the
// operator's subtree is what keeps that property: there is no handle here to a
// configuration that could be walked, so a later edit cannot turn the inventory into a
// second copy of the operator's file.
//
// The counts are reported rather than the things counted, and that distinction is the
// whole mechanism behind requirement 7.7 for this half. "One declared profile" is a
// fixed low-cardinality fact. "The declared profile names acme_internal_read_secrets"
// is not, and reporting it would tell a reader both that this feature is active and
// which private internal tool it was pointed at.

import (
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/config"
)

// Inventory is the bounded, content-free diagnostics projection of one compiled
// configuration.
//
// Every field is a boolean, a closed-enum label, or an integer, so the whole value is
// safe to serialize into a diagnostics endpoint, an operator's support bundle, or a
// metric attribute set (requirements.md 7.7, 7.8).
//
// There is deliberately no field for a tool name, a JSON Pointer, a vocabulary key, a
// project root, a workspace tag, or an alias. Each of those is either operator input or
// derived from one, and none of them is needed to answer the two questions requirement
// 7.8 actually asks: is the feature on, and how big is what it was pointed at.
type Inventory struct {
	// Enabled reports whether this generation performs path virtualization at all. A
	// disabled generation still produces an inventory, so a diagnostics surface can
	// answer "is it on?" with "no" rather than with silence (requirements.md 7.1, 7.8).
	Enabled bool `json:"enabled"`
	// Mode is the resolved rollout mode's bounded label: `audit`, `rewrite`, or
	// `unknown` for a value outside the closed enum. It is the operator-facing spelling
	// requirement 7.2 defines, so a reader does not have to parse anything.
	//
	// A DISABLED generation reports the engine's zero mode, because a disabled
	// configuration resolves no mode at all - which is why Enabled is the field a reader
	// must consult first and why this one is meaningless on its own while Enabled is
	// false.
	Mode string `json:"mode"`
	// SchemaInference reports whether the optional schema-assisted argument selection
	// step was bound into the published policy.
	SchemaInference bool `json:"schema_inference"`
	// MandatoryMaxArgsBytes is the declared completeness requirement the expansion
	// finalizer published, already validated against the shared range. It is a
	// configuration value rather than a measurement, which is why it belongs on the
	// inventory and not on the counters.
	//
	// It is worth reading together with the traffic counter
	// [InboundCounters.MandatoryOverflows]: the first is what the generation declared and
	// the second is what actually reached the pass, and a deployment where they disagree
	// has a call the assembler would have refused that the pass nonetheless saw.
	MandatoryMaxArgsBytes int `json:"mandatory_max_args_bytes"`
	// ProfileCount is how many exact-name operator profiles this generation published. A
	// count, never the profiles themselves.
	ProfileCount int `json:"profile_count"`
	// PathKeyCount is how many vocabulary entries the schema-assisted inference step was
	// compiled with. A count, never the keys.
	PathKeyCount int `json:"path_key_count"`
}

// newInventory projects one compiled configuration shape onto the inventory.
//
// It reads config.Shape and nothing else, which is what makes the content-freedom a
// property of the TYPE rather than of this function's discipline: there is no handle to
// the operator's subtree here to walk. The mode is projected through its own String
// method, so a value outside the closed enum renders as that enum's bounded "unknown"
// rather than as an ordinal or as operator text.
func newInventory(shape config.Shape) Inventory {
	return Inventory{
		Enabled:               shape.Enabled,
		Mode:                  shape.Mode.String(),
		SchemaInference:       shape.SchemaInference,
		MandatoryMaxArgsBytes: shape.MandatoryMaxArgsBytes,
		ProfileCount:          shape.ProfileCount,
		PathKeyCount:          shape.PathKeyCount,
	}
}
