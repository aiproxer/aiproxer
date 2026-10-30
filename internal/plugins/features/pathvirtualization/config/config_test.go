package config_test

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/config"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/expansion"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/rewrite"
	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
	"gopkg.in/yaml.v3"
)

// featureID is the plugins.features subtree key design.md "Configuration and
// Composition" prescribes. Every expected error carries it, so pinning it here
// keeps the whole assertion vocabulary readable.
const featureID = "path_virtualization"

// reservedMarker is the fixed V1 reserved alias namespace. It appears ONLY in
// this test file: no production source of the config package may contain it,
// which is what makes "no alias-marker override" structural rather than a
// promise. See TestConfigSurfaceExposesNoAliasMarkerOrVersionOverride.
const reservedMarker = ".__aip_v1__"

// mustYAML parses an operator configuration subtree the way the composition root
// hands one to a feature factory: as a YAML document node.
func mustYAML(t *testing.T, body string) yaml.Node {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(body), &node); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	return node
}

// mappingYAML parses a fixture and returns the MAPPING node, unwrapping the
// document. Sibling features accept both shapes, so both are exercised.
func mappingYAML(t *testing.T, body string) yaml.Node {
	t.Helper()
	node := mustYAML(t, body)
	if node.Kind == yaml.DocumentNode && len(node.Content) == 1 {
		return *node.Content[0]
	}
	return node
}

// decodeOK decodes and compiles a fixture, failing the test on any error.
func decodeOK(t *testing.T, body string) config.Resolved {
	t.Helper()
	resolved, err := config.Decode(mustYAML(t, body))
	if err != nil {
		t.Fatalf("Decode(%s): unexpected error %v", body, err)
	}
	return resolved
}

// decodeReject decodes and compiles a fixture that must be refused, and returns
// the typed rejection so a caller can assert its classification.
func decodeReject(t *testing.T, body string) *config.Error {
	t.Helper()
	resolved, err := config.Decode(mustYAML(t, body))
	if err == nil {
		t.Fatalf("Decode(%s) published %+v; want generation-compilation refusal", body, resolved)
	}
	if !resolved.Disabled() {
		t.Fatalf("Decode(%s) returned an enabled resolution alongside an error: %+v", body, resolved)
	}
	var typed *config.Error
	if !errors.As(err, &typed) {
		t.Fatalf("Decode(%s) error %v is not a *config.Error", body, err)
	}
	if typed.Reason() == config.ReasonNone {
		t.Fatalf("Decode(%s) rejection carries no reason", body)
	}
	return typed
}

// enabledYAML renders an enabled subtree with extra keys appended to the top
// level, so a test can vary one key without restating the whole mapping.
func enabledYAML(extra ...string) string {
	parts := []string{"enabled: true", "mode: audit"}
	parts = append(parts, extra...)
	return strings.Join(parts, "\n")
}

// ---------------------------------------------------------------------------
// Requirement 7.1: disabled by default, and nothing constructs.
// ---------------------------------------------------------------------------

// TestAbsentOrEmptySubtreeIsDisabledAndConstructsNothing is requirement 7.1 in
// one assertion per shape the composition root can hand a feature factory: no
// node at all, an empty document, an explicit null, an empty mapping, and an
// explicitly disabled mapping. For every one of them the published resolution
// must be inert, so a stock deployment constructs no attempt transform, no
// request-part hook, no expansion finalizer, and no resolver.
func TestAbsentOrEmptySubtreeIsDisabledAndConstructsNothing(t *testing.T) {
	t.Parallel()

	shapes := map[string]yaml.Node{
		"absent":         {},
		"empty_document": {Kind: yaml.DocumentNode},
		"null_scalar":    {Kind: yaml.ScalarNode, Tag: "!!null"},
		"empty_value":    {Kind: yaml.ScalarNode, Tag: "!!str", Value: ""},
		"spelled_null":   {Kind: yaml.ScalarNode, Tag: "!!str", Value: "null"},
		"empty_mapping":  mustYAML(t, "{}"),
		"explicitly_off": mustYAML(t, "enabled: false\n"),
		"off_with_rest":  mustYAML(t, "enabled: false\nmode: rewrite\nschema_inference: true\n"),
		"off_with_profiles": mustYAML(t,
			"enabled: false\ntool_profiles:\n  - names: [\"read_file\"]\n    arg_json_pointers: [\"/file_path\"]\n"),
	}
	names := make([]string, 0, len(shapes))
	for name := range shapes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cfg, err := config.DecodeConfig(shapes[name])
			if err != nil {
				t.Fatalf("DecodeConfig: unexpected error %v", err)
			}
			if cfg.Enabled {
				t.Fatalf("shape %q decoded as enabled; the feature must be disabled by default", name)
			}
			resolved, err := config.Decode(shapes[name])
			if err != nil {
				t.Fatalf("Decode: unexpected error %v", err)
			}
			if resolved.Enabled {
				t.Fatalf("shape %q compiled as enabled", name)
			}
			// Nothing constructs: an inert resolution publishes no policy at all, so
			// no request can resolve a selector and no factory has an argument to
			// build anything from.
			if resolved.Resolver != nil {
				t.Fatalf("shape %q published a resolver while disabled", name)
			}
			if resolved.MandatoryMaxArgsBytes != 0 {
				t.Fatalf("shape %q resolved a mandatory bound while disabled: %d", name, resolved.MandatoryMaxArgsBytes)
			}
			if resolved.ProfileCount() != 0 || resolved.PathKeyCount() != 0 {
				t.Fatalf("shape %q published a bounded configuration shape while disabled", name)
			}
			if got := resolved.Mode(); got != rewrite.ModeAudit {
				t.Fatalf("shape %q carries mode %v; a disabled resolution spells no mode", name, got)
			}
		})
	}
}

