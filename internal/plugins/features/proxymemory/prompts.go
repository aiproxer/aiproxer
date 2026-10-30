package proxymemory

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
)

// Vendored Codex templates (design §7.1). The whole templates directory is
// embedded so later tasks can add the phase 2 and injection templates without
// changing the directive; files are read by path.
//
//go:embed templates
var templatesFS embed.FS

// Embedded template paths.
const (
	stageOneSystemTemplate = "templates/codex/stage_one_system.md"
	stageOneInputTemplate  = "templates/codex/stage_one_input.md"
)

// CaptureHandlerID is the capture transform handler id. Phase 1 and phase 2
// requests disable it explicitly by exact handler id (design §7.2).
const CaptureHandlerID = "proxy-memory.capture"

// phase1Role is the auxiliary request role of phase 1 extraction (design §7.2).
const phase1Role = "proxy_memory_phase1"

// rolloutSlugMaxLen is the Codex ROLLOUT_SLUG_MAX_LEN (design §7.2).
const rolloutSlugMaxLen = 60

// defaultRolloutSlug replaces an empty or null model slug (design §7.2).
const defaultRolloutSlug = "session"

// Phase1Input is one phase 1 extraction input (design §7.2). SessionRef is
// "session-" + the first 12 hex characters of sha256(ALegID); the raw A-leg
// identifier is never part of a prompt or a memory document.
type Phase1Input struct {
	SessionRef   string
	ProjectLabel string
	Transcript   string // EncodeTranscript output
}

// Phase1Output is one parsed and sanitized phase 1 result (design §7.2).
type Phase1Output struct {
	RawMemory      string
	RolloutSummary string
	RolloutSlug    string
}

// templateVar is one literal template substitution pair.
type templateVar struct {
	name  string
	value string
}

// renderTemplate substitutes literal {{ name }} tokens in a single pass.
// Substituted values are never rescanned, so transcript or project-label
// content cannot inject or re-expand a token, and every {{ … }} token outside
// the supplied names is left exactly as it appears (design §7.1).
func renderTemplate(text string, vars ...templateVar) string {
	pairs := make([]string, 0, 2*len(vars))
	for _, v := range vars {
		pairs = append(pairs, "{{ "+v.name+" }}", v.value)
	}
	return strings.NewReplacer(pairs...).Replace(text)
}

// templateText reads one embedded template.
func templateText(path string) (string, error) {
	data, err := templatesFS.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("%s: read template %s: %w", ID, path, err)
	}
	return string(data), nil
}

// SessionRef derives the non-reversible session reference used in phase 1
// prompts and extraction records (design §7.2): "session-" plus the first 12
// hexadecimal characters of sha256(aLegID).
func SessionRef(aLegID string) string {
	sum := sha256.Sum256([]byte(aLegID))
	return "session-" + hex.EncodeToString(sum[:6])
}

// BuildPhase1Request builds one detached phase 1 extraction request (design
// §7.2, Requirements 4.2, 4.3 and 4.7): the Codex stage-one system prompt, the
// stage-one user prompt with the session reference, the project label and the
// transcript truncated to cfg.Phase1InputTokens, and a private no-tools call
// routed through the phase's derived route selector with the phase's
// effective reasoning effort and a JSON response type. DisablePlugins
// suppresses this feature's capture transform by exact handler id, so an
// extraction call is never captured.
//
// The route selector is derived from the operator's default route and the
// phase's declared backend and model (design §3): it is never taken from a
// client-facing field. The derived selector is the requested target; the
// checks against the served target belong to task 2.2. An operator that names
// no default effort keeps today's low effort.
//
// The transcript is sanitized before and after the truncation, so the rendered
// user message stays a Redact fixpoint (Requirement 3.3; design §5 and §7.2).
// TruncateTokens is lossy in the redaction sense: removing bytes can create the
// word boundary the patterns need and the inserted marker can supply one, so a
// before-transform pass alone leaves shapes the cut creates, and a final pass
// alone cannot recover fragments of a value the original already matched.
// cleanTranscriptString puts NUL removal before each Redact call, and Redact
// stays the last byte-changing operation on the value. The session reference
// and the project label are trusted metadata and are inserted as they come in.
func BuildPhase1Request(cfg Config, in Phase1Input) (auxiliary.Request, error) {
	route, effort, err := phaseCallTarget(cfg, PhaseExtraction)
	if err != nil {
		return auxiliary.Request{}, err
	}
	system, err := templateText(stageOneSystemTemplate)
	if err != nil {
		return auxiliary.Request{}, err
	}
	user, err := templateText(stageOneInputTemplate)
	if err != nil {
		return auxiliary.Request{}, err
	}
	user = renderTemplate(user,
		templateVar{name: "rollout_path", value: in.SessionRef},
		templateVar{name: "rollout_cwd", value: in.ProjectLabel},
		templateVar{name: "rollout_contents", value: cleanTranscriptString(TruncateTokens(cleanTranscriptString(in.Transcript), cfg.Phase1InputTokens))},
	)
	call := &aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleSystem, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: system}}},
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: user}}},
		},
		Route:      aipapi.RouteIntent{Selector: route},
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceNone},
		Options: aipapi.GenerationOptions{
			ReasoningEffort:  effort,
			ResponseMIMEType: "application/json",
		},
	}
	if err := call.Validate(); err != nil {
		return auxiliary.Request{}, fmt.Errorf("%s: phase 1 call: %w", ID, err)
	}
	return auxiliary.Request{
		Role:           phase1Role,
		Visibility:     "private",
		SessionMode:    auxiliary.SessionModeDetached,
		DisablePlugins: []string{ID, CaptureHandlerID},
		Call:           call,
	}, nil
}

