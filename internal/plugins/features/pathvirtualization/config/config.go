package config

// This file is the operator configuration surface of the B-leg path
// virtualization feature, and it owns the fail-closed half of requirements.md
// 7.5: an invalid selector, a duplicate or conflicting exact profile, an
// unsupported bound, or an ambiguous configuration is refused as a WHOLE at
// generation compilation, so a request can never resolve a selector, a bound, or
// a mode that was not validated.
//
// Four decisions shape everything here, and each of them is a requirement rather
// than a preference:
//
//  1. DISABLED BY DEFAULT, AND NOTHING CONSTRUCTS. An absent subtree, an empty
//     document, an explicit null, an empty mapping, and an `enabled: false`
//     mapping all decode to an inert resolution with no resolver, so a stock
//     deployment builds no attempt transform, no request-part hook, and no
//     expansion finalizer. Validation still runs, because an unusable value in a
//     parked configuration is the kind of thing that becomes an unpublishable
//     generation the moment somebody flips the switch.
//
//  2. NO ALIAS-MARKER OR VERSION OVERRIDE. The reserved alias namespace, its
//     version, and the workspace-tag encoding are fixed implementation
//     contracts (requirement 7.4), so the decoder reads exactly the six
//     design.md top-level keys and refuses anything else. There is consequently no key that
//     could move the reserved alias namespace, change the workspace-tag encoding,
//     or name a pattern: the surface does not exist to configure, and the marker
//     literal itself appears nowhere in this package's production sources, which
//     its own test proves byte for byte.
//
//  3. EVERY BOUND IS VALIDATED BY ITS SINGLE DEFINITION. The configurable
//     mandatory-expansion range is `toolcall.BufferingSpec.Validate`, the
//     selector and profile rules are `pathvirtualization.CompileToolProfiles` and
//     `CompilePathKeys`, the path-key normalization is `schemainfer.New`, and the
//     exact-name lookup is `pathvirtualization.NewResolver`. This file re-derives
//     none of them; it decides only which of their verdicts becomes which
//     Reason, and it refuses the whole configuration when any of them refuses.
//
//  4. THE MODE IS EXPLICIT AND NEVER A ZERO VALUE. `rewrite.Mode`'s zero value is
//     audit, so a configuration that spelled no mode must not become audit by
//     arithmetic. An enabled subtree therefore REQUIRES the key: an absent mode
//     is the ambiguous configuration requirement 7.5 names, and defaulting it
//     either way is wrong - to audit it would silently measure a rollout the
//     operator asked to mutate, and to rewrite it would mutate without an
//     opt-in. This does not conflict with requirement 7.1, because disabled by
//     default is decided at the enabled gate and a deployment that never enables
//     the feature never reaches this rule.
//
// The mandatory bound is the fifth decision worth naming, because it is the one
// that is easiest to get silently wrong. `mandatory_max_args_bytes` is a
// POINTER, so an absent key is distinguishable from an explicit zero, and an
// explicit zero is refused rather than read as "unset". A completeness
// requirement that silently degrades to the default is not a smaller bound; it
// is a deployment that believes it enforces a limit it is not enforcing.
//
// The compiled result, Resolved, carries no operator text at all: only bounded
// counts and the compiled policy. That is what lets requirements.md 7.8's
// diagnostics inventory report the configuration SHAPE without exposing a
// pointer, a tool name, or anything path-shaped.

import (
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/expansion"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/rewrite"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/schemainfer"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
	"gopkg.in/yaml.v3"
)

// ID is the feature plugin id and the `plugins.features` subtree key this
// configuration is read from.
//
// It is a fixed string rather than something composed from a version, because it
// is a registry lookup key: the composition root resolves a registration's
// factory by it, and every rejection carries it as the literal prefix of its
// message.
const ID = "path_virtualization"

// toolProfilesKey is the subtree key that holds the declared-profile list.
//
// It is a named constant because two places depend on the exact spelling and must
// not drift: the top-level allow-list that admits it, and the node walk that has
// to find it before the decoder runs.
const toolProfilesKey = "tool_profiles"

// configKeys is the closed set of top-level operator keys, exactly the six
// design.md "Configuration and Composition" prescribes. Together with the four
// keys of a declared profile that is the whole V1 configuration surface.
//
// The list is an allow-list rather than a decode-only list, so an unknown key is
// REFUSED instead of ignored. That is the difference between a feature that
// cannot be misconfigured and one that silently runs with less configuration
// than the operator wrote, and it is what makes requirement 7.4's "fixed
// implementation contracts" enforceable rather than aspirational.
var configKeys = []string{
	"enabled",
	"mandatory_max_args_bytes",
	"mode",
	"path_keys",
	"schema_inference",
	toolProfilesKey,
}