// TestAnEnabledRegistrationWithAnAbsentSubtreeStaysInert pins the other half of
// requirement 7.1's "enabled through feature-owned typed configuration": the
// registration being enabled is not what turns the feature on. Only the
// feature's own subtree can, so an operator who adds the row without the
// configuration gets an inert feature rather than a defaulted one.
//
// The composition guard is where the distinction is observable: it is handed an
// enabled registration carrying no subtree at all, resolves the feature to
// disabled, and composes the generation even though the repair order present
// would be refused for an actually-enabled feature.
func TestAnEnabledRegistrationWithAnAbsentSubtreeStaysInert(t *testing.T) {
	t.Parallel()

	regs := []aipsdk.Registration{
		{
			ID:          featureID,
			FactoryKind: featureID,
			Kind:        aipsdk.PluginKindFeature,
			Enabled:     true,
		},
		featureRegistration(t, toolcallrepair.ID, true, "order: 1000\n"),
	}
	if err := config.ValidateGenerationComposition(regs); err != nil {
		t.Fatalf("an enabled registration with an absent subtree must publish nothing, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Requirement 7.2: a strict audit|rewrite enum with no implicit default.
// ---------------------------------------------------------------------------

// TestAnEnabledConfigurationRequiresAnExplicitMode is the ruling this task makes
// on an ABSENT mode, stated as behavior. An enabled subtree that spells no mode
// is refused rather than defaulted to audit, because the two modes are not
// equivalent: audit measures and rewrite mutates. Defaulting to audit would
// silently disable the rollout an operator asked for (they would measure zero
// savings forever), defaulting to rewrite would mutate without an opt-in, and
// requirement 7.5 names exactly this "ambiguous configuration".
//
// It does not conflict with requirement 7.1: disabled by default is decided at
// the enabled gate, so a deployment that never enables the feature never
// reaches this rule at all.
func TestAnEnabledConfigurationRequiresAnExplicitMode(t *testing.T) {
	t.Parallel()

	for _, body := range []string{
		"enabled: true\n",
		"enabled: true\nschema_inference: true\n",
		"enabled: true\nmandatory_max_args_bytes: 1048576\n",
		"enabled: true\nmode:\n",
		"enabled: true\nmode: null\n",
	} {
		rejection := decodeReject(t, body)
		if rejection.Reason() != config.ReasonMissingMode {
			t.Fatalf("Decode(%q) reason = %s, want %s", body, rejection.Reason(), config.ReasonMissingMode)
		}
		if rejection.Field() != featureID+".mode" {
			t.Fatalf("Decode(%q) field = %q, want %q", body, rejection.Field(), featureID+".mode")
		}
	}
}

// TestTheModeEnumIsStrict pins that only the two exact spellings are accepted,
// and that every near miss is refused rather than read as audit. In particular
// no trimming, no case folding, and no empty string: an operator spelling
// "Rewrite" meant rewrite, and reading that as audit would silently measure
// instead of mutate while the operator believes the opposite.
func TestTheModeEnumIsStrict(t *testing.T) {
	t.Parallel()

	for _, body := range []string{
		"enabled: true\nmode: Rewrite\n",
		"enabled: true\nmode: REWRITE\n",
		"enabled: true\nmode: Audit\n",
		"enabled: true\nmode: \" audit\"\n",
		"enabled: true\nmode: \"audit \"\n",
		"enabled: true\nmode: \"\"\n",
		"enabled: true\nmode: rewriting\n",
		"enabled: true\nmode: none\n",
		"enabled: true\nmode: off\n",
		"enabled: true\nmode: true\n",
		"enabled: true\nmode: 1\n",
		"enabled: true\nmode: [audit]\n",
		"enabled: true\nmode: {audit: true}\n",
	} {
		rejection := decodeReject(t, body)
		if rejection.Reason() != config.ReasonUnknownMode && rejection.Reason() != config.ReasonMalformedValue {
			t.Fatalf("Decode(%q) reason = %s, want %s or %s",
				body, rejection.Reason(), config.ReasonUnknownMode, config.ReasonMalformedValue)
		}
	}

	// The two accepted spellings, and nothing else, are the design's enum.
	for _, tc := range []struct {
		body string
		want rewrite.Mode
	}{
		{body: "enabled: true\nmode: audit\n", want: rewrite.ModeAudit},
		{body: "enabled: true\nmode: rewrite\n", want: rewrite.ModeRewrite},
	} {
		resolved := decodeOK(t, tc.body)
		if !resolved.Enabled {
			t.Fatalf("Decode(%q) is not enabled", tc.body)
		}
		if got := resolved.Mode(); got != tc.want {
			t.Fatalf("Decode(%q) mode = %v, want %v", tc.body, got, tc.want)
		}
	}
}

// TestTheModeIsCarriedExplicitlyNotByZeroValue proves the decoded mode is the
// operator's choice rather than a consequence of a zero value. rewrite.Mode's
// zero value is audit, so an implementation that left the field at zero for
// every configuration would pass a test that only ever asked for audit; these
// two shapes distinguish the two.
func TestTheModeIsCarriedExplicitlyNotByZeroValue(t *testing.T) {
	t.Parallel()

	if rewrite.Mode(0) != rewrite.ModeAudit {
		t.Fatal("precondition: the audit mode must be the zero value for this guard to mean anything")
	}
	rewriteMode := decodeOK(t, "enabled: true\nmode: rewrite\n")
	if rewriteMode.Mode() == rewrite.Mode(0) {
		t.Fatal("a rewrite configuration resolved to the zero mode")
	}
	auditMode := decodeOK(t, "enabled: true\nmode: audit\n")
	if auditMode.Mode() != rewrite.ModeAudit {
		t.Fatal("an audit configuration did not resolve to audit")
	}
}

// ---------------------------------------------------------------------------
// Requirement 7.4: the reserved namespace, its version, and the workspace-tag
// encoding are fixed implementation contracts, not operator values.
// ---------------------------------------------------------------------------

// designKeys is the closed set of operator configuration keys design.md
// "Configuration and Composition" prescribes. Pinning it here is the positive
// half of the requirement 7.4 proof: the decoder reads exactly these keys, so
// there is no other key an operator could spell.
var designKeys = []string{
	"enabled",
	"mandatory_max_args_bytes",
	"mode",
	"path_keys",
	"schema_inference",
	"tool_profiles",
}

// designProfileKeys is the closed key set of one declared tool profile.
var designProfileKeys = []string{
	"arg_json_pointers",
	"names",
	"opaque_result_mode",
	"result_json_pointers",
}

// forbiddenConfigConcepts are the operator-facing concepts V1 must not expose a
// knob for. Matching is case-insensitive over a field name or a YAML tag key.
var forbiddenConfigConcepts = []string{
	"alias", "digest", "glob", "marker", "namespace", "pattern", "regexp",
	"regex", "tag", "version",
}

// TestConfigSurfaceExposesNoAliasMarkerOrVersionOverride is requirement 7.4's
// structural proof. Every exported configuration type is walked by reflection
// and every field must either carry no tag or carry exactly one key from the
// pinned design set, and no field name and no tag key may name any concept V1
// deliberately does not expose. Together with the unknown-key refusal that is
// the whole surface, so there is no spelling that reaches the reserved alias
// namespace, its version, or the workspace-tag encoding.
func TestConfigSurfaceExposesNoAliasMarkerOrVersionOverride(t *testing.T) {
	t.Parallel()

	allowed := make(map[string]struct{}, len(designKeys))
	for _, key := range designKeys {
		allowed[key] = struct{}{}
	}
	allowedProfile := make(map[string]struct{}, len(designProfileKeys))
	for _, key := range designProfileKeys {
		allowedProfile[key] = struct{}{}
	}

	types := []reflect.Type{
		reflect.TypeFor[config.Config](),
		reflect.TypeFor[config.ToolProfileConfig](),
		reflect.TypeFor[config.Resolved](),
		reflect.TypeFor[config.Error](),
		reflect.TypeFor[expansion.Policy](),
		reflect.TypeFor[pathvirtualization.ToolProfile](),
		reflect.TypeFor[pathvirtualization.ProfileInput](),
		reflect.TypeFor[pathvirtualization.CompiledProfile](),
	}
	for _, cfgType := range types {
		for field := range cfgType.Fields() {
			lower := strings.ToLower(field.Name)
			for _, concept := range forbiddenConfigConcepts {
				if strings.Contains(lower, concept) {
					t.Errorf("%s.%s names the %q concept; V1 exposes no such knob",
						cfgType.Name(), field.Name, concept)
				}
			}
			if field.Tag == "" {
				// A compile-time value carries no decode surface at all, which is
				// strictly stronger than a validated one. expansion.Policy and the
				// profile/selector shapes depend on that.
				continue
			}
			tag, ok := field.Tag.Lookup("yaml")
			if !ok || tag == "" {
				continue
			}
			key, _, _ := strings.Cut(tag, ",")
			permitted := allowed
			if cfgType.Name() == "ToolProfileConfig" {
				permitted = allowedProfile
			}
			if _, ok := permitted[key]; !ok {
				t.Errorf("%s.%s declares yaml key %q, which is not one of the design keys %v",
					cfgType.Name(), field.Name, key, keysOf(permitted))
			}
			for _, concept := range forbiddenConfigConcepts {
				if strings.Contains(strings.ToLower(key), concept) {
					t.Errorf("%s.%s declares yaml key %q, which names the %q concept",
						cfgType.Name(), field.Name, key, concept)
				}
			}
		}
	}

	// The compile-time policy types must carry NO tag at all. A tag on either would
	// let a decoder populate a completeness requirement from operator YAML, which
	// is how a silent "the zero Policy means measure only" reading could reappear.
	for _, cfgType := range []reflect.Type{
		reflect.TypeFor[expansion.Policy](),
		reflect.TypeFor[pathvirtualization.ToolProfile](),
		reflect.TypeFor[pathvirtualization.ProfileInput](),
		reflect.TypeFor[pathvirtualization.CompiledProfile](),
	} {
		for field := range cfgType.Fields() {
			if tag := field.Tag; tag != "" {
				t.Errorf("%s.%s carries tag %q; no decode surface belongs on a compile-time value",
					cfgType.Name(), field.Name, tag)
			}
		}
	}
}

// TestNoProductionSourceNamesTheReservedAliasNamespace is the byte-level half
// of the same proof: not one production source of the config package contains
// the reserved marker, so there is no literal an operator value could be
// compared against, echoed, or defaulted from.
func TestNoProductionSourceNamesTheReservedAliasNamespace(t *testing.T) {
	t.Parallel()

	checked := 0
	for _, name := range productionSources(t) {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		checked++
		if strings.Contains(string(raw), reservedMarker) {
			t.Errorf("%s names the reserved alias namespace; the config surface cannot depend on it", name)
		}
	}
	if checked == 0 {
		t.Fatal("no production sources found; the reserved-namespace guard proved nothing")
	}
}

// TestUnknownConfigKeysAreRefusedIncludingAliasAndVersionKnobs is the negative
// half of requirement 7.4: every spelling an operator might reach for to move
// the namespace, the version, the tag encoding, or a pattern is refused, so the
// refusal is what happens rather than a value this build would have ignored.
func TestUnknownConfigKeysAreRefusedIncludingAliasAndVersionKnobs(t *testing.T) {
	t.Parallel()

	for _, key := range []string{
		"alias_namespace", "namespace", "reserved_namespace", "virtual_root",
		"alias_prefix", "alias_version", "version", "profile_version",
		"workspace_tag", "workspace_tag_encoding", "tag_encoding", "tag_length",
		"digest", "digest_bits",
		"pattern", "patterns", "regex", "regexp", "glob", "match", "selector_pattern",
		"mode_alias", "rewrite_mode", "expansion", "expander", "finalizer",
	} {
		body := enabledYAML(key + ": \"x\"")
		rejection := decodeReject(t, body)
		if rejection.Reason() != config.ReasonUnknownKey {
			t.Fatalf("key %q reason = %s, want %s", key, rejection.Reason(), config.ReasonUnknownKey)
		}
		if rejection.Field() != featureID {
			t.Fatalf("key %q field = %q, want the subtree root", key, rejection.Field())
		}
	}
}

// TestUnknownProfileKeysAreRefused does the same for a declared profile, where
// a namespace or pattern key is just as reachable.
func TestUnknownProfileKeysAreRefused(t *testing.T) {
	t.Parallel()

	for _, key := range []string{
		"arg_pointers", "result_pointers", "name", "tools", "tool",
		"opaque_mode", "alias_namespace", "namespace", "version",
		"pattern", "regex", "glob", "match", "depth", "priority",
	} {
		body := enabledYAML(
			"tool_profiles:\n  - names: [\"custom_read\"]\n    " + key + ": \"x\"")
		rejection := decodeReject(t, body)
		if rejection.Reason() != config.ReasonUnknownProfileKey {
			t.Fatalf("profile key %q reason = %s, want %s", key, rejection.Reason(), config.ReasonUnknownProfileKey)
		}
	}
}

// TestTheConfigKeySetIsExactlyTheDesignSet pins the closed key set in the
// positive direction, so a later additive key has to be a deliberate edit here
// as well as in the allow-list.
func TestTheConfigKeySetIsExactlyTheDesignSet(t *testing.T) {
	t.Parallel()

	if got := config.ConfigKeys(); !reflect.DeepEqual(got, designKeys) {
		t.Fatalf("ConfigKeys() = %#v, want %#v", got, designKeys)
	}
	if got := config.ToolProfileKeys(); !reflect.DeepEqual(got, designProfileKeys) {
		t.Fatalf("ToolProfileKeys() = %#v, want %#v", got, designProfileKeys)
	}
}

// ---------------------------------------------------------------------------
// V1 exposes no regex configuration.
// ---------------------------------------------------------------------------

// TestNoProductionSourceCompilesOrAcceptsAPattern proves V1 has no regular
// expression surface at all: not one production source of the config package
// imports the regexp package, so there is no pattern key to parse, no pattern
// to compile, and nothing that could cost unbounded CPU on an operator string.
func TestNoProductionSourceCompilesOrAcceptsAPattern(t *testing.T) {
	t.Parallel()

	sources := productionSources(t)
	if len(sources) == 0 {
		t.Fatal("no production sources found; the no-pattern guard proved nothing")
	}
	fset := token.NewFileSet()
	for _, name := range sources {
		file, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, spec := range file.Imports {
			imported, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("import path in %s is not a string literal: %v", name, err)
			}
			if imported == "regexp" || strings.HasSuffix(imported, "/regexp") {
				t.Errorf("%s imports %q; V1 exposes no regex configuration", name, imported)
			}
		}
	}
}

// TestSchemaInferenceReadsOnlyDeclaredShapeAndNoPattern proves the optional
// inference step has no pattern input of its own: the only thing an operator
// configures about it is a bounded vocabulary of declared property NAMES, and
// the inference result is the compiled resolver the same exact-name machinery
// already produces.
func TestSchemaInferenceReadsOnlyDeclaredShapeAndNoPattern(t *testing.T) {
	t.Parallel()

	withInference := decodeOK(t, "enabled: true\nmode: rewrite\nschema_inference: true\n")
	if !withInference.SchemaInference {
		t.Fatal("schema_inference: true did not survive compilation")
	}
	if withInference.Resolver == nil {
		t.Fatal("an enabled configuration published no resolver")
	}

	// Inference is off unless an operator asks for it, and the shipped built-in
	// layer still resolves while it is off.
	withoutInference := decodeOK(t, "enabled: true\nmode: rewrite\n")
	if withoutInference.SchemaInference {
		t.Fatal("schema inference is on without an operator asking for it")
	}
	if got := withoutInference.Resolver.Resolve("read_file", nil); got.ArgPointers == nil {
		t.Fatal("the shipped built-in layer stopped resolving with inference off")
	}
}

// ---------------------------------------------------------------------------
// The mandatory expansion bound: validated, never re-derived, never normalized.
// ---------------------------------------------------------------------------

// TestTheMandatoryBoundUsesTheSDKRangeAndIsNeverNormalized drives the
// configurable range from toolcall's own exported endpoints rather than from
// literals, so the SDK remains the single definition of the range. An absent
// key selects the shipped default; an explicit value inside the range is
// published verbatim; an explicit value outside it is a generation-compilation
// refusal, NOT a silently clamped or silently defaulted bound.
func TestTheMandatoryBoundUsesTheSDKRangeAndIsNeverNormalized(t *testing.T) {
	t.Parallel()

	if toolcall.MinMandatoryMaxArgsBytes >= toolcall.MaxMandatoryMaxArgsBytes {
		t.Fatal("precondition: the SDK configurable range is empty")
	}

	// Absent selects the shipped default.
	defaulted := decodeOK(t, "enabled: true\nmode: rewrite\n")
	if defaulted.MandatoryMaxArgsBytes != toolcall.DefaultMandatoryMaxArgsBytes {
		t.Fatalf("absent bound = %d, want the shipped default %d",
			defaulted.MandatoryMaxArgsBytes, toolcall.DefaultMandatoryMaxArgsBytes)
	}

	for _, tc := range []struct {
		name  string
		bound int
	}{
		{name: "at_floor", bound: toolcall.MinMandatoryMaxArgsBytes},
		{name: "mid_range", bound: toolcall.DefaultMandatoryMaxArgsBytes},
		{name: "at_ceiling", bound: toolcall.MaxMandatoryMaxArgsBytes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			resolved := decodeOK(t,
				"enabled: true\nmode: rewrite\nmandatory_max_args_bytes: "+strconv.Itoa(tc.bound)+"\n")
			if resolved.MandatoryMaxArgsBytes != tc.bound {
				t.Fatalf("published bound = %d, want the declared %d", resolved.MandatoryMaxArgsBytes, tc.bound)
			}
			if resolved.ExpansionPolicy().MandatoryMaxArgsBytes != tc.bound {
				t.Fatalf("expansion policy bound = %d, want the declared %d",
					resolved.ExpansionPolicy().MandatoryMaxArgsBytes, tc.bound)
			}
		})
	}

	for _, tc := range []struct {
		name  string
		bound int
	}{
		{name: "zero", bound: 0},
		{name: "negative", bound: -1},
		{name: "below_floor", bound: toolcall.MinMandatoryMaxArgsBytes - 1},
		{name: "above_ceiling", bound: toolcall.MaxMandatoryMaxArgsBytes + 1},
		{name: "far_above_ceiling", bound: toolcall.MaxMandatoryMaxArgsBytes * 4},
		{name: "platform_int_max", bound: math.MaxInt},
	} {
		t.Run("rejects_"+tc.name, func(t *testing.T) {
			t.Parallel()
			rejection := decodeReject(t,
				"enabled: true\nmode: rewrite\nmandatory_max_args_bytes: "+strconv.Itoa(tc.bound)+"\n")
			if rejection.Reason() != config.ReasonMandatoryBound {
				t.Fatalf("bound %d reason = %s, want %s", tc.bound, rejection.Reason(), config.ReasonMandatoryBound)
			}
			if rejection.Field() != featureID+".mandatory_max_args_bytes" {
				t.Fatalf("bound %d field = %q", tc.bound, rejection.Field())
			}
		})
	}
}

// TestTheBoundRangeIsNotReDerived is the structural half of the same rule: the
// config package must not spell the range itself. Both endpoints are literal
// numbers in the SDK, so pinning their decimal spelling here means a local copy
// of either one fails, whether it was written as a constant, a comparison, or
// an error message.
func TestTheBoundRangeIsNotReDerived(t *testing.T) {
	t.Parallel()

	for _, literal := range []string{
		strconv.Itoa(toolcall.MinMandatoryMaxArgsBytes),
		strconv.Itoa(toolcall.MaxMandatoryMaxArgsBytes),
	} {
		for _, name := range productionSources(t) {
			raw, err := os.ReadFile(name)
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			if strings.Contains(string(raw), literal) {
				t.Errorf("%s spells the configurable bound %s; toolcall.BufferingSpec.Validate is the single definition",
					name, literal)
			}
		}
	}
}

// TestEveryBoundaryAgreesWithTheSharedValidator is the dynamic half of
// "one definition of the configurable range". For a sweep of bounds the decode
// verdict must equal the SDK validator's own verdict, at every point around both
// endpoints and far outside them. A decode layer that re-derived the range, or
// clamped it, or treated an explicit zero as "unset", would disagree with
// toolcall.BufferingSpec.Validate at some bound in this sweep.
func TestEveryBoundaryAgreesWithTheSharedValidator(t *testing.T) {
	t.Parallel()

	bounds := []int{
		0, 1, -1, -toolcall.MinMandatoryMaxArgsBytes,
		toolcall.MinMandatoryMaxArgsBytes - 1,
		toolcall.MinMandatoryMaxArgsBytes,
		toolcall.MinMandatoryMaxArgsBytes + 1,
		toolcall.DefaultMandatoryMaxArgsBytes - 1,
		toolcall.DefaultMandatoryMaxArgsBytes,
		toolcall.DefaultMandatoryMaxArgsBytes + 1,
		toolcall.MaxMandatoryMaxArgsBytes - 1,
		toolcall.MaxMandatoryMaxArgsBytes,
		toolcall.MaxMandatoryMaxArgsBytes + 1,
		toolcall.MaxMandatoryMaxArgsBytes * 2,
		// math.MaxInt and math.MaxInt/2 rather than a spelled power of two: the
		// point of the sweep is the largest int THIS platform can hold, and a
		// literal above the 32-bit range would not compile there.
		math.MaxInt / 2, math.MaxInt,
	}
	for _, bound := range bounds {
		// The SDK's own answer, asked with exactly the declaration this feature
		// publishes: a mandatory bound with the reject overflow policy.
		wantAccepted := (toolcall.BufferingSpec{
			MaxArgsBytes: bound,
			Overflow:     toolcall.OverflowReject,
		}).Validate() == nil

		resolved, err := config.Decode(mustYAML(t,
			"enabled: true\nmode: rewrite\nmandatory_max_args_bytes: "+strconv.Itoa(bound)+"\n"))
		if gotAccepted := err == nil; gotAccepted != wantAccepted {
			t.Errorf("bound %d: decode accepted=%v, toolcall.BufferingSpec.Validate accepted=%v",
				bound, gotAccepted, wantAccepted)
			continue
		}
		if wantAccepted && resolved.MandatoryMaxArgsBytes != bound {
			t.Errorf("bound %d: published %d", bound, resolved.MandatoryMaxArgsBytes)
		}
	}
}

// TestTheOverflowPolicyIsNotConfigurable proves the one other half of the
// mandatory declaration is a fixed implementation contract. Past the declared
// bound this feature refuses the tool call; an operator cannot turn that into a
// pass-through, which is the direction requirements.md 4.5, 4.6, and 8.3 forbid.
func TestTheOverflowPolicyIsNotConfigurable(t *testing.T) {
	t.Parallel()

	for _, key := range []string{
		"mandatory_overflow", "mandatory_overflow_policy", "overflow",
		"on_overflow", "on_mandatory_overflow", "buffering_overflow",
	} {
		rejection := decodeReject(t, enabledYAML(key+": pass_through"))
		if rejection.Reason() != config.ReasonUnknownKey {
			t.Fatalf("key %q reason = %s, want %s", key, rejection.Reason(), config.ReasonUnknownKey)
		}
	}
}

// ---------------------------------------------------------------------------
// The path-key vocabulary.
// ---------------------------------------------------------------------------

// TestPathKeyValidation pins the four rules the shared vocabulary validator
// enforces: an absent vocabulary selects the shipped default, an over-limit one
// is refused whole rather than truncated, an empty or repeated entry is refused,
// and an EXPLICITLY EMPTY vocabulary is an honest "infer nothing" rather than an
// error or a silent fallback to the defaults.
func TestPathKeyValidation(t *testing.T) {
	t.Parallel()

	absent := decodeOK(t, "enabled: true\nmode: rewrite\nschema_inference: true\n")
	want := 10 // design.md's V1 vocabulary length.
	if absent.PathKeyCount() != want {
		t.Fatalf("absent vocabulary compiled %d keys, want the shipped %d", absent.PathKeyCount(), want)
	}

	// An extended vocabulary is requirement 3.3's operator escape hatch.
	extended := decodeOK(t, "enabled: true\nmode: rewrite\nschema_inference: true\npath_keys: [\"path\", \"location\"]\n")
	if extended.PathKeyCount() != 2 {
		t.Fatalf("extended vocabulary compiled %d keys, want 2", extended.PathKeyCount())
	}

	// An explicitly empty vocabulary infers nothing. It must not fall back.
	empty := decodeOK(t, "enabled: true\nmode: rewrite\nschema_inference: true\npath_keys: []\n")
	if empty.PathKeyCount() != 0 {
		t.Fatalf("an explicit empty vocabulary compiled %d keys, want 0", empty.PathKeyCount())
	}

	overLimit := make([]string, pathvirtualization.MaxPathKeys+1)
	for i := range overLimit {
		overLimit[i] = "key" + strconv.Itoa(i)
	}
	for _, tc := range []struct {
		name string
		body string
		want pathvirtualization.SelectorReject
	}{
		{
			name: "empty_entry",
			body: "path_keys: [\"path\", \"\"]\n",
			want: pathvirtualization.SelectorRejectEmptyPathKey,
		},
		{
			name: "duplicate_entry",
			body: "path_keys: [\"path\", \"path\"]\n",
			want: pathvirtualization.SelectorRejectDuplicatePathKey,
		},
		{
			name: "over_limit",
			body: "path_keys: [" + strings.Join(quoteAll(overLimit), ", ") + "]\n",
			want: pathvirtualization.SelectorRejectPathKeyCount,
		},
		{
			// Two spellings that are DIFFERENT keys byte-for-byte but the SAME
			// key after the inference step's normalization. Byte-equality alone
			// would publish both and let one declared name match two entries, so
			// the vocabulary is refused rather than made ambiguous.
			name: "normalization_collision",
			body: "path_keys: [\"file_path\", \"file path\"]\n",
			want: pathvirtualization.SelectorRejectDuplicatePathKey,
		},
		{
			name: "not_a_list",
			body: "path_keys: \"path\"\n",
			want: pathvirtualization.SelectorRejectNone,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rejection := decodeReject(t, "enabled: true\nmode: rewrite\nschema_inference: true\n"+tc.body)
			// A broken vocabulary is ReasonPathKeySelector and names `.path_keys`;
			// a `path_keys` value of the wrong YAML TYPE is a decode refusal and
			// names the subtree root. Neither is the profile layer's reason, which
			// is what splitting them made assertable.
			if rejection.Reason() != config.ReasonPathKeySelector && rejection.Reason() != config.ReasonMalformedValue {
				t.Fatalf("%s reason = %s, want %s or %s", tc.name, rejection.Reason(),
					config.ReasonPathKeySelector, config.ReasonMalformedValue)
			}
			if tc.want != pathvirtualization.SelectorRejectNone && rejection.SelectorReject() != tc.want {
				t.Fatalf("%s selector reason = %s, want %s", tc.name, rejection.SelectorReject(), tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Declared tool profiles: exact names, parsed pointers, closed opaque modes,
// and refused duplicates or conflicts.
// ---------------------------------------------------------------------------

// TestJSONPointersAreParsedAtCompileTime is requirement 7.5's "invalid
// selectors ... shall fail generation compilation" observed at the boundary: the
// published policy holds PARSED selectors rather than operator text, so no
// request can reach a pointer that was never validated, and every accepted
// spelling survives into the compiled set in declared order.
func TestJSONPointersAreParsedAtCompileTime(t *testing.T) {
	t.Parallel()

	resolved := decodeOK(t, `enabled: true
mode: rewrite
tool_profiles:
  - names: ["custom_read"]
    arg_json_pointers: ["/path", "/dir/0"]
    result_json_pointers: ["/structured/path"]
    opaque_result_mode: path_tokens
`)
	published := resolved.Resolver.Resolve("custom_read", nil)
	if published.Source != pathvirtualization.ProfileSourceOperator {
		t.Fatalf("source = %s, want the operator layer", published.Source)
	}
	if got := selectorForms(published.ArgPointers); !reflect.DeepEqual(got, []string{"/path", "/dir/0"}) {
		t.Fatalf("compiled argument selectors = %#v, want declared order", got)
	}
	if got := selectorForms(published.ResultJSONPointers); !reflect.DeepEqual(got, []string{"/structured/path"}) {
		t.Fatalf("compiled result selectors = %#v", got)
	}
	if published.OpaqueResultMode != pathvirtualization.OpaqueResultModePathTokens {
		t.Fatalf("opaque mode = %v, want the declared token mode", published.OpaqueResultMode)
	}
	// A compiled selector carries the canonical spelling it parsed from, which is
	// what proves parsing happened here rather than at request time.
	for _, selector := range published.ArgPointers {
		if selector.String() == "" {
			t.Fatal("a compiled selector has no canonical spelling")
		}
	}
}

// TestInvalidJSONPointersAreRefusedAtGenerationCompilation drives the accepted
// pointer dialect from both sides. Every refused spelling is a pointer the RFC
// 6901 reader rejects, a depth the shared bound caps, or a shape the decoder
// cannot accept, and none of them may reach a published generation.
func TestInvalidJSONPointersAreRefusedAtGenerationCompilation(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		pointer string
		want    pathvirtualization.SelectorReject
	}{
		{name: "whole_document", pointer: `""`, want: pathvirtualization.SelectorRejectEmptyPointer},
		{name: "uri_fragment", pointer: `"#/path"`, want: pathvirtualization.SelectorRejectURIFragmentPointer},
		{name: "relative", pointer: `"path"`, want: pathvirtualization.SelectorRejectNotAbsolutePointer},
		{name: "bare_token", pointer: `"/"`, want: pathvirtualization.SelectorRejectEmptyToken},
		{name: "bad_escape", pointer: `"/~2"`, want: pathvirtualization.SelectorRejectInvalidEscape},
		{name: "array_end", pointer: `"/files/-"`, want: pathvirtualization.SelectorRejectArrayEndToken},
		{
			name: "too_deep", pointer: `"` + strings.Repeat("/a", pathvirtualization.MaxPointerDepth+1) + `"`,
			want: pathvirtualization.SelectorRejectPointerDepth,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, listKey := range []string{"arg_json_pointers", "result_json_pointers"} {
				body := "enabled: true\nmode: rewrite\ntool_profiles:\n  - names: [\"custom_read\"]\n    " +
					listKey + ": [" + tc.pointer + "]\n"
				rejection := decodeReject(t, body)
				if rejection.Reason() != config.ReasonSelector {
					t.Fatalf("%s/%s reason = %s, want %s", tc.name, listKey, rejection.Reason(), config.ReasonSelector)
				}
				if rejection.SelectorReject() != tc.want {
					t.Fatalf("%s/%s selector reason = %s, want %s",
						tc.name, listKey, rejection.SelectorReject(), tc.want)
				}
				if !strings.HasPrefix(rejection.Field(), featureID+".tool_profiles") {
					t.Fatalf("%s/%s field = %q", tc.name, listKey, rejection.Field())
				}
			}
		})
	}

	// The accepted escape spellings must survive, so the refusals above are not
	// simply a closed-door policy.
	for _, pointer := range []string{`"/a~0b"`, `"/a~1b"`, `"/files/0"`, `"/a/b/c"`} {
		resolved := decodeOK(t,
			"enabled: true\nmode: rewrite\ntool_profiles:\n  - names: [\"custom_read\"]\n    arg_json_pointers: ["+pointer+"]\n")
		if published := resolved.Resolver.Resolve("custom_read", nil); len(published.ArgPointers) != 1 {
			t.Fatalf("pointer %s compiled to %d selectors, want 1", pointer, len(published.ArgPointers))
		}
	}
}

// TestToolProfileNamesMustBeExactAndNonEmpty pins exact-name authority
// (requirements 3.6, 3.7) through the configuration surface: a profile must name
// at least one whole tool name, no name may be empty, and no two profiles may
// claim the same name. A duplicate exact name is the ambiguous configuration
// requirement 7.5 refuses, because which profile's selectors would win would then
// depend on declaration order rather than on configuration.
func TestToolProfileNamesMustBeExactAndNonEmpty(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		body string
		want pathvirtualization.SelectorReject
	}{
		{
			name: "no_names",
			body: "tool_profiles:\n  - arg_json_pointers: [\"/path\"]\n",
			want: pathvirtualization.SelectorRejectEmptyToolName,
		},
		{
			name: "empty_names_list",
			body: "tool_profiles:\n  - names: []\n",
			want: pathvirtualization.SelectorRejectEmptyToolName,
		},
		{
			name: "empty_name_entry",
			body: "tool_profiles:\n  - names: [\"custom_read\", \"\"]\n",
			want: pathvirtualization.SelectorRejectEmptyToolName,
		},
		{
			name: "repeated_name_in_one_profile",
			body: "tool_profiles:\n  - names: [\"custom_read\", \"custom_read\"]\n",
			want: pathvirtualization.SelectorRejectDuplicateToolName,
		},
		{
			name: "duplicate_pointer_in_one_profile",
			body: "tool_profiles:\n  - names: [\"custom_read\"]\n    arg_json_pointers: [\"/path\", \"/path\"]\n",
			want: pathvirtualization.SelectorRejectDuplicatePointer,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rejection := decodeReject(t, "enabled: true\nmode: rewrite\n"+tc.body)
			if rejection.Reason() != config.ReasonSelector || rejection.SelectorReject() != tc.want {
				t.Fatalf("%s = (%s, %s), want (%s, %s)", tc.name, rejection.Reason(),
					rejection.SelectorReject(), config.ReasonSelector, tc.want)
			}
		})
	}

	// Two profiles claiming one exact name, and two profiles whose names collide
	// only after a confusable spelling, are both refused as ambiguous.
	for _, tc := range []struct {
		name string
		body string
	}{
		{
			name: "two_profiles_one_name",
			body: "tool_profiles:\n" +
				"  - names: [\"custom_read\"]\n    arg_json_pointers: [\"/path\"]\n" +
				"  - names: [\"custom_read\"]\n    arg_json_pointers: [\"/other\"]\n",
		},
		{
			name: "confusable_names_are_distinct_keys",
			body: "tool_profiles:\n" +
				"  - names: [\"custom_read\"]\n    arg_json_pointers: [\"/path\"]\n" +
				"  - names: [\"CUSTOM_READ\"]\n    arg_json_pointers: [\"/other\"]\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.name == "confusable_names_are_distinct_keys" {
				// A near-miss spelling is a DIFFERENT tool, so both profiles are
				// legitimate and both must resolve to their own selectors.
				resolved := decodeOK(t, "enabled: true\nmode: rewrite\n"+tc.body)
				if got := selectorForms(resolved.Resolver.Resolve("custom_read", nil).ArgPointers); !reflect.DeepEqual(got, []string{"/path"}) {
					t.Fatalf("lower-case name resolved to %v", got)
				}
				if got := selectorForms(resolved.Resolver.Resolve("CUSTOM_READ", nil).ArgPointers); !reflect.DeepEqual(got, []string{"/other"}) {
					t.Fatalf("upper-case name resolved to %v", got)
				}
				return
			}
			rejection := decodeReject(t, "enabled: true\nmode: rewrite\n"+tc.body)
			if rejection.SelectorReject() != pathvirtualization.SelectorRejectDuplicateToolName {
				t.Fatalf("%s selector reason = %s, want %s",
					tc.name, rejection.SelectorReject(), pathvirtualization.SelectorRejectDuplicateToolName)
			}
		})
	}
}

