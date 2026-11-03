package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"gopkg.in/yaml.v3"
)

type releaseParityManifest struct {
	PluginID         string `json:"plugin_id"`
	Version          string `json:"version"`
	Executable       string `json:"executable"`
	SHA256           string `json:"sha256"`
	ProtocolMajor    uint32 `json:"protocol_major"`
	ProtocolMinMinor uint32 `json:"protocol_min_minor"`
	ProtocolMaxMinor uint32 `json:"protocol_max_minor"`
	Platforms        []struct {
		OS   string `json:"os"`
		Arch string `json:"arch"`
	} `json:"platforms"`
	Exports []struct {
		Kind           string `json:"kind"`
		Experimental   bool   `json:"experimental"`
		CredentialMode string `json:"credential_mode"`
		AccessScope    string `json:"access_scope"`
		ProcessSharing string `json:"process_sharing"`
		ExecutionClass string `json:"execution_class"`
	} `json:"exports"`
}

type releaseParityFile struct {
	Schema            string   `yaml:"schema"`
	PluginID          string   `yaml:"plugin_id"`
	FactoryKind       string   `yaml:"factory_kind"`
	Module            string   `yaml:"module"`
	Command           string   `yaml:"command"`
	ManifestTemplate  string   `yaml:"manifest_template"`
	Version           string   `yaml:"version"`
	BuildID           string   `yaml:"build_id"`
	Tag               string   `yaml:"tag"`
	Profiles          []string `yaml:"profiles"`
	PrivateCompanions []string `yaml:"private_companions"`
}