// toolProfileKeys is the closed key set of one declared tool profile, again
// exactly the four design.md prescribes.
//
// A profile is the other place a namespace, version, or pattern knob would be
// tempting to add, because a profile is where a tool's whole surface is
// described. Refusing an unknown key here is the same argument as at the top
// level, one level down.
var toolProfileKeys = []string{
	"arg_json_pointers",
	"names",
	"opaque_result_mode",
	"result_json_pointers",
}

// The two configuration spellings of the rollout modes. They are the operator
// text; the resolved value is a rewrite.Mode, and the mapping between the two is
// an exhaustive switch with no default branch that guesses.
const (
	modeAudit   = "audit"
	modeRewrite = "rewrite"
)

// Config is the feature-private typed configuration, exactly as design.md
// prescribes it and with no other field.
//
// The two pointer fields are the ones whose ABSENCE is meaningful.
// `mandatory_max_args_bytes` distinguishes "the operator did not choose" from
// "the operator chose zero", because the second must be refused rather than
// defaulted. `mode` distinguishes "no mode was spelled" from "the mode was
// spelled as an empty string", because the first must be refused for an enabled
// configuration while the second is simply an unknown spelling.
//
// Everything else is a plain value with a safe zero: `enabled` false is the
// required default, `schema_inference` false is the safe direction because
// inference can only widen automatic surface selection, and an absent
// `path_keys` or `tool_profiles` list has an unambiguous meaning.
type Config struct {
	Enabled               bool                `yaml:"enabled"`
	Mode                  *string             `yaml:"mode"`
	SchemaInference       bool                `yaml:"schema_inference"`
	MandatoryMaxArgsBytes *int                `yaml:"mandatory_max_args_bytes"`
	PathKeys              []string            `yaml:"path_keys"`
	ToolProfiles          []ToolProfileConfig `yaml:"tool_profiles"`
}

// ToolProfileConfig is one declared operator tool profile: the exact tool names
// it claims, the JSON Pointers it proves path-bearing on each side of a call,
// and the bounded opaque-result mode it declares.
//
// It is a DISTINCT type from pathvirtualization.ToolProfile rather than a reused
// one, and the separation is load-bearing in both directions. This type carries
// YAML tags because it is operator input; the compile-time type carries none, so
// requirement 7.4's reflection guard over the compile-time shapes stays a
// statement that those shapes have no decode surface at all. Reusing one type
// for both roles would force the tags onto the compile-time value and make that
// guard vacuous.
type ToolProfileConfig struct {
	Names              []string `yaml:"names"`
	ArgJSONPointers    []string `yaml:"arg_json_pointers"`
	ResultJSONPointers []string `yaml:"result_json_pointers"`
	OpaqueResultMode   string   `yaml:"opaque_result_mode"`
}

// Resolved is the compiled, immutable, CONTENT-FREE configuration one generation
// publishes.
//
// It carries bounded counts and the compiled policy, never operator text. A
// JSON Pointer, a tool name, and a vocabulary key are all operator input and all
// potentially sensitive (a tool name can name a private internal tool), so
// none of them travels here - which is what lets requirements.md 7.8's
// diagnostics inventory report the configuration SHAPE while 7.7 keeps names,
// paths, and pointers out of anything observable.
//
// The bounded parts are grouped into Shape so two generations compiled from the
// same subtree can be compared with the ordinary equality operator, without an
// equality helper that would have to decide what a policy pointer means. The
// value as a whole is deliberately NOT compared with `==`, because it holds the
// compiled resolver and two compilations produce two pointers; Shape is the
// comparison, and it is what requirement 7.8's diagnostics inventory reports.
//
// It holds no mutex, no cache, and no pointer back to the configuration it came
// from, so one resolved value is safe to share across every request of a
// generation.
type Resolved struct {
	// Enabled reports whether this generation performs path virtualization at
	// all. A disabled resolution publishes no policy and constructs nothing.
	Enabled bool
	// mode is the resolved rollout mode. It is meaningful only while Enabled: a
	// disabled resolution spells no mode, so it carries the engine's zero value
	// and nothing may read it.
	mode rewrite.Mode
	// profileCount is how many exact-name operator profiles this generation
	// published. A bounded count, never a name.
	profileCount int
	// pathKeyCount is how many vocabulary entries the inference step was
	// compiled with.
	pathKeyCount int
	// SchemaInference reports whether the optional schema-assisted inference step
	// was bound into the published resolver. It is a fixed-count dimension for
	// requirements.md 7.8's diagnostics inventory.
	SchemaInference bool
	// MandatoryMaxArgsBytes is the declared completeness requirement this
	// generation publishes, already validated against the shared range. It is
	// meaningful only while Enabled.
	MandatoryMaxArgsBytes int
	// Resolver is the compiled exact-name profile policy, or nil while disabled.
	// A nil resolver selects nothing for any tool, which is the required answer
	// for an unproved surface (requirements.md 3.5, 3.8).
	Resolver *pathvirtualization.Resolver
}

