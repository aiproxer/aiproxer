package powershellheredoc_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/powershellheredoc"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

// armingConfig maps one PowerShell tool and one mixed-shell tool.
const armingConfig = "tools:\n" +
	"  shell_command: { arg: command, shell: powershell }\n" +
	"  Bash: { arg: command, shell: auto }\n"

// featureFinalizer returns the single finalizer the shipped bundle contributes
// for cfg, through the plane composition a host actually uses.
func featureFinalizer(t *testing.T, cfg powershellheredoc.Config) toolcall.Finalizer {
	t.Helper()
	bundle, err := powershellheredoc.FeatureBundle(cfg)
	if err != nil {
		t.Fatalf("FeatureBundle: %v", err)
	}
	finalizers := aipfeature.Get(bundle.PlaneSet, aipfeature.PlaneToolCallFinalizers)
	if len(finalizers) != 1 {
		t.Fatalf("bundle contributes %d finalizers, want exactly 1", len(finalizers))
	}
	return finalizers[0]
}

func armedFinalizer(t *testing.T) toolcall.Finalizer {
	t.Helper()
	cfg, err := decodeConfig(t, armingConfig)
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	return featureFinalizer(t, cfg)
}

func finalize(t *testing.T, fin toolcall.Finalizer, name, args string) toolcall.Result {
	t.Helper()
	res, err := fin.Finalize(t.Context(),
		toolcall.CompletedCall{ToolCallID: "call-1", ToolName: name, ArgsJSON: []byte(args)},
		aipapi.ToolDef{}, nil, toolcall.Meta{})
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	return res
}

// argsJSON encodes a top-level "command" argument, escaping it as a client would.
func argsJSON(t *testing.T, command string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return string(b)
}

func TestFinalizer_IdentityAndOrder(t *testing.T) {
	t.Parallel()
	fin := armedFinalizer(t)
	if fin.ID() != powershellheredoc.ID {
		t.Fatalf("ID()=%q, want %q", fin.ID(), powershellheredoc.ID)
	}
	if fin.Order() != 46 {
		t.Fatalf("Order()=%d, want 46", fin.Order())
	}
}

func TestFinalizer_ArmingMatrix(t *testing.T) {
	t.Parallel()
	fin := armedFinalizer(t)
	cases := []struct {
		name      string
		tool      string
		command   string
		wantSteer bool
	}{
		// shell: powershell steers every heredoc match.
		{name: "powershell steers bash heredoc", tool: "shell_command", command: `cat <<EOF`, wantSteer: true},
		{name: "powershell steers dash heredoc", tool: "shell_command", command: `python - <<'PY'`, wantSteer: true},
		{name: "powershell steers line two", tool: "shell_command", command: "echo ok\npython - <<PY", wantSteer: true},
		{name: "powershell passes plain command", tool: "shell_command", command: `python -c print(1)`, wantSteer: false},
		{name: "powershell passes here-string body", tool: "shell_command", command: "Write-Output @'\n<<EOF\n'@", wantSteer: false},

		// shell: auto steers only with an unquoted Windows path before "<<".
		{name: "auto steers dot-venv path", tool: "Bash", command: `.\.venv\Scripts\python.exe - <<'PY'`, wantSteer: true},
		{name: "auto steers drive path", tool: "Bash", command: `C:\Python\python.exe - <<PY`, wantSteer: true},
		{name: "auto passes bare python", tool: "Bash", command: `python - <<'PY'`, wantSteer: false},
		{name: "auto passes quoted path", tool: "Bash", command: `'C:\Python\python.exe' - <<PY`, wantSteer: false},
		{name: "auto passes forward slash path", tool: "Bash", command: `C:/Python/python.exe - <<PY`, wantSteer: false},
		{name: "auto passes path only after heredoc", tool: "Bash", command: `python - <<PY C:\x`, wantSteer: false},
		{name: "auto passes glued heredoc", tool: "Bash", command: `.\.venv\Scripts\python.exe -<<PY`, wantSteer: false},

		// Tools that are not mapped, or differ in case, pass through.
		{name: "unmapped tool passes", tool: "Read", command: `cat <<EOF`, wantSteer: false},
		{name: "tool name is case sensitive", tool: "bash", command: `.\x.exe <<EOF`, wantSteer: false},
		{name: "empty tool name passes", tool: "", command: `cat <<EOF`, wantSteer: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res := finalize(t, fin, tc.tool, argsJSON(t, tc.command))
			if !tc.wantSteer {
				if res.Action != toolcall.ActionPass || res.Steer != nil {
					t.Fatalf("Finalize(%q, %q) = %+v, want pass", tc.tool, tc.command, res)
				}
				return
			}
			steer, ok := res.UsableSteer()
			if !ok {
				t.Fatalf("Finalize(%q, %q) produced no usable steer: %+v", tc.tool, tc.command, res)
			}
			if res.ReasonCode != powershellheredoc.ReasonPowerShellHeredoc {
				t.Fatalf("ReasonCode=%q, want %q", res.ReasonCode, powershellheredoc.ReasonPowerShellHeredoc)
			}
			if steer.TurnBudget != toolcall.MinSteerTurnBudget || !steer.AllowAfterReasoning {
				t.Fatalf("steer bounds %+v, want budget 1 and AllowAfterReasoning", steer)
			}
		})
	}
}

