package stdhttp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/refbackend/openaichat"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// Issue #831: a multi-turn agent session through the real proxy with only the
// powershell-heredoc-steering feature enabled. The session is the scratch E2E
// (T1-T6, 12 upstream requests) plus T7, whose invalid heredoc is repeated after
// the per-turn correction budget is spent, so the repeat is delivered unchanged:
// the documented fail-open path.

const (
	psModel             = "invented:model-a"
	psClientRequests    = 12
	psUpstreamRequests  = 15
	psMaxClientRequests = 30

	psCmdListFiles     = "Get-ChildItem -Name"
	psCmdPSInvalid     = "python - <<'PY'\nprint(1+1)\nPY"
	psCmdPSCorrected   = "@'\nprint(1+1)\n'@ | python -"
	psCmdBashValid     = "python3 - <<'PY'\nprint('hello-from-bash')\nPY"
	psCmdBashInvalid   = ".\\.venv\\Scripts\\python.exe - <<'PY'\nprint(1+1)\nPY"
	psCmdBashCorrected = "./.venv/Scripts/python.exe - <<'PY'\nprint(1+1)\nPY"
	psCmdPSInvalidT7   = "python - <<'PY'\nprint(2+2)\nPY"
	psParserError      = "ParserError: Missing file specification after redirection operator."

	// Distinctive substrings of the two hidden steering instructions.
	psMarkPowerShell = "heredoc/here-string redirection, which PowerShell cannot parse"
	psMarkAuto       = "combines an unquoted backslash Windows path"
)

// psSteerPhrases are the phrases of both hidden instructions that must never reach
// the client.
var psSteerPhrases = []string{"has not executed", "Rewrite it"}

// psUpstreamByClient maps each client request (1-based, in order) to the upstream
// requests it produced. A steered call adds a second upstream request.
var psUpstreamByClient = [][]int64{
	{1}, {2}, {3}, {4, 5}, {6}, {7}, {8}, {9, 10}, {11}, {12}, {13, 14}, {15},
}

const psConfigTemplate = `server:
  address: "127.0.0.1:0"
access:
  mode: single_user
routing:
  max_attempts: 2
  default_route: "invented:model-a"
continuity:
  in_memory: true
  store: memory
logging:
  level: info
  format: text
plugins:
  frontends:
    - id: openai-legacy
      enabled: true
      config: {}
  backends:
    - id: invented
      kind: custom-openai-legacy-compatible
      enabled: true
      config:
        backend_prefix: invented
        base_url: %s
        models:
          source: inline
          items:
            - canonical_id: model-a
              native_id: model-a
  features:
    - id: powershell-heredoc-steering
      enabled: true
      config:
        tools:
          shell_command: { arg: command, shell: powershell }
          Bash: { arg: command, shell: auto }
`

