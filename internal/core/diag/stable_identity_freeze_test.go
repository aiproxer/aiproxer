package diag_test

// Task 1.7 characterization: freeze deterministic request/economic identity
// for the future large-payload lane (requirements 15, 16, 18; design sections
// 10 read-only, 13 read-only, 16).
//
// What this proves with real existing seams only:
//   - diag.StableCallToken/StableCallID/StableUnix/StableTime byte-for-byte
//     stability, nil handling, and explicit Call.ID precedence (token/Unix
//     ignore ID; StableCallID returns the trimmed caller ID verbatim).
//   - Fixture coverage the wire digest must reproduce exactly: huge strings,
//     escaped Unicode/HTML-sensitive strings, tools/messages/items,
//     model/selector (Route.Selector + Extensions), session fields,
//     session-header precedence (headers win over body metadata), and optional
//     fields (pointer-vs-unset, zero-vs-absent, PreviousResponseID,
//     PromptCacheKey, ToolChoice).
//   - Non-serialized fields (Session.Metadata, Message.Metadata, Invocation,
//     MaxPendingWireEvents) are excluded from the stable sum via json:"-".
//   - Response-ID/timestamp derivation contract the bridge must preserve:
//     "resp_"+token, "chatcmpl_"+token, "msg_"+responseID, StableUnix range,
//     StableTime UTC; explicit encode opts / WallClock override wins.
//   - Trace precedence: explicit AIProxer context wins (callDiag over legacy
//     trace key); generator format t_%08d monotonic; diag re-exports match
//     lineage.
//   - Billing namespace separation: stable IDs live in call_* / resp_* /
//     chatcmpl_* / msg_* namespaces, never bc_*; billing ownership stays in
//     internal/core/billing (reused, not duplicated here).
//
// Reused, not duplicated here:
//   - Metering checkpoint/fact/source IDs (checkpoint.FrontendIngressIdentity,
//     BackendIngressIdentity, Fact SourceEventKey/IdempotencyKey) — frozen in
//     internal/core/metering/checkpoint/identity_freeze_test.go.
//   - Billing bc_ format/uniqueness/operation keys — internal/core/billing/call_id_test.go.
//   - Per-lane response writers/session carriers/keepalive — Task 1.6 freeze
//     tests (openairesponses, openailegacy, openresponses, frontendpipe).
//   - Header-selector-wins + RouteFromBodyModel guard — frontendpipe pipe_ordering_test.go.
//   - Session carrier validation/alias/redaction — sessionwire tests.
//
// What this explicitly does NOT claim (out of scope, needs future tasks):
//   - No wire execution, no ExecutionResult/ResponseFacts carrier, no profile
//     hash-writer or canonical-digest seam is constructed here (Tasks 6.x).
//     The frozen invariant is only the current stable-sum contract a future
//     digest must reproduce byte-for-byte for its certified subset.
//   - No production diff.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/lineage"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/sessionwire"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func freezeBaseCall() *aipapi.Call {
	return &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "stub:gpt-4o-mini"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("freeze hello")},
		}},
	}
}

//go:fix inline
func freezeIntPtr(v int) *int { return new(v) }

//go:fix inline
func freezeFloatPtr(v float64) *float64 { return new(v) }

func freezeModelExt(model string) map[string]json.RawMessage {
	return map[string]json.RawMessage{"openai.model": json.RawMessage(`"` + model + `"`)}
}

func freezeAssertTokenShape(t *testing.T, token string) {
	t.Helper()
	if len(token) != 16 {
		t.Fatalf("token %q must be 16 hex chars (8 bytes)", token)
	}
	if _, err := hex.DecodeString(token); err != nil {
		t.Fatalf("token %q must be hex: %v", token, err)
	}
}

