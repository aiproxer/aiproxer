package execerr_test

// Spec: b-leg-path-virtualization Task 12.1, requirements.md 5.8, 7.1 and 8.1
// (the deferred wire mapping recorded in tasks.md "Task 7.2 DEFERRED, WIRE MAPPING"),
// against design.md "Error Handling".
//
// The tool-call assembler refuses exactly one completed tool call closed when a finalizer
// published a mandatory completeness requirement the assembler cannot honor
// (requirements.md 4.5, 4.6, 8.3). That refusal is a client-visible decision about the
// client's own tool call, and it used to reach the wire as `internal error` because
// `ClassifyExecute` had no branch for it: a client could not tell "the proxy will not run
// this tool call for you" from "the proxy broke". This file pins the mapping.
//
// Four properties are asserted, and the fourth is what makes the first three mean
// something:
//
//	THE WIRE SHAPE. The refusal classifies as a client reject with one stable status,
//	    NOT as the internal-error fallthrough, and its message is a compile-time constant
//	    of this package rather than any part of the error.
//
//	THE CLOSED REASON SET. The assembler's three bounded refusal codes map to three
//	    DISTINCT constants, so a client can tell an overflow from a misconfigured
//	    declaration from an undecided requirement without reading prose.
//
//	CONTENT FREEDOM. The refusal's own content-bearing fields - the finalizer identity,
//	    the tool-call identity, the exceeded byte bound, and the reason string itself -
//	    cannot reach the wire. The reason string matters most: it is an exported `string`
//	    field, so rendering it would publish whatever a caller assembled into it.
//
//	NO WIDENING. An unrelated error still classifies as the internal error it always
//	    was, so this is one added branch and not a re-taxonomy.

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/execerr"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// The hostile content every refusal below carries at once. A projection that leaked
// nothing because it published nothing would pass a message-only assertion trivially, so
// the values are non-empty, mutually distinct, and long enough that a substring search
// over the rendered message could not match by accident.
const (
	mandatoryBufferingFinalizerID = "path-virtualization-expansion-finalizer"
	mandatoryBufferingToolCallID  = "call_7f3a2b19e04c4d5f"
	mandatoryBufferingMaxArgs     = 1048576
	// mandatoryBufferingHostileReason is a reason string this build does not define. It
	// is spelled like a leak so a projection that echoed the field would be caught by the
	// same assertion that covers the three real codes.
	mandatoryBufferingHostileReason = "/home/dev/private/.__aip_v1__/w_abcdefghijklmnopqrst"
)

// mandatoryBufferingCase is one bounded refusal code and the constant it must render as.
type mandatoryBufferingCase struct {
	// name is the bounded case name. It never carries the reason value itself.
	name string
	// reason is the assembler's own bounded refusal code.
	reason string
	// message is the compile-time constant this package must publish for it.
	message string
}

// mandatoryBufferingCases is the closed vocabulary, as the assembler's three constants
// paired with this package's three constants. It is the whole mapping: a fourth code this
// build defines cannot exist without a fifth row here, and the file's fallback case below
// covers every value outside it.
var mandatoryBufferingCases = []mandatoryBufferingCase{
	{
		name:    "overflow",
		reason:  runtime.ReasonMandatoryBufferingOverflow,
		message: execerr.MandatoryBufferingOverflowWireMessage,
	},
	{
		name:    "declaration_invalid",
		reason:  runtime.ReasonMandatoryBufferingDeclarationInvalid,
		message: execerr.MandatoryBufferingDeclarationInvalidWireMessage,
	},
	{
		name:    "incomplete",
		reason:  runtime.ReasonMandatoryBufferingIncomplete,
		message: execerr.MandatoryBufferingIncompleteWireMessage,
	},
}

// newMandatoryBufferingRefusal builds one hostile refusal carrying the given bounded code.
func newMandatoryBufferingRefusal(reason string) *runtime.MandatoryBufferingError {
	return &runtime.MandatoryBufferingError{
		Reason:       reason,
		FinalizerID:  mandatoryBufferingFinalizerID,
		ToolCallID:   mandatoryBufferingToolCallID,
		MaxArgsBytes: mandatoryBufferingMaxArgs,
	}
}

