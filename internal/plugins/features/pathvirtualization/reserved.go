package pathvirtualization

// This file implements the fixed V1 reserved-alias namespace of design.md 155-195:
// the marker `.__aip_v1__`, the tag segment `w_<20-character tag>`, and the rule
// that a recognized reserved alias is resolved against the current workspace
// BEFORE ordinary real-root prefix matching.
//
// The namespace is implementation syntax, never operator configuration, and the
// tag inside an alias is the only proof of which project root produced it. There is
// no dictionary of past roots: a stale alias carries its own evidence and is
// therefore rejected rather than rebound (design.md 376).

// reservedAlias is the parsed form of one recognized V1 reserved alias.
//
// The zero value is the state of an alias the caller did not recognize at all,
// which is not the same as a recognized but malformed one: only the latter can be
// reported as malformed_reserved_alias.
type reservedAlias struct {
	// flavor is the absolute-path form the alias root itself spells. For a mangled
	// anchor it is the form its volume head names, not the one ClassifyPath read
	// for the whole path, because that form decides both how the alias identity
	// compares and whether it can name the current mapping at all.
	flavor PathFlavor
	// drive is the upper-case drive letter of a drive-flavored alias, or 0 for
	// every other flavor. It is part of the alias identity, so a same-tag alias on
	// another drive is not this workspace's alias.
	drive byte
	// tag is the alias's workspace tag: exactly workspaceTagChars characters of the
	// unpadded base32 alphabet, read under the alias flavor's own comparison rules.
	// It is set only when wellFormed is true.
	tag string
	// rest is the untouched suffix after the alias root: empty, or a run that
	// starts with a separator. It is set only when wellFormed is true.
	rest string
	// wellFormed reports whether the alias root is exactly the frozen V1 form.
	wellFormed bool
}

// parseReservedAlias recognizes the fixed V1 reserved namespace at the head of
// path. The second result reports whether the path is inside the reserved
// namespace at all: false means path is an ordinary path that ordinary prefix
// matching may handle, while true means the caller must resolve it as an alias or
// reject it with a bounded reason.
//
// Recognition is purely lexical and host-independent, and it does not depend on
// how the path happens to spell its own volume. The marker sits in a different
// position per flavor, because the alias must keep the absolute-path shape of the
// root it replaces:
//
//	POSIX           /.__aip_v1__/w_<tag>/
//	Windows drive   C:\.__aip_v1__\w_<tag>\
//	Windows UNC     \\.__aip_v1__\w_<tag>\   (marker is the server, tag the share)
//	Extended drive  \\?\C:\.__aip_v1__\w_<tag>\
//	Extended UNC    \\?\UNC\.__aip_v1__\w_<tag>\
//
// A path that is not one of the five supported absolute forms names no absolute
// path at all, so it cannot name a V1 alias root: it is left to the caller, which
// rejects it with the bounded reason the classifier already produced. Recognition
// never invents a volume for it.
func parseReservedAlias(path string) (reservedAlias, bool) {
	parsed, reason := ClassifyPath(path)
	if reason != SkipReasonNone {
		return reservedAlias{}, false
	}
	// The classified volume is the primary alias-root position: the marker is the
	// first segment below it, or the UNC server itself.
	if alias, reserved := parseAliasRootPosition(parsed); reserved {
		return alias, true
	}
	// The classified volume is not the only way to spell an alias root. `/C:/...`
	// and `//?/C:/...` are supported absolute forms whose leading segments spell a
	// Windows volume that the classifier read as ordinary segments, which would hide
	// the marker one level deeper. Recognition must not turn on that spelling
	// accident, or a stale alias written this way would reach the caller unchanged
	// (requirement 4.4).
	return parseVolumeLookalikeAlias(path)
}

// parseAliasRootPosition recognizes the marker at the alias-root position of the
// path's own classified flavor.
func parseAliasRootPosition(parsed ParsedPath) (reservedAlias, bool) {
	alias := reservedAlias{flavor: parsed.Flavor, drive: aliasDrive(parsed.Flavor, parsed.Root)}
	switch parsed.Flavor {
	case FlavorWindowsUNC, FlavorWindowsExtendedUNC:
		return parseVolumeReservedAlias(alias, parsed)
	default:
		return parseSegmentReservedAlias(alias, parsed)
	}
}

