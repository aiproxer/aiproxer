package responses_test

// The canonical-to-wire contract test.
//
// The contract test is the executable form of the fixture matrix in
// payload_fixtures_test.go: it projects every canonical fixture through the
// connector's request seam and compares the result with the documented
// public Responses request.
//
// # Why the test is gated, and why the gate clears itself
//
// The projection seam does not exist yet: spec task 4.2 implements it. A
// committed failing test cannot be committed - the repository's pre-commit
// gate runs the staged packages' own tests and they must pass - and a test
// that does not compile breaks the module build. So the contract test is
// bound to the implementation state of the seam instead of to a switch
// somebody has to remember:
//
//   - While the seam reports ErrProjectionUnimplemented, the contract
//     assertions are skipped with a message that names the gate and the task
//     that removes it. The module suite and the pre-commit gate stay green.
//
//   - With AIP_SIWC_PROJECTION_RED=1 the assertions run against the
//     unimplemented seam and fail. That is the RED evidence of this task,
//     reproducible on demand by the implementer and by the reviewer.
//
//   - Once task 4.2 lands, the seam stops reporting the sentinel, the gate
//     opens by itself, and the assertions run unconditionally. Nothing has to
//     be removed, because the gate is a function of the implementation state
//     and not a stored preference.
//
// The failure is a real assertion about the contract: it names the fixture
// and prints the request the contract requires, so a reader can see exactly
// which part of the wire contract is not met yet.
//
// # The request document is read without loss
//
// Both checks on the projected request - the whole-document absence scan and
// the body comparison - read the document through the strict reader in
// jsonstrict_test.go. A JSON object that repeats a member can hide a value
// behind the repetition, and a reader that keeps the last value of a
// repeated member drops exactly that value: the request would carry the
// prohibited prompt while neither check saw it. The reader collects every
// decoded string and reports the first repeated member, so the scan reads
// the hidden value and the comparison refuses an ambiguous document.

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/responses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// redEnvVar runs the contract assertions against the unimplemented projector.
//
// It exists so the RED state of this task is reproducible instead of being a
// claim in a commit message.
const redEnvVar = "AIP_SIWC_PROJECTION_RED"

// wantURL is the documented public inference endpoint.
//
// The value is a literal on purpose. Deriving the expectation from the
// connector's own constant would make the fixture agree with whatever the
// constant says, so a change of the constant to a private route would pass
// unnoticed. The literal is the documentation's value, and
// TestPublicEndpointConstants pins the constants to it.
const wantURL = "https://api.openai.com/v1/responses"

// wantHeaders is the exact header set of a projected request.
//
// The names are lower-cased because a header name is case-insensitive on the
// wire: a differently spelled header is the same header, and a projection
// that spells one differently has not changed anything.
//
// The values are literals and not the connector's own constants. A
// mirrored oracle - an expected value derived from the constant it is
// supposed to pin - proves nothing: a projector that emits a Codex identity
// would satisfy it. The user-agent is the case that matters, because the
// requirement is that it identifies this product truthfully.
//
// The credential header is absent on purpose: the transport adds the
// selected profile's access token, so the projection can never hold a
// secret and no fixture can capture one.
var wantHeaders = map[string]string{
	"accept":       "text/event-stream",
	"content-type": "application/json",
	"user-agent":   "AIProxer",
}

// deliveryModes are the downstream delivery modes the matrix is projected
// in. Both must produce the same upstream request.
var deliveryModes = []aipapi.DeliveryMode{
	aipapi.DeliveryModeStreaming,
	aipapi.DeliveryModeNonStreaming,
}

// forbiddenRequestMarkers must not appear anywhere in a projected request.
//
// The check runs against the whole request document - method, endpoint,
// every header and the complete body - and not against one field, because a
// marker that reaches the wire anywhere is a defect whether it sits in the
// instructions, in a header, or in the endpoint.
var forbiddenRequestMarkers = []string{
	// The legacy Codex identity instruction and its prompt glue.
	"you are codex",
	"<user_instructions>",
	"</user_instructions>",
	// The Codex CLI originator and the private ChatGPT route.
	"codex_cli_rs",
	"chatgpt.com",
	"backend-api",
	// The Codex-private header names.
	"codex-task-type",
	"chatgpt-account-id",
}

// forbiddenHeaderNames must not appear in a projected request's header set.
//
// The names are compared case-insensitively, because a header name is
// case-insensitive on the wire and a differently spelled header is the same
// header.
var forbiddenHeaderNames = []string{
	"authorization",
	"openai-beta",
	"originator",
	"version",
	"codex-task-type",
	"conversation_id",
	"session_id",
	"chatgpt-account-id",
}