// Disabled reports whether this generation publishes no path virtualization at
// all. It is the one accessor a composition root needs to decide whether to
// construct anything, so it is named rather than left as a field comparison.
func (r Resolved) Disabled() bool { return !r.Enabled }

// Mode returns the resolved rollout mode.
//
// It is a method rather than a field so the "meaningful only while Enabled"
// rule is stated where the value is read. A caller that reads it on a disabled
// resolution gets the engine's zero value and no policy, which cannot publish
// anything.
func (r Resolved) Mode() rewrite.Mode { return r.mode }

// ProfileCount returns how many exact-name operator profiles this generation
// published. It is a bounded count rather than the profiles themselves, so a
// diagnostics inventory can report the shape without naming a tool.
func (r Resolved) ProfileCount() int { return r.profileCount }

// PathKeyCount returns how many vocabulary entries the inference step was
// compiled with.
func (r Resolved) PathKeyCount() int { return r.pathKeyCount }

// ExpansionPolicy returns the declared completeness requirement this generation
// publishes to the path-expansion finalizer.
//
// It returns a VALUE constructed fresh from the validated bound, so a caller
// cannot reach the resolution's own state through it, and it is the only way the
// bound crosses into expansion.Policy - which deliberately keeps no tags and no
// mode field. See expansion.Policy for why the mode stays a constructor
// argument: a mode field on the policy would reintroduce exactly the silent-audit
// zero value Task 8.1 refused.
func (r Resolved) ExpansionPolicy() expansion.Policy {
	return expansion.Policy{MandatoryMaxArgsBytes: r.MandatoryMaxArgsBytes}
}

// Shape is the bounded, comparable projection of a resolved configuration.
//
// It is every field a diagnostics inventory may report and nothing else: the
// enablement, the mode, whether inference is bound, the declared bound, and two
// counts. requirements.md 7.8 asks for exactly this much - enablement, mode, and
// bounded configuration shape - and 7.7 is what keeps a tool name, a pointer,
// and a vocabulary key out of it.
type Shape struct {
	Enabled               bool
	Mode                  rewrite.Mode
	SchemaInference       bool
	MandatoryMaxArgsBytes int
	ProfileCount          int
	PathKeyCount          int
}

// Shape returns the bounded projection of this resolution.
func (r Resolved) Shape() Shape {
	return Shape{
		Enabled:               r.Enabled,
		Mode:                  r.mode,
		SchemaInference:       r.SchemaInference,
		MandatoryMaxArgsBytes: r.MandatoryMaxArgsBytes,
		ProfileCount:          r.profileCount,
		PathKeyCount:          r.pathKeyCount,
	}
}

// ConfigKeys returns the closed set of accepted top-level configuration keys, in
// the design's order.
//
// It exists so a caller can render the permitted shape without duplicating the
// list, and so a test can pin the surface against an accidental widening.
func ConfigKeys() []string { return append([]string(nil), configKeys...) }

// ToolProfileKeys returns the closed set of accepted declared-profile keys, in
// the design's order.
func ToolProfileKeys() []string { return append([]string(nil), toolProfileKeys...) }

