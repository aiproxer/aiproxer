package proxymemory

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Fixed project keys pin the §3 construction: hex(sha256("proxy-memory/v1\x00" + label)).
const (
	projectKeyAB  = "6d3e59cba4acc65e5b423e493dc9c426f1d63b05161c6a05c58cdfba47de2ce6" // /a/b
	projectKeyCXY = "ac101981c8920acd604e593e61e514759f1e88cd7cb821201296daad1408e878" // C:/x/y
)

// rawExtension marshals one extension value into its json.RawMessage slot.
func rawExtension(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal extension value %v: %v", value, err)
	}
	return raw
}

// textMessage builds a single-text-part message.
func textMessage(role aipapi.Role, text string) aipapi.Message {
	return aipapi.Message{Role: role, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: text}}}
}

// textItem builds a single-text-part message item.
func textItem(role aipapi.Role, text string) aipapi.Item {
	return aipapi.Item{
		Kind:    aipapi.ItemKindMessage,
		Role:    role,
		Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: text}},
	}
}

func TestDeriveProject(t *testing.T) {
	t.Parallel()

	const (
		instructionsAB = "Working directory: /a/b/"
		envContextR    = "<environment_context>\n<cwd>/r</cwd>\n</environment_context>"
	)

	cases := []struct {
		name      string
		call      *aipapi.Call
		wantLabel string
		wantKey   string
		wantOK    bool
	}{
		{
			name: "a workspace_path extension wins over the instruction line",
			call: &aipapi.Call{
				Extensions:   map[string]json.RawMessage{"workspace_path": rawExtension(t, "/ws")},
				Instructions: []aipapi.Message{textMessage(aipapi.RoleSystem, instructionsAB)},
			},
			wantLabel: "/ws",
			wantOK:    true,
		},
		{
			name: "project_dir wins over the later extension keys",
			call: &aipapi.Call{Extensions: map[string]json.RawMessage{
				"project_dir":    rawExtension(t, "/pd"),
				"workspace_path": rawExtension(t, "/ws"),
				"cwd":            rawExtension(t, "/cwd"),
				"project":        rawExtension(t, "/proj"),
			}},
			wantLabel: "/pd",
			wantOK:    true,
		},
		{
			name: "workspace_path wins over cwd and project",
			call: &aipapi.Call{Extensions: map[string]json.RawMessage{
				"workspace_path": rawExtension(t, "/ws"),
				"cwd":            rawExtension(t, "/cwd"),
				"project":        rawExtension(t, "/proj"),
			}},
			wantLabel: "/ws",
			wantOK:    true,
		},
		{
			name: "cwd wins over project",
			call: &aipapi.Call{Extensions: map[string]json.RawMessage{
				"cwd":     rawExtension(t, "/cwd"),
				"project": rawExtension(t, "/proj"),
			}},
			wantLabel: "/cwd",
			wantOK:    true,
		},
		{
			name: "project is used when it is the only extension key",
			call: &aipapi.Call{Extensions: map[string]json.RawMessage{
				"project": rawExtension(t, "/proj"),
			}},
			wantLabel: "/proj",
			wantOK:    true,
		},
		{
			name: "a non-string extension is skipped",
			call: &aipapi.Call{Extensions: map[string]json.RawMessage{
				"project_dir":    json.RawMessage("123"),
				"workspace_path": rawExtension(t, "/ws"),
			}},
			wantLabel: "/ws",
			wantOK:    true,
		},
		{
			name: "a blank extension is skipped",
			call: &aipapi.Call{Extensions: map[string]json.RawMessage{
				"project_dir":    rawExtension(t, "   "),
				"workspace_path": rawExtension(t, "/ws"),
			}},
			wantLabel: "/ws",
			wantOK:    true,
		},
		{
			name:      "an instructions line yields the trimmed directory",
			call:      &aipapi.Call{Instructions: []aipapi.Message{textMessage(aipapi.RoleSystem, instructionsAB)}},
			wantLabel: "/a/b",
			wantKey:   projectKeyAB,
			wantOK:    true,
		},
		{
			name:      "a developer message yields a primary working directory line",
			call:      &aipapi.Call{Messages: []aipapi.Message{textMessage(aipapi.RoleDeveloper, `Primary working directory: C:\x\y`)}},
			wantLabel: "C:/x/y",
			wantKey:   projectKeyCXY,
			wantOK:    true,
		},
		{
			name:      "a system message yields a primary working directory line",
			call:      &aipapi.Call{Messages: []aipapi.Message{textMessage(aipapi.RoleSystem, `Primary working directory: C:\x\y`)}},
			wantLabel: "C:/x/y",
			wantKey:   projectKeyCXY,
			wantOK:    true,
		},
		{
			name:      "a Codex environment context part yields the cwd",
			call:      &aipapi.Call{Messages: []aipapi.Message{textMessage(aipapi.RoleUser, envContextR)}},
			wantLabel: "/r",
			wantOK:    true,
		},
		{
			name:   "the environment tag not at the start of user text is ignored",
			call:   &aipapi.Call{Messages: []aipapi.Message{textMessage(aipapi.RoleUser, "prefix "+envContextR)}},
			wantOK: false,
		},
		{
			name:      "items authority: a system item yields an instruction line",
			call:      &aipapi.Call{Items: []aipapi.Item{textItem(aipapi.RoleSystem, instructionsAB)}},
			wantLabel: "/a/b",
			wantKey:   projectKeyAB,
			wantOK:    true,
		},
		{
			name:      "items authority: a user item yields the environment context cwd",
			call:      &aipapi.Call{Items: []aipapi.Item{textItem(aipapi.RoleUser, envContextR)}},
			wantLabel: "/r",
			wantOK:    true,
		},
		{
			name:   "items authority: the environment tag not at the start is ignored",
			call:   &aipapi.Call{Items: []aipapi.Item{textItem(aipapi.RoleUser, "prefix "+envContextR)}},
			wantOK: false,
		},
		{
			name: "an empty extension value falls through to the instruction line",
			call: &aipapi.Call{
				Extensions:   map[string]json.RawMessage{"workspace_path": rawExtension(t, " \t ")},
				Instructions: []aipapi.Message{textMessage(aipapi.RoleSystem, "Working directory: /fallback")},
			},
			wantLabel: "/fallback",
			wantOK:    true,
		},
		{
			name: "an over-length extension value falls through to the instruction line",
			call: &aipapi.Call{
				Extensions:   map[string]json.RawMessage{"workspace_path": rawExtension(t, "/"+strings.Repeat("a", 4096))},
				Instructions: []aipapi.Message{textMessage(aipapi.RoleSystem, "Working directory: /fallback")},
			},
			wantLabel: "/fallback",
			wantOK:    true,
		},
		{
			name: "a control-character extension value falls through to the instruction line",
			call: &aipapi.Call{
				Extensions:   map[string]json.RawMessage{"workspace_path": rawExtension(t, "/bad\x02path")},
				Instructions: []aipapi.Message{textMessage(aipapi.RoleSystem, "Working directory: /fallback")},
			},
			wantLabel: "/fallback",
			wantOK:    true,
		},
		{
			name: "an unusable extension value falls through to the next extension key",
			call: &aipapi.Call{Extensions: map[string]json.RawMessage{
				"project_dir":    rawExtension(t, "/bad\x02path"),
				"workspace_path": rawExtension(t, "/ws"),
			}},
			wantLabel: "/ws",
			wantOK:    true,
		},
		{
			name: "an instruction line with a control character falls through to the environment context",
			call: &aipapi.Call{Messages: []aipapi.Message{
				textMessage(aipapi.RoleSystem, "Working directory: /bad\x02path"),
				textMessage(aipapi.RoleUser, envContextR),
			}},
			wantLabel: "/r",
			wantOK:    true,
		},
		{
			name: "a blank instruction value falls through to the environment context",
			call: &aipapi.Call{Messages: []aipapi.Message{
				textMessage(aipapi.RoleSystem, "Working directory:    "),
				textMessage(aipapi.RoleUser, envContextR),
			}},
			wantLabel: "/r",
			wantOK:    true,
		},
		{
			name: "no source gives ok=false",
			call: &aipapi.Call{
				Extensions: map[string]json.RawMessage{"unrelated": rawExtension(t, "/ignored")},
				Messages:   []aipapi.Message{textMessage(aipapi.RoleAssistant, "Working directory: /not-a-source")},
			},
			wantOK: false,
		},
		{
			name: "a user message is not scanned for instruction lines",
			call: &aipapi.Call{Messages: []aipapi.Message{textMessage(aipapi.RoleUser, "Working directory: /user")}},
			// The working-directory line is a system/developer source; user role
			// supplies only the environment context, so there is no project here.
			wantOK: false,
		},
		{
			name:   "a nil call gives ok=false",
			call:   nil,
			wantOK: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := DeriveProject(tc.call)
			if ok != tc.wantOK {
				t.Fatalf("DeriveProject() ok = %v (project %+v), want %v", ok, got, tc.wantOK)
			}
			if !tc.wantOK {
				if got != (Project{}) {
					t.Fatalf("DeriveProject() = %+v, want the zero project on failure", got)
				}
				return
			}
			if got.Label != tc.wantLabel {
				t.Errorf("DeriveProject() label = %q, want %q", got.Label, tc.wantLabel)
			}
			wantKey := tc.wantKey
			if wantKey == "" {
				wantKey = ProjectKey(tc.wantLabel)
			}
			if got.Key != wantKey {
				t.Errorf("DeriveProject() key = %q, want %q", got.Key, wantKey)
			}
		})
	}
}

