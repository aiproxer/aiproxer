package secretguard

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"unicode/utf16"
	"unicode/utf8"

	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

var errJSONOccurrenceMapping = errors.New("secretguard: JSON occurrence mapping failed")

// jsonStringMapping maps bytes in one decoded JSON string or raw scalar token
// back to the source byte interval that produced each decoded byte.
type jsonStringMapping struct {
	decoded    []byte
	boundaries []int
	rawStart   int    // identity mappings borrow raw bytes and need only an offset
	encoded    []byte // streamed non-identity strings defer detailed boundaries until a hit
	spans      []jsonMappingSpan
	mapped     bool
}

// Only non-identity rune intervals need records. ASCII stretches between them
// use offsets; dense exceptional input falls back to one bounded int map.
type jsonMappingSpan struct {
	decodedStart, decodedEnd int
	rawStart, rawEnd         int
}

// collectExactJSONOccurrences maps each semantic token that the canonical JSON
// scanner visits. It deliberately does not scan the raw JSON in addition to
// these tokens: punctuation, escape digits, and duplicate object entries are
// not independent canonical occurrences.
func collectExactJSONOccurrences(m exactOccurrenceMatcher, raw []byte, fieldID string) []betterLeaksOccurrence {
	out, _ := collectExactJSONOccurrencesMapped(m, raw, fieldID)
	return out
}

func collectExactJSONOccurrencesMapped(m exactOccurrenceMatcher, raw []byte, fieldID string) ([]betterLeaksOccurrence, bool) {
	if m == nil || len(raw) == 0 {
		return nil, false
	}
	return collectExactJSONOccurrencesStreaming(m, raw, fieldID, false)
}

func collectExactJSONRedactOccurrences(m exactOccurrenceMatcher, raw []byte, fieldID string) ([]betterLeaksOccurrence, bool) {
	if m == nil || len(raw) == 0 {
		return nil, false
	}
	return collectExactJSONOccurrencesStreaming(m, raw, fieldID, true)
}

func collectExactJSONOccurrencesStreaming(m exactOccurrenceMatcher, raw []byte, fieldID string, redact bool) ([]betterLeaksOccurrence, bool) {
	var out []betterLeaksOccurrence
	locationIndex := newBetterLeaksLocationIndex(raw)
	err := walkJSONOccurrenceTokens(raw, func(token jsonStringMapping, stringValue, key bool) bool {
		occurrences := m.ScanOccurrences(token.decoded)
		out = appendExactJSONTokenOccurrences(out, occurrences, token, raw, fieldID, locationIndex)
		return !redact || (stringValue && !key) || len(occurrences) == 0
	})
	if err != nil {
		return nil, false
	}
	return out, true
}

func appendExactJSONTokenOccurrences(out []betterLeaksOccurrence, occurrences []sdk.PositionalOccurrence, token jsonStringMapping, raw []byte, fieldID string, locationIndex *betterLeaksLocationIndex) []betterLeaksOccurrence {
	for _, occurrence := range occurrences {
		if occurrence.Start < 0 || occurrence.End <= occurrence.Start || occurrence.End > len(token.decoded) {
			continue
		}
		rawStart, rawEnd, ok := token.rawRange(occurrence.Start, occurrence.End)
		if !ok {
			continue
		}
		span, err := spanForByteRangeWithLocationIndex(raw, locationIndex, rawStart, rawEnd)
		if err != nil {
			continue
		}
		out = append(out, betterLeaksOccurrence{
			value:          bytes.Clone(token.decoded[occurrence.Start:occurrence.End]),
			span:           span,
			start:          rawStart,
			end:            rawEnd,
			offsetsValid:   true,
			fieldID:        fieldID,
			ruleID:         occurrence.Finding.SecretRefName,
			role:           betterLeaksOccurrencePrimary,
			representation: betterLeaksOccurrenceDecoded,
		})
	}
	return out
}