func TestFinalizer_InstructionsNeverEmbedCommand(t *testing.T) {
	t.Parallel()
	fin := armedFinalizer(t)
	const secret = "SECRETTOKEN42"
	for _, tc := range []struct{ tool, command string }{
		{"shell_command", secret + ` <<EOF`},
		{"Bash", `C:\` + secret + `\x.exe <<EOF`},
	} {
		res := finalize(t, fin, tc.tool, argsJSON(t, tc.command))
		steer, ok := res.UsableSteer()
		if !ok {
			t.Fatalf("%s: no usable steer", tc.tool)
		}
		if strings.Contains(steer.Instruction, secret) || strings.Contains(steer.Instruction, "<<EOF") {
			t.Fatalf("%s: instruction embeds command bytes: %q", tc.tool, steer.Instruction)
		}
	}
}

func TestFinalizer_PassThroughForUnusableArguments(t *testing.T) {
	t.Parallel()
	fin := armedFinalizer(t)
	cases := []struct {
		name string
		args string
	}{
		{name: "malformed json", args: `{"command":`},
		{name: "array arguments", args: `["cat <<EOF"]`},
		{name: "scalar arguments", args: `"cat <<EOF"`},
		{name: "null arguments", args: `null`},
		{name: "empty arguments", args: ``},
		{name: "missing command key", args: `{"cmd":"cat <<EOF"}`},
		{name: "non-string command", args: `{"command":5}`},
		{name: "null command", args: `{"command":null}`},
		{name: "array command", args: `{"command":["cat <<EOF"]}`},
		{name: "nested command key", args: `{"outer":{"command":"cat <<EOF"}}`},
		{name: "empty command", args: `{"command":""}`},
		{name: "streamed incomplete arguments", args: `{"command":"cat <<EOF`},
		{name: "command over 8 KiB", args: `{"command":"` + strings.Repeat("a", 8192) + ` <<EOF"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res := finalize(t, fin, "shell_command", tc.args)
			if res.Action != toolcall.ActionPass || res.Steer != nil {
				t.Fatalf("args %q = %+v, want pass", tc.args, res)
			}
		})
	}
}

func TestFinalizer_CommandSizeBoundary(t *testing.T) {
	t.Parallel()
	fin := armedFinalizer(t)
	atLimit := "cat <<EOF" + strings.Repeat("a", 8192-len("cat <<EOF"))
	if res := finalize(t, fin, "shell_command", argsJSON(t, atLimit)); res.Steer == nil {
		t.Fatal("8192-byte command was not steered")
	}
	if res := finalize(t, fin, "shell_command", argsJSON(t, atLimit+"a")); res.Action != toolcall.ActionPass {
		t.Fatalf("8193-byte command = %+v, want pass", res)
	}
}