// TestPowerShellHeredocSteering_multiTurnAgentSession drives the agent emulator
// through seven client turns and checks the client wire, the upstream requests and
// the proxy log against the #831 properties.
func TestPowerShellHeredocSteering_multiTurnAgentSession(t *testing.T) {
	t.Parallel()

	script := psScriptedReplies()
	var (
		mu       sync.Mutex
		upstream = map[int64][]byte{}
	)
	backend := httptest.NewServer(openaichat.NewHandler(openaichat.Config{
		AllowMissingBearer: true,
		Responder: func(req openaichat.Request) openaichat.Response {
			mu.Lock()
			upstream[req.Sequence] = req.Body
			mu.Unlock()
			if req.Sequence < 1 || int(req.Sequence) > len(script) {
				return openaichat.Response{SSE: psTextSSE("unscripted", "unscripted upstream request")}
			}
			return openaichat.Response{SSE: script[req.Sequence-1]}
		},
	}))
	t.Cleanup(backend.Close)

	configPath := filepath.Join(t.TempDir(), "powershell-heredoc-session.yaml")
	if err := os.WriteFile(configPath, fmt.Appendf(nil, psConfigTemplate, backend.URL), 0o600); err != nil {
		t.Fatal(err)
	}
	logs := &psLogBuffer{}
	h := startPSSessionHarness(t, configPath, logs)

	session := &psSession{t: t, client: h.srv.Client(), url: h.baseURL + "/v1/chat/completions"}
	session.userTurn("T1", "Hello, what can you do?")
	session.userTurn("T2", "List the files in the current directory.")
	session.userTurn("T3", "Compute 1+1 with python.")
	session.userTurn("T4", "Now the same in bash.")
	session.userTurn("T5", "Run the venv python on the same snippet.")
	session.userTurn("T6", "Thanks, that's all.")
	session.userTurn("T7", "Run it once more with python.")

	check := func(id, desc string, ok bool, detail string) {
		t.Helper()
		if !ok {
			t.Errorf("%s %s\n    %s", id, desc, detail)
		}
	}

	if len(session.steps) != psClientRequests {
		t.Fatalf("R1 client completed %d requests, want %d", len(session.steps), psClientRequests)
	}
	mu.Lock()
	reqs := make(map[int64][]byte, len(upstream))
	for n, raw := range upstream {
		reqs[n] = raw
	}
	mu.Unlock()
	seqs := make([]int64, 0, len(reqs))
	for n := range reqs {
		seqs = append(seqs, n)
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	wantSeqs := make([]int64, 0, psUpstreamRequests)
	for n := int64(1); n <= psUpstreamRequests; n++ {
		wantSeqs = append(wantSeqs, n)
	}
	if !reflect.DeepEqual(seqs, wantSeqs) {
		t.Fatalf("R2 upstream POST count: REQ numbers=%v, want REQ1..REQ%d contiguous", seqs, psUpstreamRequests)
	}

	// A-leg: what the client sent and received.
	for _, a := range []struct{ id, label, cmd string }{
		{"A1", "R04", psCmdPSInvalid},
		{"A2", "R09", psCmdBashInvalid},
	} {
		hits := session.clientHits(a.cmd)
		check(a.id, a.label+" invalid heredoc command never appears in client SSE or client requests",
			len(hits) == 0, fmt.Sprintf("hits=%v", hits))
	}
	var phraseHits []string
	for i, st := range session.steps {
		for _, p := range psSteerPhrases {
			if strings.Contains(string(st.response), p) {
				phraseHits = append(phraseHits, fmt.Sprintf("c%02d response %q", i+1, p))
			}
			if strings.Contains(string(st.request), p) {
				phraseHits = append(phraseHits, fmt.Sprintf("c%02d request %q", i+1, p))
			}
		}
	}
	check("A4", "steering phrases 'has not executed' and 'Rewrite it' never reach the client",
		len(phraseHits) == 0, fmt.Sprintf("hits=%v", phraseHits))

	wantT3 := []string{fmt.Sprintf("call_r05 shell_command %q", psCmdPSCorrected)}
	gotT3 := session.turnCalls("T3")
	check("A5", "T3 client received exactly one tool call: the corrected shell_command",
		reflect.DeepEqual(gotT3, wantT3), fmt.Sprintf("T3 tool calls=%q, want %q", gotT3, wantT3))

	wantT5 := []string{fmt.Sprintf("call_r10 Bash %q", psCmdBashCorrected)}
	gotT5 := session.turnCalls("T5")
	check("A6", "T5 client received exactly one tool call: the corrected Bash command",
		reflect.DeepEqual(gotT5, wantT5), fmt.Sprintf("T5 tool calls=%q, want %q", gotT5, wantT5))

	var finishProblems []string
	for i, st := range session.steps {
		want := "stop"
		if len(st.reply.calls) > 0 {
			want = "tool_calls"
		}
		if len(st.reply.finishes) != 1 || st.reply.finishes[0] != want || st.reply.dones != 1 {
			finishProblems = append(finishProblems,
				fmt.Sprintf("c%02d finishes=%v done=%d want [%s] and one [DONE]", i+1, st.reply.finishes, st.reply.dones, want))
		}
	}
	check("A7", "every client-received response ends with exactly one finish and one [DONE]",
		len(finishProblems) == 0, fmt.Sprintf("violations=%v", finishProblems))

	wantT2 := []string{fmt.Sprintf("call_r02 shell_command %q", psCmdListFiles)}
	gotT2 := session.turnCalls("T2")
	check("A8", "control: valid T2 shell_command passes through unchanged",
		reflect.DeepEqual(gotT2, wantT2), fmt.Sprintf("T2 tool calls=%q, want %q", gotT2, wantT2))

	wantT4 := []string{fmt.Sprintf("call_r07 Bash %q", psCmdBashValid)}
	gotT4 := session.turnCalls("T4")
	executedT4 := session.resultOf("call_r07") == "hello-from-bash"
	check("A9", "no false positive: valid Bash heredoc reaches the client unchanged and runs",
		reflect.DeepEqual(gotT4, wantT4) && executedT4,
		fmt.Sprintf("T4 tool calls=%q, want %q; executed=%v", gotT4, wantT4, executedT4))

	check("A10", "client final history has the natural user/assistant/tool shape with no invented results",
		session.historyProblems() == "", session.historyProblems())

	wantT7 := []string{fmt.Sprintf("call_r14 shell_command %q", psCmdPSInvalidT7)}
	gotT7 := session.turnCalls("T7")
	check("A11", "T7 client received exactly one tool call: the repeated invalid heredoc (call_r14), delivered once",
		reflect.DeepEqual(gotT7, wantT7), fmt.Sprintf("T7 tool calls=%q, want %q", gotT7, wantT7))
	hitsR13 := session.clientHits("call_r13")
	check("A12", "the steered R13 call (call_r13) never reaches the client",
		len(hitsR13) == 0, fmt.Sprintf("hits=%v", hitsR13))

	// B-leg: what the upstream backend received.
	for _, n := range seqs {
		if reqs[n] == nil {
			t.Fatalf("R2 missing upstream REQ%d", n)
		}
	}
	generic := map[int64][]map[string]any{}
	typed := map[int64]psRequest{}
	for _, n := range seqs {
		generic[n] = psGenericMessages(t, reqs[n])
		typed[n] = psDecodeRequest(t, reqs[n])
	}

	// B1: the hidden instruction is in its own steered follow-up.
	hasMark := func(n int64, mark string) (bool, string) {
		for _, m := range generic[n] {
			for _, leaf := range psLeaves(m, nil) {
				if strings.Contains(leaf, mark) {
					role, _ := m["role"].(string)
					return true, role
				}
			}
		}
		return false, ""
	}
	okPS5, _ := hasMark(5, psMarkPowerShell)
	okAuto10, _ := hasMark(10, psMarkAuto)
	okPS14, role14 := hasMark(14, psMarkPowerShell)
	check("B1", "REQ5 and REQ14 carry the PowerShell instruction, REQ10 the Bash instruction",
		okPS5 && okAuto10 && okPS14,
		fmt.Sprintf("REQ5 PowerShell=%v REQ10 Bash=%v REQ14 PowerShell=%v (REQ14 role=%q)", okPS5, okAuto10, okPS14, role14))

	// B2: every upstream history equals the client's history for that step, once the
	// overlay is removed.
	var mismatches []string
	overlayCount := map[int64]int{}
	for k, ids := range psUpstreamByClient {
		clientMsgs := psNormalize(psGenericMessages(t, session.steps[k].request), false)
		for i, n := range ids {
			overlayCount[n] = psOverlayCount(generic[n])
			wantOverlay := 0
			if i > 0 {
				wantOverlay = 1
			}
			got := psNormalize(generic[n], true)
			if overlayCount[n] != wantOverlay || !reflect.DeepEqual(got, clientMsgs) {
				mismatches = append(mismatches, fmt.Sprintf("REQ%d (client c%02d): overlay=%d want %d, history_equal=%v",
					n, k+1, overlayCount[n], wantOverlay, reflect.DeepEqual(got, clientMsgs)))
			}
		}
	}
	check("B2", "every upstream history equals the client's history for that step (overlay removed; null content as '')",
		len(mismatches) == 0, fmt.Sprintf("mismatches=%v", mismatches))

	// B3: steering text appears only in its own steered follow-up.
	var phraseReqs, psReqs, autoReqs []int64
	for _, n := range seqs {
		leaves := psMessagesLeaves(generic[n])
		if psAnyContains(leaves, psSteerPhrases) {
			phraseReqs = append(phraseReqs, n)
		}
		if psAnyContains(leaves, []string{psMarkPowerShell}) {
			psReqs = append(psReqs, n)
		}
		if psAnyContains(leaves, []string{psMarkAuto}) {
			autoReqs = append(autoReqs, n)
		}
	}
	check("B3", "steering text appears only in its own steered follow-up (REQ5 and REQ14 for PowerShell, REQ10 for Bash)",
		reflect.DeepEqual(phraseReqs, []int64{5, 10, 14}) && reflect.DeepEqual(psReqs, []int64{5, 14}) &&
			reflect.DeepEqual(autoReqs, []int64{10}),
		fmt.Sprintf("steering phrase in REQ%v; PowerShell mark in REQ%v; Bash mark in REQ%v", phraseReqs, psReqs, autoReqs))

	// B4: the invalid R04 and R09 commands appear in no upstream request.
	var invalidHits []string
	for _, n := range seqs {
		for _, a := range []struct{ label, cmd string }{{"R04", psCmdPSInvalid}, {"R09", psCmdBashInvalid}} {
			if psContainsWire(reqs[n], a.cmd) {
				invalidHits = append(invalidHits, fmt.Sprintf("REQ%d:%s", n, a.label))
			}
		}
	}
	check("B4", "the invalid heredoc commands (R04, R09) appear in no upstream request",
		len(invalidHits) == 0, fmt.Sprintf("hits=%v", invalidHits))

	// B8: the withheld R13 call appears in no upstream request, and the follow-up
	// REQ14 carries no copy of its command.
	var r13Hits []string
	for _, n := range seqs {
		if bytes.Contains(reqs[n], []byte("call_r13")) {
			r13Hits = append(r13Hits, fmt.Sprintf("REQ%d", n))
		}
	}
	req14HasCommand := psContainsWire(reqs[14], psCmdPSInvalidT7)
	check("B8", "the withheld R13 call (call_r13) appears in no upstream request, and REQ14 carries no copy of its command",
		len(r13Hits) == 0 && !req14HasCommand,
		fmt.Sprintf("call_r13 in %v; REQ14 carries the R13 command=%v", r13Hits, req14HasCommand))

	req4Prior := 0
	for _, m := range typed[4].Messages {
		for _, c := range m.ToolCalls {
			if c.Function.Name == "shell_command" {
				req4Prior++
			}
		}
	}
	check("B5", "REQ4 has a prior shell_command call from T2 in its history",
		req4Prior == 1, fmt.Sprintf("prior shell_command calls in REQ4=%d", req4Prior))

	// B6: REQ6 has the corrected call and its result, and no steering text or invalid heredoc.
	var correctedID string
	for _, m := range typed[6].Messages {
		for _, c := range m.ToolCalls {
			if cmd, _ := psCommandOf(c.Function.Arguments); cmd == psCmdPSCorrected {
				correctedID = c.ID
			}
		}
	}
	resultOK := false
	for _, m := range typed[6].Messages {
		if m.Role == "tool" && m.ToolCallID == correctedID && psText(m.Content) == "2" {
			resultOK = true
		}
	}
	clean := !psAnyContains(psMessagesLeaves(generic[6]), psSteerPhrases) && !psContainsWire(reqs[6], psCmdPSInvalid)
	check("B6", "REQ6 has the corrected call and tool result '2', with no steering text or invalid heredoc",
		correctedID != "" && resultOK && clean,
		fmt.Sprintf("corrected call id=%q result 2 present=%v clean=%v", correctedID, resultOK, clean))

	// B7: REQ12 (T6) is the genuine full history.
	got12 := psNormalize(generic[12], true)
	want12 := psNormalize(psGenericMessages(t, session.steps[9].request), false)
	check("B7", "REQ12 (T6) carries the full genuine history equal to the client's request",
		reflect.DeepEqual(got12, want12), fmt.Sprintf("REQ12 messages=%d client messages=%d", len(got12), len(want12)))

	// Proxy log.
	var tncRecords []map[string]string
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "msg=tool_name_correction") {
			tncRecords = append(tncRecords, psKV(line))
		}
	}
	corrected, allPS := 0, true
	var outcomes []string
	for _, r := range tncRecords {
		if r["outcome"] == "corrected" {
			corrected++
		}
		if r["trigger"] != "powershell_heredoc" {
			allPS = false
		}
		outcomes = append(outcomes, r["outcome"]+"/"+r["reason"])
	}
	check("P1", "four tool_name_correction records, all trigger=powershell_heredoc, three with outcome=corrected",
		len(tncRecords) == 4 && allPS && corrected == 3,
		fmt.Sprintf("records=%d corrected=%d all_powershell_heredoc=%v outcome/reason=%v", len(tncRecords), corrected, allPS, outcomes))
	budgetRecords := 0
	for _, r := range tncRecords {
		if r["outcome"] == "delivered" && r["reason"] == "budget_spent" && r["trigger"] == "powershell_heredoc" {
			budgetRecords++
		}
	}
	check("P5", "one trigger=powershell_heredoc outcome=delivered reason=budget_spent record (the T7 repeat)",
		budgetRecords == 1, fmt.Sprintf("budget_spent delivered records=%d", budgetRecords))
	for _, p := range []struct{ id, token string }{{"P2", "followup_failed"}, {"P3", "withhold_overflow"}} {
		count := strings.Count(logs.String(), p.token)
		check(p.id, fmt.Sprintf("no %s records", p.token), count == 0, fmt.Sprintf("lines containing %s=%d", p.token, count))
	}
	leaks := map[string]bool{}
	for _, tok := range []string{"print(1+1)", "<<'PY'", ".venv", "print(2+2)"} {
		leaks[tok] = strings.Contains(logs.String(), tok)
	}
	check("P4", "no raw command text in the proxy log (print(1+1), <<'PY', .venv, print(2+2))",
		!leaks["print(1+1)"] && !leaks["<<'PY'"] && !leaks[".venv"] && !leaks["print(2+2)"],
		fmt.Sprintf("tokens present=%v", leaks))
}

