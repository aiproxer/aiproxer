package proxymemory

import (
	"encoding/json"
	"io/fs"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
)

func TestPhase1BuildRequestEnvelope(t *testing.T) {
	t.Parallel()

	req, err := BuildPhase1Request(Config{ExtractRoute: "route-extract", Phase1InputTokens: 10000}, Phase1Input{
		SessionRef:   "session-2d711642b726",
		ProjectLabel: "/w/app",
		Transcript:   `[{"type":"message","role":"user","text":"hi"}]`,
	})
	if err != nil {
		t.Fatalf("BuildPhase1Request: %v", err)
	}
	if req.Call == nil {
		t.Fatalf("BuildPhase1Request returned a nil call")
	}
	if got, want := req.Role, "proxy_memory_phase1"; got != want {
		t.Errorf("Role = %q, want %q", got, want)
	}
	if got, want := req.Visibility, "private"; got != want {
		t.Errorf("Visibility = %q, want %q", got, want)
	}
	if got, want := req.SessionMode, auxiliary.SessionModeDetached; got != want {
		t.Errorf("SessionMode = %v, want SessionModeDetached", got)
	}
	if got, want := req.DisablePlugins, []string{"proxy-memory", "proxy-memory.capture"}; !reflect.DeepEqual(got, want) {
		t.Errorf("DisablePlugins = %#v, want %#v", got, want)
	}
	if got, want := CaptureHandlerID, "proxy-memory.capture"; got != want {
		t.Errorf("CaptureHandlerID = %q, want %q", got, want)
	}
	if got, want := req.Call.Route.Selector, "route-extract"; got != want {
		t.Errorf("Route.Selector = %q, want %q", got, want)
	}
	if got, want := req.Call.ToolChoice.Mode, aipapi.ToolChoiceNone; got != want {
		t.Errorf("ToolChoice.Mode = %q, want %q", got, want)
	}
	if len(req.Call.Tools) != 0 {
		t.Errorf("Tools = %#v, want none", req.Call.Tools)
	}
	if got, want := req.Call.Options.ReasoningEffort, "low"; got != want {
		t.Errorf("ReasoningEffort = %q, want %q", got, want)
	}
	if got, want := req.Call.Options.ResponseMIMEType, "application/json"; got != want {
		t.Errorf("ResponseMIMEType = %q, want %q", got, want)
	}
	if err := req.Call.Validate(); err != nil {
		t.Errorf("Call.Validate() = %v, want nil", err)
	}
}

func TestPhase1BuildRequestSystemMessageIsTheVendoredTemplate(t *testing.T) {
	t.Parallel()

	system, err := templatesFS.ReadFile(stageOneSystemTemplate)
	if err != nil {
		t.Fatalf("read embedded %s: %v", stageOneSystemTemplate, err)
	}
	if len(system) == 0 {
		t.Fatalf("embedded %s is empty", stageOneSystemTemplate)
	}
	req, err := BuildPhase1Request(Config{ExtractRoute: "route-extract", Phase1InputTokens: 10000}, Phase1Input{
		SessionRef:   "session-2d711642b726",
		ProjectLabel: "/w/app",
		Transcript:   "[]",
	})
	if err != nil {
		t.Fatalf("BuildPhase1Request: %v", err)
	}
	if req.Call == nil {
		t.Fatalf("BuildPhase1Request returned a nil call")
	}
	if len(req.Call.Messages) != 2 {
		t.Fatalf("Messages = %d, want 2 (system, user)", len(req.Call.Messages))
	}
	systemMessage := req.Call.Messages[0]
	if systemMessage.Role != aipapi.RoleSystem {
		t.Fatalf("Messages[0].Role = %q, want %q", systemMessage.Role, aipapi.RoleSystem)
	}
	if len(systemMessage.Parts) != 1 || systemMessage.Parts[0].Kind != aipapi.PartText {
		t.Fatalf("Messages[0].Parts = %#v, want one text part", systemMessage.Parts)
	}
	if got := systemMessage.Parts[0].Text; got != string(system) {
		t.Errorf("system text differs from the embedded %s:\n got: %q\nwant: %q", stageOneSystemTemplate, got, string(system))
	}
}

