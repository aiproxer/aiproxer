package proxymemory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// The document paths of one bank (design §11.1): the handbook, the summary and
// the rollout summaries under the rollout directory.
const (
	documentMemoryMD      = "MEMORY.md"
	documentMemorySummary = "memory_summary.md"
	rolloutSummaryDir     = "rollout_summaries/"

	// scopeIDHexLen is the exact length of the opaque project key (design
	// §11.1).
	scopeIDHexLen = 64
	// searchQueryMaxBytes bounds one trimmed search query (design §11.1).
	searchQueryMaxBytes = 256
	// searchHitMaxBytes is the cut of one returned search hit (design §11.1).
	searchHitMaxBytes = 400
	// noteIDBytes is the entropy of one note identifier (design §11.1).
	noteIDBytes = 16
	// readTruncatedLine is the final line of a size-limited read (design
	// §11.1).
	readTruncatedLine = "…[truncated]\n"
)

// Service is the authenticated read and control surface of the proxy-memory
// feature (design §11.1). Every method that names a memory bank resolves the
// caller's Owner from the caller principal and the opaque scope identifier, so
// a caller can only reach the banks of its own tenant and principal
// (Requirements 7.2, 8.1, 8.5).
//
// The service is synchronous: it performs no I/O of its own, spawns no
// goroutines, and composes only the injected Store, Metrics and clock.
type Service struct {
	cfg       Config
	store     Store
	metrics   *Metrics
	log       *slog.Logger
	now       func() time.Time
	decisions *DecisionCache
}

// NewService builds the service over the feature configuration, the durable
// store, the bounded counters and the control-path clock (design §11.1). An
// optional logger records the unproven deployment facts the model policy
// serves; without one the facts are still counted.
func NewService(cfg Config, store Store, m *Metrics, now func() time.Time, log ...*slog.Logger) *Service {
	return NewServiceWithDecisionCache(cfg, store, m, now, nil, log...)
}

// NewServiceWithDecisionCache builds a service that evicts process-local
// first-turn decisions before durable Forget begins. A nil cache preserves the
// standalone service behavior.
func NewServiceWithDecisionCache(cfg Config, store Store, m *Metrics, now func() time.Time, decisions *DecisionCache, log ...*slog.Logger) *Service {
	if now == nil {
		now = time.Now
	}
	service := &Service{cfg: cfg, store: store, metrics: m, now: now, decisions: decisions}
	if len(log) > 0 {
		service.log = log[0]
	}
	return service
}

// SearchHit is one matching line of one memory document (design §11.1). Line is
// the 1-based line number inside Path and Text is the line content cut to 400
// bytes.
type SearchHit struct {
	Path string
	Line int
	Text string
}

// Caller applies the ordered eligibility rules to one request's principal
// scope snapshot and returns that request's owner principal (design §11.1).
// Eligibility alone decides; no project is involved.
func (s *Service) Caller(sc scope.PrincipalScopeView) (Principal, bool) {
	principal, _, ok := CheckEligible(s.cfg, sc)
	return principal, ok
}

// List returns the caller's own bank statuses (Requirement 8.1).
func (s *Service) List(ctx context.Context, p Principal) ([]BankStatus, error) {
	return s.store.ListBanks(ctx, p)
}

// Status returns one bank's status and its published memory summary
// (Requirement 8.2). A bank that does not exist, or does not belong to the
// caller, is ErrNotFound; a bank without a published summary is its status
// with an empty summary.
func (s *Service) Status(ctx context.Context, p Principal, scopeID string) (BankStatus, string, error) {
	owner, err := s.ownerOf(p, scopeID)
	if err != nil {
		return BankStatus{}, "", err
	}
	status, err := s.store.BankStatus(ctx, owner)
	if err != nil {
		return BankStatus{}, "", err
	}
	summary, _, err := s.store.PublishedSummary(ctx, owner)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			// BankStatus just found the bank, so an empty published summary is
			// the only ErrNotFound left: the store reports a missing bank and
			// an empty summary alike (design §12.6).
			return status, "", nil
		}
		return BankStatus{}, "", err
	}
	return status, summary, nil
}

// Paths lists one bank's document paths in fixed order (design §11.1):
// MEMORY.md, memory_summary.md and the rollout summaries by name.
func (s *Service) Paths(ctx context.Context, p Principal, scopeID string) ([]string, error) {
	owner, err := s.ownerOf(p, scopeID)
	if err != nil {
		return nil, err
	}
	docs, err := s.store.Documents(ctx, owner)
	if err != nil {
		return nil, err
	}
	pages := documentPages(docs)
	paths := make([]string, 0, len(pages))
	for _, page := range pages {
		paths = append(paths, page.path)
	}
	return paths, nil
}