// psScriptedReplies is the upstream script. Entry i answers upstream request i+1.
func psScriptedReplies() []string {
	return []string{
		psTextSSE("r01", "I can run shell commands for you."),                         // REQ1 T1
		chatToolCallSSE("r02", "call_r02", "shell_command", psArgs(psCmdListFiles)),   // REQ2 T2
		psTextSSE("r03", "There are 3 files: a.txt, b.txt, c.txt."),                   // REQ3
		chatToolCallSSE("r04", "call_r04", "shell_command", psArgs(psCmdPSInvalid)),   // REQ4 T3, steered
		chatToolCallSSE("r05", "call_r05", "shell_command", psArgs(psCmdPSCorrected)), // REQ5
		psTextSSE("r06", "1+1 = 2."),                                                  // REQ6
		chatToolCallSSE("r07", "call_r07", "Bash", psArgs(psCmdBashValid)),            // REQ7 T4
		psTextSSE("r08", "Done: hello-from-bash."),                                    // REQ8
		chatToolCallSSE("r09", "call_r09", "Bash", psArgs(psCmdBashInvalid)),          // REQ9 T5, steered
		chatToolCallSSE("r10", "call_r10", "Bash", psArgs(psCmdBashCorrected)),        // REQ10
		psTextSSE("r11", "Result: 2."),                                                // REQ11
		psTextSSE("r12", "You're welcome."),                                           // REQ12 T6
		chatToolCallSSE("r13", "call_r13", "shell_command", psArgs(psCmdPSInvalidT7)), // REQ13 T7, steered
		chatToolCallSSE("r14", "call_r14", "shell_command", psArgs(psCmdPSInvalidT7)), // REQ14 repeat, delivered
		psTextSSE("r15", "PowerShell rejected the heredoc."),                          // REQ15
	}
}

