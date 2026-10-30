package routing

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ErrInvalidSelector reports a syntactically invalid route selector.
var ErrInvalidSelector = errors.New("routing: invalid route selector")

const maxParallelBranches = 16

// Parse parses a route selector string into an AST.
func Parse(s string) (*Selector, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("%w: empty selector", ErrInvalidSelector)
	}
	globals, rest, err := extractGlobalParams(s)
	if err != nil {
		return nil, err
	}
	s = strings.TrimSpace(rest)
	if s == "" {
		return nil, fmt.Errorf("%w: empty selector", ErrInvalidSelector)
	}
	parts := splitOutsideBrackets(s, '|')
	alts := make([]FailoverAlt, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("%w: empty failover alternative", ErrInvalidSelector)
		}
		alt, err := parseFailoverAlt(part)
		if err != nil {
			return nil, err
		}
		alts = append(alts, alt)
	}
	return &Selector{Alternatives: alts, GlobalTTFTTimeout: globals.ttftTimeout, Affinity: globals.affinity}, nil
}

type globalParams struct {
	ttftTimeout *time.Duration
	affinity    AffinityMode
}

func extractGlobalParams(s string) (globalParams, string, error) {
	var out globalParams
	rest := strings.TrimSpace(s)
	if !strings.HasPrefix(rest, "{") {
		return out, rest, nil
	}
	idx := strings.Index(rest, "}")
	if idx < 0 {
		return globalParams{}, "", fmt.Errorf("%w: unclosed global parameter block", ErrInvalidSelector)
	}
	inside := strings.TrimSpace(rest[1:idx])
	if inside == "" {
		return globalParams{}, "", fmt.Errorf("%w: empty global parameter block", ErrInvalidSelector)
	}
	for entry := range strings.SplitSeq(inside, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			return globalParams{}, "", fmt.Errorf("%w: malformed global parameter list", ErrInvalidSelector)
		}
		key, raw, hasValue := strings.Cut(entry, "=")
		key = strings.ToLower(strings.TrimSpace(key))
		if hasValue {
			raw = strings.TrimSpace(raw)
		}
		switch key {
		case "ttft_timeout":
			if out.ttftTimeout != nil {
				return globalParams{}, "", fmt.Errorf("%w: duplicate ttft_timeout global parameter", ErrInvalidSelector)
			}
			d, err := parsePositiveSecondsDurationAnnotation("ttft_timeout", raw, hasValue)
			if err != nil {
				return globalParams{}, "", err
			}
			out.ttftTimeout = &d
		case "affinity":
			if out.affinity != AffinityNone {
				return globalParams{}, "", fmt.Errorf("%w: duplicate affinity global parameter", ErrInvalidSelector)
			}
			if !hasValue || strings.TrimSpace(raw) == "" {
				return globalParams{}, "", fmt.Errorf("%w: affinity must be session or client", ErrInvalidSelector)
			}
			switch strings.ToLower(strings.TrimSpace(raw)) {
			case string(AffinitySession):
				out.affinity = AffinitySession
			case string(AffinityClient):
				out.affinity = AffinityClient
			default:
				return globalParams{}, "", fmt.Errorf("%w: affinity must be session or client", ErrInvalidSelector)
			}
		case "session_sticky", "client_sticky":
			if out.affinity != AffinityNone {
				return globalParams{}, "", fmt.Errorf("%w: duplicate affinity global parameter", ErrInvalidSelector)
			}
			if hasValue {
				return globalParams{}, "", fmt.Errorf("%w: %s does not take a value", ErrInvalidSelector, key)
			}
			if key == "session_sticky" {
				out.affinity = AffinitySession
			} else {
				out.affinity = AffinityClient
			}
		default:
			return globalParams{}, "", fmt.Errorf("%w: unsupported global parameter key %q", ErrInvalidSelector, key)
		}
	}
	rest = strings.TrimSpace(rest[idx+1:])
	if strings.HasPrefix(rest, "{") {
		return globalParams{}, "", fmt.Errorf("%w: duplicate global parameter block", ErrInvalidSelector)
	}
	return out, rest, nil
}