// TestOpaqueResultModeIsAClosedEnum pins the third declared profile field: three
// exact spellings, refused rather than read as the disabled default when the
// spelling is not one of them.
func TestOpaqueResultModeIsAClosedEnum(t *testing.T) {
	t.Parallel()

	for _, spelling := range []string{"None", "PATH_TOKENS", "path lines", "tokens", "lines", "all", "true", "1"} {
		body := "enabled: true\nmode: rewrite\ntool_profiles:\n  - names: [\"custom_read\"]\n" +
			"    opaque_result_mode: \"" + spelling + "\"\n"
		rejection := decodeReject(t, body)
		if rejection.Reason() != config.ReasonSelector ||
			rejection.SelectorReject() != pathvirtualization.SelectorRejectOpaqueMode {
			t.Fatalf("opaque mode %q = (%s, %s), want (%s, %s)", spelling, rejection.Reason(),
				rejection.SelectorReject(), config.ReasonSelector, pathvirtualization.SelectorRejectOpaqueMode)
		}
	}
	for _, spelling := range []string{"none", "path_tokens", "path_lines"} {
		resolved := decodeOK(t, "enabled: true\nmode: rewrite\ntool_profiles:\n  - names: [\"custom_read\"]\n"+
			"    opaque_result_mode: \""+spelling+"\"\n")
		if resolved.ProfileCount() != 1 {
			t.Fatalf("opaque mode %q published %d profiles", spelling, resolved.ProfileCount())
		}
	}
	// An absent mode is the disabled default, which is the whole reason the enum
	// is closed: nothing turns opaque rewriting on without naming a mode.
	absent := decodeOK(t, "enabled: true\nmode: rewrite\ntool_profiles:\n  - names: [\"custom_read\"]\n")
	if got := absent.Resolver.Resolve("custom_read", nil).OpaqueResultMode; got != pathvirtualization.OpaqueResultModeNone {
		t.Fatalf("an absent opaque mode = %v, want the disabled default", got)
	}
}

