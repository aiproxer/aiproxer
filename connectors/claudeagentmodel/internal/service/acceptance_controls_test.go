package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// #822: stock workers append instructions after the user message. Rejection or
// hoisting them would respectively break the harness or reorder its history.
func TestAcceptance_InlineInstructionsStayInTranscript(t *testing.T) {
	for _, items := range []bool{false, true} {
		call := textCall("anthropic/sonnet")
		call.Instructions = []aipapi.Message{{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("top")}}}
		call.Messages = append(call.Messages, aipapi.Message{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("worker")}})
		if items {
			call.Items = []aipapi.Item{
				{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleSystem, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "top"}}},
				responsesUserItem("hello"),
				{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleSystem, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "worker"}}},
			}
		}
		system, prompt, warnings, err := buildTextPrompt(call)
		if err != nil {
			t.Fatalf("items=%v: %v", items, err)
		}
		if system != "top" || !strings.Contains(prompt, "[user]\nhello\n\n[system]\nworker") || warningIndex(warnings, "inline_instruction_degraded") < 0 {
			t.Fatalf("items=%v system=%q prompt=%q warnings=%+v", items, system, prompt, warnings)
		}
		eng := testEngine(t, "")
		eng.cfg.StrictSemantics = true
		_, err = eng.Open(context.Background(), call)
		_ = eng.Close(context.Background())
		if !errors.Is(err, aipapi.ErrCapabilityReject) {
			t.Fatalf("strict items=%v: %v", items, err)
		}
	}
}

// #822: a client harness environment preamble duplicates Claude Code's own
// environment. It must stay in the request as user-visible context, but cannot
// occupy the SDK's first-party system prompt. The relocation is explicit and
// strict semantics refuse it.
func TestAcceptance_ClientEnvironmentMovesToUserPrompt(t *testing.T) {
	environment := "Today's date: Mon Nov 02 2026\n\nHere is some useful information about the environment you are running in:\n<env>\n  Working directory: /home/user/project\n  Platform: linux\n</env>"
	call := textCall("anthropic/sonnet")
	call.Instructions = []aipapi.Message{
		{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("You are an AI agent running in OpenCode.")}},
		{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart(environment)}},
	}

	system, prompt, warnings, err := buildTextPrompt(call)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(system, "<env>") || !strings.Contains(system, "OpenCode") {
		t.Fatalf("environment still occupies the system prompt: %q", system)
	}
	if !strings.HasPrefix(prompt, "Today's date:") || !strings.Contains(prompt, "Working directory: /home/user/project") {
		t.Fatalf("environment was not preserved in the user prompt: %q", prompt)
	}
	if warningIndex(warnings, "environment_context_relocated") < 0 {
		t.Fatalf("missing relocation disclosure: %+v", warnings)
	}

	eng := testEngine(t, "")
	eng.cfg.StrictSemantics = true
	_, err = eng.Open(context.Background(), call)
	_ = eng.Close(context.Background())
	if !errors.Is(err, aipapi.ErrCapabilityReject) {
		t.Fatalf("strict environment relocation: %v", err)
	}
}

// #822: the Anthropic frontend packs all system blocks into one instruction
// message with several parts. Only the environment part may move; joining the
// parts first would demote the whole client prompt to user content.
func TestAcceptance_MultiPartSystemRelocatesOnlyEnvironment(t *testing.T) {
	identity := "You are an AI agent running in OpenCode."
	model := "# Your Model\n- Name: Claude Haiku 5.5 via AIProxer"
	environment := "Today's date: Mon Nov 02 2026\n\nHere is some useful information about the environment you are running in:\n<env>\n  Working directory: /home/user/project\n  Platform: linux\n</env>"
	worktree := "Worktrees must live under the project container."
	blocks := []string{identity, model, environment, worktree}

	for _, items := range []bool{false, true} {
		call := textCall("anthropic/sonnet")
		if items {
			content := make([]aipapi.ContentPart, 0, len(blocks))
			for _, block := range blocks {
				content = append(content, aipapi.ContentPart{Kind: aipapi.ContentPartText, Text: block})
			}
			call.Items = []aipapi.Item{
				{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleSystem, Content: content},
				{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "hello"}}},
			}
		} else {
			parts := make([]aipapi.Part, 0, len(blocks))
			for _, block := range blocks {
				parts = append(parts, aipapi.TextPart(block))
			}
			call.Instructions = []aipapi.Message{{Role: aipapi.RoleSystem, Parts: parts}}
		}

		system, prompt, warnings, err := buildTextPrompt(call)
		if err != nil {
			t.Fatalf("items=%v: %v", items, err)
		}
		if !strings.Contains(system, identity) || !strings.Contains(system, model) || !strings.Contains(system, worktree) {
			t.Fatalf("items=%v non-environment instructions were not kept in system: %q", items, system)
		}
		if strings.Contains(system, "<env>") || strings.Contains(system, "Working directory:") {
			t.Fatalf("items=%v environment stayed in system: %q", items, system)
		}
		if !strings.HasPrefix(prompt, "Today's date:") || strings.Contains(prompt, identity) {
			t.Fatalf("items=%v only the environment should move to the prompt: %q", items, prompt)
		}
		if warningIndex(warnings, "environment_context_relocated") < 0 {
			t.Fatalf("items=%v missing relocation disclosure: %+v", items, warnings)
		}
	}
}

