package geminigenerate

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/stream"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"google.golang.org/genai"
)

func TestHandleResponse_thoughtPart_emitsReasoningDelta(t *testing.T) {
	t.Parallel()
	s := &genaiStream{}
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			Content: &genai.Content{
				Parts: []*genai.Part{{
					Text:    "step-1",
					Thought: true,
				}},
			},
		}},
	}
	if err := s.handleResponse(resp); err != nil {
		t.Fatal(err)
	}
	var deltas []string
	for _, ev := range stream.DrainPending(&s.pending) {
		if ev.Kind == aipapi.EventReasoningDelta {
			deltas = append(deltas, ev.Delta)
		}
	}
	if len(deltas) != 1 || deltas[0] != "step-1" {
		t.Fatalf("reasoning: %v", deltas)
	}
}

func TestHandleResponse_fileDataURI_emitsAssistantImageRef(t *testing.T) {
	t.Parallel()
	s := &genaiStream{}
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			Content: &genai.Content{
				Parts: []*genai.Part{{
					FileData: &genai.FileData{
						FileURI:  "https://storage.example.com/model.png",
						MIMEType: "image/png",
					},
				}},
			},
		}},
	}
	if err := s.handleResponse(resp); err != nil {
		t.Fatal(err)
	}
	var refs []aipapi.Event
	for _, ev := range stream.DrainPending(&s.pending) {
		if ev.Kind == aipapi.EventAssistantImageRef {
			refs = append(refs, ev)
		}
	}
	if len(refs) != 1 || refs[0].AssistantRef != "https://storage.example.com/model.png" {
		t.Fatalf("events: %+v", refs)
	}
}

func TestHandleResponse_fileDataURI_nonImage_emitsAssistantFileRef(t *testing.T) {
	t.Parallel()
	s := &genaiStream{}
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			Content: &genai.Content{
				Parts: []*genai.Part{{
					FileData: &genai.FileData{
						FileURI:  "gs://bucket/reports/out.pdf",
						MIMEType: "application/pdf",
					},
				}},
			},
		}},
	}
	if err := s.handleResponse(resp); err != nil {
		t.Fatal(err)
	}
	var refs []aipapi.Event
	for _, ev := range stream.DrainPending(&s.pending) {
		if ev.Kind == aipapi.EventAssistantFileRef {
			refs = append(refs, ev)
		}
	}
	if len(refs) != 1 || refs[0].AssistantRef != "gs://bucket/reports/out.pdf" {
		t.Fatalf("events: %+v", refs)
	}
}

func TestHandleResponse_functionCall_emitsToolEvents(t *testing.T) {
	t.Parallel()
	s := &genaiStream{}
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			Content: &genai.Content{
				Parts: []*genai.Part{{
					FunctionCall: &genai.FunctionCall{
						ID:   "call-g1",
						Name: "get_weather",
						Args: map[string]any{"city": "NYC"},
					},
				}},
			},
		}},
	}
	if err := s.handleResponse(resp); err != nil {
		t.Fatal(err)
	}
	evs := stream.DrainPending(&s.pending)
	var kinds []aipapi.EventKind
	var args strings.Builder
	for _, ev := range evs {
		kinds = append(kinds, ev.Kind)
		if ev.Kind == aipapi.EventToolCallArgsDelta {
			args.WriteString(ev.Delta)
		}
	}
	if got := args.String(); got != `{"city":"NYC"}` {
		t.Fatalf("args delta: %q", got)
	}
	var sawStart, sawFinish bool
	for _, ev := range evs {
		if ev.Kind == aipapi.EventToolCallStarted && ev.ToolCallID == "call-g1" && ev.ToolName == "get_weather" {
			sawStart = true
		}
		if ev.Kind == aipapi.EventToolCallFinished && ev.ToolCallID == "call-g1" {
			sawFinish = true
		}
	}
	if !sawStart || !sawFinish {
		t.Fatalf("tool lifecycle: start=%v finish=%v events=%v", sawStart, sawFinish, kinds)
	}
}

