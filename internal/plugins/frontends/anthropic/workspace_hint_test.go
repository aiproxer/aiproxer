package anthropic_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/anthropic"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func TestDecodeMessageRequest_WorkspaceHint(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"m","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`)
	hdr := http.Header{}
	hdr.Set(aipsdk.HeaderWorkspace, "/tmp/aip-workspace-hint-proj-a")
	d, err := anthropic.DecodeMessageRequest(body, anthropic.DecodeOptions{RouteSelector: "stub:m", Headers: hdr})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	var got string
	if err := json.Unmarshal(d.Call.Extensions["workspace_path"], &got); err != nil || got != "/tmp/aip-workspace-hint-proj-a" {
		t.Fatalf("workspace_path = %q, err %v", got, err)
	}
	d, err = anthropic.DecodeMessageRequest(body, anthropic.DecodeOptions{RouteSelector: "stub:m"})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := d.Call.Extensions["workspace_path"]; ok {
		t.Fatal("absent header must not set workspace_path")
	}
	bad := http.Header{}
	bad.Set(aipsdk.HeaderWorkspace, "relative/path")
	if _, err := anthropic.DecodeMessageRequest(body, anthropic.DecodeOptions{RouteSelector: "stub:m", Headers: bad}); err == nil {
		t.Fatal("relative workspace hint must fail decode")
	}
}
