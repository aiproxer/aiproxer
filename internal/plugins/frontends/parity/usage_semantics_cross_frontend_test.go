package parity_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/anthropic"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/gemini"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// wireUsage carries the usage counters of the four families; each family fills the fields its own protocol reports.
type wireUsage struct {
	PromptTokens             int         `json:"prompt_tokens"`
	CompletionTokens         int         `json:"completion_tokens"`
	InputTokens              int         `json:"input_tokens"`
	OutputTokens             int         `json:"output_tokens"`
	TotalTokens              int         `json:"total_tokens"`
	PromptTokenCount         int         `json:"promptTokenCount"`
	CandidatesTokenCount     int         `json:"candidatesTokenCount"`
	TotalTokenCount          int         `json:"totalTokenCount"`
	CacheReadInputTokens     int         `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int         `json:"cache_creation_input_tokens"`
	CachedContentTokenCount  int         `json:"cachedContentTokenCount"`
	UncachedTokens           int         `json:"x_aip_uncached_tokens"`
	GeminiUncachedTokens     int         `json:"xAipUncachedTokens"`
	GeminiCacheWriteTokens   int         `json:"xAipCacheWriteTokens"`
	PromptDetails            wireDetails `json:"prompt_tokens_details"`
	InputDetails             wireDetails `json:"input_tokens_details"`
}

// wireDetails carries the cache counters of the OpenAI-family prompt details.
type wireDetails struct {
	CachedTokens   int `json:"cached_tokens"`
	UncachedTokens int `json:"x_aip_uncached_tokens"`
	CacheWrite     int `json:"x_aip_cache_write_tokens"`
}

// One canonical usage perspective must reach every frontend with the same
// quantities, in both response modes. The OpenAI-family total is the inclusive
// prompt plus the completion, the Gemini total keeps the provider total when it
// does not fall below that floor, and Anthropic keeps its native counters.
// Every response must carry a usage object with the counters the family must
// report, and no opt-in extension counter while the extensions are disabled.
func TestUsage_semanticParity_selectedUsageAcrossFrontends(t *testing.T) {
	t.Parallel()
	for _, tc := range usageCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, mode := range []struct {
				name string
				ext  bool
			}{{"extensions off", false}, {"extensions on", true}} {
				t.Run(mode.name, func(t *testing.T) {
					t.Parallel()
					want := wantUsageOf(tc.want, mode.ext)
					for _, fam := range familyCases(mode.ext) {
						final, initial := fam.want(want)
						t.Run(fam.name+"/non-stream", func(t *testing.T) {
							usage, raw, keys, seen := usageObject(t, encodeResponse(t, fam.nonStream, tc.events).Body.Bytes())
							if !seen {
								t.Fatalf("%s response carries no usage", fam.name)
							}
							checkUsage(t, fam, mode.ext, usage, raw, keys, final)
						})
						t.Run(fam.name+"/stream", func(t *testing.T) {
							stream := streamUsage(t, encodeResponse(t, fam.stream, tc.events))
							if !stream.terminal {
								t.Fatalf("%s stream has no terminal", fam.name)
							}
							if !stream.seen {
								t.Fatalf("%s stream carries no usage", fam.name)
							}
							checkUsage(t, fam, mode.ext, stream.final, stream.raw, stream.keys, final)
							if stream.initial != initial {
								t.Fatalf("%s initial framing = %+v want %+v", fam.name, stream.initial, initial)
							}
						})
					}
				})
			}
		})
	}
}

// familyCase is one frontend family: its encoders and its expected counters.
type familyCase struct {
	name       string
	nonStream  func(t *testing.T, w http.ResponseWriter, es aipapi.EventStream) error
	stream     func(t *testing.T, w http.ResponseWriter, es aipapi.EventStream) error
	want       func(w wantUsage) (final, initial wireUsage)
	required   []string
	extensions []string
	forbidden  []string
}

// familyCases returns the four frontend families under test.
func familyCases(ext bool) []familyCase {
	return []familyCase{
		{
			name: "chat",
			nonStream: func(t *testing.T, w http.ResponseWriter, es aipapi.EventStream) error {
				t.Helper()
				return openailegacy.WriteNonStreamJSON(t.Context(), w, &aipapi.Call{}, es, openailegacy.EncodeOptions{ExposeAipUsageExtensions: ext})
			},
			stream: func(t *testing.T, w http.ResponseWriter, es aipapi.EventStream) error {
				t.Helper()
				return openailegacy.WriteStreamSSE(t.Context(), w, &aipapi.Call{}, es, openailegacy.EncodeOptions{ExposeAipUsageExtensions: ext})
			},
			want:       func(w wantUsage) (wireUsage, wireUsage) { return w.chat, wireUsage{} },
			required:   []string{"prompt_tokens", "completion_tokens", "total_tokens"},
			extensions: []string{"x_aip_uncached_tokens", "x_aip_cache_write_tokens"},
		},
		{
			name: "responses",
			nonStream: func(t *testing.T, w http.ResponseWriter, es aipapi.EventStream) error {
				t.Helper()
				return openairesponses.WriteNonStreamJSON(t.Context(), w, &aipapi.Call{}, es, openairesponses.EncodeOptions{ExposeAipUsageExtensions: ext})
			},
			stream: func(t *testing.T, w http.ResponseWriter, es aipapi.EventStream) error {
				t.Helper()
				return openairesponses.WriteStreamSSE(t.Context(), w, &aipapi.Call{}, es, openairesponses.EncodeOptions{ExposeAipUsageExtensions: ext})
			},
			want:       func(w wantUsage) (wireUsage, wireUsage) { return w.responses, wireUsage{} },
			required:   []string{"input_tokens", "output_tokens", "total_tokens"},
			extensions: []string{"x_aip_uncached_tokens", "x_aip_cache_write_tokens"},
		},
		{
			name: "gemini",
			nonStream: func(t *testing.T, w http.ResponseWriter, es aipapi.EventStream) error {
				t.Helper()
				return gemini.WriteNonStreamJSON(t.Context(), w, &aipapi.Call{}, es, gemini.EncodeOptions{ExposeAipUsageExtensions: ext})
			},
			stream: func(t *testing.T, w http.ResponseWriter, es aipapi.EventStream) error {
				t.Helper()
				return gemini.WriteStreamSSE(t.Context(), w, &aipapi.Call{}, es, gemini.EncodeOptions{ExposeAipUsageExtensions: ext})
			},
			want:       func(w wantUsage) (wireUsage, wireUsage) { return w.gemini, wireUsage{} },
			required:   []string{"promptTokenCount", "candidatesTokenCount", "totalTokenCount"},
			extensions: []string{"xAipUncachedTokens", "xAipCacheWriteTokens"},
		},
		{
			name: "anthropic",
			nonStream: func(t *testing.T, w http.ResponseWriter, es aipapi.EventStream) error {
				t.Helper()
				return anthropic.WriteNonStreamJSON(t.Context(), w, &aipapi.Call{}, es, anthropic.EncodeOptions{ExposeAipUsageExtensions: ext})
			},
			stream: func(t *testing.T, w http.ResponseWriter, es aipapi.EventStream) error {
				t.Helper()
				return anthropic.WriteStreamSSE(t.Context(), w, &aipapi.Call{}, es, anthropic.EncodeOptions{ExposeAipUsageExtensions: ext})
			},
			want:       func(w wantUsage) (wireUsage, wireUsage) { return w.anthropic, w.anthropicInitial },
			required:   []string{"input_tokens", "output_tokens"},
			extensions: []string{"x_aip_uncached_tokens"},
			forbidden:  []string{"total_tokens", "totalTokenCount"},
		},
	}
}

// encodeResponse runs a family's encoder over a canonical turn.
func encodeResponse(t *testing.T, write func(t *testing.T, w http.ResponseWriter, es aipapi.EventStream) error, events []aipapi.Event) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := write(t, rec, aipapi.NewFixedEventStream(events)); err != nil {
		t.Fatal(err)
	}
	return rec
}

// usageCase is one usage perspective and its expected counters.
type usageCase struct {
	name   string
	events []aipapi.Event
	want   wantCounters
}

// wantUsage is the expected wire usage of the four families.
type wantUsage struct {
	chat             wireUsage
	responses        wireUsage
	gemini           wireUsage
	anthropic        wireUsage
	anthropicInitial wireUsage
}

// wantCounters are the expected counters of one usage perspective.
type wantCounters struct {
	prompt, input, completion    int
	openAITotal, geminiTotal     int
	cached, uncached, cacheWrite int
}

// wantUsageOf builds the expected wire usage; the extension counters are absent while the extensions are disabled.
func wantUsageOf(c wantCounters, ext bool) wantUsage {
	chat := wireUsage{PromptTokens: c.prompt, CompletionTokens: c.completion, TotalTokens: c.openAITotal, PromptDetails: wireDetails{CachedTokens: c.cached}}
	responses := wireUsage{InputTokens: c.prompt, OutputTokens: c.completion, TotalTokens: c.openAITotal, InputDetails: wireDetails{CachedTokens: c.cached}}
	gemini := wireUsage{PromptTokenCount: c.prompt, CandidatesTokenCount: c.completion, TotalTokenCount: c.geminiTotal, CachedContentTokenCount: c.cached}
	anthropic := wireUsage{InputTokens: c.input, OutputTokens: c.completion, CacheReadInputTokens: c.cached, CacheCreationInputTokens: c.cacheWrite}
	if ext {
		chat.PromptDetails.UncachedTokens, chat.PromptDetails.CacheWrite = c.uncached, c.cacheWrite
		responses.InputDetails.UncachedTokens, responses.InputDetails.CacheWrite = c.uncached, c.cacheWrite
		gemini.GeminiUncachedTokens, gemini.GeminiCacheWriteTokens = c.uncached, c.cacheWrite
		anthropic.UncachedTokens = c.uncached
	}
	return wantUsage{chat: chat, responses: responses, gemini: gemini, anthropic: anthropic, anthropicInitial: wireUsage{InputTokens: c.input}}
}

// usageCases returns the usage perspectives the four frontends must project.
func usageCases() []usageCase {
	exclusive := aipapi.UsageAccountingMetadata{InputTokensExcludeCacheRead: true, InputTokensExcludeCacheWrite: true}
	var cases []usageCase
	// The Gemini wire total keeps the provider total when it does not fall below
	// the inclusive prompt plus the completion, and reports that floor otherwise;
	// the OpenAI-family wire total is always the inclusive prompt plus the completion.
	for _, total := range []struct {
		name        string
		provider    int
		geminiTotal int
	}{
		{"provider total in the producer's exclusive semantics", 320, 1020},
		{"provider total larger than the wire breakdown", 1500, 1500},
		{"no provider total", 0, 1020},
	} {
		cases = append(cases, usageCase{
			name:   "unscoped exclusive usage, " + total.name,
			events: usageEvents(aipapi.Event{InputTokens: 300, OutputTokens: 20, CacheReadTokens: 600, CacheWriteTokens: 100, TotalTokens: total.provider, Accounting: exclusive}),
			want:   wantCounters{prompt: 1000, input: 300, completion: 20, openAITotal: 1020, geminiTotal: total.geminiTotal, cached: 600, uncached: 300, cacheWrite: 100},
		})
	}
	cases = append(cases,
		usageCase{
			// The provider perspective of the event shell never reaches the client.
			name: "scoped client visible perspective",
			events: usageEvents(aipapi.Event{InputTokens: 900, OutputTokens: 90, Accounting: aipapi.UsageAccountingMetadata{Plane: aipapi.UsagePlaneProviderBillable}},
				scopedUsage(300, 20, 600, 100, 320, true, true)),
			want: wantCounters{prompt: 1000, input: 300, completion: 20, openAITotal: 1020, geminiTotal: 1020, cached: 600, uncached: 300, cacheWrite: 100},
		},
		usageCase{
			// Every delta contributes its own cache declarations: reads only, then writes only.
			name: "independent per-delta cache declarations",
			events: usageEvents(
				scopedUsage(400, 20, 600, 100, 0, true, false),
				scopedUsage(10, 5, 20, 30, 0, false, true)),
			want: wantCounters{prompt: 1040, input: 410, completion: 25, openAITotal: 1065, geminiTotal: 1065, cached: 620, uncached: 390, cacheWrite: 130},
		},
		usageCase{
			// A total-only event is not upgraded into an invented breakdown.
			name:   "provider total only",
			events: usageEvents(aipapi.Event{TotalTokens: 500}),
			want:   wantCounters{prompt: 0, input: 0, completion: 0, openAITotal: 0, geminiTotal: 500, cached: 0, uncached: 0, cacheWrite: 0},
		},
	)
	return cases
}

// scopedUsage builds a client-visible usage delta with the given counters.
func scopedUsage(input, output, cacheRead, cacheWrite, total int, excludeRead, excludeWrite bool) aipapi.Event {
	return aipapi.Event{UsageScopes: []aipapi.ScopedUsageDelta{{
		InputTokens: input, OutputTokens: output, CacheReadTokens: cacheRead, CacheWriteTokens: cacheWrite, TotalTokens: total,
		Accounting: aipapi.UsageAccountingMetadata{Plane: aipapi.UsagePlaneClientVisible, InputTokensExcludeCacheRead: excludeRead, InputTokensExcludeCacheWrite: excludeWrite},
	}}}
}

// usageEvents wraps usage deltas into a canonical turn; the usage arrives before the content delta, so the Anthropic framing carries the input.
func usageEvents(usage ...aipapi.Event) []aipapi.Event {
	events := []aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventMessageStarted}}
	for i := range usage {
		usage[i].Kind = aipapi.EventUsageDelta
		events = append(events, usage[i])
	}
	return append(events, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "hi"}, aipapi.Event{Kind: aipapi.EventResponseFinished})
}

// streamFrame is the usage a family's stream carried.
type streamFrame struct {
	final    wireUsage
	initial  wireUsage
	raw      []byte
	keys     map[string]json.RawMessage
	seen     bool
	terminal bool
}

// streamUsage decodes the frames of a recorded stream and returns the last usage object, the initial framing and the observed terminal.
func streamUsage(t *testing.T, rec *httptest.ResponseRecorder) streamFrame {
	t.Helper()
	var out streamFrame
	for _, fr := range testkit.ParseRecorderSSE(rec) {
		if fr.Data == "[DONE]" {
			out.terminal = true
			continue
		}
		if usage, raw, keys, ok := usageObject(t, []byte(fr.Data)); ok {
			out.final, out.raw, out.keys, out.seen = usage, raw, keys, true
		}
		var frame struct {
			Message *struct {
				Usage json.RawMessage `json:"usage"`
			} `json:"message"`
			Type       string `json:"type"`
			Candidates []struct {
				FinishReason string `json:"finishReason"`
			} `json:"candidates"`
		}
		decodeJSON(t, []byte(fr.Data), &frame)
		if frame.Message != nil && len(frame.Message.Usage) > 0 {
			decodeJSON(t, frame.Message.Usage, &out.initial)
		}
		if frame.Type == "message_stop" || (len(frame.Candidates) > 0 && frame.Candidates[0].FinishReason != "") {
			out.terminal = true
		}
	}
	return out
}

// usageObject decodes the usage object a recorded response or stream frame carried: the counters, the raw object, the keys the wire carried and whether one was observed.
func usageObject(t *testing.T, payload []byte) (wireUsage, []byte, map[string]json.RawMessage, bool) {
	t.Helper()
	var envelope struct {
		Usage         json.RawMessage `json:"usage"`
		UsageMetadata json.RawMessage `json:"usageMetadata"`
		Response      *struct {
			Usage json.RawMessage `json:"usage"`
		} `json:"response"`
	}
	decodeJSON(t, payload, &envelope)
	raw := envelope.Usage
	if len(raw) == 0 {
		raw = envelope.UsageMetadata
	}
	if len(raw) == 0 && envelope.Response != nil {
		raw = envelope.Response.Usage
	}
	if len(raw) == 0 {
		return wireUsage{}, nil, nil, false
	}
	var usage wireUsage
	decodeJSON(t, raw, &usage)
	var keys map[string]json.RawMessage
	decodeJSON(t, raw, &keys)
	return usage, raw, keys, true
}

// checkUsage asserts that a usage object carries the counters the family must report, no opt-in counter while the extensions are disabled, and the expected counters.
func checkUsage(t *testing.T, fam familyCase, ext bool, usage wireUsage, raw []byte, keys map[string]json.RawMessage, want wireUsage) {
	t.Helper()
	for _, key := range fam.required {
		if _, ok := keys[key]; !ok {
			t.Fatalf("%s usage is missing the %s counter", fam.name, key)
		}
	}
	if !ext {
		for _, key := range fam.extensions {
			if strings.Contains(string(raw), `"`+key+`"`) {
				t.Fatalf("%s usage carries the extension counter %s", fam.name, key)
			}
		}
	}
	for _, key := range fam.forbidden {
		if _, ok := keys[key]; ok {
			t.Fatalf("%s usage carries the forbidden %s counter", fam.name, key)
		}
	}
	if usage != want {
		t.Fatalf("%s usage = %+v want %+v", fam.name, usage, want)
	}
	// The Gemini total must never fall below the Gemini prompt floor: the
	// prompt plus the candidates. The other families report no Gemini prompt
	// count, so the check holds for them vacuously.
	if usage.TotalTokenCount < usage.PromptTokenCount+usage.CandidatesTokenCount {
		t.Fatalf("%s total %d is below the prompt floor %d", fam.name, usage.TotalTokenCount, usage.PromptTokenCount+usage.CandidatesTokenCount)
	}
}

func decodeJSON(t *testing.T, payload []byte, out any) {
	t.Helper()
	if err := json.Unmarshal(payload, out); err != nil {
		t.Fatalf("decode %s: %v", payload, err)
	}
}

// The inclusive-producer parity case reads the per-family usage shapes
// directly, so those shapes keep their own names beside the shared
// counters above.
type chatUsageWire struct {
	PromptTokens     int         `json:"prompt_tokens"`
	CompletionTokens int         `json:"completion_tokens"`
	TotalTokens      int         `json:"total_tokens"`
	Details          wireDetails `json:"prompt_tokens_details"`
}

type responsesUsageWire struct {
	InputTokens  int         `json:"input_tokens"`
	OutputTokens int         `json:"output_tokens"`
	TotalTokens  int         `json:"total_tokens"`
	Details      wireDetails `json:"input_tokens_details"`
}

type geminiUsageWire struct {
	PromptTokenCount        int `json:"promptTokenCount"`
	CandidatesTokenCount    int `json:"candidatesTokenCount"`
	TotalTokenCount         int `json:"totalTokenCount"`
	CachedContentTokenCount int `json:"cachedContentTokenCount"`
	UncachedTokens          int `json:"xAipUncachedTokens"`
	CacheWriteTokens        int `json:"xAipCacheWriteTokens"`
}

// chatUsage, responsesUsage and geminiUsage return the non-streaming usage of
// one family with the opt-in counters enabled, so a producer's own relation
// can be compared across the prompt-reporting frontends.
func chatUsage(t *testing.T, events []aipapi.Event) chatUsageWire {
	t.Helper()
	rec := encodeResponse(t, familyCases(true)[0].nonStream, events)
	var out struct {
		Usage chatUsageWire `json:"usage"`
	}
	decodeJSON(t, rec.Body.Bytes(), &out)
	return out.Usage
}

func responsesUsage(t *testing.T, events []aipapi.Event) responsesUsageWire {
	t.Helper()
	rec := encodeResponse(t, familyCases(true)[1].nonStream, events)
	var out struct {
		Usage responsesUsageWire `json:"usage"`
	}
	decodeJSON(t, rec.Body.Bytes(), &out)
	return out.Usage
}

func geminiUsage(t *testing.T, events []aipapi.Event) geminiUsageWire {
	t.Helper()
	rec := encodeResponse(t, familyCases(true)[2].nonStream, events)
	var out struct {
		Usage geminiUsageWire `json:"usageMetadata"`
	}
	decodeJSON(t, rec.Body.Bytes(), &out)
	return out.Usage
}
