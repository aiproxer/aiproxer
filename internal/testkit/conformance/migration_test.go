//go:build integration

package conformance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/aiproxer/aiproxer/internal/refclient/refclienttest"
)

func TestWireContractGoldensParseAndShape(t *testing.T) {
	t.Parallel()
	root := refclienttest.ModuleRoot(t)
	dir := filepath.Join(root, "testdata", "migration")

	cases := []struct {
		name string
		want func(tb testing.TB, raw []byte)
	}{
		{
			name: "openai_responses_http_streaming.json",
			want: func(tb testing.TB, raw []byte) {
				tb.Helper()
				var v struct {
					Events []struct {
						Type string `json:"type"`
					} `json:"events"`
					Done string `json:"done"`
				}
				if err := json.Unmarshal(raw, &v); err != nil {
					tb.Fatal(err)
				}
				if len(v.Events) == 0 {
					tb.Fatal("expected events")
				}
				if v.Done != "[DONE]" {
					tb.Fatalf("done marker: %q", v.Done)
				}
				var sawCompleted bool
				for _, e := range v.Events {
					if e.Type == "response.completed" {
						sawCompleted = true
					}
				}
				if !sawCompleted {
					tb.Fatal("expected response.completed event")
				}
			},
		},
		{
			name: "openai_responses_http_nonstream.json",
			want: func(tb testing.TB, raw []byte) {
				tb.Helper()
				var v struct {
					Object string `json:"object"`
					Status string `json:"status"`
					Output []any  `json:"output"`
				}
				if err := json.Unmarshal(raw, &v); err != nil {
					tb.Fatal(err)
				}
				if v.Object != "response" || v.Status != "completed" {
					tb.Fatalf("object/status: %+v", v)
				}
				if len(v.Output) == 0 {
					tb.Fatal("expected output")
				}
			},
		},
		{
			name: "anthropic_messages_nonstream.json",
			want: func(tb testing.TB, raw []byte) {
				tb.Helper()
				var v struct {
					ID      string `json:"id"`
					Type    string `json:"type"`
					Role    string `json:"role"`
					Content []any  `json:"content"`
				}
				if err := json.Unmarshal(raw, &v); err != nil {
					tb.Fatal(err)
				}
				if v.ID == "" || v.Type != "message" || v.Role != "assistant" {
					tb.Fatalf("header fields: %+v", v)
				}
				if len(v.Content) == 0 {
					tb.Fatal("expected content blocks")
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			p := filepath.Join(dir, c.name)
			raw, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			c.want(t, raw)
		})
	}
}
