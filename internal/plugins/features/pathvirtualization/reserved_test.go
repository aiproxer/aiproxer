//nolint:misspell // Fixtures spell path segments like "other-repository"; the linter reads the "ther" inside "other" as "there". Path data, not prose.
package pathvirtualization_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
)

// Bounded outcomes used by the reserved-alias tables. The labels are the closed
// vocabulary of design.md 408; a rejection never carries path bytes.
const (
	wantExpanded  = pathvirtualization.ExpandResultExpanded
	wantUntouched = pathvirtualization.ExpandResultNotApplicable
	wantMalformed = pathvirtualization.ExpandResultMalformedReservedAlias
	wantMismatch  = pathvirtualization.ExpandResultWorkspaceMismatch
	// wantNoPath is the only path a rejection may return: a rejected alias yields
	// no path value at all, so a caller cannot release it to the client by
	// accident even when it ignores the result code.
	wantNoPath = ""
)

// workspace tags of the golden roots in mapping_test.go, re-spelled here so this
// file's vectors read independently of the derivation that produced them
// (design.md 177-182).
const (
	posixTag         = "hr7xnivm5at2ku356qya"
	driveTag         = "nlx2ikypfzjn6gxtinya"
	uncTag           = "ilzgm4nbs56k63mipfbq"
	extendedDriveTag = "udyg5rzsrbtqzrs27oga"
	extendedUNCTag   = "2igboqkmbmwvv43ql6xa"
	// shortRootTag belongs to the golden short root `/a/b`, whose alias is never
	// activated because it is not shorter than the root it would replace.
	shortRootTag = "czzxuetd5vhpsyn2qq7q"
	// foreignTag is a well-formed 20-character tag of the unpadded base32
	// alphabet that no root in these fixtures derives.
	foreignTag = "aaaaaaaaaaaaaaaaaaaa"
)

// The golden roots whose aliases and tags are pinned in mapping_test.go.
const (
	posixRoot         = `/home/dev/projects/aiproxer-example-project`
	driveRoot         = `C:\Users\dev\source\repos\aiproxer-example-project`
	uncRoot           = `\\build01\team\source\repos\aiproxer-example-project`
	extendedDriveRoot = `\\?\C:\Users\dev\source\repos\aiproxer-example-project`
	extendedUNCRoot   = `\\?\UNC\build01\team\source\repos\aiproxer-example-project`
	// shortRoot is usable but its alias is not beneficial, so it never emits one.
	shortRoot = `/a/b`
)

// reservedAliasCase is one row of the reserved-alias recognition table.
type reservedAliasCase struct {
	name string
	root string
	// rootRejected marks a row whose root is deliberately unusable, so the mapping
	// under test is the zero mapping.
	rootRejected bool
	path         string
	wantPath     string
	wantRes      pathvirtualization.ExpandResult
}

