package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair/repair"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

// Spec: b-leg-path-virtualization Tasks 1.1 and 7.2. Requirements 4.2, 4.5,
// 4.6, 8.3, 8.4 and design.md sections "Existing Architecture and Placement"
// (step 11 response tool-call assembly/finalization), "5. Completed Tool-Call
// Finalizer Metadata", "6. Mandatory Buffering / Completeness Contract",
// "Error Handling" ("Mandatory assembly overflow: fail closed for that tool
// call"; "Numeric finalizer ordering alone is not sufficient if assembler
// fallback could bypass mandatory expansion"), and "Testing Strategy" (">64 KiB
// call proves mandatory expansion is not bypassed"; "64 KiB and 1 MiB buffering
// boundaries").
//
// Task 1.1 characterized the CURRENT, still-broken behavior as a deliberately
// RED test: toolCallAssembler enforced one shared assembly cap
// (defaultToolCallFinalizationMaxArgsBytes) across every finalizer, so a
// completed call above that cap never reached finalizeCall, the original
// fragments reached the client unchanged, and a finalizer that must see the
// complete arguments before the call may be released never ran. The cap was
// shared with tool-call-repair's repair.DefaultMaxArgsBytes, so the repair size
// budget governed whether path expansion could happen at all.
//
// Task 7.2 delivers the mandatory buffering semantics that turn Task 1.1 green:
// the assembler computes an effective assembly bound sufficient for the
// applicable mandatory declarations, capped at aipapi.MaxEventDeltaBytes, and
// refuses a call closed past that bound when the declaring finalizer asked for
// OverflowReject. The RED condition is removed below; these assertions are now
// permanent regressions and must not be weakened to silence any failure.

const (
	// mandatoryRealRoot is the client-visible workspace root the expansion
	// finalizer must produce.
	mandatoryRealRoot = "/home/dev/workspaces/aip-path-virtualization-worktree"

	// mandatoryVirtualRoot is the fixed V1 reserved alias form (POSIX flavor,
	// 20-character workspace tag) a model may emit back to the proxy.
	mandatoryVirtualRoot = "/.__aip_v1__/w_0123456789abcdefghij/"

	mandatoryToolName = "read_file"

	// mandatoryFirstFragmentBytes cuts the first args delta in the middle of
	// the reserved alias tag, so the alias only exists across fragment
	// boundaries and can never be matched on a single raw fragment.
	mandatoryFirstFragmentBytes = len(`{"path":"/.__aip_v1__/w_0`)
)

// mandatoryExpansionFin stands in for a finalizer that must receive the
// complete tool-call arguments before the call may be released, such as reverse
// expansion of a virtual path back to the real path. It records every
// invocation and the exact arguments it saw, so a silent bypass is observable,
// and it fails closed on its own declared mandatory bound.
//
// It implements the real toolcall.BufferingRequirement capability shipped by
// Task 7.1. Task 1.1 declared the contract test-locally because the SDK type did
// not exist yet; the assembler now reads the shipped capability.
type mandatoryExpansionFin struct {
	spec  toolcall.BufferingSpec
	order int
	calls int
	seen  []byte
}

func (*mandatoryExpansionFin) ID() string { return "mandatory-expansion" }

func (f *mandatoryExpansionFin) Order() int { return f.order }

func (f *mandatoryExpansionFin) ToolCallBufferingRequirement() toolcall.BufferingSpec {
	return f.spec
}

func (f *mandatoryExpansionFin) Finalize(
	_ context.Context,
	call toolcall.CompletedCall,
	_ aipapi.ToolDef,
	_ []aipapi.ToolDef,
	_ toolcall.Meta,
) (toolcall.Result, error) {
	f.calls++
	f.seen = append(f.seen[:0], call.ArgsJSON...)
	if f.spec.Overflow == toolcall.OverflowReject && len(call.ArgsJSON) > f.spec.MaxArgsBytes {
		return toolcall.Result{Action: toolcall.ActionReject, ReasonCode: toolcall.ReasonArgsTooLarge}, nil
	}
	// Reverse expansion requires completed valid JSON; malformed arguments are
	// left to the repair step that runs before this finalizer.
	if !json.Valid(call.ArgsJSON) {
		return toolcall.Result{Action: toolcall.ActionPass, ReasonCode: toolcall.ReasonUnrepairable}, nil
	}
	expanded := bytes.Replace(call.ArgsJSON, []byte(mandatoryVirtualRoot), []byte(mandatoryRealRoot+"/"), 1)
	if bytes.Equal(expanded, call.ArgsJSON) {
		return toolcall.Result{Action: toolcall.ActionPass, ReasonCode: toolcall.ReasonValidPassThrough}, nil
	}
	return toolcall.Result{
		Action:     toolcall.ActionRewrite,
		ToolName:   call.ToolName,
		ArgsJSON:   expanded,
		ReasonCode: toolcall.ReasonValidPassThrough,
	}, nil
}

func newMandatoryExpansionFin() *mandatoryExpansionFin {
	// 1 MiB default mandatory bound from design.md, well above the shared
	// finalization cap this characterization exercises.
	return &mandatoryExpansionFin{spec: toolcall.BufferingSpec{
		MaxArgsBytes: toolcall.DefaultMandatoryMaxArgsBytes,
		Overflow:     toolcall.OverflowReject,
	}}
}

func mandatoryCatalog() []aipapi.ToolDef {
	return []aipapi.ToolDef{{Name: mandatoryToolName, Parameters: []byte(`{"type":"object"}`)}}
}

// mandatoryArgsJSON builds a valid, alias-bearing argument document of at least
// minBytes bytes.
func mandatoryArgsJSON(minBytes int) string {
	// The filler is an opaque content field, not a path-bearing target: it only
	// makes the completed call exceed a given assembly bound.
	return mandatoryArgsPrefix() + strings.Repeat("x", fillerLen(minBytes)) + `"}`
}

// mandatoryMalformedArgsJSON builds an alias-bearing argument document of at
// least minBytes bytes that is NOT valid JSON: the closing quote and brace are
// missing, which is exactly the shape tool-call-repair completes into valid
// JSON when its own size budget allows.
func mandatoryMalformedArgsJSON(minBytes int) string {
	return mandatoryArgsPrefix() + strings.Repeat("x", fillerLen(minBytes))
}