// parseSegmentReservedAlias parses the flavors whose reserved marker is the first
// segment below the spelled volume: POSIX, Windows drive, and Windows extended
// drive. The workspace tag is the segment after the marker.
func parseSegmentReservedAlias(alias reservedAlias, parsed ParsedPath) (reservedAlias, bool) {
	// Leading separators below the anchor belong to no segment: skipping them
	// locates the first segment of `C:\...` and of a `//`-spelled POSIX anchor
	// alike, and can never turn an ordinary path into an alias, because the
	// reserved marker must still be that first segment.
	marker, rest := takeSegment(trimLeadingSeparators(parsed.Rest))
	if !segmentEqual(alias.flavor, marker, reservedNamespaceV1) {
		return reservedAlias{}, false
	}
	tag, suffix := takeSegment(trimLeadingSeparators(rest))
	return parseAliasTag(alias, tag, suffix), true
}

// parseVolumeReservedAlias parses the flavors whose reserved marker is the UNC
// server name, so the whole alias root is the spelled volume and the workspace tag
// is its share component.
func parseVolumeReservedAlias(alias reservedAlias, parsed ParsedPath) (reservedAlias, bool) {
	server, rest := takeSegment(validatedUNCBody(parsed.Flavor, parsed.Root))
	if !segmentEqual(alias.flavor, server, reservedNamespaceV1) {
		return reservedAlias{}, false
	}
	share, _ := takeSegment(trimLeadingSeparators(rest))
	// ClassifyPath only leaves an empty remainder or one that starts at the
	// separator following a complete share, so the suffix keeps its own bytes.
	return parseAliasTag(alias, share, parsed.Rest), true
}

// parseVolumeLookalikeAlias recognizes the reserved marker below a volume head
// that ClassifyPath did not read as a volume.
//
// The scanned heads are exactly the Windows volume spellings, each read with that
// flavor's own matching rules, so a `/C:/...` spelling is a Windows drive alias and
// can never be mistaken for a POSIX one. Nothing else is looked through: a marker
// below an ordinary segment is an ordinary path, exactly as requirement 1.8
// requires of a real project root that merely contains the marker.
func parseVolumeLookalikeAlias(path string) (reservedAlias, bool) {
	alias, body, ok := scanVolumeLookalikeHead(path)
	if !ok {
		return reservedAlias{}, false
	}
	marker, rest := takeSegment(body)
	if !segmentEqual(alias.flavor, marker, reservedNamespaceV1) {
		return reservedAlias{}, false
	}
	tag, suffix := takeSegment(trimLeadingSeparators(rest))
	return parseAliasTag(alias, tag, suffix), true
}

// scanVolumeLookalikeHead locates a Windows volume head at the front of path and
// returns it with the untouched text that follows its trailing separator, which is
// where the reserved marker must then sit.
func scanVolumeLookalikeHead(path string) (reservedAlias, string, bool) {
	head := separatorRunAt(path, 0)
	if head == 0 || head == len(path) {
		// Only an absolute path with a segment below its anchor can spell an alias
		// root; a bare separator run is a volume root with nothing under it.
		return reservedAlias{}, "", false
	}
	// The literal `?` of the `\\?\` extended anchor is the one byte no supported
	// volume spelling shares, so a mangled head that kept it still names an
	// extended volume.
	if path[head] == extendedQuery {
		return scanExtendedLookalikeHead(path, head)
	}
	if drive, next, ok := driveVolumeAt(path, head); ok {
		// `<letter>:` is exactly how a Windows drive alias spells its volume.
		return reservedAlias{flavor: FlavorWindowsDrive, drive: drive}, path[next:], true
	}
	return reservedAlias{}, "", false
}

