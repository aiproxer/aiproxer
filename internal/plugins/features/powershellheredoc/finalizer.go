package powershellheredoc

import (
	"context"
	"encoding/json"
	"maps"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

// finalizerOrder places this feature after tool-call repair (40), path
// virtualization expansion (41) and tool-name correction (45), so it inspects
// the effective call name and arguments those passes leave behind.
const finalizerOrder = 46

// maxCommandBytes bounds the command the feature inspects. Longer commands pass
// through unchanged.
const maxCommandBytes = 8 * 1024

// ReasonPowerShellHeredoc is the reason code of a steer this feature requests.
const ReasonPowerShellHeredoc = "powershell_heredoc"

// The two hidden instructions. They are static and never embed the command.
const (
	instructionPowerShell = "The proposed command uses Bash-style `<<` heredoc/here-string redirection, which PowerShell cannot parse; it has not executed. Rewrite it in PowerShell syntax (for example `@'...'@ | python -`, or `python -c`) and issue a new tool call that preserves the intended operation. Do not repeat the heredoc."
	instructionAuto       = "The proposed command has not executed: it combines an unquoted backslash Windows path with Bash-style `<<` heredoc redirection, which fails in PowerShell/cmd (heredoc) and in Bash (unquoted backslashes are stripped). Rewrite it for the shell this tool actually runs and issue a new tool call that preserves the intended operation."
)

// Finalizer is the stateless completed-call decision of the feature: it steers a
// mapped tool's call whose command cannot run as written, and passes every other
// call through unchanged. It is immutable after construction and safe for
// concurrent use.
type Finalizer struct {
	tools map[string]ToolRule
}

var _ toolcall.Finalizer = (*Finalizer)(nil)

// newFinalizer re-validates cfg and returns the finalizer it describes.
func newFinalizer(cfg Config) (*Finalizer, error) {
	if err := validateTools(cfg.Tools); err != nil {
		return nil, err
	}
	return &Finalizer{tools: maps.Clone(cfg.Tools)}, nil
}

// ID implements [toolcall.Finalizer].
func (f *Finalizer) ID() string { return ID }

// Order implements [toolcall.Finalizer].
func (f *Finalizer) Order() int { return finalizerOrder }

// Finalize implements [toolcall.Finalizer]. It steers only a call to a mapped
// tool whose command holds a heredoc redirection that the tool's shell mode
// rejects. Every other call passes: unmapped tools, arguments that are not a
// JSON object with a non-empty string command of at most 8 KiB, and commands
// with no match.
func (f *Finalizer) Finalize(
	_ context.Context,
	call toolcall.CompletedCall,
	_ aipapi.ToolDef,
	_ []aipapi.ToolDef,
	_ toolcall.Meta,
) (toolcall.Result, error) {
	pass := toolcall.Result{Action: toolcall.ActionPass, ReasonCode: toolcall.ReasonValidPassThrough}
	rule, ok := f.tools[call.ToolName]
	if !ok {
		return pass, nil
	}
	command, ok := commandArg(call.ArgsJSON, rule.Arg)
	if !ok {
		return pass, nil
	}
	match, windowsPathBefore := scanHeredoc(command)
	if !match {
		return pass, nil
	}
	var instruction string
	switch rule.Shell {
	case ShellPowerShell:
		instruction = instructionPowerShell
	case ShellAuto:
		if !windowsPathBefore {
			return pass, nil
		}
		instruction = instructionAuto
	default:
		return pass, nil
	}
	return toolcall.Result{
		Action:     toolcall.ActionSteer,
		ReasonCode: ReasonPowerShellHeredoc,
		Steer: &toolcall.SteerRequest{
			Instruction:         instruction,
			TurnBudget:          toolcall.MinSteerTurnBudget,
			AllowAfterReasoning: true,
		},
	}, nil
}

// commandArg returns the top-level string value of key in the JSON object args.
// It reports false for malformed JSON, a non-object, a missing or non-string
// value, an empty value, and a value over maxCommandBytes.
func commandArg(args []byte, key string) (string, bool) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(args, &obj); err != nil {
		return "", false
	}
	raw, ok := obj[key]
	if !ok {
		return "", false
	}
	var command string
	if err := json.Unmarshal(raw, &command); err != nil {
		return "", false
	}
	if command == "" || len(command) > maxCommandBytes {
		return "", false
	}
	return command, true
}
