package backendplugin_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestInvocationValidate_rejectsMalformedTool(t *testing.T) {
	t.Parallel()
	inv := validLegacyInvocation(t)
	inv.Tools = []backendplugin.ToolDef{{Name: "", ParametersJSON: backendplugin.RawJSONAbsentValue()}}
	if err := inv.Validate(); err == nil {
		t.Fatal("expected empty tool name rejection")
	}
}

func TestInvocationValidate_rejectsInvalidToolChoiceRequiredWithoutTools(t *testing.T) {
	t.Parallel()
	required := "required"
	inv := validLegacyInvocation(t)
	inv.ToolChoice = &required
	if err := inv.Validate(); err == nil {
		t.Fatal("expected tool choice validation failure")
	}
}

func TestInvocationValidate_rejectsOutOfRangeTemperature(t *testing.T) {
	t.Parallel()
	hot := int32(5000)
	inv := validLegacyInvocation(t)
	inv.Options.TemperatureMillis = &hot
	if err := inv.Validate(); err == nil {
		t.Fatal("expected temperature rejection")
	}
}

func TestInvocationValidate_rejectsWrongDialectSliceKind(t *testing.T) {
	t.Parallel()
	inv := validItemInvocation(t)
	inv.ProtocolRequirements = backendplugin.ProtocolRequirements{
		ItemDialects: []backendplugin.DialectRequirementDTO{{Kind: "reasoning", Dialect: "x"}},
	}
	if err := inv.Validate(); err == nil {
		t.Fatal("expected wrong dialect slice kind rejection")
	} else if !errors.Is(err, backendplugin.ErrInvalidInvocation) {
		t.Fatalf("err=%v", err)
	}
}

func TestInvocationValidate_rejectsDuplicateExtensionRequirement(t *testing.T) {
	t.Parallel()
	inv := validItemInvocation(t)
	inv.ProtocolRequirements = backendplugin.ProtocolRequirements{
		ExtensionTypes: []backendplugin.ExtensionRequirementDTO{
			{Namespace: "ns", Type: "alpha"},
			{Namespace: "ns", Type: "alpha"},
		},
	}
	if err := inv.Validate(); err == nil {
		t.Fatal("expected duplicate extension rejection")
	}
}

func validLegacyInvocation(t *testing.T) backendplugin.Invocation {
	t.Helper()
	return backendplugin.Invocation{
		RequestID: "r", AttemptID: "a", ALegID: "aleg", BLegID: "bleg", CanonicalModelID: "m",
		Messages: []backendplugin.Message{{
			Role:  backendplugin.RoleUser,
			Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: strPtr("hi")}},
		}},
		Options: backendplugin.GenerationOptions{ResponseSchemaJSON: backendplugin.RawJSONAbsentValue()},
	}
}

func validItemInvocation(t *testing.T) backendplugin.Invocation {
	t.Helper()
	inv := validLegacyInvocation(t)
	inv.ItemAuthority = true
	inv.Items = []backendplugin.InvocationItem{{
		Kind: "message", ID: "m1", Status: string(aipapi.ItemStatusCompleted), Role: backendplugin.RoleUser,
		Content: []backendplugin.InvocationContentPart{{Kind: backendplugin.PartKindText, Text: strPtr("hi")}},
	}}
	inv.Messages = nil
	return inv
}

func TestInvocationValidate_rejectsOversizedClientFrame(t *testing.T) {
	t.Parallel()
	huge := strings.Repeat("x", int(backendplugin.DefaultMaxStreamFrameBytes)+1)
	inv := validLegacyInvocation(t)
	inv.Messages[0].Parts[0].Text = &huge
	frame := backendplugin.ClientFrame{
		Kind: backendplugin.ClientFrameStart, InstanceID: "i1", Invocation: &inv,
	}
	if err := backendplugin.ValidateClientFrameBounds(frame); !errors.Is(err, backendplugin.ErrOversizedMessage) {
		t.Fatalf("err=%v", err)
	}
}

func TestForwardExecute_oversizedServerFrameNotSent(t *testing.T) {
	t.Parallel()
	// ValidateServerFrameBounds is exercised by sendServerFrame; oversized event must fail before transport.
	huge := strings.Repeat("y", int(backendplugin.DefaultMaxStreamFrameBytes)+1)
	frame := backendplugin.ServerFrame{
		Kind: backendplugin.ServerFrameEvent, Sequence: 1,
		Event: &backendplugin.CanonicalEvent{Kind: backendplugin.EventTextDelta, Delta: &huge},
	}
	if err := backendplugin.ValidateServerFrameBounds(frame); !errors.Is(err, backendplugin.ErrOversizedMessage) {
		t.Fatalf("err=%v", err)
	}
}
