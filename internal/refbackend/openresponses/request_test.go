package openresponses

import (
	"strings"
	"testing"
)

func TestParseCreateRequest_BasicFields(t *testing.T) {
	t.Parallel()
	raw := `{
		"model":"gpt-openresponses-1",
		"input":[{"type":"message","role":"user","status":"received","content":[{"type":"input_text","text":"hi"}]}],
		"instructions":"be brief",
		"tools":[{"type":"function","name":"f","parameters":{"type":"object"}}],
		"tool_choice":"auto",
		"parallel_tool_calls":true,
		"temperature":0.7,
		"top_p":0.9,
		"max_output_tokens":100,
		"max_tool_calls":3,
		"truncation":"disabled",
		"store":true,
		"background":false,
		"previous_response_id":"resp_1",
		"metadata":{"org":"t"},
		"service_tier":"standard",
		"safety_identifier":"safe",
		"prompt_cache_key":"k",
		"prompt_cache_retention":"5m",
		"stream":true,
		"acme:routing":{"region":"eu"}
	}`
	req, err := parseCreateRequest([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if req.Model != "gpt-openresponses-1" || !req.Stream {
		t.Fatalf("model/stream: %q %v", req.Model, req.Stream)
	}
	if len(req.Items()) != 1 || req.Items()[0].Type != ItemMessage || req.Items()[0].Role != "user" {
		t.Fatalf("items: %+v", req.Items())
	}
	if req.Instructions == nil || *req.Instructions != "be brief" {
		t.Fatalf("instructions: %v", req.Instructions)
	}
	if len(req.Tools) != 1 || req.Tools[0].Name != "f" {
		t.Fatalf("tools: %+v", req.Tools)
	}
	if req.ParallelToolCalls == nil || !*req.ParallelToolCalls {
		t.Fatalf("parallel: %v", req.ParallelToolCalls)
	}
	if req.Temperature == nil || *req.Temperature != 0.7 || req.TopP == nil || *req.TopP != 0.9 {
		t.Fatalf("sampling: %+v", req)
	}
	if req.MaxOutputTokens == nil || *req.MaxOutputTokens != 100 || req.MaxToolCalls == nil || *req.MaxToolCalls != 3 {
		t.Fatalf("limits: %+v", req)
	}
	if req.PreviousResponseID == nil || *req.PreviousResponseID != "resp_1" {
		t.Fatalf("previous: %v", req.PreviousResponseID)
	}
	if req.ServiceTier != "standard" || req.SafetyIdentifier != "safe" || req.PromptCacheKey != "k" {
		t.Fatalf("tier/cache: %+v", req)
	}
	if req.Extensions["acme:routing"] == nil {
		t.Fatalf("extensions: %+v", req.Extensions)
	}
}

func TestParseCreateRequest_StringInput(t *testing.T) {
	t.Parallel()
	req, err := parseCreateRequest([]byte(`{"model":"m","input":"hello world"}`))
	if err != nil {
		t.Fatal(err)
	}
	if req.Input == nil || !req.Input.TextSet || req.Input.Text != "hello world" {
		t.Fatalf("input: %+v", req.Input)
	}
	if len(req.Items()) != 0 {
		t.Fatalf("string input must have zero items")
	}
}

func TestParseCreateRequest_MalformedBodies(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"not_json", `{`},
		{"array_root", `[1]`},
		{"bad_input", `{"input":123}`},
		{"bad_tools", `{"tools":"x"}`},
		{"bad_temperature", `{"temperature":"hot"}`},
		{"bad_stream", `{"stream":"x"}`},
		{"bad_store", `{"store":"x"}`},
		{"bad_item", `{"input":[{"type":123}]}`},
		{"unknown_item_type", `{"input":[{"type":"mystery"}]}`},
		{"item_missing_type", `{"input":[{"role":"user"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := parseCreateRequest([]byte(tc.raw)); err == nil {
				t.Fatalf("expected parse error for %q", tc.name)
			}
		})
	}
}

func TestParseCreateRequest_ExtensionItemsOpaque(t *testing.T) {
	t.Parallel()
	raw := `{"input":[{"type":"acme:telemetry","id":"t1","status":"completed","latency_ms":72}]}`
	req, err := parseCreateRequest([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	items := req.Items()
	if len(items) != 1 || !items[0].IsExtension() || items[0].Type != "acme:telemetry" {
		t.Fatalf("extension item: %+v", items)
	}
	if len(items[0].Opaque) == 0 {
		t.Fatal("extension item must preserve opaque bytes")
	}
	types := req.ExtensionItemTypes()
	if len(types) != 1 || types[0] != "acme:telemetry" {
		t.Fatalf("extension item types: %v", types)
	}
}

func TestParseCreateRequest_ReasoningAndPhase(t *testing.T) {
	t.Parallel()
	raw := `{"input":[{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"sum"}],"encrypted_content":null},{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"done"}]}]}`
	req, err := parseCreateRequest([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	items := req.Items()
	if len(items) != 2 {
		t.Fatalf("items: %d", len(items))
	}
	if items[0].Type != ItemReasoning || items[0].Reasoning == nil || len(items[0].Reasoning.Summary) != 1 {
		t.Fatalf("reasoning: %+v", items[0])
	}
	if !items[0].Reasoning.EncryptedContentSet {
		t.Fatal("encrypted_content null presence must be tracked")
	}
	if items[1].Phase != "final_answer" || items[1].Role != "assistant" {
		t.Fatalf("phase message: %+v", items[1])
	}
}

func TestParseCompactRequest_Subset(t *testing.T) {
	t.Parallel()
	raw := `{"model":"m","input":[{"type":"message","role":"user","content":"compress"}],"instructions":"summarize","reasoning":{"summary":"auto"},"prompt_cache_key":"k","acme:mode":"fast"}`
	req, err := parseCompactRequest([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if req.Model != "m" || len(req.Items()) != 1 || req.PromptCacheKey != "k" {
		t.Fatalf("compact: %+v", req)
	}
	if req.Instructions == nil || *req.Instructions != "summarize" {
		t.Fatalf("instructions: %v", req.Instructions)
	}
	if req.Extensions["acme:mode"] == nil {
		t.Fatalf("extensions: %+v", req.Extensions)
	}
	if req.Reasoning == nil || !strings.Contains(string(req.Reasoning), "auto") {
		t.Fatalf("reasoning: %s", req.Reasoning)
	}
}

func TestCheckExpected_Assertions(t *testing.T) {
	t.Parallel()
	stream := true
	raw := []byte(`{"model":"m","stream":true,"input":[{"type":"message","role":"user","content":"hi"},{"type":"acme:mode","id":"m1"}],"tools":[{"type":"function","name":"f"}]}`)
	req, err := parseCreateRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	r := fakeRequest("POST", "/v1/responses", "application/json", "Bearer sk")

	exp := ExpectedRequest{
		Method: "POST", PathSuffix: "/responses", ContentType: "application/json",
		Auth: AuthBearer, Model: "m", Stream: &stream,
		MinInputItems: 1, MaxInputItems: 2, RequireTools: 1,
		RequireExtensionItems: []string{"acme:mode"},
		Contains:              []string{`"model":"m"`},
		MustOmit:              []string{"previous_response_id"},
	}
	if fails := checkExpected(exp, r, req, raw); len(fails) != 0 {
		t.Fatalf("unexpected assertion failures: %v", fails)
	}

	bad := exp
	bad.Model = "other"
	bad.Stream = nil
	bad.RequireExtensionItems = []string{"acme:nope"}
	bad.Contains = []string{"bogus"}
	bad.MustOmit = []string{"stream"}
	if fails := checkExpected(bad, r, req, raw); len(fails) < 4 {
		t.Fatalf("expected multiple failures, got %v", fails)
	}
}

func TestCheckCompactExpected_Assertions(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"model":"m","input":[{"type":"compaction","id":"cmp_1"}]}`)
	req, err := parseCompactRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	r := fakeRequest("POST", "/responses/compact", "application/json", "Bearer sk")
	exp := ExpectedRequest{
		Method: "POST", PathSuffix: "/responses/compact", ContentType: "application/json",
		Model: "m", MinInputItems: 1, MaxInputItems: 1,
	}
	if fails := checkCompactExpected(exp, r, req, raw); len(fails) != 0 {
		t.Fatalf("unexpected failures: %v", fails)
	}
	bad := exp
	bad.Model = "nope"
	bad.MaxInputItems = 0
	if fails := checkCompactExpected(bad, r, req, raw); len(fails) != 1 {
		t.Fatalf("expected model failure, got %v", fails)
	}
}
