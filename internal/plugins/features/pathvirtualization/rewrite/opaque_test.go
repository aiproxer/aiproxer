package rewrite_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/rewrite"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// This file is the conservative result-handling suite of design.md 246-250. It is
// written as a false-positive regression suite first: the inputs a path-oriented
// recognizer must REFUSE come before the inputs it must accept, because the
// recognizer's failure mode is rewriting source or prose content, not missing a
// path.
//
// Two invariants are load-bearing for everything here. An opaque result is
// byte-for-byte unchanged unless an exact operator profile declared a mode
// (requirement 2.5), and even with a declared mode the recognizer leaves anything
// it cannot prove to be a path listing unchanged (requirement 2.6).

// The tool name the operator layer claims in this file's profiles. It is a
// name no shipped built-in claims, which is what makes these profiles an
// operator decision rather than a built-in one (requirement 3.8).
const opaqueTool = "list_paths"

// winFixtureRoot is a long Windows project root, so the V1 alias for it is also
// strictly shorter (requirement 1.4). Requirement 9.6 asks for one representative
// long Windows and one representative long POSIX path in the fixtures.
const winFixtureRoot = `C:\Users\dev\projects\aiproxer-example-project`

// winFixtureAlias is the Windows V1 alias, spelled out literally so a drift in the
// tag algorithm cannot make these tests agree with a wrong implementation.
const winFixtureAlias = `C:\.__aip_v1__\w_w7y7ycrfzad7plvi3s7a\`

// opaqueRewriter binds the POSIX fixture mapping to an operator profile that claims
// one tool name with the given declared opaque mode.
func opaqueRewriter(t *testing.T, mode pathvirtualization.OpaqueResultMode) *rewrite.Rewriter {
	t.Helper()

	return operatorRewriter(t, []pathvirtualization.ToolProfile{{
		Names:            []string{opaqueTool},
		OpaqueResultMode: mode,
	}}, nil)
}

// opaqueResultItem wraps one tool-result item payload. Canonical validation refuses a
// result that carries Output and Parts at once, so the two are separate fixtures.
func opaqueResultItem(result *aipapi.ToolResultItem) aipapi.Item {
	return aipapi.Item{
		Kind:       aipapi.ItemKindToolResult,
		ID:         "item_result",
		Status:     aipapi.ItemStatusCompleted,
		ToolResult: result,
	}
}

// opaqueToolCall builds the tool call every opaque fixture carries. It uses the
// operator-layer tool name rather than a shipped built-in name, so the only thing
// these fixtures can exercise is the opaque surface.
func opaqueToolCall() aipapi.Item {
	return aipapi.Item{
		Kind:   aipapi.ItemKindToolCall,
		ID:     "item_call",
		Status: aipapi.ItemStatusCompleted,
		ToolCall: &aipapi.ToolCallItem{
			CallID:    "call_7f3a",
			Name:      opaqueTool,
			Arguments: json.RawMessage(`{"file_path":"` + fixtureTarget + `"}`),
		},
	}
}

// opaqueCalls builds one fixture per opaque surface the canonical representation
// exposes, so a single case exercises Output, a text content part, a tool-result
// content part, and the legacy PartToolResult text payload at the same time.
//
// The authorities are separate calls because canonical validation refuses a call
// carrying both ordered items and raw message parts, and an absent payload is not a
// canonical part or a canonical result at all, so the surfaces exist only when there
// is something to carry.
func opaqueCalls(text string) map[string]*aipapi.Call {
	if text == "" {
		return map[string]*aipapi.Call{}
	}
	return map[string]*aipapi.Call{
		"output": {Items: []aipapi.Item{
			opaqueToolCall(),
			opaqueResultItem(&aipapi.ToolResultItem{CallID: "call_7f3a", Name: opaqueTool, Output: text}),
		}},
		"text_part": {Items: []aipapi.Item{
			opaqueToolCall(),
			opaqueResultItem(&aipapi.ToolResultItem{
				CallID: "call_7f3a", Name: opaqueTool,
				Parts: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: text}},
			}),
		}},
		"tool_result_part": {Items: []aipapi.Item{
			opaqueToolCall(),
			opaqueResultItem(&aipapi.ToolResultItem{
				CallID: "call_7f3a", Name: opaqueTool,
				Parts: []aipapi.ContentPart{{Kind: aipapi.ContentPartToolResult, Text: text}},
			}),
		}},
		"legacy_text": {Messages: []aipapi.Message{
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
				{
					Kind: aipapi.PartJSON, ToolCallID: "call_7f3a", ToolName: opaqueTool,
					Content: json.RawMessage(`{"file_path":"` + fixtureTarget + `"}`),
				},
			}},
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{
				{Kind: aipapi.PartToolResult, ToolCallID: "call_7f3a", ToolName: opaqueTool, Text: text},
			}},
		}},
	}
}

// opaqueSurfaceNames returns the opaque surfaces a fixture set carries, in a stable
// order.
func opaqueSurfaceNames(calls map[string]*aipapi.Call) []string {
	names := make([]string, 0, len(calls))
	for _, name := range []string{"output", "text_part", "tool_result_part", "legacy_text"} {
		if _, present := calls[name]; present {
			names = append(names, name)
		}
	}
	return names
}

// opaquePayloadOf returns the opaque payload a published call carries on its one
// opaque surface.
func opaquePayloadOf(t *testing.T, call *aipapi.Call) string {
	t.Helper()

	if call.HasItemAuthority() {
		result := call.Items[1].ToolResult
		if result == nil {
			t.Fatal("tool result data lost")
		}
		if result.Output != "" {
			return result.Output
		}
		if len(result.Parts) != 1 {
			t.Fatalf("tool result carries %d parts, want exactly 1", len(result.Parts))
		}
		return result.Parts[0].Text
	}
	return call.Messages[1].Parts[0].Text
}

// runOpaque rewrites one payload through one declared mode on every opaque surface,
// and fails when two surfaces disagree. That cross-check is the only way a surface
// could leak a rewrite another surface refuses.
//
// It returns the published payload, the statistics summed over every surface (so a
// reason recorded once per surface is visible as a count), and the first error.
func runOpaque(t *testing.T, mode pathvirtualization.OpaqueResultMode, text string) (string, rewrite.Stats, error) {
	t.Helper()

	rewriter := opaqueRewriter(t, mode)
	calls := opaqueCalls(text)

	published := ""
	total := rewrite.Stats{}
	for _, name := range opaqueSurfaceNames(calls) {
		call := calls[name]
		if err := call.Validate(); err != nil {
			t.Fatalf("%s fixture must be canonical: %v", name, err)
		}
		out, stats, err := rewriter.RewriteCall(call)
		if err != nil {
			return "", total, err
		}
		got := opaquePayloadOf(t, out)
		if name == "output" {
			published = got
		} else if got != published {
			t.Fatalf("opaque surfaces disagree:\noutput: %q\n%s:   %q", published, name, got)
		}
		total.Eligible += stats.Eligible
		total.Rewritten += stats.Rewritten
		total.BytesBefore += stats.BytesBefore
		total.BytesAfter += stats.BytesAfter
		for _, skip := range stats.Skips {
			merged := false
			for i := range total.Skips {
				if total.Skips[i].Reason == skip.Reason {
					total.Skips[i].Count += skip.Count
					merged = true
					break
				}
			}
			if !merged {
				total.Skips = append(total.Skips, skip)
			}
		}
	}
	return published, total, nil
}

// opaqueSurfaceCount reports how many opaque surfaces a payload reaches, so a case
// can tell "no surface existed" apart from "the surface recorded nothing". An absent
// payload is not a canonical result surface at all, so an empty payload reaches none.
func opaqueSurfaceCount(text string) int { return len(opaqueCalls(text)) }

// TestOpaqueResultStaysUnchangedWithoutDeclaredMode proves requirement 2.5 on every
// opaque surface, including the text content part whose accounting 4.1 left open.
// The payload is a bare one-path-per-line listing, which is exactly what a declared
// mode rewrites, so this case can only pass while the declared mode is the sole
// authority for opaque rewriting.
func TestOpaqueResultStaysUnchangedWithoutDeclaredMode(t *testing.T) {
	t.Parallel()

	payload := fixtureTarget + "\n" + fixtureRoot + "/pkg/aipapi/items.go"
	calls := opaqueCalls(payload)
	for _, surface := range opaqueSurfaceNames(calls) {
		t.Run(surface, func(t *testing.T) {
			t.Parallel()

			call := calls[surface]
			if err := call.Validate(); err != nil {
				t.Fatalf("fixture call must be canonical: %v", err)
			}

			// The shipped built-in layer is the policy a deployment gets with no
			// operator configuration, and it declares no opaque mode anywhere.
			got, stats, err := fixtureRewriter(t).RewriteCall(call)
			if err != nil {
				t.Fatalf("RewriteCall: %v", err)
			}
			if output := opaquePayloadOf(t, got); output != payload {
				t.Errorf("opaque %s rewritten without a declared mode: %q", surface, output)
			}
			if stats.Eligible != 0 || stats.Rewritten != 0 || stats.BytesSaved() != 0 {
				t.Errorf("stats eligible/rewritten/saved = %d/%d/%d, want 0/0/0",
					stats.Eligible, stats.Rewritten, stats.BytesSaved())
			}
			if count, recorded := skipCount(stats, rewrite.SkipReasonOpaqueResultUnchanged); !recorded || count != 1 {
				t.Errorf("opaque_result_unchanged = %d (recorded %v), want 1", count, recorded)
			}
		})
	}
}

// TestOpaqueResultUnknownToolIsNeverRewritten proves requirement 3.8: no tool an
// exact profile does not claim receives opaque-result rewriting, even when another
// tool's profile declared a mode in the same layer.
func TestOpaqueResultUnknownToolIsNeverRewritten(t *testing.T) {
	t.Parallel()

	payload := fixtureTarget + "\n" + fixtureRoot + "/pkg/aipapi/items.go"
	call := &aipapi.Call{Items: []aipapi.Item{
		opaqueToolCall(),
		{
			Kind: aipapi.ItemKindToolResult, ID: "item_result", Status: aipapi.ItemStatusCompleted,
			ToolResult: &aipapi.ToolResultItem{
				CallID: "call_7f3a",
				Name:   "never_profiled_tool",
				Output: payload,
			},
		},
	}}
	if err := call.Validate(); err != nil {
		t.Fatalf("fixture call must be canonical: %v", err)
	}

	got, stats, err := opaqueRewriter(t, pathvirtualization.OpaqueResultModePathTokens).RewriteCall(call)
	if err != nil {
		t.Fatalf("RewriteCall: %v", err)
	}
	if output := got.Items[1].ToolResult.Output; output != payload {
		t.Errorf("unclaimed tool result was rewritten: %q", output)
	}
	if count, recorded := skipCount(stats, rewrite.SkipReasonOpaqueResultUnchanged); !recorded || count != 1 {
		t.Errorf("opaque_result_unchanged = %d (recorded %v), want 1", count, recorded)
	}
	if _, recorded := skipCount(stats, rewrite.SkipReasonOpaqueResultBounded); recorded {
		t.Error("an unclaimed tool must not be reported as a declared bounded mode")
	}
}

// TestOpaqueResultRefusesSourceAndContentFalsePositives is the false-positive
// regression suite requirement 2.6 asks for. Every case is text that CONTAINS an
// absolute path under the real project root and is not a path listing, so each
// one must survive both recognizers byte-for-byte.
//
// The rule under test: a line is rewritten only when every whitespace- or
// comma-delimited token on it is a path the mapping accepts. Source, diffs, shell
// command lines, logs, stack traces, embedded JSON, and prose all put at least one
// non-path token on the line, so the whole line is refused rather than guessed at.
func TestOpaqueResultRefusesSourceAndContentFalsePositives(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		kind string
		text string
	}{
		// Source code with path-bearing string literals.
		{"go_const_string", "source", `const root = "` + fixtureRoot + `"`},
		{"go_import_block", "source", "\t_ \"" + fixtureRoot + "/pkg/aipapi\"\n"},
		{"go_raw_literal", "source", "var doc = `" + fixtureTarget + "`"},
		{"python_open_call", "source", "    open(\"" + fixtureTarget + "\", \"rb\")"},
		{"python_fstring", "source", "target = f\"see {root}/{name}\"  # " + fixtureTarget},
		{"typescript_import", "source", "import x from \"" + fixtureRoot + "/mod\";"},
		{"shell_heredoc", "source", "cat <<'EOF' > " + fixtureTarget + "\nhello\nEOF"},
		{"sql_literal", "source", "INSERT INTO t VALUES ('" + fixtureRoot + "/x');"},
		{"go_struct_tag", "source", "\tPath string `json:\"" + fixtureTarget + "\"`"},

		// Diffs.
		{"diff_header", "diff", "diff --git a" + fixtureTarget + " b" + fixtureTarget},
		{"diff_full", "diff", strings.Join([]string{
			"diff --git a" + fixtureTarget + " b" + fixtureTarget,
			"index 83db48f..f735c2a 100644",
			"--- a" + fixtureTarget,
			"+++ b" + fixtureTarget,
			"@@ -1,7 +1,7 @@ func main() {",
			" func main() {",
			"-" + fixtureRoot + " := \"old\"",
			"+" + fixtureRoot + " := \"new\"",
			" // " + fixtureTarget,
			"diff --git a" + fixtureRoot + "/pkg/aipapi/items.go b" + fixtureRoot + "/pkg/aipapi/items.go",
		}, "\n")},
		{"diff_context_with_symbol", "diff", " func() " + fixtureRoot + "/pkg/aipapi/call.go"},
		{"diff_added", "diff", "+open(\"" + fixtureTarget + "\")"},
		{"diff_removed", "diff", "-root := \"" + fixtureRoot + "\""},
		{"diff_hunk_header", "diff", "@@ -1,7 +1,7 @@ func main() { // " + fixtureTarget},
		{"diff_stat", "diff", " " + fixtureTarget + " | 4 ++--"},
		{"diff_index", "diff", "index 83db48f..f735c2a 100644"},

		// Shell command lines.
		{"cat_command", "shell", "cat " + fixtureTarget},
		{"curl_command", "shell", `curl -sSL -o ` + fixtureTarget + ` https://example.com/pkg.tgz`},
		{"cd_and_ls", "shell", "cd " + fixtureRoot + " && ls -la"},
		{"tar_command", "shell", "tar -cf a.tar " + fixtureTarget + " " + fixtureRoot + "/pkg/aipapi"},
		{"rm_command", "shell", "rm -rf " + fixtureRoot + "/build"},
		{"grep_command", "shell", "grep -rn root " + fixtureRoot},
		{"wget_command", "shell", "wget https://example.com/a -O " + fixtureTarget},
		{"semicolon_chain", "shell", "cd " + fixtureRoot + "; ls"},
		{"subshell", "shell", "(cd " + fixtureRoot + " && go test ./...)"},

		// Logs and diagnostics.
		{"log_line", "log", "2026-10-02T11:22:33Z INFO read file " + fixtureTarget},
		{"log_quoted_path", "log", `2026-10-02T11:22:33Z INFO opened "` + fixtureTarget + `"`},
		{"log_key_value", "log", "level=debug path=" + fixtureTarget},
		{"log_structured", "log", `ts=2026-10-02 level=info msg="opened" file="` + fixtureTarget + `"`},
		{"compiler_diagnostic", "log", fixtureTarget + ":12:5: undefined: fmt"},
		{"go_test_failure", "log", "--- FAIL: TestX (0.00s)\n    " + fixtureTarget + ":42: boom"},
		{"df_line", "log", "overlay 2147483648 0 2147483648 0% /var/lib ok"},
		{"ldd_line", "log", "\tlinux-vdso.so.1 (0x00007ffd) => not found"},

		// Stack traces.
		{"go_stack_frame", "stack", "\t" + fixtureTarget + ":22 +0x1d"},
		{"go_stack_symbol", "stack", "main.(*Server).Serve(0xc0000b4000)\n\t" + fixtureRoot + "/cmd/aipstd/main.go:88 +0x9a"},
		{"python_traceback", "stack", `  File "` + fixtureTarget + `", line 42, in main` + "\n    raise SystemExit(1)"},
		{"panic_line", "stack", "panic: runtime error: index out of range [3] with length 2"},
		{"node_stack_frame", "stack", "    at Object.<anonymous> (" + fixtureTarget + ":9:11)"},

		// Embedded JSON and other structured blobs.
		{"json_blob", "json", `{"path":"` + fixtureTarget + `","ok":true}`},
		{"json_pretty", "json", "{\n  \"file_path\": \"" + fixtureTarget + "\",\n  \"bytes\": 12\n}"},
		{"logfmt_json_mix", "json", `payload={"a":1} file=` + fixtureTarget},
		{"csv_row", "json", `"` + fixtureTarget + `",42,true`},
		{"array_literal", "json", "[" + fixtureTarget + "," + fixtureRoot + "/pkg/aipapi]"},
		{"json_pointer", "json", "uri=file://" + fixtureRoot + "/pkg/aipapi"},

		// Prose.
		{"prose_sentence", "prose", "The file " + fixtureTarget + " was truncated."},
		{"prose_two_paths", "prose", "I compared " + fixtureTarget + " with " + fixtureRoot + "/pkg/aipapi."},
		{"prose_with_colon", "prose", "Note: " + fixtureTarget + ": use the alias instead"},
		{"markdown_link", "prose", "see [call.go](" + fixtureTarget + ") for details"},
		{"markdown_ref", "prose", "[call.go]: " + fixtureTarget},
		{"url_with_path", "prose", "See https://example.com" + fixtureTarget + " for details"},
		{"file_url", "prose", "file://" + fixtureRoot + "/pkg/aipapi"},
		{"commit_message", "prose", "fix: update " + fixtureTarget + " so the parser copes"},
		{"registry_path", "prose", `HKEY_LOCAL_MACHINE\SOFTWARE\Microsoft\Windows\CurrentVersion`},
		{"env_path", "prose", "PATH=" + fixtureRoot + "/bin:/usr/bin"},
		{"windows_batch", "shell", `copy ` + winFixtureRoot + `\a.txt D:\tmp\`},

		// Mid-token and mid-segment shapes.
		{"identifier_glued", "midtoken", "prefix" + fixtureTarget},
		{"scheme_glued", "midtoken", "src=" + fixtureTarget},
		{"mid_segment", "midtoken", fixtureRoot + "x/a.go"},
		{"diff_marker_glued", "midtoken", "a" + fixtureTarget},
		{"quoted_only", "midtoken", `"` + fixtureTarget + `"`},
		{"single_quoted_only", "midtoken", `'` + fixtureTarget + `'`},
		{"bracket_glued", "midtoken", "(" + fixtureTarget + ")"},
		{"brace_glued", "midtoken", "{" + fixtureTarget + `"}`},
		{"bracket_list_glued", "midtoken", "[" + fixtureTarget + "]"},
		{"relative_path", "midtoken", "pkg/aipapi/call.go"},
		{"parent_relative", "midtoken", "../aiproxer-example-project/pkg/aipapi"},
		{"grep_output_shape", "midtoken", fixtureTarget + ":12:matched line text"},
		{"gcc_diagnostic_shape", "midtoken", fixtureTarget + ": In function 'main':"},

		// Whitespace and delimiter shapes that are not path lists.
		{"blank_line", "shape", ""},
		{"whitespace_only", "shape", "   \t "},
		{"comma_only", "shape", ",,,"},
		{"indentation_only", "shape", "\t\t"},
		{"count_line", "shape", "2 files"},
		{"header_line", "shape", "Found 2 files:"},
	}

	for _, tc := range cases {
		for _, mode := range []pathvirtualization.OpaqueResultMode{
			pathvirtualization.OpaqueResultModePathTokens,
			pathvirtualization.OpaqueResultModePathLines,
		} {
			t.Run(tc.name+"/"+mode.String(), func(t *testing.T) {
				t.Parallel()

				got, stats, err := runOpaque(t, mode, tc.text)
				if err != nil {
					t.Fatalf("RewriteCall: %v", err)
				}
				if got != tc.text {
					t.Errorf("%s payload of kind %q was rewritten:\n in:  %q\n out: %q", mode, tc.kind, tc.text, got)
				}
				if stats.Eligible != 0 || stats.Rewritten != 0 {
					t.Errorf("%s stats eligible/rewritten = %d/%d, want 0/0", mode, stats.Eligible, stats.Rewritten)
				}
				// An absent payload is not a canonical result surface at all, so it
				// reaches no surface and records no reason.
				if surfaces := opaqueSurfaceCount(tc.text); surfaces > 0 {
					count, recorded := skipCount(stats, rewrite.SkipReasonOpaqueResultBounded)
					if !recorded || count != surfaces {
						t.Errorf("%s opaque_result_bounded = %d (recorded %v), want %d",
							mode, count, recorded, surfaces)
					}
				} else if len(stats.Skips) != 0 {
					t.Errorf("%s recorded a skip for a payload that reached no surface: %+v", mode, stats.Skips)
				}
			})
		}
	}
}

