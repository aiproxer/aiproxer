package pathvirtualization_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
)

// flavorCase is one row of the complete cross-platform flavor table.
//
// Every expectation is a host-independent constant: the table below must pass
// unchanged on Linux, macOS, and Windows, because a client path may name a
// foreign operating system. Reserved-namespace alias parsing and the
// stale-workspace rules live in reserved_test.go and are deliberately absent from
// this table, which covers one lexical concern only.
type flavorCase struct {
	name       string
	path       string
	wantFlavor pathvirtualization.PathFlavor
	wantRoot   string
	wantRest   string
	wantReason pathvirtualization.SkipReason
}

func TestClassifyPathFlavorTable(t *testing.T) {
	t.Parallel()

	for _, tc := range flavorCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, reason := pathvirtualization.ClassifyPath(tc.path)
			if reason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", reason, tc.wantReason)
			}
			if tc.wantReason != pathvirtualization.SkipReasonNone {
				if got != (pathvirtualization.ParsedPath{}) {
					t.Fatalf("rejected path returned %+v, want zero ParsedPath", got)
				}
				return
			}
			if got.Flavor != tc.wantFlavor {
				t.Fatalf("flavor = %v, want %v", got.Flavor, tc.wantFlavor)
			}
			if got.Root != tc.wantRoot {
				t.Fatalf("root = %q, want %q", got.Root, tc.wantRoot)
			}
			if got.Rest != tc.wantRest {
				t.Fatalf("rest = %q, want %q", got.Rest, tc.wantRest)
			}
			if got.Root+got.Rest != tc.path {
				t.Fatalf("root+rest = %q, want input bytes %q", got.Root+got.Rest, tc.path)
			}
			if got.Flavor == pathvirtualization.FlavorUnsupported {
				t.Fatal("accepted path reported FlavorUnsupported")
			}
		})
	}
}