// TestOverLimitProfileSetsAreRefusedWhole proves the shared bounds are enforced
// on the operator layer too: more profiles than the shared cap, or more pointers
// in one profile than the shared cap, refuses the WHOLE configuration rather
// than truncating it into a policy that publishes something the operator never
// asked for.
func TestOverLimitProfileSetsAreRefusedWhole(t *testing.T) {
	t.Parallel()

	var profiles []string
	for i := 0; i <= pathvirtualization.MaxProfiles; i++ {
		profiles = append(profiles, "  - names: [\"tool_"+strconv.Itoa(i)+"\"]\n    arg_json_pointers: [\"/path\"]")
	}
	rejection := decodeReject(t, "enabled: true\nmode: rewrite\ntool_profiles:\n"+strings.Join(profiles, "\n"))
	if rejection.SelectorReject() != pathvirtualization.SelectorRejectProfileCount {
		t.Fatalf("profile count reason = %s, want %s",
			rejection.SelectorReject(), pathvirtualization.SelectorRejectProfileCount)
	}

	var pointers []string
	for i := 0; i <= pathvirtualization.MaxPointersPerProfile; i++ {
		pointers = append(pointers, "\"/p"+strconv.Itoa(i)+"\"")
	}
	rejection = decodeReject(t,
		"enabled: true\nmode: rewrite\ntool_profiles:\n  - names: [\"custom_read\"]\n    arg_json_pointers: ["+
			strings.Join(pointers, ", ")+"]\n")
	if rejection.SelectorReject() != pathvirtualization.SelectorRejectPointerCount {
		t.Fatalf("pointer count reason = %s, want %s",
			rejection.SelectorReject(), pathvirtualization.SelectorRejectPointerCount)
	}
}

// TestAnOperatorProfileReplacesTheBuiltInLayerForItsExactName is requirement
// 3.7's override clause through the configuration surface. Claiming a built-in
// name is legal (it is not a duplicate: the two layers are validated
// independently) and it REPLACES rather than merges, so an operator can widen by
// declaring the superset and narrow by declaring the subset.
func TestAnOperatorProfileReplacesTheBuiltInLayerForItsExactName(t *testing.T) {
	t.Parallel()

	resolved := decodeOK(t, "enabled: true\nmode: rewrite\ntool_profiles:\n"+
		"  - names: [\"read_file\"]\n    arg_json_pointers: [\"/custom_path\"]\n")
	if resolved.ProfileCount() != 1 {
		t.Fatalf("ProfileCount() = %d, want 1", resolved.ProfileCount())
	}
	published := resolved.Resolver.Resolve("read_file", nil)
	if published.Source != pathvirtualization.ProfileSourceOperator {
		t.Fatalf("source = %s, want the operator layer", published.Source)
	}
	if got := selectorForms(published.ArgPointers); !reflect.DeepEqual(got, []string{"/custom_path"}) {
		t.Fatalf("resolved selectors = %v, want only the declared one", got)
	}
	// A built-in name nobody overrode keeps its own layer.
	if got := resolved.Resolver.Resolve("write_file", nil); got.Source != pathvirtualization.ProfileSourceBuiltin {
		t.Fatalf("write_file source = %s, want the built-in layer", got.Source)
	}
}

// ---------------------------------------------------------------------------
// Requirement 7.5 and 7.8: typed, classifiable, content-free failures, and no
// partially valid configuration ever published.
// ---------------------------------------------------------------------------

// TestNoRejectedConfigurationPublishesAnything proves the fail-closed shape
// across the whole rejection corpus: every refusal returns the ZERO resolution,
// so there is no path on which a partially validated profile set, a half-resolved
// vocabulary, or a clamped bound can reach a request.
func TestNoRejectedConfigurationPublishesAnything(t *testing.T) {
	t.Parallel()

	corpus := []string{
		"enabled: true\n",
		"enabled: true\nmode: nope\n",
		"enabled: true\nmode: audit\nmandatory_max_args_bytes: 1\n",
		"enabled: true\nmode: audit\npath_keys: [\"\"]\n",
		"enabled: true\nmode: audit\ntool_profiles:\n  - names: []\n",
		"enabled: true\nmode: audit\ntool_profiles:\n  - names: [\"a\"]\n    arg_json_pointers: [\"#/a\"]\n",
		"enabled: true\nmode: audit\nnamespace: reserved\n",
		"enabled: true\nmode: audit\ntool_profiles:\n  - names: [\"a\"]\n    regex: \".*\"\n",
		"- a\n- b\n",
		"just-a-scalar\n",
		"42\n",
	}
	for _, body := range corpus {
		resolved, err := config.Decode(mustYAML(t, body))
		if err == nil {
			t.Fatalf("Decode(%q) published %+v; want a refusal", body, resolved)
		}
		if resolved.Enabled || resolved.Resolver != nil || resolved.ProfileCount() != 0 ||
			resolved.PathKeyCount() != 0 || resolved.MandatoryMaxArgsBytes != 0 {
			t.Fatalf("Decode(%q) published state alongside an error: %+v resolver=%v",
				body, resolved, resolved.Resolver != nil)
		}
	}
}

