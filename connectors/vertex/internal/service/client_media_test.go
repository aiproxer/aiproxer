package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestDecodeNonStream_ProjectsOutputFileDataReferences(t *testing.T) {
	t.Parallel()
	raw := []byte(`{
		"candidates": [{"content": {"role": "model", "parts": [
			{"text": "generated files:"},
			{"fileData": {"fileUri": "gs://bucket/generated.png", "mimeType": "image/png"}},
			{"fileData": {"fileUri": "gs://bucket/report.pdf", "mimeType": "application/pdf"}},
			{"inlineData": {"mimeType": "audio/wav", "data": "c2Vuc2l0aXZl"}}
		]}
	}]}`)

	events, err := decodeNonStream(raw)
	if err != nil {
		t.Fatal(err)
	}

	var refs []aipapi.Event
	for _, event := range events {
		if event.Kind == aipapi.EventAssistantImageRef || event.Kind == aipapi.EventAssistantFileRef {
			refs = append(refs, event)
		}
	}
	if len(refs) != 2 {
		t.Fatalf("output file references = %+v, want image and file references", refs)
	}
	if refs[0].Kind != aipapi.EventAssistantImageRef || refs[0].AssistantRef != "gs://bucket/generated.png" || refs[0].AssistantMIME != "image/png" {
		t.Fatalf("image reference = %+v", refs[0])
	}
	if refs[1].Kind != aipapi.EventAssistantFileRef || refs[1].AssistantRef != "gs://bucket/report.pdf" || refs[1].AssistantMIME != "application/pdf" {
		t.Fatalf("file reference = %+v", refs[1])
	}
	for _, event := range events {
		if event.AssistantRef == "c2Vuc2l0aXZl" {
			t.Fatalf("inline output payload leaked into canonical reference event: %+v", event)
		}
	}
}

func TestDecodeSSEData_ProjectsOutputFileDataReferences(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"fileData":{"fileUri":"https://storage.example/out.mp4","mimeType":"video/mp4"}}]}}]}`)
	started, messageStarted := false, false
	events, err := decodeSSEData(raw, &started, &messageStarted)
	if err != nil {
		t.Fatal(err)
	}
	if !started || !messageStarted {
		t.Fatalf("SSE response/message start state = %v/%v", started, messageStarted)
	}
	if len(events) != 3 {
		t.Fatalf("SSE events = %+v, want response start, message start, file reference", events)
	}
	if got := events[2]; got.Kind != aipapi.EventAssistantFileRef || got.AssistantRef != "https://storage.example/out.mp4" || got.AssistantMIME != "video/mp4" {
		t.Fatalf("SSE output reference = %+v", got)
	}
}

func TestClientOpen_PreservesInputMediaReferenceMIME(t *testing.T) {
	t.Parallel()

	var got GenerateContentRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			http.Error(w, fmt.Sprintf("decode request: %v", err), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}]}`))
	}))
	t.Cleanup(srv.Close)

	cfg, err := ParseConfigYAML(fmt.Appendf(nil, "project: test-project\nlocation: us-central1\napi_origin: %s\n", srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{
		Config:        cfg,
		TokenProvider: StaticTokenProvider("test-token"),
		HTTPClient:    srv.Client(),
	}
	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{
				{Kind: aipapi.PartImageRef, ImageRef: "gs://bucket/input.png", ImageMIME: "image/png"},
				{Kind: aipapi.PartFileRef, FileRef: "gs://bucket/input.wav", FileMIME: "audio/wav"},
				{Kind: aipapi.PartFileRef, FileRef: "gs://bucket/input.mp4", FileMIME: "video/mp4"},
				{Kind: aipapi.PartFileRef, FileRef: "gs://bucket/input.pdf", FileMIME: "application/pdf"},
			},
		}},
		Invocation: aipapi.Invocation{
			DeliveryMode:  aipapi.DeliveryModeNonStreaming,
			TransportMode: aipapi.TransportModeNonStreaming,
		},
	}
	stream, err := client.Open(context.Background(), call, "gemini-2.5-flash")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	for {
		_, err := stream.Recv(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}

	if len(got.Contents) != 1 || len(got.Contents[0].Parts) != 4 {
		t.Fatalf("Vertex input contents = %+v", got.Contents)
	}
	want := []struct {
		uri  string
		mime string
	}{
		{"gs://bucket/input.png", "image/png"},
		{"gs://bucket/input.wav", "audio/wav"},
		{"gs://bucket/input.mp4", "video/mp4"},
		{"gs://bucket/input.pdf", "application/pdf"},
	}
	for i, part := range got.Contents[0].Parts {
		if part.FileData == nil || part.FileData.FileURI != want[i].uri || part.FileData.MIMEType != want[i].mime {
			t.Fatalf("Vertex input part %d = %+v, want fileData %q/%q", i, part, want[i].uri, want[i].mime)
		}
		if part.InlineData != nil {
			t.Fatalf("input part %d unexpectedly changed to inlineData: %+v", i, part)
		}
	}
}