// TestOpaqueResultRefusesAdversarialPayloads is the adversarial half of the suite:
// inputs chosen after the rule was fixed, from payload shapes this feature's own
// ecosystem produces, none of which the rule was designed around. Every one contains
// an absolute path under the real project root and none of them is a path listing, so
// each must survive both recognizers byte-for-byte.
//
// These were probed against the implementation while it was written; the shapes that
// did turn out to be refusable, rather than refused, are pinned separately by
// TestOpaqueResultBarePathLineIsTheIrreducibleResidual with the reasoning stated.
func TestOpaqueResultRefusesAdversarialPayloads(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		kind string
		text string
	}{
		// Go source, including a raw string literal that spans lines.
		{"go_os_open_join", "source", `func open() { f, err := os.Open(filepath.Join("` + fixtureRoot + `", "a")) }`},
		{"go_raw_string_multiline", "source", "var doc = `" + fixtureTarget + "\"\nfunc f() {}\n`"},
		{"go_struct_field", "source", "\tRoot string `json:\"" + fixtureRoot + "\"`"},
		// Python and TypeScript.
		{"python_subprocess_list", "source", `    return subprocess.run(["ls", "` + fixtureRoot + `"], check=True)`},
		{"python_fstring_path", "source", `print(f"missing: {p} under ` + fixtureRoot + `")`},
		{"typescript_const_assert", "source", `const cfg = { root: "` + fixtureRoot + `" } as const;`},
		{"typescript_jsdoc", "source", "/**\n * @param root the " + fixtureRoot + " checkout\n */"},
		// Windows batch and PowerShell.
		{"batch_set_var", "shell", "@echo off\r\nset SRC=" + winFixtureRoot + `\a.txt` + "\r\nif exist \"%SRC%\" echo yes"},
		{"powershell_alias", "shell", `Get-ChildItem -Path '` + winFixtureRoot + `\pkg' -Recurse`},
		{"cmd_for_loop", "shell", `for %%f in (` + winFixtureRoot + `\*.txt) do echo %%f`},
		// Structured config files.
		{"yaml_root", "json", "root: " + fixtureRoot + "\nmode: strict"},
		{"dockerfile_copy", "shell", "FROM golang:1.24\nCOPY ./src " + fixtureRoot + "\nRUN cd " + fixtureRoot + " && make"},
		{"makefile_var", "prose", "SRC := " + fixtureRoot + "\nBIN = bin"},
		{"toml_table", "json", "[build]\noutdir = \"" + fixtureRoot + "/out\""},
		// Build and test output.
		{"go_test_ok_line", "log", "ok  \tgithub.com/example/proj\t0.012s\tcoverage: 41.2%"},
		{"go_test_verbose", "log", "=== RUN   TestRewriteCall\n    rewrite_test.go:412: " + fixtureTarget},
		{"cargo_line", "log", "   Compiling aip " + fixtureRoot + " v0.1.0"},
		{"npm_ls_line", "log", "├─ aip@1.0.0 -> file:" + fixtureRoot + "/node_modules/aip"},
		// Runtime diagnostics.
		{"java_stack_frame", "stack", "\tat com.foo.Bar.run(Bar.java:42)"},
		{"node_stack_frame", "stack", "    at /srv/app/index.js:3:7\n    at Module._compile (node:internal/modules:1:1)"},
		{"warn_log_retry", "log", "[warn] retrying " + fixtureTarget + " (attempt 2/5)"},
		{"env_dump", "log", "ROOT=" + fixtureRoot + "\nSHELL=/bin/bash"},
		{"curl_trace_header", "shell", `curl -H "X-Trace: ` + fixtureTarget + `" https://example.com/x`},
		{"awk_command", "shell", "awk '{print $1}' " + fixtureTarget},
		{"grep_with_context", "shell", "grep -C2 -n needle " + fixtureRoot},
		{"sed_in_place", "shell", "sed -i 's/a/b/' " + fixtureTarget},
		// Markdown, registry, URLs, and commit text.
		{"markdown_angle_link", "prose", "see [call.go](<" + fixtureTarget + ">)"},
		{"markdown_code_fence", "prose", "```go\nconst root = \"" + fixtureRoot + "\"\n```"},
		{"registry_run_key", "prose", `HKEY_CURRENT_USER\Software\aip`},
		{"url_with_workspace_segment", "prose", "https://cdn.example.com" + fixtureRoot + "/a.zip"},
		{"commit_message_body", "prose", "fix: handle paths\n\nThe old code hard-coded " + fixtureTarget + "."},
		{"markdown_list_item", "prose", "* see " + fixtureRoot + "/pkg/aipapi/items.go"},
	}

	for _, tc := range cases {
		for _, mode := range []pathvirtualization.OpaqueResultMode{
			pathvirtualization.OpaqueResultModePathTokens,
			pathvirtualization.OpaqueResultModePathLines,
		} {
			t.Run(tc.name+"/"+mode.String(), func(t *testing.T) {
				t.Parallel()

				got, stats, err := runOpaque(t, mode, tc.text)
				if err != nil {
					t.Fatalf("RewriteCall: %v", err)
				}
				if got != tc.text {
					t.Errorf("%s %s payload of kind %q was rewritten:\n in:  %q\n out: %q", mode, tc.name, tc.kind, tc.text, got)
				}
				if stats.Eligible != 0 || stats.Rewritten != 0 {
					t.Errorf("%s %s stats eligible/rewritten = %d/%d, want 0/0",
						mode, tc.name, stats.Eligible, stats.Rewritten)
				}
			})
		}
	}
}