// TestADisabledSubtreeIsStillValidatedInFull is the deliberate half of the
// disabled-by-default rule that surprises people. Nothing constructs for a parked
// subtree, so the cost of validating one is nil, but it is validated ANYWAY:
// requirement 7.5 has no enabled-only carve-out, and a typo nobody has switched on
// yet should be a load-time refusal rather than a surprise on the day the operator
// flips the switch.
//
// Every case asserts BOTH halves of the refusal. The bounded classification says
// which rule refused, and the returned resolution must be the FULLY ZERO one, so
// the gate can neither skip a layer nor hand back state on its way to refusing.
//
// The stage column records WHERE each refusal is decided rather than hiding it: a
// key-set or node-shape refusal belongs to the node walk in DecodeConfig, and the
// rest belong to the compile steps in Config.Compile - the six compile-stage cases
// are exactly the ones an enabled gate placed first would skip, and the shipped
// built-in layer, which no subtree input can make invalid, is held by the
// structural test below rather than by a fixture here.
func TestADisabledSubtreeIsStillValidatedInFull(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		body  string
		stage string
		want  config.Reason
		field string
	}{
		{
			name:  "unknown_mode",
			body:  "enabled: false\nmode: ReWrItE\n",
			stage: "compile",
			want:  config.ReasonUnknownMode,
			field: featureID + ".mode",
		},
		{
			name:  "unsupported_bound",
			body:  "enabled: false\nmandatory_max_args_bytes: 1\n",
			stage: "compile",
			want:  config.ReasonMandatoryBound,
			field: featureID + ".mandatory_max_args_bytes",
		},
		{
			name:  "empty_path_key",
			body:  "enabled: false\npath_keys: [\"\"]\n",
			stage: "compile",
			want:  config.ReasonPathKeySelector,
			field: featureID + ".path_keys",
		},
		{
			name:  "nameless_declared_profile",
			body:  "enabled: false\ntool_profiles:\n  - names: []\n",
			stage: "compile",
			want:  config.ReasonSelector,
			field: featureID + ".tool_profiles",
		},
		{
			name:  "duplicate_declared_profile",
			body:  "enabled: false\ntool_profiles:\n  - names: [\"custom_read\"]\n  - names: [\"custom_read\"]\n",
			stage: "compile",
			want:  config.ReasonSelector,
			field: featureID + ".tool_profiles",
		},
		{
			name:  "unparseable_declared_pointer",
			body:  "enabled: false\ntool_profiles:\n  - names: [\"custom_read\"]\n    arg_json_pointers: [\"#/path\"]\n",
			stage: "compile",
			want:  config.ReasonSelector,
			field: featureID + ".tool_profiles",
		},
		{
			name:  "unknown_top_level_key",
			body:  "enabled: false\nnamespace: x\n",
			stage: "decode",
			want:  config.ReasonUnknownKey,
			field: featureID,
		},
		{
			name:  "unknown_profile_key",
			body:  "enabled: false\ntool_profiles:\n  - names: [\"a\"]\n    regex: \".*\"\n",
			stage: "decode",
			want:  config.ReasonUnknownProfileKey,
			field: featureID + ".tool_profiles",
		},
		{
			name:  "declared_profile_list_not_a_list",
			body:  "enabled: false\ntool_profiles: \"custom_read\"\n",
			stage: "decode",
			want:  config.ReasonMalformedProfile,
			field: featureID + ".tool_profiles",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			resolved, err := config.Decode(mustYAML(t, tc.body))
			if err == nil {
				t.Fatalf("a %s-stage refusal must fire for a DISABLED subtree too; "+
					"Decode(%q) published %+v", tc.stage, tc.body, resolved)
			}
			rejection := asConfigError(t, err)
			if rejection.Reason() != tc.want {
				t.Fatalf("reason = %s, want %s", rejection.Reason(), tc.want)
			}
			if rejection.Field() != tc.field {
				t.Fatalf("field = %q, want %q", rejection.Field(), tc.field)
			}
			// A disabled resolution carries the engine's zero mode, so the whole
			// bounded projection must compare equal to the zero Shape, and the
			// resolver must be nil: nothing constructs, so nothing is published on
			// the way to refusing.
			if resolved.Shape() != (config.Shape{}) || resolved.Resolver != nil {
				t.Fatalf("refusing a disabled subtree published state: shape=%+v resolver=%v",
					resolved.Shape(), resolved.Resolver != nil)
			}
		})
	}
}

// TestTheEnabledGateIsDecidedAfterEveryValidationStep pins the ORDER that makes a
// disabled subtree validated rather than merely accepted: inside Config.Compile
// every layer that can refuse - the mode, the mandatory bound, the declared
// profiles, the shipped built-in layer, and the path-key vocabulary - is reached
// BEFORE the enabled decision is taken, and the resolver BINDING is the one step
// after it.
//
// It is a structural test, and deliberately so. The shipped built-in layer is a
// compile-time literal in the lexical core, so nothing in this package can make it
// invalid and watch the refusal fire; the fact this package CAN observe is which
// statements run before the gate, and the gate is precisely what would skip them.
// An early `if !Enabled { return }` has to fail here, which is the whole property:
// a parked subtree may publish nothing, but it is never accepted unvalidated.
//
// The structural form is also what keeps the enabled-only exception honest. If the
// resolver binding drifted above the gate, a disabled compile would be validating a
// combination that publishes nothing, and this test would say so.
func TestTheEnabledGateIsDecidedAfterEveryValidationStep(t *testing.T) {
	t.Parallel()

	steps, gate := compileStepOrder(t)

	for _, want := range []string{
		"resolveMode",
		"resolveMandatoryBound",
		"declaredProfiles",
		"resolvePathKeys",
		"CompileToolProfiles",
		"New",
	} {
		positions := steps[want]
		if len(positions) == 0 {
			t.Errorf("Config.Compile never performs the %s step; this guard must be re-pinned if the layers move",
				want)
			continue
		}
		last := positions[len(positions)-1]
		for _, gatePos := range gate {
			if last > gatePos {
				t.Errorf("the %s step is decided AFTER the enabled gate, so a disabled subtree would skip it",
					want)
			}
		}
	}

	// Both profile layers are compiled, and both above the gate: the operator's own
	// declared profiles and the shipped built-in table. One call would mean the
	// shipped table is parsed on first use instead, which would make its validity
	// depend on request traffic.
	if got := len(steps["CompileToolProfiles"]); got != 2 {
		t.Errorf("Config.Compile compiles %d profile layers, want 2 (the operator layer and the shipped built-in layer)",
			got)
	}

	if got := len(steps["NewResolver"]); got != 1 {
		t.Errorf("Config.Compile binds the resolver %d times, want 1", got)
	}
	for _, bindPos := range steps["NewResolver"] {
		for _, gatePos := range gate {
			if bindPos < gatePos {
				t.Error("the resolver binding is decided BEFORE the enabled gate; " +
					"it is the one step that is enabled-only")
			}
		}
	}
}

// TestTheShippedBuiltInLayerCompilesAsWritten is the data half of the step above:
// the shipped table that generation compilation validates is valid, and every tool
// name in it is whole and exact. A future edit that breaks either one fails here
// at test time rather than at the first request that happens to resolve a built-in
// tool name, which is the traffic-dependent validity the step above exists to
// remove.
func TestTheShippedBuiltInLayerCompilesAsWritten(t *testing.T) {
	t.Parallel()

	declared := pathvirtualization.BuiltinToolProfiles()
	compiled, reject := pathvirtualization.CompileToolProfiles(declared)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("the shipped built-in layer is refused by its own compiler: %s", reject)
	}
	if len(compiled) != len(declared) {
		t.Fatalf("the shipped built-in layer compiled %d of %d profiles", len(compiled), len(declared))
	}
	if len(compiled) == 0 {
		t.Fatal("the shipped built-in layer is empty; this guard proved nothing")
	}
	for _, profile := range compiled {
		if len(profile.Names) == 0 {
			t.Fatalf("a shipped built-in profile claims no tool name: %+v", profile)
		}
		for _, name := range profile.Names {
			if name == "" || name != strings.TrimSpace(name) {
				t.Errorf("a shipped built-in profile claims the non-exact name %q", name)
			}
		}
	}
}

// TestConfigurationRejectionsAreTypedAndContentFree renders every reachable
// rejection over hostile operator input and proves none of it comes back out.
// Requirement 7.7 forbids real paths, virtualized suffixes, tool payloads, and
// high-cardinality hashes in anything a deployment can observe, and a
// configuration error is exactly the string an operator pastes into a bug
// report, so it must carry the classification and the location and nothing the
// operator typed.
func TestConfigurationRejectionsAreTypedAndContentFree(t *testing.T) {
	t.Parallel()

	secretProjectRoot := "/home/dev/very/long/workspace/project"
	secretToolName := "acme_internal_secret_read"
	secretPointer := "/secret_field"
	secretMode := "ReWrItE"

	for _, body := range []string{
		"enabled: true\nmode: " + secretMode + "\n",
		"enabled: true\nmode: audit\nmandatory_max_args_bytes: 7\n",
		"enabled: true\nmode: audit\nnamespace: " + secretProjectRoot + "\n",
		"enabled: true\nmode: audit\npath_keys: [\"\"]\n",
		"enabled: true\nmode: audit\npath_keys: [\"" + secretProjectRoot + "\", \"" + secretProjectRoot + "\"]\n",
		"enabled: true\nmode: audit\ntool_profiles:\n  - names: [\"" + secretToolName + "\"]\n" +
			"    arg_json_pointers: [\"" + secretPointer + "\", \"#" + secretPointer + "\"]\n",
		"enabled: true\nmode: audit\ntool_profiles:\n  - names: [\"" + secretToolName + "\"]\n    arg_json_pointers: [\"#/a\"]\n",
		"enabled: true\nmode: audit\ntool_profiles:\n  - names: [\"" + secretToolName + "\"]\n    opaque_result_mode: \"" + secretMode + "\"\n",
		"enabled: true\nmode: audit\n" + secretProjectRoot + ": \"" + secretToolName + "\"\n",
		"enabled: true\nmode: audit\ntool_profiles: \"" + secretToolName + "\"\n",
	} {
		_, err := config.Decode(mustYAML(t, body))
		if err == nil {
			t.Fatalf("Decode(%q) unexpectedly succeeded", body)
		}
		rendered := err.Error()
		for _, forbidden := range []string{
			secretProjectRoot, secretToolName, secretPointer, secretMode,
			reservedMarker, "/home/", "internal_secret", "secret_field",
			secretPointer, "/a", "ReWrItE",
		} {
			if forbidden == "" {
				continue
			}
			if strings.Contains(rendered, forbidden) {
				t.Errorf("requirements.md 7.7 - a rejection leaked %q: %s", forbidden, rendered)
			}
		}
		// The rendering names the feature and the fixed configuration location, so
		// an operator can find the offending key.
		if !strings.HasPrefix(rendered, featureID+": ") {
			t.Errorf("rejection %q does not name the feature subtree", rendered)
		}
	}
}

