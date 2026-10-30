package main

import (
	"testing"
)

func TestClaimsPlatform(t *testing.T) {
	t.Parallel()
	plats := []platformEntry{
		{OS: "linux", Arch: "amd64"},
		{OS: "windows", Arch: "amd64"},
	}
	if !claimsPlatform(plats, "linux", "amd64") {
		t.Fatal("expected linux/amd64 claim")
	}
	if claimsPlatform(plats, "darwin", "arm64") {
		t.Fatal("darwin/arm64 must not match linux/windows-only claims")
	}
	if claimsPlatform(nil, "linux", "amd64") {
		t.Fatal("empty claims must not match")
	}
}

func TestFilterReleasesClaimingPlatform_ZeroAndMixed(t *testing.T) {
	t.Parallel()
	rels := []discoveredRelease{
		{DirName: "supported", Meta: releaseMeta{ManifestTmpl: "manifest/template.backendplugin.json"}},
		{DirName: "unsupported", Meta: releaseMeta{ManifestTmpl: "manifest/template.backendplugin.json"}},
		{DirName: "also-unsupported", Meta: releaseMeta{ManifestTmpl: "manifest/template.backendplugin.json"}},
	}
	claims := map[string][]platformEntry{
		"supported": {
			{OS: "linux", Arch: "amd64"},
			{OS: "windows", Arch: "amd64"},
		},
		"unsupported": {
			{OS: "windows", Arch: "amd64"},
		},
		"also-unsupported": {
			{OS: "linux", Arch: "amd64"},
		},
	}

	zero := filterReleasesClaimingPlatform(rels, "darwin", "arm64", claims)
	if len(zero) != 0 {
		t.Fatalf("darwin host with zero native claims: got %#v want empty", zero)
	}

	mixed := filterReleasesClaimingPlatform(rels, "linux", "amd64", claims)
	if len(mixed) != 2 {
		t.Fatalf("mixed support: got %d entries %#v", len(mixed), mixed)
	}
	if mixed[0].DirName != "supported" || mixed[1].DirName != "also-unsupported" {
		t.Fatalf("mixed order/names = %#v", mixed)
	}

	winOnly := filterReleasesClaimingPlatform(rels, "windows", "amd64", claims)
	if len(winOnly) != 2 {
		t.Fatalf("windows support: got %#v", winOnly)
	}
	names := []string{winOnly[0].DirName, winOnly[1].DirName}
	if names[0] != "supported" || names[1] != "unsupported" {
		t.Fatalf("windows names = %v", names)
	}
}

func TestFilterManifestPlatforms_StillRejectsNoMatch(t *testing.T) {
	t.Parallel()
	in := `{
  "schema": "aiproxer.backendplugin.manifest/v1",
  "platforms": [{"os":"linux","arch":"amd64"},{"os":"windows","arch":"amd64"}]
}`
	if _, err := filterManifestPlatforms(in, "darwin", "arm64"); err == nil {
		t.Fatal("filterManifestPlatforms must still reject missing native platform (no fabrication)")
	}
}

func TestParsePlatforms_MissingOrEmpty(t *testing.T) {
	t.Parallel()
	plats, err := parsePlatforms(`{"schema":"x"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(plats) != 0 {
		t.Fatalf("missing platforms key: got %#v", plats)
	}
	plats, err = parsePlatforms(`{"platforms":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(plats) != 0 {
		t.Fatalf("empty platforms: got %#v", plats)
	}
}
