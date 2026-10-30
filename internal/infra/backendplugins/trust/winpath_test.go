package trust

import "testing"

func TestNormalizeWindowsPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "slash-prefix", in: `\\?\C:\Temp\a`, want: `c:\temp\a`},
		{name: "nt-object-prefix", in: `\??\C:\Temp\a`, want: `c:\temp\a`},
		{name: "unc-extended", in: `\\?\UNC\server\share\dir`, want: `\\server\share\dir`},
		{name: "forward-slashes", in: `C:/Temp/A`, want: `c:\temp\a`},
		{name: "drive-case", in: `c:\Temp\A`, want: `c:\temp\a`},
		{name: "trailing-sep", in: `C:\Temp\A\`, want: `c:\temp\a`},
		{name: "nt-device", in: `\Device\HarddiskVolume3\Temp\A`, want: `\device\harddiskvolume3\temp\a`},
		{name: "mixed-sep", in: `\\?\C:/Temp\A/`, want: `c:\temp\a`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := normalizeWindowsPath(tc.in)
			if got != tc.want {
				t.Fatalf("normalizeWindowsPath(%q)=%q want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestWindowsPathContained(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		root      string
		candidate string
		want      bool
	}{
		{
			name:      "dos-under-root",
			root:      `C:\trusted`,
			candidate: `C:\trusted\bin\plugin.exe`,
			want:      true,
		},
		{
			name:      "extended-prefix-alias",
			root:      `\\?\C:\trusted`,
			candidate: `\??\C:\trusted\bin\plugin.exe`,
			want:      true,
		},
		{
			name:      "drive-letter-case",
			root:      `c:\Trusted`,
			candidate: `C:\trusted\bin\plugin.exe`,
			want:      true,
		},
		{
			name:      "nt-device-paths",
			root:      `\Device\HarddiskVolume2\runners\work`,
			candidate: `\Device\HarddiskVolume2\runners\work\bin\plugin.exe`,
			want:      true,
		},
		{
			name:      "unc-under-share",
			root:      `\\?\UNC\server\share\root`,
			candidate: `\\server\share\root\bin\plugin.exe`,
			want:      true,
		},
		{
			name:      "sibling-prefix-not-contained",
			root:      `C:\trust`,
			candidate: `C:\trusted\bin\plugin.exe`,
			want:      false,
		},
		{
			name:      "parent-escape",
			root:      `C:\trusted`,
			candidate: `C:\outside\plugin.exe`,
			want:      false,
		},
		{
			name:      "root-itself-not-file",
			root:      `C:\trusted`,
			candidate: `C:\trusted`,
			want:      false,
		},
		{
			name:      "mapped-dos-vs-different-volume",
			root:      `\Device\HarddiskVolume1\a`,
			candidate: `\Device\HarddiskVolume2\a\bin\plugin.exe`,
			want:      false,
		},
		{
			// Raw subst drive letter vs underlying path must fail here; callers
			// canonicalize both sides via GetFinalPathNameByHandle first.
			name:      "raw-subst-without-canonicalization",
			root:      `Z:\plugin-root`,
			candidate: `C:\real\plugin-root\bin\plugin.exe`,
			want:      false,
		},
		{
			name:      "empty-rejected",
			root:      ``,
			candidate: `C:\trusted\a`,
			want:      false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := windowsPathContained(tc.root, tc.candidate)
			if got != tc.want {
				t.Fatalf("windowsPathContained(%q,%q)=%v want %v", tc.root, tc.candidate, got, tc.want)
			}
		})
	}
}
