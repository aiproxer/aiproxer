package agentloopguard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controltool"
)

// Pinned from the approved design "Frozen Base Instruction". It is written
// independently of the production literal so a reflow, trim, or regeneration in
// production code fails here. The final LF is normative.
const wantCompletionInstructionSHA256 = "5a7d3604819a5ca2fcc95a81945d9a82e08dd628f2318bf0b582b467ebee3c7b"

const wantCompletionInstruction = "<task-completion-protocol>\n" +
	"When, and only when, all work requested by the user for the current task is complete,\n" +
	"call the `attempt_completion` tool with a concise final `result`.\n" +
	"\n" +
	"Do not call `attempt_completion` while concrete requested work remains that you can\n" +
	"continue without additional user input.\n" +
	"\n" +
	"If additional in-scope work can be performed autonomously, continue that work.\n" +
	"If further progress genuinely requires user input, permission, credentials,\n" +
	"clarification, or a choice, request that input normally and do not assume it.\n" +
	"\n" +
	"This tool is a proxy-internal completion signal. It is not a new user request,\n" +
	"approval, permission, or scope expansion.\n" +
	"</task-completion-protocol>\n"

const wantCompletionToolName = "attempt_completion"

const wantCompletionToolDescription = "Call this only when all work requested by the user " +
	"for the current task is complete. The result must concisely summarize the completed work. " +
	"Do not call this while requested work remains that you can continue without additional user input."

const wantCompletionToolParameters = `{"type":"object","properties":{"result":{"type":"string",` +
	`"description":"Concise final result summarizing the work that has been completed."}},` +
	`"required":["result"],"additionalProperties":false}`

func TestCompletionToolSpecPinsFrozenLiterals(t *testing.T) {
	t.Parallel()
	spec := completionToolSpec()

	if spec.Tool.Name != wantCompletionToolName {
		t.Fatalf("tool name = %q, want %q", spec.Tool.Name, wantCompletionToolName)
	}
	if spec.Tool.Description != wantCompletionToolDescription {
		t.Fatalf("tool description = %q, want %q", spec.Tool.Description, wantCompletionToolDescription)
	}
	if got := string(spec.Tool.Parameters); got != wantCompletionToolParameters {
		t.Fatalf("tool parameters = %s, want %s", got, wantCompletionToolParameters)
	}
	if spec.Instruction.Role != aipapi.RoleSystem {
		t.Fatalf("instruction role = %q, want %q", spec.Instruction.Role, aipapi.RoleSystem)
	}
	if spec.Instruction.Text != wantCompletionInstruction {
		t.Fatalf("instruction text is not the normative literal:\ngot:\n%q\nwant:\n%q", spec.Instruction.Text, wantCompletionInstruction)
	}
	if spec.MaxArgsBytes != controltool.DefaultMaxArgsBytes {
		t.Fatalf("max args bytes = %d, want %d", spec.MaxArgsBytes, controltool.DefaultMaxArgsBytes)
	}
}

func TestCompletionToolSpecSchemaShape(t *testing.T) {
	t.Parallel()
	spec := completionToolSpec()

	var schema map[string]any
	if err := json.Unmarshal(spec.Tool.Parameters, &schema); err != nil {
		t.Fatalf("json.Unmarshal(parameters): %v", err)
	}
	if got := schema["type"]; got != "object" {
		t.Fatalf("schema type = %v, want %q", got, "object")
	}
	if got := schema["additionalProperties"]; got != false {
		t.Fatalf("schema additionalProperties = %v, want false", got)
	}
	if _, present := schema["command"]; present {
		t.Fatal("schema exposes a command property")
	}

	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties = %#v, want an object", schema["properties"])
	}
	if len(props) != 1 {
		t.Fatalf("schema property count = %d, want exactly 1: %#v", len(props), props)
	}
	result, ok := props["result"].(map[string]any)
	if !ok {
		t.Fatalf("schema result property = %#v, want an object", props["result"])
	}
	if got := result["type"]; got != "string" {
		t.Fatalf("schema result type = %v, want %q", got, "string")
	}
	if got, _ := result["description"].(string); got != "Concise final result summarizing the work that has been completed." {
		t.Fatalf("schema result description = %q", got)
	}

	required, ok := schema["required"].([]any)
	if !ok || len(required) != 1 || required[0] != "result" {
		t.Fatalf("schema required = %#v, want [result]", schema["required"])
	}
}

