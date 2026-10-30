package acp

import (
	"context"
	"encoding/json"
	"testing"
)

func TestSessionNew_OptInMetadataAndUnchangedDefault(t *testing.T) {
	for _, meta := range []json.RawMessage{nil, json.RawMessage(`{"claudeCode":{"options":{"tools":[]}}}`)} {
		tr := &metaTransport{}
		c := newClientFromTransport(tr, nil)
		if _, err := c.sessionNew(context.Background(), HandshakeProfile{SessionNewCwd: "/work", SessionNewMeta: meta}); err != nil {
			t.Fatal(err)
		}
		var req struct {
			Params map[string]json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(tr.body, &req); err != nil {
			t.Fatal(err)
		}
		got, present := req.Params["_meta"]
		if present != (meta != nil) || string(got) != string(meta) {
			t.Fatalf("metadata: %s", got)
		}
		if len(req.Params) != 2+boolInt(present) {
			t.Fatalf("changed default: %s", tr.body)
		}
	}
}

type metaTransport struct {
	Transport
	body []byte
}

func (t *metaTransport) CallUnary(_ context.Context, b []byte, _ int) ([]byte, error) {
	t.body = b
	return []byte(`{"jsonrpc":"2.0","id":1,"result":{"sessionId":"s"}}`), nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