// phase1Envelope is the strict wire shape of a phase 1 result (design §7.2):
// raw_memory and rollout_summary must be strings; rollout_slug may be a string
// or null. A missing or null required field is rejected.
type phase1Envelope struct {
	RawMemory      *string `json:"raw_memory"`
	RolloutSummary *string `json:"rollout_summary"`
	RolloutSlug    *string `json:"rollout_slug"`
}

// ParsePhase1 parses and sanitizes one phase 1 result (design §7.2,
// Requirement 4.2): one surrounding Markdown code fence is stripped, the JSON
// body is decoded strictly (unknown fields and wrong types are rejected), a
// blank rollout summary is rejected, every field is redacted, NUL bytes are
// removed, and the slug is normalized.
//
// Redact is the last byte-changing operation on every returned field
// (Requirement 3.3). The slug additionally gets an earlier Redact pass on its
// NUL-stripped original: without it, lossy slug normalization (lowercasing,
// mapping non-[a-z0-9] runs to "-") would destroy the shapes the patterns
// match before the redactor can see them.
func ParsePhase1(text string) (Phase1Output, error) {
	dec := json.NewDecoder(strings.NewReader(stripMarkdownFence(text)))
	dec.DisallowUnknownFields()
	var envelope phase1Envelope
	if err := dec.Decode(&envelope); err != nil {
		return Phase1Output{}, fmt.Errorf("%s: phase 1 output: %w", ID, err)
	}
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Phase1Output{}, fmt.Errorf("%s: phase 1 output: trailing content after the JSON object", ID)
	}
	if envelope.RawMemory == nil {
		return Phase1Output{}, fmt.Errorf("%s: phase 1 output: raw_memory must be a string", ID)
	}
	if envelope.RolloutSummary == nil {
		return Phase1Output{}, fmt.Errorf("%s: phase 1 output: rollout_summary must be a string", ID)
	}
	summary := cleanTranscriptString(*envelope.RolloutSummary)
	if strings.TrimSpace(summary) == "" {
		return Phase1Output{}, fmt.Errorf("%s: phase 1 output: rollout_summary is blank", ID)
	}
	slug := ""
	if envelope.RolloutSlug != nil {
		slug = *envelope.RolloutSlug
	}
	return Phase1Output{
		RawMemory:      cleanTranscriptString(*envelope.RawMemory),
		RolloutSummary: summary,
		RolloutSlug:    cleanRolloutSlug(slug),
	}, nil
}

// stripMarkdownFence removes one surrounding Markdown code fence when the
// trimmed text starts with a ``` or ```json line and ends with a ``` line
// (design §7.2). Text without that exact surrounding shape is returned
// trimmed, unchanged; anything else is a malformed model reply that the strict
// decoder rejects.
func stripMarkdownFence(text string) string {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed
	}
	before, after, ok := strings.Cut(trimmed, "\n")
	if !ok {
		return trimmed
	}
	opening := strings.TrimSpace(before)
	if opening != "```" && opening != "```json" {
		return trimmed
	}
	body := strings.TrimRight(after, " \t\r\n")
	lastBreak := strings.LastIndexByte(body, '\n')
	if closing := strings.TrimSpace(body[lastBreak+1:]); closing != "```" {
		return trimmed
	}
	if lastBreak < 0 {
		return ""
	}
	return strings.TrimSpace(body[:lastBreak])
}