// psTextSSE scripts one streamed chat-completions text answer.
func psTextSSE(id, text string) string {
	return fmt.Sprintf(
		"data: {\"id\":%q,\"object\":\"chat.completion.chunk\",\"created\":1715620000,\"model\":\"model-a\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":%q},\"finish_reason\":null}]}\n\n"+
			"data: {\"id\":%q,\"object\":\"chat.completion.chunk\",\"created\":1715620000,\"model\":\"model-a\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"+
			"data: [DONE]\n\n",
		id, text, id)
}

// psArgs is the arguments JSON of a call with one command argument.
func psArgs(command string) string {
	// Encoding a map of one string cannot fail.
	args, _ := psEncode(map[string]string{"command": command}, false)
	return args
}

// psEncode encodes v as one compact JSON value, optionally HTML-escaping it the way
// the Go proxy does.
func psEncode(v any, escapeHTML bool) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(escapeHTML)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

// psWireForms returns the byte forms a string may take inside a JSON body: plain and
// HTML-escaped, each also re-encoded as a JSON string, since a tool-call command sits
// inside an "arguments" string.
func psWireForms(s string) []string {
	var forms []string
	for _, escapeHTML := range []bool{false, true} {
		once, _ := psEncode(s, escapeHTML)
		twice, _ := psEncode(once, escapeHTML)
		forms = append(forms, once[1:len(once)-1], twice[1:len(twice)-1])
	}
	return forms
}