func mandatoryArgsPrefix() string {
	return `{"path":"` + mandatoryVirtualRoot + `src/main.go","content":"`
}

func fillerLen(minBytes int) int {
	// prefix+suffix of the valid form; clamp at zero for undersized requests.
	pad := minBytes - len(mandatoryArgsPrefix()) - len(`"}`)
	if pad < 0 {
		return 0
	}
	return pad
}

// mandatoryExpandedArgsJSON is the document mandatoryExpansionFin must produce
// for the corresponding mandatoryArgsJSON document.
func mandatoryExpandedArgsJSON(minBytes int) string {
	return strings.Replace(mandatoryArgsJSON(minBytes), mandatoryVirtualRoot, mandatoryRealRoot+"/", 1)
}

// splitMandatoryArgsFragments splits an argument document into stream-shaped
// deltas, cutting the first one inside the reserved alias tag.
func splitMandatoryArgsFragments(argsJSON string) []string {
	const midFragmentBytes = 4096
	rest := argsJSON
	fragments := make([]string, 0, 3)
	if len(rest) > mandatoryFirstFragmentBytes {
		fragments = append(fragments, rest[:mandatoryFirstFragmentBytes])
		rest = rest[mandatoryFirstFragmentBytes:]
	}
	if len(rest) > midFragmentBytes {
		fragments = append(fragments, rest[:midFragmentBytes])
		rest = rest[midFragmentBytes:]
	}
	return append(fragments, rest)
}

// streamMandatoryToolCall drives one completed tool call through the assembler
// as started / args-delta* / finished and returns the argument deltas the client
// would observe. Held fragments are replayed from the drain queue; fragments the
// assembler stops holding continue straight to the client, so both are
// collected in release order.
func streamMandatoryToolCall(t *testing.T, a *toolCallAssembler, id, argsJSON string) (string, error) {
	t.Helper()
	ctx := context.Background()
	meta := toolcall.Meta{}
	var released strings.Builder

	if held, ingErr := a.ingest(ctx, aipapi.Event{
		Kind: aipapi.EventToolCallStarted, ToolCallID: id, ToolName: mandatoryToolName,
	}, meta, nil); ingErr != nil || !held {
		t.Fatalf("started: held=%v err=%v", held, ingErr)
	}
	for _, fragment := range splitMandatoryArgsFragments(argsJSON) {
		held, ingErr := a.ingest(ctx, aipapi.Event{
			Kind: aipapi.EventToolCallArgsDelta, ToolCallID: id,
			ToolName: mandatoryToolName, Delta: fragment,
		}, meta, nil)
		if ingErr != nil {
			t.Fatalf("args delta: %v", ingErr)
		}
		if !held {
			released.WriteString(fragment)
		}
	}
	_, err := a.ingest(ctx, aipapi.Event{
		Kind: aipapi.EventToolCallFinished, ToolCallID: id, ToolName: mandatoryToolName,
	}, meta, nil)
	for {
		ev, ok := a.popDrain()
		if !ok {
			if err == nil {
				err = a.popDrainError()
			}
			return released.String(), err
		}
		if ev.Kind == aipapi.EventToolCallArgsDelta {
			released.WriteString(ev.Delta)
		}
	}
}

// TestToolCallAssembler_MandatoryFinalizerIsNotBypassedAboveDefaultCap is the
// Task 1.1 characterization, now GREEN (Task 7.2).
func TestToolCallAssembler_MandatoryFinalizerIsNotBypassedAboveDefaultCap(t *testing.T) {
	t.Parallel()

	catalog := mandatoryCatalog()

	// Control: the same finalizer is wired correctly below the shared cap, so a
	// failure in the subtest below is attributable to the cap and not to
	// finalizer registration, catalog lookup, or fragment buffering.
	t.Run("under_shared_cap_finalizer_sees_complete_args", func(t *testing.T) {
		t.Parallel()
		fin := newMandatoryExpansionFin()
		a := newToolCallAssembler([]toolcall.Finalizer{fin}, 0, catalog)
		if a == nil {
			t.Fatal("assembler must be constructed for a non-empty finalizer list and catalog")
		}
		if a.maxArgsBytes != defaultToolCallFinalizationMaxArgsBytes {
			t.Fatalf("shared cap: got %d want %d", a.maxArgsBytes, defaultToolCallFinalizationMaxArgsBytes)
		}
		const smallArgsBytes = 8 * 1024
		args := mandatoryArgsJSON(smallArgsBytes)
		if len(args) > a.maxArgsBytes {
			t.Fatalf("control fixture must stay under the shared cap: %d > %d", len(args), a.maxArgsBytes)
		}

		released, err := streamMandatoryToolCall(t, a, "control-1", args)
		if err != nil {
			t.Fatalf("finished: %v", err)
		}
		if fin.calls != 1 {
			t.Fatalf("control: mandatory finalizer invocations=%d want 1", fin.calls)
		}
		if string(fin.seen) != args {
			t.Fatalf("control: finalizer saw %d args bytes, want the complete %d-byte document", len(fin.seen), len(args))
		}
		if released != mandatoryExpandedArgsJSON(smallArgsBytes) {
			t.Fatalf("control: released arguments must be the expanded document; reserved alias present=%t",
				strings.Contains(released, mandatoryVirtualRoot))
		}
	})

	// The former RED: a completed call larger than the shared cap used to be
	// released as the original fragments with the declaring finalizer never
	// invoked. It must now reach the finalizer and come back expanded.
	t.Run("above_shared_cap_mandatory_expansion_must_not_be_bypassed", func(t *testing.T) {
		t.Parallel()
		fin := newMandatoryExpansionFin()
		a := newToolCallAssembler([]toolcall.Finalizer{fin}, 0, catalog)
		if a == nil {
			t.Fatal("assembler must be constructed for a non-empty finalizer list and catalog")
		}
		// The shared legacy bound keeps its own pre-existing value; only the
		// effective assembly bound is raised by a mandatory declaration.
		if a.maxArgsBytes != defaultToolCallFinalizationMaxArgsBytes {
			t.Fatalf("shared cap: got %d want %d", a.maxArgsBytes, defaultToolCallFinalizationMaxArgsBytes)
		}
		if a.mandatory.assemblyMaxArgsBytes != toolcall.DefaultMandatoryMaxArgsBytes {
			t.Fatalf("effective assembly bound: got %d want the declared %d",
				a.mandatory.assemblyMaxArgsBytes, toolcall.DefaultMandatoryMaxArgsBytes)
		}
		oversizedArgsBytes := a.maxArgsBytes + 4096
		args := mandatoryArgsJSON(oversizedArgsBytes)
		if len(args) <= a.maxArgsBytes {
			t.Fatalf("fixture must exceed the shared cap: %d <= %d", len(args), a.maxArgsBytes)
		}
		if len(args) > a.mandatory.assemblyMaxArgsBytes {
			t.Fatalf("fixture must stay within the declared mandatory bound: %d > %d",
				len(args), a.mandatory.assemblyMaxArgsBytes)
		}

		released, err := streamMandatoryToolCall(t, a, "oversize-1", args)
		aliasReleased := strings.Contains(released, mandatoryVirtualRoot)
		if fin.calls != 1 || string(fin.seen) != args {
			t.Fatalf("requirements.md 4.6/4.5/8.4 - a completed tool call of %d args bytes above the shared %d-byte finalization cap must still reach a finalizer that declares mandatory expansion; finalizer invocations=%d complete_args_seen=%d/%d reserved_alias_released=%t error=%v",
				len(args), a.maxArgsBytes, fin.calls, len(fin.seen), len(args), aliasReleased, err)
		}
		if aliasReleased {
			t.Fatalf("requirements.md 4.5 - the reserved virtual alias was released to the client without expansion")
		}
		if released != mandatoryExpandedArgsJSON(oversizedArgsBytes) {
			t.Fatalf("requirements.md 4.5 - released arguments must be the expanded document, got %d bytes with reserved alias present=%t",
				len(released), strings.Contains(released, mandatoryVirtualRoot))
		}
		if err != nil {
			t.Fatalf("finished: %v", err)
		}
	})
}

