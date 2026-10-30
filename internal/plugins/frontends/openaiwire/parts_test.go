package openaiwire

import (
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestMarshalBlock_roundTrip(t *testing.T) {
	t.Parallel()
	blk := map[string]json.RawMessage{
		"type": json.RawMessage(`"text"`),
		"text": json.RawMessage(`"hi"`),
	}
	var s struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	raw, err := MarshalBlock(blk)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	if s.Type != "text" || s.Text != "hi" {
		t.Fatalf("%+v", s)
	}
}

func TestImagePartFromURL_dataURL(t *testing.T) {
	t.Parallel()
	p, err := ImagePartFromURL("data:image/png;base64,abcd")
	if err != nil {
		t.Fatal(err)
	}
	if p.ImageMIME != "image/png" {
		t.Fatalf("mime %q", p.ImageMIME)
	}
}

func TestFilePartFromBase64_pdfFilename(t *testing.T) {
	t.Parallel()
	p := FilePartFromBase64("doc.PDF", "qqq")
	if p.FileMIME != "application/pdf" {
		t.Fatalf("mime %q", p.FileMIME)
	}
	if p.Kind != aipapi.PartFileRef {
		t.Fatalf("kind %v", p.Kind)
	}
}

func TestFilePartFromBase64_nonPdfFilename(t *testing.T) {
	t.Parallel()
	p := FilePartFromBase64("doc.txt", "qqq")
	if p.FileMIME != "application/octet-stream" {
		t.Fatalf("mime %q", p.FileMIME)
	}
	if p.Kind != aipapi.PartFileRef {
		t.Fatalf("kind %v", p.Kind)
	}
}

func TestImagePartFromURL_standardURL(t *testing.T) {
	t.Parallel()
	urlStr := "https://example.com/image.png"
	p, err := ImagePartFromURL(urlStr)
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != aipapi.PartImageRef {
		t.Fatalf("kind %v", p.Kind)
	}
	if p.ImageRef != urlStr {
		t.Fatalf("ref %q", p.ImageRef)
	}
	if p.ImageMIME != "" {
		t.Fatalf("mime %q", p.ImageMIME)
	}
}