// psContainsWire reports whether a wire body carries s in any JSON byte form.
func psContainsWire(body []byte, s string) bool {
	for _, form := range psWireForms(s) {
		if bytes.Contains(body, []byte(form)) {
			return true
		}
	}
	return false
}

// psFuncDef, psToolDef, psRequest, psMessage and psToolCall are the chat-completions
// wire shapes the emulator sends and decodes.
type psFuncDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type psToolDef struct {
	Type     string    `json:"type"`
	Function psFuncDef `json:"function"`
}

type psRequest struct {
	Model    string      `json:"model"`
	Stream   bool        `json:"stream"`
	Messages []psMessage `json:"messages"`
	Tools    []psToolDef `json:"tools,omitempty"`
}

type psMessage struct {
	Role       string       `json:"role"`
	Content    *string      `json:"content"`
	ToolCalls  []psToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
}

type psToolCall struct {
	ID       string     `json:"id"`
	Type     string     `json:"type"`
	Function psCallFunc `json:"function"`
}

type psCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// psTools is the tool list offered on every client request: both shell tools take a
// "command" string.
func psTools() []psToolDef {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{"type": "string", "description": "The command to run."},
		},
		"required": []string{"command"},
	}
	return []psToolDef{
		{Type: "function", Function: psFuncDef{Name: "shell_command", Description: "Runs a command in Windows PowerShell.", Parameters: params}},
		{Type: "function", Function: psFuncDef{Name: "Bash", Description: "Runs a command in Bash.", Parameters: params}},
	}
}