func parseFailoverAlt(s string) (FailoverAlt, error) {
	s = strings.TrimSpace(s)
	hasBang := hasTopLevelBang(s)
	hasCaret := hasTopLevelCaret(s)
	hasWeightAnn := hasWeightedAnnotationPrefix(s)

	if hasBang && (hasCaret || hasWeightAnn) {
		// Narrow hybrid exception (req 7.1/7.4/7.5): one thinker weighted branch
		// plus one non-thinker weighted branch whose target is an embedded
		// parallel executor group. Anything else stays a general mixing rejection.
		if w, herr := tryParseThinkerParallelHybrid(s); herr != nil {
			return FailoverAlt{}, herr
		} else if w != nil {
			if err := validateWeightedFirst(w); err != nil {
				return FailoverAlt{}, err
			}
			if err := validateWeightedThinker(w); err != nil {
				return FailoverAlt{}, err
			}
			return FailoverAlt{Weighted: w}, nil
		}
		return FailoverAlt{}, fmt.Errorf("%w: parallel '!' and weighted '^'/[weight]/[first] cannot be mixed in one arm", ErrInvalidSelector)
	}
	if hasBang {
		p, err := parseParallel(s)
		if err != nil {
			return FailoverAlt{}, err
		}
		return FailoverAlt{Parallel: p}, nil
	}
	if hasCaret || hasWeightAnn {
		w, err := parseWeighted(s)
		if err != nil {
			return FailoverAlt{}, err
		}
		if err := validateWeightedFirst(w); err != nil {
			return FailoverAlt{}, err
		}
		if err := validateWeightedThinker(w); err != nil {
			return FailoverAlt{}, err
		}
		return FailoverAlt{Weighted: w}, nil
	}
	p, err := parsePrimary(s)
	if err != nil {
		return FailoverAlt{}, err
	}
	return FailoverAlt{Primary: &p}, nil
}

func hasWeightedAnnotationPrefix(s string) bool {
	s = strings.TrimSpace(s)
	for strings.HasPrefix(s, "[") {
		idx := strings.Index(s, "]")
		if idx < 0 {
			return true
		}
		inside := s[1:idx]
		for entry := range strings.SplitSeq(inside, ",") {
			key := strings.TrimSpace(entry)
			if left, _, ok := strings.Cut(key, "="); ok {
				key = left
			}
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "weight", "first", "thinker":
				return true
			}
		}
		s = strings.TrimSpace(s[idx+1:])
	}
	return false
}

func hasTopLevelBang(s string) bool {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '[':
			depth++
		case ']':
			depth--
		case '!':
			if depth == 0 {
				return true
			}
		}
	}
	return false
}

func parseParallel(s string) (*Parallel, error) {
	parts := splitParallelLegs(s)
	if len(parts) > maxParallelBranches {
		return nil, fmt.Errorf("%w: too many parallel branches (max %d)", ErrInvalidSelector, maxParallelBranches)
	}
	branches := make([]ParallelBranch, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("%w: empty parallel branch", ErrInvalidSelector)
		}
		b, err := parseParallelBranch(part)
		if err != nil {
			return nil, err
		}
		branches = append(branches, b)
	}
	if len(branches) == 0 {
		return nil, fmt.Errorf("%w: no parallel branches", ErrInvalidSelector)
	}
	return &Parallel{Branches: branches}, nil
}

func parseParallelBranch(s string) (ParallelBranch, error) {
	ann, rest, err := extractPrefixAnnotations(s)
	if err != nil {
		return ParallelBranch{}, err
	}
	if ann.weight != nil || ann.first {
		return ParallelBranch{}, fmt.Errorf("%w: [weight] and [first] are not valid on parallel branches", ErrInvalidSelector)
	}
	if ann.thinker {
		return ParallelBranch{}, fmt.Errorf("%w: [thinker] is only valid on weighted branches", ErrInvalidSelector)
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return ParallelBranch{}, fmt.Errorf("%w: missing primary after annotations", ErrInvalidSelector)
	}
	p, err := parsePrimaryWithAnnotations(rest, ann)
	if err != nil {
		return ParallelBranch{}, err
	}
	var handicap time.Duration
	if ann.handicap != nil {
		handicap = *ann.handicap
	}
	return ParallelBranch{Target: p, Handicap: handicap}, nil
}

// splitParallelLegs splits on '!' at bracket depth 0.
// Query-embedded '!' must be percent-encoded (%21) in parallel selectors.
func splitParallelLegs(s string) []string {
	return splitOutsideBrackets(s, '!')
}

