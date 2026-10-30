package pathvirtualization

import (
	"crypto/sha256"
	"encoding/base32"
	"strconv"
	"strings"
)

// Mapping is the pure, workspace-bound translation between one authoritative
// workspace root and its fixed V1 alias.
//
// A Mapping is derived only from Workspace.ProjectRoot, holds no mutable state,
// and can never widen workspace access: it substitutes one fixed absolute prefix
// for another and records nothing else.
type Mapping struct {
	// Flavor is the absolute-path form shared by both roots. It is derived, never
	// configured.
	Flavor PathFlavor
	// RealRoot is the project root exactly as spelled, so expansion can
	// reconstruct the client's original bytes. It is never normalized.
	RealRoot string
	// WorkspaceTag is the 96-bit workspace identity embedded in VirtualRoot. It
	// is collision-resistant identity, not a secret, and is never a log or metric
	// attribute.
	WorkspaceTag string
	// VirtualRoot is the fixed V1 alias for RealRoot. It is empty when the alias
	// would not be strictly shorter than the real root, which leaves outbound
	// virtualization inactive for that mapping.
	VirtualRoot string
}

// ExpandResult is the bounded, path-content-free outcome of one reverse expansion
// attempt. It is an enum because the value only ever reaches fixed-count
// dimensions; it never carries path bytes, suffixes, or digests.
type ExpandResult uint8

const (
	// ExpandResultNotApplicable marks a path that carries no alias of this
	// mapping, so it is returned unchanged.
	ExpandResultNotApplicable ExpandResult = iota
	// ExpandResultExpanded marks a path whose alias prefix was replaced by the
	// mapping's RealRoot plus the untouched suffix.
	ExpandResultExpanded
	// ExpandResultMalformedReservedAlias marks a recognized V1 reserved alias whose
	// workspace tag segment is not exactly the frozen `w_` plus 20 base32
	// characters. Such a path is never expanded and never handed back as an
	// ordinary client path.
	ExpandResultMalformedReservedAlias
	// ExpandResultWorkspaceMismatch marks a well-formed reserved alias that does
	// not name the current mapping: another project root's tag, an incompatible
	// alias flavor or drive, or a mapping with no active alias. It is the stale
	// workspace outcome of requirement 6.5, and it is never expanded against the
	// current root.
	ExpandResultWorkspaceMismatch
)

// String returns the fixed, low-cardinality label for an expansion outcome. It is
// safe for content-free observability dimensions: it never contains path bytes.
func (r ExpandResult) String() string {
	switch r {
	case ExpandResultNotApplicable:
		return "not_applicable"
	case ExpandResultExpanded:
		return "expanded"
	case ExpandResultMalformedReservedAlias:
		return "malformed_reserved_alias"
	case ExpandResultWorkspaceMismatch:
		return "workspace_mismatch"
	default:
		return "unknown"
	}
}

const (
	// reservedNamespaceV1 is the fixed V1 reserved alias namespace marker. It is
	// an implementation contract, never operator configuration, so an alias a
	// provider retained across a reload stays recognizable.
	reservedNamespaceV1 = ".__aip_v1__"
	// tagPrefix introduces the workspace tag inside a virtual root.
	tagPrefix = "w_"
	// workspaceTagBytes is the digest prefix length: 96 bits, which encodes as
	// exactly 20 unpadded base32 characters.
	workspaceTagBytes = 12
	// workspaceTagChars is that encoded length. A reserved alias is recognized
	// only when its tag segment is exactly this long, so the constant is the
	// contract a malformed alias is rejected against.
	workspaceTagChars = 20
	// tagDomainV1 version-separates this derivation from any other SHA-256 use, so
	// the V1 tag algorithm cannot drift silently.
	tagDomainV1 = "aip:path-virtualization:v1"
	// tagFieldSeparator delimits the domain, flavorID, and canonical identity, so
	// no combination of field bytes can imitate another.
	tagFieldSeparator = "\x00"
	// upperASCIIOffset folds a lower-case ASCII byte to its upper-case form.
	upperASCIIOffset = 'a' - 'A'
)