// cleanRolloutSlug sanitizes one model-provided slug (design §7.2): NUL bytes
// are removed and the original is redacted, the slug is then normalized, and
// the normalized result is redacted again as the last byte-changing
// operation.
//
// Both Redact passes are required (Requirement 3.3). Lossy normalization
// (lowercasing, mapping non-[a-z0-9] runs to "-") destroys the shapes the
// patterns match, so redacting only after normalization would store
// recognized secrets verbatim; normalization can also create shapes the
// original did not have (an upper-case "SK-…" run becomes the exact
// case-sensitive key shape), which the final pass covers while keeping Redact
// last.
func cleanRolloutSlug(slug string) string {
	return cleanTranscriptString(normalizeRolloutSlug(cleanTranscriptString(slug)))
}

// normalizeRolloutSlug applies the Codex slug rules (ROLLOUT_SLUG_MAX_LEN =
// 60, design §7.2): lowercase, map non-[a-z0-9] runs to "-", trim "-", cut to
// 60, and replace an empty result with "session".
func normalizeRolloutSlug(slug string) string {
	var b strings.Builder
	b.Grow(len(slug))
	pendingDash := false
	for i := 0; i < len(slug); i++ {
		c := slug[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			if pendingDash {
				b.WriteByte('-')
				pendingDash = false
			}
			b.WriteByte(c)
		case c >= 'A' && c <= 'Z':
			if pendingDash {
				b.WriteByte('-')
				pendingDash = false
			}
			b.WriteByte(c + ('a' - 'A'))
		default:
			// Every non-[a-z0-9] byte extends the current run: a run maps to
			// one dash, a leading run produces none (the buffer is still
			// empty) and a trailing run is never flushed (trim "-"). This
			// matches rune-wise mapping for UTF-8 input: each byte of a
			// multi-byte rune lands in the same run.
			if b.Len() > 0 {
				pendingDash = true
			}
		}
	}
	out := b.String()
	if out == "" {
		return defaultRolloutSlug
	}
	if len(out) > rolloutSlugMaxLen {
		out = out[:rolloutSlugMaxLen]
	}
	return out
}

// EXTENSIONS_FOLDER_STRUCTURE and EXTENSIONS_PRIMARY_INPUTS are copied
// verbatim from the OpenAI Codex repository, codex-rs/memories/write/src/lib.rs
// (mod prompt_blocks), at commit
// 0df4aa67781ae45a3c97befa4db48b734afe46d9, licensed under Apache-2.0
// (<https://github.com/openai/codex/blob/0df4aa67781ae45a3c97befa4db48b734afe46d9/LICENSE>).
// They are rendered with memory_extensions_root → memory/extensions (design
// §7.3). The leading and trailing newlines are part of the upstream literals.
const EXTENSIONS_FOLDER_STRUCTURE = `
Memory extensions (under {{ memory_extensions_root }}/):

- <extension_name>/instructions.md
  - Source-specific guidance for interpreting additional memory signals. If an
    extension folder exists, you must read its instructions.md to determine how to use this memory
    source.

If the user has any memory extensions, you MUST read the instructions for each extension to
determine how to use the memory source. If the workspace diff shows deleted extension resource files,
remove stale memories derived only from those resources. If it has no extension folders, continue
with the standard memory inputs only.
`

const EXTENSIONS_PRIMARY_INPUTS = `
Optional source-specific inputs:
Under ` + "`{{ memory_extensions_root }}/`" + `:

- ` + "`<extension_name>/instructions.md`" + `
  - If extension folders exist, read each instructions.md first and follow it when interpreting
    that extension's memory source.

If the workspace diff shows deleted memory extension resources, use that extension-specific deletion
signal to remove stale memories derived only from those resources.
`

// Embedded proxy and consolidation template paths (design §7.3, §7.4).
const (
	proxyPhase2HeaderTemplate = "templates/proxy_phase2_header.md"
	proxyPhase2FooterTemplate = "templates/proxy_phase2_footer.md"
	proxyReadPathTemplate     = "templates/proxy_read_path.md"
	consolidationTemplate     = "templates/codex/consolidation.md"
	adHocInstructionsTemplate = "templates/codex/ad_hoc_instructions.md"
)

// phase2Role is the auxiliary request role of phase 2 consolidation (design §7.3).
const phase2Role = "proxy_memory_phase2"

// Phase 2 user-message section names, in order (design §7.3).
const (
	phase2SectionCurrentSummary   = "CURRENT memory/memory_summary.md"
	phase2SectionCurrentMemory    = "CURRENT memory/MEMORY.md"
	phase2SectionChanges          = "CHANGES"
	phase2SectionRawMemories      = "memory/raw_memories.md"
	phase2SectionRolloutSummaries = "memory/rollout_summaries/"
	phase2SectionAdHocNotes       = "AD-HOC NOTES"
)