func TestHandleResponse_functionCall_marshalArgsError_wrapsAndPreservesCause(t *testing.T) {
	t.Parallel()
	s := &genaiStream{}
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			Content: &genai.Content{
				Parts: []*genai.Part{{
					FunctionCall: &genai.FunctionCall{
						ID:   "call-g1",
						Name: "f",
						Args: map[string]any{"bad": make(chan int)},
					},
				}},
			},
		}},
	}
	err := s.handleResponse(resp)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "gemini: marshal tool arguments") {
		t.Fatalf("got %q", err.Error())
	}
	if _, ok := errors.AsType[*json.UnsupportedTypeError](err); !ok {
		t.Fatalf("expected json.UnsupportedTypeError in chain, got %T", err)
	}
}

func TestUsageEvent_usageDetails(t *testing.T) {
	t.Parallel()
	ev := usageEvent(&genai.GenerateContentResponse{
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:        11,
			CandidatesTokenCount:    5,
			TotalTokenCount:         19,
			CachedContentTokenCount: 3,
			ThoughtsTokenCount:      3,
		},
	})
	if ev == nil {
		t.Fatal("usage event is nil")
		return
	}
	if ev.InputTokens != 11 || ev.OutputTokens != 8 {
		t.Fatalf("usage tokens: in=%d out=%d", ev.InputTokens, ev.OutputTokens)
	}
	assertUsageIntField(t, *ev, "CacheReadTokens", 3)
	assertUsageIntField(t, *ev, "ReasoningTokens", 3)
	assertUsageIntField(t, *ev, "TotalTokens", 19)
	assertUsageRawJSONContains(t, *ev, "totalTokenCount")
}

func TestUsageEvent_totalTokenFallbackPreservesInput(t *testing.T) {
	t.Parallel()
	ev := usageEvent(&genai.GenerateContentResponse{
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount: 11,
			TotalTokenCount:  19,
		},
	})
	if ev == nil {
		t.Fatal("usage event is nil")
		return
	}
	if ev.InputTokens != 11 || ev.OutputTokens != 8 {
		t.Fatalf("usage tokens: in=%d out=%d", ev.InputTokens, ev.OutputTokens)
	}
	assertUsageIntField(t, *ev, "TotalTokens", 19)
}

func TestGenaiStream_Recv_wrapsIteratorErr(t *testing.T) {
	t.Parallel()
	root := errors.New("root")
	seq := func(yield func(*genai.GenerateContentResponse, error) bool) {
		yield(nil, root)
	}
	es := newGenaiStream(seq, "gemini", 0)
	_, err := es.Recv(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "gemini: recv stream") {
		t.Fatalf("got %q", err.Error())
	}
	if !errors.Is(err, root) {
		t.Fatalf("underlying: %v", err)
	}
}

func TestGenaiStream_Recv_afterClose_returnsEOF(t *testing.T) {
	t.Parallel()
	seq := func(yield func(*genai.GenerateContentResponse, error) bool) {
		_ = yield // empty iterator (no responses)
	}
	es := newGenaiStream(seq, "gemini", 0)
	if err := es.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	_, err := es.Recv(context.Background())
	if !errors.Is(err, io.EOF) {
		t.Fatalf("after close: want EOF, got %v", err)
	}
}

func TestGenaiStream_Close_idempotent_race(t *testing.T) {
	t.Parallel()
	seq := func(yield func(*genai.GenerateContentResponse, error) bool) {
		_ = yield(&genai.GenerateContentResponse{}, nil)
	}
	es := newGenaiStream(seq, "gemini", 0)
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			_ = es.Close()
		})
	}
	wg.Wait()
}

func assertUsageIntField(t *testing.T, ev aipapi.Event, name string, want int64) {
	t.Helper()
	field := reflect.ValueOf(ev).FieldByName(name)
	if !field.IsValid() {
		return
	}
	if got := field.Int(); got != want {
		t.Fatalf("%s: got %d, want %d", name, got, want)
	}
}

func assertUsageRawJSONContains(t *testing.T, ev aipapi.Event, needle string) {
	t.Helper()
	field := reflect.ValueOf(ev).FieldByName("RawUsageJSON")
	if !field.IsValid() {
		return
	}
	switch field.Kind() {
	case reflect.String:
		if !strings.Contains(field.String(), needle) {
			t.Fatalf("RawUsageJSON: %q does not contain %q", field.String(), needle)
		}
	case reflect.Slice:
		if !strings.Contains(string(field.Bytes()), needle) {
			t.Fatalf("RawUsageJSON: %q does not contain %q", string(field.Bytes()), needle)
		}
	}
}
