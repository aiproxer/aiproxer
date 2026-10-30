// Package pathvirtualization implements the pure lexical path policy for B-leg
// path virtualization.
//
// The mapper recognizes the absolute-path form a client path uses
// independently of the operating system the proxy process runs on, and reports
// bounded, content-free reason codes for paths it must not rewrite. Host
// separator, host path, and filesystem semantics are never treated as authority
// for a client path that may name another operating system.
//
// The package performs no filesystem I/O and resolves no symlinks, junctions,
// short names, environment variables, dot segments, or Unicode equivalence: a
// recognized path is only split into its spelled volume prefix and its
// untouched remainder.
package pathvirtualization

import "strings"

// PathFlavor identifies the lexical absolute-path form of a path string.
type PathFlavor uint8

// Supported absolute-path flavors. FlavorUnsupported is the zero value and marks
// a path that is not one of the five supported absolute forms.
const (
	FlavorUnsupported PathFlavor = iota
	FlavorPOSIX
	FlavorWindowsDrive
	FlavorWindowsUNC
	FlavorWindowsExtendedDrive
	FlavorWindowsExtendedUNC
)

// String returns the fixed, low-cardinality label for a flavor. It is safe for
// content-free observability dimensions: it never contains path bytes.
func (f PathFlavor) String() string {
	switch f {
	case FlavorPOSIX:
		return "posix"
	case FlavorWindowsDrive:
		return "windows_drive"
	case FlavorWindowsUNC:
		return "windows_unc"
	case FlavorWindowsExtendedDrive:
		return "windows_extended_drive"
	case FlavorWindowsExtendedUNC:
		return "windows_extended_unc"
	default:
		return "unsupported"
	}
}

// SkipReason is a bounded, path-content-free explanation of why a path is not a
// usable absolute root. It never carries path bytes, suffixes, or digests.
type SkipReason string

const (
	// SkipReasonNone marks an accepted path.
	SkipReasonNone SkipReason = ""
	// SkipReasonEmptyRoot marks an empty path.
	SkipReasonEmptyRoot SkipReason = "empty_root"
	// SkipReasonRelativeRoot marks a path with no absolute anchor: a relative
	// path, a Windows drive-relative path, or a volume-less root-relative path.
	SkipReasonRelativeRoot SkipReason = "relative_root"
	// SkipReasonMalformedVolumeRoot marks a volume prefix that cannot denote an
	// absolute root, such as a drive letter without a separator or a UNC prefix
	// without a share component.
	SkipReasonMalformedVolumeRoot SkipReason = "malformed_volume_root"
	// SkipReasonDeviceNamespace marks a Windows device or NT-namespace path such
	// as `\\.\PIPE\...` or `\\?\GLOBALROOT\...`.
	SkipReasonDeviceNamespace SkipReason = "device_namespace"
	// SkipReasonReservedNamespaceCollision marks a supported project root spelled
	// inside the fixed V1 reserved alias namespace. Rewriting is disabled for that
	// mapping: such a root's own paths would be indistinguishable from the
	// reserved namespace, so a client path could never be told apart from an
	// alias again (requirement 1.8).
	SkipReasonReservedNamespaceCollision SkipReason = "reserved_namespace_collision"
)

// String returns the fixed, low-cardinality label of a refusal code, or "unknown" for a
// value outside the closed vocabulary.
//
// It is total on purpose. This type is an exported named string, so any caller can
// construct a value that names a real root, an alias, or a path suffix and hand it to
// something that formats it; the total mapping is what keeps such a value from
// reaching a log line or a metric label verbatim.
func (r SkipReason) String() string {
	switch r {
	case SkipReasonNone:
		return ""
	case SkipReasonEmptyRoot:
		return "empty_root"
	case SkipReasonRelativeRoot:
		return "relative_root"
	case SkipReasonMalformedVolumeRoot:
		return "malformed_volume_root"
	case SkipReasonDeviceNamespace:
		return "device_namespace"
	case SkipReasonReservedNamespaceCollision:
		return "reserved_namespace_collision"
	default:
		return "unknown"
	}
}

