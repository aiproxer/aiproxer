package openairesponses

import (
	"fmt"

	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/openai/openai-go/v3/responses"
)

type StreamStats struct {
	ReasoningTextDone int
	Completed         bool
}

func ReadCompletedResponse(stream *ssestream.Stream[responses.ResponseStreamEventUnion]) (*responses.Response, StreamStats, error) {
	var stats StreamStats
	if stream == nil {
		return nil, stats, fmt.Errorf("openairesponses stream: nil")
	}
	var completed *responses.Response
	for stream.Next() {
		ev := stream.Current()
		switch ev.Type {
		case "response.reasoning_text.done":
			stats.ReasoningTextDone++
		case "response.completed":
			cp := ev.Response
			completed = &cp
			stats.Completed = true
		}
	}
	if err := stream.Err(); err != nil {
		return completed, stats, err
	}
	if completed == nil {
		return nil, stats, fmt.Errorf("openairesponses stream: missing response.completed")
	}
	return completed, stats, nil
}