func TestStableIdentityFreeze_ByteForByteStability(t *testing.T) {
	t.Parallel()
	call := freezeBaseCall()
	call.Tools = []aipapi.ToolDef{{Name: "get_weather", Description: "lookup", Parameters: json.RawMessage(`{"type":"object"}`)}}
	call.Options = aipapi.GenerationOptions{MaxOutputTokens: new(64), ReasoningEffort: "high", Verbosity: aipapi.VerbosityHigh}

	tok := diag.StableCallToken(call)
	freezeAssertTokenShape(t, tok)
	for range 3 {
		if got := diag.StableCallToken(call); got != tok {
			t.Fatalf("StableCallToken unstable across repeats: %q vs %q", got, tok)
		}
		if got := diag.StableCallID(call); got != "call_"+tok {
			t.Fatalf("StableCallID=%q want %q", got, "call_"+tok)
		}
		if got := diag.StableUnix(call); got != diag.StableUnix(call) {
			t.Fatalf("StableUnix unstable: %d", got)
		}
	}
	if !diag.StableTime(call).Equal(time.Unix(diag.StableUnix(call), 0).UTC()) {
		t.Fatal("StableTime must equal time.Unix(StableUnix,0).UTC()")
	}
	clone := aipapi.CloneCall(*call)
	if got := diag.StableCallToken(&clone); got != tok {
		t.Fatalf("CloneCall token=%q want %q", got, tok)
	}
	if id := call.ID; id != "" {
		t.Fatalf("Stable* must not mutate Call.ID, got %q", id)
	}
}

func TestStableIdentityFreeze_ExplicitIDPrecedence(t *testing.T) {
	t.Parallel()
	base := freezeBaseCall()
	plainToken := diag.StableCallToken(base)
	plainUnix := diag.StableUnix(base)

	withID := freezeBaseCall()
	withID.ID = "  call-explicit-1  "
	if got := diag.StableCallID(withID); got != "call-explicit-1" {
		t.Fatalf("StableCallID=%q want trimmed explicit ID", got)
	}
	if got := diag.StableCallToken(withID); got != plainToken {
		t.Fatalf("token must ignore Call.ID: %q vs %q", got, plainToken)
	}
	if got := diag.StableUnix(withID); got != plainUnix {
		t.Fatalf("unix must ignore Call.ID: %d vs %d", got, plainUnix)
	}

	blank := freezeBaseCall()
	blank.ID = "   "
	if got := diag.StableCallID(blank); got != "call_"+plainToken {
		t.Fatalf("whitespace ID must fall back to derived: %q", got)
	}

	billingLooking := freezeBaseCall()
	billingLooking.ID = "bc_0123456789abcdef0123456789abcdef"
	if got := diag.StableCallID(billingLooking); got != "bc_0123456789abcdef0123456789abcdef" {
		t.Fatalf("diag preserves caller ID verbatim (billing layer owns bc_ validation): %q", got)
	}
	if got := diag.StableCallToken(billingLooking); got != plainToken {
		t.Fatalf("token must still ignore bc_-looking ID: %q vs %q", got, plainToken)
	}
}

func TestStableIdentityFreeze_NilAndEmpty(t *testing.T) {
	t.Parallel()
	if got := diag.StableCallToken(nil); got != "0000000000000000" {
		t.Fatalf("nil token=%q want zero hex", got)
	}
	if got := diag.StableCallID(nil); got != "call_0000000000000000" {
		t.Fatalf("nil id=%q", got)
	}
	if got := diag.StableUnix(nil); got != 1715620000 {
		t.Fatalf("nil unix=%d want stableTimestampBase", got)
	}
	if tm := diag.StableTime(nil); !tm.Equal(time.Unix(1715620000, 0).UTC()) || tm.Location() != time.UTC {
		t.Fatalf("nil time=%v want UTC base", tm)
	}
	empty := &aipapi.Call{}
	freezeAssertTokenShape(t, diag.StableCallToken(empty))
	if got := diag.StableCallID(empty); !strings.HasPrefix(got, "call_") {
		t.Fatalf("empty id=%q", got)
	}
	var nilCall *aipapi.Call
	emptyCall := &aipapi.Call{}
	if diag.StableCallToken(nilCall) == diag.StableCallToken(emptyCall) {
		t.Fatal("nil (zero sum) and empty struct (sha of {}) must differ")
	}
}