// TestConfigurationRejectionReasonsAreClosedAndContentFree pins the bounded
// classification vocabulary. It is a fixed enum whose String is a compile-time
// literal, which is what makes it safe as a metric dimension and as the
// classification a caller branches on, and an undefined value degrades rather
// than rendering an operator byte.
func TestConfigurationRejectionReasonsAreClosedAndContentFree(t *testing.T) {
	t.Parallel()

	known := []struct {
		reason config.Reason
		want   string
	}{
		{reason: config.ReasonNone, want: "none"},
		{reason: config.ReasonNotAMapping, want: "not_a_mapping"},
		{reason: config.ReasonUnknownKey, want: "unknown_key"},
		{reason: config.ReasonUnknownProfileKey, want: "unknown_profile_key"},
		{reason: config.ReasonMissingMode, want: "missing_mode"},
		{reason: config.ReasonUnknownMode, want: "unknown_mode"},
		{reason: config.ReasonMandatoryBound, want: "mandatory_bound"},
		{reason: config.ReasonSelector, want: "selector"},
		{reason: config.ReasonPathKeySelector, want: "path_key_selector"},
		{reason: config.ReasonMalformedValue, want: "malformed_value"},
		{reason: config.ReasonMalformedProfile, want: "malformed_profile"},
		{reason: config.ReasonRepairOrder, want: "repair_order"},
		{reason: config.ReasonAmbiguousRegistration, want: "ambiguous_registration"},
		{reason: config.ReasonRepairConfigUnreadable, want: "repair_config_unreadable"},
		{reason: config.ReasonSelfConfigUnreadable, want: "self_config_unreadable"},
	}
	for _, tc := range known {
		if got := tc.reason.String(); got != tc.want {
			t.Fatalf("Reason(%d).String() = %q, want %q", int(tc.reason), got, tc.want)
		}
		if strings.ContainsAny(tc.want, "/\\.: ") || tc.want == "" && tc.reason != config.ReasonNone {
			t.Fatalf("reason label %q is not a bounded token", tc.want)
		}
	}
	if got := config.Reason(200).String(); got != "unknown" {
		t.Fatalf("an undefined reason must degrade to a bounded label, got %q", got)
	}
	// Every reported configuration location is one of a fixed set, and every
	// reason this package can raise has one. A reason without a location would
	// render an error that cannot say where to look, so the two are closed
	// together.
	for _, reason := range known {
		location := config.ReasonLocation(reason.reason)
		if reason.reason == config.ReasonNone {
			if location != "" {
				t.Fatalf("the accepted reason must have no location, got %q", location)
			}
			continue
		}
		if location == "" {
			t.Fatalf("reason %s has no fixed configuration location", reason.reason)
		}
		if !strings.HasPrefix(location, featureID) {
			t.Fatalf("reason %s location %q does not name this feature's subtree", reason.reason, location)
		}
		if strings.ContainsAny(location, "/\\") || strings.Contains(location, " ") {
			t.Fatalf("reason %s location %q is not a bounded configuration path", reason.reason, location)
		}
	}
	if got := config.ReasonLocation(config.Reason(200)); got != "" {
		t.Fatalf("an undefined reason must have no location, got %q", got)
	}
}

// TestEveryRejectionNamesItsReasonsOwnFixedLocation pins the pairing reason.go
// claims between a reason and its location, at the level an operator reads it.
//
// The claim is that a reason and its location are ONE fact, so no rejection may
// name one place for a reason whose own place is another. Every reachable refusal
// is therefore rendered here and its Field() is compared both with the literal an
// operator is meant to read and with ReasonLocation of the reason it carries.
//
// The corpus is deliberately every reason, including the ones a subtree decode
// cannot reach (`.registration`, `.tool_call_repair_config`, and `.config`, which
// come from the composition guard) and the two reasons the shared validators own
// (`.tool_profiles` for the profile rules, `.path_keys` for the vocabulary). Those
// two are what the split exists for: before it, one reason named two locations and
// `.path_keys` was asserted by nothing at all.
func TestEveryRejectionNamesItsReasonsOwnFixedLocation(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		body  string
		want  config.Reason
		field string
	}{
		{
			name: "not_a_mapping", body: "just-a-scalar\n",
			want: config.ReasonNotAMapping, field: featureID,
		},
		{
			name: "unknown_key", body: "enabled: false\nnamespace: x\n",
			want: config.ReasonUnknownKey, field: featureID,
		},
		{
			name: "unknown_profile_key", body: "enabled: false\ntool_profiles:\n  - names: [\"a\"]\n    regex: \".*\"\n",
			want: config.ReasonUnknownProfileKey, field: featureID + ".tool_profiles",
		},
		{
			name: "missing_mode", body: "enabled: true\n",
			want: config.ReasonMissingMode, field: featureID + ".mode",
		},
		{
			name: "unknown_mode", body: "enabled: false\nmode: ReWrItE\n",
			want: config.ReasonUnknownMode, field: featureID + ".mode",
		},
		{
			name: "mandatory_bound", body: "enabled: false\nmandatory_max_args_bytes: 1\n",
			want: config.ReasonMandatoryBound, field: featureID + ".mandatory_max_args_bytes",
		},
		{
			name: "profile_selector", body: enabledYAML("tool_profiles:\n  - names: []\n"),
			want: config.ReasonSelector, field: featureID + ".tool_profiles",
		},
		{
			name: "path_key_selector", body: enabledYAML("path_keys: [\"\"]\n"),
			want: config.ReasonPathKeySelector, field: featureID + ".path_keys",
		},
		{
			name: "malformed_value", body: "enabled: false\npath_keys: \"path\"\n",
			want: config.ReasonMalformedValue, field: featureID,
		},
		{
			name: "malformed_profile", body: "enabled: false\ntool_profiles: \"a\"\n",
			want: config.ReasonMalformedProfile, field: featureID + ".tool_profiles",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertNamedLocation(t, decodeReject(t, tc.body), tc.want, tc.field)
		})
	}

	for _, tc := range []struct {
		name  string
		regs  []aipsdk.Registration
		want  config.Reason
		field string
	}{
		{
			name: "ambiguous_registration",
			regs: []aipsdk.Registration{
				featureRegistration(t, featureID, true, enabledYAML()),
				featureRegistration(t, featureID, true, enabledYAML()),
			},
			want:  config.ReasonAmbiguousRegistration,
			field: featureID + ".registration",
		},
		{
			name: "repair_order",
			regs: []aipsdk.Registration{
				featureRegistration(t, featureID, true, enabledYAML()),
				featureRegistration(t, toolcallrepair.ID, true, "order: 1000\n"),
			},
			want:  config.ReasonRepairOrder,
			field: featureID + ".tool_call_repair_order",
		},
		{
			name: "repair_config_unreadable",
			regs: []aipsdk.Registration{
				featureRegistration(t, featureID, true, enabledYAML()),
				featureRegistration(t, toolcallrepair.ID, true, "unknown_repair_key: 1\n"),
			},
			want:  config.ReasonRepairConfigUnreadable,
			field: featureID + ".tool_call_repair_config",
		},
		{
			name: "self_config_unreadable",
			regs: []aipsdk.Registration{
				featureRegistration(t, featureID, true, "enabled: true\nmode: ReWrItE\n"),
				featureRegistration(t, toolcallrepair.ID, true, "{}\n"),
			},
			want:  config.ReasonSelfConfigUnreadable,
			field: featureID + ".config",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := config.ValidateGenerationComposition(tc.regs)
			if err == nil {
				t.Fatalf("%s must refuse publication", tc.name)
			}
			assertNamedLocation(t, asConfigError(t, err), tc.want, tc.field)
		})
	}
}

// TestNoRejectionConstructorTakesALocation makes the pairing structural rather
// than conventional. The only functions in this package that may build an *Error
// take a Reason, and the shared validators' constructor additionally takes their
// SelectorReject; none takes a location. Field() therefore cannot disagree with
// ReasonLocation(Reason()) even in principle, and re-introducing a location
// argument fails here instead of quietly weakening the invariant the test above
// asserts from the outside.
func TestNoRejectionConstructorTakesALocation(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	constructors := 0
	for _, name := range productionSources(t) {
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			method, ok := decl.(*ast.FuncDecl)
			if !ok || !buildsError(method) {
				continue
			}
			constructors++
			for _, field := range method.Type.Params.List {
				if !isReasonOrSelectorVerdict(field.Type) {
					t.Errorf("the %s constructor takes a %s; a location must come from ReasonLocation(reason) alone",
						method.Name.Name, exprName(field.Type))
				}
			}
		}
	}
	if constructors == 0 {
		t.Fatal("no *Error constructor found; this guard proved nothing")
	}
}

// buildsError reports whether a function's single result is this package's typed
// rejection.
func buildsError(method *ast.FuncDecl) bool {
	if method.Type.Results == nil || len(method.Type.Results.List) != 1 {
		return false
	}
	pointer, ok := method.Type.Results.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	ident, ok := pointer.X.(*ast.Ident)
	return ok && ident.Name == "Error"
}

// isReasonOrSelectorVerdict reports whether a parameter type is one of the two
// bounded classifications a rejection may be built from. Reason is declared here,
// so it is a bare identifier; SelectorReject belongs to the lexical core, so it is
// a qualified one.
func isReasonOrSelectorVerdict(expr ast.Expr) bool {
	switch typed := expr.(type) {
	case *ast.Ident:
		return typed.Name == "Reason"
	case *ast.SelectorExpr:
		return typed.Sel.Name == "Reason" || typed.Sel.Name == "SelectorReject"
	default:
		return false
	}
}

// exprName renders a parameter type for a failure message without pulling in a
// type checker.
func exprName(expr ast.Expr) string {
	switch typed := expr.(type) {
	case *ast.Ident:
		return typed.Name
	case *ast.SelectorExpr:
		return exprName(typed.X) + "." + typed.Sel.Name
	case *ast.StarExpr:
		return "*" + exprName(typed.X)
	default:
		return "value of another type"
	}
}

// assertNamedLocation asserts the one-fact pairing: the rejection carries the
// expected bounded classification, names the expected literal location, and that
// literal is the classification's own.
func assertNamedLocation(t *testing.T, rejection *config.Error, want config.Reason, field string) {
	t.Helper()

	if rejection.Reason() != want {
		t.Fatalf("reason = %s, want %s", rejection.Reason(), want)
	}
	if rejection.Field() != field {
		t.Fatalf("reason %s named location %q, want %q", rejection.Reason(), rejection.Field(), field)
	}
	if own := config.ReasonLocation(rejection.Reason()); rejection.Field() != own {
		t.Fatalf("reason %s named location %q, but its own fixed location is %q",
			rejection.Reason(), rejection.Field(), own)
	}
}

// TestDecodeConfigAcceptsBothNodeShapes matches the sibling features: the
// composition root may hand a factory either the mapping node itself or the
// document wrapping it, and both decode identically.
func TestDecodeConfigAcceptsBothNodeShapes(t *testing.T) {
	t.Parallel()

	body := "enabled: true\nmode: rewrite\nmandatory_max_args_bytes: 262144\n"
	fromDocument := decodeOK(t, body)
	fromMapping, err := config.Decode(mappingYAML(t, body))
	if err != nil {
		t.Fatalf("Decode(mapping): %v", err)
	}
	if fromDocument.Shape() != fromMapping.Shape() {
		t.Fatalf("document node decoded to %+v and mapping node to %+v",
			fromDocument.Shape(), fromMapping.Shape())
	}
	if resolutionTranscript(t, fromDocument) != resolutionTranscript(t, fromMapping) {
		t.Fatal("the two node shapes published different policies")
	}
}