// TestToolCallAssembler_ThreeBufferingDeclarationCases pins the three
// distinguishable outcomes of reading toolcall.BufferingRequirement. The third
// one, a present-but-malformed declaration, is the direction requirements 4.5,
// 4.6, and 8.3 forbid: it must fail closed, never degrade to the legacy
// pass-through.
func TestToolCallAssembler_ThreeBufferingDeclarationCases(t *testing.T) {
	t.Parallel()

	catalog := mandatoryCatalog()

	t.Run("case1_capability_absent_keeps_legacy_pass_through", func(t *testing.T) {
		t.Parallel()
		if got := (&mandatoryBuffering{assemblyMaxArgsBytes: defaultToolCallFinalizationMaxArgsBytes}).declaredCount; got != 0 {
			t.Fatalf("case 1 declaredCount: got %d want 0", got)
		}
		// legacyOptOutFin implements exactly the pre-existing Finalizer method
		// set, so it cannot carry a buffering declaration of any kind.
		fin := &legacyOptOutFin{}
		var declared toolcall.Finalizer = fin
		if _, ok := declared.(toolcall.BufferingRequirement); ok {
			t.Fatal("case 1 fixture must not satisfy the optional capability assertion")
		}
		a := newToolCallAssembler([]toolcall.Finalizer{fin}, 0, catalog)
		if a == nil {
			t.Fatal("assembler must be constructed for a non-empty finalizer list and catalog")
		}
		if a.maxArgsBytes != defaultToolCallFinalizationMaxArgsBytes {
			t.Fatalf("shared cap: got %d want %d", a.maxArgsBytes, defaultToolCallFinalizationMaxArgsBytes)
		}
		if a.mandatory.assemblyMaxArgsBytes != a.maxArgsBytes {
			t.Fatalf("case 1 must not raise the effective assembly bound: got %d want %d",
				a.mandatory.assemblyMaxArgsBytes, a.maxArgsBytes)
		}
		if a.mandatory.rejectPastBound || a.mandatory.invalidDeclaration {
			t.Fatal("case 1 must not arm the fail-closed policy")
		}
		args := mandatoryArgsJSON(a.maxArgsBytes + 4096)
		released, err := streamMandatoryToolCall(t, a, "case1-over", args)
		if err != nil {
			t.Fatalf("case 1 must keep the pre-existing pass-through error-free: %v", err)
		}
		if fin.calls != 0 {
			t.Fatalf("case 1: finalizer invocations=%d want 0 past the shared cap", fin.calls)
		}
		if released != args {
			t.Fatalf("case 1: released %d bytes, want the %d original bytes replayed unchanged",
				len(released), len(args))
		}
	})

	t.Run("case2_capability_present_and_valid_applies_declared_bound", func(t *testing.T) {
		t.Parallel()
		fin := &mandatoryExpansionFin{spec: toolcall.BufferingSpec{
			MaxArgsBytes: toolcall.DefaultMandatoryMaxArgsBytes,
			Overflow:     toolcall.OverflowReject,
		}}
		var declared toolcall.Finalizer = fin
		if _, ok := declared.(toolcall.BufferingRequirement); !ok {
			t.Fatal("case 2 fixture must satisfy the optional capability assertion")
		}
		if !fin.spec.DeclaresMandatoryBound() {
			t.Fatal("case 2 fixture must be a well-formed mandatory declaration")
		}
		a := newToolCallAssembler([]toolcall.Finalizer{fin}, 0, catalog)
		if a == nil {
			t.Fatal("assembler must be constructed for a non-empty finalizer list and catalog")
		}
		if a.mandatory.assemblyMaxArgsBytes != toolcall.DefaultMandatoryMaxArgsBytes {
			t.Fatalf("effective assembly bound: got %d want %d",
				a.mandatory.assemblyMaxArgsBytes, toolcall.DefaultMandatoryMaxArgsBytes)
		}
		if a.mandatory.declaredCount != 1 {
			t.Fatalf("case 2 declaredCount: got %d want 1", a.mandatory.declaredCount)
		}
		if !a.mandatory.rejectPastBound {
			t.Fatal("case 2 must arm the OverflowReject fail-closed policy")
		}
		if a.mandatory.invalidDeclaration {
			t.Fatal("case 2 must not record an unusable declaration")
		}
		args := mandatoryArgsJSON(defaultToolCallFinalizationMaxArgsBytes + 4096)
		released, err := streamMandatoryToolCall(t, a, "case2-over", args)
		if err != nil {
			t.Fatalf("case 2 must assemble past the shared cap: %v", err)
		}
		if fin.calls != 1 || string(fin.seen) != args {
			t.Fatalf("case 2: invocations=%d complete_args_seen=%d/%d", fin.calls, len(fin.seen), len(args))
		}
		if strings.Contains(released, mandatoryVirtualRoot) {
			t.Fatal("case 2: reserved alias released without expansion")
		}
	})

	t.Run("case3_capability_present_but_malformed_fails_closed", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name string
			spec toolcall.BufferingSpec
		}{
			{name: "bound_below_minimum", spec: toolcall.BufferingSpec{MaxArgsBytes: 1024, Overflow: toolcall.OverflowReject}},
			{name: "bound_above_ceiling", spec: toolcall.BufferingSpec{MaxArgsBytes: aipapi.MaxEventDeltaBytes + 1, Overflow: toolcall.OverflowReject}},
			{name: "unknown_overflow_policy", spec: toolcall.BufferingSpec{MaxArgsBytes: toolcall.DefaultMandatoryMaxArgsBytes, Overflow: toolcall.OverflowPolicy("nonsense")}},
			{name: "policy_without_bound", spec: toolcall.BufferingSpec{Overflow: toolcall.OverflowReject}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				if err := tc.spec.Validate(); err == nil {
					t.Fatalf("case 3 fixture must be a malformed declaration: %+v", tc.spec)
				}
				if tc.spec.DeclaresMandatoryBound() {
					t.Fatal("case 3 fixture must not report a mandatory bound")
				}
				fin := &mandatoryExpansionFin{spec: tc.spec}
				a := newToolCallAssembler([]toolcall.Finalizer{fin}, 0, catalog)
				if a == nil {
					t.Fatal("assembler must be constructed for a non-empty finalizer list and catalog")
				}
				if !a.mandatory.invalidDeclaration {
					t.Fatal("case 3: a present-but-malformed declaration must be recorded as unusable")
				}
				if a.mandatory.declaredCount != 1 {
					t.Fatalf("case 3 declaredCount: got %d want 1", a.mandatory.declaredCount)
				}
				// The unusable bound must never widen the assembly limit.
				if a.mandatory.assemblyMaxArgsBytes != a.maxArgsBytes {
					t.Fatalf("case 3 must not adopt an unusable declared bound: got %d want %d",
						a.mandatory.assemblyMaxArgsBytes, a.maxArgsBytes)
				}

				// Small and already-assembled: finalizeCall refuses before any
				// finalizer runs and before anything is released.
				released, err := streamMandatoryToolCall(t, a, "case3-small-"+tc.name, mandatoryArgsJSON(2048))
				if fin.calls != 0 {
					t.Fatalf("case 3: no finalizer may run on a refused call, got %d invocations", fin.calls)
				}
				if released != "" {
					t.Fatalf("case 3: nothing may be released on a refused call, got %d bytes", len(released))
				}
				var mbe *MandatoryBufferingError
				if !errors.As(err, &mbe) || mbe == nil {
					t.Fatalf("case 3: want a typed MandatoryBufferingError, got %v", err)
				}
				if !errors.Is(err, ErrMandatoryBuffering) || !IsMandatoryBufferingError(err) {
					t.Fatalf("case 3: refusal must classify through the sentinel, got %v", err)
				}
				if mbe.Reason != ReasonMandatoryBufferingDeclarationInvalid {
					t.Fatalf("case 3 reason: got %q want %q", mbe.Reason, ReasonMandatoryBufferingDeclarationInvalid)
				}

				// Oversized: refusing past the bound must not replay fragments.
				big := newMandatoryExpansionFin()
				big.spec = tc.spec
				a2 := newToolCallAssembler([]toolcall.Finalizer{big}, 0, catalog)
				released2, err2 := streamMandatoryToolCall(t, a2, "case3-big-"+tc.name,
					mandatoryArgsJSON(defaultToolCallFinalizationMaxArgsBytes+4096))
				if released2 != "" {
					t.Fatalf("case 3: no argument byte may be released past the bound, got %d", len(released2))
				}
				if !IsMandatoryBufferingError(err2) {
					t.Fatalf("case 3: past the bound the call must still be refused closed, got %v", err2)
				}
			})
		}
	})
}