// reservedAliasCases pins the reserved-alias rule of design.md 186-195: a
// syntactically recognized V1 reserved alias is resolved against the current
// mapping before any ordinary prefix matching, and every recognized alias ends in
// exactly one of three bounded outcomes.
func reservedAliasCases() []reservedAliasCase {
	return []reservedAliasCase{
		// The alias of the current mapping expands, once per flavor.
		{
			name:     "posix_current_workspace_alias_expands",
			root:     posixRoot,
			path:     `/.__aip_v1__/w_` + posixTag + `/src/main.go`,
			wantPath: posixRoot + `/src/main.go`,
			wantRes:  wantExpanded,
		},
		{
			name:     "drive_current_workspace_alias_expands",
			root:     driveRoot,
			path:     `C:\.__aip_v1__\w_` + driveTag + `\src\main.go`,
			wantPath: driveRoot + `\src\main.go`,
			wantRes:  wantExpanded,
		},
		{
			name:     "unc_current_workspace_alias_expands",
			root:     uncRoot,
			path:     `\\.__aip_v1__\w_` + uncTag + `\src\main.go`,
			wantPath: uncRoot + `\src\main.go`,
			wantRes:  wantExpanded,
		},
		{
			name:     "extended_drive_current_workspace_alias_expands",
			root:     extendedDriveRoot,
			path:     `\\?\C:\.__aip_v1__\w_` + extendedDriveTag + `\src\main.go`,
			wantPath: extendedDriveRoot + `\src\main.go`,
			wantRes:  wantExpanded,
		},
		{
			name:     "extended_unc_current_workspace_alias_expands",
			root:     extendedUNCRoot,
			path:     `\\?\UNC\.__aip_v1__\w_` + extendedUNCTag + `\src\main.go`,
			wantPath: extendedUNCRoot + `\src\main.go`,
			wantRes:  wantExpanded,
		},
		{
			name:     "unc_alias_with_forward_slash_separators_expands",
			root:     uncRoot,
			path:     `\\.__aip_v1__/w_` + uncTag + `/src/main.go`,
			wantPath: uncRoot + `/src/main.go`,
			wantRes:  wantExpanded,
		},
		{
			name:     "extended_unc_alias_with_forward_slash_separators_expands",
			root:     extendedUNCRoot,
			path:     `\\?\UNC/.__aip_v1__/w_` + extendedUNCTag + `/src/main.go`,
			wantPath: extendedUNCRoot + `/src/main.go`,
			wantRes:  wantExpanded,
		},
		{
			// A POSIX `//` anchor still names the same absolute namespace, so the
			// reserved marker is recognized below it rather than skipped.
			name:     "posix_repeated_anchor_separator_alias_expands",
			root:     posixRoot,
			path:     `//.__aip_v1__/w_` + posixTag + `/src/main.go`,
			wantPath: posixRoot + `/src/main.go`,
			wantRes:  wantExpanded,
		},
		{
			// Windows flavors compare ASCII case-insensitively and treat both
			// separators as equivalent, so the alias spelling is tolerated.
			name:     "drive_alias_is_case_and_separator_tolerant",
			root:     driveRoot,
			path:     `c:/.__aip_v1__/w_` + driveTag + `\src\main.go`,
			wantPath: driveRoot + `\src\main.go`,
			wantRes:  wantExpanded,
		},
		{
			name:     "drive_upper_case_alias_spelling_is_recognized",
			root:     driveRoot,
			path:     `C:\.__AIP_V1__\W_NLX2IKYPFZJN6GXTINYA\src\main.go`,
			wantPath: driveRoot + `\src\main.go`,
			wantRes:  wantExpanded,
		},

		// A recognized reserved namespace whose tag segment is not exactly the
		// frozen `w_` + 20-character tag form is malformed, never expanded and
		// never handed back as an ordinary client path.
		{
			name:    "posix_short_tag_is_malformed",
			root:    posixRoot,
			path:    `/.__aip_v1__/w_tooshort/src/main.go`,
			wantRes: wantMalformed,
		},
		{
			name:    "posix_long_tag_is_malformed",
			root:    posixRoot,
			path:    `/.__aip_v1__/w_` + posixTag + `X/src/main.go`,
			wantRes: wantMalformed,
		},
		{
			name:    "posix_tag_outside_the_base32_alphabet_is_malformed",
			root:    posixRoot,
			path:    `/.__aip_v1__/w_ylfucd77chy74zh3qw1!/src/main.go`,
			wantRes: wantMalformed,
		},
		{
			name:    "posix_tag_without_the_fixed_prefix_is_malformed",
			root:    posixRoot,
			path:    `/.__aip_v1__/` + posixTag + `/src/main.go`,
			wantRes: wantMalformed,
		},
		{
			// Exactly the right length, but not the fixed `w_` prefix.
			name:    "posix_wrong_tag_prefix_is_malformed",
			root:    posixRoot,
			path:    `/.__aip_v1__/x_` + posixTag + `/src/main.go`,
			wantRes: wantMalformed,
		},
		{
			name:    "posix_missing_tag_segment_is_malformed",
			root:    posixRoot,
			path:    `/.__aip_v1__/src/main.go`,
			wantRes: wantMalformed,
		},
		{
			name:    "posix_bare_marker_is_malformed",
			root:    posixRoot,
			path:    `/.__aip_v1__`,
			wantRes: wantMalformed,
		},
		{
			name:    "posix_marker_with_trailing_separator_is_malformed",
			root:    posixRoot,
			path:    `/.__aip_v1__/`,
			wantRes: wantMalformed,
		},
		{
			name:    "drive_malformed_tag_is_malformed",
			root:    driveRoot,
			path:    `C:\.__aip_v1__\w_tooshort\src\main.go`,
			wantRes: wantMalformed,
		},
		{
			// The reserved marker is the UNC server name, so the tag is the share.
			name:    "unc_malformed_share_is_malformed",
			root:    uncRoot,
			path:    `\\.__aip_v1__\projects\aiproxer-example-project`,
			wantRes: wantMalformed,
		},
		{
			name:    "extended_unc_malformed_share_is_malformed",
			root:    extendedUNCRoot,
			path:    `\\?\UNC\.__aip_v1__\projects\src\main.go`,
			wantRes: wantMalformed,
		},
		{
			name:    "extended_drive_malformed_tag_is_malformed",
			root:    extendedDriveRoot,
			path:    `\\?\C:\.__aip_v1__\w_tooshort\src\main.go`,
			wantRes: wantMalformed,
		},

		// A well-formed alias of another workspace, or of an incompatible alias
		// form, is a workspace mismatch: it is never expanded against the current
		// root and never reinterpreted as a client path.
		{
			name:    "posix_foreign_workspace_tag_is_a_mismatch",
			root:    posixRoot,
			path:    `/.__aip_v1__/w_` + foreignTag + `/src/main.go`,
			wantRes: wantMismatch,
		},
		{
			name:    "drive_foreign_workspace_tag_is_a_mismatch",
			root:    driveRoot,
			path:    `C:\.__aip_v1__\w_` + foreignTag + `\src\main.go`,
			wantRes: wantMismatch,
		},
		{
			name:    "unc_foreign_workspace_tag_is_a_mismatch",
			root:    uncRoot,
			path:    `\\.__aip_v1__\w_` + foreignTag + `\src\main.go`,
			wantRes: wantMismatch,
		},
		{
			name:    "extended_unc_foreign_workspace_tag_is_a_mismatch",
			root:    extendedUNCRoot,
			path:    `\\?\UNC\.__aip_v1__\w_` + foreignTag + `\src\main.go`,
			wantRes: wantMismatch,
		},
		{
			name:    "posix_alias_under_a_drive_mapping_is_a_mismatch",
			root:    driveRoot,
			path:    `/.__aip_v1__/w_` + posixTag + `/src/main.go`,
			wantRes: wantMismatch,
		},
		{
			name:    "drive_alias_under_a_posix_mapping_is_a_mismatch",
			root:    posixRoot,
			path:    `C:\.__aip_v1__\w_` + driveTag + `\src\main.go`,
			wantRes: wantMismatch,
		},
		{
			// A normal and an extended Windows spelling of one directory are
			// distinct identities, so the alias forms never interchange.
			name:    "extended_drive_alias_under_a_drive_mapping_is_a_mismatch",
			root:    driveRoot,
			path:    `\\?\C:\.__aip_v1__\w_` + extendedDriveTag + `\src\main.go`,
			wantRes: wantMismatch,
		},
		{
			name:    "drive_alias_under_an_extended_drive_mapping_is_a_mismatch",
			root:    extendedDriveRoot,
			path:    `C:\.__aip_v1__\w_` + driveTag + `\src\main.go`,
			wantRes: wantMismatch,
		},
		{
			name:    "unc_alias_under_an_extended_unc_mapping_is_a_mismatch",
			root:    extendedUNCRoot,
			path:    `\\.__aip_v1__\w_` + uncTag + `\src\main.go`,
			wantRes: wantMismatch,
		},
		{
			// Same flavor, same tag spelling, different drive: the drive form is
			// part of the alias identity.
			name:    "different_drive_alias_is_a_mismatch",
			root:    driveRoot,
			path:    `D:\.__aip_v1__\w_` + driveTag + `\src\main.go`,
			wantRes: wantMismatch,
		},
		{
			// A usable root whose alias is not beneficial never emits one, so an
			// alias carrying its tag still does not name a current alias root.
			name:    "inactive_mapping_rejects_its_own_tag_alias",
			root:    shortRoot,
			path:    `/.__aip_v1__/w_` + shortRootTag + `/src/main.go`,
			wantRes: wantMismatch,
		},
		{
			// With no usable mapping at all, no reserved alias can be resolved.
			name:         "rejected_root_rejects_every_reserved_alias",
			root:         `relative\project`,
			rootRejected: true,
			path:         `/.__aip_v1__/w_` + posixTag + `/src/main.go`,
			wantRes:      wantMismatch,
		},

		// A volume-looking head that the classifier reads as ordinary segments must
		// not be able to hide the fixed marker one level deeper. Each row below is
		// a spelling of one alias root under a different anchor, so a stale alias
		// written this way is rejected with a bounded reason and no path value
		// rather than being handed back to the caller unchanged (requirement 4.4).
		{
			// This build's own tag, but spelled as a Windows drive alias, so the
			// flavor alone rejects it under a POSIX mapping.
			name:    "posix_anchor_drive_volume_alias_is_a_mismatch",
			root:    posixRoot,
			path:    `/C:/.__aip_v1__/w_` + posixTag + `/src/main.go`,
			wantRes: wantMismatch,
		},
		{
			name:    "posix_anchor_drive_volume_foreign_tag_is_a_mismatch",
			root:    posixRoot,
			path:    `/C:/.__aip_v1__/w_` + foreignTag + `/src/main.go`,
			wantRes: wantMismatch,
		},
		{
			// The marker is what makes the path reserved, so a short tag below a
			// volume-looking head is malformed rather than passed through.
			name:    "posix_anchor_drive_volume_short_tag_is_malformed",
			root:    posixRoot,
			path:    `/C:/.__aip_v1__/w_short/src/main.go`,
			wantRes: wantMalformed,
		},
		{
			name:    "posix_anchor_extended_drive_alias_is_a_mismatch",
			root:    posixRoot,
			path:    `//?/C:/.__aip_v1__/w_` + foreignTag + `/src/main.go`,
			wantRes: wantMismatch,
		},
		{
			name:    "posix_anchor_extended_unc_alias_is_a_mismatch",
			root:    posixRoot,
			path:    `//?/UNC/.__aip_v1__/w_` + foreignTag + `/src/main.go`,
			wantRes: wantMismatch,
		},
		{
			// A single leading separator is not `\\?\`, but the marker still sits at
			// the alias-root position of that head, so it is recognized and rejected
			// instead of reaching the caller.
			name:    "posix_anchor_single_separator_extended_drive_is_a_mismatch",
			root:    posixRoot,
			path:    `/?/C:/.__aip_v1__/w_` + posixTag + `/src/main.go`,
			wantRes: wantMismatch,
		},
		{
			// A bare `C:` head is a drive volume, never the extended one, so the
			// current tag under it is still an incompatible flavor.
			name:    "posix_anchor_drive_volume_under_an_extended_drive_mapping_is_a_mismatch",
			root:    extendedDriveRoot,
			path:    `/C:/.__aip_v1__/w_` + extendedDriveTag + `/src/main.go`,
			wantRes: wantMismatch,
		},
		{
			// The reviewer reproduction: an extended-drive alias whose anchor and
			// separators are mangled, presented to the same extended mapping.
			name:     "mangled_separator_extended_drive_alias_expands",
			root:     extendedDriveRoot,
			path:     `\\?/C:\.__aip_v1__\w_` + extendedDriveTag + `\src\main.go`,
			wantPath: extendedDriveRoot + `\src\main.go`,
			wantRes:  wantExpanded,
		},
		{
			name:     "mangled_separator_and_anchor_extended_drive_alias_expands",
			root:     extendedDriveRoot,
			path:     `\\?/C:/.__aip_v1__/w_` + extendedDriveTag + `/src/main.go`,
			wantPath: extendedDriveRoot + `/src/main.go`,
			wantRes:  wantExpanded,
		},
		{
			name:    "mangled_anchor_extended_drive_foreign_tag_is_a_mismatch",
			root:    extendedDriveRoot,
			path:    `\\?/C:\.__aip_v1__\w_` + foreignTag + `\src\main.go`,
			wantRes: wantMismatch,
		},
		{
			name:     "forward_slash_extended_drive_alias_expands",
			root:     extendedDriveRoot,
			path:     `//?/C:/.__aip_v1__/w_` + extendedDriveTag + `/src/main.go`,
			wantPath: extendedDriveRoot + `/src/main.go`,
			wantRes:  wantExpanded,
		},
		{
			name:     "forward_slash_extended_unc_alias_expands",
			root:     extendedUNCRoot,
			path:     `//?/UNC/.__aip_v1__/w_` + extendedUNCTag + `/src/main.go`,
			wantPath: extendedUNCRoot + `/src/main.go`,
			wantRes:  wantExpanded,
		},
		{
			// A `//` anchor is a POSIX path, so a UNC alias spelled that way is an
			// incompatible flavor rather than an expansion.
			name:    "forward_slash_unc_anchor_alias_is_a_mismatch",
			root:    uncRoot,
			path:    `//.__aip_v1__/w_` + uncTag + `/src/main.go`,
			wantRes: wantMismatch,
		},

		// Reserved recognition splits on either separator in every flavor, including
		// POSIX, so a backslash-mangled alias is still resolved rather than released.
		// See TestReservedRecognitionIsSeparatorAgnosticForFailClosedRecognition.
		{
			name:     "posix_backslash_mangled_alias_expands",
			root:     posixRoot,
			path:     `/.__aip_v1__\w_` + posixTag + `/src/main.go`,
			wantPath: posixRoot + `/src/main.go`,
			wantRes:  wantExpanded,
		},
		{
			name:    "posix_backslash_mangled_short_tag_is_malformed",
			root:    posixRoot,
			path:    `/.__aip_v1__\w_short/src/main.go`,
			wantRes: wantMalformed,
		},
		{
			name:    "posix_backslash_mangled_foreign_tag_is_a_mismatch",
			root:    posixRoot,
			path:    `/.__aip_v1__\w_` + foreignTag + `/src/main.go`,
			wantRes: wantMismatch,
		},

		// Ordinary paths keep their task 2.2 behavior: the reserved namespace is
		// recognized only at the alias-root position.
		{
			name:     "nested_reserved_marker_is_an_ordinary_path",
			root:     posixRoot,
			path:     `/home/dev/.__aip_v1__/w_` + posixTag + `/src/main.go`,
			wantPath: `/home/dev/.__aip_v1__/w_` + posixTag + `/src/main.go`,
			wantRes:  wantUntouched,
		},
		{
			// A volume-looking head alone reserves nothing: without the marker at the
			// alias-root position this is an ordinary path.
			name:     "volume_lookalike_head_without_the_marker_is_an_ordinary_path",
			root:     posixRoot,
			path:     `/C:/projects/aiproxer-example-project/src/main.go`,
			wantPath: `/C:/projects/aiproxer-example-project/src/main.go`,
			wantRes:  wantUntouched,
		},
		{
			name:     "extended_lookalike_head_without_the_marker_is_an_ordinary_path",
			root:     posixRoot,
			path:     `//?/C:/projects/src/main.go`,
			wantPath: `//?/C:/projects/src/main.go`,
			wantRes:  wantUntouched,
		},
		{
			// Only the exact fixed marker is reserved, never a name that embeds it.
			name:     "volume_lookalike_head_with_a_near_miss_marker_is_an_ordinary_path",
			root:     posixRoot,
			path:     `/C:/.__aip_v1__other/w_` + posixTag + `/src/main.go`,
			wantPath: `/C:/.__aip_v1__other/w_` + posixTag + `/src/main.go`,
			wantRes:  wantUntouched,
		},
		{
			// `<digit>:` is not a Windows volume, so the head is an ordinary POSIX
			// directory name and the marker below it is not at an alias root.
			name:     "non_drive_volume_head_is_an_ordinary_path",
			root:     posixRoot,
			path:     `/1:/.__aip_v1__/w_` + posixTag + `/src/main.go`,
			wantPath: `/1:/.__aip_v1__/w_` + posixTag + `/src/main.go`,
			wantRes:  wantUntouched,
		},
		{
			// `C:relative` names no absolute volume, so the head is a relative
			// segment and the marker is not at an alias root.
			name:     "drive_relative_volume_head_is_an_ordinary_path",
			root:     posixRoot,
			path:     `/C:.__aip_v1__/w_` + posixTag + `/src/main.go`,
			wantPath: `/C:.__aip_v1__/w_` + posixTag + `/src/main.go`,
			wantRes:  wantUntouched,
		},
		{
			// The `?` of the extended anchor ends its own segment. A head that keeps
			// it glued to something else names no extended volume.
			name:     "extended_anchor_glued_to_a_name_is_an_ordinary_path",
			root:     posixRoot,
			path:     `/??/.__aip_v1__/w_` + posixTag + `/src/main.go`,
			wantPath: `/??/.__aip_v1__/w_` + posixTag + `/src/main.go`,
			wantRes:  wantUntouched,
		},
		{
			// `?` introduces either a drive volume or the `UNC` marker; this names
			// neither, so the head is an ordinary directory.
			name:     "extended_anchor_with_a_non_volume_payload_is_an_ordinary_path",
			root:     posixRoot,
			path:     `/?./.__aip_v1__/w_` + posixTag + `/src/main.go`,
			wantPath: `/?./.__aip_v1__/w_` + posixTag + `/src/main.go`,
			wantRes:  wantUntouched,
		},
		{
			// An extended UNC volume needs a server component, so `UNC` followed
			// directly by the marker names no volume.
			name:     "extended_unc_anchor_without_a_server_component_is_an_ordinary_path",
			root:     posixRoot,
			path:     `//?/UNC.__aip_v1__/w_` + posixTag + `/src/main.go`,
			wantPath: `//?/UNC.__aip_v1__/w_` + posixTag + `/src/main.go`,
			wantRes:  wantUntouched,
		},
		{
			// An empty segment names no volume either, and empty segments are never
			// collapsed: the marker is not the alias-root position.
			name:     "extended_anchor_with_an_empty_volume_is_an_ordinary_path",
			root:     posixRoot,
			path:     `/?//.__aip_v1__/w_` + posixTag + `/src/main.go`,
			wantPath: `/?//.__aip_v1__/w_` + posixTag + `/src/main.go`,
			wantRes:  wantUntouched,
		},
		{
			// The extended anchor is the literal `?` of `\\?\`: a server merely named
			// `UNC` is not one, so the marker stays in the share position.
			name:     "unc_server_named_unc_with_the_marker_in_the_share_is_an_ordinary_path",
			root:     posixRoot,
			path:     `\\UNC\.__aip_v1__\w_` + posixTag + `\src\main.go`,
			wantPath: `\\UNC\.__aip_v1__\w_` + posixTag + `\src\main.go`,
			wantRes:  wantUntouched,
		},
		{
			// The marker is fixed V1 syntax, so no other spelling reserves it.
			name:     "another_namespace_marker_is_not_reserved",
			root:     posixRoot,
			path:     `/.__aip_v2__/w_` + posixTag + `/src/main.go`,
			wantPath: `/.__aip_v2__/w_` + posixTag + `/src/main.go`,
			wantRes:  wantUntouched,
		},
		{
			// POSIX comparison is case-sensitive, so a differently cased marker is
			// an ordinary file name rather than the reserved namespace.
			name:     "posix_upper_case_marker_is_an_ordinary_path",
			root:     posixRoot,
			path:     `/.__AIP_V1__/w_` + posixTag + `/src/main.go`,
			wantPath: `/.__AIP_V1__/w_` + posixTag + `/src/main.go`,
			wantRes:  wantUntouched,
		},
		{
			// The reserved marker occupies the UNC server position, never the share.
			name:     "unc_share_below_a_reserved_server_is_an_ordinary_path",
			root:     uncRoot,
			path:     `\\build01\.__aip_v1__\w_` + uncTag + `\src\main.go`,
			wantPath: `\\build01\.__aip_v1__\w_` + uncTag + `\src\main.go`,
			wantRes:  wantUntouched,
		},
		{
			name:     "real_path_is_never_expanded",
			root:     posixRoot,
			path:     posixRoot + `/src/main.go`,
			wantPath: posixRoot + `/src/main.go`,
			wantRes:  wantUntouched,
		},
		{
			name:     "empty_path_is_never_expanded",
			root:     posixRoot,
			path:     "",
			wantPath: "",
			wantRes:  wantUntouched,
		},
	}
}