func hasTopLevelCaret(s string) bool {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '[':
			depth++
		case ']':
			depth--
		case '^':
			if depth == 0 {
				return true
			}
		}
	}
	return false
}

func validateWeightedFirst(w *Weighted) error {
	if w == nil {
		return nil
	}
	n := 0
	for _, b := range w.Branches {
		if b.IsFirst {
			n++
		}
	}
	if n > 1 {
		return fmt.Errorf("%w: at most one [first] branch is allowed in a weighted selector", ErrInvalidSelector)
	}
	return nil
}

func validateWeightedThinker(w *Weighted) error {
	if w == nil {
		return nil
	}
	thinkerCount := 0
	executorCount := 0
	for _, b := range w.Branches {
		if b.IsThinker {
			thinkerCount++
		} else {
			executorCount++
		}
	}
	if thinkerCount > 1 {
		return fmt.Errorf("%w: at most one [thinker] branch is allowed in a weighted selector", ErrInvalidSelector)
	}
	if thinkerCount > 0 && executorCount == 0 {
		return fmt.Errorf("%w: weighted selector with [thinker] requires at least one non-thinker branch", ErrInvalidSelector)
	}
	return nil
}

// tryParseThinkerParallelHybrid parses the narrow thinker-plus-parallel-executor
// hybrid weighted form. It returns (nil, nil) when the input does not match the
// hybrid shape so the caller falls back to the general weighted/parallel mixing
// rejection. It returns a parsed *Weighted on success, or an error when the
// shape matches the hybrid but is invalid.
func tryParseThinkerParallelHybrid(s string) (*Weighted, error) {
	parts := splitOutsideBrackets(s, '^')
	if len(parts) != 2 {
		return nil, nil
	}
	t0 := prefixHasThinker(parts[0])
	t1 := prefixHasThinker(parts[1])
	thinkerCount := 0
	if t0 {
		thinkerCount++
	}
	if t1 {
		thinkerCount++
	}
	if thinkerCount != 1 {
		return nil, nil
	}
	thinkerIdx := 0
	if t1 {
		thinkerIdx = 1
	}
	executorIdx := 1 - thinkerIdx
	if hasTopLevelBang(parts[thinkerIdx]) {
		return nil, fmt.Errorf("%w: [thinker] branch cannot target an embedded parallel executor group", ErrInvalidSelector)
	}
	if !hasTopLevelBang(parts[executorIdx]) {
		return nil, nil
	}
	thinker, err := parseWeightedBranch(parts[thinkerIdx])
	if err != nil {
		return nil, err
	}
	if !thinker.IsThinker {
		return nil, fmt.Errorf("%w: [thinker] annotation required on thinker branch", ErrInvalidSelector)
	}
	executor, err := parseWeightedBranchWithParallelTarget(parts[executorIdx])
	if err != nil {
		return nil, err
	}
	var branches []WeightedBranch
	if thinkerIdx == 0 {
		branches = []WeightedBranch{thinker, executor}
	} else {
		branches = []WeightedBranch{executor, thinker}
	}
	return &Weighted{Branches: branches}, nil
}

// parseWeightedBranchWithParallelTarget parses the non-thinker executor branch
// of a thinker hybrid: the entire branch text is an embedded parallel executor
// group, and the weighted weight is fixed to 1.
// Per-leg annotations (handicap, ttft_timeout, max_context) live inside the
// embedded parallel text; annotations that are invalid on parallel legs
// ([weight], [first], [thinker]) are rejected by parseParallelBranch.
func parseWeightedBranchWithParallelTarget(s string) (WeightedBranch, error) {
	rest := strings.TrimSpace(s)
	if rest == "" {
		return WeightedBranch{}, fmt.Errorf("%w: missing parallel executor expression", ErrInvalidSelector)
	}
	if !hasTopLevelBang(rest) {
		return WeightedBranch{}, fmt.Errorf("%w: embedded executor branch must be a parallel group", ErrInvalidSelector)
	}
	p, err := parseParallel(rest)
	if err != nil {
		return WeightedBranch{}, err
	}
	return WeightedBranch{Weight: 1, Parallel: p}, nil
}