// TestPhase1BuildRequestUsesTheConfiguredPhaseTarget pins the first consumer
// of the per-phase model policy: the operator's declared default effort and
// model reach the built request, and the route selector is derived from the
// operator's default route and the declared backend and model instead of
// being copied from a client-facing field (design §3).
func TestPhase1BuildRequestUsesTheConfiguredPhaseTarget(t *testing.T) {
	t.Parallel()

	cfg, err := DecodeConfig(mapToNode(t, "principals: [alice]\n"+
		"extract_route: openai:gpt-5-mini\n"+
		"consolidate_route: openai:gpt-5\n"+
		"models:\n"+
		"  extraction:\n"+
		"    defaults:\n"+
		"      model: gpt-5\n"+
		"      reasoning_effort: high\n"+
		"    locks: {}\n"+
		"    allowed: {}\n"+
		"  consolidation:\n"+
		"    defaults: {}\n"+
		"    locks: {}\n"+
		"    allowed: {}\n"))
	if err != nil {
		t.Fatalf("DecodeConfig() error: %v", err)
	}
	req, err := BuildPhase1Request(cfg, Phase1Input{
		SessionRef:   "session-0123456789ab",
		ProjectLabel: "/w/project-sentinel",
		Transcript:   "[]",
	})
	if err != nil {
		t.Fatalf("BuildPhase1Request: %v", err)
	}
	if got, want := req.Call.Options.ReasoningEffort, "high"; got != want {
		t.Errorf("ReasoningEffort = %q, want %q", got, want)
	}
	if got, want := req.Call.Route.Selector, "openai:gpt-5"; got != want {
		t.Errorf("Route.Selector = %q, want %q", got, want)
	}
}

