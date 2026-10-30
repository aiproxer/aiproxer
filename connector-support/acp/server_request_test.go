package acp

import (
	"encoding/json"
	"testing"
)

func TestIsInboundServerRequest(t *testing.T) {
	t.Parallel()
	var probe map[string]any
	_ = json.Unmarshal([]byte(`{"jsonrpc":"2.0","id":3,"method":"session/request_permission","params":{}}`), &probe)
	if !isInboundServerRequest(probe) {
		t.Fatal("expected server request")
	}
	var probe2 map[string]any
	_ = json.Unmarshal([]byte(`{"jsonrpc":"2.0","id":10,"result":{"stopReason":"end_turn"}}`), &probe2)
	if isInboundServerRequest(probe2) {
		t.Fatal("terminal result is not server request")
	}
	var probe3 map[string]any
	_ = json.Unmarshal([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{}}`), &probe3)
	if isInboundServerRequest(probe3) {
		t.Fatal("session/update notification")
	}
}

func TestExtractServerRequestProbe(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		raw         string
		wantMethod  string
		wantDropped bool
		wantErr     bool
		wantID      string
		wantParams  string
	}{
		{
			name:       "request with id and params",
			raw:        `{"jsonrpc":"2.0","id":7,"method":"session/request_permission","params":{"x":1}}`,
			wantMethod: "session/request_permission",
			wantID:     "7",
			wantParams: `{"x":1}`,
		},
		{
			name:        "notification without id is dropped",
			raw:         `{"jsonrpc":"2.0","method":"session/update","params":{}}`,
			wantMethod:  "session/update",
			wantDropped: true,
		},
		{
			name:    "missing method errors",
			raw:     `{"jsonrpc":"2.0","id":7,"params":{}}`,
			wantErr: true,
		},
		{
			name:       "request without params yields nil params",
			raw:        `{"jsonrpc":"2.0","id":42,"method":"item/permissions/requestApproval"}`,
			wantMethod: "item/permissions/requestApproval",
			wantID:     "42",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var probe map[string]any
			if err := json.Unmarshal([]byte(tc.raw), &probe); err != nil {
				t.Fatal(err)
			}
			method, id, params, dropped, err := ExtractServerRequestProbe("acp", probe)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if method != tc.wantMethod {
				t.Fatalf("method = %q, want %q", method, tc.wantMethod)
			}
			if dropped != tc.wantDropped {
				t.Fatalf("dropped = %v, want %v", dropped, tc.wantDropped)
			}
			if !tc.wantDropped {
				if string(id) != tc.wantID {
					t.Fatalf("id = %q, want %q", string(id), tc.wantID)
				}
				if tc.wantParams != "" && string(params) != tc.wantParams {
					t.Fatalf("params = %q, want %q", string(params), tc.wantParams)
				}
			}
		})
	}
}