// contractGateState is the decision the contract test makes before it runs
// its assertions.
type contractGateState int

const (
	// gateRun means the contract assertions run.
	gateRun contractGateState = iota
	// gateSkip means the contract assertions are skipped.
	gateSkip
)

// contractGate decides whether the contract assertions run.
//
// The implementation state alone opens the gate. The RED switch can only
// add the assertions while the seam is unimplemented; it can never close
// them once the seam exists. That is what makes the gate self-clearing.
func contractGate(implemented bool, redSwitch bool) contractGateState {
	if implemented || redSwitch {
		return gateRun
	}
	return gateSkip
}

// projectionImplemented reports whether the canonical-to-wire projection
// exists yet.
//
// The seam's own sentinel is the only signal: the unimplemented projector
// returns ErrProjectionUnimplemented, and the implemented projector never
// does. A projection that exists but refuses the probe call is not treated
// as absent - it opens the gate, and the contract test then reports that
// refusal instead of hiding it behind a skip.
func projectionImplemented() bool {
	_, err := responses.ProjectCall(responses.ProjectInput{
		Call: aipapi.Call{
			Messages: []aipapi.Message{
				{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("probe")}},
			},
		},
		Model: fixtureModel,
	})
	return !errors.Is(err, responses.ErrProjectionUnimplemented)
}

// contractAssertionsRunnable applies the gate to the current build.
func contractAssertionsRunnable(t *testing.T) bool {
	t.Helper()
	implemented := projectionImplemented()
	redSwitch := os.Getenv(redEnvVar) == "1"
	if contractGate(implemented, redSwitch) == gateRun {
		return true
	}
	t.Skipf("the canonical-to-wire projector is not implemented yet, so the wire contract is not asserted yet; "+
		"spec task 4.2 implements the projector and removes this gate by implementing it; "+
		"set %s=1 to run the contract assertions against the unimplemented projector and observe the RED failure",
		redEnvVar)
	return false
}

// TestContractGate_FollowsTheImplementationState pins the gate's contract.
//
// The gate must open on the implementation state alone: that is the property
// that lets task 4.2 land without anybody remembering to remove a switch.
// A gate that depended on the environment variable would pass this test only
// while the seam is unimplemented, so the table below is the evidence.
func TestContractGate_FollowsTheImplementationState(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		implemented bool
		redSwitch   bool
		want        contractGateState
	}{
		{name: "implemented runs without the switch", implemented: true, redSwitch: false, want: gateRun},
		{name: "implemented runs with the switch", implemented: true, redSwitch: true, want: gateRun},
		{name: "unimplemented skips by default", implemented: false, redSwitch: false, want: gateSkip},
		{name: "unimplemented runs under the switch", implemented: false, redSwitch: true, want: gateRun},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := contractGate(tc.implemented, tc.redSwitch); got != tc.want {
				t.Fatalf("contractGate(implemented=%v, redSwitch=%v) = %v, want %v", tc.implemented, tc.redSwitch, got, tc.want)
			}
		})
	}
}

// TestPublicEndpointAndIdentityConstants pins the connector's endpoint and
// user-agent constants to the documented public route and to this product's
// truthful identity.
//
// The constants and the documented values are two different artifacts, so
// they are compared here: the constants are what the connector uses, and the
// literals are what the documentation and the requirement say. A change of
// either one alone must fail. The wire fixture carries the same literals
// independently, so this test is the second line of defence and not the
// oracle.
func TestPublicEndpointAndIdentityConstants(t *testing.T) {
	t.Parallel()
	if got := responses.PublicBaseURL + responses.ResponsesPath; got != wantURL {
		t.Fatalf("the public endpoint constants compose to %q, want the documented public endpoint %q", got, wantURL)
	}
	for _, forbidden := range []string{"chatgpt.com", "backend-api", "codex"} {
		if strings.Contains(strings.ToLower(responses.PublicBaseURL+responses.ResponsesPath), forbidden) {
			t.Errorf("the public endpoint constants must not name a private route, but they contain %q", forbidden)
		}
	}
	if responses.UserAgent != wantHeaders["user-agent"] {
		t.Errorf("the user-agent constant is %q, want the truthful product identity %q", responses.UserAgent, wantHeaders["user-agent"])
	}
	for _, forbidden := range []string{"codex", "chatgpt"} {
		if strings.Contains(strings.ToLower(responses.UserAgent), forbidden) {
			t.Errorf("the user-agent must identify this product truthfully, but it contains %q", forbidden)
		}
	}
}