// psCommandOf returns the decoded "command" argument of a tool call.
func psCommandOf(args string) (string, bool) {
	var obj struct {
		Command *string `json:"command"`
	}
	if err := json.Unmarshal([]byte(args), &obj); err != nil || obj.Command == nil {
		return "", false
	}
	return *obj.Command, true
}

// psCannedResult is the result the client returns for an executed call. Any other
// call gets an error result, so an unexpected call is visible in the history checks.
func psCannedResult(tool, command string) string {
	switch {
	case tool == "shell_command" && command == psCmdListFiles:
		return "a.txt\nb.txt\nc.txt"
	case tool == "shell_command" && command == psCmdPSCorrected:
		return "2"
	case tool == "Bash" && command == psCmdBashValid:
		return "hello-from-bash"
	case tool == "Bash" && command == psCmdBashCorrected:
		return "2"
	case tool == "shell_command" && command == psCmdPSInvalidT7:
		return psParserError
	}
	return fmt.Sprintf("error: no canned result for %s", tool)
}

// psSession is the in-test agent emulator. It keeps the client message history,
// streams every request, rebuilds the assistant message, runs its tool calls with
// canned results, and repeats until the model answers with text only.
type psSession struct {
	t       *testing.T
	client  *http.Client
	url     string
	history []psMessage
	steps   []psStep
}

// psStep is one client request: the exact body sent, the SSE the client received,
// and the assistant reply rebuilt from it.
type psStep struct {
	turn     string
	request  []byte
	response []byte
	reply    psReply
}

type psReply struct {
	text     string
	calls    []psToolCall
	finishes []string
	dones    int
}

func (s *psSession) userTurn(turn, text string) {
	s.t.Helper()
	s.history = append(s.history, psMessage{Role: "user", Content: &text})
	for {
		if len(s.steps) >= psMaxClientRequests {
			s.t.Fatalf("%s: more than %d client requests; the agent loop did not finish", turn, psMaxClientRequests)
		}
		step := s.post(turn)
		s.steps = append(s.steps, step)
		msg := psMessage{Role: "assistant", ToolCalls: step.reply.calls}
		if step.reply.text != "" {
			content := step.reply.text
			msg.Content = &content
		}
		s.history = append(s.history, msg)
		if len(step.reply.calls) == 0 {
			return
		}
		for _, call := range step.reply.calls {
			command, ok := psCommandOf(call.Function.Arguments)
			if !ok {
				s.t.Fatalf("%s: client request %d: tool call %s has unparseable arguments %q",
					turn, len(s.steps), call.ID, call.Function.Arguments)
			}
			result := psCannedResult(call.Function.Name, command)
			s.history = append(s.history, psMessage{Role: "tool", ToolCallID: call.ID, Content: &result})
		}
	}
}

