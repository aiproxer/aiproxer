package compactionfacts

import (
	"errors"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
)

// ErrFactBudgetExceeded is returned when the retained facts (item hashes)
// exceed the configured budget.
var ErrFactBudgetExceeded = errors.New("compactionfacts: item count exceeds fact budget")

const (
	// ItemHashSizeBytes is the memory size in bytes of one SHA-256 item hash (32 bytes).
	ItemHashSizeBytes = 32

	// DefaultMaxFactItems bounds the maximum number of retained item hashes for wire ingestion
	// (4096 items * 32 bytes/item = 128 KiB fact memory budget).
	DefaultMaxFactItems = 4096
	// HeuristicTailItems is the number of recent semantic tail fingerprints.
	HeuristicTailItems = 2
	// HeuristicPrefixItems is the number of leading item hashes folded into PrefixHash.
	HeuristicPrefixItems = 8
)

// RuleMode declares the transaction behavior of a compaction rule.
type RuleMode int

const (
	// RuleModeSingle: the first matching opened request emits a start; a strict
	// later completion closes it; otherwise the transaction closes silently.
	RuleModeSingle RuleMode = iota
	// RuleModeSeries: later matching utility subcalls reuse one active transaction
	// and suppress repeated starts; the first strict/heuristic completion closes.
	RuleModeSeries
	// RuleModeCompletionOnly: no start is ever emitted; a post marker or heuristic
	// creates one completed event transaction.
	RuleModeCompletionOnly
)

// Evidence defines evidence levels matching canonical compaction semantics.
type Evidence = compaction.Evidence

const (
	EvidenceProtocolStrict   = compaction.EvidenceProtocolStrict
	EvidenceSignatureStrict  = compaction.EvidenceSignatureStrict
	EvidenceHistoryHeuristic = compaction.EvidenceHistoryHeuristic
)

// Shared compaction rule markers.
const (
	MarkerCodexCheckpoint     = "context checkpoint compaction"
	MarkerConversationTag     = "<conversation>"
	MarkerPiSummaryCarrier    = "<summary>"
	MarkerClineSummaryPost    = "context summary:"
	MarkerSystemNotice        = "<system_notice>"
	MarkerClineCompactedPost  = "earlier context was compacted"
	MarkerOpenCodeHistoryHead = "the following is the conversation history:"
	MarkerHermesRefOnly       = "[context compaction \u2014 reference only]"
	MarkerHermesLegacySummary = "[context summary]:"
	MarkerKiloObjective       = "objective"
	MarkerKiloDetails         = "important details"
	MarkerKiloWorkState       = "work state"
	MarkerKiloNextMove        = "next move"
	MarkerKiloFiles           = "relevant files"
	MarkerAiderUserTag        = "# user"
	MarkerAiderAssistantTag   = "# assistant"
)

// Shared compaction rule IDs.
const (
	RuleProtocolContextCompaction       = "protocol.context_compaction.v1"
	RuleCodexLocalCheckpoint            = "codex.local_checkpoint.v1"
	RulePiOpenClawCompactionSummary     = "pi_openclaw.compaction_summary.v1"
	RuleClineAgenticCompaction          = "cline.agentic_compaction.v1"
	RuleClineBasicCompactionPost        = "cline.basic_compaction_post.v1"
	RuleOpenCodeAnchoredSummary         = "opencode.anchored_summary.v1"
	RuleOpenCodeCustomCompactionHistory = "opencode.custom_compaction_history.v1"
	RuleHermesLocalCompactionPost       = "hermes.local_compaction_post.v1"
	RuleHermesLegacyCompactionPost      = "hermes.legacy_compaction_post.v1"
	RuleKiloCodeAnchoredSummary         = "kilocode.anchored_summary.v1"
	RuleClaudeCodeCompaction            = "claude_code_2026_03.compaction.v1"
	RuleGeminiCLIStateSnapshot          = "gemini_cli.state_snapshot.v1"
	RuleRooCodeCondense                 = "roo_code.condense.v1"
	RuleAiderChatSummary                = "aider.chat_summary.v1"
	RuleCrushSessionSummary             = "crush.session_summary.v1"
	RuleHeuristicCompaction             = "local.compaction_heuristic.v1"
)

// RequestFacts holds bounded semantic facts extracted from a request
// for compaction recognition without retaining prompt or payload content.
type RequestFacts struct {
	Operation       aipapi.Operation
	ToolCount       int
	EstimatedTokens int
	ItemCount       int
	ItemHashes      [][32]byte
	TailHashes      [HeuristicTailItems][32]byte
	TailLen         int
	PrefixHash      [32]byte
	PrefixItems     int

	StartRuleMatched  bool
	StartRuleID       string
	StartRuleMode     RuleMode
	StartRuleEvidence Evidence
}

// Clone returns an owned deep copy of RequestFacts, ensuring that mutations
// to ItemHashes in the clone or original do not affect each other.
func (f RequestFacts) Clone() RequestFacts {
	out := f
	if f.ItemHashes != nil {
		out.ItemHashes = make([][32]byte, len(f.ItemHashes))
		copy(out.ItemHashes, f.ItemHashes)
	}
	return out
}
