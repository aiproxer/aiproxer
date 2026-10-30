package openresponses

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCreateParams_MarshalsStringAndItemsInput(t *testing.T) {
	t.Parallel()

	p := CreateParams{
		Model: "gpt-openresponses-1",
		Input: Input{Text: "ping"},
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"input":"ping"`) {
		t.Fatalf("string input: %s", b)
	}

	p2 := CreateParams{
		Model: "gpt-openresponses-1",
		Input: Input{Items: []Item{NewMessageItem("user", "input_text", "ping")}},
	}
	b2, err := json.Marshal(p2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b2), `"type":"message"`) || !strings.Contains(string(b2), `"input":[`) {
		t.Fatalf("items input: %s", b2)
	}
}

func TestCreateParams_MarshalsControlsAndPresence(t *testing.T) {
	t.Parallel()
	tc := 0.7
	topP := 1.0
	maxOut := 200
	store := true
	previous := "resp_prev_1"
	p := CreateParams{
		Model:              "gpt-openresponses-1",
		Input:              Input{Text: "hi"},
		Instructions:       new("be brief"),
		Tools:              []Tool{{Type: "function", Name: "f", Description: "d"}},
		ToolChoice:         json.RawMessage(`{"type":"function","name":"f"}`),
		ParallelToolCalls:  new(true),
		Temperature:        &tc,
		TopP:               &topP,
		MaxOutputTokens:    &maxOut,
		MaxToolCalls:       new(1),
		Truncation:         "disabled",
		Text:               json.RawMessage(`{"format":{"type":"text"}}`),
		Reasoning:          json.RawMessage(`{"effort":"medium","summary":"auto"}`),
		Store:              &store,
		PreviousResponseID: &previous,
		Metadata:           json.RawMessage(`{"org":"test"}`),
		ServiceTier:        "standard",
		PromptCacheKey:     "k1",
		Stream:             true,
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		`"model":"gpt-openresponses-1"`,
		`"instructions":"be brief"`,
		`"tool_choice":{"type":"function","name":"f"}`,
		`"parallel_tool_calls":true`,
		`"temperature":0.7`,
		`"top_p":1`,
		`"max_output_tokens":200`,
		`"max_tool_calls":1`,
		`"truncation":"disabled"`,
		`"store":true`,
		`"previous_response_id":"resp_prev_1"`,
		`"stream":true`,
		`"service_tier":"standard"`,
		`"prompt_cache_key":"k1"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
}

func TestCreateParams_OmitsZeroControls(t *testing.T) {
	t.Parallel()
	p := CreateParams{Model: "m", Input: Input{Text: "x"}}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, notWanted := range []string{"previous_response_id", "instructions", "tool_choice", "stream", "temperature"} {
		if strings.Contains(s, notWanted) {
			t.Errorf("unexpected %q present in %s", notWanted, s)
		}
	}
}

func TestCreateParams_ExtensionsRoundTrip(t *testing.T) {
	t.Parallel()
	p := CreateParams{
		Model: "m",
		Input: Input{Text: "x"},
		Extensions: map[string]json.RawMessage{
			"acme:routing": json.RawMessage(`{"region":"eu"}`),
		},
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"acme:routing":{"region":"eu"}`) {
		t.Fatalf("extensions: %s", b)
	}
}

func TestCompactParams_Marshals(t *testing.T) {
	t.Parallel()
	p := CompactParams{
		Model:          "m",
		Input:          Input{Items: []Item{NewMessageItem("user", "input_text", "compact me")}},
		PromptCacheKey: "openresponses-compact-test",
		Reasoning:      json.RawMessage(`{"summary":"auto"}`),
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"model":"m"`) || !strings.Contains(s, `"prompt_cache_key":"openresponses-compact-test"`) {
		t.Fatalf("compact params: %s", s)
	}
	if strings.Contains(s, `"stream":`) {
		t.Fatalf("compact must not carry stream control: %s", s)
	}
}

func TestInput_MarshalRejectsBothForms(t *testing.T) {
	t.Parallel()
	// Neither text nor items set: marshal as null.
	i := Input{}
	b, err := json.Marshal(i)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "null" {
		t.Fatalf("empty input: %s", b)
	}
}

//go:fix inline
func strPtr(s string) *string { return new(s) }

//go:fix inline
func boolPtr(b bool) *bool { return new(b) }

//go:fix inline
func intPtr(i int) *int { return new(i) }
