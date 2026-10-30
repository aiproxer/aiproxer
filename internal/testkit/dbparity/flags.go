package dbparity

import (
	"fmt"
	"strings"
	"unicode"
)

// ParseFlagWords parses a flag string (such as GO_TEST_FLAGS or a CLI -flags argument)
// into individual argument words using a deliberately minimal, predictable word format.
//
// Format rules:
//   - Whitespace (spaces, tabs, newlines) outside quotes separates words.
//   - Single ('...') and double ("...") quotes group characters into words and are stripped.
//   - Adjacent quoted and unquoted segments concatenate into a single word
//     (e.g., -run="^$" -> -run=^$, -literal=a'b' -> -literal=ab, prefix"mid"suffix -> prefixmidsuffix).
//   - Backslashes ('\') are ALWAYS literal characters (inside and outside quotes), preserving
//     UNC paths (\\server\share), drive roots (C:\), trailing separators (C:\path\), and
//     regular expression escapes (\d+, \s+) without escaping or stripping.
//   - Quote characters cannot be escaped inside their own quote type; users switch quote types
//     to embed the other quote delimiter (e.g., 'he said "hello"' or "it's fine").
//   - Unmatched (unclosed) single or double quotes are rejected with an error.
func ParseFlagWords(s string) ([]string, error) {
	var words []string
	var buf strings.Builder
	inWord := false
	inSingle := false
	inDouble := false

	for _, r := range s {
		if inSingle {
			if r == '\'' {
				inSingle = false
			} else {
				buf.WriteRune(r)
			}
			continue
		}

		if inDouble {
			if r == '"' {
				inDouble = false
			} else {
				buf.WriteRune(r)
			}
			continue
		}

		if unicode.IsSpace(r) {
			if inWord {
				words = append(words, buf.String())
				buf.Reset()
				inWord = false
			}
			continue
		}

		inWord = true

		switch r {
		case '\'':
			inSingle = true
		case '"':
			inDouble = true
		default:
			buf.WriteRune(r)
		}
	}

	if inSingle {
		return nil, fmt.Errorf("unclosed single quote in %q", s)
	}
	if inDouble {
		return nil, fmt.Errorf("unclosed double quote in %q", s)
	}
	if inWord {
		words = append(words, buf.String())
	}

	return words, nil
}

var (
	knownValueFlags = map[string]bool{
		"mode":        true,
		"component":   true,
		"only":        true,
		"format":      true,
		"list-format": true,
		"flags":       true,
	}
	knownBoolFlags = map[string]bool{
		"json": true,
		"h":    true,
		"help": true,
	}
)

// ReorderCLIArgs reorders CLI arguments so that all flag options (including trailing flags
// specified after a positional mode argument) precede any positional arguments.
//
// It validates that all flags are recognized documented flags, verifies that value flags
// are supplied with an argument, supports both -flag and --flag variants as well as
// equals forms (-flag=value and --flag=value), and respects the double-dash (--) delimiter.
//
// If an unknown flag or missing flag value is encountered, an actionable error is returned.
func ReorderCLIArgs(args []string) ([]string, error) {
	var flags []string
	var prePositional []string
	var postDelimiter []string
	hasDelimiter := false

	for i := 0; i < len(args); i++ {
		arg := args[i]

		if arg == "--" {
			hasDelimiter = true
			postDelimiter = args[i+1:]
			break
		}

		if !strings.HasPrefix(arg, "-") || arg == "-" {
			prePositional = append(prePositional, arg)
			continue
		}

		flagBody := strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-")

		if strings.Contains(flagBody, "=") {
			name, _, _ := strings.Cut(flagBody, "=")
			if !knownValueFlags[name] && !knownBoolFlags[name] {
				return nil, fmt.Errorf("flag provided but not defined: %s", arg)
			}
			flags = append(flags, arg)
			continue
		}

		if knownBoolFlags[flagBody] {
			flags = append(flags, arg)
			continue
		}

		if knownValueFlags[flagBody] {
			if i+1 >= len(args) {
				return nil, fmt.Errorf("flag needs an argument: %s", arg)
			}
			nextArg := args[i+1]
			if flagBody != "flags" && strings.HasPrefix(nextArg, "-") && nextArg != "-" {
				return nil, fmt.Errorf("flag needs an argument: %s", arg)
			}
			flags = append(flags, arg, nextArg)
			i++
			continue
		}

		return nil, fmt.Errorf("flag provided but not defined: %s", arg)
	}

	res := make([]string, 0, len(flags)+len(prePositional)+len(postDelimiter)+1)
	res = append(res, flags...)
	if hasDelimiter {
		res = append(res, "--")
	}
	res = append(res, prePositional...)
	res = append(res, postDelimiter...)
	return res, nil
}