// TestOpaqueResultRewritesBoundedPathLists proves the recognizers are reachable at
// all: with a declared mode, a payload that is genuinely a list of locations is
// virtualized, and every byte that is not a recognized path is carried across.
func TestOpaqueResultRewritesBoundedPathLists(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		text       string
		want       string
		tokensOnly bool
	}{
		{
			name: "single_line",
			text: fixtureTarget,
			want: fixtureVPath,
		},
		{
			name: "bare_root",
			text: fixtureRoot,
			want: fixtureAlias,
		},
		{
			name: "one_per_line",
			text: fixtureTarget + "\n" + fixtureRoot + "/pkg/aipapi/items.go\n",
			want: fixtureVPath + "\n" + fixtureAlias + "pkg/aipapi/items.go\n",
		},
		{
			name: "indented_list",
			text: "  " + fixtureTarget + "\n\t" + fixtureRoot + "/pkg/aipapi\n",
			want: "  " + fixtureVPath + "\n\t" + fixtureAlias + "pkg/aipapi\n",
		},
		{
			name: "crlf_list",
			text: fixtureTarget + "\r\n" + fixtureRoot + "/pkg/aipapi\r\n",
			want: fixtureVPath + "\r\n" + fixtureAlias + "pkg/aipapi\r\n",
		},
		{
			name:       "comma_separated_line",
			text:       fixtureTarget + ", " + fixtureRoot + "/pkg/aipapi",
			want:       fixtureVPath + ", " + fixtureAlias + "pkg/aipapi",
			tokensOnly: true,
		},
		{
			name: "trailing_comma",
			text: fixtureTarget + ",\n",
			want: fixtureVPath + ",\n",
		},
		{
			name: "list_around_prose",
			text: "Found 2 files:\n" + fixtureTarget + "\n" + fixtureRoot + "/pkg/aipapi\ndone\n",
			want: "Found 2 files:\n" + fixtureVPath + "\n" + fixtureAlias + "pkg/aipapi\ndone\n",
		},
		{
			name: "empty_payload",
			text: "",
			want: "",
		},
		{
			name: "trailing_newlines",
			text: fixtureTarget + "\n\n",
			want: fixtureVPath + "\n\n",
		},
	}

	for _, tc := range cases {
		modes := []pathvirtualization.OpaqueResultMode{
			pathvirtualization.OpaqueResultModePathTokens,
			pathvirtualization.OpaqueResultModePathLines,
		}
		if tc.tokensOnly {
			// A line carrying two locations belongs to the token-mode recognizer
			// alone; the line-only mode's own refusal is covered separately.
			modes = modes[:1]
		}
		for _, mode := range modes {
			t.Run(tc.name+"/"+mode.String(), func(t *testing.T) {
				t.Parallel()

				got, stats, err := runOpaque(t, mode, tc.text)
				if err != nil {
					t.Fatalf("RewriteCall: %v", err)
				}
				if got != tc.want {
					t.Errorf("published payload:\n got: %q\nwant: %q", got, tc.want)
				}
				if stats.BytesSaved() != stats.BytesBefore-stats.BytesAfter {
					t.Errorf("BytesSaved disagrees with its totals: %+v", stats)
				}
				if stats.BytesBefore < stats.BytesAfter {
					t.Errorf("an opaque rewrite must shorten the payload: %+v", stats)
				}
			})
		}
	}
}