// materializeBoundaries retains one detailed map on the token that needs it.
// Identity tokens and escaped tokens without partial-span lookups stay cheap.
func (m *jsonStringMapping) materializeBoundaries() bool {
	if m.encoded == nil {
		return true
	}
	decoded := 0
	for pos := 0; pos < len(m.encoded); {
		start := pos
		rawWidth, decodedWidth := 1, 1
		if m.encoded[pos] >= utf8.RuneSelf {
			r, width := utf8.DecodeRune(m.encoded[pos:])
			rawWidth, decodedWidth = width, width
			if r == utf8.RuneError && width == 1 {
				decodedWidth = utf8.RuneLen(utf8.RuneError)
			}
		} else if m.encoded[pos] == '\\' {
			if pos+1 >= len(m.encoded) {
				return false
			}
			rawWidth = 2
			if m.encoded[pos+1] == 'u' {
				if pos+6 > len(m.encoded) {
					return false
				}
				first, ok := jsonHexQuad(m.encoded[pos+2 : pos+6])
				if !ok {
					return false
				}
				code := rune(first)
				rawWidth = 6
				if first >= 0xD800 && first <= 0xDBFF && pos+12 <= len(m.encoded) && m.encoded[pos+6] == '\\' && m.encoded[pos+7] == 'u' {
					second, valid := jsonHexQuad(m.encoded[pos+8 : pos+12])
					if valid && second >= 0xDC00 && second <= 0xDFFF {
						code = utf16.DecodeRune(rune(first), rune(second))
						rawWidth = 12
					}
				}
				if code >= 0xD800 && code <= 0xDFFF {
					code = utf8.RuneError
				}
				decodedWidth = utf8.RuneLen(code)
			}
		}
		pos += rawWidth
		if decoded+decodedWidth > len(m.decoded) {
			return false
		}
		if m.boundaries != nil {
			for i := decoded + 1; i <= decoded+decodedWidth; i++ {
				m.boundaries[i] = m.rawStart + pos
			}
		} else if rawWidth != 1 || decodedWidth != 1 {
			// Four ints per span: switch before retained spans can exceed half
			// the dense map. This also bounds multibyte/invalid/escape-dense input.
			if len(m.spans)+1 > (len(m.decoded)+1)/8 {
				m.makeDenseBoundaries(decoded)
				for i := decoded + 1; i <= decoded+decodedWidth; i++ {
					m.boundaries[i] = m.rawStart + pos
				}
			} else {
				m.spans = append(m.spans, jsonMappingSpan{decoded, decoded + decodedWidth, m.rawStart + start, m.rawStart + pos})
			}
		}
		decoded += decodedWidth
	}
	if decoded != len(m.decoded) {
		return false
	}
	m.mapped = true
	m.encoded = nil
	return true
}

func (m *jsonStringMapping) makeDenseBoundaries(decodedEnd int) {
	m.boundaries = make([]int, len(m.decoded)+1)
	decoded, raw := 0, m.rawStart
	for _, span := range m.spans {
		for ; decoded <= span.decodedStart; decoded++ {
			m.boundaries[decoded] = raw
			raw++
		}
		for ; decoded <= span.decodedEnd; decoded++ {
			m.boundaries[decoded] = span.rawEnd
		}
		raw = span.rawEnd + 1
	}
	for ; decoded <= decodedEnd; decoded++ {
		m.boundaries[decoded] = raw
		raw++
	}
	m.spans = nil
}

func (m *jsonStringMapping) boundaryAt(index int) int {
	if m.boundaries != nil {
		return m.boundaries[index]
	}
	next := sort.Search(len(m.spans), func(i int) bool { return m.spans[i].decodedEnd >= index })
	if next < len(m.spans) && index > m.spans[next].decodedStart {
		return m.spans[next].rawEnd
	}
	if next == 0 {
		return m.rawStart + index
	}
	previous := m.spans[next-1]
	return previous.rawEnd + index - previous.decodedEnd
}

func (m *jsonStringMapping) rawRange(start, end int) (int, int, bool) {
	if start < 0 || end <= start || end > len(m.decoded) {
		return 0, 0, false
	}
	if m.boundaries == nil {
		if m.encoded != nil {
			if start == 0 && end == len(m.decoded) {
				return m.rawStart, m.rawStart + len(m.encoded), true
			}
			if !m.materializeBoundaries() {
				return 0, 0, false
			}
			return m.rawRange(start, end)
		}
		return m.boundaryAt(start), m.boundaryAt(end), m.boundaryAt(start) < m.boundaryAt(end)
	}
	if len(m.boundaries) != len(m.decoded)+1 {
		return 0, 0, false
	}
	rawStart, rawEnd := m.boundaries[start], m.boundaries[end]
	return rawStart, rawEnd, rawStart < rawEnd
}

