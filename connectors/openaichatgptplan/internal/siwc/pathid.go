package siwc

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// The path-identity contract of this package.
//
// A configured record path is an operator's spelling of a location, not the
// identity of a file. The spelling may contain symlinks and ".." components,
// and the operating system resolves it component by component: it follows a
// symlink first and applies the ".." afterwards, while a lexical cleanup
// would apply the ".." first. The two can name different directories, so a
// store that derives the record's parent lexically can protect and confirm
// a directory that does not hold the record.
//
// The contract is therefore: every operation resolves the configured
// spelling to one physical location and works on that location. The
// resolved directory holds no symlink component, so its lexical ancestors
// are its physical ancestors, and the durability chain that walks up from it
// covers the directories the operating system will really use.
//
// The contract itself - what the configured and the resolved path are, when
// each accessor resolves, and what the store guarantees about the record's
// parent - is stated in the package documentation. This file implements it.

// recordLocation is the resolved, physical identity of the host record.
//
// dir is the physical directory the operating system will use for the
// record. It holds no symlink component, so its lexical ancestors are its
// physical ancestors. name is the record's file name inside that
// directory. configured and rawParent keep the operator's spelling: for
// diagnostics, and for the cross-check that the resolution agrees with the
// operating system.
type recordLocation struct {
	configured string
	rawParent  string
	dir        string
	name       string
}

// path returns the physical path of the record.
func (l recordLocation) path() string { return filepath.Join(l.dir, l.name) }

// locate resolves the configured record path to one physical identity.
//
// It is the single entry point of the contract. The load, the publication
// and the durability confirmation all start here, so they cannot disagree
// about which file the record is.
//
// The path must be absolute and must name a file. The parent directory does
// not have to exist yet: a first start creates it, and the resolution
// describes where it will be created.
func locate(configured string) (recordLocation, error) {
	trimmed := strings.TrimSpace(configured)
	if trimmed == "" {
		return recordLocation{}, errors.New("siwc: the host record path is empty")
	}
	if !filepath.IsAbs(trimmed) {
		return recordLocation{}, fmt.Errorf(
			"siwc: the host record path %q must be absolute",
			filepath.Base(trimmed),
		)
	}
	name, rawParent, err := splitRecordPath(trimmed)
	if err != nil {
		return recordLocation{}, err
	}
	dir, err := resolvePhysicalDir(rawParent)
	if err != nil {
		return recordLocation{}, err
	}
	return recordLocation{configured: trimmed, rawParent: rawParent, dir: dir, name: name}, nil
}

// locateStateDir resolves the configured directory spelling to the physical
// directory the operating system will use.
//
// It is the directory-shaped entry point of the same contract that locate
// states for a record path: the configured spelling is an operator's
// spelling, not a directory identity, and the resolution is performed the way
// the operating system performs it.
//
// The directory does not have to exist yet: a first start creates it, and
// the resolution describes where it will be created. The caller creates the
// resolved directory and then cross-checks it against the operating system,
// so the protection check, the publication and the durability confirmation
// all work on one physical location.
func locateStateDir(configured string) (string, error) {
	trimmed := strings.TrimSpace(configured)
	if trimmed == "" {
		return "", errors.New("siwc: the state directory path is empty")
	}
	if !filepath.IsAbs(trimmed) {
		return "", fmt.Errorf(
			"siwc: the state directory path %q must be absolute; a relative path would address a different directory after a restart with a different working directory",
			filepath.Base(trimmed),
		)
	}
	return resolvePhysicalDir(trimmed)
}