// Read returns one bank document, limited to the requested line range and the
// configured byte limit (Requirement 7.3). Lines are 1-based and inclusive; a
// non-positive bound is unbounded. An unknown path is ErrNotFound.
func (s *Service) Read(ctx context.Context, p Principal, scopeID, path string, startLine, endLine int) (string, error) {
	owner, err := s.ownerOf(p, scopeID)
	if err != nil {
		return "", err
	}
	docs, err := s.store.Documents(ctx, owner)
	if err != nil {
		return "", err
	}
	content, ok := documentContent(docs, path)
	if !ok {
		return "", ErrNotFound
	}
	return s.cutReadOutput(renderLineRange(documentLines(content), startLine, endLine)), nil
}

// Search returns the matching lines of one bank's documents, up to the
// configured result limit (Requirement 7.4). The match is a case-insensitive
// substring match of the trimmed query, which must be non-empty and at most
// 256 bytes, in document path order and then line order. Each hit's text is
// cut to 400 bytes.
func (s *Service) Search(ctx context.Context, p Principal, scopeID, query string) ([]SearchHit, error) {
	owner, err := s.ownerOf(p, scopeID)
	if err != nil {
		return nil, err
	}
	needle := strings.TrimSpace(query)
	if needle == "" || len(needle) > searchQueryMaxBytes {
		return nil, ErrInvalid
	}
	docs, err := s.store.Documents(ctx, owner)
	if err != nil {
		return nil, err
	}
	hits := make([]SearchHit, 0)
	folded := strings.ToLower(needle)
	for _, page := range documentPages(docs) {
		for i, line := range documentLines(page.content) {
			if !strings.Contains(strings.ToLower(line), folded) {
				continue
			}
			hits = append(hits, SearchHit{
				Path: page.path,
				Line: i + 1,
				Text: cutReturnedContent(line, searchHitMaxBytes),
			})
			if len(hits) >= s.cfg.MaxSearchResults {
				return hits, nil
			}
		}
	}
	return hits, nil
}

// AddNote redacts and stores one explicit note for the bank's next
// consolidation (Requirement 7.5). Empty and oversized notes are ErrInvalid,
// and a bank that does not exist is the store's ErrNotFound.
func (s *Service) AddNote(ctx context.Context, p Principal, scopeID, text string) error {
	owner, err := s.ownerOf(p, scopeID)
	if err != nil {
		return err
	}
	note := strings.TrimSpace(text)
	if note == "" || len(note) > s.cfg.MaxNoteBytes {
		return ErrInvalid
	}
	id, err := newNoteID()
	if err != nil {
		return err
	}
	return s.store.AddNote(ctx, owner, Note{
		NoteID:      id,
		Text:        cleanTranscriptString(note),
		CreatedAtMS: s.now().UnixMilli(),
	})
}

// Forget deletes one bank, its sessions, extractions and notes and fences its
// A-legs (Requirement 8.3), then counts control_forget. A bank with nothing to
// forget is the store's ErrNotFound.
func (s *Service) Forget(ctx context.Context, p Principal, scopeID string) error {
	owner, err := s.ownerOf(p, scopeID)
	if err != nil {
		return err
	}
	s.decisions.Forget(owner)
	if err := s.store.Forget(ctx, owner, s.now().UnixMilli()); err != nil {
		return err
	}
	s.metrics.Inc(EventControlForget)
	return nil
}

// ownerOf validates the opaque scope identifier and builds the caller's owner
// triple. A malformed identifier is not found, exactly like an unknown one, and
// is rejected before the store is reached (design §11.1, Requirement 8.5).
func (s *Service) ownerOf(p Principal, scopeID string) (Owner, error) {
	if !validScopeID(scopeID) {
		return Owner{}, ErrNotFound
	}
	return Owner{TenantID: p.TenantID, PrincipalID: p.PrincipalID, ProjectKey: scopeID}, nil
}

