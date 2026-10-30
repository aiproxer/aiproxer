//nolint:misspell // Fixtures spell path segments like "other-repository"; the linter reads the "ther" inside "other" as "there". Path data, not prose.
package pathvirtualization_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
)

// reservedNamespaceV1 is the fixed, non-operator-configurable V1 alias namespace
// marker from design.md 159. The test spells it literally rather than reusing an
// unexported package constant so a rename cannot silently follow the code.
const reservedNamespaceV1 = ".__aip_v1__"

// workspaceTagChars is the exact length of the V1 workspace identity tag:
// 96 digest bits encode as exactly 20 unpadded base32 characters (design.md 175).
const workspaceTagChars = 20

// goldenVector pins one full derivation: the supported real root, its flavor, the
// 20-character workspace tag, and the exact V1 virtual-root spelling (design.md
// 177-182). The expected tag and alias were computed independently of the
// implementation from design.md 161-182:
//
//	digest = SHA-256("aip:path-virtualization:v1\x00" + flavorID + "\x00" + canonicalRootIdentity)
//	workspaceTag = lower-case base32-no-padding(digest[0:12])
//
// Hardcoded constants are what make these vectors a process-recreation proof: a
// freshly built proxy that read no prior state must reproduce the same bytes.
type goldenVector struct {
	name         string
	root         string
	wantFlavor   pathvirtualization.PathFlavor
	wantTag      string
	wantVirtual  string
	wantRealRoot string
	wantActive   bool
}

