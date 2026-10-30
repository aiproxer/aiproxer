package openaiwire

import (
	"encoding/json"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func MarshalBlock(blk map[string]json.RawMessage) ([]byte, error) {
	return json.Marshal(blk)
}

// ImagePartFromURL maps a URL or data-URL string to a canonical image part.
func ImagePartFromURL(imageURL string) (aipapi.Part, error) {
	p := aipapi.Part{Kind: aipapi.PartImageRef, ImageRef: imageURL}
	if rest, ok := strings.CutPrefix(imageURL, "data:"); ok {
		if mimePart, _, found := strings.Cut(rest, ";"); found && mimePart != "" {
			p.ImageMIME = mimePart
		}
	}
	return p, nil
}

// FilePartFromBase64 builds a data-URL file part from base64 payload and optional filename.
func FilePartFromBase64(filename, fileData string) aipapi.Part {
	mime := "application/octet-stream"
	low := strings.ToLower(strings.TrimSpace(filename))
	if strings.HasSuffix(low, ".pdf") {
		mime = "application/pdf"
	}
	ref := "data:" + mime + ";base64," + fileData
	return aipapi.FilePart(ref, mime, filename)
}
