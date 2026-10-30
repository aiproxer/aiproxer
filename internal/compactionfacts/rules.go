package compactionfacts

import (
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// StartRule defines one compaction request start rule.
type StartRule struct {
	ID       string
	Mode     RuleMode
	Evidence Evidence
	Match    func(op aipapi.Operation, toolCount int, hasText func(string) bool) bool
}

// StartRules is the ordered table of compaction start rules.
// The protocol rule must stay first so canonical semantics dominate text signatures.
var StartRules = []StartRule{
	{
		ID:       RuleProtocolContextCompaction,
		Mode:     RuleModeSingle,
		Evidence: EvidenceProtocolStrict,
		Match: func(op aipapi.Operation, _ int, _ func(string) bool) bool {
			return op == aipapi.OperationContextCompaction
		},
	},
	{
		ID:       RuleCodexLocalCheckpoint,
		Mode:     RuleModeSingle,
		Evidence: EvidenceSignatureStrict,
		Match: func(_ aipapi.Operation, _ int, hasText func(string) bool) bool {
			return hasText(MarkerCodexCheckpoint)
		},
	},
	{
		ID:       RulePiOpenClawCompactionSummary,
		Mode:     RuleModeSeries,
		Evidence: EvidenceSignatureStrict,
		Match: func(_ aipapi.Operation, _ int, hasText func(string) bool) bool {
			return hasText(MarkerConversationTag) && hasText("summar") && hasText("checkpoint")
		},
	},
	{
		ID:       RuleClineAgenticCompaction,
		Mode:     RuleModeSingle,
		Evidence: EvidenceSignatureStrict,
		Match: func(_ aipapi.Operation, _ int, hasText func(string) bool) bool {
			return hasText("continuation-note") && hasText("summar")
		},
	},
	{
		ID:       RuleOpenCodeAnchoredSummary,
		Mode:     RuleModeSingle,
		Evidence: EvidenceSignatureStrict,
		Match: func(_ aipapi.Operation, _ int, hasText func(string) bool) bool {
			return hasText(MarkerConversationTag) && hasText("summar") &&
				hasText(MarkerKiloObjective) && hasText(MarkerKiloWorkState)
		},
	},
	{
		ID:       RuleOpenCodeCustomCompactionHistory,
		Mode:     RuleModeSingle,
		Evidence: EvidenceSignatureStrict,
		Match: func(_ aipapi.Operation, _ int, hasText func(string) bool) bool {
			return hasText(MarkerOpenCodeHistoryHead)
		},
	},
	{
		ID:       RuleKiloCodeAnchoredSummary,
		Mode:     RuleModeSingle,
		Evidence: EvidenceSignatureStrict,
		Match: func(_ aipapi.Operation, _ int, hasText func(string) bool) bool {
			return hasText(MarkerKiloObjective) && hasText(MarkerKiloDetails) &&
				hasText(MarkerKiloWorkState) && hasText(MarkerKiloNextMove) &&
				hasText(MarkerKiloFiles)
		},
	},
	{
		ID:       RuleClaudeCodeCompaction,
		Mode:     RuleModeSingle,
		Evidence: EvidenceSignatureStrict,
		Match: func(_ aipapi.Operation, toolCount int, hasText func(string) bool) bool {
			return toolCount == 0 && hasText("text only") && hasText("compaction") && hasText("conversation")
		},
	},
	{
		ID:       RuleGeminiCLIStateSnapshot,
		Mode:     RuleModeSeries,
		Evidence: EvidenceSignatureStrict,
		Match: func(_ aipapi.Operation, _ int, hasText func(string) bool) bool {
			if !hasText("state snapshot") {
				return false
			}
			return hasText("generate") || hasText("verify")
		},
	},
	{
		ID:       RuleRooCodeCondense,
		Mode:     RuleModeSingle,
		Evidence: EvidenceSignatureStrict,
		Match: func(_ aipapi.Operation, _ int, hasText func(string) bool) bool {
			return hasText("condense") && hasText("summar") && hasText("conversation")
		},
	},
	{
		ID:       RuleAiderChatSummary,
		Mode:     RuleModeSeries,
		Evidence: EvidenceSignatureStrict,
		Match: func(_ aipapi.Operation, _ int, hasText func(string) bool) bool {
			return hasText(MarkerAiderUserTag) && hasText(MarkerAiderAssistantTag) && hasText("summar")
		},
	},
	{
		ID:       RuleCrushSessionSummary,
		Mode:     RuleModeSingle,
		Evidence: EvidenceSignatureStrict,
		Match: func(_ aipapi.Operation, _ int, hasText func(string) bool) bool {
			return hasText("session summary") && hasText("preserve") && hasText("context")
		},
	},
}

// MatchStartRule evaluates the ordered compaction start-rule table against request facts.
func MatchStartRule(op aipapi.Operation, toolCount int, hasText func(string) bool) (StartRule, bool) {
	if hasText == nil {
		hasText = func(string) bool { return false }
	}
	for _, r := range StartRules {
		if r.Match != nil && r.Match(op, toolCount, hasText) {
			return r, true
		}
	}
	return StartRule{}, false
}
