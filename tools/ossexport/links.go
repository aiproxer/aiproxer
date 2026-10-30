package main

import (
	"errors"
	stdhtml "html"
	"net/url"
	"path"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
	xhtml "golang.org/x/net/html"
)

// errBaseElement reports a document that carries a base element with a
// destination. A browser resolves the relative destinations of such a document
// against that base, and the export does not resolve base URLs, so it refuses
// the document instead of resolving its destinations against the wrong
// directory. The published tree of the repository carries no such document.
var errBaseElement = errors.New("the document carries a base element with a destination, which the export does not resolve")

// linkFormat selects the parser a document uses.
type linkFormat int

const (
	// linkFormatNone marks a file without link syntax.
	linkFormatNone linkFormat = iota
	// linkFormatMarkdown marks a CommonMark document.
	linkFormatMarkdown
	// linkFormatHTML marks an HTML document.
	linkFormatHTML
)

// linkFormatForFile reports the parser a repository file uses.
//
// Only Markdown documents (.md and .markdown) and HTML documents (.html and
// .htm) carry link syntax and are scanned. No other extension is: a path
// string in a JSON, YAML, or shell file is not a link.
func linkFormatForFile(name string) linkFormat {
	switch strings.ToLower(path.Ext(name)) {
	case ".md", ".markdown":
		return linkFormatMarkdown
	case ".html", ".htm":
		return linkFormatHTML
	default:
		return linkFormatNone
	}
}

// linkTargets returns the link destinations of a text document. It reports a
// document that carries a construct the export does not support.
func linkTargets(content string, format linkFormat) ([]string, error) {
	switch format {
	case linkFormatMarkdown:
		return markdownLinkTargets(content, 0)
	case linkFormatHTML:
		return htmlLinkTargets(content)
	default:
		return nil, nil
	}
}

// markdownLinkTargets returns the link destinations of a CommonMark document:
// its links and images, its link reference definitions, and the HTML markup it
// carries.
//
// The parser decides what is code, what is a comment, and what is a literal
// backtick, so a link inside a code block, a code span, or a comment is not a
// link, and a link that follows an unmatched or escaped backtick is a link.
//
// A document that carries a base element with a destination is reported as
// unsupported, because the export does not resolve base URLs.
func markdownLinkTargets(content string, depth int) ([]string, error) {
	source := []byte(content)
	document := goldmark.New().Parser().Parse(text.NewReader(source))
	var targets []string
	var walkErr error
	_ = ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch typed := node.(type) {
		case *ast.Link:
			targets = append(targets, markdownDestination(typed.Destination))
		case *ast.Image:
			targets = append(targets, markdownDestination(typed.Destination))
		case *ast.AutoLink:
			targets = append(targets, string(typed.URL(source)))
		case *ast.LinkReferenceDefinition:
			targets = append(targets, markdownDestination(typed.Destination))
		case *ast.RawHTML:
			markup, err := htmlLinkTargets(string(typed.Segments.Value(source)))
			if err != nil {
				walkErr = err
				return ast.WalkStop, nil
			}
			targets = append(targets, markup...)
		case *ast.HTMLBlock:
			raw := string(typed.Lines().Value(source))
			markup, err := htmlLinkTargets(raw)
			if err != nil {
				walkErr = err
				return ast.WalkStop, nil
			}
			targets = append(targets, markup...)
			rest, err := markdownAfterComment(raw, depth)
			if err != nil {
				walkErr = err
				return ast.WalkStop, nil
			}
			targets = append(targets, rest...)
		}
		return ast.WalkContinue, nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return targets, nil
}

// markdownAfterComment returns the link destinations of the Markdown that
// follows an HTML comment on the same line.
//
// A comment is not a link and does not hide the links around it, so the text
// after the comment is parsed as Markdown even though CommonMark ends an HTML
// block at the line of the comment.
func markdownAfterComment(raw string, depth int) ([]string, error) {
	if depth > 4 {
		return nil, nil
	}
	line := raw
	if before, _, ok := strings.Cut(raw, "\n"); ok {
		line = before
	}
	trimmed := strings.TrimLeft(line, " \t")
	if !strings.HasPrefix(trimmed, "<!--") {
		return nil, nil
	}
	_, after, ok := strings.Cut(trimmed, "-->")
	if !ok {
		return nil, nil
	}
	return markdownLinkTargets(after, depth+1)
}

// markdownDestination decodes the character references and the backslash
// escapes of a Markdown link destination, the way a Markdown renderer does.
func markdownDestination(destination []byte) string {
	return stdhtml.UnescapeString(string(util.UnescapePunctuations(destination)))
}

// htmlLinkTargets returns the href and src destinations of the HTML markup of
// a document. The tokenizer decodes the character references of an attribute
// value, so an attribute that encodes its path is the path it encodes.
//
// A base element with a destination is refused: the export does not resolve
// base URLs, and a document that carries one would have its relative
// destinations resolved against the wrong directory.
func htmlLinkTargets(content string) ([]string, error) {
	var targets []string
	tokenizer := xhtml.NewTokenizer(strings.NewReader(content))
	for {
		switch tokenizer.Next() {
		case xhtml.ErrorToken:
			return targets, nil
		case xhtml.StartTagToken, xhtml.SelfClosingTagToken:
			tag, _ := tokenizer.TagName()
			name := strings.ToLower(string(tag))
			for {
				key, value, more := tokenizer.TagAttr()
				switch strings.ToLower(string(key)) {
				case "href", "src":
					if name == "base" {
						return nil, errBaseElement
					}
					if len(value) > 0 {
						targets = append(targets, string(value))
					}
				}
				if !more {
					break
				}
			}
		}
	}
}

// resolveLinkTarget resolves a link destination against the file that carries
// it. It reports the repository path the link points at, and whether the
// destination addresses the repository at all.
func resolveLinkTarget(sourceFile, raw string) (string, bool) {
	target := strings.TrimSpace(raw)
	if target == "" {
		return "", false
	}
	if strings.HasPrefix(target, "<") && strings.HasSuffix(target, ">") {
		target = strings.TrimSpace(target[1 : len(target)-1])
	}
	if target == "" {
		return "", false
	}
	if strings.HasPrefix(target, "#") {
		return "", false
	}
	if strings.HasPrefix(target, "//") {
		return "", false
	}
	if targetScheme(target) != "" {
		return "", false
	}
	if index := strings.Index(target, "#"); index >= 0 {
		target = target[:index]
	}
	if index := strings.Index(target, "?"); index >= 0 {
		target = target[:index]
	}
	if target == "" {
		return "", false
	}
	if decoded, err := url.PathUnescape(target); err == nil {
		target = decoded
	}
	if after, ok := strings.CutPrefix(target, "/"); ok {
		target = after
	} else {
		target = path.Join(path.Dir(sourceFile), target)
	}
	target = path.Clean(target)
	if target == "." || target == ".." || strings.HasPrefix(target, "../") {
		return "", false
	}
	return target, true
}

// targetScheme returns the URI scheme of a link destination, if it has one.
func targetScheme(target string) string {
	index := strings.Index(target, ":")
	if index <= 0 {
		return ""
	}
	scheme := target[:index]
	for position := 0; position < len(scheme); position++ {
		character := scheme[position]
		switch {
		case character >= 'a' && character <= 'z', character >= 'A' && character <= 'Z':
		case character >= '0' && character <= '9', character == '+', character == '-', character == '.':
			if position == 0 {
				return ""
			}
		default:
			return ""
		}
	}
	return scheme
}