func firstJSONOccurrenceValue(raw []byte) ([]byte, error) {
	// Complete values need syntax validation only. json.Valid scans the
	// admitted bytes directly, avoiding the decoder's growing input copy on
	// every occurrence traversal. Trim only JSON whitespace to preserve the
	// canonical decoder's first-value InputOffset.
	if json.Valid(raw) {
		return bytes.TrimRight(raw, " \t\r\n"), nil
	}
	// Validate and bound the same first JSON value as canonical decoding.
	// The decoder validates syntax before calling UnmarshalJSON. Discard that
	// validated value rather than cloning it or building a second decoded tree;
	// scalar spellings are read directly from the admitted raw bytes.
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var first jsonOccurrenceValidationTarget
	if err := dec.Decode(&first); err != nil {
		return nil, errJSONOccurrenceMapping
	}
	return raw[:int(dec.InputOffset())], nil
}

type jsonOccurrenceValidationTarget struct{}

func (*jsonOccurrenceValidationTarget) UnmarshalJSON([]byte) error { return nil }

// jsonOccurrenceParser maps the first JSON value already validated and bounded
// by firstJSONOccurrenceValue. It is not an independent JSON validator.
type jsonOccurrenceParser struct {
	raw       []byte
	pos       int
	valueEnds map[int]int
}

// walkJSONOccurrenceTokens visits canonical semantic tokens without retaining
// an array-sized mapping tree. Only objects need a key index: last duplicate
// wins, then keys and values are visited in the canonical sorted order.
func walkJSONOccurrenceTokens(raw []byte, visit func(jsonStringMapping, bool, bool) bool) error {
	first, err := firstJSONOccurrenceValue(raw)
	if err != nil {
		return err
	}
	p := jsonOccurrenceParser{raw: first}
	_, err = p.walkTokens(visit)
	return err
}

type jsonOccurrenceObjectSpan struct {
	key   jsonStringMapping
	start int
}

func (p *jsonOccurrenceParser) walkTokens(visit func(jsonStringMapping, bool, bool) bool) (bool, error) {
	p.skipSpace()
	if p.pos >= len(p.raw) {
		return false, errJSONOccurrenceMapping
	}
	switch p.raw[p.pos] {
	case '{':
		p.pos++
		p.skipSpace()
		if p.consume('}') {
			return true, nil
		}
		entries := make(map[string]jsonOccurrenceObjectSpan)
		for {
			p.skipSpace()
			key, err := p.streamingString()
			if err != nil {
				return false, err
			}
			p.skipSpace()
			if !p.consume(':') {
				return false, errJSONOccurrenceMapping
			}
			p.skipSpace()
			entries[string(key.decoded)] = jsonOccurrenceObjectSpan{key: key, start: p.pos}
			if err := p.skipValue(); err != nil {
				return false, err
			}
			p.skipSpace()
			if p.consume('}') {
				break
			}
			if !p.consume(',') {
				return false, errJSONOccurrenceMapping
			}
		}
		keys := make([]string, 0, len(entries))
		for key := range entries {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			entry := entries[key]
			if !visit(entry.key, true, true) {
				return false, nil
			}
			child := jsonOccurrenceParser{raw: p.raw, pos: entry.start, valueEnds: p.valueEnds}
			if proceed, err := child.walkTokens(visit); !proceed || err != nil {
				return proceed, err
			}
		}
		return true, nil
	case '[':
		p.pos++
		p.skipSpace()
		if p.consume(']') {
			return true, nil
		}
		for {
			if proceed, err := p.walkTokens(visit); !proceed || err != nil {
				return proceed, err
			}
			p.skipSpace()
			if p.consume(']') {
				return true, nil
			}
			if !p.consume(',') {
				return false, errJSONOccurrenceMapping
			}
		}
	case '"':
		mapping, err := p.streamingString()
		if err != nil {
			return false, err
		}
		return visit(mapping, true, false), nil
	default:
		mapping, err := p.literal()
		if err != nil {
			return false, err
		}
		return visit(mapping, false, false), nil
	}
}