// TestToolCallAssembler_MandatoryBufferingBoundBoundaries pins the exact
// numeric boundaries: the configurable floor at 64 KiB, one byte above it, and
// the hard aipapi.MaxEventDeltaBytes ceiling.
func TestToolCallAssembler_MandatoryBufferingBoundBoundaries(t *testing.T) {
	t.Parallel()

	catalog := mandatoryCatalog()

	t.Run("exactly_at_the_floor_is_assembled", func(t *testing.T) {
		t.Parallel()
		fin := &mandatoryExpansionFin{spec: toolcall.BufferingSpec{
			MaxArgsBytes: toolcall.MinMandatoryMaxArgsBytes,
			Overflow:     toolcall.OverflowReject,
		}}
		a := newToolCallAssembler([]toolcall.Finalizer{fin}, 0, catalog)
		if a.mandatory.assemblyMaxArgsBytes != toolcall.MinMandatoryMaxArgsBytes {
			t.Fatalf("effective assembly bound: got %d want the declared floor %d",
				a.mandatory.assemblyMaxArgsBytes, toolcall.MinMandatoryMaxArgsBytes)
		}
		args := mandatoryArgsJSON(toolcall.MinMandatoryMaxArgsBytes)
		if len(args) != toolcall.MinMandatoryMaxArgsBytes {
			t.Fatalf("fixture must be exactly the floor: %d want %d", len(args), toolcall.MinMandatoryMaxArgsBytes)
		}
		released, err := streamMandatoryToolCall(t, a, "floor-exact", args)
		if err != nil {
			t.Fatalf("exactly the declared bound must be honored, not refused: %v", err)
		}
		if fin.calls != 1 || string(fin.seen) != args {
			t.Fatalf("finalizer invocations=%d complete_args_seen=%d/%d", fin.calls, len(fin.seen), len(args))
		}
		if released != mandatoryExpandedArgsJSON(toolcall.MinMandatoryMaxArgsBytes) {
			t.Fatalf("released %d bytes, want the expanded document with reserved alias present=%t",
				len(released), strings.Contains(released, mandatoryVirtualRoot))
		}
	})

	t.Run("one_byte_above_the_floor_fails_closed", func(t *testing.T) {
		t.Parallel()
		fin := &mandatoryExpansionFin{spec: toolcall.BufferingSpec{
			MaxArgsBytes: toolcall.MinMandatoryMaxArgsBytes,
			Overflow:     toolcall.OverflowReject,
		}}
		a := newToolCallAssembler([]toolcall.Finalizer{fin}, 0, catalog)
		args := mandatoryArgsJSON(toolcall.MinMandatoryMaxArgsBytes + 1)
		if len(args) != toolcall.MinMandatoryMaxArgsBytes+1 {
			t.Fatalf("fixture must be exactly one byte above the floor: %d", len(args))
		}
		released, err := streamMandatoryToolCall(t, a, "floor-plus-one", args)
		if released != "" {
			t.Fatalf("no argument byte may be released past the declared bound, got %d", len(released))
		}
		if fin.calls != 0 {
			t.Fatalf("the mandatory finalizer must not run past its own bound, got %d invocations", fin.calls)
		}
		var mbe *MandatoryBufferingError
		if !errors.As(err, &mbe) || mbe == nil {
			t.Fatalf("want a typed MandatoryBufferingError, got %v", err)
		}
		if mbe.Reason != ReasonMandatoryBufferingOverflow {
			t.Fatalf("reason: got %q want %q", mbe.Reason, ReasonMandatoryBufferingOverflow)
		}
		if mbe.MaxArgsBytes != toolcall.MinMandatoryMaxArgsBytes {
			t.Fatalf("refusal must report the exceeded bound: got %d want %d",
				mbe.MaxArgsBytes, toolcall.MinMandatoryMaxArgsBytes)
		}
		if err.Error() != "tool call finalization: tool call assembler: mandatory buffering requirement not honored ("+
			ReasonMandatoryBufferingOverflow+")" {
			t.Fatalf("refusal message must be the bounded, content-free classification: %q", err.Error())
		}
	})

	t.Run("effective_bound_is_capped_at_the_ceiling", func(t *testing.T) {
		t.Parallel()
		atCeiling := &mandatoryExpansionFin{spec: toolcall.BufferingSpec{
			MaxArgsBytes: aipapi.MaxEventDeltaBytes,
			Overflow:     toolcall.OverflowReject,
		}}
		a := newToolCallAssembler([]toolcall.Finalizer{atCeiling}, aipapi.MaxEventDeltaBytes+1, catalog)
		if a.mandatory.assemblyMaxArgsBytes != aipapi.MaxEventDeltaBytes {
			t.Fatalf("effective assembly bound: got %d want the hard ceiling %d",
				a.mandatory.assemblyMaxArgsBytes, aipapi.MaxEventDeltaBytes)
		}
		overCeiling := &mandatoryExpansionFin{spec: toolcall.BufferingSpec{
			MaxArgsBytes: aipapi.MaxEventDeltaBytes + 1,
			Overflow:     toolcall.OverflowReject,
		}}
		a2 := newToolCallAssembler([]toolcall.Finalizer{overCeiling}, 0, catalog)
		if a2.mandatory.assemblyMaxArgsBytes > aipapi.MaxEventDeltaBytes {
			t.Fatalf("effective assembly bound must never exceed the ceiling: %d", a2.mandatory.assemblyMaxArgsBytes)
		}
		if !a2.mandatory.invalidDeclaration {
			t.Fatal("a bound above the ceiling is an unusable declaration and must be recorded as such")
		}
	})

	t.Run("declaration_never_lowers_the_shared_assembly_bound_but_is_still_enforced", func(t *testing.T) {
		t.Parallel()
		// REPLACES `declaration_never_lowers_the_shared_bound`, which asserted
		// only the first half of the rule and therefore ENCODED blocker 2. The
		// maximum aggregation is sound ONLY if each declarer's own limit is
		// enforced afterwards; nothing did, because the shipped expansion pass
		// records `Report.ArgsOverDeclaredBound` for telemetry and never refuses.
		// So a 64 KiB declaration beside a 128 KiB shared cap was unenforceable.
		//
		// The first half is unchanged: an operator-raised shared bound stays
		// authoritative for ordinary finalizers, so a smaller mandatory
		// declaration must not narrow what the assembler buffers.
		fin := &mandatoryExpansionFin{spec: toolcall.BufferingSpec{
			MaxArgsBytes: toolcall.MinMandatoryMaxArgsBytes,
			Overflow:     toolcall.OverflowReject,
		}}
		const raised = 128 * 1024
		a := newToolCallAssembler([]toolcall.Finalizer{fin}, raised, catalog)
		if a.maxArgsBytes != raised {
			t.Fatalf("shared bound: got %d want %d", a.maxArgsBytes, raised)
		}
		if a.mandatory.assemblyMaxArgsBytes != raised {
			t.Fatalf("effective assembly bound: got %d want the operator-raised %d",
				a.mandatory.assemblyMaxArgsBytes, raised)
		}

		// The second half, with the real numbers: the call fits the ceiling the
		// assembler buffers to and exceeds the bound the declarer published, so
		// the only thing that can refuse it is the consumer enforcing that
		// declarer's own limit.
		if err := fin.spec.Validate(); err != nil {
			t.Fatalf("fixture: the declaration must be well formed: %v", err)
		}
		args := mandatoryArgsJSON(toolcall.MinMandatoryMaxArgsBytes + 32*1024)
		if len(args) <= fin.spec.MaxArgsBytes || len(args) > raised {
			t.Fatalf("fixture must sit between the declarer's own bound and the shared cap: %d not in (%d, %d]",
				len(args), fin.spec.MaxArgsBytes, raised)
		}
		released, err := streamMandatoryToolCall(t, a, "own-bound-not-raised", args)
		if released != "" {
			t.Fatalf("requirements.md 4.5 - released %d bytes past the declarer's own bound", len(released))
		}
		var mbe *MandatoryBufferingError
		if !errors.As(err, &mbe) || mbe == nil {
			t.Fatalf("requirements.md 4.5 - want a typed MandatoryBufferingError, got %T", err)
		}
		if mbe.Reason != ReasonMandatoryBufferingOverflow {
			t.Fatalf("reason: got %q want %q", mbe.Reason, ReasonMandatoryBufferingOverflow)
		}
		if mbe.MaxArgsBytes != toolcall.MinMandatoryMaxArgsBytes {
			t.Fatalf("the refusal must report the declarer's OWN bound, not the shared one: got %d want %d",
				mbe.MaxArgsBytes, toolcall.MinMandatoryMaxArgsBytes)
		}
		if mbe.FinalizerID != fin.ID() {
			t.Fatalf("the refusal must name the declarer: got %q want %q", mbe.FinalizerID, fin.ID())
		}
	})

	t.Run("zero_spec_declaration_declares_nothing", func(t *testing.T) {
		t.Parallel()
		// Opting in with the zero BufferingSpec is observationally identical to
		// not implementing the capability at all.
		fin := &mandatoryExpansionFin{}
		if fin.spec.DeclaresMandatoryBound() {
			t.Fatal("the zero BufferingSpec must declare no requirement")
		}
		if err := fin.spec.Validate(); err != nil {
			t.Fatalf("the zero BufferingSpec must be valid: %v", err)
		}
		a := newToolCallAssembler([]toolcall.Finalizer{fin}, 0, catalog)
		if a.mandatory.assemblyMaxArgsBytes != a.maxArgsBytes {
			t.Fatalf("zero spec must not raise the effective assembly bound: got %d want %d",
				a.mandatory.assemblyMaxArgsBytes, a.maxArgsBytes)
		}
		if a.mandatory.rejectPastBound || a.mandatory.invalidDeclaration {
			t.Fatal("zero spec must not arm the fail-closed policy")
		}
	})
}