// MarshalText implements [encoding.TextMarshaler] so a refusal code reaches any exporter
// as the closed-vocabulary label rather than as the caller's own bytes.
//
// The risk this closes is specific to a string-typed enum: every other enum in this
// feature is an integer, so json.Marshal already renders it as a number no caller
// controls, while THIS one renders verbatim by default. A value assembled from a real
// project root would therefore export as that root, which is exactly the content
// requirements.md 7.7 forbids from reaching a metric label, a log field, or a
// diagnostics inventory. Marshalling delegates to [SkipReason.String] rather than
// restating the vocabulary a second time.
func (r SkipReason) MarshalText() ([]byte, error) { return []byte(r.String()), nil }

// ParsedPath is the canonical lexical result of classifying one absolute path.
// Both fields preserve the exact input spelling, so Root+Rest reproduces the
// input bytes.
type ParsedPath struct {
	// Flavor is the recognized absolute-path form.
	Flavor PathFlavor
	// Root is the spelled volume/anchor prefix: "/" for POSIX, "C:" for a
	// Windows drive, `\\server\share` for UNC, `\\?\C:` for an extended drive,
	// and `\\?\UNC\server\share` for an extended UNC. It is empty when the path
	// was rejected.
	Root string
	// Rest is the untouched remainder following Root, starting with the
	// separator that follows the volume. Case, separator style, and dot segments
	// are never normalized.
	Rest string
}

