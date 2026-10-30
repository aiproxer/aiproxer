package compactiondetect

import (
	"strings"

	"github.com/aiproxer/aiproxer/internal/compactionfacts"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
)

// ruleMode declares the transaction behavior of a rule (requirement 6.1).
type ruleMode = compactionfacts.RuleMode

const (
	// modeSingle: the first matching opened request emits a start; a strict
	// later completion closes it; otherwise the transaction closes silently.
	modeSingle = compactionfacts.RuleModeSingle
	// modeSeries: later matching utility subcalls reuse one active transaction
	// and suppress repeated starts; the first strict/heuristic completion closes.
	modeSeries = compactionfacts.RuleModeSeries
	// modeCompletionOnly: no start is ever emitted; a post marker or heuristic
	// creates one completed event transaction.
	modeCompletionOnly = compactionfacts.RuleModeCompletionOnly
)

// StartRuleMatchInput provides facts to MatchStartRule for wire or canonical requests.
type StartRuleMatchInput struct {
	Operation aipapi.Operation
	ToolCount int
	HasText   func(marker string) bool
}

// StartRuleMatchResult is the output of MatchStartRule.
type StartRuleMatchResult struct {
	Matched  bool
	RuleID   string
	Mode     compactionfacts.RuleMode
	Evidence compaction.Evidence
}

// MatchStartRule evaluates the ordered compaction start-rule table against request facts.
func MatchStartRule(in StartRuleMatchInput) StartRuleMatchResult {
	hasText := in.HasText
	if hasText == nil {
		hasText = func(string) bool { return false }
	}
	sr, ok := compactionfacts.MatchStartRule(in.Operation, in.ToolCount, hasText)
	if !ok {
		return StartRuleMatchResult{}
	}
	return StartRuleMatchResult{
		Matched:  true,
		RuleID:   sr.ID,
		Mode:     sr.Mode,
		Evidence: sr.Evidence,
	}
}

// rule is one versioned detection rule for response-side post-marker completion.
type rule struct {
	id       string
	mode     ruleMode
	evidence compaction.Evidence
	// complete matches a released installed-summary/post marker in the
	// lowercased released-text window (folded once per chunk by the detector).
	// Nil when the family exposes no response-side marker (the rule then
	// completes only via strict canonical output or the history heuristic).
	complete func(string) bool
}

// Rule marker constants consolidated under compactionfacts as shared owner.
const (
	markerCodexCheckpoint     = compactionfacts.MarkerCodexCheckpoint
	markerConversationTag     = compactionfacts.MarkerConversationTag
	markerPiSummaryCarrier    = compactionfacts.MarkerPiSummaryCarrier
	markerClineSummaryPost    = compactionfacts.MarkerClineSummaryPost
	markerSystemNotice        = compactionfacts.MarkerSystemNotice
	markerClineCompactedPost  = compactionfacts.MarkerClineCompactedPost
	markerOpenCodeHistoryHead = compactionfacts.MarkerOpenCodeHistoryHead
	markerHermesRefOnly       = compactionfacts.MarkerHermesRefOnly
	markerHermesLegacySummary = compactionfacts.MarkerHermesLegacySummary
	markerKiloObjective       = compactionfacts.MarkerKiloObjective
	markerKiloDetails         = compactionfacts.MarkerKiloDetails
	markerKiloWorkState       = compactionfacts.MarkerKiloWorkState
	markerKiloNextMove        = compactionfacts.MarkerKiloNextMove
	markerKiloFiles           = compactionfacts.MarkerKiloFiles
	markerAiderUserTag        = compactionfacts.MarkerAiderUserTag
	markerAiderAssistantTag   = compactionfacts.MarkerAiderAssistantTag
)