func TestStableIdentityFreeze_HugeStringDeterminism(t *testing.T) {
	t.Parallel()
	huge := strings.Repeat("a", 1<<20) // 1 MiB prompt-scale scalar
	a := freezeBaseCall()
	a.Messages[0].Parts[0] = aipapi.TextPart(huge)
	b := freezeBaseCall()
	b.Messages[0].Parts[0] = aipapi.TextPart(huge)
	if diag.StableCallToken(a) != diag.StableCallToken(b) {
		t.Fatal("identical huge content must hash identically")
	}
	if diag.StableUnix(a) != diag.StableUnix(b) {
		t.Fatal("identical huge content must share StableUnix")
	}
	c := freezeBaseCall()
	c.Messages[0].Parts[0] = aipapi.TextPart(huge + "b")
	if diag.StableCallToken(a) == diag.StableCallToken(c) {
		t.Fatal("one-byte huge suffix change must change the token")
	}
	small := freezeBaseCall()
	if diag.StableCallToken(a) == diag.StableCallToken(small) {
		t.Fatal("huge content must differ from small fixture")
	}
}

func TestStableIdentityFreeze_EscapedUnicodeHTML(t *testing.T) {
	t.Parallel()
	tricky := "<div>&\"'\\</div>\u2028\u2029🧪 café \\u0041 \n\t\r"
	a := freezeBaseCall()
	a.Messages[0].Parts[0] = aipapi.TextPart(tricky)
	b := freezeBaseCall()
	b.Messages[0].Parts[0] = aipapi.TextPart(tricky)
	if diag.StableCallToken(a) != diag.StableCallToken(b) {
		t.Fatal("escaped/Unicode content must be byte-for-byte stable")
	}
	plain := freezeBaseCall()
	if diag.StableCallToken(a) == diag.StableCallToken(plain) {
		t.Fatal("HTML/Unicode content must affect identity")
	}
	// Go-level escape spelling of the same logical text must still hash the
	// same way because the sum runs over json.Marshal output, not Go source.
	c := freezeBaseCall()
	c.Messages[0].Parts[0] = aipapi.TextPart(strings.Clone(tricky))
	if diag.StableCallToken(a) != diag.StableCallToken(c) {
		t.Fatal("cloned tricky string must hash identically")
	}
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip aipapi.Call
	if err := json.Unmarshal(raw, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if diag.StableCallToken(&roundTrip) != diag.StableCallToken(a) {
		t.Fatal("JSON round-trip of escaped content must preserve identity")
	}
}

func TestStableIdentityFreeze_ToolsMessagesItems(t *testing.T) {
	t.Parallel()
	base := freezeBaseCall()
	baseTok := diag.StableCallToken(base)

	withTools := freezeBaseCall()
	withTools.Tools = []aipapi.ToolDef{{Name: "get_weather", Parameters: json.RawMessage(`{"type":"object"}`)}}
	withTools.ToolChoice = aipapi.ToolChoice{Mode: aipapi.ToolChoiceAny}
	if diag.StableCallToken(withTools) == baseTok {
		t.Fatal("declared tools must affect identity")
	}
	toolsTok := diag.StableCallToken(withTools)
	renamed := freezeBaseCall()
	renamed.Tools = []aipapi.ToolDef{{Name: "get_forecast", Parameters: json.RawMessage(`{"type":"object"}`)}}
	renamed.ToolChoice = aipapi.ToolChoice{Mode: aipapi.ToolChoiceAny}
	if diag.StableCallToken(renamed) == toolsTok {
		t.Fatal("tool rename must change identity")
	}

	withInstructions := freezeBaseCall()
	withInstructions.Instructions = []aipapi.Message{{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("be helpful")}}}
	if diag.StableCallToken(withInstructions) == baseTok {
		t.Fatal("instructions must affect identity")
	}

	itemCall := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "stub:gpt-4o-mini"},
		Items: []aipapi.Item{{
			Kind:    aipapi.ItemKindMessage,
			ID:      "m1",
			Status:  aipapi.ItemStatusCompleted,
			Role:    aipapi.RoleUser,
			Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "freeze hello"}},
		}},
	}
	itemTok := diag.StableCallToken(itemCall)
	freezeAssertTokenShape(t, itemTok)
	if itemTok == baseTok {
		t.Fatal("item-authoritative shape must differ from message shape")
	}
	mutated := aipapi.CloneCall(*itemCall)
	mutated.Items[0].Content[0].Text = "freeze hello?"
	if diag.StableCallToken(&mutated) == itemTok {
		t.Fatal("item content edit must change identity")
	}
}

