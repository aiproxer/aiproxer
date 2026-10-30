package main

import (
	"testing"
)

func TestClaimsPlatform(t *testing.T) {
	t.Parallel()
	claims := []platformClaim{
		{OS: "linux", Arch: "amd64"},
		{OS: "windows", Arch: "arm64"},
	}
	if !claimsPlatform(claims, "linux", "amd64") {
		t.Fatal("expected exact linux/amd64 match")
	}
	if claimsPlatform(claims, "darwin", "arm64") {
		t.Fatal("darwin must not match linux/windows-only claims")
	}
	if claimsPlatform(claims, "windows", "amd64") {
		t.Fatal("arch mismatch must not match")
	}
}

func TestNativeSupportedFullProfileNames_ZeroAndMixed(t *testing.T) {
	t.Parallel()
	selected := []discoveredRelease{
		{DirName: "full-native", Meta: releaseMeta{Profiles: []string{"full"}}},
		{DirName: "full-other", Meta: releaseMeta{Profiles: []string{"full"}}},
		{DirName: "minimal-only", Meta: releaseMeta{Profiles: []string{"minimal"}}},
		{DirName: "full-unsupported", Meta: releaseMeta{Profiles: []string{"full"}}},
	}
	claims := map[string][]platformClaim{
		"full-native": {
			{OS: "linux", Arch: "amd64"},
			{OS: "darwin", Arch: "arm64"},
		},
		"full-other": {
			{OS: "linux", Arch: "amd64"},
		},
		"minimal-only": {
			{OS: "darwin", Arch: "arm64"},
		},
		"full-unsupported": {
			{OS: "windows", Arch: "amd64"},
		},
	}

	zero := nativeSupportedFullProfileNames(selected, "plan9", "amd64", claims)
	if len(zero) != 0 {
		t.Fatalf("zero-supported host: got %v want empty", zero)
	}

	mixed := nativeSupportedFullProfileNames(selected, "darwin", "arm64", claims)
	if len(mixed) != 1 || mixed[0] != "full-native" {
		t.Fatalf("mixed darwin support: got %v want [full-native]", mixed)
	}

	linux := nativeSupportedFullProfileNames(selected, "linux", "amd64", claims)
	if len(linux) != 2 || linux[0] != "full-native" || linux[1] != "full-other" {
		t.Fatalf("linux support: got %v", linux)
	}
}