// DecodeConfig decodes one feature-private YAML subtree into Config and rejects
// a subtree whose SHAPE this build does not accept.
//
// It returns the raw typed configuration, matching every sibling feature's
// convention: the caller decides whether to compile it. A caller that wants the
// compiled policy in one step wants Decode.
//
// The node-shape handling follows toolcallrepair exactly, because the composition
// root may hand a factory either the mapping node itself or the document
// wrapping it:
//
//   - no node, an empty document, an explicit null, and an empty string all
//     decode to the zero Config, which is DISABLED - that is requirement 7.1's
//     "disabled by default" arriving from four directions at once;
//   - a mapping is decoded, after every top-level and every profile key has been
//     checked against the closed allow-lists;
//   - anything else is refused, because a scalar or a sequence cannot carry this
//     feature's typed configuration.
//
// Every refusal is a typed *Error whose message is composed only of compile-time
// literals. In particular the decoder's own type error is TRANSLATED rather than
// wrapped: yaml.v3 names the offending value in its message, so wrapping it would
// put operator input into an error an operator will paste into a bug report.
func DecodeConfig(n yaml.Node) (Config, error) {
	root := n
	switch root.Kind {
	case 0:
		return Config{}, nil
	case yaml.DocumentNode:
		if len(root.Content) == 0 {
			return Config{}, nil
		}
		root = *root.Content[0]
	}
	switch root.Kind {
	case 0:
		return Config{}, nil
	case yaml.ScalarNode:
		if root.Tag == "!!null" || root.Value == "" || root.Value == "null" {
			return Config{}, nil
		}
		return Config{}, reject(ReasonNotAMapping)
	case yaml.MappingNode:
		if err := checkKeys(root, configKeys, ReasonUnknownKey); err != nil {
			return Config{}, err
		}
		if err := checkProfileShapes(root); err != nil {
			return Config{}, err
		}
		var cfg Config
		if err := root.Decode(&cfg); err != nil {
			return Config{}, reject(ReasonMalformedValue)
		}
		return cfg, nil
	default:
		return Config{}, reject(ReasonNotAMapping)
	}
}

// Decode decodes AND compiles one feature subtree, and is the entry point a
// registry factory should use.
//
// It is a single call on purpose. Requirement 7.5 says an invalid selector, a
// duplicate or conflicting profile, an unsupported bound, or an ambiguous
// configuration shall fail generation compilation BEFORE PUBLICATION, and the
// only way that property survives a later edit is if there is one call a factory
// cannot forget to make. Splitting decode from compile is useful for a caller
// that wants to inspect the raw subtree, which is why DecodeConfig and Compile
// are both exported, but the factory path goes through here.
//
// On any refusal it returns the ZERO Resolved, so there is no path on which a
// partially validated profile set, a half-resolved vocabulary, or a clamped
// bound reaches a request.
func Decode(n yaml.Node) (Resolved, error) {
	cfg, err := DecodeConfig(n)
	if err != nil {
		return Resolved{}, err
	}
	return cfg.Compile()
}

// Compile validates every value in a decoded configuration and publishes the
// immutable policy, or refuses the WHOLE configuration.
//
// The order of the checks is the order in which a mistake becomes cheapest to
// make and most expensive to keep, and each step is a refusal rather than a
// repair:
//
//  1. the mode, because an enabled configuration without one is ambiguous and
//     nothing else can be decided until it is answered;
//  2. the mandatory bound, validated by the shared SDK validator so the
//     configurable range has exactly one definition;
//  3. the declared profiles, compiled by the shared compiler so the exact-name,
//     pointer, count, and opaque-mode rules keep one implementation;
//  4. the vocabulary, compiled by the shared inference compiler so normalization
//     collisions are caught alongside the count and emptiness rules;
//  5. and only then, for an ENABLED configuration, the resolver binding, which is
//     validated again at BIND time because a caller may assemble compiled layers
//     itself.
//
// A DISABLED configuration is still validated in full and then published inert.
// That is the deliberate half of the disabled-by-default rule that surprises
// people: nothing constructs, so the cost is nil, but an unusable value in a
// parked configuration is a latent refusal rather than a latent surprise.
//
// The gate is therefore the LAST statement that can refuse, not the first. Every
// step above it - the mode, the bound, the declared profiles, the shipped
// built-in layer, and the vocabulary - runs for a parked subtree too, so a typo in
// a subtree nobody has switched on yet is a load-time refusal rather than a
// surprise on the day the switch is flipped. Requirement 7.5 has no enabled-only
// carve-out, and the one rule that IS enabled-only is the resolver binding below:
// a caller may assemble compiled layers itself, so that combination is validated
// at bind time as well.
func (c Config) Compile() (Resolved, error) {
	mode, err := c.resolveMode()
	if err != nil {
		return Resolved{}, err
	}
	bound, err := c.resolveMandatoryBound()
	if err != nil {
		return Resolved{}, err
	}
	declared, err := c.declaredProfiles()
	if err != nil {
		return Resolved{}, err
	}
	profiles, reject := pathvirtualization.CompileToolProfiles(declared)
	if reject != pathvirtualization.SelectorRejectNone {
		return Resolved{}, newSelectorError(ReasonSelector, reject)
	}
	// The built-in layer is compiled here, at generation compilation, for the same
	// reason the operator layer is: a shipped table that is only parsed on first
	// use would make the built-in policy's validity depend on request traffic.
	// It is compiled ABOVE the enabled gate on purpose, so a parked generation
	// still proves the shipped table is valid at load time rather than at first
	// traffic.
	builtin, reject := pathvirtualization.CompileToolProfiles(pathvirtualization.BuiltinToolProfiles())
	if reject != pathvirtualization.SelectorRejectNone {
		return Resolved{}, newSelectorError(ReasonSelector, reject)
	}
	keys := c.resolvePathKeys()
	inferrer, reject := schemainfer.New(keys)
	if reject != pathvirtualization.SelectorRejectNone {
		return Resolved{}, newSelectorError(ReasonPathKeySelector, reject)
	}

	if !c.Enabled {
		// Validation is complete and nothing is published: no resolver, no bound,
		// no counts. A factory therefore has nothing to construct from, which is
		// what makes requirement 7.1's "nothing constructs" a property of the
		// published value rather than of the caller's discipline.
		return Resolved{}, nil
	}

	// The inference step is bound only when an operator asked for it. A nil
	// interface is passed when it is off rather than a typed nil pointer, so the
	// resolver's own "is inference on" check stays honest.
	var inference pathvirtualization.ArgumentInference
	if c.SchemaInference {
		inference = inferrer
	}
	resolver, reject := pathvirtualization.NewResolver(profiles, builtin, inference)
	if reject != pathvirtualization.SelectorRejectNone {
		return Resolved{}, newSelectorError(ReasonSelector, reject)
	}
	return Resolved{
		Enabled:               true,
		mode:                  mode,
		SchemaInference:       c.SchemaInference,
		MandatoryMaxArgsBytes: bound,
		profileCount:          len(profiles),
		pathKeyCount:          len(keys),
		Resolver:              resolver,
	}, nil
}

