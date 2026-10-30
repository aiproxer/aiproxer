package dbparity_test

import (
	"flag"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/dbparity"
)

func TestParseFlagWords_Success(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "empty input string returns nil",
			input:    "",
			expected: nil,
		},
		{
			name:     "whitespace only returns nil",
			input:    "   \t\r\n  ",
			expected: nil,
		},
		{
			name:     "standard flags without quotes",
			input:    "-timeout=10m -parallel=8 -count=1",
			expected: []string{"-timeout=10m", "-parallel=8", "-count=1"},
		},
		{
			name:     "double quoted flag value with spaces",
			input:    `-run "Test Name" -count=1`,
			expected: []string{"-run", "Test Name", "-count=1"},
		},
		{
			name:     "single quoted flag value with spaces",
			input:    `-run 'Test Name'`,
			expected: []string{"-run", "Test Name"},
		},
		{
			name:     "adjacent single and unquoted segments",
			input:    `-literal=a'b'`,
			expected: []string{"-literal=ab"},
		},
		{
			name:     "adjacent double and unquoted segments",
			input:    `-run="Test Name"`,
			expected: []string{"-run=Test Name"},
		},
		{
			name:     "multiple adjacent quoted segments",
			input:    `'a'"b"'c'`,
			expected: []string{"abc"},
		},
		{
			name:     "prefix and suffix surrounding quotes",
			input:    `prefix"mid"suffix`,
			expected: []string{"prefixmidsuffix"},
		},
		{
			name:     "empty double quoted word",
			input:    `""`,
			expected: []string{""},
		},
		{
			name:     "empty single quoted word",
			input:    `''`,
			expected: []string{""},
		},
		{
			name:     "multiple empty quoted words separated by space",
			input:    `"" ''`,
			expected: []string{"", ""},
		},
		{
			name:     "unquoted word containing empty quote",
			input:    `a""b`,
			expected: []string{"ab"},
		},
		{
			name:     "flag with empty quoted value",
			input:    `-flag=""`,
			expected: []string{"-flag="},
		},
		{
			name:     "UNC network path preserves double leading backslashes",
			input:    `\\server\share\subfolder`,
			expected: []string{`\\server\share\subfolder`},
		},
		{
			name:     "Windows drive root preserves trailing backslash without error",
			input:    `C:\`,
			expected: []string{`C:\`},
		},
		{
			name:     "Windows path outside quotes",
			input:    `-dir=C:\Users\Mateusz\source\repos`,
			expected: []string{`-dir=C:\Users\Mateusz\source\repos`},
		},
		{
			name:     "quoted path ending separator using single quotes",
			input:    `-dir='C:\path\'`,
			expected: []string{`-dir=C:\path\`},
		},
		{
			name:     "quoted path ending separator using double quotes",
			input:    `-dir="C:\path\"`,
			expected: []string{`-dir=C:\path\`},
		},
		{
			name:     "regex with backslashes inside single quotes",
			input:    `-run='^Test\d+\s+$'`,
			expected: []string{`-run=^Test\d+\s+$`},
		},
		{
			name:     "regex with backslashes inside double quotes",
			input:    `-run="^Test\d+\s+$"`,
			expected: []string{`-run=^Test\d+\s+$`},
		},
		{
			name:     "regex flags separated by space",
			input:    `-run '\s+' -count=1`,
			expected: []string{"-run", `\s+`, "-count=1"},
		},
		{
			name:     "single quote inside double quotes (switch quote types)",
			input:    `-msg="it's fine"`,
			expected: []string{`-msg=it's fine`},
		},
		{
			name:     "double quote inside single quotes (switch quote types)",
			input:    `-msg='he said "hello"'`,
			expected: []string{`-msg=he said "hello"`},
		},
		{
			name:     "literal backslashes outside quotes",
			input:    `foo\\bar foo\bar -flag=\`,
			expected: []string{`foo\\bar`, `foo\bar`, `-flag=\`},
		},
		{
			name:     "command substitution syntax is treated as literal",
			input:    `$(echo evil) ` + "`date`",
			expected: []string{"$(echo", "evil)", "`date`"},
		},
		{
			name:     "environment variable syntax is treated as literal",
			input:    `$FOO ${BAR} "$BAZ"`,
			expected: []string{"$FOO", "${BAR}", "$BAZ"},
		},
		{
			name:     "extra whitespace between words is collapsed",
			input:    "   -a   -b    -c   ",
			expected: []string{"-a", "-b", "-c"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := dbparity.ParseFlagWords(tc.input)
			if err != nil {
				t.Fatalf("ParseFlagWords(%q) returned unexpected error: %v", tc.input, err)
			}
			if !reflect.DeepEqual(got, tc.expected) {
				t.Errorf("ParseFlagWords(%q)\ngot:  %#v\nwant: %#v", tc.input, got, tc.expected)
			}
		})
	}
}

func TestParseFlagWords_Errors(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		errorContains string
	}{
		{
			name:          "unclosed single quote",
			input:         `-run 'Test Name`,
			errorContains: "unclosed single quote",
		},
		{
			name:          "unclosed double quote",
			input:         `-run "Test Name`,
			errorContains: "unclosed double quote",
		},
		{
			name:          "unclosed single quote inside word",
			input:         `-flag=prefix'suffix`,
			errorContains: "unclosed single quote",
		},
		{
			name:          "unclosed double quote inside word",
			input:         `-flag=prefix"suffix`,
			errorContains: "unclosed double quote",
		},
		{
			name:          "unclosed quote with trailing path separator",
			input:         `-dir="C:\path\`,
			errorContains: "unclosed double quote",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := dbparity.ParseFlagWords(tc.input)
			if err == nil {
				t.Fatalf("ParseFlagWords(%q) expected error, got: %#v", tc.input, got)
			}
			if !strings.Contains(err.Error(), tc.errorContains) {
				t.Errorf("ParseFlagWords(%q) error %q does not contain %q", tc.input, err.Error(), tc.errorContains)
			}
		})
	}
}

func TestReorderCLIArgs_Success(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		expected []string
	}{
		{
			name:     "empty arguments returns empty",
			input:    []string{},
			expected: []string{},
		},
		{
			name:     "positional mode only without flags",
			input:    []string{"sqlite"},
			expected: []string{"sqlite"},
		},
		{
			name:     "standard flags before positional mode",
			input:    []string{"-component", "billing", "sqlite"},
			expected: []string{"-component", "billing", "sqlite"},
		},
		{
			name:     "spaced value flag after positional mode",
			input:    []string{"sqlite", "-component", "billing"},
			expected: []string{"-component", "billing", "sqlite"},
		},
		{
			name:     "double-dash spaced flag after positional mode",
			input:    []string{"sqlite", "--component", "billing"},
			expected: []string{"--component", "billing", "sqlite"},
		},
		{
			name:     "equals form flag after positional mode",
			input:    []string{"sqlite", "--component=billing"},
			expected: []string{"--component=billing", "sqlite"},
		},
		{
			name:     "alias flag -only after positional mode",
			input:    []string{"sqlite", "-only", "billing"},
			expected: []string{"-only", "billing", "sqlite"},
		},
		{
			name:     "alias flag --only=billing after positional mode",
			input:    []string{"sqlite", "--only=billing"},
			expected: []string{"--only=billing", "sqlite"},
		},
		{
			name:     "boolean flag -json after positional mode",
			input:    []string{"list", "-json"},
			expected: []string{"-json", "list"},
		},
		{
			name:     "boolean flag --json after positional mode",
			input:    []string{"list", "--json"},
			expected: []string{"--json", "list"},
		},
		{
			name:     "boolean flag with explicit equals value",
			input:    []string{"list", "-json=true"},
			expected: []string{"-json=true", "list"},
		},
		{
			name:     "-format flag after positional mode",
			input:    []string{"list", "-format", "json"},
			expected: []string{"-format", "json", "list"},
		},
		{
			name:     "--format=json flag after positional mode",
			input:    []string{"list", "--format=json"},
			expected: []string{"--format=json", "list"},
		},
		{
			name:     "-list-format flag alias after positional mode",
			input:    []string{"list", "-list-format", "json"},
			expected: []string{"-list-format", "json", "list"},
		},
		{
			name:     "-mode flag with value",
			input:    []string{"-mode", "sqlite"},
			expected: []string{"-mode", "sqlite"},
		},
		{
			name:     "-flags with multi-word string value",
			input:    []string{"sqlite", "-flags", "-count=1 -parallel=4"},
			expected: []string{"-flags", "-count=1 -parallel=4", "sqlite"},
		},
		{
			name:     "-flags with value starting with dash",
			input:    []string{"sqlite", "-flags", "-count=1"},
			expected: []string{"-flags", "-count=1", "sqlite"},
		},
		{
			name:     "--flags=-count=1 equals form",
			input:    []string{"sqlite", "--flags=-count=1"},
			expected: []string{"--flags=-count=1", "sqlite"},
		},
		{
			name:     "multiple trailing flags reordered before positional mode",
			input:    []string{"sqlite", "-component", "billing", "-flags", "-count=1", "-json"},
			expected: []string{"-component", "billing", "-flags", "-count=1", "-json", "sqlite"},
		},
		{
			name:     "double-dash argument terminator preserves remainder as positional",
			input:    []string{"sqlite", "--", "-not-a-flag"},
			expected: []string{"--", "sqlite", "-not-a-flag"},
		},
		{
			name:     "flags before and after mode with delimiter",
			input:    []string{"sqlite", "-component", "billing", "--", "-dash-arg"},
			expected: []string{"-component", "billing", "--", "sqlite", "-dash-arg"},
		},
		{
			name:     "delimiter at beginning followed by positional",
			input:    []string{"--", "sqlite"},
			expected: []string{"--", "sqlite"},
		},
		{
			name:     "delimiter at end of flags",
			input:    []string{"-component", "billing", "--"},
			expected: []string{"-component", "billing", "--"},
		},
		{
			name:     "complex flags mode and multiple post-delimiter args",
			input:    []string{"-component", "billing", "sqlite", "-flags", "-v", "--", "-not-a-flag", "extra"},
			expected: []string{"-component", "billing", "-flags", "-v", "--", "sqlite", "-not-a-flag", "extra"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := dbparity.ReorderCLIArgs(tc.input)
			if err != nil {
				t.Fatalf("ReorderCLIArgs(%#v) unexpected error: %v", tc.input, err)
			}
			if !reflect.DeepEqual(got, tc.expected) {
				t.Errorf("ReorderCLIArgs(%#v)\ngot:  %#v\nwant: %#v", tc.input, got, tc.expected)
			}
		})
	}
}

func TestReorderCLIArgs_FlagParserCompatibility(t *testing.T) {
	// Ensure standard flag.FlagSet does not reinterpret dash args placed after --
	args := []string{"sqlite", "-component", "billing", "--", "-not-a-flag", "-unknown"}
	reordered, err := dbparity.ReorderCLIArgs(args)
	if err != nil {
		t.Fatalf("ReorderCLIArgs failed: %v", err)
	}

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	var component string
	fs.StringVar(&component, "component", "", "")

	if err := fs.Parse(reordered); err != nil {
		t.Fatalf("fs.Parse failed: %v", err)
	}

	if component != "billing" {
		t.Errorf("component = %q, want billing", component)
	}
	if fs.NArg() != 3 {
		t.Fatalf("expected 3 positional args, got %d: %v", fs.NArg(), fs.Args())
	}
	if fs.Arg(0) != "sqlite" || fs.Arg(1) != "-not-a-flag" || fs.Arg(2) != "-unknown" {
		t.Errorf("unexpected positional args: %v", fs.Args())
	}
}

func TestReorderCLIArgs_Errors(t *testing.T) {
	tests := []struct {
		name          string
		input         []string
		errorContains string
	}{
		{
			name:          "missing value for -component at end",
			input:         []string{"sqlite", "-component"},
			errorContains: "flag needs an argument: -component",
		},
		{
			name:          "missing value for --component at end",
			input:         []string{"sqlite", "--component"},
			errorContains: "flag needs an argument: --component",
		},
		{
			name:          "missing value for -component followed by another flag",
			input:         []string{"sqlite", "-component", "-json"},
			errorContains: "flag needs an argument: -component",
		},
		{
			name:          "missing value for -format at end",
			input:         []string{"list", "-format"},
			errorContains: "flag needs an argument: -format",
		},
		{
			name:          "missing value for -list-format at end",
			input:         []string{"list", "-list-format"},
			errorContains: "flag needs an argument: -list-format",
		},
		{
			name:          "missing value for -only at end",
			input:         []string{"sqlite", "-only"},
			errorContains: "flag needs an argument: -only",
		},
		{
			name:          "missing value for -flags at end",
			input:         []string{"sqlite", "-flags"},
			errorContains: "flag needs an argument: -flags",
		},
		{
			name:          "missing value for -mode at end",
			input:         []string{"-mode"},
			errorContains: "flag needs an argument: -mode",
		},
		{
			name:          "unknown single dash flag",
			input:         []string{"sqlite", "-unknown"},
			errorContains: "flag provided but not defined: -unknown",
		},
		{
			name:          "unknown double dash flag",
			input:         []string{"sqlite", "--bogus"},
			errorContains: "flag provided but not defined: --bogus",
		},
		{
			name:          "unknown equals flag",
			input:         []string{"sqlite", "-unknown=val"},
			errorContains: "flag provided but not defined: -unknown=val",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := dbparity.ReorderCLIArgs(tc.input)
			if err == nil {
				t.Fatalf("ReorderCLIArgs(%#v) expected error, got: %#v", tc.input, got)
			}
			if !strings.Contains(err.Error(), tc.errorContains) {
				t.Errorf("ReorderCLIArgs(%#v) error %q does not contain %q", tc.input, err.Error(), tc.errorContains)
			}
		})
	}
}