// Consolidation prompt substitutions (design §7.3): the memory root, the
// workspace-diff file name, and the extensions root handed to the Codex
// extension blocks.
const (
	phase2MemoryRoot     = "memory"
	phase2ExtensionsRoot = "memory/extensions"
)

// extensionsBlockNote follows the rendered extension folder structure: the
// proxy exposes exactly one extension, whose inputs are inline (design §7.3).
const extensionsBlockNote = "The only extension is `ad_hoc`, whose instructions and notes are provided inline in the section `AD-HOC NOTES`.\n"

// Stored phase 2 document limits (design §7.3).
const (
	phase2MemoryMaxBytes  = 262144
	phase2SummaryMaxBytes = 32768
)

// Phase2Input is one phase 2 consolidation input (design §7.3). Extractions
// are the consolidation window, ascending by (GeneratedAtMS, ALegID); Notes
// are all pending notes, ascending by CreatedAtMS; PreviousNames is the
// bank's ConsolidatedNames from its last publication. ProjectLabel is the
// bank label rendered as the rollout documents' cwd (the Codex
// rollout_cwd).
type Phase2Input struct {
	CurrentSummary  string
	CurrentMemoryMD string
	ProjectLabel    string
	Extractions     []Extraction
	WindowNames     []string
	Notes           []Note
	PreviousNames   []string
}

// Phase2Output is one parsed and sanitized phase 2 result (design §7.3).
type Phase2Output struct {
	MemoryMD      string
	MemorySummary string
}

// Phase2Included reports what one phase 2 request actually included (design
// §7.3): the note IDs sent in full, the SummaryNames of the extractions sent
// as raw-memory and rollout-summary pairs, and whether any note was left out,
// any extraction was left out, or the current MEMORY.md was cut.
type Phase2Included struct {
	NoteIDs      []string
	SummaryNames []string
	Extractions  []ExtractionRef
	Truncated    bool
}

// BuildPhase2Request builds one detached phase 2 consolidation request (design
// §7.3, Requirements 5.1, 5.7 and 5.9): the proxy phase 2 header, the vendored
// consolidation prompt with its substitutions, and the proxy footer as the
// system message; the six inline sections as the user message; and a private
// no-tools call routed through the phase's derived route selector with the
// phase's effective reasoning effort and a JSON response type.
//
// The route selector is derived from the operator's default route and the
// phase's declared backend and model (design §3): it is never taken from a
// client-facing field. The derived selector is the requested target; the
// checks against the served target belong to task 2.2. An operator that names
// no default effort keeps today's medium effort.
//
// Input budget (design §7.3, Requirement 5.9): the budget is enforced on the
// rendered user message as the request carries it — every metadata byte and
// any truncation marker included. Notes are included oldest first, in full,
// while their total size stays within cfg.Phase2NoteBytes and the message
// carrying them still measures within cfg.Phase2InputTokens; once one note
// does not fit either budget, it and every later note stay pending for a later
// consolidation. Extractions are included newest first, as whole raw-memory
// and rollout-summary pairs, while the rendered user message stays within
// cfg.Phase2InputTokens; the included ones are rendered in ascending order.
// With no extraction left and the message still over budget, CURRENT
// memory/MEMORY.md is cut to the remaining budget, sanitized before and after
// the cut, and the sanitized cut is measured again, so neither the marker
// bytes nor the redaction can push the message back over the limit, and the
// cut can neither expose nor fragment a secret-like value. No extraction or
// note is ever cut mid-text. Any omission or cut is reported through
// Phase2Included.Truncated; the worker counts and logs it. When even the
// immutable required sections alone exceed the configured limit, an explicit
// error is returned instead of a successful over-budget request.
func BuildPhase2Request(cfg Config, in Phase2Input) (auxiliary.Request, Phase2Included, error) {
	route, effort, err := phaseCallTarget(cfg, PhaseConsolidation)
	if err != nil {
		return auxiliary.Request{}, Phase2Included{}, err
	}
	system, err := phase2SystemMessage()
	if err != nil {
		return auxiliary.Request{}, Phase2Included{}, err
	}
	adHocInstructions, err := templateText(adHocInstructionsTemplate)
	if err != nil {
		return auxiliary.Request{}, Phase2Included{}, err
	}
	base := len(phase2UserMessage(in, nil, nil, "", adHocInstructions))
	if required := approxTokensForBytes(base); required > cfg.Phase2InputTokens {
		return auxiliary.Request{}, Phase2Included{}, fmt.Errorf("%s: phase 2 input: the required sections alone need %d tokens, over the %d-token budget; raise Phase2InputTokens", ID, required, cfg.Phase2InputTokens)
	}
	var included Phase2Included
	sentNotes, baseBytes := fitPhase2Notes(cfg, in, base, &included)
	selected, memoryMD := fitPhase2Extractions(cfg, in, sentNotes, baseBytes, &included)
	for _, e := range selected {
		included.SummaryNames = append(included.SummaryNames, SummaryName(e))
		included.Extractions = append(included.Extractions, ExtractionRef{
			ALegID: e.ALegID, Revision: e.Revision, GeneratedAtMS: e.GeneratedAtMS,
		})
	}
	user := phase2UserMessage(in, selected, sentNotes, memoryMD, adHocInstructions)
	call := &aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleSystem, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: system}}},
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: user}}},
		},
		Route:      aipapi.RouteIntent{Selector: route},
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceNone},
		Options: aipapi.GenerationOptions{
			ReasoningEffort:  effort,
			ResponseMIMEType: "application/json",
		},
	}
	if err := call.Validate(); err != nil {
		return auxiliary.Request{}, Phase2Included{}, fmt.Errorf("%s: phase 2 call: %w", ID, err)
	}
	return auxiliary.Request{
		Role:           phase2Role,
		Visibility:     "private",
		SessionMode:    auxiliary.SessionModeDetached,
		DisablePlugins: []string{ID, CaptureHandlerID},
		Call:           call,
	}, included, nil
}