// TestToolCallAssembler_MandatoryOverflowRefusalCarriesNoContent proves the
// refusal is classifiable and content-free, and that nothing alias-bearing is
// released before it.
func TestToolCallAssembler_MandatoryOverflowRefusalCarriesNoContent(t *testing.T) {
	t.Parallel()

	fin := &mandatoryExpansionFin{spec: toolcall.BufferingSpec{
		MaxArgsBytes: toolcall.MinMandatoryMaxArgsBytes,
		Overflow:     toolcall.OverflowReject,
	}}
	a := newToolCallAssembler([]toolcall.Finalizer{fin}, 0, mandatoryCatalog())
	args := mandatoryArgsJSON(toolcall.MinMandatoryMaxArgsBytes + 8192)

	released, err := streamMandatoryToolCall(t, a, "no-content", args)

	if err == nil {
		t.Fatal("a call past the declared OverflowReject bound must be refused")
	}
	if released != "" {
		t.Fatalf("nothing may be released before the refusal: got %d bytes", len(released))
	}
	if strings.Contains(released, mandatoryVirtualRoot) || strings.Contains(released, mandatoryRealRoot) {
		t.Fatal("no alias or real root may be released on a refused call")
	}
	msg := err.Error()
	for _, forbidden := range []string{
		mandatoryVirtualRoot,
		mandatoryRealRoot,
		"w_0123456789abcdefghij",
		"src/main.go",
		"no-content",
	} {
		if strings.Contains(msg, forbidden) {
			t.Fatalf("refusal message leaked %q: %q", forbidden, msg)
		}
	}
	var mbe *MandatoryBufferingError
	if !errors.As(err, &mbe) {
		t.Fatalf("want a typed MandatoryBufferingError, got %v", err)
	}
	if mbe.ToolCallID != "no-content" {
		t.Fatalf("classification field ToolCallID: got %q", mbe.ToolCallID)
	}
	if mbe.FinalizerID != fin.ID() {
		t.Fatalf("classification field FinalizerID: got %q want %q", mbe.FinalizerID, fin.ID())
	}
	// The refusal must clear the per-call state so the assembler is reusable.
	if _, ok := a.refusing["no-content"]; ok {
		t.Fatal("refused tool call must be removed from the refusing set")
	}
	if _, ok := a.active["no-content"]; ok {
		t.Fatal("refused tool call must be removed from the active set")
	}
}