func TestStableIdentityFreeze_ModelSelector(t *testing.T) {
	t.Parallel()
	base := freezeBaseCall()
	base.Extensions = freezeModelExt("gpt-4o-mini")
	baseTok := diag.StableCallToken(base)

	otherSelector := freezeBaseCall()
	otherSelector.Route.Selector = "stub:gpt-4o"
	otherSelector.Extensions = freezeModelExt("gpt-4o-mini")
	if diag.StableCallToken(otherSelector) == baseTok {
		t.Fatal("Route.Selector change must change identity")
	}

	otherModel := freezeBaseCall()
	otherModel.Extensions = freezeModelExt("gpt-4o")
	if diag.StableCallToken(otherModel) == baseTok {
		t.Fatal("extension model change must change identity")
	}

	noModel := freezeBaseCall()
	if diag.StableCallToken(noModel) == baseTok {
		t.Fatal("dropping the model extension must change identity")
	}
	// Header-selector-wins is frontendpipe behavior (pipe_ordering_test.go);
	// diag only freezes that both inputs participate in the sum.
	emptySelector := freezeBaseCall()
	emptySelector.Route.Selector = ""
	emptySelector.Extensions = freezeModelExt("gpt-4o-mini")
	if diag.StableCallToken(emptySelector) == baseTok {
		t.Fatal("empty selector must differ from stub selector")
	}
}

func TestStableIdentityFreeze_SessionFieldsParticipate(t *testing.T) {
	t.Parallel()
	base := freezeBaseCall()
	baseTok := diag.StableCallToken(base)

	cases := map[string]func(*aipapi.Call){
		"authoritative": func(c *aipapi.Call) { c.Session.AuthoritativeSessionID = "sess-1" },
		"client":        func(c *aipapi.Call) { c.Session.ClientSessionID = "client-1" },
		"aleg":          func(c *aipapi.Call) { c.Session.ALegID = "a-1" },
		"resume":        func(c *aipapi.Call) { c.Session.ResumeToken = "tok-secret" },
		"continuity":    func(c *aipapi.Call) { c.Session.ContinuityKey = "ck-1" },
	}
	for name, mutate := range cases {
		c := freezeBaseCall()
		mutate(c)
		if diag.StableCallToken(c) == baseTok {
			t.Fatalf("session field %s must affect identity", name)
		}
	}

	if got := (aipapi.SessionRef{AuthoritativeSessionID: "sess-1", ClientSessionID: "client-1"}).CorrelationID(); got != "sess-1" {
		t.Fatalf("CorrelationID=%q want authoritative session", got)
	}
	if got := (aipapi.SessionRef{ClientSessionID: "client-1"}).CorrelationID(); got != "client-1" {
		t.Fatalf("CorrelationID=%q want client hint fallback", got)
	}
}

func TestStableIdentityFreeze_NonSerializedExcluded(t *testing.T) {
	t.Parallel()
	base := freezeBaseCall()
	baseTok := diag.StableCallToken(base)

	withMeta := freezeBaseCall()
	withMeta.Session.Metadata = map[string]string{"k": "v"}
	withMeta.Messages[0].Metadata = map[string]string{"m": "1"}
	withMeta.MaxPendingWireEvents = 7
	if diag.StableCallToken(withMeta) != baseTok {
		t.Fatal("json:\"-\" fields (Session.Metadata, Message.Metadata, MaxPendingWireEvents, Invocation) must not affect identity")
	}
}