const (
	slashSeparator     = '/'
	backslashSeparator = '\\'
	// extendedPrefix introduces the Windows `\\?\` extended namespace, spelled
	// here as it appears after the leading `\\`.
	extendedPrefix = `?\`
	// extendedQuery is the leading `?` of extendedPrefix on its own, used to spot
	// an extended volume whose anchor the client spelled with a different
	// separator.
	extendedQuery = '?'
	// devicePrefix introduces the Windows `\\.\` device namespace, spelled here
	// as it appears after the leading `\\`.
	devicePrefix = `.\`
	// uncMarker follows the extended prefix in an extended UNC volume.
	uncMarker = "UNC"
	// uncPrefix introduces a plain UNC volume.
	uncPrefix = `\\`
	// driveColon separates a Windows drive letter from its remainder.
	driveColon = ':'
)

// ClassifyPath recognizes the lexical absolute-path flavor of path without
// consulting the host operating system, its separators, or the filesystem.
//
// It returns the spelled volume prefix with the untouched remainder, or a
// bounded reason code when the path is not absolute, its volume root is
// malformed, or it names the Windows device namespace. Both Windows separators
// are accepted as equivalent at volume boundaries.
func ClassifyPath(path string) (ParsedPath, SkipReason) {
	if path == "" {
		return ParsedPath{}, SkipReasonEmptyRoot
	}
	switch path[0] {
	case slashSeparator:
		return ParsedPath{Flavor: FlavorPOSIX, Root: string(slashSeparator), Rest: path[1:]}, SkipReasonNone
	case backslashSeparator:
		return classifyWindowsPath(path)
	}
	if !isASCIILetter(path[0]) || len(path) < 2 || path[1] != driveColon {
		return ParsedPath{}, SkipReasonRelativeRoot
	}
	if len(path) == 2 {
		// `C:` is drive-relative: it names no volume root.
		return ParsedPath{}, SkipReasonMalformedVolumeRoot
	}
	if !isSeparator(path[2]) {
		// `C:relative` is drive-relative too.
		return ParsedPath{}, SkipReasonRelativeRoot
	}
	return ParsedPath{Flavor: FlavorWindowsDrive, Root: path[:2], Rest: path[2:]}, SkipReasonNone
}

// classifyWindowsPath parses a path anchored on a backslash, which is either the
// UNC family (`\\server\share`, `\\?\C:`, `\\?\UNC\server\share`) or a
// volume-less root-relative path that is not absolute.
func classifyWindowsPath(path string) (ParsedPath, SkipReason) {
	if len(path) < 2 || path[1] != backslashSeparator {
		// `\Users\dev` is rooted on the current drive, not absolute.
		return ParsedPath{}, SkipReasonRelativeRoot
	}
	body := path[2:]
	if hasPrefixASCII(body, extendedPrefix) {
		return classifyExtendedPath(body[len(extendedPrefix):])
	}
	if body == "." || hasPrefixASCII(body, devicePrefix) {
		return ParsedPath{}, SkipReasonDeviceNamespace
	}
	volume, rest, ok := uncVolume(body)
	if !ok {
		return ParsedPath{}, SkipReasonMalformedVolumeRoot
	}
	return ParsedPath{Flavor: FlavorWindowsUNC, Root: uncPrefix + volume, Rest: rest}, SkipReasonNone
}

// classifyExtendedPath parses the payload that follows the `\\?\` extended
// prefix. Only an extended drive and an extended UNC volume are supported; every
// other `\\?\` payload names an NT object and is rejected as a device path.
func classifyExtendedPath(body string) (ParsedPath, SkipReason) {
	if body == "" {
		return ParsedPath{}, SkipReasonMalformedVolumeRoot
	}
	// The UNC marker is tested before the drive letter because a marker can
	// never spell a drive: index 1 of a drive volume is the colon.
	if hasPrefixFoldASCII(body, uncMarker) {
		marker := body[:len(uncMarker)]
		if len(body) == len(marker) || !isSeparator(body[len(marker)]) {
			return ParsedPath{}, SkipReasonMalformedVolumeRoot
		}
		volume, rest, ok := uncVolume(body[len(marker)+1:])
		if !ok {
			return ParsedPath{}, SkipReasonMalformedVolumeRoot
		}
		return ParsedPath{
			Flavor: FlavorWindowsExtendedUNC,
			Root:   uncPrefix + extendedPrefix + marker + body[len(marker):len(marker)+1] + volume,
			Rest:   rest,
		}, SkipReasonNone
	}
	if !isASCIILetter(body[0]) {
		return ParsedPath{}, SkipReasonDeviceNamespace
	}
	if len(body) < 2 || body[1] != driveColon {
		return ParsedPath{}, SkipReasonDeviceNamespace
	}
	if len(body) < 3 || !isSeparator(body[2]) {
		return ParsedPath{}, SkipReasonMalformedVolumeRoot
	}
	return ParsedPath{
		Flavor: FlavorWindowsExtendedDrive,
		Root:   uncPrefix + extendedPrefix + body[:2],
		Rest:   body[2:],
	}, SkipReasonNone
}

// uncVolume splits the `server\share` volume off a UNC body, where body is the
// text that follows the leading `\\`. Both separators are accepted. It returns
// the volume and the untouched remainder that follows it.
func uncVolume(body string) (volume, rest string, ok bool) {
	// The server name must be non-empty and followed by a separator.
	sep := indexSeparator(body)
	if sep <= 0 {
		return "", "", false
	}
	share := body[sep+1:]
	if share == "" {
		return "", "", false
	}
	// The share name must be non-empty; an empty one leaves a bare `server\`
	// prefix that names no volume root.
	next := indexSeparator(share)
	if next == 0 {
		return "", "", false
	}
	if next < 0 {
		return body[:sep+1] + share, "", true
	}
	return body[:sep+1] + share[:next], share[next:], true
}

func isSeparator(c byte) bool {
	return c == slashSeparator || c == backslashSeparator
}

func isASCIILetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// indexSeparator returns the index of the first separator in s, or -1 when s
// holds neither. It never treats the host separator set as authoritative.
func indexSeparator(s string) int {
	backslash := strings.IndexByte(s, backslashSeparator)
	slash := strings.IndexByte(s, slashSeparator)
	switch {
	case backslash < 0:
		return slash
	case slash < 0:
		return backslash
	case slash < backslash:
		return slash
	default:
		return backslash
	}
}

// hasPrefixASCII reports whether s starts with the exact prefix bytes.
func hasPrefixASCII(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// hasPrefixFoldASCII reports whether s starts with prefix under ASCII-only case
// folding. Non-ASCII bytes never fold, so no Unicode equivalence is applied.
func hasPrefixFoldASCII(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		if toLowerASCII(s[i]) != toLowerASCII(prefix[i]) {
			return false
		}
	}
	return true
}

func toLowerASCII(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
}