// TestReservedAliasOutcomesAreBounded is the behavioral half of the fixed V1
// reserved namespace: every recognized alias resolves to exactly one bounded
// outcome, and no outcome is guessed.
func TestReservedAliasOutcomesAreBounded(t *testing.T) {
	t.Parallel()

	for _, tc := range reservedAliasCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mapping, reason := pathvirtualization.DeriveMapping(tc.root)
			if tc.rootRejected {
				if reason == pathvirtualization.SkipReasonNone {
					t.Fatalf("DeriveMapping(%q) accepted a root the row expects to be rejected", tc.root)
				}
			} else if reason != pathvirtualization.SkipReasonNone {
				t.Fatalf("DeriveMapping(%q) reason = %q", tc.root, reason)
			}
			wantPath := tc.wantPath
			if tc.wantRes == wantMalformed || tc.wantRes == wantMismatch {
				wantPath = wantNoPath
			}
			got, result := mapping.ExpandPath(tc.path)
			if got != wantPath {
				t.Errorf("ExpandPath(%q) = %q, want %q", tc.path, got, wantPath)
			}
			if result != tc.wantRes {
				t.Errorf("ExpandPath(%q) result = %v, want %v", tc.path, result, tc.wantRes)
			}
		})
	}
}

// TestReservedAliasRejectionReleasesNoPath proves the fail-closed half of
// requirements 1.11 and 4.4: a rejected alias produces no path value at all, so
// no caller can release the reserved namespace to the client even when it ignores
// the bounded result code.
func TestReservedAliasRejectionReleasesNoPath(t *testing.T) {
	t.Parallel()

	rejected := 0
	for _, tc := range reservedAliasCases() {
		if tc.wantRes != wantMalformed && tc.wantRes != wantMismatch {
			continue
		}
		rejected++
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mapping, _ := pathvirtualization.DeriveMapping(tc.root)
			got, result := mapping.ExpandPath(tc.path)
			if result != wantMalformed && result != wantMismatch {
				t.Fatalf("ExpandPath(%q) result = %v, want a rejection", tc.path, result)
			}
			if got != "" {
				t.Errorf("ExpandPath(%q) returned %q for a rejection, want no path value", tc.path, got)
			}
			if strings.Contains(got, reservedNamespaceV1) {
				t.Errorf("rejection returned the reserved namespace %q", got)
			}
		})
	}
	if rejected == 0 {
		t.Fatal("no rejection rows; the fail-closed property would prove nothing")
	}
}