// TestOpaqueResultPathLinesRefusesMultiTokenLines pins the difference between the
// two enabled modes: path_lines accepts only a line that holds nothing but a
// location, so a line carrying two locations is left to path_tokens.
func TestOpaqueResultPathLinesRefusesMultiTokenLines(t *testing.T) {
	t.Parallel()

	text := fixtureTarget + " " + fixtureRoot + "/pkg/aipapi"

	tokens, _, err := runOpaque(t, pathvirtualization.OpaqueResultModePathTokens, text)
	if err != nil {
		t.Fatalf("RewriteCall: %v", err)
	}
	if want := fixtureVPath + " " + fixtureAlias + "pkg/aipapi"; tokens != want {
		t.Errorf("path_tokens published %q, want %q", tokens, want)
	}

	lines, stats, err := runOpaque(t, pathvirtualization.OpaqueResultModePathLines, text)
	if err != nil {
		t.Fatalf("RewriteCall: %v", err)
	}
	if lines != text {
		t.Errorf("path_lines rewrote a line carrying two locations: %q", lines)
	}
	if stats.Eligible != 0 || stats.Rewritten != 0 {
		t.Errorf("path_lines stats eligible/rewritten = %d/%d, want 0/0", stats.Eligible, stats.Rewritten)
	}
	if stats.BytesBefore != 0 || stats.BytesAfter != 0 {
		t.Errorf("a refused line must contribute no byte totals: %+v", stats)
	}
}

