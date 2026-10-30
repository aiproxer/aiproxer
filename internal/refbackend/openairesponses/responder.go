package openairesponses

import "net/http"

type Request struct {
	Sequence int64
	Body     []byte
	Stream   bool
}

type Response struct {
	Status  int
	Headers http.Header
	JSON    string
	SSE     string
}

type Responder func(Request) Response