// TestToolCallAssembler_RaisedMandatoryBoundDoesNotRaiseRepairBudget proves the
// effective assembly bound does not widen any other finalizer's own size policy:
// tool-call-repair keeps its 64 KiB repair budget (requirements 4.2 and 8.4),
// while a repair refusal still cannot skip the mandatory finalizer
// (requirements 4.6 and 8.4).
func TestToolCallAssembler_RaisedMandatoryBoundDoesNotRaiseRepairBudget(t *testing.T) {
	t.Parallel()

	catalog := mandatoryCatalog()
	// tool-call-repair at its production order, registered alongside a
	// mandatory-declaring finalizer that keeps its natural default order, so
	// repair runs after it.
	repairFin := repair.NewFinalizer(repair.FinalizerPolicy{ID: "tool-call-repair", Order: repair.DefaultFinalizerOrder})

	newAssembler := func(t *testing.T) (*toolCallAssembler, *mandatoryExpansionFin) {
		t.Helper()
		mand := newMandatoryExpansionFin()
		a := newToolCallAssembler([]toolcall.Finalizer{repairFin, mand}, 0, catalog)
		if a == nil {
			t.Fatal("assembler must be constructed for a non-empty finalizer list and catalog")
		}
		if a.maxArgsBytes != defaultToolCallFinalizationMaxArgsBytes {
			t.Fatalf("shared bound must stay at the legacy default: got %d want %d",
				a.maxArgsBytes, defaultToolCallFinalizationMaxArgsBytes)
		}
		if a.mandatory.assemblyMaxArgsBytes != toolcall.DefaultMandatoryMaxArgsBytes {
			t.Fatalf("effective assembly bound: got %d want %d",
				a.mandatory.assemblyMaxArgsBytes, toolcall.DefaultMandatoryMaxArgsBytes)
		}
		return a, mand
	}

	t.Run("repair_budget_still_refuses_an_oversized_payload_without_bypassing_expansion", func(t *testing.T) {
		t.Parallel()
		a, mand := newAssembler(t)
		args := mandatoryMalformedArgsJSON(defaultToolCallFinalizationMaxArgsBytes + 4096)
		if len(args) <= repair.DefaultMaxArgsBytes {
			t.Fatalf("fixture must exceed the repair budget: %d <= %d", len(args), repair.DefaultMaxArgsBytes)
		}

		released, err := streamMandatoryToolCall(t, a, "repair-over", args)
		if err != nil {
			t.Fatalf("a payload past the repair budget must not fail the call: %v", err)
		}
		// Requirement 4.6/8.4: the unrelated finalizer's size limit must not skip
		// the mandatory one, which still saw the complete document.
		if mand.calls != 1 || string(mand.seen) != args {
			t.Fatalf("tool-call-repair's size policy must not bypass mandatory expansion; invocations=%d complete_args_seen=%d/%d",
				mand.calls, len(mand.seen), len(args))
		}
		// The repair budget itself is unchanged: the malformed payload was not
		// completed, so the original fragments are released unchanged.
		if released != args {
			t.Fatalf("tool-call-repair must keep refusing to repair past its own %d-byte budget; released %d bytes, changed=%t",
				repair.DefaultMaxArgsBytes, len(released), released != args)
		}
	})
}