// scanExtendedLookalikeHead continues scanVolumeLookalikeHead past the `?` of an
// extended anchor, which introduces either a drive volume or the `UNC` marker.
func scanExtendedLookalikeHead(path string, at int) (reservedAlias, string, bool) {
	next := at + 1 + separatorRunAt(path, at+1)
	if next == at+1 {
		// `?` does not end its own segment, so this spells no extended volume.
		return reservedAlias{}, "", false
	}
	if drive, after, ok := driveVolumeAt(path, next); ok {
		return reservedAlias{flavor: FlavorWindowsExtendedDrive, drive: drive}, path[after:], true
	}
	if !hasPrefixFoldASCII(path[next:], uncMarker) {
		return reservedAlias{}, "", false
	}
	// The extended UNC volume is spelled `\\?\UNC<sep>server<sep>share`, and the
	// marker is the server, so the reserved marker itself follows the separator.
	share := next + len(uncMarker)
	after := share + separatorRunAt(path, share)
	if after == share {
		return reservedAlias{}, "", false
	}
	return reservedAlias{flavor: FlavorWindowsExtendedUNC}, path[after:], true
}

// driveVolumeAt recognizes a `<letter>:` volume at path[at:] followed by at least
// one separator, returning the upper-case drive letter and the index of the segment
// that follows it.
func driveVolumeAt(path string, at int) (drive byte, next int, ok bool) {
	if at+1 >= len(path) || !isASCIILetter(path[at]) || path[at+1] != driveColon {
		return 0, 0, false
	}
	run := separatorRunAt(path, at+2)
	if run == 0 {
		// `C:relative` is drive-relative, so it names no absolute volume.
		return 0, 0, false
	}
	return toUpperASCII(path[at]), at + 2 + run, true
}

// parseAliasTag completes a recognized reserved alias from its candidate workspace
// tag segment and the untouched suffix that follows the tag.
//
// The namespace is recognized from the marker on, so a candidate that does not
// spell the frozen tag segment leaves the alias malformed rather than ordinary: no
// identity is guessed from it, and it can never be released as a client path.
func parseAliasTag(alias reservedAlias, tagSegment, suffix string) reservedAlias {
	tag, ok := reservedWorkspaceTag(alias.flavor, tagSegment)
	if !ok {
		return alias
	}
	alias.tag = tag
	alias.rest = suffix
	alias.wellFormed = true
	return alias
}

// expandReservedAlias resolves one recognized reserved alias against this mapping,
// following design.md 186-195.
//
// The three outcomes are the whole vocabulary: a well-formed alias of the current
// mapping expands, a malformed alias is reported as malformed_reserved_alias, and
// anything else is reported as workspace_mismatch. A stale alias is never expanded
// against the current root and never reinterpreted as an ordinary client path, so
// both rejections return no path value at all: a caller that checks only the path
// cannot release the reserved namespace to the client.
func (m Mapping) expandReservedAlias(alias reservedAlias) (string, ExpandResult) {
	if !alias.wellFormed {
		return "", ExpandResultMalformedReservedAlias
	}
	// The alias must name this mapping in every identity dimension: the absolute
	// form, the drive letter, and the workspace tag. An inactive mapping derived
	// no alias at all, so it can name none either.
	if alias.flavor != m.Flavor ||
		alias.drive != aliasDrive(m.Flavor, m.RealRoot) ||
		m.VirtualRoot == "" ||
		!segmentEqual(alias.flavor, alias.tag, m.WorkspaceTag) {
		return "", ExpandResultWorkspaceMismatch
	}
	return joinRootSuffix(m.Flavor, m.RealRoot, alias.rest), ExpandResultExpanded
}

// reservedWorkspaceTag validates one candidate workspace tag segment and returns
// the tag it spells. The segment is the fixed `w_` prefix followed by exactly
// workspaceTagChars characters of the unpadded base32 alphabet, compared under the
// alias flavor's own rules.
func reservedWorkspaceTag(flavor PathFlavor, segment string) (string, bool) {
	if len(segment) != len(tagPrefix)+workspaceTagChars {
		return "", false
	}
	if !segmentEqual(flavor, segment[:len(tagPrefix)], tagPrefix) {
		return "", false
	}
	for i := len(tagPrefix); i < len(segment); i++ {
		if !isTagChar(flavor, segment[i]) {
			return "", false
		}
	}
	return segment[len(tagPrefix):], true
}

// isTagChar reports whether c can spell a workspace tag: the unpadded base32
// alphabet a-z and 2-7.
//
// A Windows flavor also accepts the upper-case spelling of the same alphabet,
// because Windows matching is ASCII case-insensitive, so such a spelling still
// identifies the same workspace. A POSIX alias must carry the canonical lower-case
// tag exactly as the derivation emits it, because POSIX matching is
// case-sensitive.
func isTagChar(flavor PathFlavor, c byte) bool {
	if c >= '2' && c <= '7' {
		return true
	}
	if c >= 'a' && c <= 'z' {
		return true
	}
	return flavor != FlavorPOSIX && c >= 'A' && c <= 'Z'
}