// prefixHasThinker reports whether s begins with a [...] prefix annotation
// block containing a thinker key. It mirrors hasWeightedAnnotationPrefix but
// only looks for thinker, and never returns an error.
func prefixHasThinker(s string) bool {
	rest := strings.TrimSpace(s)
	for strings.HasPrefix(rest, "[") {
		idx := strings.Index(rest, "]")
		if idx < 0 {
			return false
		}
		inside := rest[1:idx]
		for entry := range strings.SplitSeq(inside, ",") {
			key := strings.TrimSpace(entry)
			if left, _, ok := strings.Cut(key, "="); ok {
				key = left
			}
			if strings.ToLower(strings.TrimSpace(key)) == "thinker" {
				return true
			}
		}
		rest = strings.TrimSpace(rest[idx+1:])
	}
	return false
}

func parseWeighted(s string) (*Weighted, error) {
	parts := splitOutsideBrackets(s, '^')
	branches := make([]WeightedBranch, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("%w: empty weighted branch", ErrInvalidSelector)
		}
		b, err := parseWeightedBranch(part)
		if err != nil {
			return nil, err
		}
		branches = append(branches, b)
	}
	if len(branches) == 0 {
		return nil, fmt.Errorf("%w: no weighted branches", ErrInvalidSelector)
	}
	return &Weighted{Branches: branches}, nil
}

func parseWeightedBranch(s string) (WeightedBranch, error) {
	ann, rest, err := extractPrefixAnnotations(s)
	if err != nil {
		return WeightedBranch{}, err
	}
	if ann.handicap != nil {
		return WeightedBranch{}, fmt.Errorf("%w: [handicap] is only valid on parallel branches", ErrInvalidSelector)
	}
	weight := 1
	if ann.weight != nil {
		weight = *ann.weight
	}
	first := ann.first
	thinker := ann.thinker
	if first && thinker {
		return WeightedBranch{}, fmt.Errorf("%w: [first] and [thinker] cannot be combined on the same branch", ErrInvalidSelector)
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return WeightedBranch{}, fmt.Errorf("%w: missing primary after annotations", ErrInvalidSelector)
	}
	p, err := parsePrimaryWithAnnotations(rest, ann)
	if err != nil {
		return WeightedBranch{}, err
	}
	return WeightedBranch{Weight: weight, IsFirst: first, IsThinker: thinker, Target: p}, nil
}

func parsePrimary(s string) (Primary, error) {
	ann, rest, err := extractPrefixAnnotations(s)
	if err != nil {
		return Primary{}, err
	}
	if ann.weight != nil || ann.first {
		return Primary{}, fmt.Errorf("%w: [weight] and [first] are only valid on weighted branches", ErrInvalidSelector)
	}
	if ann.thinker {
		return Primary{}, fmt.Errorf("%w: [thinker] is only valid on weighted branches", ErrInvalidSelector)
	}
	if ann.handicap != nil {
		return Primary{}, fmt.Errorf("%w: [handicap] is only valid on parallel branches", ErrInvalidSelector)
	}
	return parsePrimaryWithAnnotations(rest, ann)
}

func parsePrimaryWithAnnotations(s string, ann prefixAnnotations) (Primary, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return Primary{}, fmt.Errorf("%w: empty primary", ErrInvalidSelector)
	}
	path, queryStr, hasQuery := strings.Cut(raw, "?")
	var vals url.Values
	if hasQuery {
		q, err := url.ParseQuery(queryStr)
		if err != nil {
			return Primary{}, fmt.Errorf("%w: parse query: %w", ErrInvalidSelector, err)
		}
		vals = q
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return Primary{}, fmt.Errorf("%w: missing model in primary", ErrInvalidSelector)
	}
	if containsGlobalParamBlock(path) {
		return Primary{}, fmt.Errorf("%w: global parameters must appear before the first leaf", ErrInvalidSelector)
	}
	backend, model, hasColon := strings.Cut(path, ":")
	if !hasColon {
		backend = ""
		model = path
	}
	backend = strings.TrimSpace(backend)
	model = strings.TrimSpace(model)
	if model == "" {
		return Primary{}, fmt.Errorf("%w: model is required", ErrInvalidSelector)
	}
	if strings.Contains(backend, "|") || strings.Contains(model, "|") {
		return Primary{}, fmt.Errorf("%w: unexpected '|' in primary (use failover '|' at top level)", ErrInvalidSelector)
	}
	return Primary{Backend: backend, Model: model, Params: vals, Size: ann.size, TTFTTimeout: ann.ttftTimeout}, nil
}