// skipValue advances over already validated syntax without decoding or keeping
// mappings for overwritten object entries and deferred child values.
func (p *jsonOccurrenceParser) skipValue() error {
	p.skipSpace()
	if p.pos >= len(p.raw) {
		return errJSONOccurrenceMapping
	}
	if p.raw[p.pos] == '"' {
		_, _, err := p.stringRange()
		return err
	}
	if p.raw[p.pos] != '{' && p.raw[p.pos] != '[' {
		_, err := p.literal()
		return err
	}
	if end, cached := p.valueEnds[p.pos]; cached {
		p.pos = end
		return nil
	}
	// Deferred nested objects share these container ends. Without the cache,
	// each enclosing object would rescan the same large descendant payload.
	if p.valueEnds == nil {
		p.valueEnds = make(map[int]int)
	}
	var starts []int
	for p.pos < len(p.raw) {
		switch p.raw[p.pos] {
		case '"':
			if _, _, err := p.stringRange(); err != nil {
				return err
			}
			continue
		case '{', '[':
			starts = append(starts, p.pos)
		case '}', ']':
			p.pos++
			start := starts[len(starts)-1]
			starts = starts[:len(starts)-1]
			p.valueEnds[start] = p.pos
			if len(starts) == 0 {
				return nil
			}
			continue
		}
		p.pos++
	}
	return errJSONOccurrenceMapping
}

func (p *jsonOccurrenceParser) literal() (jsonStringMapping, error) {
	start := p.pos
	if start >= len(p.raw) {
		return jsonStringMapping{}, errJSONOccurrenceMapping
	}
	switch p.raw[start] {
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9', 't', 'f', 'n':
	default:
		return jsonStringMapping{}, errJSONOccurrenceMapping
	}
	// The outer UseNumber decoder has validated scalar syntax. Its first-value
	// bound excludes trailing content, so delimiters retain the original numeric
	// spelling and bool/null bytes without decoding every token again.
scalar:
	for p.pos < len(p.raw) {
		switch p.raw[p.pos] {
		case ',', ']', '}', ' ', '\t', '\r', '\n':
			break scalar
		}
		p.pos++
	}
	return rawJSONTokenMapping(p.raw, start, p.pos), nil
}

func rawJSONTokenMapping(raw []byte, start, end int) jsonStringMapping {
	return jsonStringMapping{decoded: raw[start:end], rawStart: start}
}

func (p *jsonOccurrenceParser) streamingString() (jsonStringMapping, error) {
	start, end, err := p.stringRange()
	if err != nil {
		return jsonStringMapping{}, err
	}
	encoded := p.raw[start:end]
	if bytes.IndexByte(encoded, '\\') < 0 && utf8.Valid(encoded) {
		return rawJSONTokenMapping(p.raw, start, end), nil
	}
	var decoded string
	if err := json.Unmarshal(p.raw[start-1:end+1], &decoded); err != nil {
		return jsonStringMapping{}, errJSONOccurrenceMapping
	}
	return jsonStringMapping{decoded: []byte(decoded), encoded: encoded, rawStart: start}, nil
}

func (p *jsonOccurrenceParser) stringRange() (int, int, error) {
	if !p.consume('"') {
		return 0, 0, errJSONOccurrenceMapping
	}
	contentStart := p.pos
	escaped := false
	for p.pos < len(p.raw) {
		b := p.raw[p.pos]
		if b == '"' && !escaped {
			contentEnd := p.pos
			p.pos++
			return contentStart, contentEnd, nil
		}
		if b < 0x20 {
			return 0, 0, errJSONOccurrenceMapping
		}
		if b == '\\' && !escaped {
			escaped = true
			p.pos++
			continue
		}
		escaped = false
		p.pos++
	}
	return 0, 0, errJSONOccurrenceMapping
}

func jsonHexQuad(value []byte) (uint16, bool) {
	if len(value) != 4 {
		return 0, false
	}
	var out uint16
	for _, b := range value {
		out <<= 4
		switch {
		case b >= '0' && b <= '9':
			out += uint16(b - '0')
		case b >= 'a' && b <= 'f':
			out += uint16(b-'a') + 10
		case b >= 'A' && b <= 'F':
			out += uint16(b-'A') + 10
		default:
			return 0, false
		}
	}
	return out, true
}

func (p *jsonOccurrenceParser) skipSpace() {
	for p.pos < len(p.raw) {
		switch p.raw[p.pos] {
		case ' ', '\t', '\r', '\n':
			p.pos++
		default:
			return
		}
	}
}

func (p *jsonOccurrenceParser) consume(want byte) bool {
	if p.pos >= len(p.raw) || p.raw[p.pos] != want {
		return false
	}
	p.pos++
	return true
}
