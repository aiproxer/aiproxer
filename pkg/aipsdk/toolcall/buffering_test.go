package toolcall_test

import (
	"context"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

// Spec: b-leg-path-virtualization Task 7.1. Requirements 4.5, 4.6, 8.4 and
// design.md "6. Mandatory Buffering / Completeness Contract".
//
// The contract under test is an OPTIONAL capability: a finalizer may declare
// that it needs the complete assembled tool-call arguments before the call may
// be released. Declaring it must be possible without changing the existing
// Finalizer interface, so every already-shipped implementation keeps compiling
// and keeps behaving exactly as before.

// plainFinalizer implements only the pre-existing Finalizer method set. It is
// the shape every already-shipped finalizer has, so it is the negative control
// for the whole capability: it must keep satisfying Finalizer and must not
// satisfy the optional capability.
type plainFinalizer struct{}

func (plainFinalizer) ID() string { return "plain-finalizer" }

func (plainFinalizer) Order() int { return 0 }

func (*plainFinalizer) Finalize(
	context.Context,
	toolcall.CompletedCall,
	aipapi.ToolDef,
	[]aipapi.ToolDef,
	toolcall.Meta,
) (toolcall.Result, error) {
	return toolcall.Result{Action: toolcall.ActionPass, ReasonCode: toolcall.ReasonValidPassThrough}, nil
}

// optInFinalizer adds the single optional method on top of the unchanged
// Finalizer method set.
type optInFinalizer struct {
	plainFinalizer
	spec toolcall.BufferingSpec
}

func (f optInFinalizer) ToolCallBufferingRequirement() toolcall.BufferingSpec { return f.spec }

var (
	_ toolcall.Finalizer            = (*plainFinalizer)(nil)
	_ toolcall.BufferingRequirement = optInFinalizer{}
)

// methodNames lists an interface's method names in canonical order so a
// contract failure can name what was found.
func methodNames(iface reflect.Type) []string {
	names := []string{}
	for method := range iface.Methods() {
		names = append(names, method.Name)
	}
	return names
}

// mandatoryBoundOf is the read-side idiom consumers use: a plain type
// assertion on the finalizer, never an added interface method. Consumers that
// see a zero spec must fall back to pre-existing behavior.
func mandatoryBoundOf(f toolcall.Finalizer) (toolcall.BufferingSpec, bool) {
	req, ok := f.(toolcall.BufferingRequirement)
	if !ok {
		return toolcall.BufferingSpec{}, false
	}
	return req.ToolCallBufferingRequirement(), true
}

// TestFinalizerContract_IsUnchangedByTheOptionalBufferingCapability freezes the
// pre-existing Finalizer interface. Adding a required method would break every
// already-shipped implementation, which design.md forbids outright.
func TestFinalizerContract_IsUnchangedByTheOptionalBufferingCapability(t *testing.T) {
	t.Parallel()

	finalizerType := reflect.TypeFor[toolcall.Finalizer]()
	wantMethods := []string{"Finalize", "ID", "Order"}
	if got := finalizerType.NumMethod(); got != len(wantMethods) {
		t.Fatalf("Finalizer method count = %d, want exactly %d %v (design.md forbids adding required methods)",
			got, len(wantMethods), wantMethods)
	}
	for index, want := range wantMethods {
		if got := finalizerType.Method(index).Name; got != want {
			t.Errorf("Finalizer method %d = %q, want %q (the pre-existing method set is frozen)", index, got, want)
		}
	}
	if _, ok := finalizerType.MethodByName("ToolCallBufferingRequirement"); ok {
		t.Error("Finalizer must not declare the optional capability method; the capability must stay a separate interface")
	}
}

// TestBufferingRequirement_IsASeparateSingleMethodCapability pins the optional
// interface shape: exactly one method, the design-prescribed name, and a
// BufferingSpec return value.
func TestBufferingRequirement_IsASeparateSingleMethodCapability(t *testing.T) {
	t.Parallel()

	reqType := reflect.TypeFor[toolcall.BufferingRequirement]()
	method, ok := reqType.MethodByName("ToolCallBufferingRequirement")
	if !ok {
		t.Fatalf("optional capability must declare method ToolCallBufferingRequirement, got %v", methodNames(reqType))
	}
	if got := reqType.NumMethod(); got != 1 {
		t.Fatalf("optional capability method count = %d, want exactly 1 %v", got, methodNames(reqType))
	}

	wantSig := "func() toolcall.BufferingSpec"
	if got := method.Type.String(); got != wantSig {
		t.Errorf("optional capability method signature = %q, want %q", got, wantSig)
	}
	if got := method.Type.NumOut(); got != 1 {
		t.Fatalf("optional capability method result count = %d, want 1", got)
	}
	if got, want := method.Type.Out(0), reflect.TypeFor[toolcall.BufferingSpec](); got != want {
		t.Errorf("optional capability method result = %v, want %v", got, want)
	}
}

// TestBufferingRequirement_IsOptionalByTypeAssertion is the opt-in/opt-out
// proof: implementing Finalizer alone must not, and cannot, declare a
// mandatory bound, while adding the single optional method must.
func TestBufferingRequirement_IsOptionalByTypeAssertion(t *testing.T) {
	t.Parallel()

	plain := &plainFinalizer{}
	var finalizer toolcall.Finalizer = plain

	if _, optedIn := finalizer.(toolcall.BufferingRequirement); optedIn {
		t.Fatal("a finalizer that does not implement the optional capability must fail the type assertion")
	}
	spec, declared := mandatoryBoundOf(finalizer)
	if declared {
		t.Error("a non-opted-in finalizer must expose no declared bound")
	}
	if spec != (toolcall.BufferingSpec{}) {
		t.Errorf("a non-opted-in finalizer must expose the zero spec, got %+v", spec)
	}

	// The pre-existing method set is untouched by opting in: the same three
	// methods still satisfy Finalizer, and MaterializeSorted still accepts it.
	opted := optInFinalizer{spec: toolcall.BufferingSpec{
		MaxArgsBytes: toolcall.DefaultMandatoryMaxArgsBytes,
		Overflow:     toolcall.OverflowReject,
	}}
	var optedFinalizer toolcall.Finalizer = &opted
	if _, ok := optedFinalizer.(toolcall.BufferingRequirement); !ok {
		t.Fatal("a finalizer that implements the optional capability must satisfy the type assertion")
	}
	if got, declared := mandatoryBoundOf(&opted); !declared || got != opted.spec {
		t.Errorf("declared spec = %+v (declared=%t), want %+v (declared=true)", got, declared, opted.spec)
	}
	if got := toolcall.MaterializeSorted([]toolcall.Finalizer{&opted}); len(got) != 1 {
		t.Errorf("opted-in finalizer count after MaterializeSorted = %d, want 1", len(got))
	}
}

// TestBufferingSpec_ShapeMatchesDesignContract pins the prescribed struct:
// MaxArgsBytes then Overflow, in that order, with the prescribed types.
func TestBufferingSpec_ShapeMatchesDesignContract(t *testing.T) {
	t.Parallel()

	specType := reflect.TypeFor[toolcall.BufferingSpec]()
	wantFields := []struct {
		name string
		typ  string
	}{
		{name: "MaxArgsBytes", typ: "int"},
		{name: "Overflow", typ: "toolcall.OverflowPolicy"},
	}
	// Lower bound: the change is purely additive, so a later deliberate field
	// must not fail here. Field identity, order and type are pinned above.
	if got, want := specType.NumField(), len(wantFields); got < want {
		t.Fatalf("BufferingSpec field count = %d, want at least %d (the change is purely additive)", got, want)
	}
	for index, want := range wantFields {
		field := specType.Field(index)
		if field.Name != want.name {
			t.Errorf("BufferingSpec field %d = %q, want %q (design order MaxArgsBytes, Overflow)", index, field.Name, want.name)
			continue
		}
		if got := field.Type.String(); got != want.typ {
			t.Errorf("BufferingSpec field %q type = %q, want %q", field.Name, got, want.typ)
		}
	}

	// The policy is a string enum so the declaration is diagnosable and
	// printable without a numeric reinterpretation.
	if got, want := reflect.TypeFor[toolcall.OverflowPolicy]().Kind(), reflect.String; got != want {
		t.Errorf("OverflowPolicy kind = %v, want %v", got, want)
	}
}

// TestOverflowPolicy_ConstantsAreExactAndDistinct pins the prescribed constant
// values and proves the two policies cannot be confused with each other or with
// the unspecified zero value.
func TestOverflowPolicy_ConstantsAreExactAndDistinct(t *testing.T) {
	t.Parallel()

	if got, want := toolcall.OverflowPassThrough, toolcall.OverflowPolicy("pass_through"); got != want {
		t.Errorf("OverflowPassThrough = %q, want %q", got, want)
	}
	if got, want := toolcall.OverflowReject, toolcall.OverflowPolicy("reject"); got != want {
		t.Errorf("OverflowReject = %q, want %q", got, want)
	}
	if toolcall.OverflowPassThrough == toolcall.OverflowReject {
		t.Error("OverflowPassThrough and OverflowReject must be distinct policies")
	}
	for _, policy := range []toolcall.OverflowPolicy{toolcall.OverflowPassThrough, toolcall.OverflowReject} {
		if policy == "" {
			t.Errorf("policy %q must not collide with the unspecified zero value", policy)
		}
	}
}

// TestMandatoryBufferingBound_DefaultAndConfigurableRange pins the design
// values: the requested default bound is 1 MiB and the configurable range is
// [64 KiB, aipapi.MaxEventDeltaBytes]. Both endpoints and the default must be
// representable as a valid declaration.
func TestMandatoryBufferingBound_DefaultAndConfigurableRange(t *testing.T) {
	t.Parallel()

	if got, want := toolcall.DefaultMandatoryMaxArgsBytes, 1<<20; got != want {
		t.Errorf("DefaultMandatoryMaxArgsBytes = %d, want %d (1 MiB)", got, want)
	}
	if got, want := toolcall.MinMandatoryMaxArgsBytes, 64<<10; got != want {
		t.Errorf("MinMandatoryMaxArgsBytes = %d, want %d (64 KiB)", got, want)
	}
	if got, want := toolcall.MaxMandatoryMaxArgsBytes, aipapi.MaxEventDeltaBytes; got != want {
		t.Errorf("MaxMandatoryMaxArgsBytes = %d, want aipapi.MaxEventDeltaBytes %d", got, want)
	}
	if toolcall.MinMandatoryMaxArgsBytes > toolcall.DefaultMandatoryMaxArgsBytes ||
		toolcall.DefaultMandatoryMaxArgsBytes > toolcall.MaxMandatoryMaxArgsBytes {
		t.Errorf("declared range %d..%d must contain the default %d",
			toolcall.MinMandatoryMaxArgsBytes, toolcall.MaxMandatoryMaxArgsBytes, toolcall.DefaultMandatoryMaxArgsBytes)
	}
	// The range endpoints are meaningful against the canonical event ceiling,
	// otherwise the configurable range could not be expressed at all.
	if toolcall.MinMandatoryMaxArgsBytes >= aipapi.MaxEventDeltaBytes {
		t.Error("the configurable range floor must stay below the canonical event ceiling")
	}

	for _, maxArgsBytes := range []int{
		toolcall.MinMandatoryMaxArgsBytes,
		toolcall.DefaultMandatoryMaxArgsBytes,
		toolcall.MaxMandatoryMaxArgsBytes,
	} {
		for _, policy := range []toolcall.OverflowPolicy{toolcall.OverflowPassThrough, toolcall.OverflowReject} {
			spec := toolcall.BufferingSpec{MaxArgsBytes: maxArgsBytes, Overflow: policy}
			if err := spec.Validate(); err != nil {
				t.Errorf("BufferingSpec{MaxArgsBytes: %d, Overflow: %q} must be a valid declaration: %v", maxArgsBytes, policy, err)
			}
			if !spec.DeclaresMandatoryBound() {
				t.Errorf("BufferingSpec{MaxArgsBytes: %d, Overflow: %q} must declare a mandatory bound", maxArgsBytes, policy)
			}
		}
	}
}

// TestBufferingSpec_Validate covers the declaration contract: the zero spec and
// a well-formed declaration are valid, while a bound outside the documented
// range, a negative bound, an unknown policy, and a policy without a bound are
// rejected so a declaration can never silently change behavior.
func TestBufferingSpec_Validate(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		spec    toolcall.BufferingSpec
		wantErr bool
	}{
		{name: "zero spec declares nothing", spec: toolcall.BufferingSpec{}},
		{name: "default bound with reject", spec: toolcall.BufferingSpec{
			MaxArgsBytes: toolcall.DefaultMandatoryMaxArgsBytes, Overflow: toolcall.OverflowReject,
		}},
		{name: "range floor with reject", spec: toolcall.BufferingSpec{
			MaxArgsBytes: toolcall.MinMandatoryMaxArgsBytes, Overflow: toolcall.OverflowReject,
		}},
		{name: "range ceiling with pass through", spec: toolcall.BufferingSpec{
			MaxArgsBytes: toolcall.MaxMandatoryMaxArgsBytes, Overflow: toolcall.OverflowPassThrough,
		}},
		{name: "declared bound with unspecified policy", spec: toolcall.BufferingSpec{
			MaxArgsBytes: toolcall.DefaultMandatoryMaxArgsBytes,
		}},
		{name: "below range floor", spec: toolcall.BufferingSpec{
			MaxArgsBytes: toolcall.MinMandatoryMaxArgsBytes - 1, Overflow: toolcall.OverflowReject,
		}, wantErr: true},
		{name: "smallest positive bound", spec: toolcall.BufferingSpec{
			MaxArgsBytes: 1, Overflow: toolcall.OverflowReject,
		}, wantErr: true},
		{name: "above canonical ceiling", spec: toolcall.BufferingSpec{
			MaxArgsBytes: toolcall.MaxMandatoryMaxArgsBytes + 1, Overflow: toolcall.OverflowReject,
		}, wantErr: true},
		{name: "negative bound", spec: toolcall.BufferingSpec{
			MaxArgsBytes: -1, Overflow: toolcall.OverflowReject,
		}, wantErr: true},
		{name: "unknown policy", spec: toolcall.BufferingSpec{
			MaxArgsBytes: toolcall.DefaultMandatoryMaxArgsBytes, Overflow: toolcall.OverflowPolicy("drop"),
		}, wantErr: true},
		{name: "policy without a declared bound", spec: toolcall.BufferingSpec{
			Overflow: toolcall.OverflowReject,
		}, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.spec.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("BufferingSpec%+v must be rejected as a declaration", tc.spec)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("BufferingSpec%+v must be a valid declaration: %v", tc.spec, err)
			}
			if got, want := tc.spec.DeclaresMandatoryBound(), !tc.wantErr && tc.spec.MaxArgsBytes > 0; got != want {
				t.Errorf("BufferingSpec%+v DeclaresMandatoryBound() = %t, want %t", tc.spec, got, want)
			}
		})
	}
}