// assertMandatoryBufferingContentFree fails when a rendered wire message carries any part
// of the refusal's own content. It is called for every case rather than asserted once, so
// the guarantee is per-code and not merely per-file.
func assertMandatoryBufferingContentFree(t *testing.T, label, message string) {
	t.Helper()
	for _, forbidden := range []string{
		mandatoryBufferingFinalizerID,
		mandatoryBufferingToolCallID,
		mandatoryBufferingHostileReason,
		"1048576",
		"w_abcdefghijklmnopqrst",
		".__aip_v1__",
		"/home/dev/private",
	} {
		if strings.Contains(message, forbidden) {
			t.Errorf("%s: wire message carries refusal content %q: %q", label, forbidden, message)
		}
	}
}

// TestClassifyExecute_mandatoryBufferingRefusalIsAClientReject is the wire shape.
//
// The refusal is a decision about the client's own tool call, so it is a client reject
// with the status this package already uses for one, and NOT the internal-error
// fallthrough it used to be. The message is a compile-time constant, and the original
// error is still handed back for server-side logging.
func TestClassifyExecute_mandatoryBufferingRefusalIsAClientReject(t *testing.T) {
	t.Parallel()
	for _, tc := range mandatoryBufferingCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			refusal := newMandatoryBufferingRefusal(tc.reason)
			out := execerr.ClassifyExecute(refusal)
			if out.Kind != execerr.KindClientReject {
				t.Errorf("kind = %v, want KindClientReject", out.Kind)
			}
			if out.Status != http.StatusBadRequest {
				t.Errorf("status = %d, want %d", out.Status, http.StatusBadRequest)
			}
			if out.Message != tc.message {
				t.Errorf("message = %q, want %q", out.Message, tc.message)
			}
			// The internal-error fallthrough is what this mapping replaces, so asserting
			// the refusal does NOT read as an internal fault is the substantive claim; a
			// test that only checked "not empty" would have passed before the fix.
			if out.Message == execerr.InternalWireMessage {
				t.Errorf("message = %q, which is the internal-error fallthrough", out.Message)
			}
			if out.Kind == execerr.KindInternalError {
				t.Error("the refusal is still classified as an internal fault")
			}
			if !errors.Is(out.Err, runtime.ErrMandatoryBuffering) {
				t.Errorf("Err must unwrap to ErrMandatoryBuffering, got %v", out.Err)
			}
			if !runtime.IsMandatoryBufferingError(out.Err) {
				t.Error("Err must remain classifiable as a mandatory-buffering refusal")
			}
		})
	}
}

// TestClassifyExecute_mandatoryBufferingRefusalCarriesNoRefusalContent is requirement
// 7.7 at this seam: the refusal's own fields are content and none of them may reach a
// client.
//
// The reason field is the one that needs stating twice. It is a compile-time constant
// wherever the assembler builds it, but it is an exported `string` on an exported struct,
// so the projection cannot assume its value: it has to SELECT a constant rather than
// render the field. The hostile-reason subtest is what proves it does.
func TestClassifyExecute_mandatoryBufferingRefusalCarriesNoRefusalContent(t *testing.T) {
	t.Parallel()
	for _, tc := range mandatoryBufferingCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := execerr.ClassifyExecute(newMandatoryBufferingRefusal(tc.reason))
			assertMandatoryBufferingContentFree(t, tc.name, out.Message)
			if strings.ContainsAny(out.Message, "\n\r\t") {
				t.Errorf("%s: wire message must be control-free: %q", tc.name, out.Message)
			}
			if len(out.Message) > aipapi.MaxClientMessageBytes {
				t.Errorf("%s: wire message is %d bytes, over the %d-byte client bound",
					tc.name, len(out.Message), aipapi.MaxClientMessageBytes)
			}
		})
	}

	t.Run("a_reason_this_build_does_not_define_falls_back_to_a_constant", func(t *testing.T) {
		t.Parallel()
		out := execerr.ClassifyExecute(newMandatoryBufferingRefusal(mandatoryBufferingHostileReason))
		if out.Kind != execerr.KindClientReject {
			t.Errorf("kind = %v, want KindClientReject", out.Kind)
		}
		if out.Message != execerr.MandatoryBufferingWireMessage {
			t.Errorf("message = %q, want the bounded fallback %q",
				out.Message, execerr.MandatoryBufferingWireMessage)
		}
		assertMandatoryBufferingContentFree(t, "hostile_reason", out.Message)
	})

	t.Run("an_empty_reason_falls_back_to_a_constant", func(t *testing.T) {
		t.Parallel()
		out := execerr.ClassifyExecute(newMandatoryBufferingRefusal(""))
		if out.Message != execerr.MandatoryBufferingWireMessage {
			t.Errorf("message = %q, want the bounded fallback %q",
				out.Message, execerr.MandatoryBufferingWireMessage)
		}
	})
}

