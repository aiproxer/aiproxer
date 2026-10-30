package service

import "github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"

func capsFromOllama(caps []string) backendplugin.CapabilitySummary {
	out := backendplugin.CapabilitySummary{Streaming: true}
	for _, cap := range caps {
		switch cap {
		case "tools":
			out.Tools = true
		case "thinking":
			out.Reasoning = true
		case "vision":
			// The OpenAI-compatible request mapper rejects canonical image and
			// file parts, so Ollama's provider hint cannot be advertised as a
			// route capability until that mapping is lossless.
		}
	}
	return out
}