// tagEncoding is unpadded standard base32, which renders the 96-bit digest prefix
// as exactly 20 characters with no padding bytes to strip.
var tagEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// DeriveMapping derives the deterministic workspace-bound mapping for a project
// root without filesystem I/O, host path semantics, or any stored dictionary.
//
// It returns a bounded reason code for a root that is not one of the five
// supported absolute forms and for a supported root spelled inside the fixed V1
// reserved alias namespace. A supported root always yields its RealRoot and its
// WorkspaceTag; outbound virtualization additionally stays active only when the
// complete virtual root is strictly shorter both than the real root and than the
// prefix that prefix matching actually replaces.
func DeriveMapping(projectRoot string) (Mapping, SkipReason) {
	parsed, reason := ClassifyPath(projectRoot)
	if reason != SkipReasonNone {
		return Mapping{}, reason
	}
	// Requirement 1.8: a root inside the reserved namespace is unusable rather than
	// merely awkward. Its own alias would be nested in that namespace, and every
	// path under the root would then be recognized as a reserved alias instead of
	// a client path, so rewriting is disabled with a bounded reason instead of
	// guessing which spelling the client meant.
	if _, reserved := parseReservedAlias(projectRoot); reserved {
		return Mapping{}, SkipReasonReservedNamespaceCollision
	}
	mapping := Mapping{
		Flavor:       parsed.Flavor,
		RealRoot:     parsed.Root + parsed.Rest,
		WorkspaceTag: workspaceTag(parsed.Flavor, canonicalRootIdentity(parsed)),
	}
	virtual := virtualRoot(parsed, mapping.WorkspaceTag)
	// The alias must shorten the real root (design.md 184) and, per requirement
	// 9.1, it must also shorten the prefix that is actually replaced. The two
	// lengths diverge when the root was spelled with trailing separators, so both
	// gates are required: otherwise a long trailing-separator run could make an
	// active mapping whose rewrite is longer than the path it replaced. Both
	// comparisons are strict, because an equal-length alias would leave the bare
	// root unshortened.
	if len(virtual) < len(mapping.RealRoot) &&
		len(virtual) < len(matchableRoot(mapping.RealRoot)) {
		mapping.VirtualRoot = virtual
	}
	return mapping, SkipReasonNone
}

// VirtualizePath replaces a leading RealRoot prefix with the alias, ending the
// replacement on a path-segment boundary.
//
// The suffix keeps its original bytes and the real-root spelling is dropped, so
// expansion can reconstruct the client's path exactly. It reports false without
// touching the path when the mapping has no active alias, when the prefix does
// not match under the flavor's comparison rules, or when the match would end
// mid-segment.
func (m Mapping) VirtualizePath(path string) (string, bool) {
	if m.VirtualRoot == "" {
		return path, false
	}
	suffix, ok := stripRootPrefix(path, m.Flavor, matchableRoot(m.RealRoot))
	if !ok {
		return path, false
	}
	if suffix == "" {
		return m.VirtualRoot, true
	}
	// The alias already ends with its flavor's separator, so the original
	// boundary separator is dropped instead of being doubled.
	return m.VirtualRoot[:len(m.VirtualRoot)-1] + suffix, true
}

// ExpandPath replaces a leading VirtualRoot prefix with RealRoot plus the
// untouched suffix, reconstructing the mapping's original real-root spelling.
//
// Reserved-alias recognition runs BEFORE ordinary prefix matching (design.md
// 186-195), so a path that spells the fixed V1 reserved namespace at an alias-root
// position is never handled as an ordinary path: a malformed alias is reported as
// ExpandResultMalformedReservedAlias, an alias that does not name this mapping is
// reported as ExpandResultWorkspaceMismatch, and both rejections return no path
// value at all, so a reserved alias can never be released to the client.
//
// Recognition covers every alias root this build derives, in its derived spelling
// and in the separator- and anchor-mangled spellings of it, so the ordinary
// matcher is a guard rather than the normal path: it keeps expansion resolving
// against the real root if a later version emits an alias spelling this build does
// not recognize, instead of returning that alias unchanged.
func (m Mapping) ExpandPath(path string) (string, ExpandResult) {
	if alias, reserved := parseReservedAlias(path); reserved {
		return m.expandReservedAlias(alias)
	}
	if m.VirtualRoot == "" {
		return path, ExpandResultNotApplicable
	}
	suffix, ok := stripRootPrefix(path, m.Flavor, matchableRoot(m.VirtualRoot))
	if !ok {
		return path, ExpandResultNotApplicable
	}
	return joinRootSuffix(m.Flavor, m.RealRoot, suffix), ExpandResultExpanded
}

