package gemini

import "strings"

// ParseGenerateContentPath extracts model id and streaming mode from a Gemini REST path
// (e.g. /v1beta/models/gemini-2.0-flash:generateContent or …:streamGenerateContent?alt=sse).
func ParseGenerateContentPath(urlPath string) (model string, stream bool, ok bool) {
	path := strings.TrimPrefix(urlPath, "/")
	idx := strings.LastIndex(path, "/models/")
	if idx < 0 {
		return "", false, false
	}
	sub := path[idx+len("/models/"):]
	if j := strings.Index(sub, ":streamGenerateContent"); j > 0 {
		return sub[:j], true, true
	}
	if j := strings.Index(sub, ":generateContent"); j > 0 {
		return sub[:j], false, true
	}
	return "", false, false
}