func TestPhase1BuildRequestUserMessageSubstitutesInputs(t *testing.T) {
	t.Parallel()

	transcript := `[{"type":"message","role":"user","text":"the-transcript-sentinel"}]`
	req, err := BuildPhase1Request(Config{ExtractRoute: "route-extract", Phase1InputTokens: 10000}, Phase1Input{
		SessionRef:   "session-0123456789ab",
		ProjectLabel: "/w/project-sentinel",
		Transcript:   transcript,
	})
	if err != nil {
		t.Fatalf("BuildPhase1Request: %v", err)
	}
	if req.Call == nil {
		t.Fatalf("BuildPhase1Request returned a nil call")
	}
	if len(req.Call.Messages) != 2 {
		t.Fatalf("Messages = %d, want 2 (system, user)", len(req.Call.Messages))
	}
	user := req.Call.Messages[1]
	if user.Role != aipapi.RoleUser {
		t.Fatalf("Messages[1].Role = %q, want %q", user.Role, aipapi.RoleUser)
	}
	if len(user.Parts) != 1 || user.Parts[0].Kind != aipapi.PartText {
		t.Fatalf("Messages[1].Parts = %#v, want one text part", user.Parts)
	}
	text := user.Parts[0].Text
	for _, want := range []string{"session-0123456789ab", "/w/project-sentinel", transcript} {
		if !strings.Contains(text, want) {
			t.Errorf("user text does not contain %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "{{") {
		t.Errorf("user text still contains an unsubstituted template token:\n%s", text)
	}
}

func TestPhase1BuildRequestTranscriptBudget(t *testing.T) {
	t.Parallel()

	overLimit := strings.Repeat("A", 400) + "middle-sentinel" + strings.Repeat("B", 400)
	underLimit := `[{"type":"message","role":"user","text":"short"}]`

	cases := []struct {
		name        string
		tokens      int
		transcript  string
		wantMarker  bool
		wantContain []string
		wantMissing []string
	}{
		{
			name:        "an over-limit transcript is truncated with the marker",
			tokens:      8,
			transcript:  overLimit,
			wantMarker:  true,
			wantContain: []string{strings.Repeat("A", 16), strings.Repeat("B", 16)},
			wantMissing: []string{"middle-sentinel"},
		},
		{
			name:        "a transcript within the limit is included verbatim",
			tokens:      10000,
			transcript:  underLimit,
			wantMarker:  false,
			wantContain: []string{underLimit},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req, err := BuildPhase1Request(Config{ExtractRoute: "route-extract", Phase1InputTokens: tc.tokens}, Phase1Input{
				SessionRef:   "session-0123456789ab",
				ProjectLabel: "/w/app",
				Transcript:   tc.transcript,
			})
			if err != nil {
				t.Fatalf("BuildPhase1Request: %v", err)
			}
			if req.Call == nil {
				t.Fatalf("BuildPhase1Request returned a nil call")
			}
			text := req.Call.Messages[1].Parts[0].Text
			if got := strings.Contains(text, "tokens truncated"); got != tc.wantMarker {
				t.Errorf("truncation marker present = %v, want %v", got, tc.wantMarker)
			}
			for _, want := range tc.wantContain {
				if !strings.Contains(text, want) {
					t.Errorf("user text does not contain %q", want)
				}
			}
			for _, missing := range tc.wantMissing {
				if strings.Contains(text, missing) {
					t.Errorf("user text still contains %q", missing)
				}
			}
			if want := TruncateTokens(tc.transcript, tc.tokens); !strings.Contains(text, want) {
				t.Errorf("user text does not contain TruncateTokens(transcript, %d)", tc.tokens)
			}
		})
	}
}

func TestPhase1BuildRequestRequiresRoute(t *testing.T) {
	t.Parallel()

	if _, err := BuildPhase1Request(Config{ExtractRoute: "   "}, Phase1Input{}); err == nil {
		t.Fatal("BuildPhase1Request with a blank extract route: want error, got nil")
	}
}

func TestPhase1SessionRef(t *testing.T) {
	t.Parallel()

	if got, want := SessionRef("x"), "session-2d711642b726"; got != want {
		t.Errorf("SessionRef(%q) = %q, want %q", "x", got, want)
	}
	shape := regexp.MustCompile(`^session-[0-9a-f]{12}$`)
	for _, id := range []string{"", "x", "a-leg-0123456789", "żółć-ülç"} {
		ref := SessionRef(id)
		if !shape.MatchString(ref) {
			t.Errorf("SessionRef(%q) = %q, want session- plus 12 lowercase hex characters", id, ref)
		}
	}
	if SessionRef("a-leg-a") == SessionRef("a-leg-b") {
		t.Error("distinct A-leg ids produced the same session ref")
	}
	if re := regexp.MustCompile(`a-leg`); re.MatchString(SessionRef("a-leg-0123456789")) {
		t.Error("SessionRef echoed the raw A-leg id")
	}
}

func TestPhase1Parse(t *testing.T) {
	t.Parallel()

	longSlug := strings.Repeat("a", 70)
	cases := []struct {
		name string
		in   string
		want Phase1Output
	}{
		{
			name: "plain JSON",
			in:   `{"raw_memory":"raw text","rollout_summary":"summary text","rollout_slug":"fix-tests"}`,
			want: Phase1Output{RawMemory: "raw text", RolloutSummary: "summary text", RolloutSlug: "fix-tests"},
		},
		{
			name: "a fenced ```json reply",
			in:   "```json\n{\"raw_memory\":\"raw text\",\"rollout_summary\":\"summary text\",\"rollout_slug\":\"fix-tests\"}\n```",
			want: Phase1Output{RawMemory: "raw text", RolloutSummary: "summary text", RolloutSlug: "fix-tests"},
		},
		{
			name: "a bare fenced reply with trailing whitespace",
			in:   "```\n{\"raw_memory\":\"raw text\",\"rollout_summary\":\"summary text\",\"rollout_slug\":\"fix-tests\"}\n```\n",
			want: Phase1Output{RawMemory: "raw text", RolloutSummary: "summary text", RolloutSlug: "fix-tests"},
		},
		{
			name: "a null slug becomes session",
			in:   `{"raw_memory":"raw","rollout_summary":"summary","rollout_slug":null}`,
			want: Phase1Output{RawMemory: "raw", RolloutSummary: "summary", RolloutSlug: "session"},
		},
		{
			name: "an empty slug becomes session",
			in:   `{"raw_memory":"raw","rollout_summary":"summary","rollout_slug":""}`,
			want: Phase1Output{RawMemory: "raw", RolloutSummary: "summary", RolloutSlug: "session"},
		},
		{
			name: "a missing slug becomes session",
			in:   `{"raw_memory":"raw","rollout_summary":"summary"}`,
			want: Phase1Output{RawMemory: "raw", RolloutSummary: "summary", RolloutSlug: "session"},
		},
		{
			name: "a slug with spaces and capitals is normalized",
			in:   `{"raw_memory":"raw","rollout_summary":"summary","rollout_slug":"  My  Fancy Slug! "}`,
			want: Phase1Output{RawMemory: "raw", RolloutSummary: "summary", RolloutSlug: "my-fancy-slug"},
		},
		{
			name: "a long slug is cut to 60 characters",
			in:   `{"raw_memory":"raw","rollout_summary":"summary","rollout_slug":"` + longSlug + `"}`,
			want: Phase1Output{RawMemory: "raw", RolloutSummary: "summary", RolloutSlug: strings.Repeat("a", 60)},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParsePhase1(tc.in)
			if err != nil {
				t.Fatalf("ParsePhase1(%q): %v", tc.in, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ParsePhase1(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}

func TestPhase1ParseRejects(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
	}{
		{"an unknown field", `{"raw_memory":"raw","rollout_summary":"summary","rollout_slug":"s","extra":"x"}`},
		{"an empty summary", `{"raw_memory":"raw","rollout_summary":"","rollout_slug":"s"}`},
		{"a whitespace-only summary", `{"raw_memory":"raw","rollout_summary":" \n\t ","rollout_slug":"s"}`},
		{"a NUL-only summary", `{"raw_memory":"raw","rollout_summary":"\u0000","rollout_slug":"s"}`},
		{"a missing summary", `{"raw_memory":"raw","rollout_slug":"s"}`},
		{"a null summary", `{"raw_memory":"raw","rollout_summary":null,"rollout_slug":"s"}`},
		{"a null raw_memory", `{"raw_memory":null,"rollout_summary":"summary","rollout_slug":"s"}`},
		{"a missing raw_memory", `{"rollout_summary":"summary","rollout_slug":"s"}`},
		{"a numeric slug", `{"raw_memory":"raw","rollout_summary":"summary","rollout_slug":7}`},
		{"a numeric raw_memory", `{"raw_memory":7,"rollout_summary":"summary","rollout_slug":"s"}`},
		{"plain text", `not json at all`},
		{"two JSON values", `{"raw_memory":"raw","rollout_summary":"summary","rollout_slug":"s"}{"raw_memory":"raw2","rollout_summary":"summary2","rollout_slug":"s"}`},
		{"an opening fence without a closing fence", "```json\n{\"raw_memory\":\"raw\",\"rollout_summary\":\"summary\",\"rollout_slug\":\"s\"}"},
		{"empty input", ``},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParsePhase1(tc.in)
			if err == nil {
				t.Fatalf("ParsePhase1(%q) = %#v, want error", tc.in, got)
			}
		})
	}
}

func TestPhase1ParseSanitizes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want Phase1Output
	}{
		{
			name: "a secret in the raw memory is redacted",
			in:   `{"raw_memory":"use sk-abcdefghijklmnopqrstuvwx now","rollout_summary":"summary","rollout_slug":"s"}`,
			want: Phase1Output{RawMemory: "use [REDACTED_SECRET] now", RolloutSummary: "summary", RolloutSlug: "s"},
		},
		{
			name: "a secret assignment in the summary is redacted",
			in:   `{"raw_memory":"raw","rollout_summary":"set api_key = \"abcdefgh\" first","rollout_slug":"s"}`,
			want: Phase1Output{RawMemory: "raw", RolloutSummary: `set api_key = "[REDACTED_SECRET]" first`, RolloutSlug: "s"},
		},
		{
			name: "a bearer token in the raw memory is redacted",
			in:   `{"raw_memory":"Authorization: Bearer abcdefghijklmnop12345","rollout_summary":"summary","rollout_slug":"s"}`,
			want: Phase1Output{RawMemory: "Authorization: Bearer [REDACTED_SECRET]", RolloutSummary: "summary", RolloutSlug: "s"},
		},
		{
			name: "NUL bytes are removed from the raw memory and summary",
			in:   "{\"raw_memory\":\"a\\u0000b\",\"rollout_summary\":\"s\\u0000s\",\"rollout_slug\":\"s\"}",
			want: Phase1Output{RawMemory: "ab", RolloutSummary: "ss", RolloutSlug: "s"},
		},
		{
			name: "a NUL-split key is spliced and redacted",
			in:   "{\"raw_memory\":\"sk-\\u0000abcdefghijklmnopqrstuvwx\",\"rollout_summary\":\"summary\",\"rollout_slug\":\"s\"}",
			want: Phase1Output{RawMemory: "[REDACTED_SECRET]", RolloutSummary: "summary", RolloutSlug: "s"},
		},
		{
			name: "a secret in the slug is redacted after normalization",
			in:   `{"raw_memory":"raw","rollout_summary":"summary","rollout_slug":"SK-ABCDEFGHIJKLMNOPQRSTUVWX"}`,
			want: Phase1Output{RawMemory: "raw", RolloutSummary: "summary", RolloutSlug: "[REDACTED_SECRET]"},
		},
		{
			name: "NUL bytes are removed from the slug",
			in:   "{\"raw_memory\":\"raw\",\"rollout_summary\":\"summary\",\"rollout_slug\":\"a\\u0000b\"}",
			want: Phase1Output{RawMemory: "raw", RolloutSummary: "summary", RolloutSlug: "ab"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParsePhase1(tc.in)
			if err != nil {
				t.Fatalf("ParsePhase1(%q): %v", tc.in, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ParsePhase1(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}

func TestPhase1VendoredCodexTemplateFiles(t *testing.T) {
	t.Parallel()

	entries, err := fs.ReadDir(templatesFS, "templates/codex")
	if err != nil {
		t.Fatalf("read embedded templates/codex: %v", err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	want := []string{
		"NOTICE.md",
		"ad_hoc_instructions.md",
		"consolidation.md",
		"read_path.md",
		"stage_one_input.md",
		"stage_one_system.md",
	}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("templates/codex files = %#v, want %#v", names, want)
	}
}

func TestPhase1RenderTemplate(t *testing.T) {
	t.Parallel()

	if got, want := renderTemplate("a {{ known }} b", templateVar{"known", "X"}), "a X b"; got != want {
		t.Errorf("renderTemplate sparse = %q, want %q", got, want)
	}
	if got, want := renderTemplate("a {{ known }} {{ unknown }}", templateVar{"known", "X"}), "a X {{ unknown }}"; got != want {
		t.Errorf("renderTemplate unknown token = %q, want %q", got, want)
	}
	if got, want := renderTemplate("{{ a }}", templateVar{"a", "{{ b }}"}, templateVar{"b", "B"}), "{{ b }}"; got != want {
		t.Errorf("renderTemplate must not rescan substituted values: = %q, want %q", got, want)
	}
}

// TestPhase1ParseSlugRedactsOriginalBeforeNormalization pins the slug
// sanitization order required to keep lossy normalization from destroying
// secret shapes before the redactor can see them (design §7.2 and §4;
// Requirement 3.3): NUL-strip → Redact → normalize → Redact. Re-running Redact
// on a broken output proves nothing — a leaked lowercase key is already a
// Redact fixed point — so every case asserts the exact safe slug and the
// absence of the secret value itself.
func TestPhase1ParseSlugRedactsOriginalBeforeNormalization(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		slug   string
		secret string
		want   string
	}{
		{
			name:   "a password assignment",
			slug:   "password=abcdefgh",
			secret: "abcdefgh",
			want:   "password-redacted-secret",
		},
		{
			name:   "a bearer token",
			slug:   "Bearer abcdefghijklmnop12345",
			secret: "abcdefghijklmnop12345",
			want:   "bearer-redacted-secret",
		},
		{
			name:   "an AWS access key",
			slug:   "AKIAIOSFODNN7EXAMPLE",
			secret: "akiaiosfodnn7example",
			want:   "redacted-secret",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if Redact(tc.slug) == tc.slug {
				t.Fatalf("synthetic fixture %q is not recognized by Redact", tc.slug)
			}
			reply, err := json.Marshal(map[string]string{
				"raw_memory":      "raw",
				"rollout_summary": "summary",
				"rollout_slug":    tc.slug,
			})
			if err != nil {
				t.Fatalf("marshal phase 1 reply: %v", err)
			}
			got, err := ParsePhase1(string(reply))
			if err != nil {
				t.Fatalf("ParsePhase1(%q): %v", tc.slug, err)
			}
			if got.RolloutSlug != tc.want {
				t.Errorf("RolloutSlug(%q) = %q, want %q", tc.slug, got.RolloutSlug, tc.want)
			}
			if strings.Contains(strings.ToLower(got.RolloutSlug), tc.secret) {
				t.Errorf("RolloutSlug(%q) = %q still contains the secret value %q", tc.slug, got.RolloutSlug, tc.secret)
			}
		})
	}
}

// TestPhase1ParseSlugNULSpliceIsRedacted extends the NUL-splice protection to
// the slug path: NUL bytes are stripped before Redact, so a key split by a NUL
// is spliced back into the shape the redactor matches before normalization can
// lower or dash it away (design §5 step 1 and §7.2; Requirement 3.3).
func TestPhase1ParseSlugNULSpliceIsRedacted(t *testing.T) {
	t.Parallel()

	got, err := ParsePhase1("{\"raw_memory\":\"raw\",\"rollout_summary\":\"summary\",\"rollout_slug\":\"sk-\\u0000abcdefghijklmnopqrstuvwx\"}")
	if err != nil {
		t.Fatalf("ParsePhase1: %v", err)
	}
	if want := "redacted-secret"; got.RolloutSlug != want {
		t.Errorf("RolloutSlug = %q, want %q", got.RolloutSlug, want)
	}
	if strings.Contains(strings.ToLower(got.RolloutSlug), "abcdefghijklmnopqrstuvwx") {
		t.Errorf("RolloutSlug = %q still contains the secret value", got.RolloutSlug)
	}
}

// TestPhase1BuildRequestTranscriptCutRedaction pins that the phase-1
// transcript truncation can neither recreate nor fragment a secret value
// (Requirement 3.3; design §5 and §7.2). TruncateTokens is lossy in the
// redaction sense: removing bytes can create the word boundary the patterns
// need and the inserted marker can supply one, while a value the original
// already matched would otherwise survive as an unmatchable fragment. The
// rendered transcript is therefore sanitized before and after the cut.
func TestPhase1BuildRequestTranscriptCutRedaction(t *testing.T) {
	t.Parallel()

	const key = "AKIAABCDEFGHIJKLMNOP" // synthetic AKIA-shaped key

	t.Run("a cut that lands on the key keeps the rendered message redacted", func(t *testing.T) {
		t.Parallel()

		// EncodeTranscript output: the key stays glued to the preceding word
		// bytes, so it is not a shape Redact matches and the encoded
		// transcript is a Redact fixpoint.
		entry := Entry{Type: "message", Role: "assistant", Text: strings.Repeat("x", 20000) + key + " trailing padding"}
		transcript, cut, err := EncodeTranscript([]Entry{entry}, 1<<20)
		if err != nil {
			t.Fatalf("EncodeTranscript: %v", err)
		}
		if cut {
			t.Fatalf("fixture premise: EncodeTranscript truncated the entry")
		}
		if Redact(transcript) != transcript {
			t.Fatalf("fixture premise: the encoded transcript is not a Redact fixpoint")
		}
		keyOffset := strings.Index(transcript, key)
		if keyOffset <= 0 || transcript[keyOffset-1] != 'x' {
			t.Fatalf("fixture premise: the key is not glued to preceding word bytes (offset %d)", keyOffset)
		}
		// Pick the limit whose kept tail starts exactly at the key: the
		// truncation marker then supplies the newline boundary Redact matches.
		limit := (len(transcript) - keyOffset) / 2
		if tailStart := len(transcript) - 2*limit; tailStart != keyOffset {
			t.Fatalf("fixture premise: the cut would start at %d, want the key offset %d", tailStart, keyOffset)
		}

		req, err := BuildPhase1Request(Config{ExtractRoute: "route-extract", Phase1InputTokens: limit}, Phase1Input{
			SessionRef:   "session-2d711642b726",
			ProjectLabel: "/w/app",
			Transcript:   transcript,
		})
		if err != nil {
			t.Fatalf("BuildPhase1Request: %v", err)
		}
		if req.Call == nil || len(req.Call.Messages) != 2 || len(req.Call.Messages[1].Parts) != 1 {
			t.Fatalf("BuildPhase1Request returned an unexpected call: %#v", req.Call)
		}
		if err := req.Call.Validate(); err != nil {
			t.Fatalf("Call.Validate() = %v, want nil", err)
		}
		user := req.Call.Messages[1].Parts[0].Text
		if !strings.Contains(user, "tokens truncated") {
			t.Fatalf("fixture premise: the transcript was not cut, no truncation marker in the user message")
		}
		if strings.Contains(user, key) {
			t.Errorf("the user message still contains the stored key %q at the truncation boundary", key)
		}
		if got := Redact(user); got != user {
			t.Errorf("the rendered user message is not a Redact fixpoint: Redact(user) != user")
		}
	})

	t.Run("a cut into a matchable value leaves no key fragment", func(t *testing.T) {
		t.Parallel()

		// The field is documented as EncodeTranscript output, but the
		// before-and-after wrap keeps any value the field carries safe: this
		// transcript is not a Redact fixpoint, so only a pass before the cut
		// can stop the cut from leaving an unmatchable key fragment.
		transcript := strings.Repeat("x", 20000) + " " + key + " trailing padding"
		if Redact(transcript) == transcript {
			t.Fatalf("fixture premise: the transcript is already a Redact fixpoint")
		}
		keyOffset := strings.Index(transcript, key)
		const overCut = 5 // the cut tail starts five bytes into the key
		limit := (len(transcript) - keyOffset - overCut) / 2
		if tailStart := len(transcript) - 2*limit; tailStart != keyOffset+overCut {
			t.Fatalf("fixture premise: the cut would start at %d, want %d", tailStart, keyOffset+overCut)
		}

		req, err := BuildPhase1Request(Config{ExtractRoute: "route-extract", Phase1InputTokens: limit}, Phase1Input{
			SessionRef:   "session-2d711642b726",
			ProjectLabel: "/w/app",
			Transcript:   transcript,
		})
		if err != nil {
			t.Fatalf("BuildPhase1Request: %v", err)
		}
		if req.Call == nil || len(req.Call.Messages) != 2 || len(req.Call.Messages[1].Parts) != 1 {
			t.Fatalf("BuildPhase1Request returned an unexpected call: %#v", req.Call)
		}
		if err := req.Call.Validate(); err != nil {
			t.Fatalf("Call.Validate() = %v, want nil", err)
		}
		user := req.Call.Messages[1].Parts[0].Text
		if !strings.Contains(user, "tokens truncated") {
			t.Fatalf("fixture premise: the transcript was not cut, no truncation marker in the user message")
		}
		if strings.Contains(user, key) {
			t.Errorf("the user message still contains the stored key %q", key)
		}
		for i := 0; i+4 <= len(key); i++ {
			if gram := key[i : i+4]; strings.Contains(user, gram) {
				t.Errorf("the user message still contains the key fragment %q: the cut fragmented a matchable value", gram)
			}
		}
		if got := Redact(user); got != user {
			t.Errorf("the rendered user message is not a Redact fixpoint: Redact(user) != user")
		}
	})
}