// TestOpaqueResultRewriteIsIdempotent proves requirement 2.9 on the opaque surface:
// reapplying the recognizer to what it published changes nothing, so the idempotent
// request-part hook can run this rewriter again on its own output.
func TestOpaqueResultRewriteIsIdempotent(t *testing.T) {
	t.Parallel()

	for _, mode := range []pathvirtualization.OpaqueResultMode{
		pathvirtualization.OpaqueResultModePathTokens,
		pathvirtualization.OpaqueResultModePathLines,
	} {
		t.Run(mode.String(), func(t *testing.T) {
			t.Parallel()

			rewriter := opaqueRewriter(t, mode)
			first := fixtureTarget + "\n" + fixtureRoot + "/pkg/aipapi"

			published, stats, err := rewriter.RewriteCall(opaqueCalls(first)["output"])
			if err != nil {
				t.Fatalf("RewriteCall: %v", err)
			}
			if stats.Rewritten == 0 {
				t.Fatal("the first pass rewrote nothing; the idempotence proof is vacuous")
			}
			again, secondStats, err := rewriter.RewriteCall(published)
			if err != nil {
				t.Fatalf("second RewriteCall: %v", err)
			}
			if again != published {
				t.Fatal("the second pass published a new call; an idempotent reapplication must change nothing")
			}
			if secondStats.Rewritten != 0 {
				t.Errorf("the second pass rewrote %d payloads", secondStats.Rewritten)
			}
			if output := opaquePayloadOf(t, again); strings.Contains(output, fixtureRoot) {
				t.Errorf("a reapplication lost a rewritten path: %q", output)
			}
		})
	}
}

