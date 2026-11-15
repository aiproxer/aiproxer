package powershellheredoc

import (
	"strings"
	"testing"
)

// TestScanHeredoc_ParserTable pins every row of the PowerShell-parser table in
// issue #831: rows that PowerShell rejects must match, rows it accepts must not.
func TestScanHeredoc_ParserTable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		cmd   string
		match bool
	}{
		// PowerShell rejects these: a token-start unquoted "<<".
		{name: "dot-venv heredoc", cmd: `.\.venv\Scripts\python.exe - <<'PY'`, match: true},
		{name: "dash heredoc", cmd: `python - <<PY`, match: true},
		{name: "bare heredoc arg", cmd: `python <<'PY'`, match: true},
		{name: "dash tab heredoc", cmd: `py - <<-EOF`, match: true},
		{name: "cat heredoc", cmd: `cat <<EOF`, match: true},
		{name: "heredoc then redirect", cmd: `cat <<EOF > out.txt`, match: true},
		{name: "redirect then heredoc", cmd: `cat > out.txt <<'EOF'`, match: true},
		{name: "semicolon chain", cmd: `cd x; python - <<PY`, match: true},
		{name: "and chain", cmd: `cd x && python - <<'PY'`, match: true},
		{name: "heredoc piped", cmd: `python - <<'PY' | Out-File log.txt`, match: true},
		{name: "git commit file from stdin", cmd: `git commit -F - <<EOF`, match: true},
		{name: "here-string redirect", cmd: `grep foo <<< "$x"`, match: true},
		{name: "subshell heredoc", cmd: `(cat)<<EOF`, match: true},
		{name: "heredoc on line two", cmd: "echo ok\npython - <<'PY'\nprint(1)\nPY", match: true},
		{name: "heredoc inside subexpression", cmd: `Write-Host "a$(cat <<EOF)"`, match: true},
		{name: "heredoc at input start", cmd: `<<EOF`, match: true},

		// PowerShell accepts these: "<<" is glued to a word, quoted, commented,
		// escaped, or inside a here-string or block comment.
		{name: "glued dash", cmd: `python -<<PY`, match: false},
		{name: "glued word", cmd: `cat<<EOF`, match: false},
		{name: "glued argument", cmd: `python -c x<<EOF`, match: false},
		{name: "shift inside double quotes", cmd: `python -c "print(1 << 2)"`, match: false},
		{name: "single quoted literal", cmd: `Write-Output 'python - <<PY'`, match: false},
		{name: "bash -c single quoted", cmd: `bash -c 'python - <<PY'`, match: false},
		{name: "wsl bash double quoted", cmd: `wsl bash -lc "cat <<EOF"`, match: false},
		{name: "here-string body", cmd: "Write-Output @\"\n<<EOF\n\"@", match: false},
		{name: "block comment", cmd: `<# <<EOF #>`, match: false},
		{name: "line comment", cmd: `# cat <<EOF`, match: false},
		{name: "stop-parsing token", cmd: `python --% - <<PY`, match: false},

		// Unterminated constructs before the "<<" leave it inside them.
		{name: "unterminated single quote", cmd: `echo 'python - <<PY`, match: false},
		{name: "unterminated double quote", cmd: `echo "python <<PY`, match: false},
		{name: "unterminated here-string", cmd: "echo @'\npython <<PY", match: false},
		{name: "unterminated block comment", cmd: `<# python <<PY`, match: false},
		{name: "unterminated subexpression", cmd: `echo "a$(cat`, match: false},

		// Escapes and quoting rules that keep the "<<" inside a string or literal.
		{name: "backtick escaped redirect", cmd: "python `<<PY", match: false},
		{name: "doubled single quote keeps string open", cmd: `Write-Output 'it''s <<PY'`, match: false},
		{name: "backtick escaped quote keeps string open", cmd: "echo \"a`\"b <<PY\"", match: false},
		{name: "doubled double quote keeps string open", cmd: `echo "a""b <<PY"`, match: false},
		{name: "smart single quotes are a string", cmd: "Write-Output \u2018python - <<PY\u2019", match: false},
		{name: "smart double quotes are a string", cmd: "Write-Output \u201cpython - <<PY\u201d", match: false},
		{name: "subexpression closes before redirect", cmd: `Write-Host "$("x")<<EOF"`, match: false},
		{name: "empty input", cmd: ``, match: false},
		{name: "no shift at all", cmd: `python -c "print(1)"`, match: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			match, _ := scanHeredoc(tc.cmd)
			if match != tc.match {
				t.Fatalf("scanHeredoc(%q) match=%v, want %v", tc.cmd, match, tc.match)
			}
		})
	}
}