func TestStableIdentityFreeze_SessionHeaderPrecedence(t *testing.T) {
	t.Parallel()
	var ref aipapi.SessionRef
	sessionwire.ApplyMetadata(&ref, map[string]string{
		sessionwire.MetaKeyAuthoritativeSessionID: "meta-sid",
		sessionwire.MetaKeyResumeToken:            "meta-tok",
	})
	h := http.Header{}
	h.Set(sessionwire.HeaderAuthoritativeSessionID, " hdr-sid ")
	h.Set(sessionwire.HeaderResumeToken, " hdr-tok ")
	sessionwire.ApplyAuthoritativeHeaders(&ref, h)
	if ref.AuthoritativeSessionID != "hdr-sid" || ref.ResumeToken != "hdr-tok" {
		t.Fatalf("headers must win over body metadata: %+v", ref)
	}

	metaCall := freezeBaseCall()
	metaCall.Session.AuthoritativeSessionID = "meta-sid"
	headerCall := freezeBaseCall()
	headerCall.Session.AuthoritativeSessionID = "hdr-sid"
	if diag.StableCallToken(metaCall) == diag.StableCallToken(headerCall) {
		t.Fatal("header-vs-metadata session values must produce distinct identity inputs")
	}
}

func TestStableIdentityFreeze_OptionalFields(t *testing.T) {
	t.Parallel()
	base := freezeBaseCall()
	baseTok := diag.StableCallToken(base)

	withMax := freezeBaseCall()
	withMax.Options.MaxOutputTokens = new(1024)
	if diag.StableCallToken(withMax) == baseTok {
		t.Fatal("MaxOutputTokens set must differ from unset")
	}
	withZeroMax := freezeBaseCall()
	withZeroMax.Options.MaxOutputTokens = new(0)
	if diag.StableCallToken(withZeroMax) == baseTok || diag.StableCallToken(withZeroMax) == diag.StableCallToken(withMax) {
		t.Fatal("MaxOutputTokens 0 must be distinct from unset and 1024")
	}

	withTemp := freezeBaseCall()
	withTemp.Options.Temperature = new(0.5)
	if diag.StableCallToken(withTemp) == baseTok {
		t.Fatal("Temperature set must differ from unset")
	}
	withTopP := freezeBaseCall()
	withTopP.Options.TopP = new(0.9)
	if diag.StableCallToken(withTopP) == baseTok {
		t.Fatal("TopP set must differ from unset")
	}

	withEffort := freezeBaseCall()
	withEffort.Options.ReasoningEffort = "high"
	if diag.StableCallToken(withEffort) == baseTok {
		t.Fatal("ReasoningEffort must affect identity")
	}
	withVerbosity := freezeBaseCall()
	withVerbosity.Options.Verbosity = aipapi.VerbosityLow
	if diag.StableCallToken(withVerbosity) == baseTok {
		t.Fatal("Verbosity must affect identity")
	}

	withPrev := freezeBaseCall()
	withPrev.PreviousResponseID = "resp_parent"
	if diag.StableCallToken(withPrev) == baseTok {
		t.Fatal("PreviousResponseID must affect identity")
	}
	withCacheKey := freezeBaseCall()
	withCacheKey.PromptCacheKey = "pc-1"
	if diag.StableCallToken(withCacheKey) == baseTok {
		t.Fatal("PromptCacheKey must affect identity")
	}
	withChoice := freezeBaseCall()
	withChoice.Tools = []aipapi.ToolDef{{Name: "get_weather"}}
	withChoice.ToolChoice = aipapi.ToolChoice{Mode: aipapi.ToolChoiceNone}
	noChoice := freezeBaseCall()
	noChoice.Tools = []aipapi.ToolDef{{Name: "get_weather"}}
	if diag.StableCallToken(withChoice) == diag.StableCallToken(noChoice) {
		t.Fatal("ToolChoice mode must affect identity")
	}
}