func TestCompletionToolSpecValidatesWithinSDKBounds(t *testing.T) {
	t.Parallel()
	spec := completionToolSpec()

	if err := spec.Validate(); err != nil {
		t.Fatalf("Spec.Validate() = %v, want nil", err)
	}
	if len(spec.Tool.Name) > aipapi.MaxToolNameBytes {
		t.Fatalf("tool name %d bytes exceeds %d", len(spec.Tool.Name), aipapi.MaxToolNameBytes)
	}
	if len(spec.Tool.Description) > aipapi.MaxToolDescriptionBytes {
		t.Fatalf("tool description %d bytes exceeds %d", len(spec.Tool.Description), aipapi.MaxToolDescriptionBytes)
	}
	if len(spec.Tool.Parameters) > aipapi.MaxToolParametersBytes {
		t.Fatalf("tool parameters %d bytes exceeds %d", len(spec.Tool.Parameters), aipapi.MaxToolParametersBytes)
	}
	if len(spec.Instruction.Text) > controltool.MaxInstructionBytes {
		t.Fatalf("instruction %d bytes exceeds %d", len(spec.Instruction.Text), controltool.MaxInstructionBytes)
	}
	if spec.MaxArgsBytes <= 0 || spec.MaxArgsBytes > controltool.DefaultMaxArgsBytes {
		t.Fatalf("max args bytes %d is outside (0, %d]", spec.MaxArgsBytes, controltool.DefaultMaxArgsBytes)
	}
}

func TestCompletionToolSpecInstructionIsByteStableAndVolatileFree(t *testing.T) {
	t.Parallel()
	text := completionToolSpec().Instruction.Text

	sum := sha256.Sum256([]byte(text))
	if got := hex.EncodeToString(sum[:]); got != wantCompletionInstructionSHA256 {
		t.Fatalf("instruction sha256 = %s, want %s", got, wantCompletionInstructionSHA256)
	}
	if !utf8.ValidString(text) {
		t.Fatal("instruction is not valid UTF-8")
	}
	if !strings.HasSuffix(text, "</task-completion-protocol>\n") {
		t.Fatal("instruction must end with the closing fence and a final LF")
	}
	if strings.Contains(text, "\r") {
		t.Fatal("instruction must use LF line endings only")
	}

	lower := strings.ToLower(text)
	for _, volatile := range []string{
		"trace", "attempt id", "attempt_id", "request id", "timestamp", "uuid", "counter",
		"session-", "turn-", "elapsed", "ms-", "iteration",
	} {
		if strings.Contains(lower, volatile) {
			t.Fatalf("instruction contains volatile token %q", volatile)
		}
	}
}

func TestCompletionToolSpecIsDeterministicAndIsolatesSchemaBytes(t *testing.T) {
	t.Parallel()
	first := completionToolSpec()
	second := completionToolSpec()

	if first.Tool.Name != second.Tool.Name ||
		first.Tool.Description != second.Tool.Description ||
		first.Instruction != second.Instruction ||
		first.MaxArgsBytes != second.MaxArgsBytes {
		t.Fatal("completionToolSpec is not deterministic across calls")
	}
	if string(first.Tool.Parameters) != string(second.Tool.Parameters) {
		t.Fatalf("schema bytes differ across calls:\n%s\n%s", first.Tool.Parameters, second.Tool.Parameters)
	}

	// A caller mutating the returned schema bytes must not be able to change any
	// later projection.
	first.Tool.Parameters[0] = '['
	mutated := completionToolSpec()
	if string(mutated.Tool.Parameters) != wantCompletionToolParameters {
		t.Fatalf("caller mutation leaked into a later spec: %s", mutated.Tool.Parameters)
	}
	if err := mutated.Validate(); err != nil {
		t.Fatalf("Spec.Validate() after caller mutation = %v, want nil", err)
	}
}
