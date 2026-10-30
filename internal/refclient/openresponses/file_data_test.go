package openresponses

import (
	"encoding/json"
	"testing"
)

func TestRefClient_InputFileFileDataPreserved(t *testing.T) {
	t.Parallel()
	part := ContentPart{
		Type:     "input_file",
		FileData: json.RawMessage(`"aGVsbG8="`),
		FileURL:  json.RawMessage(`"https://x/report.pdf"`),
	}
	raw, err := json.Marshal(part)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{`"file_data":"aGVsbG8="`, `"file_url":"https://x/report.pdf"`} {
		if !contains(string(raw), marker) {
			t.Fatalf("marshal missing %q: %s", marker, raw)
		}
	}
	var back ContentPart
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if string(back.FileData) != `"aGVsbG8="` || string(back.FileURL) != `"https://x/report.pdf"` {
		t.Fatalf("file_data/file_url not preserved: %+v", back)
	}
}

func TestRefClient_ItemRoundTripPreservesInputFileAndVideo(t *testing.T) {
	t.Parallel()
	item := Item{
		Type: string(ItemMessage),
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

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