// TestBufferingSpec_ZeroSpecIsEquivalentToNotOptingIn is the contract-level
// legacy-preservation proof: a finalizer that opts in but returns the zero
// spec requests no mandatory bound, exactly like a finalizer that never opted
// in, so neither can change pre-existing buffering behavior.
func TestBufferingSpec_ZeroSpecIsEquivalentToNotOptingIn(t *testing.T) {
	t.Parallel()

	zero := toolcall.BufferingSpec{}
	if err := zero.Validate(); err != nil {
		t.Fatalf("the zero spec must be well defined, not an error: %v", err)
	}
	if zero.DeclaresMandatoryBound() {
		t.Error("the zero spec must not declare a mandatory bound")
	}

	notOptedIn := &plainFinalizer{}
	optedInWithZero := optInFinalizer{}

	specA, declaredA := mandatoryBoundOf(notOptedIn)
	specB, declaredB := mandatoryBoundOf(&optedInWithZero)
	if declaredA {
		t.Error("a finalizer that does not implement the optional capability must declare nothing")
	}
	if !declaredB {
		t.Fatal("a finalizer that implements the optional capability must be readable through the type assertion")
	}
	if specA != specB {
		t.Errorf("an opted-in finalizer returning the zero spec must read like a non-opted-in one: %+v vs %+v", specA, specB)
	}
	if specA.DeclaresMandatoryBound() || specB.DeclaresMandatoryBound() {
		t.Error("neither shape may request a mandatory bound, so pre-existing behavior is preserved for both")
	}
}