// TestDecodingIsDeterministic pins that the same subtree always publishes the
// same resolution. Nothing here may depend on map iteration order or on the
// order a lookup table happens to visit, or a reload could publish a different
// policy from the same configuration.
func TestDecodingIsDeterministic(t *testing.T) {
	t.Parallel()

	body := `enabled: true
mode: rewrite
schema_inference: true
mandatory_max_args_bytes: 2097152
path_keys: [path, file_path, dir]
tool_profiles:
  - names: ["custom_read", "custom_list"]
    arg_json_pointers: ["/path", "/nested/dir"]
    result_json_pointers: ["/result/path"]
    opaque_result_mode: path_lines
`
	first := decodeOK(t, body)
	firstShape := first.Shape()
	firstResolved := resolutionTranscript(t, first)
	for range 8 {
		again := decodeOK(t, body)
		if again.Shape() != firstShape {
			t.Fatalf("bounded configuration shape drifted:\n%+v\n%+v", firstShape, again.Shape())
		}
		if got := resolutionTranscript(t, again); got != firstResolved {
			t.Fatalf("published policy drifted:\n%s\n%s", firstResolved, got)
		}
		if got := selectorForms(again.Resolver.Resolve("custom_read", nil).ArgPointers); !reflect.DeepEqual(got, []string{"/path", "/nested/dir"}) {
			t.Fatalf("declared pointer order drifted: %v", got)
		}
	}
}

// ---------------------------------------------------------------------------
// Obligation (A): the cross-feature composition guard.
//
// Requirement 8.4's first clause asks mandatory path expansion to receive VALID
// completed JSON, which holds only while tool-call repair's effective finalizer
// order sorts strictly before the expansion pass's declared order. Repair's
// order is a real operator key with no upper bound, so declaring a higher
// expansion order cannot fix a generation that configures repair to sort later.
// The guard below refuses publication of exactly that generation.
// ---------------------------------------------------------------------------

// featureRegistration builds one enabled feature registration carrying a raw
// configuration subtree, the shape the composition root hands to composition.
func featureRegistration(t *testing.T, id string, enabled bool, body string) aipsdk.Registration {
	t.Helper()
	return aipsdk.Registration{
		ID:          id,
		FactoryKind: id,
		Kind:        aipsdk.PluginKindFeature,
		Enabled:     enabled,
		Config:      aipsdk.ConfigPayload{Node: mustYAML(t, body)},
	}
}

// TestTheCompositionGuardRefusesAGenerationWhereRepairSortsAfterExpansion is the
// guard's positive obligation, pinned at the exact comparison boundary: repair at
// one below the expansion finalizer's declared order composes, and repair AT that
// order does not. The boundary is read from expansion.FinalizerOrder so the
// guard cannot drift away from the constant it exists to defend.
func TestTheCompositionGuardRefusesAGenerationWhereRepairSortsAfterExpansion(t *testing.T) {
	t.Parallel()

	enabled := enabledYAML("mandatory_max_args_bytes: 1048576")

	if repairOrderAtDefault := toolcallrepair.DefaultFinalizerOrder; repairOrderAtDefault >= expansion.FinalizerOrder {
		t.Fatalf("precondition: the shipped repair order %d must sort before the expansion order %d",
			repairOrderAtDefault, expansion.FinalizerOrder)
	}

	// Below the boundary, and exactly at the shipped default, compose fine.
	for _, order := range []int{0, expansion.FinalizerOrder - 2, expansion.FinalizerOrder - 1, toolcallrepair.DefaultFinalizerOrder} {
		body := "order: " + strconv.Itoa(order)
		regs := []aipsdk.Registration{
			featureRegistration(t, featureID, true, enabled),
			featureRegistration(t, toolcallrepair.ID, true, body),
		}
		if err := config.ValidateGenerationComposition(regs); err != nil {
			t.Fatalf("repair order %d must compose, got %v", order, err)
		}
	}

	// At or above the boundary, publication is refused.
	for _, order := range []int{
		expansion.FinalizerOrder,
		expansion.FinalizerOrder + 1,
		100,
		1 << 20,
	} {
		body := "order: " + strconv.Itoa(order)
		regs := []aipsdk.Registration{
			featureRegistration(t, featureID, true, enabled),
			featureRegistration(t, toolcallrepair.ID, true, body),
		}
		err := config.ValidateGenerationComposition(regs)
		if err == nil {
			t.Fatalf("repair order %d must refuse publication", order)
		}
		var typed *config.Error
		if !errors.As(err, &typed) {
			t.Fatalf("guard error %v is not a *config.Error", err)
		}
		if typed.Reason() != config.ReasonRepairOrder {
			t.Fatalf("repair order %d reason = %s, want %s", order, typed.Reason(), config.ReasonRepairOrder)
		}
		if typed.Field() != featureID+".tool_call_repair_order" {
			t.Fatalf("repair order %d field = %q", order, typed.Field())
		}
	}
}

// TestTheCompositionGuardTracksRepairEffectiveOrderNotItsOwnConstant proves the
// guard reads repair's EFFECTIVE order, which is the operator key when present
// and the shipped default otherwise. A guard that compared against a hard-coded
// 40 would pass the boundary subtest above and still be wrong the moment
// expansion.FinalizerOrder moved.
func TestTheCompositionGuardTracksRepairEffectiveOrderNotItsOwnConstant(t *testing.T) {
	t.Parallel()

	enabled := enabledYAML()

	// Absent order key: the shipped default composes.
	regs := []aipsdk.Registration{
		featureRegistration(t, featureID, true, enabled),
		featureRegistration(t, toolcallrepair.ID, true, "{}\n"),
	}
	if err := config.ValidateGenerationComposition(regs); err != nil {
		t.Fatalf("an explicit null repair subtree must compose, got %v", err)
	}

	// Present order key below the boundary composes.
	regs = []aipsdk.Registration{
		featureRegistration(t, featureID, true, enabled),
		featureRegistration(t, toolcallrepair.ID, true, "order: 5\n"),
	}
	if err := config.ValidateGenerationComposition(regs); err != nil {
		t.Fatalf("repair order 5 must compose, got %v", err)
	}

	// Present order key above the boundary does not.
	regs = []aipsdk.Registration{
		featureRegistration(t, featureID, true, enabled),
		featureRegistration(t, toolcallrepair.ID, true, "order: 41\n"),
	}
	if err := config.ValidateGenerationComposition(regs); err == nil {
		t.Fatal("repair order 41 must refuse publication")
	}
}

// TestTheCompositionGuardFiresInBothRolloutModes proves the guard is not a
// rewrite-mode-only concern. Audit mode mints no alias, but the expansion pass
// is still registered and still the last line of defense for an alias a model
// emits on its own initiative, so a generation that sorts repair after it is
// equally unsafe to publish in audit mode.
func TestTheCompositionGuardFiresInBothRolloutModes(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"audit", "rewrite"} {
		regs := []aipsdk.Registration{
			featureRegistration(t, featureID, true, "enabled: true\nmode: "+mode+"\n"),
			featureRegistration(t, toolcallrepair.ID, true, "order: "+strconv.Itoa(expansion.FinalizerOrder+5)),
		}
		err := config.ValidateGenerationComposition(regs)
		if err == nil {
			t.Fatalf("mode %s must refuse a repair order at or above the expansion order", mode)
		}
		var typed *config.Error
		if errors.As(err, &typed) && typed.Reason() != config.ReasonRepairOrder {
			t.Fatalf("mode %s reason = %s", mode, typed.Reason())
		}
	}
}

// TestTheCompositionGuardOnlyFiresWhenBothFeaturesAreActive walks the four
// combinations that must NOT be refused, so the guard cannot be mistaken for a
// blanket rejection of a legal configuration.
func TestTheCompositionGuardOnlyFiresWhenBothFeaturesAreActive(t *testing.T) {
	t.Parallel()

	const lateRepair = "order: 1000\n"

	enabled := enabledYAML()
	disabled := "enabled: false\n"

	cases := []struct {
		name string
		regs []aipsdk.Registration
	}{
		{name: "no_registrations_at_all"},
		{
			name: "no_repair_registration",
			regs: []aipsdk.Registration{featureRegistration(t, featureID, true, enabled)},
		},
		{
			name: "path_virtualization_disabled",
			regs: []aipsdk.Registration{
				featureRegistration(t, featureID, false, disabled),
				featureRegistration(t, toolcallrepair.ID, true, lateRepair),
			},
		},
		{
			name: "repair_registration_disabled",
			regs: []aipsdk.Registration{
				featureRegistration(t, featureID, true, enabled),
				featureRegistration(t, toolcallrepair.ID, false, lateRepair),
			},
		},
		{
			name: "unrelated_feature_with_a_late_order_key",
			regs: []aipsdk.Registration{
				featureRegistration(t, featureID, true, enabled),
				featureRegistration(t, "unrelated-feature", true, lateRepair),
			},
		},
		{
			name: "non_feature_registration",
			regs: []aipsdk.Registration{
				{
					ID: toolcallrepair.ID, Kind: aipsdk.PluginKindBackend, Enabled: true,
					Config: aipsdk.ConfigPayload{Node: mustYAML(t, lateRepair)},
				},
				featureRegistration(t, featureID, true, enabled),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := config.ValidateGenerationComposition(tc.regs); err != nil {
				t.Fatalf("unexpected refusal: %v", err)
			}
		})
	}
}

// TestTheCompositionGuardRefusesAnUndecidableRepairSubtree keeps the guard
// decidable independently of the order the composition root happens to call it
// in. A repair subtree this build cannot decode means its effective order is
// unknown, and an unknown order cannot be proven to sort before the expansion
// pass, so publication is refused rather than assumed safe.
func TestTheCompositionGuardRefusesAnUndecidableRepairSubtree(t *testing.T) {
	t.Parallel()

	for _, body := range []string{
		"mode: aggressive\n",
		"order: -1\n",
		"max_args_bytes: 0\n",
		"not_a_mapping\n",
		"unknown_repair_key: 1\n",
	} {
		regs := []aipsdk.Registration{
			featureRegistration(t, featureID, true, enabledYAML()),
			featureRegistration(t, toolcallrepair.ID, true, body),
		}
		err := config.ValidateGenerationComposition(regs)
		if err == nil {
			t.Fatalf("repair subtree %q must refuse publication", body)
		}
		var typed *config.Error
		if errors.As(err, &typed) && typed.Reason() != config.ReasonRepairConfigUnreadable {
			t.Fatalf("repair subtree %q reason = %s, want %s",
				body, typed.Reason(), config.ReasonRepairConfigUnreadable)
		}
	}
}

// TestTheCompositionGuardRefusesItsOwnAmbiguity proves the guard refuses a
// generation it cannot decide rather than picking one instance: two enabled
// expansions at the same declared order, or two enabled repairs whose effective
// orders disagree, are both ambiguous configuration under requirement 7.5.
func TestTheCompositionGuardRefusesItsOwnAmbiguity(t *testing.T) {
	t.Parallel()

	enabled := enabledYAML()

	regs := []aipsdk.Registration{
		featureRegistration(t, featureID, true, enabled),
		featureRegistration(t, featureID+"-2", true, enabled),
	}
	// The second row is a different instance, not a duplicate claim of this
	// feature, so it must not be mistaken for ambiguity in the guarded feature.
	if err := config.ValidateGenerationComposition(regs); err != nil {
		t.Fatalf("a differently identified instance must not trip the guard: %v", err)
	}

	regs = []aipsdk.Registration{
		featureRegistration(t, featureID, true, enabled),
		featureRegistration(t, featureID, true, enabled),
	}
	err := config.ValidateGenerationComposition(regs)
	if err == nil {
		t.Fatal("two enabled expansions at one declared order must refuse publication")
	}
	if typed := asConfigError(t, err); typed.Reason() != config.ReasonAmbiguousRegistration {
		t.Fatalf("duplicate expansion reason = %s, want %s", typed.Reason(), config.ReasonAmbiguousRegistration)
	}

	regs = []aipsdk.Registration{
		featureRegistration(t, featureID, true, enabled),
		featureRegistration(t, toolcallrepair.ID, true, "{}\n"),
		featureRegistration(t, toolcallrepair.ID, true, "order: 3\n"),
	}
	err = config.ValidateGenerationComposition(regs)
	if err == nil {
		t.Fatal("two enabled repairs must refuse publication")
	}
	if typed := asConfigError(t, err); typed.Reason() != config.ReasonAmbiguousRegistration {
		t.Fatalf("duplicate repair reason = %s, want %s", typed.Reason(), config.ReasonAmbiguousRegistration)
	}
}

