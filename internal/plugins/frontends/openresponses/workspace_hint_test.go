package openresponses_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func workspaceCreateOpts(hdr http.Header) openresponses.DecodeCreateOptions {
	return openresponses.DecodeCreateOptions{
		Auth:          &mockAuthorizer{authenticated: true},
		Headers:       hdr,
		Method:        http.MethodPost,
		Path:          "/openresponses/v1/responses",
		RouteSelector: "stub:gpt-4o",
	}
}

func workspaceCompactOpts(hdr http.Header) openresponses.DecodeCompactOptions {
	return openresponses.DecodeCompactOptions{
		Auth:          &mockAuthorizer{authenticated: true},
		Headers:       hdr,
		Method:        http.MethodPost,
		Path:          "/openresponses/v1/compact",
		RouteSelector: "stub:gpt-4o",
	}
}

func mustWorkspaceExt(t *testing.T, ext map[string]json.RawMessage) string {
	t.Helper()
	var got string
	if err := json.Unmarshal(ext["workspace_path"], &got); err != nil {
		t.Fatalf("workspace_path: %v", err)
	}
	return got
}

func TestDecodeCreate_WorkspaceHint(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"gpt-4o","input":"hello"}`)
	hdr := http.Header{}
	hdr.Set(aipsdk.HeaderWorkspace, "/tmp/aip-workspace-hint-proj-a")
	decoded, err := openresponses.AuthenticateAndDecodeCreate(context.Background(), body, workspaceCreateOpts(hdr))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := mustWorkspaceExt(t, decoded.Call.Extensions); got != "/tmp/aip-workspace-hint-proj-a" {
		t.Fatalf("workspace_path = %q", got)
	}

	decoded, err = openresponses.AuthenticateAndDecodeCreate(context.Background(), body, workspaceCreateOpts(nil))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := decoded.Call.Extensions["workspace_path"]; ok {
		t.Fatal("absent header must not set workspace_path")
	}

	bad := http.Header{}
	bad.Set(aipsdk.HeaderWorkspace, "relative/path")
	if _, err := openresponses.AuthenticateAndDecodeCreate(context.Background(), body, workspaceCreateOpts(bad)); err == nil {
		t.Fatal("relative workspace hint must fail decode")
	}
}

func TestDecodeCompact_WorkspaceHint(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"gpt-4o","input":"hello"}`)
	hdr := http.Header{}
	hdr.Set(aipsdk.HeaderWorkspace, "/tmp/aip-workspace-hint-proj-a")
	decoded, err := openresponses.AuthenticateAndDecodeCompact(context.Background(), body, workspaceCompactOpts(hdr))
	if err != nil {
		t.Fatalf("compact decode: %v", err)
	}
	if got := mustWorkspaceExt(t, decoded.Call.Extensions); got != "/tmp/aip-workspace-hint-proj-a" {
		t.Fatalf("compact workspace_path = %q", got)
	}

	bad := http.Header{}
	bad.Set(aipsdk.HeaderWorkspace, "relative/path")
	if _, err := openresponses.AuthenticateAndDecodeCompact(context.Background(), body, workspaceCompactOpts(bad)); err == nil {
		t.Fatal("relative workspace hint must fail compact decode")
	}
}