// TestOpaqueResultMixedAliasAndRealLineIsRefused proves the recognizer fails closed
// on a line that already mixes the alias namespace with the real root. Refusing the
// line is what keeps reapplication stable instead of oscillating between passes.
func TestOpaqueResultMixedAliasAndRealLineIsRefused(t *testing.T) {
	t.Parallel()

	text := fixtureAlias + "pkg/aipapi/items.go " + fixtureTarget

	got, stats, err := runOpaque(t, pathvirtualization.OpaqueResultModePathTokens, text)
	if err != nil {
		t.Fatalf("RewriteCall: %v", err)
	}
	if got != text {
		t.Errorf("a line mixing the alias namespace with the real root was rewritten: %q", got)
	}
	if stats.Rewritten != 0 {
		t.Errorf("stats reported %d rewrites on a refused line", stats.Rewritten)
	}
}

// TestOpaqueResultWindowsFlavor proves the recognizer works through the Windows
// flavor's own matching rules: ASCII case-insensitive prefix comparison, separator
// equivalence, and the short-name refusal of the lexical core.
func TestOpaqueResultWindowsFlavor(t *testing.T) {
	t.Parallel()

	mapping, reason := pathvirtualization.DeriveMapping(winFixtureRoot)
	if reason != pathvirtualization.SkipReasonNone {
		t.Fatalf("DeriveMapping(%q) reason = %q, want none", winFixtureRoot, reason)
	}
	if mapping.VirtualRoot != winFixtureAlias {
		t.Fatalf("DeriveMapping(%q) VirtualRoot = %q, want %q", winFixtureRoot, mapping.VirtualRoot, winFixtureAlias)
	}
	compiled, reject := pathvirtualization.CompileToolProfiles([]pathvirtualization.ToolProfile{{
		Names:            []string{opaqueTool},
		OpaqueResultMode: pathvirtualization.OpaqueResultModePathTokens,
	}})
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("CompileToolProfiles reject = %q, want none", reject)
	}
	resolver, reject := pathvirtualization.NewResolver(compiled, nil, nil)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("NewResolver reject = %q, want none", reject)
	}
	rewriter := rewrite.New(mapping, resolver)

	cases := []struct {
		name string
		text string
		want string
	}{
		{"bare_path", winFixtureRoot + `\a.txt`, winFixtureAlias + `a.txt`},
		{"case_folded_root", `c:\users\DEV\Projects\aiproxer-example-project\a.txt`, winFixtureAlias + `a.txt`},
		// The alias carries the flavor's own separator while the suffix keeps the
		// client's bytes, which is the lexical core's documented behavior rather
		// than a decision this recognizer makes.
		{"forward_separators", "C:/Users/dev/projects/aiproxer-example-project/a.txt", `C:\.__aip_v1__\w_w7y7ycrfzad7plvi3s7a/a.txt`},
		{"bare_root", winFixtureRoot, winFixtureAlias},
		{"relative_child", winFixtureRoot + `\pkg\aipapi\items.go`, winFixtureAlias + `pkg\aipapi\items.go`},
		{"mid_segment_refused", winFixtureRoot + `x\a.txt`, winFixtureRoot + `x\a.txt`},
		{"posix_token_refused", fixtureTarget, fixtureTarget},
		{"unc_token_refused", `\\server\share\a.txt`, `\\server\share\a.txt`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			call := &aipapi.Call{Items: []aipapi.Item{
				opaqueToolCall(),
				{
					Kind: aipapi.ItemKindToolResult, ID: "item_result", Status: aipapi.ItemStatusCompleted,
					ToolResult: &aipapi.ToolResultItem{CallID: "call_7f3a", Name: opaqueTool, Output: tc.text},
				},
			}}
			if err := call.Validate(); err != nil {
				t.Fatalf("fixture call must be canonical: %v", err)
			}
			got, _, err := rewriter.RewriteCall(call)
			if err != nil {
				t.Fatalf("RewriteCall: %v", err)
			}
			if output := got.Items[1].ToolResult.Output; output != tc.want {
				t.Errorf("published %q, want %q", output, tc.want)
			}
		})
	}
}