// matchableRoot drops the trailing separators of root that sit above its spelled
// volume boundary, so the segment-boundary rule behaves identically whether or not
// the root was spelled with a trailing separator.
//
// The floor is the spelled volume itself, with no allowance: matching operates on
// the bytes a path actually carries. A drive volume therefore matches as `C:`, a
// UNC volume as `\\server\share`, and the POSIX anchor as `/`. The `/` anchor
// keeps its single separator, so it matches only itself and never `/x`; a drive
// volume matches its children because the separator that follows it in the path is
// the first byte of the suffix.
//
// Trailing separators are recognised under the ROOT'S OWN flavor rather than a
// separator set shared by all of them, which is what keeps a POSIX directory whose
// name ends in a literal backslash intact. Under a POSIX flavor the backslash is an
// ordinary file-name byte, so trimming it would delete the last byte of the workspace
// identity: every legitimate child would stop matching, and every path beneath the
// similarly named directory WITHOUT that byte would start matching.
func matchableRoot(root string) string {
	parsed, reason := ClassifyPath(root)
	if reason != SkipReasonNone {
		return root
	}
	volumeLen := len(parsed.Root)
	trimmed := root
	for len(trimmed) > volumeLen && isFlavorSeparator(parsed.Flavor, trimmed[len(trimmed)-1]) {
		trimmed = trimmed[:len(trimmed)-1]
	}
	return trimmed
}

// joinRootSuffix appends a path suffix to a root without inventing, dropping, or
// doubling the boundary separator, so expansion reproduces the client's bytes.
//
// The one directional exception is an empty suffix: expansion then returns the
// mapping's RealRoot spelling, which is design-mandated (design.md 152) and is not
// byte-reversible when RealRoot itself carries a trailing separator the client did
// not send. A non-empty suffix is always preserved in full, including a lone
// boundary separator.
//
// The boundary separator is recognised under the ROOT'S OWN flavor, for the same
// reason the trailing trim above is: a POSIX root may end in an ordinary backslash,
// and treating that byte as the boundary would consume the client's leading separator
// and join two paths into one name.
func joinRootSuffix(flavor PathFlavor, root, suffix string) string {
	switch {
	case suffix == "":
		return root
	case isFlavorSeparator(flavor, root[len(root)-1]):
		// The root already spells the boundary separator the suffix repeats, so it
		// is not doubled. A suffix that is nothing but that separator needs no
		// byte of its own.
		if len(suffix) == 1 {
			return root
		}
		return root + suffix[1:]
	default:
		// The root ends on a name, so the client sent a boundary separator this
		// implementation would otherwise drop.
		return root + suffix
	}
}

// isFlavorSeparator reports whether b ends a path segment under the flavor's own rules.
//
// This is the one place the shared [isSeparator] predicate is deliberately NOT used,
// and the difference is the whole POSIX-versus-Windows split of design.md 150: POSIX has
// exactly one separator and treats a backslash as an ordinary byte, while every Windows
// flavor accepts both interchangeably. Reserved-alias recognition stays stricter than both
// on purpose - see [takeSegment] - but ordinary root matching must not be, because the
// bytes this predicate skips are part of a POSIX workspace's identity.
func isFlavorSeparator(flavor PathFlavor, b byte) bool {
	if flavor == FlavorPOSIX {
		return b == slashSeparator
	}
	return isSeparator(b)
}

// stripRootPrefix matches root against a leading region of path on a
// path-segment boundary and returns the untouched suffix.
//
// POSIX compares case-sensitively and treats `/` as the only separator, so a
// backslash stays an ordinary file-name byte. Windows flavors compare ASCII
// case-insensitively and treat both separators as equivalent.
func stripRootPrefix(path string, flavor PathFlavor, root string) (string, bool) {
	if root == "" || len(path) < len(root) {
		return "", false
	}
	if flavor == FlavorPOSIX {
		if path[:len(root)] != root {
			return "", false
		}
		rest := path[len(root):]
		if rest != "" && rest[0] != slashSeparator {
			return "", false
		}
		return rest, true
	}
	for i := range len(root) {
		if !equalWindowsByte(root[i], path[i]) {
			return "", false
		}
	}
	rest := path[len(root):]
	if rest != "" && !isSeparator(rest[0]) {
		return "", false
	}
	return rest, true
}

// equalWindowsByte compares two path bytes under Windows matching rules: ASCII
// case-insensitivity plus separator equivalence. Non-ASCII bytes never fold, so no
// Unicode equivalence is applied.
func equalWindowsByte(a, b byte) bool {
	if a == b {
		return true
	}
	if isSeparator(a) && isSeparator(b) {
		return true
	}
	return toLowerASCII(a) == toLowerASCII(b)
}

