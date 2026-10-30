package main

import "testing"

func TestSelectReleases_StubAndMultiFromMetadata(t *testing.T) {
	t.Parallel()
	rels := []discoveredRelease{
		{DirName: "alpha", Meta: releaseMeta{FactoryKind: "alpha", PluginID: "io.a", Profiles: []string{"full"}}, Exports: 1},
		{DirName: "beta-stub", Meta: releaseMeta{FactoryKind: "local-stub", PluginID: "com.aiproxer.backend.localstub", Profiles: []string{"full"}}, Exports: 1},
		{DirName: "gamma", Meta: releaseMeta{FactoryKind: "gamma-go", PluginID: "io.gamma", Profiles: []string{"full"}}, Exports: 2},
	}
	stub, multi, err := selectReleases(rels)
	if err != nil {
		t.Fatal(err)
	}
	if stub.DirName != "beta-stub" {
		t.Fatalf("stub=%s", stub.DirName)
	}
	if multi.DirName != "gamma" {
		t.Fatalf("multi=%s", multi.DirName)
	}
}
