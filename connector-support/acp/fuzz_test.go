package acp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func FuzzParseNDJSONLine(f *testing.F) {
	f.Add([]byte(`{"method":"session/update","params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"textDelta":"x"}}}}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		raw = capBytes(raw, 1<<20)
		line := string(raw)
		_, _ = parseNDJSONLine(context.Background(), SessionUpdateMapperOptions{}, line, 1)
	})
}

func FuzzMapSessionUpdateToEvents(f *testing.F) {
	f.Add([]byte(`{"sessionUpdate":"agent_message_chunk","content":{"textDelta":"hi"}}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		raw = capBytes(raw, 256<<10)
		var upd map[string]any
		if err := json.Unmarshal(raw, &upd); err != nil {
			return
		}
		_, _ = mapSessionUpdateToEvents(context.Background(), SessionUpdateMapperOptions{}, upd)
	})
}

func FuzzMergeHandshakeProfileExtensions(f *testing.F) {
	f.Add([]byte(`{"acp.session_id":"\"sid\""}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		raw = capBytes(raw, 8<<10)
		var ext map[string]json.RawMessage
		if err := json.Unmarshal(raw, &ext); err != nil {
			return
		}
		call := &aipapi.Call{Extensions: ext}
		_ = mergeHandshakeProfile(Config{}, call)
		_ = sessionIDFromExtensions(call)
	})
}