// TestToolcallPackage_ProductionSourcesStayInsidePublicSDK pins feature
// neutrality for this public SDK package: its production sources may only depend
// on other public pkg packages, so no concrete feature package and no domain
// vocabulary can be reached from here.
func TestToolcallPackage_ProductionSourcesStayInsidePublicSDK(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", name, parseErr)
		}
		checked++
		for _, spec := range file.Imports {
			imported, unquoteErr := strconv.Unquote(spec.Path.Value)
			if unquoteErr != nil {
				t.Fatalf("unquote import in %s: %v", name, unquoteErr)
			}
			if strings.Contains(imported, "/internal/") {
				t.Errorf("%s imports internal package %q; a public SDK contract must not depend on internal trees", name, imported)
			}
			if strings.Contains(imported, "plugins/features") {
				t.Errorf("%s imports concrete feature package %q; a generic SDK contract must not name a concrete feature", name, imported)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no production sources were scanned")
	}
}

// scopedOptInFinalizer adds the per-call applicability capability on top of the
// single-method declaration capability, so the narrowing contract can be driven
// against the real shipped types rather than against a local stand-in.
type scopedOptInFinalizer struct {
	optInFinalizer
	selected map[string]struct{}
}

func (f *scopedOptInFinalizer) ToolCallBufferingApplies(toolName string, _ aipapi.ToolDef, _ []aipapi.ToolDef) bool {
	_, ok := f.selected[toolName]
	return ok
}

