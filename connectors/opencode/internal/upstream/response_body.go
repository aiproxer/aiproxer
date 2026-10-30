package upstream

import (
	"errors"
	"fmt"
	"io"
)

const maxNonStreamResponseBytes = 8 << 20

var errNonStreamResponseTooLarge = errors.New("opencode: response too large")

func readNonStreamResponse(body io.Reader) ([]byte, error) {
	if body == nil {
		return nil, fmt.Errorf("opencode: nil response body")
	}
	limited := io.LimitReader(body, maxNonStreamResponseBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("opencode: read non-stream response: %w", err)
	}
	if int64(len(data)) > maxNonStreamResponseBytes {
		return nil, errNonStreamResponseTooLarge
	}
	return data, nil
}
