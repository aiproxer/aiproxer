package aipsdk_test

import (
	"net/http"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func TestHTTPHeaders_APIKeyFrom_bearerAndVendorAliases(t *testing.T) {
	t.Parallel()
	h := aipsdk.DefaultHTTPHeaders()

	t.Run("bearer", func(t *testing.T) {
		t.Parallel()
		hdr := http.Header{}
		hdr.Set("Authorization", "Bearer sk-test")
		if got := h.APIKeyFrom(hdr); got != "sk-test" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("x-api-key", func(t *testing.T) {
		t.Parallel()
		hdr := http.Header{}
		hdr.Set("x-api-key", "ant-key")
		if got := h.APIKeyFrom(hdr); got != "ant-key" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("x-goog-api-key", func(t *testing.T) {
		t.Parallel()
		hdr := http.Header{}
		hdr.Set("x-goog-api-key", "goog-key")
		if got := h.APIKeyFrom(hdr); got != "goog-key" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("api-key", func(t *testing.T) {
		t.Parallel()
		hdr := http.Header{}
		hdr.Set("api-key", "azure-key")
		if got := h.APIKeyFrom(hdr); got != "azure-key" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("authorization_wins_over_alias", func(t *testing.T) {
		t.Parallel()
		hdr := http.Header{}
		hdr.Set("Authorization", "Bearer first")
		hdr.Set("x-api-key", "second")
		if got := h.APIKeyFrom(hdr); got != "first" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("basic_falls_through_to_x-api-key", func(t *testing.T) {
		t.Parallel()
		hdr := http.Header{}
		hdr.Set("Authorization", "Basic dGVzdA==")
		hdr.Set("x-api-key", "ant-key")
		if got := h.APIKeyFrom(hdr); got != "ant-key" {
			t.Fatalf("got %q", got)
		}
	})
}

func TestHTTPHeaders_RouteSelector_firstNonEmpty(t *testing.T) {
	t.Parallel()
	h := aipsdk.HTTPHeaders{Route: []string{aipsdk.HeaderRoute, "X-Custom-Route"}}
	hdr := http.Header{}
	hdr.Set("X-Custom-Route", "alias:model")
	if got := h.RouteSelector(hdr); got != "alias:model" {
		t.Fatalf("got %q", got)
	}
	hdr.Set(aipsdk.HeaderRoute, "default:model")
	if got := h.RouteSelector(hdr); got != "default:model" {
		t.Fatalf("default must win when both present, got %q", got)
	}
}

func TestBearerCredential(t *testing.T) {
	t.Parallel()
	if got := aipsdk.BearerCredential("Bearer tok"); got != "tok" {
		t.Fatalf("got %q", got)
	}
	if got := aipsdk.BearerCredential("Basic x"); got != "" {
		t.Fatalf("basic: got %q", got)
	}
	if got := aipsdk.BearerCredential("Bearer   "); got != "" {
		t.Fatalf("empty bearer: got %q", got)
	}
}

func TestHTTPHeaders_WorkspaceValue_defaultAndAliases(t *testing.T) {
	t.Parallel()
	d := aipsdk.DefaultHTTPHeaders()
	if len(d.Workspace) == 0 || d.Workspace[0] != aipsdk.HeaderWorkspace {
		t.Fatalf("default workspace headers = %v, want [%q]", d.Workspace, aipsdk.HeaderWorkspace)
	}
	h := aipsdk.HTTPHeaders{Workspace: []string{aipsdk.HeaderWorkspace, "X-Custom-WS"}}
	hdr := http.Header{}
	hdr.Set("X-Custom-WS", "/tmp/aip-alias-path")
	if got := h.WorkspaceValue(hdr); got != "/tmp/aip-alias-path" {
		t.Fatalf("alias value = %q", got)
	}
	hdr.Set(aipsdk.HeaderWorkspace, "/tmp/aip-default-path")
	if got := h.WorkspaceValue(hdr); got != "/tmp/aip-default-path" {
		t.Fatalf("default must win when both present, got %q", got)
	}
	if got := h.WorkspaceValue(http.Header{}); got != "" {
		t.Fatalf("absent must be empty, got %q", got)
	}
}
