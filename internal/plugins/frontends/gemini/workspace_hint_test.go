package gemini_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/gemini"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func TestDecodeGenerateContentRequest_WorkspaceHint(t *testing.T) {
	t.Parallel()
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	hdr := http.Header{}
	hdr.Set(aipsdk.HeaderWorkspace, "/tmp/aip-workspace-hint-proj-a")
	d, err := gemini.DecodeGenerateContentRequest(body, gemini.DecodeOptions{RouteSelector: "stub:m", Model: "m", Headers: hdr})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	var got string
	if err := json.Unmarshal(d.Call.Extensions["workspace_path"], &got); err != nil || got != "/tmp/aip-workspace-hint-proj-a" {
		t.Fatalf("workspace_path = %q, err %v", got, err)
	}
	d, err = gemini.DecodeGenerateContentRequest(body, gemini.DecodeOptions{RouteSelector: "stub:m", Model: "m"})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := d.Call.Extensions["workspace_path"]; ok {
		t.Fatal("absent header must not set workspace_path")
	}
	bad := http.Header{}
	bad.Set(aipsdk.HeaderWorkspace, "relative/path")
	if _, err := gemini.DecodeGenerateContentRequest(body, gemini.DecodeOptions{RouteSelector: "stub:m", Model: "m", Headers: bad}); err == nil {
		t.Fatal("relative workspace hint must fail decode")
	}
}