func TestStableIdentityFreeze_ResponseIDTimestampDerivation(t *testing.T) {
	t.Parallel()
	call := freezeBaseCall()
	call.Extensions = freezeModelExt("gpt-4o-mini")
	tok := diag.StableCallToken(call)
	ts := diag.StableUnix(call)

	if want := "resp_" + tok; want == "" || !strings.HasPrefix(want, "resp_") {
		t.Fatalf("response derivation broken: %q", want)
	}
	if want := "chatcmpl_" + tok; !strings.HasPrefix(want, "chatcmpl_") {
		t.Fatalf("completion derivation broken: %q", want)
	}
	if want := "msg_resp_" + tok; !strings.HasPrefix(want, "msg_") {
		t.Fatalf("message derivation broken: %q", want)
	}
	const base int64 = 1715620000
	if ts < base || ts >= base+86_400 {
		t.Fatalf("StableUnix=%d want [%d,%d)", ts, base, base+86_400)
	}
	if tm := diag.StableTime(call); tm.Location() != time.UTC || tm.Unix() != ts {
		t.Fatalf("StableTime=%v want UTC unix %d", tm, ts)
	}
	// Stream/non-stream parity follows because both writers derive from the
	// same token/unix (Task 1.6 end-to-end parity reused, not re-proven here).
	again := freezeBaseCall()
	again.Extensions = freezeModelExt("gpt-4o-mini")
	if diag.StableCallToken(again) != tok || diag.StableUnix(again) != ts {
		t.Fatal("same logical body must reproduce response identity inputs")
	}
}

func TestStableIdentityFreeze_TracePrecedence(t *testing.T) {
	t.Parallel()
	gen := lineage.NewTraceIDGenerator()
	first, second := gen.Next(), gen.Next()
	if first == second {
		t.Fatal("generator must be monotonic distinct")
	}
	for _, id := range []string{first, second} {
		if !strings.HasPrefix(id, "t_") || len(id) != 10 {
			t.Fatalf("trace id %q want t_%%08d shape", id)
		}
	}

	ctx := context.Background()
	if got := lineage.TraceID(ctx); got != "" {
		t.Fatalf("empty ctx trace=%q", got)
	}
	if got := diag.TraceID(ctx); got != "" {
		t.Fatalf("diag trace=%q", got)
	}
	combined := lineage.WithCallDiag(lineage.WithTraceID(ctx, "t_legacy"), "t_new", "a-1")
	if got := lineage.TraceID(combined); got != "t_new" {
		t.Fatalf("callDiag must win over legacy trace: %q", got)
	}
	if got := diag.TraceID(combined); got != "t_new" {
		t.Fatalf("diag re-export mismatch: %q", got)
	}
	if got := lineage.ALegID(combined); got != "a-1" {
		t.Fatalf("a-leg=%q", got)
	}
	if got := diag.ALegID(combined); got != "a-1" {
		t.Fatalf("diag a-leg mismatch: %q", got)
	}
	ensured := lineage.EnsureCallDiag(combined, "t_new", "a-1")
	if lineage.TraceID(ensured) != "t_new" || lineage.ALegID(ensured) != "a-1" {
		t.Fatal("EnsureCallDiag must preserve matching diag")
	}
}

func TestStableIdentityFreeze_BillingNamespaceSeparation(t *testing.T) {
	t.Parallel()
	call := freezeBaseCall()
	id := diag.StableCallID(call)
	if !strings.HasPrefix(id, "call_") {
		t.Fatalf("stable id=%q want call_ namespace", id)
	}
	if strings.HasPrefix(id, "bc_") {
		t.Fatalf("stable id must never use billing bc_ namespace: %q", id)
	}
	tok := diag.StableCallToken(call)
	for _, derived := range []string{"resp_" + tok, "chatcmpl_" + tok, "msg_resp_" + tok} {
		if strings.HasPrefix(derived, "bc_") {
			t.Fatalf("response id %q must not collide with billing namespace", derived)
		}
	}
	// bc_ shape/ownership (random, validated, per-invocation unique,
	// operation keys) is frozen by internal/core/billing/call_id_test.go and
	// is intentionally not re-implemented here.
}
