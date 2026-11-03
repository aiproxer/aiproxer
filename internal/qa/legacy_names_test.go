package qa

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The former product names are assembled from fragments so that this file
// never contains a retired spelling itself.
var (
	formerAbbr     = "l" + "ip"
	formerGoAbbr   = "go" + formerAbbr
	formerFullName = regexp.MustCompile(`(?i)llm[ _-]?inter` + `active[ _-]?proxy`)
	alnumRun       = regexp.MustCompile(`[A-Za-z0-9]+`)
	unicodeEscape  = regexp.MustCompile(`\\u00([0-7][0-9a-fA-F])`)

	// englishWithAbbr holds ordinary and third-party words that contain the
	// former abbreviation's letters. Matching is on lower-cased runs.
	englishWithAbbr = regexp.MustCompile(`c` + formerAbbr + `|f` + formerAbbr + `|s` + formerAbbr + `|b` + formerAbbr +
		`|el` + formerAbbr + `s|ec` + formerAbbr + `s|mil` + formerAbbr + `rec|fi` + formerAbbr + `po|` +
		formerAbbr + `id|` + formerAbbr + `stick|` + formerAbbr + `s\b`)
)

// legacyArchivePrefix is the immutable spec archive, which keeps its
// historical names.
const legacyArchivePrefix = ".kiro/specs/archive/"

// TestLegacyNames_MaintainedTreeUsesOnlyAIProxerNames fails when a tracked
// path or text file outside the spec archive carries a former product name.
// go.sum files are skipped: they hold third-party checksums only.
func TestLegacyNames_MaintainedTreeUsesOnlyAIProxerNames(t *testing.T) {
	root := repoRoot(t)
	out, err := exec.Command("git", "-C", root, "ls-files", "-z").Output()
	if err != nil {
		t.Skipf("git ls-files unavailable: %v", err)
	}
	var hits []string
	for name := range strings.SplitSeq(strings.TrimRight(string(out), "\x00"), "\x00") {
		if name == "" || strings.HasPrefix(name, legacyArchivePrefix) || filepath.Base(name) == "go.sum" {
			continue
		}
		if legacyNameIn(name) {
			hits = append(hits, name+": path")
		}
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			continue // deleted in the working tree
		}
		if bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
			continue
		}
		for i, line := range strings.Split(string(data), "\n") {
			if legacyNameIn(line) {
				hits = append(hits, name+":"+strconv.Itoa(i+1))
			}
		}
	}
	if len(hits) > 0 {
		if len(hits) > 50 {
			hits = append(hits[:50], "...")
		}
		t.Fatalf("former product names found (%d); use AIProxer/aiproxer/aip names:\n%s", len(hits), strings.Join(hits, "\n"))
	}
}

func TestLegacyNames_DetectsEveryFamilyButNotEnglishWords(t *testing.T) {
	upper, title := strings.ToUpper(formerAbbr), strings.ToUpper(formerAbbr[:1])+formerAbbr[1:]
	mixed := strings.ToUpper(formerAbbr[:1]) + formerAbbr[1:2] + strings.ToUpper(formerAbbr[2:])
	for _, s := range []string{
		"pkg/" + formerAbbr + "api",
		upper + "_TOKEN",
		"X-" + upper + "-Session",
		"from" + title + "sdk",
		"my" + upper + "sdk",
		"/.__" + mixed + "_V1__/",
		"g" + strings.ToUpper(formerGoAbbr[1:2]) + formerAbbr,
		"." + formerGoAbbr + "-plugins",
		`/.` + `\u005f\u005f` + `\u006c` + `\u0069` + `\u0070` + `_`,
		`_` + formerAbbr + `_v1`,
		"LLM Inter" + "active Proxy",
		"the " + upper + " proxy",
	} {
		if !legacyNameIn(s) {
			t.Errorf("legacyNameIn(%q) = false, want true", s)
		}
	}
	for _, s := range []string{
		"clip", "slices.Clip", "flipped", "ZipSlip", "ellipsis", "eclipse", "milliprecision", "filippo.io",
		"CLIPostOutputPolicy", "isInternalIP", "cliPID", formerAbbr + "id", formerAbbr + "stick", formerAbbr + "s",
		"aipapi", "interactive-proxy",
	} {
		if legacyNameIn(s) {
			t.Errorf("legacyNameIn(%q) = true, want false", s)
		}
	}
}

// legacyNameIn reports whether s holds a former product name in any case,
// separator or \u00XX-escaped form.
func legacyNameIn(s string) bool {
	s = unicodeEscape.ReplaceAllStringFunc(s, func(m string) string {
		v, _ := strconv.ParseUint(m[4:], 16, 8)
		return string(rune(v))
	})
	if formerFullName.MatchString(s) {
		return true
	}
	if !strings.Contains(strings.ToLower(s), formerAbbr) {
		return false
	}
	return slices.ContainsFunc(alnumRun.FindAllString(s, -1), legacyRun)
}

// legacyRun reports whether one alphanumeric run carries the abbreviation as a
// name: as the start of a camel-case segment ("isXyzapi" style compounds), or
// in any casing outside English words and camel-case boundaries such as
// "internalIP".
func legacyRun(run string) bool {
	for _, seg := range camelSegments(run) {
		low := strings.ToLower(seg)
		if englishWithAbbr.MatchString(low) {
			continue
		}
		if strings.HasPrefix(low, formerAbbr) || strings.HasPrefix(low, formerGoAbbr) {
			return true
		}
	}
	lower := strings.ToLower(run)
	masked := englishWithAbbr.ReplaceAllStringFunc(lower, func(m string) string { return strings.Repeat("_", len(m)) })
	for i := 0; ; {
		j := strings.Index(masked[i:], formerAbbr)
		if j < 0 {
			return false
		}
		at := i + j
		boundary := at > 0 && run[at] == 'l' && run[at-1] >= 'a' && run[at-1] <= 'z' && run[at+1] == 'I'
		if !boundary {
			return true
		}
		i = at + 1
	}
}

// camelSegments splits an alphanumeric run: "CLIPost" -> CLI, Post.
func camelSegments(run string) []string {
	isUpper := func(c byte) bool { return c >= 'A' && c <= 'Z' }
	isLower := func(c byte) bool { return c >= 'a' && c <= 'z' }
	isDigit := func(c byte) bool { return c >= '0' && c <= '9' }
	var segs []string
	for i := 0; i < len(run); {
		j := i
		switch {
		case isDigit(run[i]):
			for j < len(run) && isDigit(run[j]) {
				j++
			}
		case isUpper(run[i]):
			for j < len(run) && isUpper(run[j]) {
				j++
			}
			if j-i == 1 {
				for j < len(run) && isLower(run[j]) {
					j++
				}
			} else if j < len(run) && isLower(run[j]) {
				j--
			}
		default:
			for j < len(run) && isLower(run[j]) {
				j++
			}
		}
		segs = append(segs, run[i:j])
		i = j
	}
	return segs
}