// TestScanHeredoc_WindowsPathBefore pins the auto-arming signal: an unquoted
// token before the matched "<<" that starts with a Windows path prefix.
func TestScanHeredoc_WindowsPathBefore(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cmd  string
		want bool
	}{
		{name: "dot-venv path", cmd: `.\.venv\Scripts\python.exe - <<'PY'`, want: true},
		{name: "parent path", cmd: `..\tools\x.exe <<EOF`, want: true},
		{name: "drive path", cmd: `C:\Python\python.exe - <<PY`, want: true},
		{name: "lowercase drive path", cmd: `c:\x <<EOF`, want: true},
		{name: "UNC path", cmd: `\\server\share\x.exe <<EOF`, want: true},
		{name: "path after command word", cmd: `python .\x.py <<EOF`, want: true},
		{name: "path after separator", cmd: `cd x; C:\x\run.exe <<EOF`, want: true},
		{name: "single quoted path", cmd: `'C:\Python\python.exe' - <<PY`, want: false},
		{name: "double quoted path", cmd: `"C:\Python\python.exe" - <<PY`, want: false},
		{name: "forward slash drive path", cmd: `C:/Python/python.exe - <<PY`, want: false},
		{name: "path only after heredoc", cmd: `python - <<'PY' C:\x`, want: false},
		{name: "plain word before heredoc", cmd: `python - <<'PY'`, want: false},
		{name: "letters before colon are not a drive", cmd: `python foo:\bar <<EOF`, want: false},
		{name: "path not at token start", cmd: `python x.\y <<EOF`, want: false},
		{name: "path inside quotes after token start", cmd: `python "C:\x" <<EOF`, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			match, winPath := scanHeredoc(tc.cmd)
			if !match {
				t.Fatalf("scanHeredoc(%q) match=false, want a heredoc match", tc.cmd)
			}
			if winPath != tc.want {
				t.Fatalf("scanHeredoc(%q) windowsPathBefore=%v, want %v", tc.cmd, winPath, tc.want)
			}
		})
	}
}

// TestScanHeredoc_NoAllocations keeps the scanner on the stack: the feature runs
// on every completed call of an armed tool.
func TestScanHeredoc_NoAllocations(t *testing.T) {
	cmd := `Write-Host "a$(cat <<EOF)"; .\.venv\Scripts\python.exe - <<'PY'`
	allocs := testing.AllocsPerRun(100, func() {
		scanHeredoc(cmd)
	})
	if allocs != 0 {
		t.Fatalf("scanHeredoc allocates %v times per call, want 0", allocs)
	}
}

// FuzzScanHeredoc checks that arbitrary input never panics and that the scanner
// reports a match only for input that contains "<<", and windowsPathBefore only
// alongside a match.
func FuzzScanHeredoc(f *testing.F) {
	for _, seed := range []string{
		`.\.venv\Scripts\python.exe - <<'PY'`,
		`python - <<PY`,
		`Write-Host "a$(cat <<EOF)"`,
		"@'\n<<EOF\n'@",
		`<# <<EOF #>`,
		`'it''s <<PY'`,
		"python `<<PY",
		`echo "a$("b")"`,
		`--% <<`,
		"\u201cpython <<\u201d",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, cmd string) {
		match, winPath := scanHeredoc(cmd)
		if match && !strings.Contains(cmd, "<<") {
			t.Fatalf("match without \"<<\" in %q", cmd)
		}
		if !match && winPath {
			t.Fatalf("windowsPathBefore without a match in %q", cmd)
		}
	})
}