// TestClassifyExecute_mandatoryBufferingRefusalIsRecognizedThroughWrapping is the
// production shape.
//
// The assembler wraps its typed refusal at the chokepoint that produced it, and every
// layer above adds its own context. A branch that matched only the bare type would be
// dead code, so the classification is driven through the same wrapping the runtime
// applies, twice.
func TestClassifyExecute_mandatoryBufferingRefusalIsRecognizedThroughWrapping(t *testing.T) {
	t.Parallel()
	refusal := newMandatoryBufferingRefusal(runtime.ReasonMandatoryBufferingOverflow)
	wrapped := fmt.Errorf("tool call finalization: %w", refusal)
	deeper := fmt.Errorf("executor submit upstream=http://internal-host:9090/v1 token=sekret: %w", wrapped)

	for label, err := range map[string]error{"wrapped": wrapped, "wrapped_twice": deeper} {
		out := execerr.ClassifyExecute(err)
		if out.Kind != execerr.KindClientReject {
			t.Errorf("%s: kind = %v, want KindClientReject", label, out.Kind)
		}
		if out.Message != execerr.MandatoryBufferingOverflowWireMessage {
			t.Errorf("%s: message = %q, want %q", label, out.Message,
				execerr.MandatoryBufferingOverflowWireMessage)
		}
		assertMandatoryBufferingContentFree(t, label, out.Message)
		if strings.Contains(out.Message, "internal-host") || strings.Contains(out.Message, "sekret") {
			t.Errorf("%s: wire message leaked wrapped detail: %q", label, out.Message)
		}
	}
}

// TestClassifyExecute_mandatoryBufferingMappingIsTotalAndNotWidening pins the two ends
// of the new branch.
//
// Every assembler reason resolves to a constant, so the branch cannot publish an empty
// message the way a partial switch would; and every distinct reason resolves to a
// DISTINCT constant, so the mapping discriminates rather than collapsing into one answer
// that would be no better than the single internal-error string it replaces.
func TestClassifyExecute_mandatoryBufferingMappingIsTotalAndNotWidening(t *testing.T) {
	t.Parallel()
	seen := map[string]string{}
	for _, tc := range mandatoryBufferingCases {
		out := execerr.ClassifyExecute(newMandatoryBufferingRefusal(tc.reason))
		if out.Message == "" {
			t.Errorf("%s: the branch published an empty message", tc.name)
		}
		if previous, clash := seen[out.Message]; clash {
			t.Errorf("%s: renders the same wire message as %s, so the client cannot tell them apart",
				tc.name, previous)
		}
		seen[out.Message] = tc.name
	}

	// Not widening: an unrelated error is still the internal error it always was, and the
	// nil-error case keeps its own documented answer.
	unrelated := errors.New("some unrelated executor failure")
	if out := execerr.ClassifyExecute(unrelated); out.Kind != execerr.KindInternalError ||
		out.Message != execerr.InternalWireMessage {
		t.Errorf("an unrelated error changed classification: kind=%v message=%q", out.Kind, out.Message)
	}
	if runtime.IsMandatoryBufferingError(nil) {
		t.Error("a nil error must not classify as a mandatory-buffering refusal")
	}
	if out := execerr.ClassifyExecute(nil); out.Kind != execerr.KindInternalError ||
		out.Message != execerr.UnknownExecuteErrorMessage {
		t.Errorf("a nil error changed classification: kind=%v message=%q", out.Kind, out.Message)
	}
}
