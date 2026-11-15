package powershellheredoc

// maxNesting bounds the open double-quoted strings and subexpressions. Input
// nested deeper than this is reported as no match.
const maxNesting = 32

type quoteKind uint8

const (
	noQuote quoteKind = iota
	singleQuote
	doubleQuote
)

type frameKind uint8

const (
	// frameDouble is an open "..." string. Its "$(" subexpressions are scanned
	// as code, so a heredoc inside them still matches.
	frameDouble frameKind = iota + 1
	// frameSubexpr is an open $( ... ); depth counts its parentheses.
	frameSubexpr
)

type frame struct {
	kind  frameKind
	depth int
}

// scanHeredoc reports whether cmd contains an unquoted "<<" that starts a
// PowerShell token. PowerShell reserves "<", so such a redirection is a parse
// error whatever follows it. The first match decides the scan.
//
// windowsPathBefore reports whether an unquoted token before the match starts
// with a Windows path prefix (".\", "..\", "X:\" or "\\"). It is false when
// there is no match.
//
// The scan is one left-to-right byte pass. It returns no match at "--%", at an
// unterminated string, here-string, block comment, or subexpression, and when
// nesting exceeds maxNesting. It allocates nothing.
func scanHeredoc(cmd string) (match, windowsPathBefore bool) {
	var stack [maxNesting]frame
	n := 0
	winPath := false
	for i := 0; i < len(cmd); {
		if n > 0 && stack[n-1].kind == frameDouble {
			switch {
			case cmd[i] == '`':
				i += 2
			case cmd[i] == '$' && i+1 < len(cmd) && cmd[i+1] == '(':
				if n == maxNesting {
					return false, false
				}
				stack[n] = frame{kind: frameSubexpr, depth: 1}
				n++
				i += 2
			default:
				kind, w := quoteAt(cmd, i)
				if kind != doubleQuote {
					i++
					continue
				}
				if next, nw := quoteAt(cmd, i+w); next == doubleQuote {
					i += w + nw
					continue
				}
				n--
				i += w
			}
			continue
		}

		atStart := i == 0 || isBoundary(cmd[i-1])
		if atStart && windowsPathPrefix(cmd[i:]) {
			winPath = true
		}
		if kind, w := quoteAt(cmd, i); kind != noQuote {
			if kind == doubleQuote {
				if n == maxNesting {
					return false, false
				}
				stack[n] = frame{kind: frameDouble}
				n++
				i += w
				continue
			}
			end := skipSingleQuoted(cmd, i, w)
			if end < 0 {
				return false, false
			}
			i = end
			continue
		}
		switch cmd[i] {
		case '`':
			i += 2
		case '$':
			if i+1 < len(cmd) && cmd[i+1] == '(' {
				if n == maxNesting {
					return false, false
				}
				stack[n] = frame{kind: frameSubexpr, depth: 1}
				n++
				i += 2
			} else {
				i++
			}
		case '(':
			if n > 0 && stack[n-1].kind == frameSubexpr {
				stack[n-1].depth++
			}
			i++
		case ')':
			if n > 0 && stack[n-1].kind == frameSubexpr {
				stack[n-1].depth--
				if stack[n-1].depth == 0 {
					n--
				}
			}
			i++
		case '<':
			if i+1 < len(cmd) && cmd[i+1] == '<' {
				if atStart {
					return true, winPath
				}
				i += 2
			} else if i+1 < len(cmd) && cmd[i+1] == '#' {
				end := skipBlockComment(cmd, i+2)
				if end < 0 {
					return false, false
				}
				i = end
			} else {
				i++
			}
		case '#':
			if atStart {
				for i < len(cmd) && cmd[i] != '\n' {
					i++
				}
			} else {
				i++
			}
		case '-':
			if i+2 < len(cmd) && cmd[i+1] == '-' && cmd[i+2] == '%' {
				return false, false
			}
			i++
		case '@':
			if kind, w := quoteAt(cmd, i+1); atStart && kind != noQuote {
				end := skipHereString(cmd, i+1, kind, w)
				if end < 0 {
					return false, false
				}
				i = end
			} else {
				i++
			}
		default:
			i++
		}
	}
	return false, false
}

// quoteAt reports the quote kind of the character at byte i and its width in
// bytes. PowerShell accepts the typographic quotes as ASCII quotes too.
func quoteAt(s string, i int) (quoteKind, int) {
	if i >= len(s) {
		return noQuote, 0
	}
	switch s[i] {
	case '\'':
		return singleQuote, 1
	case '"':
		return doubleQuote, 1
	case 0xE2:
		if i+2 < len(s) && s[i+1] == 0x80 {
			switch s[i+2] {
			case 0x98, 0x99, 0x9A, 0x9B:
				return singleQuote, 3
			case 0x9C, 0x9D, 0x9E:
				return doubleQuote, 3
			}
		}
	}
	return noQuote, 0
}

// skipSingleQuoted returns the index just past the single-quoted string whose
// opening quote of width w starts at i, or -1 when it is unterminated. A
// doubled quote is an escaped quote inside the string.
func skipSingleQuoted(s string, i, w int) int {
	j := i + w
	for j < len(s) {
		kind, cw := quoteAt(s, j)
		if kind != singleQuote {
			j++
			continue
		}
		if next, nw := quoteAt(s, j+cw); next == singleQuote {
			j += cw + nw
			continue
		}
		return j + cw
	}
	return -1
}

// skipHereString returns the index just past the here-string whose opening
// quote (of kind and width w) is at quote, or -1 when no line-initial closing
// quote followed by "@" exists.
func skipHereString(s string, quote int, kind quoteKind, w int) int {
	for j := quote + w; j < len(s); j++ {
		if s[j] != '\n' {
			continue
		}
		if k, cw := quoteAt(s, j+1); k == kind && cw > 0 && j+1+cw < len(s) && s[j+1+cw] == '@' {
			return j + 1 + cw + 1
		}
	}
	return -1
}

// skipBlockComment returns the index just past the first "#>" at or after from,
// or -1 when the block comment is unterminated.
func skipBlockComment(s string, from int) int {
	for j := from; j+1 < len(s); j++ {
		if s[j] == '#' && s[j+1] == '>' {
			return j + 2
		}
	}
	return -1
}

// isBoundary reports whether b can end the previous token, so that the next
// byte starts a new one.
func isBoundary(b byte) bool {
	switch b {
	case ' ', '\t', '\r', '\n', ';', '|', '&', '(', ')', '{', '}':
		return true
	}
	return false
}

// windowsPathPrefix reports whether s starts with a Windows path prefix.
func windowsPathPrefix(s string) bool {
	switch {
	case len(s) >= 2 && s[0] == '.' && s[1] == '\\':
		return true
	case len(s) >= 3 && s[0] == '.' && s[1] == '.' && s[2] == '\\':
		return true
	case len(s) >= 3 && isASCIILetter(s[0]) && s[1] == ':' && s[2] == '\\':
		return true
	case len(s) >= 2 && s[0] == '\\' && s[1] == '\\':
		return true
	}
	return false
}

func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}