// TestOpaqueResultAccountsContentFree proves the opaque surfaces reach the same
// content-free accounting as every other selected surface, and that the declared
// mode's refusal is reported as the declared state rather than as the default.
func TestOpaqueResultAccountsContentFree(t *testing.T) {
	t.Parallel()

	published, stats, err := runOpaque(t, pathvirtualization.OpaqueResultModePathLines,
		"Found 2 files:\n"+fixtureTarget+"\n"+fixtureRoot+"/pkg/aipapi\n")
	if err != nil {
		t.Fatalf("RewriteCall: %v", err)
	}
	if !strings.Contains(published, fixtureAlias) {
		t.Fatalf("payload was not virtualized: %q", published)
	}
	// One payload over every opaque surface. Two of its three lines hold a location and
	// each of them holds exactly one, so every surface contributes two eligible leaves
	// and no surface is a refusal: a payload the recognizer accepted records no skip.
	if stats.Eligible != 8 || stats.Rewritten != 8 {
		t.Errorf("stats eligible/rewritten = %d/%d, want 8/8", stats.Eligible, stats.Rewritten)
	}
	if _, recorded := skipCount(stats, rewrite.SkipReasonOpaqueResultBounded); recorded {
		t.Error("an accepted payload must not be reported as a refusal")
	}
	if _, recorded := skipCount(stats, rewrite.SkipReasonOpaqueResultUnchanged); recorded {
		t.Error("a declared mode must not be reported as the default refusal")
	}
	if stats.BytesBefore <= stats.BytesAfter {
		t.Errorf("a virtualizing pass must report savings: %+v", stats)
	}
	// The accounting carries counts and byte totals only, so no payload byte can
	// reach a metric or a log line through it.
	if strings.Contains(reasonLabels(stats), fixtureRoot) ||
		strings.Contains(reasonLabels(stats), fixtureAlias) ||
		strings.Contains(reasonLabels(stats), opaqueTool) {
		t.Error("skip labels carried content")
	}
}