// phase2SystemMessage renders the phase 2 system message (design §7.3): the
// proxy header, the vendored consolidation prompt with memory_root,
// phase2_workspace_diff_file and the two extension blocks substituted, and the
// proxy footer. The extension blocks are rendered first, in their own pass,
// because the template renderer never rescans substituted values.
func phase2SystemMessage() (string, error) {
	header, err := templateText(proxyPhase2HeaderTemplate)
	if err != nil {
		return "", err
	}
	footer, err := templateText(proxyPhase2FooterTemplate)
	if err != nil {
		return "", err
	}
	consolidation, err := templateText(consolidationTemplate)
	if err != nil {
		return "", err
	}
	folderStructure := renderTemplate(EXTENSIONS_FOLDER_STRUCTURE,
		templateVar{name: "memory_extensions_root", value: phase2ExtensionsRoot},
	) + extensionsBlockNote
	primaryInputs := renderTemplate(EXTENSIONS_PRIMARY_INPUTS,
		templateVar{name: "memory_extensions_root", value: phase2ExtensionsRoot},
	)
	consolidation = renderTemplate(consolidation,
		templateVar{name: "memory_root", value: phase2MemoryRoot},
		templateVar{name: "phase2_workspace_diff_file", value: phase2SectionChanges},
		templateVar{name: "memory_extensions_folder_structure", value: folderStructure},
		templateVar{name: "memory_extensions_primary_inputs", value: primaryInputs},
	)
	return header + consolidation + footer, nil
}

// fitPhase2Notes selects the prefix of the pending notes that fits both
// cfg.Phase2NoteBytes and the total rendered-message budget (design §7.3).
// Notes are included oldest first, in full, while their total text size stays
// within the byte budget and adding the note keeps the rendered user message
// within cfg.Phase2InputTokens; the first note that does not fit either budget
// stops the scan, so it and every later note stay pending for a later
// consolidation and are not consumed. baseBytes is the rendered size of the
// message with no notes and no extractions (an empty CURRENT memory/MEMORY.md
// included), and the returned size carries the accepted notes, so the caller's
// accounting measures exactly the message the request will carry: each
// accepted note adds its note block plus the digit the CHANGES note count may
// gain.
func fitPhase2Notes(cfg Config, in Phase2Input, baseBytes int, included *Phase2Included) ([]Note, int) {
	var sent []Note
	noteBytes := 0
	for _, note := range in.Notes {
		if noteBytes+len(note.Text) > cfg.Phase2NoteBytes {
			included.Truncated = true
			break
		}
		block := len(phase2NoteBlock(note))
		countDelta := len(phase2NotesCountLine(len(sent)+1)) - len(phase2NotesCountLine(len(sent)))
		if approxTokensForBytes(baseBytes+block+countDelta) > cfg.Phase2InputTokens {
			included.Truncated = true
			break
		}
		baseBytes += block + countDelta
		noteBytes += len(note.Text)
		sent = append(sent, note)
		included.NoteIDs = append(included.NoteIDs, note.NoteID)
	}
	return sent, baseBytes
}