// segmentEqual compares two path segments under a flavor's matching rules: POSIX
// compares byte-exactly, while Windows flavors fold ASCII case and treat both
// separators as equivalent. Non-ASCII bytes never fold, so no Unicode equivalence
// can enter the comparison.
func segmentEqual(flavor PathFlavor, got, want string) bool {
	if len(got) != len(want) {
		return false
	}
	if flavor == FlavorPOSIX {
		return got == want
	}
	for i := range len(want) {
		if !equalWindowsByte(want[i], got[i]) {
			return false
		}
	}
	return true
}

// takeSegment splits the first path segment off body and returns it with the
// untouched remainder, which starts at the segment's separator or is empty.
//
// Both Windows separators end a segment in every flavor, including POSIX, which is
// stricter than the POSIX match rule of design.md 150 where a backslash is an
// ordinary file-name byte. Reserved recognition has to be the stricter of the two:
// a marker followed by a backslash is either a mangled alias or a real POSIX file
// name, and only rejecting or expanding the first is safe. Splitting on `/` alone
// would classify `/.__aip_v1__\w_<tag>/x` as an ordinary path and hand the reserved
// namespace back to the caller, which requirement 4.4 forbids. Ordinary prefix
// matching keeps the POSIX rule unchanged: stripRootPrefix still treats a
// backslash there as an ordinary byte.
func takeSegment(body string) (segment, rest string) {
	sep := indexSeparator(body)
	if sep < 0 {
		return body, ""
	}
	return body[:sep], body[sep:]
}

// trimLeadingSeparators drops every separator at the head of s and changes no
// other byte. It only locates where the first segment starts; no path segment,
// dot segment, or separator run inside the path is ever resolved or collapsed.
func trimLeadingSeparators(s string) string {
	for len(s) > 0 && isSeparator(s[0]) {
		s = s[1:]
	}
	return s
}

// separatorRunAt returns the number of consecutive separators at s[at:]. It is
// separator-agnostic because Windows volumes are spelled with either separator
// interchangeably, and it is used only to locate a segment boundary.
func separatorRunAt(s string, at int) int {
	end := at
	for end < len(s) && isSeparator(s[end]) {
		end++
	}
	return end - at
}

// aliasDrive returns the upper-case drive letter of a drive-flavored spelled
// volume, or 0 for every other flavor. Both Windows separators are accepted where
// the volume itself uses them.
func aliasDrive(flavor PathFlavor, volume string) byte {
	switch flavor {
	case FlavorWindowsDrive:
		// The volume is spelled `C:`.
		if len(volume) > 1 && volume[1] == driveColon {
			return toUpperASCII(volume[0])
		}
	case FlavorWindowsExtendedDrive:
		// The volume is spelled `\\?\C:`, so the drive letter follows the fixed
		// extended prefix.
		drive := len(uncPrefix) + len(extendedPrefix)
		if len(volume) > drive+1 && volume[drive+1] == driveColon {
			return toUpperASCII(volume[drive])
		}
	}
	return 0
}

// validatedUNCBody returns the `server<sep>share<rest>` body of a classified UNC
// volume, dropping the spelled volume prefix.
//
// The volume must come from ClassifyPath with a UNC or extended-UNC flavor, which
// is exactly what guarantees a complete `\\server\share` or `\\?\UNC\server\share`
// volume: both segments are non-empty, so the body can be sliced without a bounds
// check. Re-validating a prefix the classifier already proved would duplicate that
// rule instead of reusing it.
func validatedUNCBody(flavor PathFlavor, volume string) string {
	body := volume[len(uncPrefix):]
	if flavor != FlavorWindowsExtendedUNC {
		return body
	}
	// An extended UNC volume is spelled `\\?\UNC<sep>server<sep>share`, and the
	// separator after the marker keeps the client's own byte, so the fixed prefix
	// is the extended prefix, the marker, and one separator.
	return body[len(extendedPrefix)+len(uncMarker)+1:]
}