func containsGlobalParamBlock(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '{' || (i > 0 && s[i-1] == '$') {
			continue
		}
		end := strings.IndexByte(s[i+1:], '}')
		if end < 0 {
			continue
		}
		inside := strings.TrimSpace(s[i+1 : i+1+end])
		key, _, _ := strings.Cut(inside, "=")
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "ttft_timeout", "affinity", "session_sticky", "client_sticky":
			return true
		}
		i += end + 1
	}
	return false
}

type prefixAnnotations struct {
	weight      *int
	first       bool
	thinker     bool
	size        RequestSizeConstraint
	ttftTimeout *time.Duration
	handicap    *time.Duration
}

func extractPrefixAnnotations(s string) (prefixAnnotations, string, error) {
	var ann prefixAnnotations
	rest := strings.TrimSpace(s)
	for strings.HasPrefix(rest, "[") {
		idx := strings.Index(rest, "]")
		if idx < 0 {
			return prefixAnnotations{}, "", fmt.Errorf("%w: unclosed annotation prefix", ErrInvalidSelector)
		}
		inside := strings.TrimSpace(rest[1:idx])
		if inside == "" {
			return prefixAnnotations{}, "", fmt.Errorf("%w: empty annotation prefix", ErrInvalidSelector)
		}
		for entry := range strings.SplitSeq(inside, ",") {
			entry = strings.TrimSpace(entry)
			if entry == "" {
				return prefixAnnotations{}, "", fmt.Errorf("%w: malformed annotation list", ErrInvalidSelector)
			}
			key, raw, hasValue := strings.Cut(entry, "=")
			key = strings.ToLower(strings.TrimSpace(key))
			if hasValue {
				raw = strings.TrimSpace(raw)
			}
			switch key {
			case "weight":
				if ann.weight != nil {
					return prefixAnnotations{}, "", fmt.Errorf("%w: duplicate [weight=N] annotation", ErrInvalidSelector)
				}
				n, err := parsePositiveIntAnnotation("weight", raw, hasValue)
				if err != nil {
					return prefixAnnotations{}, "", err
				}
				ann.weight = &n
			case "first":
				if ann.first {
					return prefixAnnotations{}, "", fmt.Errorf("%w: duplicate [first] annotation", ErrInvalidSelector)
				}
				if hasValue && strings.TrimSpace(raw) != "" {
					return prefixAnnotations{}, "", fmt.Errorf("%w: [first] does not take a value", ErrInvalidSelector)
				}
				ann.first = true
			case "thinker":
				if ann.thinker {
					return prefixAnnotations{}, "", fmt.Errorf("%w: duplicate [thinker] annotation", ErrInvalidSelector)
				}
				on, err := parseThinkerValue(raw, hasValue)
				if err != nil {
					return prefixAnnotations{}, "", err
				}
				ann.thinker = on
			case "max_context":
				if ann.size.MaxContextTokens != nil {
					return prefixAnnotations{}, "", fmt.Errorf("%w: duplicate [max_context=N] annotation", ErrInvalidSelector)
				}
				n, err := parsePositiveTokenCountAnnotation("max_context", raw, hasValue)
				if err != nil {
					return prefixAnnotations{}, "", err
				}
				ann.size.MaxContextTokens = &n
			case "min_context":
				if ann.size.MinContextTokens != nil {
					return prefixAnnotations{}, "", fmt.Errorf("%w: duplicate [min_context=N] annotation", ErrInvalidSelector)
				}
				n, err := parsePositiveTokenCountAnnotation("min_context", raw, hasValue)
				if err != nil {
					return prefixAnnotations{}, "", err
				}
				ann.size.MinContextTokens = &n
			case "ttft_timeout":
				if ann.ttftTimeout != nil {
					return prefixAnnotations{}, "", fmt.Errorf("%w: duplicate [ttft_timeout=N] annotation", ErrInvalidSelector)
				}
				d, err := parsePositiveSecondsDurationAnnotation("ttft_timeout", raw, hasValue)
				if err != nil {
					return prefixAnnotations{}, "", err
				}
				ann.ttftTimeout = &d
			case "handicap":
				if ann.handicap != nil {
					return prefixAnnotations{}, "", fmt.Errorf("%w: duplicate [handicap=N] annotation", ErrInvalidSelector)
				}
				d, err := parsePositiveSecondsDurationAnnotation("handicap", raw, hasValue)
				if err != nil {
					return prefixAnnotations{}, "", err
				}
				ann.handicap = &d
			default:
				return prefixAnnotations{}, "", fmt.Errorf("%w: unsupported annotation key %q", ErrInvalidSelector, key)
			}
		}
		rest = strings.TrimSpace(rest[idx+1:])
	}
	if ann.size.MinContextTokens != nil && ann.size.MaxContextTokens != nil && *ann.size.MinContextTokens >= *ann.size.MaxContextTokens {
		return prefixAnnotations{}, "", fmt.Errorf("%w: min_context must be less than max_context", ErrInvalidSelector)
	}
	return ann, rest, nil
}

