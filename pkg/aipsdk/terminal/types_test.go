package terminal_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

func TestState_IsKnown(t *testing.T) {
	t.Parallel()
	for _, s := range []terminal.State{
		terminal.StateOpen, terminal.StateTerminalizing, terminal.StateWorkPending,
		terminal.StateSettled, terminal.StateReleasePending, terminal.StateReleased, terminal.StateFailed,
	} {
		if !s.IsKnown() {
			t.Fatalf("state %q must be known", s)
		}
		if err := s.Validate(); err != nil {
			t.Fatalf("state %q Validate: %v", s, err)
		}
	}
	if terminal.State("bogus").IsKnown() {
		t.Fatal("unknown state must not be known")
	}
}

func TestCommand_AllKnownAndRetryFlag(t *testing.T) {
	t.Parallel()
	cmds := terminal.AllCommands()
	if len(cmds) != 13 {
		t.Fatalf("expected 13 commands, got %d", len(cmds))
	}
	for _, c := range cmds {
		if !c.IsKnown() {
			t.Fatalf("command %q must be known", c)
		}
		if err := c.Validate(); err != nil {
			t.Fatalf("command %q Validate: %v", c, err)
		}
		code := terminal.OutcomeCodeFor(c)
		if !code.IsKnown() {
			t.Fatalf("outcome for %q must be known, got %q", c, code)
		}
	}
	if !terminal.CommandGateReplacement.IsRetryOrReplacement() {
		t.Fatal("gate_replacement must be retry/replacement")
	}
	if terminal.CommandClose.IsRetryOrReplacement() {
		t.Fatal("close must not be retry/replacement")
	}
}

func TestWorkKindAndState_Contracts(t *testing.T) {
	t.Parallel()
	if len(terminal.AllWorkKinds()) != 8 {
		t.Fatalf("expected 8 work kinds, got %d", len(terminal.AllWorkKinds()))
	}
	if len(terminal.AllWorkStates()) != 6 {
		t.Fatalf("expected 6 work states, got %d", len(terminal.AllWorkStates()))
	}
	if !terminal.WorkKindSettleRequestProvider.RequiresProvider() {
		t.Fatal("settle_request_provider requires provider")
	}
	if terminal.WorkKindAppendFact.RequiresProvider() {
		t.Fatal("append_fact must not require provider")
	}
	if !terminal.WorkStateCompleted.IsTerminal() || !terminal.WorkStateQuarantined.IsTerminal() {
		t.Fatal("completed and quarantined must be terminal")
	}
	if terminal.WorkStatePending.IsTerminal() {
		t.Fatal("pending must not be terminal")
	}
}

func TestScope_Validate(t *testing.T) {
	t.Parallel()
	if err := terminal.ScopeRequest.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := terminal.ScopeAttempt.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := terminal.Scope("other").Validate(); err == nil {
		t.Fatal("unknown scope must fail Validate")
	}
}