// TestClassifyPathRejectionsAreDistinguishable proves the bounded reason set
// separates the rejection classes the specification names instead of collapsing
// them into one opaque failure.
func TestClassifyPathRejectionsAreDistinguishable(t *testing.T) {
	t.Parallel()

	want := map[pathvirtualization.SkipReason][]string{
		pathvirtualization.SkipReasonEmptyRoot: {
			"",
		},
		pathvirtualization.SkipReasonRelativeRoot: {
			`project`,
			`relative/path`,
			`./relative`,
			`C:Users\dev`,
			`\Users\dev`,
		},
		pathvirtualization.SkipReasonMalformedVolumeRoot: {
			`C:`,
			`\\server`,
			`\\server\`,
			`\\?\`,
			`\\?\C:`,
			`\\?\UNC\server`,
		},
		pathvirtualization.SkipReasonDeviceNamespace: {
			`\\.\`,
			`\\.\C:\dev`,
			`\\.\PIPE\aip`,
			`\\?\Volume{9f1c-0001}\dev`,
			`\\?\GLOBALROOT\Device\HarddiskVolumeShadowCopy1`,
		},
	}
	for reason, paths := range want {
		if reason == pathvirtualization.SkipReasonNone {
			t.Fatal("SkipReasonNone must not appear as a rejection reason")
		}
		for _, path := range paths {
			if _, gotReason := pathvirtualization.ClassifyPath(path); gotReason != reason {
				t.Errorf("ClassifyPath(%q) reason = %q, want %q", path, gotReason, reason)
			}
		}
	}
}

// TestSkipReasonLabelsAreBounded pins the closed refusal vocabulary of flavor
// parsing as fixed, content-free labels, and proves that a value outside it
// degrades to "unknown" in both renderings.
//
// This is the content-freedom rule of requirements.md 7.7 applied to the one
// string-typed enum in the package: any caller can construct a SkipReason that
// holds a real project root, and String plus MarshalText are the only things
// keeping those bytes out of a log line or a metric label.
func TestSkipReasonLabelsAreBounded(t *testing.T) {
	t.Parallel()

	want := []struct {
		reason pathvirtualization.SkipReason
		label  string
	}{
		{pathvirtualization.SkipReasonNone, ""},
		{pathvirtualization.SkipReasonEmptyRoot, "empty_root"},
		{pathvirtualization.SkipReasonRelativeRoot, "relative_root"},
		{pathvirtualization.SkipReasonMalformedVolumeRoot, "malformed_volume_root"},
		{pathvirtualization.SkipReasonDeviceNamespace, "device_namespace"},
		{pathvirtualization.SkipReasonReservedNamespaceCollision, "reserved_namespace_collision"},
	}
	for _, tc := range want {
		if got := tc.reason.String(); got != tc.label {
			t.Errorf("SkipReason(%q).String() = %q, want %q", string(tc.reason), got, tc.label)
		}
		rendered, err := tc.reason.MarshalText()
		if err != nil {
			t.Fatalf("SkipReason(%q).MarshalText: %v", string(tc.reason), err)
		}
		if string(rendered) != tc.label {
			t.Errorf("SkipReason(%q).MarshalText() = %q, want %q", string(tc.reason), rendered, tc.label)
		}
	}
	for _, hostile := range []pathvirtualization.SkipReason{
		"/home/dev/projects/aiproxer-example-project",
		`C:\Users\dev\source\repos\aiproxer-example-project`,
		".__aip_v1__/w_AAAAAAAAAAAAAAAAAAAA",
	} {
		if got := hostile.String(); got != "unknown" {
			t.Errorf("SkipReason(%q).String() = %q, want the bounded fallback %q", string(hostile), got, "unknown")
		}
		rendered, err := hostile.MarshalText()
		if err != nil {
			t.Fatalf("SkipReason(%q).MarshalText: %v", string(hostile), err)
		}
		if string(rendered) != "unknown" {
			t.Errorf("SkipReason(%q).MarshalText() = %q, want the bounded fallback %q", string(hostile), rendered, "unknown")
		}
	}
}

// TestPathFlavorLabelsAreBounded proves PathFlavor.String emits the fixed
// low-cardinality labels used for content-free observability.
func TestPathFlavorLabelsAreBounded(t *testing.T) {
	t.Parallel()

	want := []struct {
		flavor pathvirtualization.PathFlavor
		label  string
	}{
		{pathvirtualization.FlavorUnsupported, "unsupported"},
		{pathvirtualization.FlavorPOSIX, "posix"},
		{pathvirtualization.FlavorWindowsDrive, "windows_drive"},
		{pathvirtualization.FlavorWindowsUNC, "windows_unc"},
		{pathvirtualization.FlavorWindowsExtendedDrive, "windows_extended_drive"},
		{pathvirtualization.FlavorWindowsExtendedUNC, "windows_extended_unc"},
	}
	for _, tc := range want {
		if got := tc.flavor.String(); got != tc.label {
			t.Errorf("flavor %d label = %q, want %q", tc.flavor, got, tc.label)
		}
	}
	if got := pathvirtualization.PathFlavor(200).String(); got != "unsupported" {
		t.Errorf("out-of-range flavor label = %q, want %q", got, "unsupported")
	}
}

func flavorCases() []flavorCase {
	return []flavorCase{
		// POSIX: a leading '/' is the only anchor requirement.
		{name: "posix_root_only", path: `/`, wantFlavor: pathvirtualization.FlavorPOSIX, wantRoot: `/`},
		{name: "posix_workspace", path: `/home/dev/project`, wantFlavor: pathvirtualization.FlavorPOSIX, wantRoot: `/`, wantRest: `home/dev/project`},
		{name: "posix_case_is_preserved", path: `/home/dev/Project`, wantFlavor: pathvirtualization.FlavorPOSIX, wantRoot: `/`, wantRest: `home/dev/Project`},
		{name: "posix_double_slash_is_still_posix", path: `//srv/share/dev`, wantFlavor: pathvirtualization.FlavorPOSIX, wantRoot: `/`, wantRest: `/srv/share/dev`},
		{name: "posix_trailing_separator_kept", path: `/home/dev/project/`, wantFlavor: pathvirtualization.FlavorPOSIX, wantRoot: `/`, wantRest: `home/dev/project/`},
		{name: "posix_dot_segments_not_resolved", path: `/home/dev/../dev/project`, wantFlavor: pathvirtualization.FlavorPOSIX, wantRoot: `/`, wantRest: `home/dev/../dev/project`},
		{name: "posix_non_ascii_bytes_untouched", path: `/home/dév/pröjekt/ürsü`, wantFlavor: pathvirtualization.FlavorPOSIX, wantRoot: `/`, wantRest: `home/dév/pröjekt/ürsü`},
		{name: "posix_whitespace_and_space_bytes", path: `/home/dev/my project/a b.txt`, wantFlavor: pathvirtualization.FlavorPOSIX, wantRoot: `/`, wantRest: `home/dev/my project/a b.txt`},

		// Windows drive: 'C:\...' and separator-equivalent 'C:/...'.
		{name: "drive_backslash", path: `C:\Users\dev\project`, wantFlavor: pathvirtualization.FlavorWindowsDrive, wantRoot: `C:`, wantRest: `\Users\dev\project`},
		{name: "drive_forward_slash", path: `C:/Users/dev/project`, wantFlavor: pathvirtualization.FlavorWindowsDrive, wantRoot: `C:`, wantRest: `/Users/dev/project`},
		{name: "drive_volume_only_backslash", path: `C:\`, wantFlavor: pathvirtualization.FlavorWindowsDrive, wantRoot: `C:`, wantRest: `\`},
		{name: "drive_volume_only_forward_slash", path: `D:/`, wantFlavor: pathvirtualization.FlavorWindowsDrive, wantRoot: `D:`, wantRest: `/`},
		{name: "drive_lowercase_spelling_preserved", path: `c:\Users\dev`, wantFlavor: pathvirtualization.FlavorWindowsDrive, wantRoot: `c:`, wantRest: `\Users\dev`},
		{name: "drive_mixed_separators_preserved", path: `Z:\work\dev/project\file.txt`, wantFlavor: pathvirtualization.FlavorWindowsDrive, wantRoot: `Z:`, wantRest: `\work\dev/project\file.txt`},
		{name: "drive_spaces_untouched", path: `C:\Program Files\dev\my project`, wantFlavor: pathvirtualization.FlavorWindowsDrive, wantRoot: `C:`, wantRest: `\Program Files\dev\my project`},

		// UNC: '\\server\share\...'.
		{name: "unc_workspace", path: `\\build01\dev\project`, wantFlavor: pathvirtualization.FlavorWindowsUNC, wantRoot: `\\build01\dev`, wantRest: `\project`},
		{name: "unc_share_root_only", path: `\\build01\dev`, wantFlavor: pathvirtualization.FlavorWindowsUNC, wantRoot: `\\build01\dev`},
		{name: "unc_share_root_trailing_separator", path: `\\build01\dev\`, wantFlavor: pathvirtualization.FlavorWindowsUNC, wantRoot: `\\build01\dev`, wantRest: `\`},
		{name: "unc_case_preserved", path: `\\Build01\Dev\Project`, wantFlavor: pathvirtualization.FlavorWindowsUNC, wantRoot: `\\Build01\Dev`, wantRest: `\Project`},
		{name: "unc_forward_slash_inside_share", path: `\\build01/dev/project`, wantFlavor: pathvirtualization.FlavorWindowsUNC, wantRoot: `\\build01/dev`, wantRest: `/project`},
		{name: "unc_dot_segments_not_resolved", path: `\\build01\dev\..\..\dev\project`, wantFlavor: pathvirtualization.FlavorWindowsUNC, wantRoot: `\\build01\dev`, wantRest: `\..\..\dev\project`},

		// Extended drive: '\\?\C:\...'.
		{name: "extended_drive_backslash", path: `\\?\C:\Users\dev\project`, wantFlavor: pathvirtualization.FlavorWindowsExtendedDrive, wantRoot: `\\?\C:`, wantRest: `\Users\dev\project`},
		{name: "extended_drive_forward_slash", path: `\\?\C:/Users/dev`, wantFlavor: pathvirtualization.FlavorWindowsExtendedDrive, wantRoot: `\\?\C:`, wantRest: `/Users/dev`},
		{name: "extended_drive_lowercase_preserved", path: `\\?\c:\Users`, wantFlavor: pathvirtualization.FlavorWindowsExtendedDrive, wantRoot: `\\?\c:`, wantRest: `\Users`},
		{name: "extended_drive_volume_only", path: `\\?\D:\`, wantFlavor: pathvirtualization.FlavorWindowsExtendedDrive, wantRoot: `\\?\D:`, wantRest: `\`},

		// Extended UNC: '\\?\UNC\server\share\...'.
		{name: "extended_unc_workspace", path: `\\?\UNC\build01\dev\project`, wantFlavor: pathvirtualization.FlavorWindowsExtendedUNC, wantRoot: `\\?\UNC\build01\dev`, wantRest: `\project`},
		{name: "extended_unc_share_root_only", path: `\\?\UNC\build01\dev`, wantFlavor: pathvirtualization.FlavorWindowsExtendedUNC, wantRoot: `\\?\UNC\build01\dev`},
		{name: "extended_unc_lowercase_marker_preserved", path: `\\?\unc\build01\dev\project`, wantFlavor: pathvirtualization.FlavorWindowsExtendedUNC, wantRoot: `\\?\unc\build01\dev`, wantRest: `\project`},
		{name: "extended_unc_trailing_separator", path: `\\?\UNC\build01\dev\`, wantFlavor: pathvirtualization.FlavorWindowsExtendedUNC, wantRoot: `\\?\UNC\build01\dev`, wantRest: `\`},

		// Rejected: empty input.
		{name: "empty", path: ``, wantReason: pathvirtualization.SkipReasonEmptyRoot},

		// Rejected: relative or volume-less paths.
		{name: "bare_word", path: `project`, wantReason: pathvirtualization.SkipReasonRelativeRoot},
		{name: "relative_with_separators", path: `relative\path\project`, wantReason: pathvirtualization.SkipReasonRelativeRoot},
		{name: "dot_relative", path: `./project`, wantReason: pathvirtualization.SkipReasonRelativeRoot},
		{name: "dotdot_relative", path: `../project`, wantReason: pathvirtualization.SkipReasonRelativeRoot},
		{name: "home_relative", path: `~/project`, wantReason: pathvirtualization.SkipReasonRelativeRoot},
		{name: "drive_relative", path: `C:Users\dev`, wantReason: pathvirtualization.SkipReasonRelativeRoot},
		{name: "windows_root_relative", path: `\Users\dev`, wantReason: pathvirtualization.SkipReasonRelativeRoot},
		{name: "single_backslash", path: `\`, wantReason: pathvirtualization.SkipReasonRelativeRoot},
		{name: "digit_volume", path: `1:\Users\dev`, wantReason: pathvirtualization.SkipReasonRelativeRoot},
		{name: "device_drive_colon_only", path: `::\Users`, wantReason: pathvirtualization.SkipReasonRelativeRoot},
		{name: "drive_colon_separator", path: `C::\Users`, wantReason: pathvirtualization.SkipReasonRelativeRoot},
		{name: "unc_spelled_with_single_leading_backslash", path: `\build01\dev\project`, wantReason: pathvirtualization.SkipReasonRelativeRoot},

		// Rejected: malformed volume roots.
		{name: "drive_without_separator", path: `C:`, wantReason: pathvirtualization.SkipReasonMalformedVolumeRoot},
		{name: "bare_unc_prefix", path: `\\`, wantReason: pathvirtualization.SkipReasonMalformedVolumeRoot},
		{name: "unc_without_share", path: `\\server`, wantReason: pathvirtualization.SkipReasonMalformedVolumeRoot},
		{name: "unc_without_share_trailing_separator", path: `\\server\`, wantReason: pathvirtualization.SkipReasonMalformedVolumeRoot},
		{name: "unc_with_empty_server", path: `\\\\share\dev`, wantReason: pathvirtualization.SkipReasonMalformedVolumeRoot},
		{name: "extended_prefix_only", path: `\\?\`, wantReason: pathvirtualization.SkipReasonMalformedVolumeRoot},
		{name: "extended_drive_without_separator", path: `\\?\C:`, wantReason: pathvirtualization.SkipReasonMalformedVolumeRoot},
		{name: "extended_drive_forward_separator_missing", path: `\\?\C:x\Users`, wantReason: pathvirtualization.SkipReasonMalformedVolumeRoot},
		{name: "extended_unc_marker_without_separator", path: `\\?\UNC`, wantReason: pathvirtualization.SkipReasonMalformedVolumeRoot},
		{name: "extended_unc_without_share", path: `\\?\UNC\server`, wantReason: pathvirtualization.SkipReasonMalformedVolumeRoot},
		{name: "extended_unc_without_share_trailing_separator", path: `\\?\UNC\server\`, wantReason: pathvirtualization.SkipReasonMalformedVolumeRoot},
		{name: "extended_unc_with_empty_server", path: `\\?\UNC\\dev`, wantReason: pathvirtualization.SkipReasonMalformedVolumeRoot},

		// Rejected: Windows device namespace.
		{name: "device_prefix_only", path: `\\.`, wantReason: pathvirtualization.SkipReasonDeviceNamespace},
		{name: "device_root", path: `\\.\`, wantReason: pathvirtualization.SkipReasonDeviceNamespace},
		{name: "device_drive", path: `\\.\C:\dev`, wantReason: pathvirtualization.SkipReasonDeviceNamespace},
		{name: "device_pipe", path: `\\.\PIPE\aip\stdin`, wantReason: pathvirtualization.SkipReasonDeviceNamespace},
		{name: "extended_device_volume", path: `\\?\Volume{9f1c-0001}\dev`, wantReason: pathvirtualization.SkipReasonDeviceNamespace},
		{name: "extended_device_globalroot", path: `\\?\GLOBALROOT\Device\HarddiskVolumeShadowCopy1\x`, wantReason: pathvirtualization.SkipReasonDeviceNamespace},
		{name: "extended_device_physical_drive", path: `\\?\PhysicalDrive0`, wantReason: pathvirtualization.SkipReasonDeviceNamespace},
	}
}