func TestDeriveProjectNormalizePath(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "a trailing slash is removed", in: "/a/b/", want: "/a/b"},
		{name: "backslashes become slashes", in: `C:\x\y`, want: "C:/x/y"},
		{name: "slash runs collapse", in: "//a///b//", want: "/a/b"},
		{name: "the root slash is kept", in: "/", want: "/"},
		{name: "whitespace is trimmed", in: "  /a/b  ", want: "/a/b"},
		{name: "an all-whitespace value normalizes to empty", in: " \t ", want: ""},
		{name: "relative paths are labels too", in: "src/app/", want: "src/app"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := NormalizePath(tc.in); got != tc.want {
				t.Errorf("NormalizePath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestDeriveProjectKey(t *testing.T) {
	t.Parallel()

	if got := ProjectKey("/a/b"); got != projectKeyAB {
		t.Errorf("ProjectKey(%q) = %q, want %q", "/a/b", got, projectKeyAB)
	}
	if got := ProjectKey("C:/x/y"); got != projectKeyCXY {
		t.Errorf("ProjectKey(%q) = %q, want %q", "C:/x/y", got, projectKeyCXY)
	}
	if got := ProjectKey("/a/b"); len(got) != 64 {
		t.Errorf("ProjectKey(%q) length = %d, want 64 hex characters", "/a/b", len(got))
	}
}