// TestCanonicalToWireContract is the fixture matrix.
//
// Every case is projected in both downstream delivery modes, and the two
// requests must be identical: the downstream mode is a client-side
// collection concern and never changes the upstream request.
func TestCanonicalToWireContract(t *testing.T) {
	t.Parallel()
	if !contractAssertionsRunnable(t) {
		return
	}
	cases := wireContractCases()
	if len(cases) == 0 {
		t.Fatal("the wire contract matrix is empty")
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertWireContract(t, tc)
		})
	}
}

// assertWireContract projects one fixture in both delivery modes and
// compares every request with the documented wire request.
//
// The assertion helpers below do not mark themselves as test helpers on
// purpose: a contract failure must point at the assertion that failed, not
// at the caller of the helper.
//
//nolint:thelper // a contract failure must point at the assertion, not at its caller
func assertWireContract(t *testing.T, tc wireContractCase) {
	if err := tc.call.Validate(); err != nil {
		t.Fatalf("the canonical input of %q must be a valid canonical call, otherwise the fixture can fail for the wrong reason: %v", tc.name, err)
	}
	requests := make([]responses.WireRequest, 0, len(deliveryModes))
	for _, mode := range deliveryModes {
		call := tc.call
		call.Invocation = aipapi.Invocation{
			Operation:    aipapi.OperationOpenAIResponses,
			DeliveryMode: mode,
		}
		req, err := responses.ProjectCall(responses.ProjectInput{Call: call, Model: tc.model})
		if err != nil {
			if errors.Is(err, responses.ErrProjectionUnimplemented) {
				t.Fatalf("the wire contract %q is not met: the canonical-to-wire projector is not implemented (spec task 4.2); "+
					"the request the contract requires is:\n%s", tc.name, tc.wantBody)
			}
			t.Fatalf("the wire contract %q is not met: projecting the canonical call must produce the documented request, got error: %v", tc.name, err)
		}
		assertRequestShape(t, tc, req)
		requests = append(requests, req)
	}
	if len(requests) != 2 {
		t.Fatalf("the fixture %q must be projected in %d delivery modes, got %d", tc.name, len(deliveryModes), len(requests))
	}
	if !sameRequest(requests[0], requests[1]) {
		t.Fatalf("the wire contract %q is not met: the downstream delivery mode must not change the upstream request; "+
			"streaming produced %s and non-streaming produced %s", tc.name, describeRequest(requests[0]), describeRequest(requests[1]))
	}
}

// assertRequestShape checks one projected request against the contract.
//
//nolint:thelper // a contract failure must point at the assertion, not at its caller
func assertRequestShape(t *testing.T, tc wireContractCase, req responses.WireRequest) {
	if req.Method != http.MethodPost {
		t.Errorf("the wire contract %q is not met: the request method is %q, want %q", tc.name, req.Method, http.MethodPost)
	}
	if req.URL != wantURL {
		t.Errorf("the wire contract %q is not met: the request endpoint is %q, want the public endpoint %q", tc.name, req.URL, wantURL)
	}
	assertHeaderSet(t, tc, req)
	// The body is read WITHOUT loss. A document that repeats a member can
	// hide a value behind the repetition, and both checks below have to see
	// it: the absence scan reads every decoded string, and the comparison
	// is refused for an ambiguous document.
	document := scanStrictWireDocument(req.Body)
	assertNoForbiddenMarkers(t, tc, req, document)
	if repeated := document.repeatedMember(); repeated != nil {
		t.Errorf("the wire contract %q is not met: the request body is ambiguous and cannot be compared: %v", tc.name, repeated)
		return
	}
	if document.parseErr != nil {
		t.Fatalf("the wire contract %q is not met: the request body is not a JSON document: %v", tc.name, document.parseErr)
	}
	got := canonicalJSON(t, "projected body", req.Body)
	want := canonicalJSON(t, "expected body", []byte(tc.wantBody))
	if got != want {
		t.Errorf("the wire contract %q is not met: the projected request body differs from the documented request\n--- projected ---\n%s\n--- documented ---\n%s", tc.name, got, want)
	}
}