// splitRecordPath splits the configured path into the record's file name and
// the raw spelling of its parent directory.
//
// The split is deliberately not lexical. filepath.Dir and filepath.Join
// clean a path, and a cleaned path can name a different directory than the
// one the operating system resolves: the "alias/.." pair is exactly that
// case. The raw parent spelling is therefore kept as it was written, so the
// resolution sees the components the operator wrote.
//
// A path whose last component is empty, "." or ".." does not name a file
// and is refused.
func splitRecordPath(configured string) (name, rawParent string, err error) {
	volume := filepath.VolumeName(configured)
	rest := configured[len(volume):]
	index := -1
	for i := len(rest) - 1; i >= 0; i-- {
		if os.IsPathSeparator(rest[i]) {
			index = i
			break
		}
	}
	if index < 0 {
		return "", "", fmt.Errorf(
			"siwc: the host record path %q does not name a file",
			filepath.Base(configured),
		)
	}
	name = rest[index+1:]
	switch name {
	case "", ".", "..":
		return "", "", fmt.Errorf(
			"siwc: the host record path %q does not name a file",
			filepath.Base(configured),
		)
	}
	rawParent = volume + rest[:index]
	switch {
	case rawParent == "":
		rawParent = string(filepath.Separator)
	case volume != "" && rawParent == volume:
		// A bare volume name addresses the volume's current directory, which
		// is not a stable location. The volume root is the stable one.
		rawParent = volume + string(filepath.Separator)
	}
	return name, rawParent, nil
}

// resolvePhysicalDir resolves the raw parent spelling the way the
// operating system resolves it.
//
// The whole path is resolved first: filepath.EvalSymlinks implements the
// POSIX rule that a ".." is applied after a symlink has been followed, so
// "<dir>/alias/.." names the parent of the alias target and not "<dir>".
//
// EvalSymlinks needs every component to exist, so a parent that does not
// exist yet - the first start of a fresh installation - is resolved by a
// prefix walk: the longest existing prefix is resolved and the missing tail
// is appended to it.
//
// The walk starts at the root of the path's volume. That root exists for
// every absolute path, so a first start below a directory that does not
// exist yet still has an existing ancestor to append the missing tail to.
//
// The tail keeps the operator's spelling: the separators that are cut off
// are cut off as bytes, so no letter of a directory name is ever mistaken
// for a separator.
//
// A missing tail that contains a "." or a ".." component is refused: the
// operating system cannot resolve it either, because the component the ".."
// refers to does not exist, so no single physical identity exists for it.
func resolvePhysicalDir(rawParent string) (string, error) {
	physical, err := filepath.EvalSymlinks(rawParent)
	if err == nil {
		return requireDirectory(physical, rawParent)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("siwc: resolve the state directory %s: %w", filepath.Base(rawParent), err)
	}

	prefixes := pathPrefixes(rawParent)
	lastPhysical := ""
	lastPrefix := ""
	for _, prefix := range prefixes {
		resolved, resolveErr := filepath.EvalSymlinks(prefix)
		if resolveErr != nil {
			if errors.Is(resolveErr, fs.ErrNotExist) {
				break
			}
			return "", fmt.Errorf("siwc: resolve the state directory %s: %w", filepath.Base(prefix), resolveErr)
		}
		directory, statErr := os.Stat(resolved)
		if statErr != nil {
			return "", fmt.Errorf("siwc: stat the state directory %s: %w", filepath.Base(prefix), statErr)
		}
		if !directory.IsDir() {
			return "", fmt.Errorf("siwc: the state directory %s is not a directory", filepath.Base(prefix))
		}
		lastPhysical = resolved
		lastPrefix = prefix
	}
	if lastPhysical == "" {
		return "", fmt.Errorf("siwc: resolve the state directory %s: no existing ancestor", filepath.Base(rawParent))
	}

	tail := strings.TrimPrefix(rawParent, lastPrefix)
	tail = trimLeadingSeparators(tail)
	if tail == "" {
		return lastPhysical, nil
	}
	for _, component := range splitComponents(tail) {
		if component == "." || component == ".." {
			return "", fmt.Errorf(
				"siwc: the state directory %s does not resolve to a single directory: %q follows a component that does not exist",
				filepath.Base(rawParent), component,
			)
		}
	}
	return filepath.Join(lastPhysical, tail), nil
}