// reasonLabels joins every recorded reason label of one rewrite.
func reasonLabels(stats rewrite.Stats) string {
	var labels strings.Builder
	for _, skip := range stats.Skips {
		labels.WriteString(skip.Reason.String())
		labels.WriteByte(' ')
	}
	return labels.String()
}

// TestOpaqueResultBarePathLineIsTheIrreducibleResidual pins the one shape the
// recognizer cannot refuse, stated explicitly rather than left for a reviewer to
// discover.
//
// A line holding nothing but one absolute path is byte-identical whether it came
// from a path-listing tool, a tab-indented stack trace, or a line of a commit
// message. No rule can separate them from the payload alone, so the guard is the
// one the spec puts in place of a rule: no shipped built-in declares a mode
// (design.md 250), only an exact operator profile can, and that operator is
// asserting this tool's output is path-oriented.
//
// The consequence is bounded and is asserted here: the same payload is untouched
// without a declared mode, so a deployment that has not made the assertion is never
// exposed to it at all.
func TestOpaqueResultBarePathLineIsTheIrreducibleResidual(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		text string
		want string
	}{
		{
			name: "indented_traceback_line",
			text: "panic: boom\n\t" + fixtureTarget + "\nexit status 2",
			want: "panic: boom\n\t" + fixtureVPath + "\nexit status 2",
		},
		{
			name: "commit_body_line",
			text: "The alias applies here.\n\n" + fixtureTarget + "\n",
			want: "The alias applies here.\n\n" + fixtureVPath + "\n",
		},
		{
			// A trailing marker is part of the token the mapping already accepts,
			// and the mapping preserves those suffix bytes, so the marker survives
			// the substitution unchanged.
			name: "trailing_marker_on_a_path_token",
			text: fixtureTarget + ":",
			want: fixtureVPath + ":",
		},
		{
			// A unified-diff CONTEXT line is byte-identical to an indented path
			// listing: one leading space, then one path, then nothing. Every other
			// diff shape is refused outright (see the diff_* cases of the
			// false-positive suite), so a whole diff is untouched; only an isolated
			// context line is indistinguishable from a listing entry.
			name: "isolated_diff_context_line",
			text: " " + fixtureTarget,
			want: " " + fixtureVPath,
		},
		{
			// A hunk header's trailing function context is the same single-token
			// line with a suffix the mapping preserves.
			name: "hunk_header_context_path",
			text: " " + fixtureRoot + "/pkg/aipapi/call.go:42",
			want: " " + fixtureAlias + "pkg/aipapi/call.go:42",
		},
		{
			// A config file's line holding nothing but one absolute path is the same
			// shape as a listing entry. The first line of this payload is refused,
			// and only the bare-path line is re-spelled.
			name: "config_file_bare_path_line",
			text: "/build/\n" + fixtureRoot + "/dist",
			want: "/build/\n" + fixtureAlias + "dist",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			for _, mode := range []pathvirtualization.OpaqueResultMode{
				pathvirtualization.OpaqueResultModePathTokens,
				pathvirtualization.OpaqueResultModePathLines,
			} {
				got, _, err := runOpaque(t, mode, tc.text)
				if err != nil {
					t.Fatalf("RewriteCall: %v", err)
				}
				if got != tc.want {
					t.Errorf("%s published:\n got: %q\nwant: %q", mode, got, tc.want)
				}
			}

			// Without a declared mode the same payload is not merely unrewritten by
			// luck: nothing looks at it.
			calls := opaqueCalls(tc.text)
			for _, surface := range opaqueSurfaceNames(calls) {
				got, _, err := fixtureRewriter(t).RewriteCall(calls[surface])
				if err != nil {
					t.Fatalf("RewriteCall: %v", err)
				}
				if output := opaquePayloadOf(t, got); output != tc.text {
					t.Errorf("%s changed without a declared mode: %q", surface, output)
				}
			}
		})
	}
}

// TestOpaqueResultDeclaredModeWithoutRecognizablePathsIsRefused proves the whole
// reason for a declared mode existing: recognition is narrow enough that a payload
// holding nothing unambiguous stays byte-for-byte unchanged.
func TestOpaqueResultDeclaredModeWithoutRecognizablePathsIsRefused(t *testing.T) {
	t.Parallel()

	for _, text := range []string{
		"",
		"no paths here at all",
		"only " + fixtureRoot + " appears mid-sentence",
		"2026-10-02 read " + fixtureTarget,
		`{"file_path":"` + fixtureTarget + `"}`,
	} {
		got, stats, err := runOpaque(t, pathvirtualization.OpaqueResultModePathTokens, text)
		if err != nil {
			t.Fatalf("RewriteCall(%q): %v", text, err)
		}
		if got != text {
			t.Errorf("payload %q was rewritten to %q, want byte-for-byte unchanged", text, got)
		}
		if stats.Eligible != 0 || stats.Rewritten != 0 {
			t.Errorf("payload %q stats eligible/rewritten = %d/%d, want 0/0", text, stats.Eligible, stats.Rewritten)
		}
		if surfaces := opaqueSurfaceCount(text); surfaces > 0 {
			if count, recorded := skipCount(stats, rewrite.SkipReasonOpaqueResultBounded); !recorded || count != surfaces {
				t.Errorf("payload %q opaque_result_bounded = %d (recorded %v), want %d", text, count, recorded, surfaces)
			}
		} else if len(stats.Skips) != 0 {
			t.Errorf("payload %q reached no surface but recorded skips: %+v", text, stats.Skips)
		}
	}
}
