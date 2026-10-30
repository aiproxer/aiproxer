package codex

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/connectors/codex/internal/routingstub"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const defaultCodexInstruction = "You are Codex, based on GPT-5. You are running as a coding agent in the Codex CLI on a user's computer."

// ExtToolStrict is the canonical-call extension key (bool) used by the codex
// client-compat feature to signal Hermes-marked calls. When false, tools are
// emitted with strict=false and parallel_tool_calls defaults to true.
const ExtToolStrict = "openai_codex.tool_strict"

// ExtIgnoreUnsupportedGenParams is the canonical-call extension key (bool). When
// true, temperature, top_p, and max_output_tokens are dropped instead of failing
// payload build; used by codex-client-compat for OpenCode and similar clients.
const ExtIgnoreUnsupportedGenParams = "openai_codex.ignore_unsupported_gen_params"

type Payload struct {
	Model              string            `json:"model"`
	Stream             bool              `json:"stream,omitempty"`
	Store              bool              `json:"store"`
	Instructions       string            `json:"instructions"`
	Input              []inputItem       `json:"input"`
	Tools              []toolPayload     `json:"tools,omitempty"`
	ToolChoice         string            `json:"tool_choice,omitempty"`
	Reasoning          *reasoningSpec    `json:"reasoning,omitempty"`
	Text               *textSpec         `json:"text,omitempty"`
	Include            []string          `json:"include,omitempty"`
	ParallelToolCalls  *bool             `json:"parallel_tool_calls,omitempty"`
	PromptCacheKey     string            `json:"prompt_cache_key,omitempty"`
	PreviousResponseID string            `json:"previous_response_id,omitempty"`
	Metadata           map[string]string `json:"metadata,omitempty"`
}

type reasoningSpec struct {
	// An empty effort must be omitted. The endpoint accepts its model default,
	// but does not accept an explicit empty string.
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type textSpec struct {
	Verbosity aipapi.VerbosityLevel `json:"verbosity,omitempty"`
}

// normalizeCodexModel strips client provider-namespace prefixes (e.g. OpenCode's
// "openai/<model>") that the Codex Responses API rejects.
func normalizeCodexModel(model string) string {
	return strings.TrimPrefix(strings.TrimSpace(model), "openai/")
}

func PayloadForCall(call *aipapi.Call, cand routingstub.AttemptCandidate, cfg Config) (Payload, error) {
	return payloadForCall(call, cand, cfg)
}

func payloadForCall(call *aipapi.Call, cand routingstub.AttemptCandidate, cfg Config) (Payload, error) {
	if call == nil {
		return Payload{}, fmt.Errorf("%s: nil call", ID)
	}
	callCopy := aipapi.CloneCall(*call)
	call = &callCopy
	model := normalizeCodexModel(cand.Primary.Model)
	if model == "" {
		return Payload{}, fmt.Errorf("%s: model is required", ID)
	}
	if err := validateUnsupportedGenParams(call); err != nil {
		return Payload{}, err
	}
	items, err := buildInputItems(call)
	if err != nil {
		return Payload{}, err
	}
	toolStrictDisabled := false
	if strict, ok := extensionBool(call, ExtToolStrict); ok {
		toolStrictDisabled = !strict
	}
	p := Payload{
		Model:        model,
		Stream:       true,
		Instructions: resolveCodexInstructions(call),
		Input:        items,
	}
	if len(call.Tools) > 0 {
		tools, err := buildTools(call.Tools, toolStrictDisabled)
		if err != nil {
			return Payload{}, err
		}
		p.Tools = tools
		// Codex accepts tool_choice only when callable tools are present. OpenCode
		// sends no-tools turns during compaction/continuation, and forwarding
		// tool_choice:auto in that state can make the upstream model behave as if a
		// tool protocol still exists. Keep the absence of tools explicit.
		p.ToolChoice = "auto"
		if toolStrictDisabled && p.ParallelToolCalls == nil {
			t := true
			p.ParallelToolCalls = &t
		}
	}
	p.Reasoning = applyReasoningRequestPolicy(call, model, cfg)
	verbosity, err := aipapi.ParseVerbosityLevel(string(call.Options.Verbosity))
	if err != nil {
		return Payload{}, fmt.Errorf("%s: request verbosity: %w", ID, err)
	}
	if verbosity == "" {
		verbosity, err = aipapi.ParseVerbosityLevel(string(cfg.DefaultVerbosity))
		if err != nil {
			return Payload{}, fmt.Errorf("%s: default verbosity: %w", ID, err)
		}
	}
	if verbosity != "" {
		p.Text = &textSpec{Verbosity: verbosity}
	}
	if p.Reasoning != nil {
		p.Include = []string{"reasoning.encrypted_content"}
	}
	if len(call.Tools) > 0 {
		if call.Options.ParallelToolCalls != nil {
			p.ParallelToolCalls = call.Options.ParallelToolCalls
		} else if p.ParallelToolCalls == nil {
			v := false
			p.ParallelToolCalls = &v
		}
	}
	return p, nil
}

func extensionBool(call *aipapi.Call, key string) (bool, bool) {
	if call == nil || call.Extensions == nil {
		return false, false
	}
	raw, ok := call.Extensions[key]
	if !ok {
		return false, false
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return false, false
	}
	return b, true
}

func validateUnsupportedGenParams(call *aipapi.Call) error {
	if ignore, ok := extensionBool(call, ExtIgnoreUnsupportedGenParams); ok && ignore {
		return nil
	}
	var unsupported []string
	if call.Options.Temperature != nil {
		unsupported = append(unsupported, "temperature")
	}
	if call.Options.TopP != nil {
		unsupported = append(unsupported, "top_p")
	}
	if call.Options.MaxOutputTokens != nil {
		unsupported = append(unsupported, "max_output_tokens")
	}
	if len(unsupported) == 0 {
		return nil
	}
	return fmt.Errorf("%s: unsupported generation parameter(s) %s (Codex Responses API); set extension %q to ignore",
		ID, strings.Join(unsupported, ", "), ExtIgnoreUnsupportedGenParams)
}