// validScopeID reports whether id is the design §11.1 project-key shape:
// exactly 64 lowercase hexadecimal characters.
func validScopeID(id string) bool {
	if len(id) != scopeIDHexLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// documentPage is one document of a bank: its path and its current content.
type documentPage struct {
	path    string
	content string
}

// documentPages lists a bank's documents in the fixed order of design §11.1:
// the handbook, the summary and the rollout summaries ascending by name
// (chronological, because the name starts with the generation timestamp,
// design §7.3).
func documentPages(docs Documents) []documentPage {
	names := make([]string, 0, len(docs.RolloutSummaries))
	for name := range docs.RolloutSummaries {
		names = append(names, name)
	}
	slices.Sort(names)
	pages := make([]documentPage, 0, 2+len(names))
	pages = append(pages,
		documentPage{path: documentMemoryMD, content: docs.MemoryMD},
		documentPage{path: documentMemorySummary, content: docs.MemorySummary},
	)
	for _, name := range names {
		pages = append(pages, documentPage{path: rolloutSummaryDir + name, content: docs.RolloutSummaries[name]})
	}
	return pages
}

// documentContent resolves one document path per design §11.1: the two fixed
// documents or a rollout summary of the bank. Any other path is not found.
func documentContent(docs Documents, path string) (string, bool) {
	for _, page := range documentPages(docs) {
		if page.path == path {
			return page.content, true
		}
	}
	return "", false
}

// documentLines splits one document into lines. A trailing newline does not
// create an empty last line.
func documentLines(content string) []string {
	if content == "" {
		return nil
	}
	lines := strings.Split(content, "\n")
	if last := len(lines) - 1; lines[last] == "" {
		lines = lines[:last]
	}
	return lines
}

// renderLineRange renders the 1-based inclusive [startLine, endLine] window as
// "<n>: <line>\n" per line (design §11.1). A non-positive bound is unbounded; a
// window outside the document renders nothing.
func renderLineRange(lines []string, startLine, endLine int) string {
	start := max(startLine, 1)
	end := len(lines)
	if endLine > 0 && endLine < end {
		end = endLine
	}
	if start > len(lines) || end < start {
		return ""
	}
	var b strings.Builder
	for i := start - 1; i < end; i++ {
		b.WriteString(strconv.Itoa(i + 1))
		b.WriteString(": ")
		b.WriteString(lines[i])
		b.WriteByte('\n')
	}
	return b.String()
}

// cutReadOutput cuts one read output at the configured byte limit and ends it
// with the truncation marker line when content was removed (Requirement 7.3).
// The separator newline and the marker line count inside the limit, so the
// returned document never exceeds MaxReadBytes: Validate rejects positive
// limits below minReadBytesLimit, which cannot fit a complete marker. The cut
// passes through cutReturnedContent, so a cut of stored content cannot leak a
// secret fragment or a boundary it creates (Requirement 3.3).
func (s *Service) cutReadOutput(out string) string {
	if len(out) <= s.cfg.MaxReadBytes {
		return out
	}
	// A config that bypassed Validate is below the marker floor: cut within the
	// cap and return without a marker instead of emitting a partial one.
	if s.cfg.MaxReadBytes < minReadBytesLimit {
		return cutReturnedContent(out, s.cfg.MaxReadBytes)
	}
	// The budget leaves room for the marker line and the separator newline that
	// makes it a line of its own.
	budget := s.cfg.MaxReadBytes - len(readTruncatedLine) - len(readTruncationSeparator)
	body := cutReturnedContent(out, budget)
	if !strings.HasSuffix(body, readTruncationSeparator) {
		body += readTruncationSeparator
	}
	return body + readTruncatedLine
}

// cutReturnedContent returns at most maxBytes bytes of s, cut on a rune
// boundary, as a Redact fixpoint. Cutting is a lossy byte transform on returned
// content, so it follows the ratified before-and-after pattern (Requirement
// 3.3): the value is sanitized before the cut, so the cut cannot leave a
// fragment of a value the original matched, and sanitized again after it, so a
// boundary the cut creates cannot survive. The budget keeps room for the
// redaction marker the final pass may insert at that boundary, so the returned
// value never exceeds maxBytes.
func cutReturnedContent(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	sanitized := cleanTranscriptString(s)
	if len(sanitized) <= maxBytes {
		return sanitized
	}
	budget := max(maxBytes-len(redactedSecret), 0)
	return cleanTranscriptString(cutRunes(sanitized, budget))
}

// cutRunes returns the longest prefix of s that fits limit bytes and does not
// split a UTF-8 rune.
func cutRunes(s string, limit int) string {
	if limit >= len(s) {
		return s
	}
	if limit < 0 {
		limit = 0
	}
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return s[:limit]
}

// newNoteID generates one note identifier: 16 random bytes in lowercase hex
// (design §11.1).
func newNoteID() (string, error) {
	var buf [noteIDBytes]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("%s: note id: %w", ID, err)
	}
	return hex.EncodeToString(buf[:]), nil
}