// canonicalRootIdentity computes the versioned, path-flavor-aware identity of a
// parsed project root.
//
// The flavor is carried separately in the tag payload, so a normal and an
// extended Windows spelling of one directory never share an identity. POSIX
// identity preserves case and every other byte and only drops non-root trailing
// separators; Windows identity normalizes `/` to `\` and ASCII-case-folds before
// dropping non-volume trailing separators. No dot segment, symlink, short name,
// or filesystem state is ever resolved.
func canonicalRootIdentity(parsed ParsedPath) string {
	joined := parsed.Root + parsed.Rest
	if parsed.Flavor == FlavorPOSIX {
		trimmed := strings.TrimRight(joined, "/")
		if trimmed == "" {
			// `/` is itself a complete root and keeps its separator.
			return string(slashSeparator)
		}
		return trimmed
	}
	identity := normalizeWindowsIdentity(joined)
	// Separator normalization is byte-wise, so the spelled volume keeps its
	// length and marks the floor below which trailing separators are not removed.
	volumeLen := len(parsed.Root)
	switch parsed.Flavor {
	case FlavorWindowsDrive, FlavorWindowsExtendedDrive:
		// A drive volume is spelled as `C:` or `\\?\C:`, which names no absolute
		// root on its own, so the boundary separator that follows it belongs to
		// the volume and survives the trim. A UNC volume is spelled with its
		// share name and is already a complete root, so every separator after it
		// is a non-volume trailing separator.
		volumeLen++
	}
	for volumeLen < len(identity) && identity[len(identity)-1] == backslashSeparator {
		identity = identity[:len(identity)-1]
	}
	return identity
}

// normalizeWindowsIdentity maps `/` to `\` and ASCII-case-folds every byte of a
// canonical Windows identity. Non-ASCII bytes are returned unchanged, so no
// Unicode case folding or equivalence can enter the tag.
func normalizeWindowsIdentity(s string) string {
	out := make([]byte, len(s))
	for i := range len(s) {
		c := s[i]
		if c == slashSeparator {
			c = backslashSeparator
		}
		out[i] = toLowerASCII(c)
	}
	return string(out)
}

// virtualRoot builds the exact V1 alias for a parsed root and its workspace tag.
// The alias keeps an OS-appropriate absolute-path shape and always ends with its
// flavor's separator, so appending an original suffix never invents one.
func virtualRoot(parsed ParsedPath, tag string) string {
	switch parsed.Flavor {
	case FlavorPOSIX:
		return string(slashSeparator) + reservedNamespaceV1 + string(slashSeparator) +
			tagPrefix + tag + string(slashSeparator)
	case FlavorWindowsDrive:
		return string(toUpperASCII(parsed.Root[0])) + string(driveColon) + string(backslashSeparator) +
			reservedNamespaceV1 + string(backslashSeparator) + tagPrefix + tag + string(backslashSeparator)
	case FlavorWindowsUNC:
		return uncPrefix + reservedNamespaceV1 + string(backslashSeparator) +
			tagPrefix + tag + string(backslashSeparator)
	case FlavorWindowsExtendedDrive:
		// Root is `\\?\C:`, so the drive letter follows the fixed extended prefix.
		drive := toUpperASCII(parsed.Root[len(uncPrefix)+len(extendedPrefix)])
		return uncPrefix + extendedPrefix + string(drive) + string(driveColon) + string(backslashSeparator) +
			reservedNamespaceV1 + string(backslashSeparator) + tagPrefix + tag + string(backslashSeparator)
	case FlavorWindowsExtendedUNC:
		return uncPrefix + extendedPrefix + uncMarker + string(backslashSeparator) +
			reservedNamespaceV1 + string(backslashSeparator) + tagPrefix + tag + string(backslashSeparator)
	default:
		return ""
	}
}

// workspaceTag derives the 96-bit workspace identity:
//
//	digest = SHA-256("aip:path-virtualization:v1\x00" + flavorID + "\x00" + canonicalRootIdentity)
//	tag    = lower-case base32-no-padding(digest[0:12])
//
// flavorID is the decimal PathFlavor value, so the fixed V1 tag algorithm depends
// on no mutable process state: not a B-leg ID, provider, model, retry ordinal,
// trace ID, or previously stored mapping.
func workspaceTag(flavor PathFlavor, canonicalIdentity string) string {
	digest := sha256.Sum256([]byte(tagDomainV1 + tagFieldSeparator +
		strconv.FormatUint(uint64(flavor), 10) + tagFieldSeparator +
		canonicalIdentity))
	return strings.ToLower(tagEncoding.EncodeToString(digest[:workspaceTagBytes]))
}

// toUpperASCII folds a lower-case ASCII byte to its upper-case form. Non-ASCII
// bytes are returned unchanged.
func toUpperASCII(c byte) byte {
	if c >= 'a' && c <= 'z' {
		return c - upperASCIIOffset
	}
	return c
}
