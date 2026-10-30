package agentloopguard

import (
	"bytes"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controltool"
)

// completionToolName is the stable model-facing ABI for the explicit completion
// protocol. It is not operator-renamable in this version, and it is not derived
// from configuration, defaults, or environment.
const completionToolName = "attempt_completion"

// completionToolDescription is the normative model-facing description pinned by
// design.md "Frozen Tool Definition". `command` is intentionally absent from the
// schema: it would add side-effect semantics unrelated to completion and weaken
// the local-control boundary.
const completionToolDescription = "Call this only when all work requested by the user " +
	"for the current task is complete. The result must concisely summarize the completed work. " +
	"Do not call this while requested work remains that you can continue without additional user input."

// completionToolParameters is the normative input schema pinned by design.md
// "Frozen Tool Definition": exactly one required string `result`, additional
// properties rejected, and no `command` parameter.
const completionToolParameters = `{"type":"object","properties":{"result":{"type":"string",` +
	`"description":"Concise final result summarizing the work that has been completed."}},` +
	`"required":["result"],"additionalProperties":false}`

// completionInstruction is the normative byte-stable base instruction from
// design.md "Frozen Base Instruction". It is the exact UTF-8 literal with LF
// line endings, including the final LF before the closing fence. It must not be
// reflowed, trimmed, normalized, templated, localized, or regenerated, and it
// carries no timestamp, trace ID, attempt ID, counter, or other per-turn
// volatile data. Both projection points reuse these exact bytes; any change
// requires explicit compatibility review and a specification update.
const completionInstruction = "<task-completion-protocol>\n" +
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

// completionToolSpec returns the frozen model-facing contract for the explicit
// completion protocol: the canonical tool definition, the stable base
// instruction, and the bounded args budget.
//
// The projection is entirely literal and takes no configuration input, so the
// model-facing contract cannot drift per host, per turn, or per configuration.
// The schema bytes are copied on every call, so a caller that mutates the
// returned slice cannot alter a later projection.
//
// This function is the projection only. The concrete controltool.Provider,
// including the strict completion-argument handler, belongs to task 7.2.
func completionToolSpec() controltool.Spec {
	return controltool.Spec{
		Tool: aipapi.ToolDef{
			Name:        completionToolName,
			Description: completionToolDescription,
			Parameters:  bytes.Clone([]byte(completionToolParameters)),
		},
		Instruction:  controltool.Instruction{Role: aipapi.RoleSystem, Text: completionInstruction},
		MaxArgsBytes: controltool.DefaultMaxArgsBytes,
	}
}