// resolveMode answers the closed audit|rewrite enum.
//
// There is no default branch that guesses, and there is no trimming or case
// folding. An operator who wrote "Rewrite" meant rewrite, and reading that as
// audit would silently measure instead of mutate while the operator believed the
// opposite - the one failure mode requirements.md 7.2 and 7.3 exist to prevent.
func (c Config) resolveMode() (rewrite.Mode, error) {
	if c.Mode == nil {
		if c.Enabled {
			return rewrite.ModeAudit, reject(ReasonMissingMode)
		}
		// A disabled configuration spells no mode, so it resolves the engine's
		// zero value. Nothing is constructed from it.
		return rewrite.ModeAudit, nil
	}
	switch *c.Mode {
	case modeAudit:
		return rewrite.ModeAudit, nil
	case modeRewrite:
		return rewrite.ModeRewrite, nil
	default:
		return rewrite.ModeAudit, reject(ReasonUnknownMode)
	}
}

// resolveMandatoryBound answers the declared completeness requirement.
//
// The range is validated by toolcall.BufferingSpec.Validate and NOT re-derived
// here: the floor, the ceiling, and the default have exactly one definition, and
// this package deliberately contains neither endpoint as a literal so a second
// copy cannot drift out of step with the SDK.
//
// OverflowReject is supplied to the validator unconditionally, including for the
// absent-key case, because that is the only policy this feature ever declares and
// a validation call that omitted it would accept an explicit zero as "declares
// nothing" - which is precisely the silent degradation the pointer exists to
// prevent.
func (c Config) resolveMandatoryBound() (int, error) {
	bound := toolcall.DefaultMandatoryMaxArgsBytes
	if c.MandatoryMaxArgsBytes != nil {
		bound = *c.MandatoryMaxArgsBytes
	}
	spec := toolcall.BufferingSpec{MaxArgsBytes: bound, Overflow: toolcall.OverflowReject}
	if err := spec.Validate(); err != nil {
		return 0, reject(ReasonMandatoryBound)
	}
	return bound, nil
}

// resolvePathKeys answers the schema path-key vocabulary.
//
// An ABSENT key selects the shipped V1 vocabulary, because requirement 3.3 makes
// that vocabulary the default and an operator only reaches for this key to widen
// it. An EXPLICITLY EMPTY key is a legitimate "infer nothing" and is not an
// error; it must not silently fall back to the defaults, because an operator who
// empties a vocabulary is narrowing the feature's reach and falling back would
// widen it back. Presence is read from the decoded slice's own nil-ness, which
// yaml.v3 keeps distinct from an empty sequence.
func (c Config) resolvePathKeys() []string {
	if c.PathKeys == nil {
		return schemainfer.DefaultPathKeys()
	}
	return c.PathKeys
}