var (
	// protocolRule is the response-side rule for explicit compact operations.
	// Its completion is the released compaction item or the successful terminal.
	protocolRule = rule{
		id:       compactionfacts.RuleProtocolContextCompaction,
		mode:     modeSingle,
		evidence: compaction.EvidenceProtocolStrict,
	}

	// ruleTable specifies response completion rules.
	ruleTable = []rule{
		protocolRule,
		{
			id:       compactionfacts.RuleCodexLocalCheckpoint,
			mode:     modeSingle,
			evidence: compaction.EvidenceSignatureStrict,
			complete: func(text string) bool {
				return strings.Contains(text, markerCodexCheckpoint)
			},
		},
		{
			id:       compactionfacts.RulePiOpenClawCompactionSummary,
			mode:     modeSeries,
			evidence: compaction.EvidenceSignatureStrict,
			complete: func(text string) bool {
				return strings.Contains(text, markerPiSummaryCarrier)
			},
		},
		{
			id:       compactionfacts.RuleClineAgenticCompaction,
			mode:     modeSingle,
			evidence: compaction.EvidenceSignatureStrict,
			complete: func(text string) bool {
				return strings.Contains(text, markerClineSummaryPost)
			},
		},
		{
			id:       compactionfacts.RuleClineBasicCompactionPost,
			mode:     modeCompletionOnly,
			evidence: compaction.EvidenceSignatureStrict,
			complete: func(text string) bool {
				return strings.Contains(text, markerSystemNotice) &&
					strings.Contains(text, markerClineCompactedPost)
			},
		},
		{
			id:       compactionfacts.RuleOpenCodeAnchoredSummary,
			mode:     modeSingle,
			evidence: compaction.EvidenceSignatureStrict,
		},
		{
			id:       compactionfacts.RuleOpenCodeCustomCompactionHistory,
			mode:     modeSingle,
			evidence: compaction.EvidenceSignatureStrict,
		},
		{
			id:       compactionfacts.RuleHermesLocalCompactionPost,
			mode:     modeCompletionOnly,
			evidence: compaction.EvidenceSignatureStrict,
			complete: func(text string) bool {
				return strings.Contains(text, markerHermesRefOnly)
			},
		},
		{
			id:       compactionfacts.RuleHermesLegacyCompactionPost,
			mode:     modeCompletionOnly,
			evidence: compaction.EvidenceSignatureStrict,
			complete: func(text string) bool {
				return strings.Contains(text, markerHermesLegacySummary)
			},
		},
		{
			id:       compactionfacts.RuleKiloCodeAnchoredSummary,
			mode:     modeSingle,
			evidence: compaction.EvidenceSignatureStrict,
		},
		{
			id:       compactionfacts.RuleClaudeCodeCompaction,
			mode:     modeSingle,
			evidence: compaction.EvidenceSignatureStrict,
			complete: func(text string) bool {
				return strings.Contains(text, "continuation") &&
					strings.Contains(text, "previous conversation")
			},
		},
		{
			id:       compactionfacts.RuleGeminiCLIStateSnapshot,
			mode:     modeSeries,
			evidence: compaction.EvidenceSignatureStrict,
		},
		{
			id:       compactionfacts.RuleRooCodeCondense,
			mode:     modeSingle,
			evidence: compaction.EvidenceSignatureStrict,
		},
		{
			id:       compactionfacts.RuleAiderChatSummary,
			mode:     modeSeries,
			evidence: compaction.EvidenceSignatureStrict,
			complete: func(text string) bool {
				return strings.Contains(text, "previous conversation")
			},
		},
		{
			id:       compactionfacts.RuleCrushSessionSummary,
			mode:     modeSingle,
			evidence: compaction.EvidenceSignatureStrict,
		},
	}
)

// matchCompleteRule returns the first rule whose released-text post-marker
// predicate matches. Start-bearing rules may complete only their active
// transaction; completion-only rules may match without an active transaction.
// The protocol rule completes through canonical items and the explicit-compact
// terminal instead of text, so it is not consulted here.
func matchCompleteRule(text, activeRuleID string) (rule, bool) {
	if text == "" {
		return rule{}, false
	}
	for _, r := range ruleTable {
		if r.complete == nil {
			continue
		}
		if r.mode != modeCompletionOnly && r.id != activeRuleID {
			continue
		}
		if r.complete(text) {
			return r, true
		}
	}
	return rule{}, false
}