// failingOrdinaryFin is an ordinary, non-declaring finalizer that fails before
// it can hand back a usable result. It stands in for an unrelated optional
// finalizer that errors (requirement 4.6's failure clause), never for a
// declaring one: it publishes no buffering requirement at all.
type failingOrdinaryFin struct {
	order int
	calls int
}

func (*failingOrdinaryFin) ID() string { return "failing-ordinary" }

func (f *failingOrdinaryFin) Order() int { return f.order }

func (f *failingOrdinaryFin) Finalize(
	_ context.Context,
	_ toolcall.CompletedCall,
	_ aipapi.ToolDef,
	_ []aipapi.ToolDef,
	_ toolcall.Meta,
) (toolcall.Result, error) {
	f.calls++
	return toolcall.Result{}, context.Canceled
}

// unusableOrdinaryFin is an ordinary, non-declaring finalizer that returns a
// well-formed Result whose Action the assembler cannot use: an unknown action
// and an invalid rewrite envelope are the two remaining fall-back shapes.
type unusableOrdinaryFin struct {
	order int
	calls int
	res   toolcall.Result
}

func (*unusableOrdinaryFin) ID() string { return "unusable-ordinary" }

func (f *unusableOrdinaryFin) Order() int { return f.order }

func (f *unusableOrdinaryFin) Finalize(
	_ context.Context,
	_ toolcall.CompletedCall,
	_ aipapi.ToolDef,
	_ []aipapi.ToolDef,
	_ toolcall.Meta,
) (toolcall.Result, error) {
	f.calls++
	return f.res, nil
}

// failingMandatoryFin declares a mandatory completeness requirement and then
// fails on its own invocation. It stands in for the blocker-3 case: the DECLARING
// finalizer itself produces no usable decision, so nothing may be released and
// the call is refused closed (requirements.md 8.3, 4.4).
type failingMandatoryFin struct {
	order int
	calls int
}

func (*failingMandatoryFin) ID() string { return "failing-mandatory" }

func (f *failingMandatoryFin) Order() int { return f.order }

func (f *failingMandatoryFin) ToolCallBufferingRequirement() toolcall.BufferingSpec {
	return toolcall.BufferingSpec{
		MaxArgsBytes: toolcall.DefaultMandatoryMaxArgsBytes,
		Overflow:     toolcall.OverflowReject,
	}
}

func (f *failingMandatoryFin) Finalize(
	_ context.Context,
	_ toolcall.CompletedCall,
	_ aipapi.ToolDef,
	_ []aipapi.ToolDef,
	_ toolcall.Meta,
) (toolcall.Result, error) {
	f.calls++
	return toolcall.Result{}, context.Canceled
}