// fitPhase2Extractions selects the oldest prefix of the outstanding batch that
// keeps the rendered user message within cfg.Phase2InputTokens (design §7.3)
// and applies the zero-extraction MEMORY.md cut. The store schedules outstanding
// extractions oldest first, so the prefix is the scheduled work: what does not
// fit stays outstanding for the next attempt instead of being silently skipped
// behind newer content. phase2UserMessage is the exact concatenation of its
// pieces: baseBytes is its size with the sent notes, no extractions and an empty
// CURRENT memory/MEMORY.md, and the only extraction-dependent pieces are the raw
// block and the rollout summary block of each extraction, so the rendered size
// is tracked exactly without re-rendering. The MEMORY.md cut is fitted on the
// measured message with the truncation marker accounted for, so a cut value can
// never push the request back over the budget.
func fitPhase2Extractions(cfg Config, in Phase2Input, sentNotes []Note, baseBytes int, included *Phase2Included) ([]Extraction, string) {
	blocks := make([]int, len(in.Extractions))
	total := baseBytes + len(in.CurrentMemoryMD)
	previous := make(map[string]struct{}, len(in.PreviousNames))
	for _, name := range in.PreviousNames {
		previous[name] = struct{}{}
	}
	for i, e := range in.Extractions {
		blocks[i] = len(phase2RawBlock(e)) + len(phase2RolloutBlock(in.ProjectLabel, e))
		if _, exists := previous[SummaryName(e)]; !exists {
			blocks[i] += len("added: rollout_summaries/" + SummaryName(e) + ".md\n")
		}
		total += blocks[i]
	}
	end := len(in.Extractions)
	for end > 0 && approxTokensForBytes(total) > cfg.Phase2InputTokens {
		total -= blocks[end-1]
		end--
		included.Truncated = true
	}
	selected := in.Extractions[:end]
	memoryMD := in.CurrentMemoryMD
	if len(selected) == 0 && approxTokensForBytes(total) > cfg.Phase2InputTokens {
		memoryMD = fitPhase2MemoryMD(in.CurrentMemoryMD, baseBytes, cfg.Phase2InputTokens)
		included.Truncated = true
	}
	return selected, memoryMD
}

// fitPhase2MemoryMD cuts the CURRENT memory/MEMORY.md value so that the
// rendered user message — baseBytes plus the cut value — stays within
// budgetTokens. TruncateTokens removes bytes but inserts a marker, so a cut
// fitted to the remaining budget can still overshoot it; each candidate is
// therefore measured on the exact phase2UserMessage arithmetic and the limit
// is then reduced by the measured excess. TruncateTokens is also lossy in the
// redaction sense: removing bytes can create the word boundary that makes a
// value the store legitimately kept matchable, and the inserted marker can
// supply one, while a value the original already matched would otherwise
// survive as an unmatchable fragment. Every candidate is therefore sanitized
// before and after the cut (cleanTranscriptString puts NUL removal before each
// Redact call, which stays the last byte-changing operation) and the
// sanitized candidate is what the arithmetic measures. Every iteration
// strictly lowers the limit, so the loop converges, and it returns the first
// candidate that measures within the budget (or the unchanged value when it
// already fits). When even the marker alone does not fit, the field is
// emptied rather than overrun; the caller reports the cut through
// Phase2Included.Truncated.
func fitPhase2MemoryMD(memoryMD string, baseBytes, budgetTokens int) string {
	if approxTokensForBytes(baseBytes+len(memoryMD)) <= budgetTokens {
		return memoryMD
	}
	limit := max(budgetTokens-approxTokensForBytes(baseBytes), 0)
	for {
		cut := cleanTranscriptString(TruncateTokens(cleanTranscriptString(memoryMD), limit))
		if approxTokensForBytes(baseBytes+len(cut)) <= budgetTokens {
			return cut
		}
		if limit == 0 {
			return ""
		}
		excess := max(approxTokensForBytes(baseBytes+len(cut))-budgetTokens, 1)
		limit -= excess
		if limit < 0 {
			limit = 0
		}
	}
}

// approxTokensForBytes is ApproxTokens applied to a byte size known without
// materializing the text (design §6).
func approxTokensForBytes(size int) int {
	return (size + 3) / 4
}