func goldenVectors() []goldenVector {
	return []goldenVector{
		{
			name:         "posix_long_workspace",
			root:         `/home/dev/projects/aiproxer-example-project`,
			wantFlavor:   pathvirtualization.FlavorPOSIX,
			wantTag:      "hr7xnivm5at2ku356qya",
			wantVirtual:  `/.__aip_v1__/w_hr7xnivm5at2ku356qya/`,
			wantRealRoot: `/home/dev/projects/aiproxer-example-project`,
			wantActive:   true,
		},
		{
			name:         "windows_drive_long_workspace",
			root:         `C:\Users\dev\source\repos\aiproxer-example-project`,
			wantFlavor:   pathvirtualization.FlavorWindowsDrive,
			wantTag:      "nlx2ikypfzjn6gxtinya",
			wantVirtual:  `C:\.__aip_v1__\w_nlx2ikypfzjn6gxtinya\`,
			wantRealRoot: `C:\Users\dev\source\repos\aiproxer-example-project`,
			wantActive:   true,
		},
		{
			name:         "windows_unc_long_workspace",
			root:         `\\build01\team\source\repos\aiproxer-example-project`,
			wantFlavor:   pathvirtualization.FlavorWindowsUNC,
			wantTag:      "ilzgm4nbs56k63mipfbq",
			wantVirtual:  `\\.__aip_v1__\w_ilzgm4nbs56k63mipfbq\`,
			wantRealRoot: `\\build01\team\source\repos\aiproxer-example-project`,
			wantActive:   true,
		},
		{
			name:         "windows_extended_drive_long_workspace",
			root:         `\\?\C:\Users\dev\source\repos\aiproxer-example-project`,
			wantFlavor:   pathvirtualization.FlavorWindowsExtendedDrive,
			wantTag:      "udyg5rzsrbtqzrs27oga",
			wantVirtual:  `\\?\C:\.__aip_v1__\w_udyg5rzsrbtqzrs27oga\`,
			wantRealRoot: `\\?\C:\Users\dev\source\repos\aiproxer-example-project`,
			wantActive:   true,
		},
		{
			name:         "windows_extended_unc_long_workspace",
			root:         `\\?\UNC\build01\team\source\repos\aiproxer-example-project`,
			wantFlavor:   pathvirtualization.FlavorWindowsExtendedUNC,
			wantTag:      "2igboqkmbmwvv43ql6xa",
			wantVirtual:  `\\?\UNC\.__aip_v1__\w_2igboqkmbmwvv43ql6xa\`,
			wantRealRoot: `\\?\UNC\build01\team\source\repos\aiproxer-example-project`,
			wantActive:   true,
		},
		// Roots too short for their alias: the gate at design.md 184 keeps the
		// derived tag observable but leaves virtualization inactive.
		{
			name:         "posix_short_root_is_inactive",
			root:         `/a/b`,
			wantFlavor:   pathvirtualization.FlavorPOSIX,
			wantTag:      "czzxuetd5vhpsyn2qq7q",
			wantRealRoot: `/a/b`,
		},
		{
			name:         "drive_short_root_is_inactive",
			root:         `C:\x`,
			wantFlavor:   pathvirtualization.FlavorWindowsDrive,
			wantTag:      "wdsefqpxckykwc7bathq",
			wantRealRoot: `C:\x`,
		},
		{
			name:         "posix_anchor_is_inactive",
			root:         `/`,
			wantFlavor:   pathvirtualization.FlavorPOSIX,
			wantTag:      "scjplzwvlsshgpdizcrq",
			wantRealRoot: `/`,
		},
		{
			name:         "drive_volume_root_is_inactive",
			root:         `C:\`,
			wantFlavor:   pathvirtualization.FlavorWindowsDrive,
			wantTag:      "dpxhpgbrye3kcq4mxaia",
			wantRealRoot: `C:\`,
		},
		{
			name:         "unc_volume_root_is_inactive",
			root:         `\\build01\share`,
			wantFlavor:   pathvirtualization.FlavorWindowsUNC,
			wantTag:      "pakocziz4blvrjdcxblq",
			wantRealRoot: `\\build01\share`,
		},
		{
			name:         "extended_drive_volume_root_is_inactive",
			root:         `\\?\C:\`,
			wantFlavor:   pathvirtualization.FlavorWindowsExtendedDrive,
			wantTag:      "txd4tldvu3xhb7u2xd2q",
			wantRealRoot: `\\?\C:\`,
		},
		{
			name:         "extended_unc_volume_root_is_inactive",
			root:         `\\?\UNC\build01\share`,
			wantFlavor:   pathvirtualization.FlavorWindowsExtendedUNC,
			wantTag:      "jofbtj7t56uagu2krida",
			wantRealRoot: `\\?\UNC\build01\share`,
		},
		// A UNC volume already ends with its share name, so the separator that
		// follows a bare share is a non-volume trailing separator and is removed.
		// The two spellings of one workspace must share the frozen V1 tag, so these
		// tags repeat the bare-share values above; they were computed from
		// design.md 161-182, not read from the implementation.
		{
			name:         "unc_volume_root_with_trailing_separator_is_inactive",
			root:         `\\build01\share\`,
			wantFlavor:   pathvirtualization.FlavorWindowsUNC,
			wantTag:      "pakocziz4blvrjdcxblq",
			wantRealRoot: `\\build01\share\`,
		},
		{
			name:         "extended_unc_volume_root_with_trailing_separator_is_inactive",
			root:         `\\?\UNC\build01\share\`,
			wantFlavor:   pathvirtualization.FlavorWindowsExtendedUNC,
			wantTag:      "jofbtj7t56uagu2krida",
			wantRealRoot: `\\?\UNC\build01\share\`,
		},
		{
			// A drive volume is spelled `C:`, which names no root on its own, so
			// its boundary separator belongs to the volume and survives the trim.
			name:         "drive_volume_root_with_repeated_separator_is_inactive",
			root:         `C:\\`,
			wantFlavor:   pathvirtualization.FlavorWindowsDrive,
			wantTag:      "dpxhpgbrye3kcq4mxaia",
			wantRealRoot: `C:\\`,
		},
		{
			name:         "extended_drive_volume_root_with_repeated_separator_is_inactive",
			root:         `\\?\C:\\`,
			wantFlavor:   pathvirtualization.FlavorWindowsExtendedDrive,
			wantTag:      "txd4tldvu3xhb7u2xd2q",
			wantRealRoot: `\\?\C:\\`,
		},
	}
}

// TestDeriveMappingGoldenVectors pins the exact V1 contract: one deterministic
// virtual root per project root, without any prior state.
func TestDeriveMappingGoldenVectors(t *testing.T) {
	t.Parallel()

	for _, tc := range goldenVectors() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, reason := pathvirtualization.DeriveMapping(tc.root)
			if reason != pathvirtualization.SkipReasonNone {
				t.Fatalf("DeriveMapping(%q) reason = %q, want %q", tc.root, reason, pathvirtualization.SkipReasonNone)
			}
			if got.Flavor != tc.wantFlavor {
				t.Errorf("flavor = %v, want %v", got.Flavor, tc.wantFlavor)
			}
			if got.RealRoot != tc.wantRealRoot {
				t.Errorf("real root = %q, want %q", got.RealRoot, tc.wantRealRoot)
			}
			if got.WorkspaceTag != tc.wantTag {
				t.Errorf("workspace tag = %q, want %q", got.WorkspaceTag, tc.wantTag)
			}
			if got.VirtualRoot != tc.wantVirtual {
				t.Errorf("virtual root = %q, want %q", got.VirtualRoot, tc.wantVirtual)
			}
			if (got.VirtualRoot != "") != tc.wantActive {
				t.Errorf("active = %v (virtual root %q), want %v", got.VirtualRoot != "", got.VirtualRoot, tc.wantActive)
			}
		})
	}
}

// TestDeriveMappingIsStatelessAndDeterministic proves requirement 6.2: the same
// supported root derives the same tag and virtual root on every derivation, with
// no stored dictionary, so retries, races, reloads, and restarts converge.
func TestDeriveMappingIsStatelessAndDeterministic(t *testing.T) {
	t.Parallel()

	for _, tc := range goldenVectors() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			first, firstReason := pathvirtualization.DeriveMapping(tc.root)
			for range 8 {
				again, againReason := pathvirtualization.DeriveMapping(tc.root)
				if again != first || againReason != firstReason {
					t.Fatalf("derivation is not stable for %q: %+v/%q then %+v/%q",
						tc.root, first, firstReason, again, againReason)
				}
			}
			if first.WorkspaceTag != tc.wantTag || first.VirtualRoot != tc.wantVirtual {
				t.Fatalf("derivation drifted from the golden vector for %q: tag=%q virtual=%q",
					tc.root, first.WorkspaceTag, first.VirtualRoot)
			}
		})
	}
}

// TestWorkspaceTagIsExactlyTwentyLowerCaseBase32Characters pins the tag encoding
// from design.md 175: exactly 96 digest bits rendered as 20 unpadded base32
// characters with no padding and no upper-case leakage.
func TestWorkspaceTagIsExactlyTwentyLowerCaseBase32Characters(t *testing.T) {
	t.Parallel()

	const base32Alphabet = "abcdefghijklmnopqrstuvwxyz234567"
	roots := []string{
		`/home/dev/projects/aiproxer-example-project`,
		`C:\Users\dev\source\repos\aiproxer-example-project`,
		`\\build01\team\source\repos\aiproxer-example-project`,
		`\\?\C:\Users\dev\source\repos\aiproxer-example-project`,
		`\\?\UNC\build01\team\source\repos\aiproxer-example-project`,
		`/a/b`,
		`C:\`,
		`\\?\UNC\build01\share`,
		`//weird/root/with/odd/bytes/` + reservedNamespaceV1,
	}
	for _, root := range roots {
		got, reason := pathvirtualization.DeriveMapping(root)
		if reason != pathvirtualization.SkipReasonNone {
			t.Fatalf("DeriveMapping(%q) reason = %q", root, reason)
		}
		if len(got.WorkspaceTag) != workspaceTagChars {
			t.Errorf("DeriveMapping(%q) tag = %q has %d characters, want %d", root, got.WorkspaceTag, len(got.WorkspaceTag), workspaceTagChars)
		}
		for i := range len(got.WorkspaceTag) {
			if !strings.ContainsRune(base32Alphabet, rune(got.WorkspaceTag[i])) {
				t.Errorf("DeriveMapping(%q) tag = %q holds non lower-case-base32 byte %q at %d",
					root, got.WorkspaceTag, got.WorkspaceTag[i], i)
			}
		}
	}
}

// canonicalIdentityCase pins which spellings of one workspace root must collapse
// onto a single tag and which must not (design.md 161-166). The first block is
// the Windows separator/case normalization plus non-volume trailing separator
// removal; the second block is POSIX case sensitivity; the third block is the
// retained flavor identity, including normal versus extended Windows forms.
func TestCanonicalRootIdentity(t *testing.T) {
	t.Parallel()

	sameTag := []struct {
		name   string
		a, b   string
		flavor pathvirtualization.PathFlavor
	}{
		{
			name:   "drive_separator_style_is_normalized",
			a:      `C:\Users\dev\source\repos\aiproxer-example-project`,
			b:      `C:/Users/dev/source/repos/aiproxer-example-project`,
			flavor: pathvirtualization.FlavorWindowsDrive,
		},
		{
			name:   "drive_ascii_case_is_folded",
			a:      `C:\Users\dev\source\repos\aiproxer-example-project`,
			b:      `c:\USERS\DEV\source\repos\AIpROXeR-example-prOject`,
			flavor: pathvirtualization.FlavorWindowsDrive,
		},
		{
			name:   "drive_trailing_separator_is_removed",
			a:      `C:\Users\dev\source\repos\aiproxer-example-project`,
			b:      `C:\Users\dev\source\repos\aiproxer-example-project\`,
			flavor: pathvirtualization.FlavorWindowsDrive,
		},
		{
			name:   "drive_volume_root_trailing_separator_is_removed",
			a:      `C:\`,
			b:      `C:\\`,
			flavor: pathvirtualization.FlavorWindowsDrive,
		},
		{
			name:   "unc_trailing_separator_is_removed",
			a:      `\\build01\team\source\repos\aiproxer-example-project`,
			b:      `\\build01\team\source\repos\aiproxer-example-project\`,
			flavor: pathvirtualization.FlavorWindowsUNC,
		},
		{
			// A UNC volume already ends with its share name, so the separator that
			// follows a bare `\\server\share` is a non-volume trailing separator and
			// is removed just like the one that follows a subdirectory.
			name:   "unc_volume_root_trailing_separator_is_removed",
			a:      `\\build01\share`,
			b:      `\\build01\share\`,
			flavor: pathvirtualization.FlavorWindowsUNC,
		},
		{
			name:   "unc_forward_separator_volume_root_is_normalized",
			a:      `\\build01\share`,
			b:      `\\build01/share/`,
			flavor: pathvirtualization.FlavorWindowsUNC,
		},
		{
			name:   "extended_drive_separator_style_is_normalized",
			a:      `\\?\C:\Users\dev\source\repos\aiproxer-example-project`,
			b:      `\\?\C:/Users/dev/source/repos/aiproxer-example-project`,
			flavor: pathvirtualization.FlavorWindowsExtendedDrive,
		},
		{
			name:   "extended_drive_trailing_separator_is_removed",
			a:      `\\?\C:\Users\dev\source\repos\aiproxer-example-project`,
			b:      `\\?\C:\Users\dev\source\repos\aiproxer-example-project\`,
			flavor: pathvirtualization.FlavorWindowsExtendedDrive,
		},
		{
			name:   "extended_drive_volume_root_trailing_separator_is_removed",
			a:      `\\?\C:\`,
			b:      `\\?\C:\\`,
			flavor: pathvirtualization.FlavorWindowsExtendedDrive,
		},
		{
			name:   "extended_unc_marker_case_is_folded",
			a:      `\\?\UNC\build01\team\source\repos\aiproxer-example-project`,
			b:      `\\?\unc\build01\team\source\repos\aiproxer-example-project`,
			flavor: pathvirtualization.FlavorWindowsExtendedUNC,
		},
		{
			// The extended UNC marker is followed by the same `server\share` volume
			// spelling, so the same non-volume trailing separator rule applies.
			name:   "extended_unc_volume_root_trailing_separator_is_removed",
			a:      `\\?\UNC\build01\share`,
			b:      `\\?\UNC\build01\share\`,
			flavor: pathvirtualization.FlavorWindowsExtendedUNC,
		},
		{
			name:   "posix_trailing_separator_is_removed",
			a:      `/home/dev/projects/aiproxer-example-project`,
			b:      `/home/dev/projects/aiproxer-example-project/`,
			flavor: pathvirtualization.FlavorPOSIX,
		},
	}
	for _, tc := range sameTag {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			a, aReason := pathvirtualization.DeriveMapping(tc.a)
			b, bReason := pathvirtualization.DeriveMapping(tc.b)
			if aReason != pathvirtualization.SkipReasonNone || bReason != pathvirtualization.SkipReasonNone {
				t.Fatalf("unexpected skip reasons: %q/%q", aReason, bReason)
			}
			if a.Flavor != tc.flavor || b.Flavor != tc.flavor {
				t.Fatalf("flavors = %v/%v, want %v", a.Flavor, b.Flavor, tc.flavor)
			}
			if a.WorkspaceTag != b.WorkspaceTag {
				t.Errorf("tags differ for equivalent spellings: %q -> %q, %q -> %q",
					tc.a, a.WorkspaceTag, tc.b, b.WorkspaceTag)
			}
			if a.VirtualRoot != b.VirtualRoot {
				t.Errorf("virtual roots differ for equivalent spellings: %q vs %q", a.VirtualRoot, b.VirtualRoot)
			}
		})
	}

	differentTag := []struct {
		name string
		a, b string
	}{
		{
			name: "posix_is_case_sensitive",
			a:    `/home/dev/projects/aiproxer-example-project`,
			b:    `/home/dev/Projects/aiproxer-example-project`,
		},
		{
			name: "dot_segments_are_never_resolved",
			a:    `/home/dev/projects/aiproxer-example-project`,
			b:    `/home/dev/projects/../projects/aiproxer-example-project`,
		},
		{
			name: "drive_and_extended_drive_are_distinct_flavors",
			a:    `C:\Users\dev\source\repos\aiproxer-example-project`,
			b:    `\\?\C:\Users\dev\source\repos\aiproxer-example-project`,
		},
		{
			name: "unc_and_extended_unc_are_distinct_flavors",
			a:    `\\build01\team\source\repos\aiproxer-example-project`,
			b:    `\\?\UNC\build01\team\source\repos\aiproxer-example-project`,
		},
		{
			name: "drive_letter_is_part_of_identity",
			a:    `C:\Users\dev\source\repos\aiproxer-example-project`,
			b:    `D:\Users\dev\source\repos\aiproxer-example-project`,
		},
		{
			name: "unc_server_is_part_of_identity",
			a:    `\\build01\team\source\repos\aiproxer-example-project`,
			b:    `\\build02\team\source\repos\aiproxer-example-project`,
		},
		{
			name: "different_directories_are_distinct",
			a:    `/home/dev/projects/aiproxer-example-project`,
			b:    `/home/dev/projects/other-repository`,
		},
		{
			name: "non_ascii_bytes_never_fold",
			a:    `/home/dev/projects/straße-repository`,
			b:    `/home/dev/projects/STRAẞE-repository`,
		},
	}
	for _, tc := range differentTag {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			a, aReason := pathvirtualization.DeriveMapping(tc.a)
			b, bReason := pathvirtualization.DeriveMapping(tc.b)
			if aReason != pathvirtualization.SkipReasonNone || bReason != pathvirtualization.SkipReasonNone {
				t.Fatalf("unexpected skip reasons: %q/%q", aReason, bReason)
			}
			if a.WorkspaceTag == b.WorkspaceTag {
				t.Errorf("tags collide for distinct roots: %q and %q both derive %q", tc.a, tc.b, a.WorkspaceTag)
			}
		})
	}
}

// TestDistinctRootsDeriveDistinctTags is the fixture-level half of requirement
// 6.2: across the supported flavors, different project roots never share a tag.
func TestDistinctRootsDeriveDistinctTags(t *testing.T) {
	t.Parallel()

	roots := []string{
		`/home/dev/projects/aiproxer-example-project`,
		`/home/dev/projects/other-repository`,
		`/srv/shared/workspace-one`,
		`/srv/shared/workspace-two`,
		`C:\Users\dev\source\repos\aiproxer-example-project`,
		`D:\Users\dev\source\repos\aiproxer-example-project`,
		`C:\Users\dev\source\repos\other-repository`,
		`\\build01\team\source\repos\aiproxer-example-project`,
		`\\build02\team\source\repos\aiproxer-example-project`,
		`\\?\C:\Users\dev\source\repos\aiproxer-example-project`,
		`\\?\D:\Users\dev\source\repos\aiproxer-example-project`,
		`\\?\UNC\build01\team\source\repos\aiproxer-example-project`,
		`\\?\UNC\build01\other\source\repos\aiproxer-example-project`,
	}
	seen := make(map[string]string, len(roots))
	for _, root := range roots {
		got, reason := pathvirtualization.DeriveMapping(root)
		if reason != pathvirtualization.SkipReasonNone {
			t.Fatalf("DeriveMapping(%q) reason = %q", root, reason)
		}
		if previous, duplicated := seen[got.WorkspaceTag]; duplicated {
			t.Errorf("roots %q and %q both derive workspace tag %q", previous, root, got.WorkspaceTag)
			continue
		}
		seen[got.WorkspaceTag] = root
	}
}

// TestDeriveMappingRequiresStrictlyShorterAlias pins design.md 184 and
// requirements 1.4/9.1: virtualization stays inactive whenever the complete
// virtual root is not strictly shorter than the real root it would replace, and
// whenever it is not strictly shorter than the prefix that is actually matched.
//
// The two lengths diverge when a supported root is spelled with a trailing
// separator run, so a root can be long enough while its matched prefix is not.
func TestDeriveMappingRequiresStrictlyShorterAlias(t *testing.T) {
	t.Parallel()

	// posixAliasLen is the exact length of a POSIX V1 alias
	// (`/.__aip_v1__/w_<20 chars>/`), used to place roots on both sides of the
	// requirement 9.1 boundary. A matched prefix of exactly this length would
	// leave every rewritten path the same size, so the gate must be strict.
	const posixAliasLen = 36
	// matchedPrefixLen is the length of the root that prefix matching actually
	// replaces: trailing separators above the spelled volume are trimmed first.
	// It equals the real root's length for any root not spelled with a trailing
	// separator, and is shorter otherwise.
	equalLengthRoot := "/" + strings.Repeat("a", posixAliasLen-1)
	oneByteShorterRoot := "/" + strings.Repeat("a", posixAliasLen)

	cases := []struct {
		name       string
		root       string
		wantActive bool
	}{
		{name: "posix_long_enough", root: `/home/dev/projects/aiproxer-example-project`, wantActive: true},
		{name: "posix_too_short", root: `/a/b`},
		{name: "posix_anchor", root: `/`},
		{name: "posix_trailing_separator_run_is_inactive", root: `/ab` + strings.Repeat(`/`, 35)},
		{name: "posix_equal_length_matched_prefix_is_inactive", root: equalLengthRoot + `/`},
		{name: "posix_equal_length_root_is_inactive", root: equalLengthRoot},
		{name: "posix_one_byte_savings_is_active", root: oneByteShorterRoot, wantActive: true},
		{name: "drive_long_enough", root: `C:\Users\dev\source\repos\aiproxer-example-project`, wantActive: true},
		{name: "drive_too_short", root: `C:\x`},
		{name: "drive_volume_root", root: `C:\`},
		{name: "drive_trailing_separator_run_is_inactive", root: `C:` + strings.Repeat(`\`, 100)},
		{name: "unc_long_enough", root: `\\build01\team\source\repos\aiproxer-example-project`, wantActive: true},
		{name: "unc_volume_root", root: `\\build01\share`},
		{name: "unc_trailing_separator_run_is_inactive", root: `\\build01\share` + strings.Repeat(`\`, 30)},
		{name: "extended_drive_long_enough", root: `\\?\C:\Users\dev\source\repos\aiproxer-example-project`, wantActive: true},
		{name: "extended_drive_volume_root", root: `\\?\C:\`},
		{name: "extended_drive_trailing_separator_run_is_inactive", root: `\\?\C:` + strings.Repeat(`\`, 100)},
		{name: "extended_unc_long_enough", root: `\\?\UNC\build01\team\source\repos\aiproxer-example-project`, wantActive: true},
		{name: "extended_unc_volume_root", root: `\\?\UNC\build01\share`},
		{name: "extended_unc_trailing_separator_run_is_inactive", root: `\\?\UNC\build01\share` + strings.Repeat(`\`, 30)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, reason := pathvirtualization.DeriveMapping(tc.root)
			if reason != pathvirtualization.SkipReasonNone {
				t.Fatalf("reason = %q, want %q", reason, pathvirtualization.SkipReasonNone)
			}
			if got.VirtualRoot == "" {
				if tc.wantActive {
					t.Fatalf("mapping for %q is inactive; its alias must be shorter", tc.root)
				}
				return
			}
			if !tc.wantActive {
				t.Fatalf("mapping for %q derived alias %q that is not shorter than the real root %q",
					tc.root, got.VirtualRoot, got.RealRoot)
			}
			if len(got.VirtualRoot) >= len(got.RealRoot) {
				t.Fatalf("active mapping for %q has alias length %d >= real root length %d",
					tc.root, len(got.VirtualRoot), len(got.RealRoot))
			}
			if len(got.VirtualRoot) < len(reservedNamespaceV1) {
				t.Fatalf("derived alias %q is suspiciously short", got.VirtualRoot)
			}
			// Requirement 9.1: the active replacement must shorten a path of the
			// root's own spelling plus a separator, so the alias is provably
			// strictly shorter than the prefix that is matched.
			child, changed := got.VirtualizePath(tc.root + `/probe`)
			if !changed {
				t.Fatalf("active mapping for %q refused to virtualize its own child", tc.root)
			}
			if len(child) >= len(tc.root)+len(`/probe`) {
				t.Fatalf("virtualized child %q is not shorter than the real path for %q", child, tc.root)
			}
		})
	}
}

// TestMappingInactivityIsDistinguishableFromRejection pins the three-way state a
// caller can observe from DeriveMapping, so no state is ever guessed:
//
//  1. an unusable root: a bounded SkipReason and a zero Mapping;
//  2. a usable root whose alias is not beneficial: no SkipReason, the derived
//     WorkspaceTag is observable, and VirtualRoot is empty;
//  3. a usable root whose alias is beneficial: no SkipReason, WorkspaceTag, and
//     VirtualRoot.
//
// Requirements 1.4 and 1.8 are satisfied by states 1 and 2: state 2 only requires
// that the path is left unchanged. Requirement 1.8's bounded-reason duty belongs
// to state 1, and requirement 7.6's skip-reason duty for state 2 is owned by the
// rewrite-mode task, which must report an empty VirtualRoot as a bounded reason
// instead of counting it as a saving.
func TestMappingInactivityIsDistinguishableFromRejection(t *testing.T) {
	t.Parallel()

	// State 1: unusable roots, distinguishable only by their bounded reason.
	rejected := []struct {
		name       string
		root       string
		wantReason pathvirtualization.SkipReason
	}{
		{name: "empty", root: "", wantReason: pathvirtualization.SkipReasonEmptyRoot},
		{name: "relative", root: `repos\aiproxer-example-project`, wantReason: pathvirtualization.SkipReasonRelativeRoot},
		{name: "drive_relative", root: `C:`, wantReason: pathvirtualization.SkipReasonMalformedVolumeRoot},
		{name: "device_namespace", root: `\\.\PIPE\aip`, wantReason: pathvirtualization.SkipReasonDeviceNamespace},
	}
	for _, tc := range rejected {
		t.Run("rejected_"+tc.name, func(t *testing.T) {
			t.Parallel()

			got, reason := pathvirtualization.DeriveMapping(tc.root)
			if reason != tc.wantReason {
				t.Fatalf("DeriveMapping(%q) reason = %q, want %q", tc.root, reason, tc.wantReason)
			}
			if got != (pathvirtualization.Mapping{}) {
				t.Fatalf("rejected root %q returned %+v, want a zero Mapping", tc.root, got)
			}
			if got.VirtualRoot != "" || got.WorkspaceTag != "" {
				t.Fatalf("rejected root %q derived a tag or alias: %+v", tc.root, got)
			}
		})
	}

	// State 2: usable roots whose alias is not beneficial. No reason is reported
	// because nothing was skipped for an unusable root.
	usableButInactive := []struct {
		name string
		root string
	}{
		{name: "posix_subdirectory", root: `/a/b`},
		{name: "posix_anchor", root: `/`},
		{name: "drive_subdirectory", root: `C:\x`},
		{name: "drive_volume_root", root: `C:\`},
		{name: "unc_volume_root", root: `\\build01\share`},
		{name: "extended_drive_volume_root", root: `\\?\C:\`},
		{name: "extended_unc_volume_root", root: `\\?\UNC\build01\share`},
		// Roots long enough to beat their own length but not the prefix that is
		// actually matched, so the alias would never shorten a path.
		{name: "posix_trailing_separator_run", root: "/ab" + strings.Repeat("/", 35)},
		{name: "drive_trailing_separator_run", root: `C:` + strings.Repeat(`\`, 100)},
	}
	for _, tc := range usableButInactive {
		t.Run("usable_inactive_"+tc.name, func(t *testing.T) {
			t.Parallel()

			root := tc.root
			got, reason := pathvirtualization.DeriveMapping(root)
			if reason != pathvirtualization.SkipReasonNone {
				t.Fatalf("DeriveMapping(%q) reason = %q, want %q for a usable root", root, reason, pathvirtualization.SkipReasonNone)
			}
			if got.RealRoot != root {
				t.Fatalf("RealRoot = %q, want the original spelling %q", got.RealRoot, root)
			}
			if len(got.WorkspaceTag) != workspaceTagChars {
				t.Errorf("usable but inactive root %q derived tag %q; the identity stays observable",
					root, got.WorkspaceTag)
			}
			if got.VirtualRoot != "" {
				t.Fatalf("usable but inactive root %q derived alias %q", root, got.VirtualRoot)
			}
			// Requirement 1.4: no alias means the path is left unchanged.
			path := root + `/src/main.go`
			if virtual, changed := got.VirtualizePath(path); changed || virtual != path {
				t.Errorf("inactive mapping rewrote %q to %q (changed=%v)", path, virtual, changed)
			}
			if expanded, result := got.ExpandPath(path); result != pathvirtualization.ExpandResultNotApplicable || expanded != path {
				t.Errorf("inactive mapping expanded %q to %q (%v)", path, expanded, result)
			}
		})
	}

	// State 3: usable roots with a beneficial alias.
	for _, root := range goldenVectors() {
		if !root.wantActive {
			continue
		}
		t.Run("usable_active_"+root.name, func(t *testing.T) {
			t.Parallel()

			got, reason := pathvirtualization.DeriveMapping(root.root)
			if reason != pathvirtualization.SkipReasonNone {
				t.Fatalf("reason = %q, want %q", reason, pathvirtualization.SkipReasonNone)
			}
			if got.VirtualRoot == "" {
				t.Fatalf("usable root %q derived no alias", root.root)
			}
			if len(got.WorkspaceTag) != workspaceTagChars {
				t.Errorf("root %q derived tag %q", root.root, got.WorkspaceTag)
			}
		})
	}
}

// TestDeriveMappingRejectsUnsupportedRoots keeps requirement 1.8 honest: an
// unusable root yields a bounded reason code and a zero mapping rather than a
// guess. Reserved-namespace collision roots carry their own bounded reason and are
// covered by reserved_test.go.
func TestDeriveMappingRejectsUnsupportedRoots(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		root       string
		wantReason pathvirtualization.SkipReason
	}{
		{name: "empty", root: "", wantReason: pathvirtualization.SkipReasonEmptyRoot},
		{name: "relative", root: `relative\path`, wantReason: pathvirtualization.SkipReasonRelativeRoot},
		{name: "posix_relative", root: `home/dev/project`, wantReason: pathvirtualization.SkipReasonRelativeRoot},
		{name: "rooted_without_volume", root: `\Users\dev`, wantReason: pathvirtualization.SkipReasonRelativeRoot},
		{name: "drive_relative", root: `C:`, wantReason: pathvirtualization.SkipReasonMalformedVolumeRoot},
		{name: "drive_relative_with_body", root: `C:Users\dev`, wantReason: pathvirtualization.SkipReasonRelativeRoot},
		{name: "unc_without_share", root: `\\build01`, wantReason: pathvirtualization.SkipReasonMalformedVolumeRoot},
		{name: "unc_empty_share", root: `\\build01\`, wantReason: pathvirtualization.SkipReasonMalformedVolumeRoot},
		{name: "device_pipe", root: `\\.\PIPE\aip`, wantReason: pathvirtualization.SkipReasonDeviceNamespace},
		{name: "extended_drive_relative_body", root: `\\?\C:com1`, wantReason: pathvirtualization.SkipReasonMalformedVolumeRoot},
		{name: "extended_empty", root: `\\?\`, wantReason: pathvirtualization.SkipReasonMalformedVolumeRoot},
		{name: "extended_unc_without_share", root: `\\?\UNC\build01\`, wantReason: pathvirtualization.SkipReasonMalformedVolumeRoot},
		{name: "extended_global_root", root: `\\?\GLOBALROOT\Device\HarddiskVolume1\src`, wantReason: pathvirtualization.SkipReasonDeviceNamespace},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, reason := pathvirtualization.DeriveMapping(tc.root)
			if reason != tc.wantReason {
				t.Fatalf("DeriveMapping(%q) reason = %q, want %q", tc.root, reason, tc.wantReason)
			}
			if got != (pathvirtualization.Mapping{}) {
				t.Fatalf("rejected root returned %+v, want a zero Mapping", got)
			}
		})
	}
}

// TestVirtualRootKeepsTheRealRootFlavor proves the alias is usable as a path of
// the same absolute form: every derived virtual root reclassifies into the same
// flavor as the real root it replaces, which is what the reserved-alias flavor
// comparison in reserved.go relies on.
func TestVirtualRootKeepsTheRealRootFlavor(t *testing.T) {
	t.Parallel()

	for _, tc := range goldenVectors() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, reason := pathvirtualization.DeriveMapping(tc.root)
			if reason != pathvirtualization.SkipReasonNone {
				t.Fatalf("reason = %q", reason)
			}
			if got.VirtualRoot == "" {
				// An inactive mapping derives no alias; a root colliding
				// with the reserved namespace never gets this far.
				if tc.wantVirtual != "" {
					t.Fatalf("mapping for %q is inactive, want alias %q", tc.root, tc.wantVirtual)
				}
				return
			}
			if !strings.Contains(got.VirtualRoot, reservedNamespaceV1) {
				t.Fatalf("virtual root %q does not use the fixed V1 reserved namespace %q",
					got.VirtualRoot, reservedNamespaceV1)
			}
			if !strings.Contains(got.VirtualRoot, got.WorkspaceTag) {
				t.Fatalf("virtual root %q does not embed the workspace tag %q", got.VirtualRoot, got.WorkspaceTag)
			}
			parsed, parsedReason := pathvirtualization.ClassifyPath(got.VirtualRoot)
			if parsedReason != pathvirtualization.SkipReasonNone {
				t.Fatalf("ClassifyPath(%q) rejected the derived alias with %q", got.VirtualRoot, parsedReason)
			}
			if parsed.Flavor != got.Flavor {
				t.Errorf("alias %q reclassifies as %v, want the real root flavor %v",
					got.VirtualRoot, parsed.Flavor, got.Flavor)
			}
			if parsed.Root+parsed.Rest != got.VirtualRoot {
				t.Errorf("alias %q is not reproduced byte-exactly from %q + %q",
					got.VirtualRoot, parsed.Root, parsed.Rest)
			}
		})
	}
}

// TestRealRootKeepsItsOriginalSpelling pins design.md 152: the mapping stores the
// root exactly as spelled so expansion can reconstruct it. Case, separator style,
// and dot segments are never rewritten.
func TestRealRootKeepsItsOriginalSpelling(t *testing.T) {
	t.Parallel()

	roots := []string{
		`C:\Users\dev\source\repos\aiproxer-example-project`,
		`C:/Users/dev/source/repos/aiproxer-example-project/`,
		`c:\USERS\DEV\source\repos\AIpROXeR-example-prOject`,
		`\\build01\team\source\repos\aiproxer-example-project\`,
		`\\?\C:/Users/dev/source/repos/aiproxer-example-project`,
		`\\?\unc/build01/team/source/repos/aiproxer-example-project`,
		`/home/dev/projects/../projects/aiproxer-example-project`,
		`/home/dev/projects/aiproxer-example-project/`,
	}
	for _, root := range roots {
		got, reason := pathvirtualization.DeriveMapping(root)
		if reason != pathvirtualization.SkipReasonNone {
			t.Fatalf("DeriveMapping(%q) reason = %q", root, reason)
		}
		if got.RealRoot != root {
			t.Errorf("RealRoot = %q, want the original spelling %q", got.RealRoot, root)
		}
	}
}

// virtualizeCase is one row of the segment-boundary and matching-semantics table.
type virtualizeCase struct {
	name        string
	root        string
	path        string
	wantPath    string
	wantChanged bool
}

func virtualizeCases() []virtualizeCase {
	return []virtualizeCase{
		{
			name:        "posix_rewrites_root_prefix",
			root:        `/home/dev/projects/aiproxer-example-project`,
			path:        `/home/dev/projects/aiproxer-example-project/src/main.go`,
			wantPath:    `/.__aip_v1__/w_hr7xnivm5at2ku356qya/src/main.go`,
			wantChanged: true,
		},
		{
			name:        "posix_rewrites_the_bare_root",
			root:        `/home/dev/projects/aiproxer-example-project`,
			path:        `/home/dev/projects/aiproxer-example-project`,
			wantPath:    `/.__aip_v1__/w_hr7xnivm5at2ku356qya/`,
			wantChanged: true,
		},
		{
			name:        "posix_trailing_separator_root_matches_once",
			root:        `/home/dev/projects/aiproxer-example-project/`,
			path:        `/home/dev/projects/aiproxer-example-project/src/main.go`,
			wantPath:    `/.__aip_v1__/w_hr7xnivm5at2ku356qya/src/main.go`,
			wantChanged: true,
		},
		{
			name:     "posix_is_case_sensitive",
			root:     `/home/dev/projects/aiproxer-example-project`,
			path:     `/home/dev/Projects/aiproxer-example-project/src/main.go`,
			wantPath: `/home/dev/Projects/aiproxer-example-project/src/main.go`,
		},
		{
			name:     "posix_backslash_is_a_file_name_byte",
			root:     `/home/dev/projects/aiproxer-example-project`,
			path:     `/home/dev/projects/aiproxer-example-project\other/main.go`,
			wantPath: `/home/dev/projects/aiproxer-example-project\other/main.go`,
		},
		{
			name:     "posix_mid_segment_prefix_is_refused",
			root:     `/home/dev/projects/aiproxer-example-project`,
			path:     `/home/dev/projects/aiproxer-example-projectX/src/main.go`,
			wantPath: `/home/dev/projects/aiproxer-example-projectX/src/main.go`,
		},
		{
			name:     "posix_shorter_prefix_is_refused",
			root:     `/home/dev/projects/aiproxer-example-project`,
			path:     `/home/dev/projects/aiproxer-example-projec`,
			wantPath: `/home/dev/projects/aiproxer-example-projec`,
		},
		{
			name:     "posix_other_workspace_is_refused",
			root:     `/home/dev/projects/aiproxer-example-project`,
			path:     `/home/dev/projects/other-repository/src/main.go`,
			wantPath: `/home/dev/projects/other-repository/src/main.go`,
		},
		{
			name:     "posix_inactive_mapping_never_rewrites",
			root:     `/a/b`,
			path:     `/a/b/src/main.go`,
			wantPath: `/a/b/src/main.go`,
		},
		{
			name:        "drive_rewrites_root_prefix",
			root:        `C:\Users\dev\source\repos\aiproxer-example-project`,
			path:        `C:\Users\dev\source\repos\aiproxer-example-project\src\main.go`,
			wantPath:    `C:\.__aip_v1__\w_nlx2ikypfzjn6gxtinya\src\main.go`,
			wantChanged: true,
		},
		{
			name:        "drive_rewrites_the_bare_root",
			root:        `C:\Users\dev\source\repos\aiproxer-example-project`,
			path:        `C:\Users\dev\source\repos\aiproxer-example-project`,
			wantPath:    `C:\.__aip_v1__\w_nlx2ikypfzjn6gxtinya\`,
			wantChanged: true,
		},
		{
			name:        "drive_folds_ascii_case_and_separators",
			root:        `C:\Users\dev\source\repos\aiproxer-example-project`,
			path:        `c:/users/DEV/source/repos/aiproxer-example-project/src/main.go`,
			wantPath:    `C:\.__aip_v1__\w_nlx2ikypfzjn6gxtinya/src/main.go`,
			wantChanged: true,
		},
		{
			name:     "drive_mid_segment_prefix_is_refused",
			root:     `C:\Users\dev\source\repos\aiproxer-example-project`,
			path:     `C:\Users\dev\source\repos\aiproxer-example-projectX\src\main.go`,
			wantPath: `C:\Users\dev\source\repos\aiproxer-example-projectX\src\main.go`,
		},
		{
			name:     "drive_other_drive_is_refused",
			root:     `C:\Users\dev\source\repos\aiproxer-example-project`,
			path:     `D:\Users\dev\source\repos\aiproxer-example-project\src\main.go`,
			wantPath: `D:\Users\dev\source\repos\aiproxer-example-project\src\main.go`,
		},
		{
			name:     "drive_inactive_mapping_never_rewrites",
			root:     `C:\x`,
			path:     `C:\x\src\main.go`,
			wantPath: `C:\x\src\main.go`,
		},
		{
			name:        "unc_rewrites_root_prefix",
			root:        `\\build01\team\source\repos\aiproxer-example-project`,
			path:        `\\build01\team\source\repos\aiproxer-example-project\src\main.go`,
			wantPath:    `\\.__aip_v1__\w_ilzgm4nbs56k63mipfbq\src\main.go`,
			wantChanged: true,
		},
		{
			name:        "unc_folds_case_and_separators",
			root:        `\\build01\team\source\repos\aiproxer-example-project`,
			path:        `\\BUILD01/team/source/repos/aiproxer-example-project/src/main.go`,
			wantPath:    `\\.__aip_v1__\w_ilzgm4nbs56k63mipfbq/src/main.go`,
			wantChanged: true,
		},
		{
			name:     "unc_other_server_is_refused",
			root:     `\\build01\team\source\repos\aiproxer-example-project`,
			path:     `\\build02\team\source\repos\aiproxer-example-project\src\main.go`,
			wantPath: `\\build02\team\source\repos\aiproxer-example-project\src\main.go`,
		},
		{
			name:        "extended_drive_rewrites_root_prefix",
			root:        `\\?\C:\Users\dev\source\repos\aiproxer-example-project`,
			path:        `\\?\C:\Users\dev\source\repos\aiproxer-example-project\src\main.go`,
			wantPath:    `\\?\C:\.__aip_v1__\w_udyg5rzsrbtqzrs27oga\src\main.go`,
			wantChanged: true,
		},
		{
			name:     "extended_drive_refuses_the_plain_drive_spelling",
			root:     `\\?\C:\Users\dev\source\repos\aiproxer-example-project`,
			path:     `C:\Users\dev\source\repos\aiproxer-example-project\src\main.go`,
			wantPath: `C:\Users\dev\source\repos\aiproxer-example-project\src\main.go`,
		},
		{
			name:        "extended_unc_rewrites_root_prefix",
			root:        `\\?\UNC\build01\team\source\repos\aiproxer-example-project`,
			path:        `\\?\UNC\build01\team\source\repos\aiproxer-example-project\src\main.go`,
			wantPath:    `\\?\UNC\.__aip_v1__\w_2igboqkmbmwvv43ql6xa\src\main.go`,
			wantChanged: true,
		},
		{
			name:     "extended_unc_refuses_the_plain_unc_spelling",
			root:     `\\?\UNC\build01\team\source\repos\aiproxer-example-project`,
			path:     `\\build01\team\source\repos\aiproxer-example-project\src\main.go`,
			wantPath: `\\build01\team\source\repos\aiproxer-example-project\src\main.go`,
		},
	}
}

// TestVirtualizePathMatchSemantics pins requirement 1.6/1.7 and design.md 148-151:
// POSIX matches case-sensitively on `/`, Windows matches ASCII case-insensitively
// with both separators, and every replacement ends on a segment boundary.
func TestVirtualizePathMatchSemantics(t *testing.T) {
	t.Parallel()

	for _, tc := range virtualizeCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mapping, reason := pathvirtualization.DeriveMapping(tc.root)
			if reason != pathvirtualization.SkipReasonNone {
				t.Fatalf("DeriveMapping(%q) reason = %q", tc.root, reason)
			}
			got, changed := mapping.VirtualizePath(tc.path)
			if got != tc.wantPath {
				t.Errorf("VirtualizePath(%q) = %q, want %q", tc.path, got, tc.wantPath)
			}
			if changed != tc.wantChanged {
				t.Errorf("VirtualizePath(%q) changed = %v, want %v", tc.path, changed, tc.wantChanged)
			}
			if !changed && got != tc.path {
				t.Errorf("VirtualizePath(%q) rewrote the path to %q while reporting no change", tc.path, got)
			}
		})
	}
}

// TestVirtualizePathIsIdempotent proves requirement 2.9 at the mapper level: a
// second outbound pass over an already virtualized path changes nothing.
func TestVirtualizePathIsIdempotent(t *testing.T) {
	t.Parallel()

	seeds := []struct {
		root string
		seed string
	}{
		{
			root: `/home/dev/projects/aiproxer-example-project`,
			seed: `/home/dev/projects/aiproxer-example-project/src/main.go`,
		},
		{
			root: `C:\Users\dev\source\repos\aiproxer-example-project`,
			seed: `C:\Users\dev\source\repos\aiproxer-example-project\src\main.go`,
		},
		{
			root: `\\build01\team\source\repos\aiproxer-example-project`,
			seed: `\\build01\team\source\repos\aiproxer-example-project\src\main.go`,
		},
		{
			root: `\\?\C:\Users\dev\source\repos\aiproxer-example-project`,
			seed: `\\?\C:\Users\dev\source\repos\aiproxer-example-project\src\main.go`,
		},
		{
			root: `\\?\UNC\build01\team\source\repos\aiproxer-example-project`,
			seed: `\\?\UNC\build01\team\source\repos\aiproxer-example-project\src\main.go`,
		},
	}
	for _, tc := range seeds {
		t.Run(tc.root, func(t *testing.T) {
			t.Parallel()

			mapping, reason := pathvirtualization.DeriveMapping(tc.root)
			if reason != pathvirtualization.SkipReasonNone {
				t.Fatalf("reason = %q", reason)
			}
			seed := tc.seed
			first, changed := mapping.VirtualizePath(seed)
			if !changed {
				t.Fatalf("VirtualizePath(%q) reported no change for an active mapping", seed)
			}
			second, changedAgain := mapping.VirtualizePath(first)
			if changedAgain {
				t.Errorf("re-virtualizing %q returned %q; the rewrite must be idempotent", first, second)
			}
			if second != first {
				t.Errorf("re-virtualizing %q returned %q, want it unchanged", first, second)
			}
			third, changedThird := mapping.VirtualizePath(second)
			if changedThird || third != second {
				t.Errorf("third pass returned %q (changed=%v), want %q unchanged", third, changedThird, second)
			}
		})
	}
}

// expandCase is one row of the inverse prefix mapping table.
type expandCase struct {
	name     string
	root     string
	path     string
	wantPath string
	wantRes  pathvirtualization.ExpandResult
}

func expandCases() []expandCase {
	const expanded = pathvirtualization.ExpandResultExpanded
	const untouched = pathvirtualization.ExpandResultNotApplicable
	return []expandCase{
		{
			name:     "posix_alias_is_expanded",
			root:     `/home/dev/projects/aiproxer-example-project`,
			path:     `/.__aip_v1__/w_hr7xnivm5at2ku356qya/src/main.go`,
			wantPath: `/home/dev/projects/aiproxer-example-project/src/main.go`,
			wantRes:  expanded,
		},
		{
			// The alias ends with the boundary separator the client sent, so
			// expansion keeps it: the client's bytes are never dropped.
			name:     "posix_bare_alias_keeps_its_separator",
			root:     `/home/dev/projects/aiproxer-example-project`,
			path:     `/.__aip_v1__/w_hr7xnivm5at2ku356qya/`,
			wantPath: `/home/dev/projects/aiproxer-example-project/`,
			wantRes:  expanded,
		},
		{
			name:     "posix_repeated_separator_alias_is_preserved",
			root:     `/home/dev/projects/aiproxer-example-project`,
			path:     `/.__aip_v1__/w_hr7xnivm5at2ku356qya//src/main.go`,
			wantPath: `/home/dev/projects/aiproxer-example-project//src/main.go`,
			wantRes:  expanded,
		},
		{
			name:     "drive_bare_alias_keeps_its_separator",
			root:     `C:\Users\dev\source\repos\aiproxer-example-project`,
			path:     `C:\.__aip_v1__\w_nlx2ikypfzjn6gxtinya\`,
			wantPath: `C:\Users\dev\source\repos\aiproxer-example-project\`,
			wantRes:  expanded,
		},
		{
			name:     "extended_unc_bare_alias_keeps_its_separator",
			root:     `\\?\UNC\build01\team\source\repos\aiproxer-example-project`,
			path:     `\\?\UNC\.__aip_v1__\w_2igboqkmbmwvv43ql6xa\`,
			wantPath: `\\?\UNC\build01\team\source\repos\aiproxer-example-project\`,
			wantRes:  expanded,
		},
		{
			// A real root spelled with a trailing separator is the mirror case: the
			// mapping's own spelling wins, because expansion reconstructs RealRoot.
			name:     "trailing_separator_real_root_reconstructs_its_own_spelling",
			root:     `C:\Users\dev\source\repos\aiproxer-example-project\`,
			path:     `C:\.__aip_v1__\w_nlx2ikypfzjn6gxtinya`,
			wantPath: `C:\Users\dev\source\repos\aiproxer-example-project\`,
			wantRes:  expanded,
		},
		{
			// A reserved alias of another workspace is no longer passed through as
			// an ordinary client path: it is rejected with a bounded reason.
			name: "posix_foreign_tag_is_rejected_as_a_mismatch",
			root: `/home/dev/projects/aiproxer-example-project`,
			path: `/.__aip_v1__/w_aaaaaaaaaaaaaaaaaaaa/src/main.go`,
			// A rejection returns no path value at all, so an alias can never be
			// released to the client even when the caller ignores the result.
			wantPath: "",
			wantRes:  pathvirtualization.ExpandResultWorkspaceMismatch,
		},
		{
			name:     "posix_alias_mid_segment_is_malformed",
			root:     `/home/dev/projects/aiproxer-example-project`,
			path:     `/.__aip_v1__/w_hr7xnivm5at2ku356qyaX/src/main.go`,
			wantPath: "",
			wantRes:  pathvirtualization.ExpandResultMalformedReservedAlias,
		},
		{
			name:     "real_path_is_never_expanded",
			root:     `/home/dev/projects/aiproxer-example-project`,
			path:     `/home/dev/projects/aiproxer-example-project/src/main.go`,
			wantPath: `/home/dev/projects/aiproxer-example-project/src/main.go`,
			wantRes:  untouched,
		},
		{
			name:     "unrelated_path_is_never_expanded",
			root:     `/home/dev/projects/aiproxer-example-project`,
			path:     `/etc/passwd`,
			wantPath: `/etc/passwd`,
			wantRes:  untouched,
		},
		{
			name:     "empty_path_is_never_expanded",
			root:     `/home/dev/projects/aiproxer-example-project`,
			path:     "",
			wantPath: "",
			wantRes:  untouched,
		},
		{
			name: "inactive_posix_mapping_rejects_its_own_alias",
			root: `/a/b`,
			path: `/.__aip_v1__/w_czzxuetd5vhpsyn2qq7q/src/main.go`,
			// The root is usable, but its alias is not beneficial, so it never emits
			// one: no alias of this workspace can be resolved against it.
			wantPath: "",
			wantRes:  pathvirtualization.ExpandResultWorkspaceMismatch,
		},
		{
			name:     "drive_alias_is_expanded",
			root:     `C:\Users\dev\source\repos\aiproxer-example-project`,
			path:     `C:\.__aip_v1__\w_nlx2ikypfzjn6gxtinya\src\main.go`,
			wantPath: `C:\Users\dev\source\repos\aiproxer-example-project\src\main.go`,
			wantRes:  expanded,
		},
		{
			name:     "drive_alias_is_case_and_separator_tolerant",
			root:     `C:\Users\dev\source\repos\aiproxer-example-project`,
			path:     `c:/.__aip_v1__/w_nlx2ikypfzjn6gxtinya\src\main.go`,
			wantPath: `C:\Users\dev\source\repos\aiproxer-example-project\src\main.go`,
			wantRes:  expanded,
		},
		{
			name:     "posix_alias_under_a_drive_mapping_is_a_mismatch",
			root:     `C:\Users\dev\source\repos\aiproxer-example-project`,
			path:     `/.__aip_v1__/w_hr7xnivm5at2ku356qya/src/main.go`,
			wantPath: "",
			wantRes:  pathvirtualization.ExpandResultWorkspaceMismatch,
		},
		{
			name:     "unc_alias_is_expanded",
			root:     `\\build01\team\source\repos\aiproxer-example-project`,
			path:     `\\.__aip_v1__\w_ilzgm4nbs56k63mipfbq\src\main.go`,
			wantPath: `\\build01\team\source\repos\aiproxer-example-project\src\main.go`,
			wantRes:  expanded,
		},
		{
			name:     "extended_drive_alias_is_expanded",
			root:     `\\?\C:\Users\dev\source\repos\aiproxer-example-project`,
			path:     `\\?\C:\.__aip_v1__\w_udyg5rzsrbtqzrs27oga\src\main.go`,
			wantPath: `\\?\C:\Users\dev\source\repos\aiproxer-example-project\src\main.go`,
			wantRes:  expanded,
		},
		{
			name:     "extended_unc_alias_is_expanded",
			root:     `\\?\UNC\build01\team\source\repos\aiproxer-example-project`,
			path:     `\\?\UNC\.__aip_v1__\w_2igboqkmbmwvv43ql6xa\src\main.go`,
			wantPath: `\\?\UNC\build01\team\source\repos\aiproxer-example-project\src\main.go`,
			wantRes:  expanded,
		},
		{
			name:     "extended_alias_under_a_plain_drive_mapping_is_a_mismatch",
			root:     `C:\Users\dev\source\repos\aiproxer-example-project`,
			path:     `\\?\C:\.__aip_v1__\w_udyg5rzsrbtqzrs27oga\src\main.go`,
			wantPath: "",
			wantRes:  pathvirtualization.ExpandResultWorkspaceMismatch,
		},
	}
}

// TestExpandPathInversePrefixMapping pins design.md 152: expansion reconstructs
// the mapping's original RealRoot spelling plus the untouched suffix. The rows
// that carry a foreign or incompatible reserved alias assert the bounded rejection
// outcomes of design.md 186-195 instead of a pass-through; reserved_test.go owns
// the full recognition vocabulary.
func TestExpandPathInversePrefixMapping(t *testing.T) {
	t.Parallel()

	for _, tc := range expandCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mapping, reason := pathvirtualization.DeriveMapping(tc.root)
			if reason != pathvirtualization.SkipReasonNone {
				t.Fatalf("DeriveMapping(%q) reason = %q", tc.root, reason)
			}
			got, result := mapping.ExpandPath(tc.path)
			if got != tc.wantPath {
				t.Errorf("ExpandPath(%q) = %q, want %q", tc.path, got, tc.wantPath)
			}
			if result != tc.wantRes {
				t.Errorf("ExpandPath(%q) result = %v, want %v", tc.path, result, tc.wantRes)
			}
			if result == pathvirtualization.ExpandResultNotApplicable && got != tc.path {
				t.Errorf("ExpandPath(%q) reported no expansion but returned %q", tc.path, got)
			}
			if result != pathvirtualization.ExpandResultNotApplicable &&
				result != pathvirtualization.ExpandResultExpanded && got != "" {
				t.Errorf("ExpandPath(%q) rejected with %v but still returned %q", tc.path, result, got)
			}
		})
	}
}

// TestExpandPathReservedAliasSeamNowFailsClosed closes the task 2.2 handoff seam.
// Task 2.2 had no reserved-alias vocabulary, so an alias it did not derive fell
// through to ExpandResultNotApplicable and was handed back to the caller as an
// ordinary client path. Task 2.3 REPLACED that fallthrough: per design.md 186-195,
// reserved-alias parsing runs BEFORE ordinary prefix matching, so every row below
// now ends in a bounded reason and never in a pass-through. The row names are kept
// so the replacement stays greppable against the task 2.2 notes.
func TestExpandPathReservedAliasSeamNowFailsClosed(t *testing.T) {
	t.Parallel()

	const root = `/home/dev/projects/aiproxer-example-project`
	mapping, reason := pathvirtualization.DeriveMapping(root)
	if reason != pathvirtualization.SkipReasonNone {
		t.Fatalf("DeriveMapping(%q) reason = %q", root, reason)
	}
	// The mapping must be active, otherwise the seam would be untestable.
	if mapping.VirtualRoot == "" {
		t.Fatalf("mapping for %q derived no alias", root)
	}

	cases := []struct {
		name    string
		path    string
		wantRes pathvirtualization.ExpandResult
	}{
		{
			name: "foreign_workspace_alias", path: `/.__aip_v1__/w_aaaaaaaaaaaaaaaaaaaa/src/main.go`,
			wantRes: pathvirtualization.ExpandResultWorkspaceMismatch,
		},
		{
			name: "malformed_short_tag", path: `/.__aip_v1__/w_tooshort/src/main.go`,
			wantRes: pathvirtualization.ExpandResultMalformedReservedAlias,
		},
		{
			name: "malformed_missing_tag", path: `/.__aip_v1__/src/main.go`,
			wantRes: pathvirtualization.ExpandResultMalformedReservedAlias,
		},
		{
			name: "alias_mid_segment", path: `/.__aip_v1__/w_hr7xnivm5at2ku356qyaX/src/main.go`,
			wantRes: pathvirtualization.ExpandResultMalformedReservedAlias,
		},
		{
			name: "drive_alias_under_a_posix_mapping", path: `C:\.__aip_v1__\w_nlx2ikypfzjn6gxtinya\src\main.go`,
			wantRes: pathvirtualization.ExpandResultWorkspaceMismatch,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, result := mapping.ExpandPath(tc.path)
			if result != tc.wantRes {
				t.Errorf("ExpandPath(%q) result = %v, want %v", tc.path, result, tc.wantRes)
			}
			// A rejection releases no path value at all, so the reserved namespace
			// can never reach the client through an ignored result code.
			if got != "" {
				t.Errorf("ExpandPath(%q) = %q, want no path value for %v", tc.path, got, tc.wantRes)
			}
		})
	}
}

// TestReservedNamespaceCollisionStaysOwnedByTaskTwoThree keeps the one collision
// case the task 2.2 handoff left open visible here. Task 2.2 deliberately did not
// detect it (requirements 1.8 and tasks.md 70 assign collision detection to task
// 2.3), so this fixture used to derive an alias nested inside the reserved
// namespace; task 2.3 rejects the root outright. reserved_test.go owns the full
// collision table across every flavor.
func TestReservedNamespaceCollisionStaysOwnedByTaskTwoThree(t *testing.T) {
	t.Parallel()

	const root = `C:\.__aip_v1__\w_abcdefghijklmnopqrstxx`
	mapping, reason := pathvirtualization.DeriveMapping(root)
	if reason != pathvirtualization.SkipReasonReservedNamespaceCollision {
		t.Fatalf("DeriveMapping(%q) reason = %q, want the bounded collision reason", root, reason)
	}
	if mapping != (pathvirtualization.Mapping{}) {
		t.Fatalf("colliding root %q returned %+v, want a zero Mapping", root, mapping)
	}
	if mapping.VirtualRoot != "" || mapping.WorkspaceTag != "" {
		t.Fatalf("colliding root %q derived %+v, want neither tag nor alias", root, mapping)
	}
	if !strings.Contains(root, reservedNamespaceV1) {
		t.Fatalf("fixture %q no longer collides with the reserved namespace", root)
	}
}

// roundTripCase walks a real path through virtualization and back.
type roundTripCase struct {
	name     string
	root     string
	realPath string
	// wantReverse overrides the path expansion must produce. It is set only for
	// the one shape that cannot be byte-for-byte reversible, because the alias
	// spelling is fixed and always ends with a separator (design.md 178-182): a
	// client path that is exactly the bare real root therefore gains that single
	// trailing separator on the way back, since `/…/w_<tag>` and
	// `/…/w_<tag>/` are the same alias after boundary trimming. Client bytes are
	// never dropped.
	wantReverse string
}

// wantRestored is the path expansion must produce for this case.
func (tc roundTripCase) wantRestored() string {
	if tc.wantReverse != "" {
		return tc.wantReverse
	}
	return tc.realPath
}

func roundTripCases() []roundTripCase {
	return []roundTripCase{
		{
			name:     "posix_plain_suffix",
			root:     `/home/dev/projects/aiproxer-example-project`,
			realPath: `/home/dev/projects/aiproxer-example-project/src/main.go`,
		},
		{
			name:     "posix_trailing_separator_root",
			root:     `/home/dev/projects/aiproxer-example-project/`,
			realPath: `/home/dev/projects/aiproxer-example-project/src/main.go`,
		},
		{
			name:        "posix_bare_root_without_separator",
			root:        `/home/dev/projects/aiproxer-example-project`,
			realPath:    `/home/dev/projects/aiproxer-example-project`,
			wantReverse: `/home/dev/projects/aiproxer-example-project/`,
		},
		{
			name:     "posix_bare_root_with_separator",
			root:     `/home/dev/projects/aiproxer-example-project/`,
			realPath: `/home/dev/projects/aiproxer-example-project/`,
		},
		{
			name:     "posix_dot_segment_suffix_is_untouched",
			root:     `/home/dev/projects/aiproxer-example-project`,
			realPath: `/home/dev/projects/aiproxer-example-project/pkg/../src/main.go`,
		},
		{
			name:     "posix_non_ascii_suffix_is_untouched",
			root:     `/home/dev/projects/aiproxer-example-project`,
			realPath: `/home/dev/projects/aiproxer-example-project/cmd/straße/main.go`,
		},
		{
			name:     "drive_plain_suffix",
			root:     `C:\Users\dev\source\repos\aiproxer-example-project`,
			realPath: `C:\Users\dev\source\repos\aiproxer-example-project\src\main.go`,
		},
		{
			name:     "drive_trailing_separator_root",
			root:     `C:\Users\dev\source\repos\aiproxer-example-project\`,
			realPath: `C:\Users\dev\source\repos\aiproxer-example-project\src\main.go`,
		},
		{
			name:        "drive_bare_root_without_separator",
			root:        `C:\Users\dev\source\repos\aiproxer-example-project`,
			realPath:    `C:\Users\dev\source\repos\aiproxer-example-project`,
			wantReverse: `C:\Users\dev\source\repos\aiproxer-example-project\`,
		},
		{
			name:     "drive_bare_root_with_separator",
			root:     `C:\Users\dev\source\repos\aiproxer-example-project\`,
			realPath: `C:\Users\dev\source\repos\aiproxer-example-project\`,
		},
		{
			name:     "drive_mixed_separator_root",
			root:     `C:/Users/dev/source/repos/aiproxer-example-project`,
			realPath: `C:/Users/dev/source/repos/aiproxer-example-project/src/main.go`,
		},
		{
			name:     "drive_lower_case_root_is_reconstructed",
			root:     `c:\users\dev\source\repos\aiproxer-example-project`,
			realPath: `c:\users\dev\source\repos\aiproxer-example-project\src\main.go`,
		},
		{
			name:     "unc_plain_suffix",
			root:     `\\build01\team\source\repos\aiproxer-example-project`,
			realPath: `\\build01\team\source\repos\aiproxer-example-project\src\main.go`,
		},
		{
			name:        "unc_bare_root",
			root:        `\\build01\team\source\repos\aiproxer-example-project`,
			realPath:    `\\build01\team\source\repos\aiproxer-example-project`,
			wantReverse: `\\build01\team\source\repos\aiproxer-example-project\`,
		},
		{
			name:     "extended_drive_plain_suffix",
			root:     `\\?\C:\Users\dev\source\repos\aiproxer-example-project`,
			realPath: `\\?\C:\Users\dev\source\repos\aiproxer-example-project\src\main.go`,
		},
		{
			name:     "extended_drive_trailing_separator_root",
			root:     `\\?\C:\Users\dev\source\repos\aiproxer-example-project\`,
			realPath: `\\?\C:\Users\dev\source\repos\aiproxer-example-project\src\main.go`,
		},
		{
			name:     "extended_unc_plain_suffix",
			root:     `\\?\UNC\build01\team\source\repos\aiproxer-example-project`,
			realPath: `\\?\UNC\build01\team\source\repos\aiproxer-example-project\src\main.go`,
		},
		{
			name:     "extended_unc_forward_separator_root",
			root:     `\\?\unc/build01/team/source/repos/aiproxer-example-project`,
			realPath: `\\?\unc/build01/team/source/repos/aiproxer-example-project/src/main.go`,
		},
		// A path that names the root plus a lone boundary separator carries bytes
		// the alias keeps, so expansion must hand them back (design.md 152).
		{
			name:     "posix_lone_separator_after_root",
			root:     `/home/dev/projects/aiproxer-example-project`,
			realPath: `/home/dev/projects/aiproxer-example-project/`,
		},
		{
			name:     "posix_repeated_separator_after_root",
			root:     `/home/dev/projects/aiproxer-example-project`,
			realPath: `/home/dev/projects/aiproxer-example-project//`,
		},
		{
			name:     "drive_lone_separator_after_root",
			root:     `C:\Users\dev\source\repos\aiproxer-example-project`,
			realPath: `C:\Users\dev\source\repos\aiproxer-example-project\`,
		},
		{
			name:     "drive_repeated_separator_after_root",
			root:     `C:\Users\dev\source\repos\aiproxer-example-project`,
			realPath: `C:\Users\dev\source\repos\aiproxer-example-project\\`,
		},
		{
			name:     "unc_lone_separator_after_root",
			root:     `\\build01\team\source\repos\aiproxer-example-project`,
			realPath: `\\build01\team\source\repos\aiproxer-example-project\`,
		},
		{
			name:     "extended_drive_lone_separator_after_root",
			root:     `\\?\C:\Users\dev\source\repos\aiproxer-example-project`,
			realPath: `\\?\C:\Users\dev\source\repos\aiproxer-example-project\`,
		},
		{
			name:     "extended_unc_lone_separator_after_root",
			root:     `\\?\UNC\build01\team\source\repos\aiproxer-example-project`,
			realPath: `\\?\UNC\build01\team\source\repos\aiproxer-example-project\`,
		},
	}
}

// TestExpandPathReversesVirtualizePath pins design.md 429: expanding a virtualized
// path reproduces the original bytes, including the original real-root spelling.
//
// The single exception is spelled out per row in wantRestored: the alias spelling
// is fixed and always ends with a separator, so a client path that is exactly the
// bare real root comes back with that one separator instead of losing it.
func TestExpandPathReversesVirtualizePath(t *testing.T) {
	t.Parallel()

	for _, tc := range roundTripCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mapping, reason := pathvirtualization.DeriveMapping(tc.root)
			if reason != pathvirtualization.SkipReasonNone {
				t.Fatalf("DeriveMapping(%q) reason = %q", tc.root, reason)
			}
			virtual, changed := mapping.VirtualizePath(tc.realPath)
			if !changed {
				t.Fatalf("VirtualizePath(%q) reported no change; the round trip would prove nothing", tc.realPath)
			}
			if virtual == tc.realPath {
				t.Fatalf("VirtualizePath(%q) returned the real path unchanged", tc.realPath)
			}
			if strings.Contains(virtual, mapping.RealRoot) {
				t.Errorf("virtualized path %q still exposes the real root", virtual)
			}
			if len(virtual) >= len(tc.realPath) {
				t.Errorf("virtualized path %q is not shorter than %q", virtual, tc.realPath)
			}
			back, result := mapping.ExpandPath(virtual)
			if result != pathvirtualization.ExpandResultExpanded {
				t.Fatalf("ExpandPath(%q) result = %v, want %v", virtual, result, pathvirtualization.ExpandResultExpanded)
			}
			if want := tc.wantRestored(); back != want {
				t.Errorf("round trip = %q, want %q", back, want)
			}
			// Requirement 1.6: the untouched suffix survives, and only the fixed
			// alias spelling may add a byte to a path that is exactly the root.
			if len(back) < len(tc.realPath) {
				t.Errorf("round trip dropped bytes: %q -> %q", tc.realPath, back)
			}
		})
	}
}

// TestExpandPathResolvesAnAliasSpellingThisBuildCannotRecognize covers the
// ordinary-matcher guard in ExpandPath, and states exactly which inputs reach it:
// a mapping whose VirtualRoot spells an alias this build does not recognize, which
// no alias the V1 derivation emits today.
//
// Every spelling the current derivation emits, in its derived and in its separator-
// and anchor-mangled forms, is recognized by reserved parsing first, which
// TestNoSpellingOfTheOwnAliasReachesTheCaller pins. The guard therefore exists for
// one direction only: a mapping carrying an unrecognized alias spelling must still
// expand it against the real root, so an alias never reaches the client because
// this build does not know its spelling. It resolves, it does not guess: the prefix
// must match VirtualRoot exactly, under the flavor's own comparison rules and
// ending on a segment boundary.
func TestExpandPathResolvesAnAliasSpellingThisBuildCannotRecognize(t *testing.T) {
	t.Parallel()

	// A future namespace marker stands for an alias spelling this build cannot
	// recognize: the fixed V1 marker is the only reserved one, so reserved parsing
	// declines it and the ordinary matcher owns the path.
	mapping := pathvirtualization.Mapping{
		Flavor:       pathvirtualization.FlavorWindowsDrive,
		RealRoot:     `C:\projects\workspace`,
		WorkspaceTag: "hr7xnivm5at2ku356qya",
		VirtualRoot:  `C:\.__aip_v2__\w_hr7xnivm5at2ku356qya\`,
	}
	for _, tc := range []struct {
		name     string
		path     string
		wantPath string
		wantRes  pathvirtualization.ExpandResult
	}{
		{
			name:     "unrecognized_alias_expands_against_the_real_root",
			path:     `C:\.__aip_v2__\w_hr7xnivm5at2ku356qya\src\main.go`,
			wantPath: `C:\projects\workspace\src\main.go`,
			wantRes:  pathvirtualization.ExpandResultExpanded,
		},
		{
			// Requirement 1.6: Windows matching folds ASCII case and treats both
			// separators as equivalent, so the guard matches the way the derivation
			// spells its own alias.
			name:     "unrecognized_alias_is_case_and_separator_tolerant",
			path:     `c:/.__aip_v2__/W_HR7XNIVM5AT2KU356QYA/src/main.go`,
			wantPath: `C:\projects\workspace/src/main.go`,
			wantRes:  pathvirtualization.ExpandResultExpanded,
		},
		{
			// The match must end on a segment boundary, so a mid-segment hit is not
			// an alias of this mapping.
			name:     "mid_segment_hit_is_not_expanded",
			path:     `C:\.__aip_v2__\w_hr7xnivm5at2ku356qyaX\src\main.go`,
			wantPath: `C:\.__aip_v2__\w_hr7xnivm5at2ku356qyaX\src\main.go`,
			wantRes:  pathvirtualization.ExpandResultNotApplicable,
		},
		{
			name:     "unrelated_path_is_not_expanded",
			path:     `C:\projects\workspace\src\main.go`,
			wantPath: `C:\projects\workspace\src\main.go`,
			wantRes:  pathvirtualization.ExpandResultNotApplicable,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, result := mapping.ExpandPath(tc.path)
			if got != tc.wantPath {
				t.Errorf("ExpandPath(%q) = %q, want %q", tc.path, got, tc.wantPath)
			}
			if result != tc.wantRes {
				t.Errorf("ExpandPath(%q) result = %v, want %v", tc.path, result, tc.wantRes)
			}
		})
	}

	// A mapping with no alias at all has no ordinary prefix to match, so the guard
	// cannot resurrect one.
	inactive := pathvirtualization.Mapping{
		Flavor:   pathvirtualization.FlavorWindowsDrive,
		RealRoot: `C:\projects\workspace`,
	}
	const alias = `C:\.__aip_v2__\w_hr7xnivm5at2ku356qya\src\main.go`
	if got, result := inactive.ExpandPath(alias); got != alias || result != pathvirtualization.ExpandResultNotApplicable {
		t.Errorf("ExpandPath(%q) = %q/%v on a mapping with no alias, want it untouched",
			alias, got, result)
	}
}

// TestExpandPathIsIdempotentOnRealPaths proves a model-emitted real path that
// never left the client namespace is handed back untouched.
func TestExpandPathIsIdempotentOnRealPaths(t *testing.T) {
	t.Parallel()

	for _, tc := range roundTripCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mapping, reason := pathvirtualization.DeriveMapping(tc.root)
			if reason != pathvirtualization.SkipReasonNone {
				t.Fatalf("reason = %q", reason)
			}
			got, result := mapping.ExpandPath(tc.realPath)
			if got != tc.realPath || result != pathvirtualization.ExpandResultNotApplicable {
				t.Errorf("ExpandPath(%q) = %q/%v, want it untouched", tc.realPath, got, result)
			}
		})
	}
}

// TestVirtualizeAndExpandAreInverse proves the mapper is a reversible namespace
// translation (design.md 5-7): virtualizing an expanded alias returns the alias.
func TestVirtualizeAndExpandAreInverse(t *testing.T) {
	t.Parallel()

	for _, tc := range roundTripCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mapping, reason := pathvirtualization.DeriveMapping(tc.root)
			if reason != pathvirtualization.SkipReasonNone {
				t.Fatalf("DeriveMapping(%q) reason = %q", tc.root, reason)
			}
			alias, changed := mapping.VirtualizePath(tc.realPath)
			if !changed {
				t.Fatalf("VirtualizePath(%q) reported no change", tc.realPath)
			}
			real, result := mapping.ExpandPath(alias)
			if result != pathvirtualization.ExpandResultExpanded {
				t.Fatalf("ExpandPath(%q) result = %v", alias, result)
			}
			if want := tc.wantRestored(); real != want {
				t.Fatalf("ExpandPath(%q) = %q, want %q", alias, real, want)
			}
			// Re-virtualizing the restored path must return the same alias, so the
			// namespace translation is an involution apart from the documented
			// bare-root shape.
			again, changedAgain := mapping.VirtualizePath(real)
			if !changedAgain || again != alias {
				t.Errorf("VirtualizePath(%q) = %q (changed=%v), want %q", real, again, changedAgain, alias)
			}
		})
	}
}

// TestMappingIsAPureValueObject proves the derivation holds no mutable state: the
// returned Mapping is self-contained, later derivations of other roots cannot
// change it, and two mappings derived independently never alias each other's
// storage.
func TestMappingIsAPureValueObject(t *testing.T) {
	t.Parallel()

	const root = `/home/dev/projects/aiproxer-example-project`
	mapping, reason := pathvirtualization.DeriveMapping(root)
	if reason != pathvirtualization.SkipReasonNone {
		t.Fatalf("reason = %q", reason)
	}

	alias := mapping.VirtualRoot + `src/main.go`
	got, result := mapping.ExpandPath(alias)
	if result != pathvirtualization.ExpandResultExpanded {
		t.Fatalf("ExpandPath(%q) result = %v", alias, result)
	}
	if want := `/home/dev/projects/aiproxer-example-project/src/main.go`; got != want {
		t.Errorf("ExpandPath(%q) = %q, want %q", alias, got, want)
	}

	first, _ := pathvirtualization.DeriveMapping(root)
	second, _ := pathvirtualization.DeriveMapping(root)
	if first != second || first != mapping {
		t.Fatalf("independent derivations differ: %+v vs %+v vs %+v", mapping, first, second)
	}
	// Interleaving other roots' derivations must not change an already derived
	// value: there is no cache, counter, or shared buffer behind it.
	others := []string{
		`/srv/shared/workspace-one`,
		`C:\Users\dev\source\repos\other-repository`,
		`\\build01\team\source\repos\aiproxer-example-project`,
		`\\?\UNC\build01\team\source\repos\aiproxer-example-project`,
	}
	for _, other := range others {
		derived, otherReason := pathvirtualization.DeriveMapping(other)
		if otherReason != pathvirtualization.SkipReasonNone {
			t.Fatalf("DeriveMapping(%q) reason = %q", other, otherReason)
		}
		if derived == mapping {
			t.Fatalf("root %q derived the same mapping as %q", other, root)
		}
	}
	if again, _ := pathvirtualization.DeriveMapping(root); again != mapping {
		t.Fatalf("derivation drifted after other roots: %+v vs %+v", mapping, again)
	}

	// Clobbering one derived value must not be visible through another.
	third, _ := pathvirtualization.DeriveMapping(`/home/dev/projects/other-repository`)
	if first.WorkspaceTag == third.WorkspaceTag {
		t.Fatal("distinct roots share a workspace tag")
	}
	third.VirtualRoot = "/clobbered/"
	if first.VirtualRoot == third.VirtualRoot || mapping.VirtualRoot == third.VirtualRoot {
		t.Fatal("derived mappings share mutable storage")
	}
	if again, _ := pathvirtualization.DeriveMapping(root); again != mapping {
		t.Fatalf("a clobbered sibling mapping changed %q: %+v", root, again)
	}
}

// TestExpandResultLabelsAreBounded keeps the expansion outcome a fixed,
// content-free dimension suitable for metrics.
func TestExpandResultLabelsAreBounded(t *testing.T) {
	t.Parallel()

	want := []struct {
		result pathvirtualization.ExpandResult
		label  string
	}{
		{pathvirtualization.ExpandResultExpanded, "expanded"},
		{pathvirtualization.ExpandResultNotApplicable, "not_applicable"},
	}
	for _, tc := range want {
		if got := tc.result.String(); got != tc.label {
			t.Errorf("ExpandResult(%d).String() = %q, want %q", tc.result, got, tc.label)
		}
	}
	if got := pathvirtualization.ExpandResult(99).String(); got != "unknown" {
		t.Errorf("unbounded ExpandResult label = %q, want %q", got, "unknown")
	}
}

// TestWorkspaceTagIsNeverEmittedInBoundedDiagnostics guards the observability
// rule from requirement 7.7 and design.md 400: the 96-bit tag is backend-visible
// identity, never a reason code, label, or error string.
func TestWorkspaceTagIsNeverEmittedInBoundedDiagnostics(t *testing.T) {
	t.Parallel()

	tagged := []string{
		`/home/dev/projects/aiproxer-example-project`,
		`C:\Users\dev\source\repos\aiproxer-example-project`,
		`\\build01\team\source\repos\aiproxer-example-project`,
		`\\?\C:\Users\dev\source\repos\aiproxer-example-project`,
		`\\?\UNC\build01\team\source\repos\aiproxer-example-project`,
	}
	for _, root := range tagged {
		mapping, reason := pathvirtualization.DeriveMapping(root)
		if reason != pathvirtualization.SkipReasonNone {
			t.Fatalf("DeriveMapping(%q) reason = %q", root, reason)
		}
		// A workspace tag injected into a rejected root must not reappear in the
		// bounded reason code that classifies it.
		_, deviceReason := pathvirtualization.DeriveMapping(`\\.\PIPE\` + mapping.WorkspaceTag)
		if strings.Contains(string(deviceReason), mapping.WorkspaceTag) {
			t.Errorf("reason %q leaks the workspace tag", deviceReason)
		}
		if label := pathvirtualization.ExpandResultNotApplicable.String(); strings.Contains(label, mapping.WorkspaceTag) {
			t.Errorf("expansion label %q leaks the workspace tag", label)
		}
		// The bounded reserved-alias rejections are diagnostics too, so they must
		// stay as fixed as the pass-through labels.
		for _, label := range []string{
			pathvirtualization.ExpandResultMalformedReservedAlias.String(),
			pathvirtualization.ExpandResultWorkspaceMismatch.String(),
			string(pathvirtualization.SkipReasonReservedNamespaceCollision),
		} {
			if strings.Contains(label, mapping.WorkspaceTag) {
				t.Errorf("reserved-alias reason %q leaks the workspace tag", label)
			}
		}
		if label := mapping.Flavor.String(); strings.Contains(label, mapping.WorkspaceTag) {
			t.Errorf("flavor label %q leaks the workspace tag", label)
		}
	}
}

// posixTrailingBackslashRoot is a supported POSIX root whose FINAL directory name ends
// in a literal backslash byte.
//
// It is not a Windows spelling and not a malformed root: on POSIX a backslash is an
// ordinary file-name byte, so this names one real directory. The fixture root is long
// enough that its derived alias is strictly shorter, which is what keeps the mapping
// active (requirement 1.4) and every expectation below non-vacuous.
const posixTrailingBackslashRoot = `/home/dev/workspaces/aip-path-virtualization\`

// posixTrailingBackslashSiblingRoot is the same root with the trailing backslash removed:
// a DIFFERENT directory that happens to have the same name up to that one byte.
const posixTrailingBackslashSiblingRoot = `/home/dev/workspaces/aip-path-virtualization`

// TestPosixRootKeepsATrailingBackslashByte pins design.md 150-151 on the byte that
// separates them from a Windows spelling.
//
// Both helpers that decide where a root ENDS - the trailing-separator trim that makes a
// root matchable, and the suffix join that reconstructs a client's own bytes - used one
// separator predicate that accepted both separators for every flavor. On POSIX that
// predicate was strictly too permissive, because POSIX has exactly one separator: a
// trailing backslash is part of the directory's name, and treating it as a separator
// deleted a byte of the workspace identity. Two failure modes follow from that one byte,
// and both are worse than a path that simply fails to match:
//
//   - a legitimate child of this root stops matching, so a path the client spelled is
//     never virtualized;
//   - a path beneath the SIMILARLY NAMED sibling directory without the backslash now
//     matches this root's trimmed spelling, so a file outside the workspace is
//     virtualized as though it were inside it.
//
// The sibling assertion is the one that matters most, because the trimmed root is a
// prefix of a path that has nothing to do with this workspace.
func TestPosixRootKeepsATrailingBackslashByte(t *testing.T) {
	t.Parallel()

	mapping, reason := pathvirtualization.DeriveMapping(posixTrailingBackslashRoot)
	if reason != pathvirtualization.SkipReasonNone {
		t.Fatalf("DeriveMapping reason = %q, want none", reason)
	}
	if mapping.Flavor != pathvirtualization.FlavorPOSIX {
		t.Fatalf("flavor = %v, want POSIX", mapping.Flavor)
	}
	if mapping.VirtualRoot == "" {
		t.Fatal("the fixture root must derive an active alias (requirement 1.4)")
	}
	if mapping.RealRoot != posixTrailingBackslashRoot {
		t.Fatal("the mapping did not keep the root exactly as spelled")
	}

	child := posixTrailingBackslashRoot + `/src/main.go`
	virtual, changed := mapping.VirtualizePath(child)
	if !changed {
		t.Fatal("a legitimate child of the root must match the root's matchable spelling")
	}
	wantVirtual := mapping.VirtualRoot[:len(mapping.VirtualRoot)-1] + `/src/main.go`
	if virtual != wantVirtual {
		t.Fatal("the virtualized child did not keep the client's suffix below the alias root")
	}

	// The reverse direction has the same one byte: expansion must reconstruct the
	// client's bytes, and the backslash is one of them.
	expanded, result := mapping.ExpandPath(virtual)
	if result != pathvirtualization.ExpandResultExpanded {
		t.Fatalf("ExpandPath result = %v, want expanded", result)
	}
	if expanded != child {
		t.Fatal("expansion did not reproduce the client's own bytes")
	}

	// The bare alias root expands under the same rule an ordinary POSIX root ending on
	// a name does, and what matters here is only that the expansion keeps the root's own
	// trailing name byte: the result must still be a path under THIS root and never the
	// similarly named sibling.
	for _, aliasRoot := range []string{mapping.VirtualRoot, mapping.VirtualRoot + `/`} {
		expanded, result := mapping.ExpandPath(aliasRoot)
		if result != pathvirtualization.ExpandResultExpanded {
			t.Fatalf("ExpandPath of an alias root = %v, want expanded", result)
		}
		if !strings.HasPrefix(expanded, posixTrailingBackslashRoot) {
			t.Fatal("expanding an alias root left the workspace root's trailing name byte behind")
		}
	}

	// The near-named sibling is a different workspace and must stay untouched.
	sibling, reason := pathvirtualization.DeriveMapping(posixTrailingBackslashSiblingRoot)
	if reason != pathvirtualization.SkipReasonNone {
		t.Fatalf("sibling DeriveMapping reason = %q, want none", reason)
	}
	if sibling.WorkspaceTag == mapping.WorkspaceTag {
		t.Fatal("the two fixture roots must be different workspaces")
	}
	for _, foreign := range []string{
		posixTrailingBackslashSiblingRoot + `/src/main.go`,
		posixTrailingBackslashSiblingRoot,
		posixTrailingBackslashSiblingRoot + `/`,
	} {
		if got, changed := mapping.VirtualizePath(foreign); changed || got != foreign {
			t.Fatal("a path beneath the near-named sibling was rewritten")
		}
		if got, result := mapping.ExpandPath(foreign); result != pathvirtualization.ExpandResultNotApplicable || got != foreign {
			t.Fatalf("a path beneath the near-named sibling expanded, result = %v", result)
		}
	}
}

// TestWindowsRootsKeepSeparatorAgnosticTrimming is the flavor-scoping half.
//
// The POSIX fix must not narrow the Windows rules: a Windows root spelled with a forward
// slash and a client that sent backslashes (or the reverse) is one root under Windows
// matching, and the trailing-separator trim and the suffix join have to agree. This is
// asserted on both separators and on the mixed spelling, so scoping the predicate by
// flavor cannot quietly become a global narrowing.
func TestWindowsRootsKeepSeparatorAgnosticTrimming(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		root       string
		wantFlavor pathvirtualization.PathFlavor
	}{
		{
			name:       "drive_backslash_spelling",
			root:       `C:\Users\dev\source\repos\aip-path-virtualization`,
			wantFlavor: pathvirtualization.FlavorWindowsDrive,
		},
		{
			name:       "drive_forward_slash_spelling",
			root:       `C:/Users/dev/source/repos/aip-path-virtualization`,
			wantFlavor: pathvirtualization.FlavorWindowsDrive,
		},
		{
			name:       "unc_spelling",
			root:       `\\build01\dev\source\repos\aip-path-virtualization`,
			wantFlavor: pathvirtualization.FlavorWindowsUNC,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mapping, reason := pathvirtualization.DeriveMapping(tc.root)
			if reason != pathvirtualization.SkipReasonNone {
				t.Fatalf("DeriveMapping reason = %q, want none", reason)
			}
			if mapping.Flavor != tc.wantFlavor {
				t.Fatalf("flavor = %v, want %v", mapping.Flavor, tc.wantFlavor)
			}
			if mapping.VirtualRoot == "" {
				t.Fatal("the fixture root must derive an active alias (requirement 1.4)")
			}

			// A child spelled with EITHER separator matches, and expansion puts back
			// the separator the client actually sent.
			for _, child := range []string{
				tc.root + `\src\main.go`,
				tc.root + `/src/main.go`,
				tc.root + `\src/main.go`,
			} {
				virtual, changed := mapping.VirtualizePath(child)
				if !changed {
					t.Fatalf("a child of the root must match under Windows matching rules")
				}
				expanded, result := mapping.ExpandPath(virtual)
				if result != pathvirtualization.ExpandResultExpanded {
					t.Fatalf("ExpandPath result = %v, want expanded", result)
				}
				if expanded != child {
					t.Fatalf("ExpandPath did not reproduce the client's own separator bytes")
				}
			}
		})
	}
}
