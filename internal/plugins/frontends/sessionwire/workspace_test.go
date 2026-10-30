package sessionwire_test

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/sessionwire"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func workspaceCall() *aipapi.Call {
	return &aipapi.Call{
		Route:      aipapi.RouteIntent{Selector: "stub:m"},
		Extensions: map[string]json.RawMessage{"keep": json.RawMessage(`"v"`)},
	}
}

func workspaceHeader(v string) http.Header {
	h := http.Header{}
	h.Set(aipsdk.HeaderWorkspace, v)
	return h
}

func workspaceExt(t *testing.T, c *aipapi.Call) (string, bool) {
	t.Helper()
	raw, ok := c.Extensions[sessionwire.WorkspaceExtensionKey]
	if !ok {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("workspace extension is not a JSON string: %s", string(raw))
	}
	return s, true
}

func TestApplyWorkspaceHint_AbsentAndBlankAreInert(t *testing.T) {
	t.Parallel()
	for name, hdr := range map[string]http.Header{
		"nil":   nil,
		"blank": workspaceHeader("   "),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			call := workspaceCall()
			before := call.Extensions["keep"]
			if err := sessionwire.ApplyWorkspaceHint(call, hdr, aipsdk.HTTPHeaders{}); err != nil {
				t.Fatalf("inert input must not error: %v", err)
			}
			if _, ok := call.Extensions[sessionwire.WorkspaceExtensionKey]; ok {
				t.Fatal("inert input must not add a workspace extension")
			}
			if string(call.Extensions["keep"]) != string(before) {
				t.Fatal("inert input must not mutate other extensions")
			}
		})
	}
	// Nil call must not panic.
	if err := sessionwire.ApplyWorkspaceHint(nil, workspaceHeader("/x"), aipsdk.HTTPHeaders{}); err != nil {
		t.Fatalf("nil call must not error: %v", err)
	}
}

func TestApplyWorkspaceHint_ProjectsValidSyntax(t *testing.T) {
	t.Parallel()
	aliases := aipsdk.HTTPHeaders{Workspace: []string{aipsdk.HeaderWorkspace, "X-Custom-WS"}}
	aliasHdr := http.Header{}
	aliasHdr.Set("X-Custom-WS", "/tmp/aip-alias-only")
	trimHdr := http.Header{}
	trimHdr.Set(aipsdk.HeaderWorkspace, "  /tmp/aip-default-path  ")
	trimHdr.Set("X-Custom-WS", "/tmp/aip-alias-path")
	cases := []struct {
		name    string
		headers http.Header
		aliases aipsdk.HTTPHeaders
		want    string
	}{
		{"posix", workspaceHeader("/tmp/aip-workspace-hint-proj-a"), aipsdk.HTTPHeaders{}, "/tmp/aip-workspace-hint-proj-a"},
		{"drive", workspaceHeader(`C:/work/proj-a`), aipsdk.HTTPHeaders{}, `C:/work/proj-a`},
		{"unc", workspaceHeader(`\\build-srv\share\proj-a`), aipsdk.HTTPHeaders{}, `\\build-srv\share\proj-a`},
		{"default_wins_and_trims", trimHdr, aliases, "/tmp/aip-default-path"},
		{"alias", aliasHdr, aliases, "/tmp/aip-alias-only"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			call := workspaceCall()
			if err := sessionwire.ApplyWorkspaceHint(call, tc.headers, tc.aliases); err != nil {
				t.Fatalf("valid hint must project: %v", err)
			}
			got, ok := workspaceExt(t, call)
			if !ok || got != tc.want {
				t.Fatalf("workspace_path = %q, %v; want %q", got, ok, tc.want)
			}
			if string(call.Extensions["keep"]) != `"v"` || call.Route.Selector != "stub:m" {
				t.Fatal("projection must preserve unrelated extensions and routing")
			}
		})
	}
}

