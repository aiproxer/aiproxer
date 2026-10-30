package aipapi

import "strings"

// StripDataURLBase64 parses a "data:<mime>;base64,<payload>" URL and returns
// the mime type and base64 body. ok is false when the input is not a base64
// data URL.
func StripDataURLBase64(dataURL string) (mime, b64 string, ok bool) {
	rest, ok := strings.CutPrefix(dataURL, "data:")
	if !ok {
		return "", "", false
	}
	mime, enc, found := strings.Cut(rest, ";")
	if !found {
		return "", "", false
	}
	encBody, ok := strings.CutPrefix(enc, "base64,")
	if !ok {
		return "", "", false
	}
	return mime, encBody, true
}