// phase2UserMessage renders the six inline sections of the phase 2 user
// message, in order (design §7.3). Every extraction contributes exactly its
// raw block and its rollout summary block, and every note exactly its note
// block; fitPhase2Extractions relies on that.
func phase2UserMessage(in Phase2Input, extractions []Extraction, notes []Note, memoryMD, adHocInstructions string) string {
	var b strings.Builder
	b.WriteString(phase2SectionHeader(phase2SectionCurrentSummary))
	b.WriteString(in.CurrentSummary + "\n\n")
	b.WriteString(phase2SectionHeader(phase2SectionCurrentMemory))
	b.WriteString(memoryMD + "\n\n")
	b.WriteString(phase2SectionHeader(phase2SectionChanges))
	changes := in
	changes.Extractions = extractions
	b.WriteString(phase2ChangesText(changes, notes) + "\n\n")
	b.WriteString(phase2SectionHeader(phase2SectionRawMemories))
	b.WriteString("# Raw Memories\n\n")
	for _, e := range extractions {
		b.WriteString(phase2RawBlock(e))
	}
	b.WriteString("\n")
	b.WriteString(phase2SectionHeader(phase2SectionRolloutSummaries))
	for _, e := range extractions {
		b.WriteString(phase2RolloutBlock(in.ProjectLabel, e))
	}
	b.WriteString("\n")
	b.WriteString(phase2SectionHeader(phase2SectionAdHocNotes))
	b.WriteString(adHocInstructions)
	for _, n := range notes {
		b.WriteString(phase2NoteBlock(n))
	}
	return b.String()
}

// phase2SectionHeader is one section-introducing line (design §7.3).
func phase2SectionHeader(name string) string {
	return "===== " + name + " =====\n"
}

// phase2ChangesText renders the CHANGES section (design §7.3): the summary
// names added and removed against the bank's previous publication, plus the
// number of new ad-hoc notes sent in full.
//
// The comparison is against the whole consolidation window, not the budget
// fit: PreviousNames is what the last publication saw, so a previously
// published extraction that a temporary budget squeeze leaves out this round
// stays "unchanged" instead of being reported as removed, and its memory is
// not dropped only because its document was omitted. Extractions that do not
// fit are still left out of the raw memories, the rollout summaries and
// ConsolidatedNames (design §7.3).
func phase2ChangesText(in Phase2Input, notes []Note) string {
	previous := make(map[string]struct{}, len(in.PreviousNames))
	for _, name := range in.PreviousNames {
		previous[name] = struct{}{}
	}
	current := make(map[string]struct{}, len(in.WindowNames))
	var lines []string
	for _, name := range in.WindowNames {
		current[name] = struct{}{}
	}
	for _, e := range in.Extractions {
		name := SummaryName(e)
		if _, ok := previous[name]; !ok {
			lines = append(lines, "added: rollout_summaries/"+name+".md")
		}
	}
	for _, name := range in.PreviousNames {
		if _, ok := current[name]; !ok {
			lines = append(lines, "removed: rollout_summaries/"+name+".md")
		}
	}
	lines = append(lines, phase2NotesCountLine(len(notes)))
	return strings.Join(lines, "\n")
}

// phase2NotesCountLine is the CHANGES notes-count line for n notes sent in
// full. The budget accounting prices that line through this one definition, so
// the measured message size and the rendered message cannot drift apart.
func phase2NotesCountLine(n int) string {
	return fmt.Sprintf("notes: %d new ad-hoc notes", n)
}

// phase2RawBlock is one raw_memories.md entry in the Codex format (design
// §7.3): a ## Thread heading followed by the extraction's raw memory.
func phase2RawBlock(e Extraction) string {
	return "## Thread `" + e.SessionRef + "`\n" + e.RawMemory + "\n\n"
}

// phase2RolloutBlock is one rollout_summaries/ document (design §7.3). Its
// content matches the store's rollout summary document: session ref, project
// label as cwd, the RFC 3339 source-update time as updated_at, then the
// summary.
func phase2RolloutBlock(label string, e Extraction) string {
	return "--- rollout_summaries/" + SummaryName(e) + ".md ---\n" +
		"session: " + e.SessionRef + "\n" +
		"cwd: " + label + "\n" +
		"updated_at: " + time.UnixMilli(e.SourceUpdatedMS).UTC().Format(time.RFC3339) + "\n\n" +
		e.RolloutSummary + "\n"
}

// phase2NoteBlock is one AD-HOC NOTES entry (design §7.3): the Codex
// extension note file name (created time and a short note id) followed by the
// note text.
func phase2NoteBlock(n Note) string {
	return "--- extensions/ad_hoc/notes/" +
		time.UnixMilli(n.CreatedAtMS).UTC().Format(time.RFC3339) + "-" + shortNoteID(n.NoteID) + ".md ---\n" +
		n.Text + "\n"
}

// shortNoteID is the first eight characters of a note ID (design §7.3).
func shortNoteID(noteID string) string {
	if len(noteID) > 8 {
		return noteID[:8]
	}
	return noteID
}