func TestApplyWorkspaceHint_RejectsMalformedStatically(t *testing.T) {
	t.Parallel()
	big := "/tmp/" + strings.Repeat("x", 4096)
	cases := map[string]string{
		"oversize":         big,
		"control":          "/tmp/a\nb",
		"padded_control":   "  /tmp/a\x7f  ",
		"invalid_utf8":     string([]byte{'/', 0xff, 0xfe}),
		"relative":         "some/relative/path",
		"dot":              ".",
		"drive_relative":   `C:rel`,
		"drive_bare":       `C:`,
		"single_backslash": `\rooted`,
		"unc_bare":         `\\`,
		"unc_server_only":  `\\srv`,
		"unc_empty_share":  `\\srv\`,
	}
	for name, v := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			call := workspaceCall()
			err := sessionwire.ApplyWorkspaceHint(call, workspaceHeader(v), aipsdk.HTTPHeaders{})
			if err == nil {
				t.Fatal("malformed hint must error")
			}
			if !strings.Contains(err.Error(), "invalid workspace hint") {
				t.Fatalf("error must be static, got %v", err)
			}
			if strings.Contains(err.Error(), v) && v != "" {
				t.Fatalf("error must not echo the value: %v", err)
			}
			if _, ok := call.Extensions[sessionwire.WorkspaceExtensionKey]; ok {
				t.Fatal("rejected hint must not project")
			}
		})
	}
}

func TestApplyWorkspaceHint_NeverOverwritesExistingHints(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		key      string
		existing string
		blocked  bool
	}{
		{"project_dir", `"/tmp/existing-owner"`, true},
		{"workspace_path", `"/tmp/existing-owner"`, true},
		{"cwd", `"/tmp/existing-owner"`, true},
		{"project", `"/tmp/existing-owner"`, true},
		{"cwd", `"   "`, false},
	} {
		t.Run(tc.key, func(t *testing.T) {
			t.Parallel()
			call := workspaceCall()
			call.Extensions[tc.key] = json.RawMessage(tc.existing)
			if err := sessionwire.ApplyWorkspaceHint(call, workspaceHeader("/tmp/aip-new-hint"), aipsdk.HTTPHeaders{}); err != nil {
				t.Fatal(err)
			}
			got, ok := workspaceExt(t, call)
			if tc.blocked {
				if tc.key == "workspace_path" && got != "/tmp/existing-owner" {
					t.Fatalf("existing workspace_path must be preserved, got %q", got)
				}
				if tc.key != "workspace_path" && ok {
					t.Fatalf("existing %s must block workspace_path projection", tc.key)
				}
			} else if !ok || got != "/tmp/aip-new-hint" {
				t.Fatalf("blank hints must not block, got %q", got)
			}
		})
	}
}

func TestApplyWorkspaceHint_PreservesAuthorityFields(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "stub:m"},
		Session: aipapi.SessionRef{
			AuthoritativeSessionID: "sid",
			ClientSessionID:        "cli",
			ResumeToken:            "tok",
			ALegID:                 "a-leg",
		},
		Extensions: map[string]json.RawMessage{
			"acp.sessionId": json.RawMessage(`"s1"`),
			"mcpServers":    json.RawMessage(`[]`),
		},
		Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions},
	}
	before := *call
	if err := sessionwire.ApplyWorkspaceHint(call, workspaceHeader("/tmp/aip-workspace-hint-proj-a"), aipsdk.HTTPHeaders{}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(call.Session, before.Session) || call.Route != before.Route ||
		call.Invocation.Operation != before.Invocation.Operation ||
		string(call.Extensions["acp.sessionId"]) != `"s1"` ||
		string(call.Extensions["mcpServers"]) != `[]` {
		t.Fatal("hint must not touch session/route/invocation/auth/provider/MCP keys")
	}
	if got, _ := workspaceExt(t, call); got != "/tmp/aip-workspace-hint-proj-a" {
		t.Fatalf("hint must project alongside ACP keys, got %q", got)
	}
}