// assertHeaderSet checks the exact header set of a projected request.
//
// The raw header map is inspected, not the canonical accessor: a header that
// was assigned under a non-canonical spelling is still a header on the wire,
// and a set that collapses two spellings of one name can hide a value
// behind an arbitrary one of them. A repeated name is therefore a failure of
// its own, and every raw spelling is checked against the forbidden names.
//
//nolint:thelper // a contract failure must point at the assertion, not at its caller
func assertHeaderSet(t *testing.T, tc wireContractCase, req responses.WireRequest) {
	got := map[string]string{}
	spelling := map[string]string{}
	for name, values := range req.Headers {
		lower := strings.ToLower(name)
		if previous, ok := spelling[lower]; ok {
			t.Errorf("the wire contract %q is not met: the request carries two spellings of the same header name: %q and %q", tc.name, previous, name)
			continue
		}
		spelling[lower] = name
		if len(values) != 1 {
			t.Errorf("the wire contract %q is not met: the header %q carries %d values, want exactly one", tc.name, name, len(values))
		}
		for _, forbidden := range forbiddenHeaderNames {
			if lower == forbidden {
				t.Errorf("the wire contract %q is not met: the request carries the private header %q", tc.name, name)
			}
		}
		if strings.HasPrefix(lower, "x-codex") {
			t.Errorf("the wire contract %q is not met: the request carries the Codex beta header %q", tc.name, name)
		}
		if len(values) == 1 {
			got[lower] = values[0]
		}
	}
	for lower, want := range wantHeaders {
		value, ok := got[lower]
		if !ok {
			t.Errorf("the wire contract %q is not met: the request is missing the header %q", tc.name, lower)
			continue
		}
		if value != want {
			t.Errorf("the wire contract %q is not met: the header %q is %q, want %q", tc.name, lower, value, want)
		}
	}
	for lower := range got {
		if _, ok := wantHeaders[lower]; !ok {
			t.Errorf("the wire contract %q is not met: the request carries the unexpected header %q", tc.name, lower)
		}
	}
}

// assertNoForbiddenMarkers checks the whole request document for the
// markers that must never reach the wire.
//
// The document is searched through every decoded string the body carries -
// so a marker inside an escaped string is found, and a marker inside a
// member the body REPEATS is found too - next to the raw bytes, the
// endpoint and every raw header. The check is a whole-document check: a
// marker that reaches the wire anywhere is a defect, whether it sits in the
// instructions, in a header, or in the endpoint.
//
//nolint:thelper // a contract failure must point at the assertion, not at its caller
func assertNoForbiddenMarkers(t *testing.T, tc wireContractCase, req responses.WireRequest, document strictWireDocument) {
	text := strings.ToLower(strictDocumentText(req, document))
	for _, marker := range forbiddenRequestMarkers {
		if strings.Contains(text, marker) {
			t.Errorf("the wire contract %q is not met: the request document contains the forbidden marker %q", tc.name, marker)
		}
	}
}

// sameRequest reports whether two projected requests are identical.
func sameRequest(a, b responses.WireRequest) bool {
	if a.Method != b.Method || a.URL != b.URL {
		return false
	}
	if len(a.Headers) != len(b.Headers) {
		return false
	}
	for name, values := range a.Headers {
		other, ok := b.Headers[name]
		if !ok || len(values) != len(other) {
			return false
		}
		for i := range values {
			if values[i] != other[i] {
				return false
			}
		}
	}
	return bytes.Equal(a.Body, b.Body)
}

// describeRequest renders a request for a failure message.
func describeRequest(req responses.WireRequest) string {
	return strings.TrimSpace(req.Method + " " + req.URL + "\n" + string(req.Body))
}

// canonicalJSON renders a JSON document in a stable, readable form.
//
// The comparison is structural, so a projection that reorders keys or
// changes whitespace still passes, while a projection that adds, drops or
// changes a value fails with a readable diff.
//
// The numbers keep their EXACT value, and the rule is the one stated in
// jsonnumeric_test.go: two numbers are equal when they denote the same
// exact decimal value, so "1" and "1.0" are one value and two large
// integers that differ are never the same value. A decoder into float64
// would round 9007199254740993 into 9007199254740992 and the comparison
// would then call two different documents equal.
func canonicalJSON(t *testing.T, label string, raw []byte) string {
	t.Helper()
	value, err := decodeJSONDocument(raw)
	if err != nil {
		t.Fatalf("the %s is not a valid JSON document: %v\n%s", label, err, raw)
	}
	out, err := json.MarshalIndent(canonicalizeNumbers(value), "", "  ")
	if err != nil {
		t.Fatalf("the %s cannot be rendered: %v", label, err)
	}
	return string(out)
}