// TestTheCompositionGuardMatchesEitherRegistrationIdentity proves the guard
// finds a feature by its factory key as well as by its instance id, which is how
// the registry resolves a bundled factory, and that it does so case- and
// space-tolerantly in the same way the rest of the feature surface does.
func TestTheCompositionGuardMatchesEitherRegistrationIdentity(t *testing.T) {
	t.Parallel()

	enabled := enabledYAML()
	late := "order: 1000\n"

	for _, tc := range []struct {
		name       string
		instanceID string
		factory    string
	}{
		{name: "by_instance_id", instanceID: toolcallrepair.ID, factory: ""},
		{name: "by_factory_kind", instanceID: "acme-repair", factory: toolcallrepair.ID},
		{name: "case_insensitive_factory", instanceID: "acme-repair", factory: strings.ToUpper(toolcallrepair.ID)},
		// A padded FactoryKind is trimmed by the SDK accessor, so the
		// discriminating whitespace case pads the instance id with an empty
		// factory: only this guard's own TrimSpace can match it.
		{name: "space_padded_instance_id", instanceID: "  " + toolcallrepair.ID + "  ", factory: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			regs := []aipsdk.Registration{
				featureRegistration(t, featureID, true, enabled),
				{
					ID:          tc.instanceID,
					FactoryKind: tc.factory,
					Kind:        aipsdk.PluginKindFeature,
					Enabled:     true,
					Config:      aipsdk.ConfigPayload{Node: mustYAML(t, late)},
				},
			}
			if err := config.ValidateGenerationComposition(regs); err == nil {
				t.Fatalf("%s: a late repair order must refuse publication", tc.name)
			}
		})
	}

	// The same tolerance applies to this feature's own registration.
	for _, tc := range []struct{ id, factory string }{
		{id: featureID, factory: ""},
		{id: "acme-virtualization", factory: featureID},
		{id: "acme-virtualization", factory: strings.ToUpper(featureID)},
		{id: "  " + featureID + "  ", factory: ""},
	} {
		regs := []aipsdk.Registration{
			{
				ID:          tc.id,
				FactoryKind: tc.factory,
				Kind:        aipsdk.PluginKindFeature,
				Enabled:     true,
				Config:      aipsdk.ConfigPayload{Node: mustYAML(t, enabledYAML())},
			},
			featureRegistration(t, toolcallrepair.ID, true, "order: 1000\n"),
		}
		if err := config.ValidateGenerationComposition(regs); err == nil {
			t.Fatalf("identity (%q,%q) must find the feature and refuse a late repair order", tc.id, tc.factory)
		}
	}
}

// TestTheCompositionGuardSeesAFailedSelfDecode proves the guard is not a
// shortcut around this feature's own validation: a path-virtualization subtree
// this build refuses is refused by the guard too, so a caller cannot reach
// publication by asking the guard about an undecodable configuration.
func TestTheCompositionGuardSeesAFailedSelfDecode(t *testing.T) {
	t.Parallel()

	regs := []aipsdk.Registration{
		featureRegistration(t, featureID, true, "enabled: true\nmode: nonsense\n"),
		featureRegistration(t, toolcallrepair.ID, true, "{}\n"),
	}
	err := config.ValidateGenerationComposition(regs)
	if err == nil {
		t.Fatal("a self-undecodable configuration must refuse publication")
	}
	if typed := asConfigError(t, err); typed.Reason() != config.ReasonSelfConfigUnreadable {
		t.Fatalf("reason = %s, want %s", typed.Reason(), config.ReasonSelfConfigUnreadable)
	}
}

// ---------------------------------------------------------------------------
// Helpers.
// ---------------------------------------------------------------------------

func quoteAll(values []string) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = strconv.Quote(value)
	}
	return out
}

// resolutionTranscript renders the published policy as a bounded, deterministic
// string: what each shipped and operator tool name resolves to, in a fixed probe
// order. Two compilations of the same subtree must produce the same transcript,
// which is the determinism requirement stated at the level a request observes.
func resolutionTranscript(t *testing.T, resolved config.Resolved) string {
	t.Helper()
	if resolved.Resolver == nil {
		return "<disabled>"
	}
	names := []string{
		"read_file", "write_file", "edit_file", "replace_in_file", "delete_file",
		"remove_file", "notebook_read", "notebook_edit", "custom_read", "CUSTOM_READ",
	}
	shapes := make([]string, 0, len(names))
	for _, name := range names {
		published := resolved.Resolver.Resolve(name, nil)
		shapes = append(shapes, name+"="+published.Source.String()+
			":"+strings.Join(selectorForms(published.ArgPointers), "+")+
			":"+strings.Join(selectorForms(published.ResultJSONPointers), "+")+
			":"+published.OpaqueResultMode.String())
	}
	return strings.Join(shapes, ";")
}

func selectorForms(set pathvirtualization.SelectorSet) []string {
	forms := make([]string, 0, len(set))
	for _, selector := range set {
		forms = append(forms, selector.String())
	}
	return forms
}

func asConfigError(t *testing.T, err error) *config.Error {
	t.Helper()
	var typed *config.Error
	if !errors.As(err, &typed) {
		t.Fatalf("error %v is not a *config.Error", err)
	}
	return typed
}

func keysOf(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// compileStepOrder parses this package's own production sources and reports, in
// source order inside Config.Compile, which validation step is decided where.
//
// It returns the source position of every recognised step keyed by the step's
// own name, plus the source position of every read of the receiver's Enabled
// field. Token positions order correctly inside one file, which is all this needs
// because the ordering question is intra-procedural.
//
// It fails rather than reporting an empty map, because a guard that inspects
// nothing must not look like a guard that found nothing wrong.
func compileStepOrder(t *testing.T) (map[string][]token.Pos, []token.Pos) {
	t.Helper()

	sources := productionSources(t)
	fset := token.NewFileSet()
	steps := map[string][]token.Pos{}
	var gate []token.Pos
	methods := 0

	for _, name := range sources {
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			method, ok := decl.(*ast.FuncDecl)
			if !ok || method.Name.Name != "Compile" || !hasConfigReceiver(method) {
				continue
			}
			methods++
			ast.Inspect(method.Body, func(node ast.Node) bool {
				switch typed := node.(type) {
				case *ast.SelectorExpr:
					if base, ok := typed.X.(*ast.Ident); ok && base.Name == "c" && typed.Sel.Name == "Enabled" {
						gate = append(gate, typed.Pos())
					}
				case *ast.CallExpr:
					if step := compileStepOf(typed); step != "" {
						steps[step] = append(steps[step], typed.Pos())
					}
				}
				return true
			})
		}
	}
	if methods != 1 {
		t.Fatalf("expected exactly one Config.Compile method across %d production sources, found %d; "+
			"this guard must be re-pinned if the method is renamed or moved", len(sources), methods)
	}
	if len(gate) == 0 {
		t.Fatal("Config.Compile never reads the receiver's Enabled field, so the enabled gate is not in Compile")
	}
	return steps, gate
}

// hasConfigReceiver reports whether a method is declared on Config, by value or by
// pointer.
func hasConfigReceiver(method *ast.FuncDecl) bool {
	if method.Recv == nil || len(method.Recv.List) != 1 {
		return false
	}
	typ := method.Recv.List[0].Type
	if pointer, ok := typ.(*ast.StarExpr); ok {
		typ = pointer.X
	}
	ident, ok := typ.(*ast.Ident)
	return ok && ident.Name == "Config"
}

// compileStepOf names the validation step a call performs, or returns the empty
// string for a call that is not one of them.
//
// The qualified names are the shared validators this package delegates to; the
// bare ones are its own private steps. schemainfer.New is reported under its own
// step name so a future second inference constructor cannot be mistaken for it.
func compileStepOf(call *ast.CallExpr) string {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	pkg, ok := selector.X.(*ast.Ident)
	if !ok {
		return ""
	}
	switch {
	case pkg.Name == "pathvirtualization" &&
		(selector.Sel.Name == "CompileToolProfiles" || selector.Sel.Name == "NewResolver"):
		return selector.Sel.Name
	case pkg.Name == "schemainfer" && selector.Sel.Name == "New":
		return "New"
	case pkg.Name == "c":
		return selector.Sel.Name
	default:
		return ""
	}
}

// productionSources lists this package's non-test Go sources. It fails when the
// set is empty, because a structural guard over no files proves nothing.
func productionSources(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}
	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		names = append(names, filepath.Clean(name))
	}
	if len(names) == 0 {
		t.Fatal("no production sources found")
	}
	sort.Strings(names)
	return names
}

// TestTheCompositionGuardKeepsThePreciseCauseReachable pins the guard's error
// contract for the decode step it short-circuits.
//
// The guard answers a composition question ("does this generation publish an
// expansion pass, and does repair sort before it"), so a coarse reason is the right
// ANSWER to that question. It is not the whole story, though: the guard runs BEFORE
// the factory, so replacing the decoder's own verdict with a coarse reason means the
// precise reason an operator needs - unknown_key, missing_mode, a bad bound - is
// produced nowhere at all. The configuration that cannot be published is exactly the
// one whose reason is worth reporting.
//
// The cause is safe to carry. This package's own *Error renders as a compile-time
// prefix, a literal reason label, and a literal location, and every refusal from
// Decode and Compile is one, so joining costs no operator text and still satisfies
// requirement 7.7.
func TestTheCompositionGuardKeepsThePreciseCauseReachable(t *testing.T) {
	t.Parallel()

	// The subtree names "enabled" but no mode, so the guard's coarse reason and the
	// decoder's precise one are genuinely different verdicts.
	regs := []aipsdk.Registration{
		featureRegistration(t, featureID, true, "enabled: true\n"),
		featureRegistration(t, toolcallrepair.ID, true, "{}\n"),
	}

	err := config.ValidateGenerationComposition(regs)
	if err == nil {
		t.Fatal("an enabled subtree with no mode must refuse publication")
	}

	// The guard's own classification is unchanged and still comes first, so a caller
	// that branches on it keeps working.
	var typed *config.Error
	if !errors.As(err, &typed) || typed == nil {
		t.Fatalf("the guard must still classify as a *config.Error, got %v", err)
	}
	if typed.Reason() != config.ReasonSelfConfigUnreadable {
		t.Fatalf("guard reason = %q, want %q", typed.Reason(), config.ReasonSelfConfigUnreadable)
	}
	assertNamedLocation(t, typed, config.ReasonSelfConfigUnreadable, featureID+".config")

	// And the decoder's own verdict must still be reachable, because the guard runs
	// first and is therefore the only place this error is ever produced.
	var found *config.Error
	for _, cur := range errorChain(err) {
		var candidate *config.Error
		if errors.As(cur, &candidate) && candidate != nil &&
			candidate.Reason() == config.ReasonMissingMode {
			found = candidate
			break
		}
	}
	if found == nil {
		t.Fatalf("the decoder's own verdict is lost: want a %q cause in the chain, got %v",
			config.ReasonMissingMode, err)
	}
	if found.Field() != featureID+".mode" {
		t.Fatalf("cause field = %q, want %q", found.Field(), featureID+".mode")
	}
}

// errorChain yields err and every error reachable through it, so a test can assert
// that a cause is PRESENT without depending on how the chain is shaped. It reads
// errors.Unwrap in both forms so the assertion survives either representation.
func errorChain(err error) []error {
	seen := make(map[error]struct{})
	var out []error
	var walk func(error)
	walk = func(cur error) {
		if cur == nil {
			return
		}
		if _, dup := seen[cur]; dup {
			return
		}
		seen[cur] = struct{}{}
		out = append(out, cur)
		switch u := cur.(type) {
		case interface{ Unwrap() error }:
			walk(u.Unwrap())
		case interface{ Unwrap() []error }:
			for _, sub := range u.Unwrap() {
				walk(sub)
			}
		}
	}
	walk(err)
	return out
}