// post sends the current history as one streamed request and returns the step.
func (s *psSession) post(turn string) psStep {
	s.t.Helper()
	body, err := psEncode(psRequest{Model: psModel, Stream: true, Messages: s.history, Tools: psTools()}, false)
	if err != nil {
		s.t.Fatalf("%s: encode client request %d: %v", turn, len(s.steps)+1, err)
	}
	resp, err := s.client.Post(s.url, "application/json", strings.NewReader(body))
	if err != nil {
		s.t.Fatalf("%s: client request %d: %v", turn, len(s.steps)+1, err)
	}
	raw, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		s.t.Fatalf("%s: client request %d: read response: %v", turn, len(s.steps)+1, err)
	}
	if resp.StatusCode != http.StatusOK {
		s.t.Fatalf("%s: client request %d: HTTP %d: %s", turn, len(s.steps)+1, resp.StatusCode, raw)
	}
	return psStep{turn: turn, request: []byte(body), response: raw, reply: psParseStream(s.t, raw)}
}

// clientHits lists the client requests and responses that carry s in any wire form.
func (s *psSession) clientHits(needle string) []string {
	var hits []string
	for i, st := range s.steps {
		if psContainsWire(st.request, needle) {
			hits = append(hits, fmt.Sprintf("c%02d request", i+1))
		}
		if psContainsWire(st.response, needle) {
			hits = append(hits, fmt.Sprintf("c%02d response", i+1))
		}
	}
	return hits
}

// turnCalls lists the tool calls the client received during one turn, as
// "id name command".
func (s *psSession) turnCalls(turn string) []string {
	var out []string
	for _, st := range s.steps {
		if st.turn != turn {
			continue
		}
		for _, c := range st.reply.calls {
			command, _ := psCommandOf(c.Function.Arguments)
			out = append(out, fmt.Sprintf("%s %s %q", c.ID, c.Function.Name, command))
		}
	}
	return out
}

// resultOf returns the content of the tool message answering the call id.
func (s *psSession) resultOf(callID string) string {
	for _, m := range s.history {
		if m.Role == "tool" && m.ToolCallID == callID {
			return psText(m.Content)
		}
	}
	return ""
}

// historyProblems checks the final client history: the expected role shape, one
// result per call with the canned content, and no orphan results.
func (s *psSession) historyProblems() string {
	want := []string{"user", "assistant"}
	for range 4 {
		want = append(want, "user", "assistant", "tool", "assistant")
	}
	want = append(want, "user", "assistant")
	want = append(want, "user", "assistant", "tool", "assistant")
	var shape []string
	for _, m := range s.history {
		shape = append(shape, m.Role)
	}
	var problems []string
	if !reflect.DeepEqual(shape, want) {
		problems = append(problems, fmt.Sprintf("shape=%v, want %v", shape, want))
	}
	callIDs := map[string]bool{}
	for _, m := range s.history {
		if m.Role != "assistant" {
			continue
		}
		for _, c := range m.ToolCalls {
			callIDs[c.ID] = true
			command, _ := psCommandOf(c.Function.Arguments)
			results := 0
			for _, r := range s.history {
				if r.Role == "tool" && r.ToolCallID == c.ID {
					results++
					if got, wantResult := psText(r.Content), psCannedResult(c.Function.Name, command); got != wantResult {
						problems = append(problems, fmt.Sprintf("call %s result %q, want %q", c.ID, got, wantResult))
					}
				}
			}
			if results != 1 {
				problems = append(problems, fmt.Sprintf("call %s has %d results", c.ID, results))
			}
		}
	}
	for _, m := range s.history {
		if m.Role == "tool" && !callIDs[m.ToolCallID] {
			problems = append(problems, fmt.Sprintf("orphan result for %s", m.ToolCallID))
		}
	}
	return strings.Join(problems, "; ")
}

