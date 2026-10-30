package testkit

import (
	"net/http/httptest"
	"strings"
)

type SSEFrame struct {
	Event string
	Data  string
}

func ParseRecorderSSE(rec *httptest.ResponseRecorder) []SSEFrame {
	return ParseSSEBody(rec.Body.String())
}

func ParseSSEBody(body string) []SSEFrame {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	var frames []SSEFrame
	for block := range strings.SplitSeq(body, "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		var ev string
		var dataB strings.Builder
		for line := range strings.SplitSeq(block, "\n") {
			line = strings.TrimRight(line, "\r")
			switch {
			case strings.HasPrefix(line, "event:"):
				ev = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				d := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				if dataB.Len() > 0 {
					dataB.WriteByte('\n')
				}
				dataB.WriteString(d)
			}
		}
		frames = append(frames, SSEFrame{Event: ev, Data: dataB.String()})
	}
	return frames
}
