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
		// claudeBlockOpen is the native Claude Code 2.1.322 non-bare preamble
		// up to its first fact: header line, lead line, then the fact list.
		claudeBlockOpen = "# Environment\nYou have been invoked in the following environment: \n"
		// claudeBlockA is that preamble with one project fact.
		claudeBlockA = claudeBlockOpen + " - Primary working directory: /a/b\n - Platform: linux"
		// claudeBlockPlatform is a block whose facts name no project.
		claudeBlockPlatform = claudeBlockOpen + " - Platform: linux"
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
			name: "a Claude Code bare-mode CWD line yields the working directory",
			call: &aipapi.Call{Instructions: []aipapi.Message{{
				Role: aipapi.RoleSystem,
				Parts: []aipapi.Part{
					{Kind: aipapi.PartText, Text: "x-anthropic-billing-header: cc_version=2.1.316.0fe; cc_entrypoint=sdk-cli;"},
					{Kind: aipapi.PartText, Text: "You are a Claude agent, built on Anthropic's Claude Agent SDK."},
					{Kind: aipapi.PartText, Text: "CWD: /home/user/project\nDate: 2026-11-04"},
				},
			}}},
			wantLabel: "/home/user/project",
			wantOK:    true,
		},
		{
			name: "an unanchored bulleted primary working directory line is not a project",
			call: &aipapi.Call{Messages: []aipapi.Message{textMessage(
				aipapi.RoleSystem, "- Primary working directory: /home/user/project")}},
			// A bullet is presentation of Claude Code's anchored environment
			// block, not a general line form: arbitrary prose must not name a
			// project, or a same-principal request would select an unrelated
			// bank.
			wantOK: false,
		},
		{
			name: "an unanchored bulleted line cannot outrank the Codex environment context",
			call: &aipapi.Call{Messages: []aipapi.Message{
				textMessage(aipapi.RoleSystem, "Notes:\n- Primary working directory: /other"),
				textMessage(aipapi.RoleUser, envContextR),
			}},
			wantLabel: "/r",
			wantOK:    true,
		},
		{
			name: "an anchored environment fact run keeps its bulleted facts",
			call: &aipapi.Call{Messages: []aipapi.Message{textMessage(aipapi.RoleSystem,
				claudeBlockOpen+" - Primary working directory: /real\n - Platform: linux"+
					"\n\nQuoted transcript below:\n - Primary working directory: /quoted")}},
			// The fact run is the list immediately under the lead line; prose
			// below the blank line is model-directed text and cannot name a
			// project with a bullet.
			wantLabel: "/real",
			wantOK:    true,
		},
		{
			name: "a bulleted quoted line below the fact run is not a project",
			call: &aipapi.Call{Messages: []aipapi.Message{textMessage(aipapi.RoleSystem,
				claudeBlockPlatform+"\n\n - Primary working directory: /quoted")}},
			wantOK: false,
		},
		{
			name: "a plain line below the fact run keeps its pre-existing trust",
			call: &aipapi.Call{Messages: []aipapi.Message{textMessage(aipapi.RoleSystem,
				claudeBlockPlatform+"\n\nPrimary working directory: /below")}},
			// Unchanged by this slice: a plain, unbulleted line is the design
			// §3 source wherever it appears in instruction text, so this pins
			// the pre-existing trust boundary instead of implying the anchor
			// protects prose below the facts.
			wantLabel: "/below",
			wantOK:    true,
		},
		{
			name: "a CRLF anchored environment block yields the working directory",
			call: &aipapi.Call{Messages: []aipapi.Message{textMessage(aipapi.RoleSystem,
				"# Environment\r\nYou have been invoked in the following environment: \r\n"+
					" - Primary working directory: C:\\work\\project\r\n - Platform: windows")}},
			wantLabel: "C:/work/project",
			wantOK:    true,
		},
		{
			name: "a blank line directly after the lead line ends the fact run",
			call: &aipapi.Call{Messages: []aipapi.Message{textMessage(aipapi.RoleSystem,
				"# Environment\nYou have been invoked in the following environment:\n\n"+
					"Transcript:\n - Primary working directory: /other")}},
			// The block has no facts, and the bulleted line below belongs to a
			// transcript. Selecting it would contradict the documented rule that
			// a blank line ends the environment fact run.
			wantOK: false,
		},
		{
			name: "prose that continues the lead line is not the anchor",
			call: &aipapi.Call{Messages: []aipapi.Message{textMessage(aipapi.RoleSystem,
				"# Environment\nYou have been invoked in the following environment: Transcript:\n"+
					" - Primary working directory: /other")}},
			wantOK: false,
		},
		{
			name: "a bulleted CWD line is not a project",
			call: &aipapi.Call{Messages: []aipapi.Message{textMessage(aipapi.RoleSystem,
				claudeBlockOpen+" - CWD: /a/b")}},
			// No harness was observed to bullet a "CWD:" line, so only the two
			// environment facts accept a bullet inside the anchored block.
			wantOK: false,
		},
		{
			name: "a double-bulleted environment fact is not a project",
			call: &aipapi.Call{Messages: []aipapi.Message{textMessage(aipapi.RoleSystem,
				claudeBlockOpen+" - - Primary working directory: /a/b")}},
			wantOK: false,
		},
		{
			name: "the environment header without its lead line is not an anchor",
			call: &aipapi.Call{Messages: []aipapi.Message{textMessage(aipapi.RoleSystem,
				"# Environment\n - Primary working directory: /a/b")}},
			wantOK: false,
		},
		{
			name: "an environment block embedded in a rendered transcript is not live",
			call: &aipapi.Call{Messages: []aipapi.Message{textMessage(aipapi.RoleSystem,
				"Transcript follows:\n"+claudeBlockA)}},
			wantOK: false,
		},
		{
			name: "the native Claude Code non-bare environment block yields the working directory",
			call: &aipapi.Call{Messages: []aipapi.Message{
				textMessage(aipapi.RoleUser, "probe"),
				textMessage(aipapi.RoleSystem,
					"# Environment\nYou have been invoked in the following environment: \n"+
						" - Primary working directory: /home/user/project\n - Is a git repository: true"),
			}},
			wantLabel: "/home/user/project",
			wantOK:    true,
		},
		{
			name: "an anchored Claude environment block outranks the Codex environment context",
			call: &aipapi.Call{Messages: []aipapi.Message{
				textMessage(aipapi.RoleUser, envContextR),
				textMessage(aipapi.RoleSystem, claudeBlockA),
			}},
			// The declared design §3 order is unchanged: an instruction line
			// outranks the environment context. This pins the existing order; a
			// request that carries two machine environment authors is an open
			// decision, not something this slice silently reverses.
			wantLabel: "/a/b",
			wantKey:   projectKeyAB,
			wantOK:    true,
		},
		{
			name:      "items authority: an anchored Claude environment item yields the working directory",
			call:      &aipapi.Call{Items: []aipapi.Item{textItem(aipapi.RoleSystem, claudeBlockA)}},
			wantLabel: "/a/b",
			wantKey:   projectKeyAB,
			wantOK:    true,
		},
		{
			name:   "a bulleted line without a recognized prefix is not a project",
			call:   &aipapi.Call{Messages: []aipapi.Message{textMessage(aipapi.RoleSystem, "- Build: run make")}},
			wantOK: false,
		},
		{
			name:      "a CWD line accepts rooted Windows syntax",
			call:      &aipapi.Call{Messages: []aipapi.Message{textMessage(aipapi.RoleSystem, `CWD: C:\work\project`)}},
			wantLabel: "C:/work/project",
			wantOK:    true,
		},
		{
			name:      "a CWD line accepts a UNC path",
			call:      &aipapi.Call{Messages: []aipapi.Message{textMessage(aipapi.RoleSystem, `CWD: \\server\share\project`)}},
			wantLabel: "/server/share/project",
			wantOK:    true,
		},
		{
			name:   "a CWD line rejects a UNC root without a share",
			call:   &aipapi.Call{Messages: []aipapi.Message{textMessage(aipapi.RoleSystem, `CWD: \\server`)}},
			wantOK: false,
		},
		{
			name: "a non-path CWD value is not a project",
			call: &aipapi.Call{Instructions: []aipapi.Message{
				textMessage(aipapi.RoleSystem, "CWD: project"),
			}},
			wantOK: false,
		},
		{
			name: "a non-path CWD value falls through to a working-directory line",
			call: &aipapi.Call{Instructions: []aipapi.Message{
				textMessage(aipapi.RoleSystem, "CWD: unknown\nWorking directory: /fallback"),
			}},
			wantLabel: "/fallback",
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
			name:   "a double-bulleted line is not a project",
			call:   &aipapi.Call{Messages: []aipapi.Message{textMessage(aipapi.RoleSystem, "- - Primary working directory: /two")}},
			wantOK: false,
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
			name:   "a user-role CWD line is not a project",
			call:   &aipapi.Call{Messages: []aipapi.Message{textMessage(aipapi.RoleUser, "CWD: /user")}},
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

func TestDeriveProject_ClaudeCWDSharesProjectWithCodexEnvironment(t *testing.T) {
	t.Parallel()

	codex, ok := DeriveProject(&aipapi.Call{Messages: []aipapi.Message{
		textMessage(aipapi.RoleUser, "<environment_context>\n<cwd>/r</cwd>\n</environment_context>"),
	}})
	if !ok {
		t.Fatal("DeriveProject did not derive the Codex environment project")
	}
	claude, ok := DeriveProject(&aipapi.Call{Instructions: []aipapi.Message{{
		Role: aipapi.RoleSystem,
		Parts: []aipapi.Part{
			{Kind: aipapi.PartText, Text: "CWD: /r\nDate: 2026-11-04"},
		},
	}}})
	if !ok {
		t.Fatal("DeriveProject did not derive the Claude Code CWD project")
	}
	if claude != codex {
		t.Fatalf("Claude Code project = %+v, want the Codex project %+v", claude, codex)
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