// declaredProfiles translates the decoded profiles into the compile-time shape.
//
// The translation is the only place the two representations meet, and it is
// deliberately narrow: names and pointers cross as text, because that is what
// the shared compiler validates; the opaque mode crosses as a parsed value,
// because that is the closed enum the shared compiler re-validates.
//
// An absent mode is the disabled default, which is the whole reason the enum is
// closed: nothing turns opaque rewriting on without naming a mode, and a spelling
// outside the enum is refused rather than read as off.
func (c Config) declaredProfiles() ([]pathvirtualization.ToolProfile, error) {
	if len(c.ToolProfiles) == 0 {
		return nil, nil
	}
	declared := make([]pathvirtualization.ToolProfile, 0, len(c.ToolProfiles))
	for _, profile := range c.ToolProfiles {
		mode := pathvirtualization.OpaqueResultModeNone
		if profile.OpaqueResultMode != "" {
			parsed, ok := pathvirtualization.ParseOpaqueResultMode(profile.OpaqueResultMode)
			if !ok {
				return nil, newSelectorError(ReasonSelector, pathvirtualization.SelectorRejectOpaqueMode)
			}
			mode = parsed
		}
		declared = append(declared, pathvirtualization.ToolProfile{
			Names:              profile.Names,
			ArgPointers:        profile.ArgJSONPointers,
			ResultJSONPointers: profile.ResultJSONPointers,
			OpaqueResultMode:   mode,
		})
	}
	return declared, nil
}

// checkKeys refuses any member of a mapping that is outside a closed key set.
//
// The allow-list is what makes requirement 7.4 enforceable: the decoder reads
// exactly these keys, so there is no spelling that could reach the reserved
// alias namespace, its version, the workspace-tag encoding, or a pattern.
// Refusing is also the only honest answer for a key this build does not know:
// ignoring it would run a configuration that differs from the one the operator
// wrote.
//
// It takes a REASON and not a location, because the reason already names the
// mapping whose key set was violated: the top-level allow-list passes
// ReasonUnknownKey (the subtree root) and the per-profile allow-list passes
// ReasonUnknownProfileKey (the profile list), and both locations follow from
// ReasonLocation rather than from this call site.
func checkKeys(node yaml.Node, allowed []string, reason Reason) error {
	permitted := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		permitted[key] = struct{}{}
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if _, ok := permitted[key]; !ok {
			return reject(reason)
		}
	}
	return nil
}

// checkProfileShapes refuses an unknown key inside a declared profile, or a
// declared profile that is not a mapping.
//
// It runs on the NODE rather than after decoding so the refusal is about the
// shape, which keeps the location literal and keeps a scalar profile (a string
// where a mapping was expected) from decoding into a zero profile that would
// then be refused again with a less specific reason. The refusals here use
// ReasonMalformedProfile rather than ReasonMalformedValue because the offending
// value is inside the declared-profile list, and saying so is the whole reason
// that reason exists.
func checkProfileShapes(root yaml.Node) error {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != toolProfilesKey {
			continue
		}
		profiles := root.Content[i+1]
		if profiles.Kind == yaml.DocumentNode {
			if len(profiles.Content) == 0 {
				continue
			}
			profiles = profiles.Content[0]
		}
		switch profiles.Kind {
		case 0:
			continue
		case yaml.ScalarNode:
			if profiles.Tag == "!!null" || profiles.Value == "" || profiles.Value == "null" {
				continue
			}
			return reject(ReasonMalformedProfile)
		case yaml.SequenceNode:
			for _, entry := range profiles.Content {
				profile := entry
				if profile.Kind == yaml.DocumentNode {
					if len(profile.Content) == 0 {
						continue
					}
					profile = profile.Content[0]
				}
				if profile.Kind == yaml.ScalarNode &&
					(profile.Tag == "!!null" || profile.Value == "" || profile.Value == "null") {
					continue
				}
				if profile.Kind != yaml.MappingNode {
					return reject(ReasonMalformedProfile)
				}
				if err := checkKeys(*profile, toolProfileKeys, ReasonUnknownProfileKey); err != nil {
					return err
				}
			}
		default:
			return reject(ReasonMalformedProfile)
		}
	}
	return nil
}