// psParseStream assembles one chat-completions SSE body into text, tool calls, and
// the finish and [DONE] counts the client saw.
func psParseStream(t *testing.T, raw []byte) psReply {
	t.Helper()
	type callDelta struct {
		Index    int    `json:"index"`
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	type chunk struct {
		Choices []struct {
			Delta struct {
				Content   string      `json:"content"`
				ToolCalls []callDelta `json:"tool_calls"`
			} `json:"delta"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
	}
	var out psReply
	byIndex := map[int]*psToolCall{}
	var order []int
	for line := range strings.SplitSeq(string(raw), "\n") {
		payload, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		payload = strings.TrimSpace(payload)
		if payload == "[DONE]" {
			out.dones++
			continue
		}
		var c chunk
		if err := json.Unmarshal([]byte(payload), &c); err != nil {
			t.Fatalf("client wire chunk is not valid JSON: %v: %s", err, payload)
		}
		for _, choice := range c.Choices {
			out.text += choice.Delta.Content
			if choice.FinishReason != nil {
				out.finishes = append(out.finishes, *choice.FinishReason)
			}
			for _, d := range choice.Delta.ToolCalls {
				call := byIndex[d.Index]
				if call == nil {
					call = &psToolCall{Type: "function"}
					byIndex[d.Index] = call
					order = append(order, d.Index)
				}
				if d.ID != "" {
					call.ID = d.ID
				}
				call.Function.Name += d.Function.Name
				call.Function.Arguments += d.Function.Arguments
			}
		}
	}
	sort.Ints(order)
	for _, i := range order {
		out.calls = append(out.calls, *byIndex[i])
	}
	return out
}

// psText returns the string content of a message, or "" when it is null.
func psText(content *string) string {
	if content == nil {
		return ""
	}
	return *content
}

// psGenericMessages decodes an upstream request body into its messages as generic
// JSON values.
func psGenericMessages(t *testing.T, raw []byte) []map[string]any {
	t.Helper()
	var body struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("upstream request body is not JSON: %v", err)
	}
	return body.Messages
}

// psDecodeRequest decodes an upstream request body into typed messages.
func psDecodeRequest(t *testing.T, raw []byte) psRequest {
	t.Helper()
	var req psRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("upstream request body is not a chat request: %v", err)
	}
	return req
}

// psNormalize treats null content and empty string as one representation, and can
// drop the steering overlay.
func psNormalize(msgs []map[string]any, dropOverlay bool) []map[string]any {
	out := []map[string]any{}
	for _, m := range msgs {
		if dropOverlay && psOverlayCount([]map[string]any{m}) > 0 {
			continue
		}
		copied := make(map[string]any, len(m))
		for k, v := range m {
			copied[k] = v
		}
		if copied["content"] == nil {
			copied["content"] = ""
		}
		out = append(out, copied)
	}
	return out
}

// psOverlayCount counts the messages that carry a steering phrase.
func psOverlayCount(msgs []map[string]any) int {
	n := 0
	for _, m := range msgs {
		if psAnyContains(psLeaves(m, nil), psSteerPhrases) {
			n++
		}
	}
	return n
}

// psLeaves collects every string value of a decoded JSON value.
func psLeaves(v any, out []string) []string {
	switch x := v.(type) {
	case string:
		return append(out, x)
	case map[string]any:
		for _, e := range x {
			out = psLeaves(e, out)
		}
	case []any:
		for _, e := range x {
			out = psLeaves(e, out)
		}
	}
	return out
}

// psMessagesLeaves collects every string value of a list of decoded messages.
func psMessagesLeaves(msgs []map[string]any) []string {
	var out []string
	for _, m := range msgs {
		out = psLeaves(m, out)
	}
	return out
}

// psAnyContains reports whether any leaf contains any of the needles.
func psAnyContains(leaves []string, needles []string) bool {
	for _, leaf := range leaves {
		for _, needle := range needles {
			if strings.Contains(leaf, needle) {
				return true
			}
		}
	}
	return false
}

var psKVPattern = regexp.MustCompile(`(\w+)=("[^"]*"|\S+)`)

// psKV parses one text-format log line into its key=value pairs.
func psKV(line string) map[string]string {
	out := map[string]string{}
	for _, m := range psKVPattern.FindAllStringSubmatch(line, -1) {
		out[m[1]] = strings.Trim(m[2], `"`)
	}
	return out
}

// psLogBuffer collects the host's log output. The runtime writes from request
// goroutines, so access is serialized.
type psLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *psLogBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *psLogBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// startPSSessionHarness starts the real host like startDogfoodHarness does, but keeps
// the host's log output in logs for the assertions instead of discarding it.
func startPSSessionHarness(tb testing.TB, configAbsPath string, logs io.Writer) dogfoodHarness {
	tb.Helper()
	host, err := runtimebundle.BuildHost(context.Background(), runtimebundle.BuildHostInput{
		ConfigPath:      configAbsPath,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       logs,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		tb.Fatalf("BuildHost: %v", err)
	}
	h := host.HTTPHandler()
	if h == nil {
		tb.Fatalf("nil host HTTP handler")
	}
	srv := httptest.NewServer(h)
	tb.Cleanup(func() {
		srv.Close()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = host.Close(shutdownCtx)
	})
	return dogfoodHarness{baseURL: srv.URL, srv: srv}
}
