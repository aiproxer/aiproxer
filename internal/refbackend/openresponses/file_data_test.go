package openresponses

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRefBackend_InputFileFileDataPreserved(t *testing.T) {
	t.Parallel()
	part := ContentPart{
		Type:     "input_file",
		Filename: "minimal.pdf",
		FileData: json.RawMessage(`"aGVsbG8="`),
		FileURL:  json.RawMessage(`"https://x/report.pdf"`),
	}
	raw, err := json.Marshal(part)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{`"file_data":"aGVsbG8="`, `"file_url":"https://x/report.pdf"`, `"filename":"minimal.pdf"`} {
		if !strings.Contains(string(raw), marker) {
			t.Fatalf("marshal missing %q: %s", marker, raw)
		}
	}
	var back ContentPart
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if string(back.FileData) != `"aGVsbG8="` || back.Filename != "minimal.pdf" || string(back.FileURL) != `"https://x/report.pdf"` {
		t.Fatalf("file fields not preserved: %+v", back)
	}
}

func TestRefBackend_InputFileAndVideoRoundTripThroughItem(t *testing.T) {
	t.Parallel()
	item := Item{
		Type: ItemMessage,
		Role: "user",
		Content: []ContentPart{
			{Type: "input_file", FileData: json.RawMessage(`"aGVsbG8="`), Filename: "minimal.pdf"},
			{Type: "input_video", VideoURL: json.RawMessage(`"https://x/v.mp4"`)},
			{Type: "acme:part", Opaque: json.RawMessage(`{"type":"acme:part","payload":{"k":1}}`)},
		},
	}
	raw, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	var back Item
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if len(back.Content) != 3 {
		t.Fatalf("content = %d, want 3", len(back.Content))
	}
	if string(back.Content[0].FileData) != `"aGVsbG8="` || back.Content[0].Filename != "minimal.pdf" {
		t.Fatalf("file_data/filename lost: %+v", back.Content[0])
	}
	if string(back.Content[1].VideoURL) != `"https://x/v.mp4"` {
		t.Fatalf("video_url lost: %+v", back.Content[1])
	}
	if string(back.Content[2].Opaque) != `{"type":"acme:part","payload":{"k":1}}` {
		t.Fatalf("opaque prefixed part lost: %+v", back.Content[2])
	}
}