// phase2Envelope is the strict wire shape of a phase 2 result (design §7.3):
// memory_md and memory_summary must both be strings.
type phase2Envelope struct {
	MemoryMD      *string `json:"memory_md"`
	MemorySummary *string `json:"memory_summary"`
}

// ParsePhase2 parses and sanitizes one phase 2 result (design §7.3,
// Requirement 5.5): one surrounding Markdown code fence is stripped, the JSON
// body is decoded strictly (unknown fields and wrong types are rejected), NUL
// bytes are removed and both documents are redacted, and a document over its
// size limit is rejected.
//
// cleanTranscriptString applies NUL removal before Redact, so a secret split
// by a NUL byte is spliced back into the shape the patterns match before the
// redaction runs; Redact is the last byte-changing operation on both returned
// documents (Requirement 3.3).
func ParsePhase2(text string) (Phase2Output, error) {
	dec := json.NewDecoder(strings.NewReader(stripMarkdownFence(text)))
	dec.DisallowUnknownFields()
	var envelope phase2Envelope
	if err := dec.Decode(&envelope); err != nil {
		return Phase2Output{}, fmt.Errorf("%s: phase 2 output: %w", ID, err)
	}
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Phase2Output{}, fmt.Errorf("%s: phase 2 output: trailing content after the JSON object", ID)
	}
	if envelope.MemoryMD == nil {
		return Phase2Output{}, fmt.Errorf("%s: phase 2 output: memory_md must be a string", ID)
	}
	if envelope.MemorySummary == nil {
		return Phase2Output{}, fmt.Errorf("%s: phase 2 output: memory_summary must be a string", ID)
	}
	out := Phase2Output{
		MemoryMD:      cleanTranscriptString(*envelope.MemoryMD),
		MemorySummary: cleanTranscriptString(*envelope.MemorySummary),
	}
	if len(out.MemoryMD) > phase2MemoryMaxBytes {
		return Phase2Output{}, fmt.Errorf("%s: phase 2 output: memory_md exceeds %d bytes", ID, phase2MemoryMaxBytes)
	}
	if len(out.MemorySummary) > phase2SummaryMaxBytes {
		return Phase2Output{}, fmt.Errorf("%s: phase 2 output: memory_summary exceeds %d bytes", ID, phase2SummaryMaxBytes)
	}
	return out, nil
}

// RenderInjection renders the memory instructions injected as a proxy-owned
// system overlay on the first turn of an eligible session (design §7.4,
// Requirement 6.1): the proxy read-path template with the bank's opaque scope
// identifier and the published summary truncated to cfg.SummaryTokens. The
// template keeps the Codex decision boundary, quick memory pass, quick-pass
// budget, verification and answering-from-memory rules, replaces file paths
// with the memory tools and the scope, drops citations, rollout paths and
// skills, and adds the tools-not-available fallback line.
//
// The summary is sanitized before the truncation and the truncated value is
// sanitized again before it is inserted into either rendering branch
// (Requirement 3.3, see cleanInjectionSummary); the scope identifier is a
// trusted opaque hash at the integration seam, not model-authored text, and is
// inserted as it comes in.
func RenderInjection(cfg Config, summary, scopeID string) string {
	rendered := cleanInjectionSummary(summary, cfg.SummaryTokens)
	text, err := templateText(proxyReadPathTemplate)
	if err != nil {
		// The template is embedded at build time, so this branch cannot run in
		// a built binary. Degrade to the summary block rather than return an
		// empty overlay.
		return memorySummaryBlock(rendered)
	}
	return renderTemplate(text,
		templateVar{name: "memory_scope", value: scopeID},
		templateVar{name: "memory_summary", value: rendered},
	)
}

// cleanInjectionSummary sanitizes the published summary, truncates it to limit
// approximate tokens and sanitizes the truncated value again, so the rendered
// summary stays a Redact fixpoint (Requirement 3.3). TruncateTokens is lossy:
// removing bytes can create a word boundary and the inserted marker can supply
// one, so a before-transform pass alone leaves shapes the cut creates, and a
// final pass alone cannot recover fragments of a value the original already
// matched. The published summary is a Redact fixpoint, and the two passes keep
// it one; cleanTranscriptString puts NUL removal before each Redact call.
func cleanInjectionSummary(summary string, limit int) string {
	return cleanTranscriptString(TruncateTokens(cleanTranscriptString(summary), limit))
}

// memorySummaryBlock is the fallback injection body used when the embedded
// read-path template cannot be read.
func memorySummaryBlock(summary string) string {
	return "========= MEMORY_SUMMARY BEGINS =========\n" + summary + "\n========= MEMORY_SUMMARY ENDS =========\n"
}