var _ toolcall.BufferingApplicability = (*scopedOptInFinalizer)(nil)

// TestBufferingApplicability_IsASeparateSingleMethodCapability pins the shape of
// the narrowing capability the same way the declaration capability's shape is
// pinned: one method, the prescribed name, and no addition to either existing
// interface. Both facts are load-bearing, because a required method would break
// every already-shipped finalizer and a second method would make the answer
// ambiguous.
func TestBufferingApplicability_IsASeparateSingleMethodCapability(t *testing.T) {
	t.Parallel()

	applType := reflect.TypeFor[toolcall.BufferingApplicability]()
	method, ok := applType.MethodByName("ToolCallBufferingApplies")
	if !ok {
		t.Fatalf("narrowing capability must declare method ToolCallBufferingApplies, got %v", methodNames(applType))
	}
	if got := applType.NumMethod(); got != 1 {
		t.Fatalf("narrowing capability method count = %d, want exactly 1 %v", got, methodNames(applType))
	}
	if got, want := method.Type.String(),
		"func(string, aipapi.ToolDef, []aipapi.ToolDef) bool"; got != want {
		t.Errorf("narrowing capability method signature = %q, want %q", got, want)
	}

	// Neither existing interface grows: that is what keeps every already-shipped
	// finalizer compiling unchanged.
	for _, frozen := range []struct {
		name string
		typ  reflect.Type
		want []string
	}{
		{name: "Finalizer", typ: reflect.TypeFor[toolcall.Finalizer](), want: []string{"Finalize", "ID", "Order"}},
		{name: "BufferingRequirement", typ: reflect.TypeFor[toolcall.BufferingRequirement](), want: []string{"ToolCallBufferingRequirement"}},
	} {
		if got := methodNames(frozen.typ); !slices.Equal(got, frozen.want) {
			t.Errorf("%s methods = %v, want %v (the pre-existing method set is frozen)", frozen.name, got, frozen.want)
		}
	}

	// The capability's inputs are exactly the three the consumer is permitted to
	// derive applicability from. A payload or request view here would make the
	// answer depend on state the consumer cannot have when it decides how much of
	// a call to buffer.
	inputs := method.Type.NumIn()
	if inputs != 3 {
		t.Fatalf("narrowing capability input count = %d, want 3 (tool name, tool definition, catalog)", inputs)
	}
	if got := method.Type.In(0).String(); got != "string" {
		t.Errorf("narrowing capability input 0 = %q, want the tool name (string)", got)
	}
	if got := method.Type.In(1).String(); got != "aipapi.ToolDef" {
		t.Errorf("narrowing capability input 1 = %q, want aipapi.ToolDef", got)
	}
	if got := method.Type.In(2).String(); got != "[]aipapi.ToolDef" {
		t.Errorf("narrowing capability input 2 = %q, want []aipapi.ToolDef", got)
	}
}

