package responses_test

// Focused proofs for the strict wire-document reader.
//
// The proofs are self-contained on purpose: they use their own documents and
// their own literals, so they keep proving what they prove even when the
// contract test file around them is replaced.
//
// The bar for every proof here is the same as the bar for a fixture: a proof
// that cannot fail for the reason it names is not evidence.
import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/responses"
)

// TestScanStrictWireDocument_ReadsEveryString proves the reader collects
// every decoded string the document carries, including the strings of a
// member the document repeats.
//
// A reader that stopped at the repeated member would drop exactly the value
// the repetition hides, which is the defect the reader exists to close.
func TestScanStrictWireDocument_ReadsEveryString(t *testing.T) {
	t.Parallel()
	document := scanStrictWireDocument([]byte(`{"input":[{"type":"message","content":[{"type":"input_text","text":"first"}]}],"input":[{"type":"message","content":[{"type":"input_text","text":"hidden"}]}]}`))
	if document.parseErr != nil {
		t.Fatalf("the document must be read, got a parse error: %v", document.parseErr)
	}
	for _, want := range []string{"first", "hidden"} {
		if !containsString(document.decodedStrings(), want) {
			t.Errorf("the reader dropped the string %q; it collected %v", want, document.decodedStrings())
		}
	}
	if document.repeatedMember() == nil {
		t.Error("the reader must report the repeated member")
	}
}

// TestScanStrictWireDocument_ReportsTheRepeatedMemberAndItsPath proves the
// reader names the repeated member and the path it occurs at, at every
// nesting level.
//
// The paths are the reader's own: a repeated member at the top level, inside
// a nested object and inside an array element are three different findings
// and each has to name where it was found.
func TestScanStrictWireDocument_ReportsTheRepeatedMemberAndItsPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		document string
		wantPath string
	}{
		{
			name:     "top level",
			document: `{"input":[],"input":[]}`,
			wantPath: "$.input",
		},
		{
			name:     "nested object",
			document: `{"input":[{"type":"message","role":"user","role":"assistant"}]}`,
			wantPath: "$.input[0].role",
		},
		{
			name:     "array element",
			document: `{"input":[{"type":"message"},{"type":"message","type":"function_call"}]}`,
			wantPath: "$.input[1].type",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			document := scanStrictWireDocument([]byte(tc.document))
			if document.parseErr != nil {
				t.Fatalf("the document must be read, got a parse error: %v", document.parseErr)
			}
			repeated := document.repeatedMember()
			if repeated == nil {
				t.Fatalf("the reader must report the repeated member of %s", tc.document)
			}
			if repeated.Path != tc.wantPath {
				t.Errorf("the repeated member is reported at %q, want %q", repeated.Path, tc.wantPath)
			}
			if !strings.Contains(repeated.Error(), repeated.Member) {
				t.Errorf("the diagnostic %q must name the repeated member %q", repeated.Error(), repeated.Member)
			}
		})
	}
}

// TestScanStrictWireDocument_DecodesEscapedStrings proves the reader returns
// the DECODED text, so a marker that is written escaped is still read as
// the text it stands for.
//
// The marker in the document is written ESCAPED - the JSON escape sequence
// for "<" and ">" - and the reader must return the characters, not the
// escape. This is the earlier hole: a raw-byte scan cannot see
// "\u003cuser_instructions\u003e" in a body
// that carries it as the escape.
//
// The input is escaped on purpose. A reader that preserved the escape
// literally would return the escape sequence, and this proof would fail at
// this assertion.
func TestScanStrictWireDocument_DecodesEscapedStrings(t *testing.T) {
	t.Parallel()
	document := scanStrictWireDocument([]byte(`{"instructions":"\u003cuser_instructions\u003e"}`))
	if document.parseErr != nil {
		t.Fatalf("the document must be read, got a parse error: %v", document.parseErr)
	}
	if !containsString(document.decodedStrings(), "<user_instructions>") {
		t.Errorf("the reader must decode the escaped string, it collected %v", document.decodedStrings())
	}
}

// TestStrictDocumentText_CarriesTheDecodedStrings proves the decoded strings
// are WIRED into the search text the absence scan reads.
//
// Deleting the loop that appends the decoded strings leaves the search text
// with the raw bytes only, and a marker that is written escaped is
// invisible in the raw bytes: the scan would stop naming it while every
// other check still passed. The proof pins that connection.
//
// The raw bytes are deliberately kept in the search text, so this proof
// asserts that the decoded marker OCCURS in it and not that two different
// documents render the same text.
func TestStrictDocumentText_CarriesTheDecodedStrings(t *testing.T) {
	t.Parallel()
	body := []byte(`{"instructions":"\u003cuser_instructions\u003e"}`)
	document := scanStrictWireDocument(body)
	if document.parseErr != nil {
		t.Fatalf("the document must be read, got a parse error: %v", document.parseErr)
	}
	request := responses.WireRequest{
		Method:  "POST",
		URL:     "https://api.openai.com/v1/responses",
		Headers: http.Header{},
		Body:    body,
	}
	text := strictDocumentText(request, document)
	if !strings.Contains(text, "<user_instructions>") {
		t.Errorf("the search text must carry the decoded marker, got %q", text)
	}
}

// TestScanStrictWireDocument_ReadsAnUnambiguousDocument proves the reader
// accepts a document that repeats no member, whatever its key order and
// whitespace are.
//
// A reader that refused every document would prove nothing, and a reader
// that refused a reordered document would refuse documents that differ only
// in how they are written.
func TestScanStrictWireDocument_ReadsAnUnambiguousDocument(t *testing.T) {
	t.Parallel()
	for _, document := range []string{
		`{"model":"m","input":[],"store":false,"stream":true}`,
		`{"stream":true,"store":false,"input":[],"model":"m"}`,
		"{\n  \"model\" : \"m\",\n  \"input\" : [ ]\n}",
	} {
		read := scanStrictWireDocument([]byte(document))
		if read.parseErr != nil {
			t.Errorf("the document %s must be read, got a parse error: %v", document, read.parseErr)
		}
		if read.repeatedMember() != nil {
			t.Errorf("the document %s repeats no member, got %v", document, read.repeatedMember())
		}
	}
}

// TestScanStrictWireDocument_RefusesAMalformedDocument proves a document
// that is not JSON is reported as a parse error and is not read as though it
// had not arrived.
func TestScanStrictWireDocument_RefusesAMalformedDocument(t *testing.T) {
	t.Parallel()
	for _, document := range []string{`{"a":`, `not json`, `{"a":1} {"b":2}`} {
		read := scanStrictWireDocument([]byte(document))
		if read.parseErr == nil {
			t.Errorf("the document %q must be refused, got a read", document)
		}
	}
}

// containsString reports whether values carries want.
func containsString(values []string, want string) bool {
	return slices.Contains(values, want)
}