// aliasRewrite turns one derived alias root into an alternative spelling of the
// same alias root. Every rewrite changes only separator style or the volume
// anchor, never a byte of the fixed marker or of the workspace tag.
type aliasRewrite struct {
	name    string
	rewrite func(alias string) string
}

// aliasRewrites are the separator and anchor rewrites a client can produce while
// still naming one alias root. ASCII-case rewrites are deliberately absent
// because they are flavor-dependent: Windows flavors fold ASCII case while POSIX
// does not, and both directions are pinned per flavor by the table above.
var aliasRewrites = []aliasRewrite{
	{"forward_slash_separators", func(alias string) string { return strings.ReplaceAll(alias, `\`, "/") }},
	{"backslash_separators", func(alias string) string { return strings.ReplaceAll(alias, "/", `\`) }},
	{"forward_slash_unc_anchor", func(alias string) string { return "//" + strings.TrimPrefix(alias, `\\`) }},
	{"single_separator_extended_anchor", func(alias string) string {
		return strings.Replace(alias, `\\?\`, `/?/`, 1)
	}},
	{"forward_slash_extended_anchor", func(alias string) string {
		return strings.Replace(alias, `\\?\`, `//?\`, 1)
	}},
	{"forward_slash_extended_unc_marker", func(alias string) string {
		return strings.Replace(alias, `\UNC\`, `/UNC/`, 1)
	}},
}

// TestNoSpellingOfTheOwnAliasReachesTheCaller is the fail-closed property that
// makes the ordinary-matcher guard in ExpandPath safe: a path that spells this
// mapping's own alias root is resolved by reserved parsing, whatever separator or
// anchor spelling it uses, so it is never returned to the caller unchanged.
//
// The three outcomes are checked per spelling, because only one of them is
// required: the spelling either expands to the real root, or it is rejected with a
// bounded reason and no path value. A spelling that no longer names one of the
// five supported absolute forms is outside the alias grammar entirely, and the
// caller's bounded skip reason owns it.
func TestNoSpellingOfTheOwnAliasReachesTheCaller(t *testing.T) {
	t.Parallel()

	for _, root := range []string{posixRoot, driveRoot, uncRoot, extendedDriveRoot, extendedUNCRoot} {
		t.Run(root, func(t *testing.T) {
			t.Parallel()

			mapping, reason := pathvirtualization.DeriveMapping(root)
			if reason != pathvirtualization.SkipReasonNone {
				t.Fatalf("DeriveMapping(%q) reason = %q", root, reason)
			}
			if mapping.VirtualRoot == "" {
				t.Fatalf("root %q derived no alias", root)
			}
			spellings := map[string]string{mapping.VirtualRoot: "derived_alias"}
			for _, rewrite := range aliasRewrites {
				spellings[rewrite.rewrite(mapping.VirtualRoot)] = rewrite.name
			}
			// The property must not pass vacuously: at least the derived alias itself
			// has to resolve, so the spellings under test are real inputs.
			if _, result := mapping.ExpandPath(mapping.VirtualRoot + `src/main.go`); result != pathvirtualization.ExpandResultExpanded {
				t.Fatalf("the derived alias of root %q does not expand: %v", root, result)
			}
			for spelling, origin := range spellings {
				t.Run(origin, func(t *testing.T) {
					if _, skip := pathvirtualization.ClassifyPath(spelling); skip != pathvirtualization.SkipReasonNone {
						// A spelling that is not a supported absolute form names no
						// absolute path, so the caller skips it with this bounded
						// reason. The mapper never guesses a volume for it.
						return
					}
					got, result := mapping.ExpandPath(spelling + "src/main.go")
					switch result {
					case pathvirtualization.ExpandResultNotApplicable:
						t.Fatalf("ExpandPath(%q) returned %q unchanged: the reserved namespace reached the caller",
							spelling, got)
					case pathvirtualization.ExpandResultExpanded:
						if !strings.HasPrefix(got, mapping.RealRoot) {
							t.Errorf("ExpandPath(%q) = %q, want a path under the real root %q",
								spelling, got, mapping.RealRoot)
						}
						if strings.Contains(got, reservedNamespaceV1) {
							t.Errorf("ExpandPath(%q) = %q still carries the reserved namespace", spelling, got)
						}
					default:
						if got != "" {
							t.Errorf("ExpandPath(%q) rejected with %v but returned %q", spelling, result, got)
						}
					}
				})
			}
		})
	}
}

// TestReservedRecognitionIsSeparatorAgnosticForFailClosedRecognition pins the
// separator rule reserved recognition uses, which is deliberately stricter than
// the POSIX match rule of design.md 150.
//
// Ordinary prefix matching treats a backslash on POSIX as an ordinary file-name
// byte. Reserved recognition does not: it splits segments on either separator in
// every flavor, because a marker followed by a backslash is either a mangled alias
// or a real POSIX file name, and only one of those two can be resolved safely.
// Splitting keeps the fail-closed reading (`.__aip_v1__\w_<tag>` expands or is
// rejected, never released), while POSIX-only splitting would hand the reserved
// namespace back to the caller, which requirement 4.4 forbids.
func TestReservedRecognitionIsSeparatorAgnosticForFailClosedRecognition(t *testing.T) {
	t.Parallel()

	mapping, reason := pathvirtualization.DeriveMapping(posixRoot)
	if reason != pathvirtualization.SkipReasonNone {
		t.Fatalf("DeriveMapping(%q) reason = %q", posixRoot, reason)
	}
	for _, tc := range []struct {
		name    string
		path    string
		wantRes pathvirtualization.ExpandResult
	}{
		{
			// The tag matches, so the mangled alias resolves to the real root.
			name:    "backslash_mangled_current_alias_expands",
			path:    `/.__aip_v1__\w_` + posixTag + `/src/main.go`,
			wantRes: pathvirtualization.ExpandResultExpanded,
		},
		{
			name:    "backslash_mangled_short_tag_is_malformed",
			path:    `/.__aip_v1__\w_short/src/main.go`,
			wantRes: pathvirtualization.ExpandResultMalformedReservedAlias,
		},
		{
			name:    "backslash_mangled_foreign_tag_is_a_mismatch",
			path:    `/.__aip_v1__\w_` + foreignTag + `/src/main.go`,
			wantRes: pathvirtualization.ExpandResultWorkspaceMismatch,
		},
		{
			// A backslash inside the marker is an ordinary byte: the marker does not
			// match, so nothing is reserved.
			name:    "backslash_inside_the_marker_is_not_reserved",
			path:    `/.__aip_v1_\_w_` + posixTag + `/src/main.go`,
			wantRes: pathvirtualization.ExpandResultNotApplicable,
		},
		{
			name:    "backslash_inside_the_tag_is_malformed",
			path:    `/.__aip_v1__/w_` + posixTag[:10] + `\` + posixTag[10:] + `/src/main.go`,
			wantRes: pathvirtualization.ExpandResultMalformedReservedAlias,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, result := mapping.ExpandPath(tc.path)
			if result != tc.wantRes {
				t.Fatalf("ExpandPath(%q) result = %v, want %v", tc.path, result, tc.wantRes)
			}
			switch tc.wantRes {
			case pathvirtualization.ExpandResultExpanded:
				if !strings.HasPrefix(got, posixRoot) || strings.Contains(got, reservedNamespaceV1) {
					t.Errorf("ExpandPath(%q) = %q, want a real-root path without the reserved namespace", tc.path, got)
				}
			case pathvirtualization.ExpandResultNotApplicable:
				if got != tc.path {
					t.Errorf("ExpandPath(%q) = %q, want it untouched", tc.path, got)
				}
			default:
				if got != "" {
					t.Errorf("ExpandPath(%q) = %q for a rejection, want no path value", tc.path, got)
				}
			}
		})
	}
}

// TestReservedNamespaceIsFixedSyntaxNotConfiguration pins design.md 159: the V1
// marker is an implementation contract, so exactly one marker is reserved and no
// other spelling can claim the namespace.
func TestReservedNamespaceIsFixedSyntaxNotConfiguration(t *testing.T) {
	t.Parallel()

	roots := []string{posixRoot, driveRoot, uncRoot, extendedDriveRoot, extendedUNCRoot}
	for _, root := range roots {
		mapping, reason := pathvirtualization.DeriveMapping(root)
		if reason != pathvirtualization.SkipReasonNone {
			t.Fatalf("DeriveMapping(%q) reason = %q", root, reason)
		}
		if mapping.VirtualRoot == "" {
			t.Fatalf("root %q derived no alias", root)
		}
		if got := strings.Count(mapping.VirtualRoot, reservedNamespaceV1); got != 1 {
			t.Errorf("alias %q spells the fixed marker %d times, want once", mapping.VirtualRoot, got)
		}
		// The derived alias root in its derived spelling is resolved by reserved
		// parsing, not by the ordinary matcher. The separator- and anchor-mangled
		// spellings of that same alias are covered by
		// TestNoSpellingOfTheOwnAliasReachesTheCaller, which is the invariant the
		// ordinary-matcher guard in ExpandPath rests on.
		expanded, result := mapping.ExpandPath(mapping.VirtualRoot + `probe`)
		wantReal := mapping.RealRoot + mapping.VirtualRoot[len(mapping.VirtualRoot)-1:] + `probe`
		if result != pathvirtualization.ExpandResultExpanded || expanded != wantReal {
			t.Errorf("ExpandPath(%q) = %q/%v, want %q resolved through reserved parsing",
				mapping.VirtualRoot+`probe`, expanded, result, wantReal)
		}
	}

	// A different marker is an ordinary directory name: it is neither recognized
	// as a reserved alias nor expanded, whatever tag it carries.
	for _, path := range []string{
		`/.__aip_v2__/w_` + posixTag + `/src/main.go`,
		`/.__aip__/w_` + posixTag + `/src/main.go`,
		`/.__aip_v1__x/w_` + posixTag + `/src/main.go`,
		`/__aip_v1__/w_` + posixTag + `/src/main.go`,
	} {
		mapping, reason := pathvirtualization.DeriveMapping(posixRoot)
		if reason != pathvirtualization.SkipReasonNone {
			t.Fatalf("DeriveMapping(%q) reason = %q", posixRoot, reason)
		}
		got, result := mapping.ExpandPath(path)
		if result != pathvirtualization.ExpandResultNotApplicable || got != path {
			t.Errorf("ExpandPath(%q) = %q/%v, want it untouched: only the fixed marker is reserved",
				path, got, result)
		}
	}
}

// staleWorkspaceCase is one authoritative-root change.
type staleWorkspaceCase struct {
	name string
	// rootA is the workspace the model-visible alias was emitted under.
	rootA string
	// rootB is the authoritative root that replaced it.
	rootB string
}

// staleWorkspaceCases covers the root changes requirement 6.5 enumerates: a POSIX
// change, a same-drive Windows change, a different-drive change, UNC and
// extended-path changes, and a cross-flavor change.
func staleWorkspaceCases() []staleWorkspaceCase {
	return []staleWorkspaceCase{
		{
			name:  "posix_root_change",
			rootA: posixRoot,
			rootB: `/srv/team/workspace/aiproxer-example-project`,
		},
		{
			name:  "same_drive_windows_root_change",
			rootA: driveRoot,
			rootB: `C:\Users\dev\source\repos\aiproxer-example-project-fork`,
		},
		{
			name:  "different_drive_windows_root_change",
			rootA: driveRoot,
			rootB: `D:\Users\dev\source\repos\aiproxer-example-project`,
		},
		{
			name:  "unc_root_change",
			rootA: uncRoot,
			rootB: `\\build01\team\source\repos\other-workspace`,
		},
		{
			name:  "extended_drive_root_change",
			rootA: extendedDriveRoot,
			rootB: `\\?\C:\Users\dev\source\repos\other-workspace`,
		},
		{
			name:  "extended_unc_root_change",
			rootA: extendedUNCRoot,
			rootB: `\\?\UNC\build01\team\source\repos\other-workspace`,
		},
		{
			name:  "unc_to_posix_root_change",
			rootA: uncRoot,
			rootB: posixRoot,
		},
	}
}

// TestStaleWorkspaceAliasIsRejectedAfterAuthoritativeRootChange is the critical
// regression of design.md 431 and requirements 6.5 and 4.4: an alias emitted under
// workspace A is rejected after the authoritative root becomes workspace B, and
// the rejection never yields a path rooted under B.
func TestStaleWorkspaceAliasIsRejectedAfterAuthoritativeRootChange(t *testing.T) {
	t.Parallel()

	for _, tc := range staleWorkspaceCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			before, beforeReason := pathvirtualization.DeriveMapping(tc.rootA)
			if beforeReason != pathvirtualization.SkipReasonNone || before.VirtualRoot == "" {
				t.Fatalf("root A %q is not an active workspace: %+v/%q", tc.rootA, before, beforeReason)
			}
			after, afterReason := pathvirtualization.DeriveMapping(tc.rootB)
			if afterReason != pathvirtualization.SkipReasonNone || after.VirtualRoot == "" {
				t.Fatalf("root B %q is not an active workspace: %+v/%q", tc.rootB, after, afterReason)
			}
			// Requirement 6.5: a changed root derives a different tag, which is the
			// only thing that distinguishes a stale alias from a current one.
			if before.WorkspaceTag == after.WorkspaceTag {
				t.Fatalf("roots %q and %q derive the same workspace tag", tc.rootA, tc.rootB)
			}

			// The stale alias is produced by the mapper itself, never hand-written.
			real := tc.rootA + `/src/main.go`
			stale, changed := before.VirtualizePath(real)
			if !changed {
				t.Fatalf("root A %q did not virtualize %q", tc.rootA, real)
			}
			for _, alias := range []string{stale, before.VirtualRoot} {
				got, result := after.ExpandPath(alias)
				if result != pathvirtualization.ExpandResultWorkspaceMismatch {
					t.Errorf("root B %q expanding stale alias %q result = %v, want %v",
						tc.rootB, alias, result, pathvirtualization.ExpandResultWorkspaceMismatch)
				}
				if got != "" {
					t.Errorf("root B %q expanding stale alias %q returned %q, want no path value",
						tc.rootB, alias, got)
				}
				if strings.Contains(got, tc.rootB) {
					t.Errorf("stale alias %q expanded to %q under the new root %q", alias, got, tc.rootB)
				}
				if strings.Contains(got, reservedNamespaceV1) {
					t.Errorf("stale alias %q leaked the reserved namespace as %q", alias, got)
				}
			}

			// The rejection is alias-specific: root B still expands its own alias,
			// so the mapper is not failing closed by refusing everything. The alias
			// already ends with its flavor's separator, so the appended suffix
			// carries none.
			own := after.VirtualRoot + `src/main.go`
			wantReal := tc.rootB + after.VirtualRoot[len(after.VirtualRoot)-1:] + `src/main.go`
			expanded, result := after.ExpandPath(own)
			if result != pathvirtualization.ExpandResultExpanded {
				t.Fatalf("root B %q cannot expand its own alias %q: %v", tc.rootB, own, result)
			}
			if expanded != wantReal {
				t.Errorf("root B expanded %q to %q, want %q", own, expanded, wantReal)
			}
		})
	}
}

// reservedRootCase is one root that does or does not collide with the fixed V1
// reserved namespace.
type reservedRootCase struct {
	name string
	root string
	// wantCollision is true when the root is spelled inside the reserved
	// namespace and must therefore be rejected with a bounded reason.
	wantCollision bool
}

// reservedRootCases enumerates requirement 1.8's reserved-namespace collision
// across every flavor, plus the near-miss roots that must keep working: a marker
// below the first segment, a UNC share named like the marker, and a client root
// whose own name merely embeds the marker.
func reservedRootCases() []reservedRootCase {
	return []reservedRootCase{
		// The known open case from the task 2.2 handoff: this root derived an active
		// alias nested inside the reserved namespace, which made every real path
		// under it look like a reserved alias to the expansion path.
		{name: "drive_root_is_itself_an_alias_root", root: `C:\.__aip_v1__\w_abcdefghijklmnopqrstxx`, wantCollision: true},
		{name: "posix_root_is_itself_an_alias_root", root: `/.__aip_v1__/w_` + posixTag, wantCollision: true},
		{name: "posix_root_is_the_reserved_namespace", root: `/.__aip_v1__`, wantCollision: true},
		{name: "posix_root_below_the_reserved_namespace", root: `/.__aip_v1__/projects/aiproxer-example-project`, wantCollision: true},
		{name: "drive_root_below_the_reserved_namespace", root: `C:\.__aip_v1__\projects\aiproxer-example-project`, wantCollision: true},
		{name: "extended_drive_root_below_the_reserved_namespace", root: `\\?\C:\.__aip_v1__\w_` + extendedDriveTag, wantCollision: true},
		{name: "drive_root_is_only_the_reserved_namespace", root: `C:\.__aip_v1__`, wantCollision: true},
		{name: "unc_server_is_the_reserved_namespace", root: `\\.__aip_v1__\share\aiproxer-example-project`, wantCollision: true},
		{name: "unc_share_root_is_the_reserved_namespace", root: `\\.__aip_v1__\share`, wantCollision: true},
		{name: "extended_unc_server_is_the_reserved_namespace", root: `\\?\UNC\.__aip_v1__\share\aiproxer-example-project`, wantCollision: true},
		{name: "posix_root_with_a_trailing_separator", root: `/.__aip_v1__/projects/aiproxer-example-project/`, wantCollision: true},

		// Near misses: the marker is not at the alias-root position, so the root
		// keeps an ordinary alias whose expansion stays unambiguous.
		{name: "posix_marker_below_the_first_segment", root: `/home/dev/.__aip_v1__/aiproxer-example-project`},
		{name: "posix_client_root_named_after_the_namespace", root: `/home/dev/.__aip_v1__`},
		// The marker is a directory name one level below a real root, and the tag
		// segment is a directory of its own: this is a legitimate client root.
		{name: "posix_marker_and_tag_below_a_real_root", root: `/home/dev/.__aip_v1__/w_` + posixTag + `/x`},
		{name: "drive_marker_below_the_first_segment", root: `C:\Users\dev\.__aip_v1__\aiproxer-example-project`},
		{name: "unc_share_named_after_the_namespace", root: `\\build01\.__aip_v1__\aiproxer-example-project`},
		{name: "unc_deeper_marker_segment", root: `\\build01\team\.__aip_v1__\aiproxer-example-project`},
		{name: "posix_root_merely_containing_the_marker", root: `/home/dev/my.__aip_v1__project/aiproxer-example-project`},
		{name: "posix_other_namespace_marker", root: `/.__aip_v2__/projects/aiproxer-example-project`},
	}
}

// TestDeriveMappingRejectsReservedNamespaceRoots pins requirement 1.8: a root that
// collides with the fixed V1 reserved namespace disables rewriting with a bounded
// reason code instead of deriving an alias that could be rebound.
func TestDeriveMappingRejectsReservedNamespaceRoots(t *testing.T) {
	t.Parallel()

	for _, tc := range reservedRootCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, reason := pathvirtualization.DeriveMapping(tc.root)
			if !tc.wantCollision {
				if reason != pathvirtualization.SkipReasonNone {
					t.Fatalf("DeriveMapping(%q) reason = %q, want %q: the marker is not at the alias-root position",
						tc.root, reason, pathvirtualization.SkipReasonNone)
				}
				if got.RealRoot != tc.root {
					t.Errorf("RealRoot = %q, want the original spelling %q", got.RealRoot, tc.root)
				}
				return
			}
			if reason != pathvirtualization.SkipReasonReservedNamespaceCollision {
				t.Fatalf("DeriveMapping(%q) reason = %q, want %q", tc.root, reason,
					pathvirtualization.SkipReasonReservedNamespaceCollision)
			}
			if got != (pathvirtualization.Mapping{}) {
				t.Fatalf("colliding root %q returned %+v, want a zero Mapping", tc.root, got)
			}
			// No alias means no outbound mutation, and any reserved alias a model
			// emits still fails closed instead of resolving against this root.
			path := tc.root + `/src/main.go`
			if virtual, changed := got.VirtualizePath(path); changed || virtual != path {
				t.Errorf("colliding root %q rewrote %q to %q", tc.root, path, virtual)
			}
			expanded, result := got.ExpandPath(`/.__aip_v1__/w_` + posixTag + `/src/main.go`)
			if result != pathvirtualization.ExpandResultWorkspaceMismatch || expanded != "" {
				t.Errorf("colliding root %q expanded a reserved alias to %q (%v)", tc.root, expanded, result)
			}
		})
	}
}

// TestReservedNamespaceCollisionDisablesVirtualization proves requirement 1.8 at
// the behavioral level for the case the task 2.2 handoff left open: the colliding
// root must not derive a usable alias, so no path under it can ever be written
// into the reserved namespace.
func TestReservedNamespaceCollisionDisablesVirtualization(t *testing.T) {
	t.Parallel()

	const root = `C:\.__aip_v1__\w_abcdefghijklmnopqrstxx`
	mapping, reason := pathvirtualization.DeriveMapping(root)
	if reason != pathvirtualization.SkipReasonReservedNamespaceCollision {
		t.Fatalf("DeriveMapping(%q) reason = %q, want %q", root, reason,
			pathvirtualization.SkipReasonReservedNamespaceCollision)
	}
	if mapping.VirtualRoot != "" || mapping.WorkspaceTag != "" {
		t.Fatalf("colliding root %q derived %+v, want no alias and no tag", root, mapping)
	}
	for _, path := range []string{
		root,
		root + `\src\main.go`,
		root + `/src/main.go`,
	} {
		virtual, changed := mapping.VirtualizePath(path)
		if changed || virtual != path {
			t.Errorf("VirtualizePath(%q) = %q (changed=%v), want it untouched", path, virtual, changed)
		}
	}
}

// TestReservedReasonLabelsAreBounded keeps the rejection outcomes a fixed,
// path-content-free dimension suitable for metrics (design.md 400 and 408).
func TestReservedReasonLabelsAreBounded(t *testing.T) {
	t.Parallel()

	want := []struct {
		result pathvirtualization.ExpandResult
		label  string
	}{
		{pathvirtualization.ExpandResultNotApplicable, "not_applicable"},
		{pathvirtualization.ExpandResultExpanded, "expanded"},
		{pathvirtualization.ExpandResultMalformedReservedAlias, "malformed_reserved_alias"},
		{pathvirtualization.ExpandResultWorkspaceMismatch, "workspace_mismatch"},
	}
	for _, tc := range want {
		if got := tc.result.String(); got != tc.label {
			t.Errorf("ExpandResult(%d).String() = %q, want %q", tc.result, got, tc.label)
		}
	}
	if got := pathvirtualization.ExpandResult(99).String(); got != "unknown" {
		t.Errorf("unbounded reserved-alias label = %q, want %q", got, "unknown")
	}
	if got := pathvirtualization.SkipReasonReservedNamespaceCollision; got != "reserved_namespace_collision" {
		t.Errorf("collision reason = %q, want %q", got, "reserved_namespace_collision")
	}
}

// TestReservedNamespaceDetectionDoesNotDependOnHostAuthority keeps the collision
// rule lexical: the same roots are classified identically whatever separator the
// host uses, so the reserved namespace can never be smuggled past the check with a
// host-specific spelling.
func TestReservedNamespaceDetectionDoesNotDependOnHostAuthority(t *testing.T) {
	t.Parallel()

	cases := []struct {
		root          string
		wantCollision bool
	}{
		{root: `C:\.__aip_v1__\w_abcdefghijklmnopqrstxx`, wantCollision: true},
		{root: `C:/.__aip_v1__/w_abcdefghijklmnopqrstxx`, wantCollision: true},
		{root: `c:\.__aip_v1__\w_abcdefghijklmnopqrstxx`, wantCollision: true},
		{root: `\\?\C:\.__aip_v1__\w_abcdefghijklmnopqrstxx`, wantCollision: true},
		{root: `\\.__aip_v1__\w_abcdefghijklmnopqrstxx`, wantCollision: true},
		{root: `\\?\UNC\.__aip_v1__\w_abcdefghijklmnopqrstxx`, wantCollision: true},
		// A `//`-spelled POSIX anchor still names the reserved namespace.
		{root: `//.__aip_v1__/w_abcdefghijklmnopqrstxx`, wantCollision: true},
		{root: `C:\Users\dev\.__aip_v1__\w_abcdefghijklmnopqrstxx`},
	}
	for _, tc := range cases {
		t.Run(tc.root, func(t *testing.T) {
			t.Parallel()

			_, reason := pathvirtualization.DeriveMapping(tc.root)
			collision := reason == pathvirtualization.SkipReasonReservedNamespaceCollision
			if collision != tc.wantCollision {
				t.Fatalf("DeriveMapping(%q) reason = %q, collision = %v, want %v",
					tc.root, reason, collision, tc.wantCollision)
			}
		})
	}
}