// TestBufferingApplicability_IsOptionalAndDefaultsToEveryCall pins the opt-in
// half: a finalizer that does not implement it must fail the assertion, so the
// capability is inert rather than silently satisfied, and a finalizer that
// implements only the DECLARATION must keep its declared bound applying to every
// call it receives. That default is the conservative direction and the one the
// requirements forbid weakening.
func TestBufferingApplicability_IsOptionalAndDefaultsToEveryCall(t *testing.T) {
	t.Parallel()

	plain := &plainFinalizer{}
	if _, narrowed := toolcall.Finalizer(plain).(toolcall.BufferingApplicability); narrowed {
		t.Error("a finalizer that does not implement the narrowing capability must fail the type assertion")
	}
	declaredOnly := &optInFinalizer{spec: toolcall.BufferingSpec{
		MaxArgsBytes: toolcall.DefaultMandatoryMaxArgsBytes,
		Overflow:     toolcall.OverflowReject,
	}}
	if _, narrowed := toolcall.Finalizer(declaredOnly).(toolcall.BufferingApplicability); narrowed {
		t.Error("a finalizer that declares a bound but implements no narrowing must fail the type assertion")
	}

	scoped := &scopedOptInFinalizer{selected: map[string]struct{}{"selected-tool": {}}}
	scoped.spec = declaredOnly.spec
	if !scoped.ToolCallBufferingApplies("selected-tool", aipapi.ToolDef{Name: "selected-tool"}, nil) {
		t.Error("a configured tool must be selected")
	}
	if scoped.ToolCallBufferingApplies("other-tool", aipapi.ToolDef{Name: "other-tool"}, nil) {
		t.Error("an unconfigured tool must not be selected")
	}
	// The answer never consults the spec, so narrowing cannot silently weaken the
	// declared bound: it can only decide which calls that bound governs.
	if !scoped.spec.DeclaresMandatoryBound() {
		t.Error("narrowing must leave the published bound itself well formed")
	}
}
