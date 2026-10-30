package responses_test

// A strict reader for the wire document.
//
// # Why this exists
//
// The contract test claims that a projected request is free of the forbidden
// Codex prompt, the `<user_instructions>` glue, the Codex CLI originator and
// the private route, and it claims that the request body is exactly the
// documented document. Both claims were made against a document read with
// json.Unmarshal into `any`, and that read is LOSSY in one specific way: a
// JSON object becomes a map, and a map keeps the LAST value of a repeated
// member and drops the others.
//
// A document that repeats a member can therefore hide a value behind the
// repetition, and the two checks were reading the same lossy view: the
// absence scan did not read the dropped value, and the comparison did not
// read it either. The request carried the value; the verification did not
// see it. That is a COLLECT-against-SAMPLE failure - a value arrived on the
// wire and disappeared before verification - and it is fixed here rather
// than deferred to the production policy task.
//
// # What this reader does
//
// The reader walks the document member by member and keeps every value it
// carries:
//
//   - every decoded string - the escaped ones included - is collected, so
//     the absence scan reads the text the provider reads and not the
//     escaped bytes;
//   - the first member the document repeats is reported, at every nesting
//     level, with the member name and its JSON path;
//   - the walk CONTINUES after a repeated member, because a document that
//     repeats a member is exactly the document in which a value can hide,
//     and the absence scan must still read that value.
//
// The shape comparison keeps its tolerance: the golden bodies are compared
// after a normalising decode, so a document whose keys are in another order
// or whose whitespace differs still matches. Only an AMBIGUOUS document - one
// that repeats a member - is refused, because a last-wins decoder would
// read a document the projection did not produce.
//
// # Relationship to the SIWC reader
//
// The technique is the one connectors/openaichatgptplan/internal/siwc/
// jsonstrict.go already uses: a token walk that remembers the member names
// it has seen. The two packages are not coupled. The SIWC reader is the
// auth domain's own boundary and it is unexported; this reader is the
// transport contract test's boundary.
//
// The duplication is DELIBERATE, not an oversight: the review ruled that
// the two readers stay independent, and that a shared reader is
// reconsidered only when a real consumer demonstrates shared parsing
// behaviour. That decision and its criteria are recorded in this spec's
// Implementation Notes (task 4.1). The two readers have different
// contracts - constant auth errors versus developer-facing duplicate and
// path diagnostics - and a shared reader is not to be manufactured to
// remove a test helper.
//
// One difference is deliberate. The SIWC reader reports CONSTANT errors,
// because a provider document is untrusted input and an attacker-chosen
// member name has no place in an operator-facing diagnostic. This reader
// names the repeated member and its path, because its diagnostic is a
// developer-facing test failure and a reviewer has to be able to see which
// member was repeated. It never names the VALUE: the value is the untrusted
// part. The production policy path owns its own operator-facing
// classification.
import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/responses"
)

// errStrictMalformedDocument reports a document that is not a JSON document.
var errStrictMalformedDocument = errors.New("the JSON document is malformed")

// errStrictTrailingContent reports content after the document's own value.
var errStrictTrailingContent = errors.New("the JSON document carries trailing content")

// strictDuplicateMember reports a JSON object that repeats a member.
type strictDuplicateMember struct {
	// Member is the repeated member name.
	Member string
	// Path is the JSON path of the repeated member, e.g. "$.input[0]".
	Path string
}

// Error names the repeated member and its path, and never the value.
func (d *strictDuplicateMember) Error() string {
	return fmt.Sprintf("the JSON document repeats the member %q at %s", d.Member, d.Path)
}

// strictWireDocument is what a wire document yields when it is read without
// loss.
type strictWireDocument struct {
	// strings holds every decoded string value and every member name of the
	// document, in document order. A member the document repeats contributes
	// its name and its value again, so nothing is lost.
	strings []string
	// duplicate is the first member the document repeats, if it repeats one.
	duplicate *strictDuplicateMember
	// parseErr is why the document could not be read at all, if it could not.
	parseErr error
}

// decodedStrings returns every decoded string the document carries.
func (d strictWireDocument) decodedStrings() []string {
	return d.strings
}

// repeatedMember returns the first member the document repeats, if any.
func (d strictWireDocument) repeatedMember() *strictDuplicateMember {
	return d.duplicate
}

// scanStrictWireDocument reads a wire document and keeps every value it
// carries.
//
// The three states are distinct on purpose. A document that is READ carries
// its strings and repeats no member. A document that REPEATS a member is
// read in full - so the absence scan sees every string - and is ambiguous
// for a shape comparison. A document that cannot be read at all is reported
// as a parse error. An arrived-but-unusable document is never read as
// though it had not arrived.
func scanStrictWireDocument(raw []byte) strictWireDocument {
	document := strictWireDocument{}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := scanStrictWireValue(decoder, "$", &document); err != nil {
		document.parseErr = err
		return document
	}
	// The document holds exactly one value: trailing content is refused
	// rather than ignored, so a document cannot hide a second document
	// behind the first.
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		document.parseErr = errStrictTrailingContent
	}
	return document
}

// scanStrictWireValue reads one JSON value, collects its strings and
// reports a repeated member inside it.
//
// The member names are the EXACT names of the document. A member whose name
// differs only in case is a different member and is carried, not refused.
func scanStrictWireValue(decoder *json.Decoder, path string, document *strictWireDocument) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errStrictMalformedDocument
				}
				memberPath := path + "." + key
				document.strings = append(document.strings, key)
				if _, repeated := seen[key]; repeated {
					if document.duplicate == nil {
						document.duplicate = &strictDuplicateMember{Member: key, Path: memberPath}
					}
				} else {
					seen[key] = struct{}{}
				}
				// The walk continues after a repeated member: the value it
				// carries must still be read.
				if err := scanStrictWireValue(decoder, memberPath, document); err != nil {
					return err
				}
			}
			// The closing brace.
			if _, err := decoder.Token(); err != nil {
				return err
			}
		case '[':
			index := 0
			for decoder.More() {
				if err := scanStrictWireValue(decoder, fmt.Sprintf("%s[%d]", path, index), document); err != nil {
					return err
				}
				index++
			}
			// The closing bracket.
			if _, err := decoder.Token(); err != nil {
				return err
			}
		}
	case string:
		document.strings = append(document.strings, value)
	}
	return nil
}

// strictDocumentText renders the whole request document as one searchable
// text: the method, the endpoint, every raw header, the raw body bytes and
// every decoded string the body carries.
//
// The raw bytes are searched next to the decoded strings on purpose. The
// decoded strings are the text the provider reads, so a marker inside an
// escaped string is found; the raw bytes are searched too, because a decoder
// does not surface every byte of a string it had to repair.
func strictDocumentText(req responses.WireRequest, document strictWireDocument) string {
	var b strings.Builder
	b.WriteString(req.Method)
	b.WriteString(" ")
	b.WriteString(req.URL)
	b.WriteByte('\n')
	names := make([]string, 0, len(req.Headers))
	for name := range req.Headers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, value := range req.Headers[name] {
			b.WriteString(name)
			b.WriteString(": ")
			b.WriteString(value)
			b.WriteByte('\n')
		}
	}
	b.Write(req.Body)
	b.WriteByte('\n')
	for _, value := range document.decodedStrings() {
		b.WriteString(value)
		b.WriteByte('\n')
	}
	return b.String()
}