// TestRelease_ManifestReleaseDescriptorParity keeps the closed manifest,
// release metadata and runtime descriptor on one exact version, protocol and
// export posture. A release cannot advertise bytes or capabilities that the
// installed executable does not describe.
func TestRelease_ManifestReleaseDescriptorParity(t *testing.T) {
	t.Parallel()
	root := moduleRoot(t)
	desc, err := New().Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	factory := desc.Factories[0]

	rawManifest, err := os.ReadFile(filepath.Join(root, "manifest", "template.backendplugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest releaseParityManifest
	if err := json.Unmarshal(rawManifest, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.PluginID != PluginID || manifest.Version != ConnectorVersion || desc.PluginID != PluginID || desc.Version != ConnectorVersion {
		t.Fatalf("identity: manifest=%s/%s descriptor=%s/%s", manifest.PluginID, manifest.Version, desc.PluginID, desc.Version)
	}
	if manifest.Executable != "bin/aip-backend-claudeagentmodel" || manifest.SHA256 != "REPLACE_SHA256" {
		t.Fatalf("manifest executable placeholder contract: %+v", manifest)
	}
	if manifest.ProtocolMajor != desc.ProtocolMajor || manifest.ProtocolMaxMinor != desc.ProtocolMinor || manifest.ProtocolMinMinor > manifest.ProtocolMaxMinor {
		t.Fatalf("protocol: manifest=%d/%d/%d descriptor=%d/%d", manifest.ProtocolMajor, manifest.ProtocolMinMinor, manifest.ProtocolMaxMinor, desc.ProtocolMajor, desc.ProtocolMinor)
	}
	if len(manifest.Platforms) != 1 || manifest.Platforms[0].OS != "linux" || manifest.Platforms[0].Arch != "amd64" {
		t.Fatalf("platforms=%+v", manifest.Platforms)
	}
	if len(manifest.Exports) != 1 {
		t.Fatalf("exports=%+v", manifest.Exports)
	}
	export := manifest.Exports[0]
	if export.Kind != FactoryKind || !export.Experimental || export.CredentialMode != "oauth_user" ||
		export.AccessScope != "local_only" || export.ProcessSharing != "per_instance" || export.ExecutionClass != "agent_runtime" {
		t.Fatalf("export=%+v", export)
	}
	if factory.Kind != export.Kind || factory.Experimental != export.Experimental ||
		factory.CredentialMode != backendplugin.CredentialModeOAuthUser ||
		factory.AccessScope != backendplugin.AccessScopeLocalOnly ||
		factory.ProcessSharing != backendplugin.ProcessSharingPerInstance {
		t.Fatalf("descriptor export posture=%+v", factory)
	}
	if !factory.StaticCapabilities.Streaming || !factory.StaticCapabilities.Tools || !factory.StaticCapabilities.OrderedItems ||
		factory.StaticCapabilities.ItemReferences || factory.StaticCapabilities.ParallelToolCalls ||
		factory.StaticCapabilities.StructuredOutputs || factory.StaticCapabilities.Vision || factory.StaticCapabilities.Documents ||
		factory.StaticCapabilities.ReasoningReplay || !factory.SupportsDynamicInventory {
		t.Fatalf("capabilities=%+v", factory.StaticCapabilities)
	}

	rawRelease, err := os.ReadFile(filepath.Join(root, "release.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var release releaseParityFile
	if err := yaml.Unmarshal(rawRelease, &release); err != nil {
		t.Fatal(err)
	}
	if release.Schema != "aip.connector.release/v1" || release.PluginID != PluginID || release.FactoryKind != FactoryKind ||
		release.Module != "github.com/aiproxer/aiproxer/connectors/claudeagentmodel" || release.Command != "./cmd/aip-backend-claudeagentmodel" ||
		release.ManifestTemplate != "manifest/template.backendplugin.json" || release.Version != ConnectorVersion ||
		release.Tag != "claudeagentmodel-v"+ConnectorVersion {
		t.Fatalf("release=%+v", release)
	}
	if len(release.Profiles) != 1 || release.Profiles[0] != "full" {
		t.Fatalf("profiles=%v", release.Profiles)
	}
	companions := make(map[string]bool, len(release.PrivateCompanions))
	for _, companion := range release.PrivateCompanions {
		companions[companion] = true
	}
	for _, want := range []string{
		"sidecar/package.json",
		"sidecar/package-lock.json",
		"sidecar/bridge.mjs",
		"sidecar/session.mjs",
		"sidecar/tool-server.mjs",
	} {
		if !companions[want] {
			t.Fatalf("release companion %q missing from %v", want, release.PrivateCompanions)
		}
		if info, err := os.Stat(filepath.Join(root, want)); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("release companion %q is not a shipped regular file", want)
		}
	}
}

func TestRelease_SidecarPackageAndLockMatchPinnedSDK(t *testing.T) {
	t.Parallel()
	root := moduleRoot(t)
	rawPackage, err := os.ReadFile(filepath.Join(root, "sidecar", "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pkg struct {
		Name         string            `json:"name"`
		Version      string            `json:"version"`
		Dependencies map[string]string `json:"dependencies"`
		Engines      struct {
			Node string `json:"node"`
		} `json:"engines"`
	}
	if err := json.Unmarshal(rawPackage, &pkg); err != nil {
		t.Fatal(err)
	}
	if pkg.Version != ConnectorVersion || pkg.Dependencies["@anthropic-ai/claude-agent-sdk"] != pinnedSDKVersion ||
		pkg.Engines.Node == "" {
		t.Fatalf("sidecar package=%+v", pkg)
	}
	rawLock, err := os.ReadFile(filepath.Join(root, "sidecar", "package-lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		Name     string `json:"name"`
		Version  string `json:"version"`
		Packages map[string]struct {
			Version   string `json:"version"`
			Integrity string `json:"integrity"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(rawLock, &lock); err != nil {
		t.Fatal(err)
	}
	rootPackage, ok := lock.Packages[""]
	if !ok || rootPackage.Version != ConnectorVersion {
		t.Fatalf("lock root=%+v", lock.Packages[""])
	}
	sdk, ok := lock.Packages["node_modules/@anthropic-ai/claude-agent-sdk"]
	if !ok || sdk.Version != pinnedSDKVersion || sdk.Integrity == "" {
		t.Fatalf("lock SDK row=%+v", sdk)
	}
}