// trimLeadingSeparators removes the leading path separators of a path
// fragment.
//
// The separators are trimmed as BYTES, never as decoded code points. A
// decoded code point narrowed to a byte aliases a legitimate name character
// onto a separator: "Я" (U+042F) and "į" (U+012F) both have a low byte of
// 0x2f, which is "/". A rune-wise trim would therefore delete the first
// letter of a directory whose name starts with such a character, and the
// store would create a different directory than the operator configured.
//
// A byte-wise trim cannot cut a name in half: a UTF-8 encoded code point
// never contains a separator byte inside its encoding, because the
// continuation bytes are all above 0x7f, and a path that is not valid UTF-8
// is a byte string on Unix anyway.
func trimLeadingSeparators(fragment string) string {
	for len(fragment) > 0 && os.IsPathSeparator(fragment[0]) {
		fragment = fragment[1:]
	}
	return fragment
}

// verifySameDirectory refuses a resolution that the operating system
// disagrees with.
//
// The resolver implements the POSIX rule for a ".." after a symlink, while
// the Windows kernel resolves a ".." naively in the path string, without
// taking reparse points into account. Asking the operating system whether
// the configured parent and the resolved parent are the same directory
// makes the contract safe on both: a disagreement is refused explicitly
// instead of silently writing the record somewhere else.
//
// Both paths must exist. The caller creates the resolved directory before
// the check, and the operating system resolves the configured spelling to
// that same directory.
func verifySameDirectory(configuredParent, resolvedParent string) error {
	configured, err := os.Stat(configuredParent)
	if err != nil {
		return fmt.Errorf("siwc: stat the configured state directory %s: %w", filepath.Base(configuredParent), err)
	}
	resolved, err := os.Stat(resolvedParent)
	if err != nil {
		return fmt.Errorf("siwc: stat the resolved state directory %s: %w", filepath.Base(resolvedParent), err)
	}
	if !os.SameFile(configured, resolved) {
		return fmt.Errorf(
			"siwc: the configured state directory %s does not resolve to the directory the record is stored in",
			filepath.Base(configuredParent),
		)
	}
	return nil
}

// pathPrefixes returns the component-wise prefixes of an absolute path, from
// the shortest to the longest.
//
// The prefixes are cut out of the original spelling, so they keep the
// separators the operator wrote and never lose a ".." component. They are
// never built with filepath.Join: joining cleans the path, and a cleaned
// path drops the ".." component that the resolution has to honour.
//
// The root of the path's volume is the first candidate. Every absolute path
// has exactly one ancestor that exists without any of its components
// existing - the root - and a walk that ignored it would report "no
// existing ancestor" for a path whose first component does not exist yet,
// even though the operating system would resolve it without trouble.
func pathPrefixes(path string) []string {
	volume := filepath.VolumeName(path)
	rest := path[len(volume):]
	prefixes := make([]string, 0, 8)
	if root := volumeRoot(volume); root != "" {
		prefixes = append(prefixes, root)
	}
	start := 0
	for i := 0; i <= len(rest); i++ {
		if i < len(rest) && !os.IsPathSeparator(rest[i]) {
			continue
		}
		if i > start {
			prefixes = append(prefixes, volume+rest[:i])
		}
		start = i + 1
	}
	return prefixes
}

// volumeRoot returns the root of the volume an absolute path lives on.
//
// It is the one ancestor that exists for every absolute path: "/" on Unix,
// the volume root "C:\" on Windows, and the root of a UNC share
// "\\server\share\".
func volumeRoot(volume string) string {
	if volume == "" {
		return string(filepath.Separator)
	}
	return volume + string(filepath.Separator)
}

// splitComponents splits a path fragment into its components.
func splitComponents(fragment string) []string {
	components := make([]string, 0, 4)
	start := 0
	for i := 0; i <= len(fragment); i++ {
		if i < len(fragment) && !os.IsPathSeparator(fragment[i]) {
			continue
		}
		if i > start {
			components = append(components, fragment[start:i])
		}
		start = i + 1
	}
	return components
}

// requireDirectory refuses a resolved path that is not a directory.
func requireDirectory(physical, raw string) (string, error) {
	info, err := os.Stat(physical)
	if err != nil {
		return "", fmt.Errorf("siwc: stat the state directory %s: %w", filepath.Base(raw), err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("siwc: the state directory %s is not a directory", filepath.Base(raw))
	}
	return physical, nil
}
