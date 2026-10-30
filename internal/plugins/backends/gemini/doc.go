// Package gemini implements the Google Gemini generateContent backend connector using
// google.golang.org/genai. It maps aipapi.Call to streamGenerateContent (?alt=sse) and
// emits canonical events from streamed [genai.GenerateContentResponse] chunks.
//
// BaseURL must be the API origin only (for example https://generativelanguage.googleapis.com),
// without a model path suffix; the SDK constructs /v1beta/models/{model}:streamGenerateContent.
//
// Model resolution uses the route candidate when present, otherwise the JSON string
// stored under the gemini.model extension key (same key as the Gemini frontend decoder).
package gemini

// ID is the reserved plugin identifier for the Gemini backend.
const ID = "gemini"
