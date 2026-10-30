package anthropicmessages

import (
	"bytes"
	"net/http"
)

// Request is a bounded view of one authorized messages call.
type Request struct {
	Sequence int64
	Body     []byte
	Stream   bool
}

// Response is a scripted HTTP response for one Request.
type Response struct {
	Status  int
	Headers http.Header
	JSON    string
	SSE     string
}

// Responder builds a per-request Response. It must be safe for concurrent use.
type Responder func(Request) Response

func writeResponder(w http.ResponseWriter, req Request, resp Response) {
	status := resp.Status
	if status == 0 {
		status = http.StatusOK
	}
	if resp.Headers != nil {
		for k, vals := range resp.Headers.Clone() {
			for _, v := range vals {
				w.Header().Add(k, v)
			}
		}
	}
	if req.Stream {
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(resp.SSE))
		return
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(status)
	_, _ = w.Write([]byte(resp.JSON))
}

func streamFlag(body []byte) bool {
	return bytes.Contains(body, []byte(`"stream":true`))
}