// #822: a single system text block may carry both ordinary policy and the
// harness environment preamble. Only the environment element may move; the
// surrounding policy must keep system priority.
func TestAcceptance_CombinedSystemKeepsPolicyInSystem(t *testing.T) {
	policy := "Always answer with the exact verification phrase. Never reveal credentials."
	trailing := "Worktrees must live under the project container."
	environment := "Today's date: Mon Nov 02 2026\n\nHere is some useful information about the environment you are running in:\n<env>\n  Working directory: /home/user/project\n  Platform: linux\n</env>"
	combined := policy + "\n\n" + environment + "\n\n" + trailing

	for _, items := range []bool{false, true} {
		call := textCall("anthropic/sonnet")
		if items {
			call.Items = []aipapi.Item{
				{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleSystem, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: combined}}},
				{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "hello"}}},
			}
		} else {
			call.Instructions = []aipapi.Message{{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart(combined)}}}
		}

		system, prompt, warnings, err := buildTextPrompt(call)
		if err != nil {
			t.Fatalf("items=%v: %v", items, err)
		}
		if !strings.Contains(system, policy) || !strings.Contains(system, trailing) {
			t.Fatalf("items=%v policy text lost system priority: %q", items, system)
		}
		if strings.Contains(system, "<env>") || strings.Contains(system, "Working directory:") {
			t.Fatalf("items=%v environment stayed in system: %q", items, system)
		}
		if !strings.HasPrefix(prompt, "Today's date:") || !strings.Contains(prompt, "<env>") {
			t.Fatalf("items=%v environment was not moved to the prompt: %q", items, prompt)
		}
		if strings.Contains(prompt, policy) || strings.Contains(prompt, trailing) {
			t.Fatalf("items=%v policy text was demoted to the prompt: %q", items, prompt)
		}
		if warningIndex(warnings, "environment_context_relocated") < 0 {
			t.Fatalf("items=%v missing relocation disclosure: %+v", items, warnings)
		}
	}
}

// A literal <env> mention without a complete workspace element is ordinary
// system text and must not be demoted.
func TestAcceptance_IncompleteEnvironmentTagIsNotRelocated(t *testing.T) {
	text := "Document the literal <env> tag in your answer; this block has no closing tag."
	call := textCall("anthropic/sonnet")
	call.Instructions = []aipapi.Message{{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart(text)}}}

	system, prompt, warnings, err := buildTextPrompt(call)
	if err != nil {
		t.Fatal(err)
	}
	if system != text {
		t.Fatalf("system=%q", system)
	}
	if prompt != "hello" {
		t.Fatalf("prompt=%q", prompt)
	}
	if warningIndex(warnings, "environment_context_relocated") >= 0 {
		t.Fatalf("unexpected relocation disclosure: %+v", warnings)
	}
}

// #822: Codex sends a cache hint even with function-only tools. The transport
// must carry it and the SDK limitation must be disclosed, never silently ignored.
func TestAcceptance_PromptCacheHintNegotiatedAndDisclosed(t *testing.T) {
	descriptor, err := New().Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	neg := backendplugin.Negotiation{Compatible: true, NegotiatedMinor: descriptor.ProtocolMinor}
	for _, f := range descriptor.Features {
		neg.EnabledFeatures = append(neg.EnabledFeatures, f.Name)
	}
	call := textCall("anthropic/sonnet")
	call.PromptCacheKey = "private-hint-canary"
	if err := backendplugin.RequireExactOpenResponsesABISupport(neg, *call); err != nil {
		t.Fatal(err)
	}
	eng := testEngine(t, "")
	defer func() { _ = eng.Close(context.Background()) }()
	stream, err := eng.Open(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	events, err := collect(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	if warningIndex(events, "prompt_cache_key_unsupported") < 0 {
		t.Fatalf("missing cache-hint disclosure: %+v", events)
	}
	for _, ev := range events {
		if strings.Contains(ev.WarningMessage, call.PromptCacheKey) {
			t.Fatal("cache hint leaked in diagnostics")
		}
	}
	eng.cfg.StrictSemantics = true
	if _, err := eng.Open(context.Background(), call); !errors.Is(err, aipapi.ErrCapabilityReject) {
		t.Fatalf("strict cache hint: %v", err)
	}
}