func parsePositiveIntAnnotation(key, raw string, hasValue bool) (int, error) {
	n, err := parsePositiveInt64Annotation(key, raw, hasValue)
	if err != nil {
		return 0, err
	}
	if n > int64(int(^uint(0)>>1)) {
		return 0, fmt.Errorf("%w: %s is too large", ErrInvalidSelector, key)
	}
	return int(n), nil
}

// parsePositiveTokenCountAnnotation accepts decimal token counts with optional K/M suffixes.
func parsePositiveTokenCountAnnotation(key, raw string, hasValue bool) (int64, error) {
	if !hasValue || strings.TrimSpace(raw) == "" {
		return 0, fmt.Errorf("%w: %s must be a positive token count with optional K/M suffix", ErrInvalidSelector, key)
	}
	s := strings.TrimSpace(raw)
	multiplier := int64(1)
	switch s[len(s)-1] {
	case 'k', 'K':
		multiplier = 1_000
		s = strings.TrimSpace(s[:len(s)-1])
	case 'm', 'M':
		multiplier = 1_000_000
		s = strings.TrimSpace(s[:len(s)-1])
	}
	if s == "" {
		return 0, fmt.Errorf("%w: %s must be a positive token count with optional K/M suffix", ErrInvalidSelector, key)
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%w: %s must be a positive token count with optional K/M suffix", ErrInvalidSelector, key)
	}
	if n > math.MaxInt64/multiplier {
		return 0, fmt.Errorf("%w: %s is too large", ErrInvalidSelector, key)
	}
	return n * multiplier, nil
}

func parsePositiveInt64Annotation(key, raw string, hasValue bool) (int64, error) {
	if !hasValue || strings.TrimSpace(raw) == "" {
		return 0, fmt.Errorf("%w: %s must be a positive integer", ErrInvalidSelector, key)
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%w: %s must be a positive integer", ErrInvalidSelector, key)
	}
	return n, nil
}

func parsePositiveSecondsDurationAnnotation(key, raw string, hasValue bool) (time.Duration, error) {
	n, err := parsePositiveInt64Annotation(key, raw, hasValue)
	if err != nil {
		return 0, err
	}
	if n > int64((1<<63-1)/time.Second) {
		return 0, fmt.Errorf("%w: %s is too large", ErrInvalidSelector, key)
	}
	return time.Duration(n) * time.Second, nil
}

// parseThinkerValue interprets a [thinker] annotation value. The bare form and
// the true-valued forms (1, yes, true) mark the branch as thinker; false-valued
// forms (0, no, false), the empty value, and any other value are rejected.
func parseThinkerValue(raw string, hasValue bool) (bool, error) {
	if !hasValue {
		return true, nil
	}
	v := strings.ToLower(strings.TrimSpace(raw))
	switch v {
	case "1", "yes", "true":
		return true, nil
	case "0", "no", "false", "":
		return false, fmt.Errorf("%w: [thinker] must be a true-valued boolean", ErrInvalidSelector)
	default:
		return false, fmt.Errorf("%w: [thinker] must be a true-valued boolean", ErrInvalidSelector)
	}
}

func splitOutsideBrackets(s string, sep byte) []string {
	out := []string{}
	depth := 0
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '[':
			depth++
		case ']':
			depth--
		case sep:
			if depth == 0 && s[i] == sep {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	out = append(out, s[start:])
	return out
}