// TestToolCallAssembler_UnrelatedFinalizerFailureCannotSkipMandatoryExpansion
// closes requirement 4.6's FAILURE clause, the complement of the LIMIT clause
// pinned above. An unrelated optional finalizer that fails before the declaring
// finalizer runs used to make finalizeCall replay the original fragments with
// err == nil — a silent path-expansion bypass — so the call is now refused
// closed instead.
//
// This is deliberately NOT a reordering rule: nothing about
// toolcall.MaterializeSorted or Finalizer.Order() changes, and an assembler with
// no declared requirement keeps the pre-existing replay fallback.
func TestToolCallAssembler_UnrelatedFinalizerFailureCannotSkipMandatoryExpansion(t *testing.T) {
	t.Parallel()

	const (
		ordinaryErrOrder = 10
		mandatoryOrder   = 20
	)

	// The mandatory declaration must survive being read a second time at
	// composition: this is a plain capability projection, never a request-state
	// decision.
	catalog := mandatoryCatalog()

	t.Run("unrelated_error_before_the_declaring_finalizer_refuses_closed", func(t *testing.T) {
		t.Parallel()
		ordinary := &failingOrdinaryFin{order: ordinaryErrOrder}
		var undeclared toolcall.Finalizer = ordinary
		if _, ok := undeclared.(toolcall.BufferingRequirement); ok {
			t.Fatal("fixture must be an unrelated finalizer without a declaration")
		}
		mand := newMandatoryExpansionFin()
		mand.order = mandatoryOrder
		a := newToolCallAssembler([]toolcall.Finalizer{mand, ordinary}, 0, catalog)
		if a == nil {
			t.Fatal("assembler must be constructed for a non-empty finalizer list and catalog")
		}
		if !a.mandatory.mandatoryBoundDeclared {
			t.Fatal("the mandatory declaration must arm the requirement 4.6 failure clause")
		}
		args := mandatoryArgsJSON(8 * 1024)

		released, err := streamMandatoryToolCall(t, a, "unrelated-err", args)

		// Requirement 4.6: nothing alias-bearing may reach the client.
		if released != "" {
			t.Fatalf("requirements.md 4.6 - an unrelated finalizer failure must not replay %d original argument bytes", len(released))
		}
		if strings.Contains(released, mandatoryVirtualRoot) {
			t.Fatal("requirements.md 4.6 - the reserved alias was released without expansion")
		}
		var mbe *MandatoryBufferingError
		if !errors.As(err, &mbe) || mbe == nil {
			t.Fatalf("requirements.md 4.6 - want a typed MandatoryBufferingError, got %v", err)
		}
		if !errors.Is(err, ErrMandatoryBuffering) || !IsMandatoryBufferingError(err) {
			t.Fatalf("refusal must classify through the sentinel, got %v", err)
		}
		if mbe.Reason != ReasonMandatoryBufferingIncomplete {
			t.Fatalf("reason: got %q want %q", mbe.Reason, ReasonMandatoryBufferingIncomplete)
		}
		if mbe.FinalizerID != mand.ID() {
			t.Fatalf("refusal must name the declaring finalizer: got %q want %q", mbe.FinalizerID, mand.ID())
		}
		if mbe.ToolCallID != "unrelated-err" {
			t.Fatalf("refusal must carry the refused call id: got %q", mbe.ToolCallID)
		}
		if mbe.MaxArgsBytes != 0 {
			t.Fatalf("no bound was exceeded, so MaxArgsBytes must stay zero: got %d", mbe.MaxArgsBytes)
		}
		if err.Error() != "tool call finalization: tool call assembler: mandatory buffering requirement not honored ("+
			ReasonMandatoryBufferingIncomplete+")" {
			t.Fatalf("refusal message must be the bounded, content-free classification: %q", err.Error())
		}
		// The failing finalizer really did run first, and the declaring one
		// really was skipped, so the refusal is caused by the ordering-neutral
		// fallback rather than by a fixture that never reached finalizeCall.
		if ordinary.calls != 1 {
			t.Fatalf("unrelated finalizer invocations=%d want 1", ordinary.calls)
		}
		if mand.calls != 0 {
			t.Fatalf("the declaring finalizer must not have decided, got %d invocations", mand.calls)
		}
	})

	t.Run("unusable_result_before_the_declaring_finalizer_refuses_closed", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name string
			res  toolcall.Result
		}{
			{
				name: "invalid_rewrite_envelope",
				res: toolcall.Result{
					Action:     toolcall.ActionRewrite,
					ToolName:   mandatoryToolName,
					ArgsJSON:   []byte(`{"path":`),
					ReasonCode: toolcall.ReasonValidPassThrough,
				},
			},
			{
				name: "unknown_action",
				res:  toolcall.Result{Action: toolcall.Action(999), ReasonCode: toolcall.ReasonValidPassThrough},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				ordinary := &unusableOrdinaryFin{order: ordinaryErrOrder, res: tc.res}
				mand := newMandatoryExpansionFin()
				mand.order = mandatoryOrder
				a := newToolCallAssembler([]toolcall.Finalizer{mand, ordinary}, 0, catalog)
				if a == nil {
					t.Fatal("assembler must be constructed for a non-empty finalizer list and catalog")
				}

				released, err := streamMandatoryToolCall(t, a, "unusable-"+tc.name, mandatoryArgsJSON(8*1024))
				if released != "" {
					t.Fatalf("requirements.md 4.6 - %d original argument bytes were replayed", len(released))
				}
				if !IsMandatoryBufferingError(err) {
					t.Fatalf("requirements.md 4.6 - want a typed MandatoryBufferingError, got %v", err)
				}
			})
		}
	})

	t.Run("declaring_finalizer_own_failure_refuses_closed", func(t *testing.T) {
		t.Parallel()
		// REPLACES `declaring_finalizer_failure_keeps_the_pre_existing_replay`,
		// which codified the defect the second adversarial review raised as
		// blocker 3. That characterization asserted the pre-existing error-free
		// replay when the DECLARING finalizer itself failed, on the ground that
		// the pending flag was cleared as soon as it was invoked. That is exactly
		// the hole: no requirement was satisfied, no mandatory-safe document
		// existed, and the alias-bearing ORIGINALS were released to tool policies
		// and the client. A requirement is satisfied only when its declarer
		// returns a usable Pass, Rewrite, or explicit Reject
		// (requirements.md 8.3, 4.4).
		//
		// `failingMandatoryFin` declares a well-formed mandatory bound and then
		// fails, which is the only way to reach this case.
		ordinary := &legacyOptOutFin{} // Order 0, rewrites, no declaration
		mand := &failingMandatoryFin{order: 10}
		a := newToolCallAssembler([]toolcall.Finalizer{ordinary, mand}, 0, catalog)
		if a == nil {
			t.Fatal("assembler must be constructed for a non-empty finalizer list and catalog")
		}
		if !a.mandatory.mandatoryBoundDeclared {
			t.Fatal("a declaring finalizer must arm the failure clause until it decides")
		}

		args := mandatoryArgsJSON(8 * 1024)
		released, err := streamMandatoryToolCall(t, a, "declaring-err", args)
		if released != "" {
			t.Fatalf("requirements.md 8.3 - the declaring finalizer's own failure released %d original argument bytes",
				len(released))
		}
		if strings.Contains(released, mandatoryVirtualRoot) {
			t.Fatal("requirements.md 8.3 - the reserved alias reached the client")
		}
		var mbe *MandatoryBufferingError
		if !errors.As(err, &mbe) || mbe == nil {
			t.Fatalf("requirements.md 8.3 - want a typed MandatoryBufferingError, got %T", err)
		}
		if mbe.Reason != ReasonMandatoryBufferingIncomplete {
			t.Fatalf("requirements.md 8.3 - reason: got %q want %q", mbe.Reason, ReasonMandatoryBufferingIncomplete)
		}
		if mbe.FinalizerID != mand.ID() {
			t.Fatalf("requirements.md 8.3 - the refusal must name the declarer that never decided: got %q want %q",
				mbe.FinalizerID, mand.ID())
		}
		if mand.calls != 1 {
			t.Fatalf("the declaring finalizer must have been invoked: got %d invocations", mand.calls)
		}
	})

	t.Run("without_a_declaration_the_replay_fallback_is_untouched", func(t *testing.T) {
		t.Parallel()
		// The control: with no declaration anywhere, an unrelated failure still
		// replays the originals error-free exactly as before.
		ordinary := &failingOrdinaryFin{order: ordinaryErrOrder}
		a := newToolCallAssembler([]toolcall.Finalizer{ordinary}, 0, catalog)
		if a == nil {
			t.Fatal("assembler must be constructed for a non-empty finalizer list and catalog")
		}
		if a.mandatory.mandatoryBoundDeclared {
			t.Fatal("a finalizer without a declaration must not arm the failure clause")
		}
		args := mandatoryArgsJSON(8 * 1024)
		released, err := streamMandatoryToolCall(t, a, "no-decl-err", args)
		if err != nil {
			t.Fatalf("no declared requirement means no assembler refusal: %v", err)
		}
		if released != args {
			t.Fatalf("released %d bytes, want the %d original bytes replayed unchanged", len(released), len(args))
		}
	})
}
