package secretguard

import (
	"bytes"
	"encoding/json"
	"unicode/utf16"
	"unicode/utf8"
)

// decodedJSONOccurrenceMappings collects the live walker's output for comparison
// against the independent encoding/json semantic oracle in the tests.
func decodedJSONOccurrenceMappings(raw []byte) ([]jsonStringMapping, error) {
	var mappings []jsonStringMapping
	err := walkJSONOccurrenceTokens(raw, func(mapping jsonStringMapping, _, _ bool) bool {
		mappings = append(mappings, mapping)
		return true
	})
	return mappings, err
}

// decodeJSONStringMapping is the eager byte-boundary reference for the compact
// production mapper. It is intentionally test-only and never calls the lazy
// materialization algorithm whose decoded ranges it checks.
func decodeJSONStringMapping(raw []byte, start, end int) (jsonStringMapping, error) {
	if value := raw[start:end]; bytes.IndexByte(value, '\\') < 0 && utf8.Valid(value) {
		return rawJSONTokenMapping(raw, start, end), nil
	}
	token := make([]byte, 0, end-start+2)
	token = append(token, '"')
	token = append(token, raw[start:end]...)
	token = append(token, '"')
	var decoded string
	if err := json.Unmarshal(token, &decoded); err != nil {
		return jsonStringMapping{}, errJSONOccurrenceMapping
	}
	mapping := jsonStringMapping{decoded: make([]byte, 0, len(decoded)), boundaries: make([]int, 1, len(decoded)+1)}
	mapping.boundaries[0] = start
	for pos := start; pos < end; {
		rawStart := pos
		if raw[pos] != '\\' {
			r, size := utf8.DecodeRune(raw[pos:end])
			if r == utf8.RuneError && size == 1 {
				mapping.append([]byte(string(utf8.RuneError)), rawStart, pos+1)
				pos++
				continue
			}
			mapping.append(raw[pos:pos+size], rawStart, pos+size)
			pos += size
			continue
		}
		if pos+1 >= end {
			return jsonStringMapping{}, errJSONOccurrenceMapping
		}
		if raw[pos+1] != 'u' {
			var value byte
			switch raw[pos+1] {
			case '"':
				value = '"'
			case '\\':
				value = '\\'
			case '/':
				value = '/'
			case 'b':
				value = '\b'
			case 'f':
				value = '\f'
			case 'n':
				value = '\n'
			case 'r':
				value = '\r'
			case 't':
				value = '\t'
			default:
				return jsonStringMapping{}, errJSONOccurrenceMapping
			}
			mapping.append([]byte{value}, rawStart, pos+2)
			pos += 2
			continue
		}
		if pos+6 > end {
			return jsonStringMapping{}, errJSONOccurrenceMapping
		}
		first, ok := jsonHexQuad(raw[pos+2 : pos+6])
		if !ok {
			return jsonStringMapping{}, errJSONOccurrenceMapping
		}
		pos += 6
		code := rune(first)
		if first >= 0xD800 && first <= 0xDBFF && pos+6 <= end && raw[pos] == '\\' && raw[pos+1] == 'u' {
			second, valid := jsonHexQuad(raw[pos+2 : pos+6])
			if valid && second >= 0xDC00 && second <= 0xDFFF {
				code = utf16.DecodeRune(rune(first), rune(second))
				pos += 6
			}
		}
		r := code
		if code >= 0xD800 && code <= 0xDFFF {
			r = utf8.RuneError
		}
		var encoded [utf8.UTFMax]byte
		n := utf8.EncodeRune(encoded[:], r)
		mapping.append(encoded[:n], rawStart, pos)
	}
	if !bytes.Equal(mapping.decoded, []byte(decoded)) {
		return jsonStringMapping{}, errJSONOccurrenceMapping
	}
	return mapping, nil
}

func (m *jsonStringMapping) append(value []byte, rawStart, rawEnd int) {
	m.boundaries[len(m.decoded)] = rawStart
	m.decoded = append(m.decoded, value...)
	for range value {
		m.boundaries = append(m.boundaries, rawEnd)
	}
}
