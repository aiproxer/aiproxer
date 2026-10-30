package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/tools/backendplugin/runner"
	"github.com/aiproxer/aiproxer/tools/taskrunner"
	"gopkg.in/yaml.v3"
)

const packageCommandTimeout = 15 * time.Minute

// Executable companions are explicit regular files, never directories or links.
// Targets are relative to bin/, without packaging operator-installed runtimes.
func safeCompanionPath(path string) bool {
	return path != "" && filepath.IsLocal(path) && !strings.Contains(path, "\\") && filepath.ToSlash(filepath.Clean(path)) == path
}

func copyExecutableCompanions(root, bin string, companions map[string]string) error {
	sources := make([]string, 0, len(companions))
	for source := range companions {
		sources = append(sources, source)
	}
	slices.Sort(sources)
	seen := make(map[string]bool, len(sources))
	for _, source := range sources {
		target := companions[source]
		if !safeCompanionPath(source) || !safeCompanionPath(target) || seen[target] {
			return fmt.Errorf("executable companion unsafe or duplicate path: %q -> %q", source, target)
		}
		seen[target] = true
		path := root
		for part := range strings.SplitSeq(source, "/") {
			path = filepath.Join(path, part)
			info, err := os.Lstat(path)
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("executable companion links are forbidden: %q", source)
			}
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("executable companion must be a regular file: %q", source)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		dest := filepath.Join(bin, filepath.FromSlash(target))
		if _, err := os.Lstat(dest); err == nil || !os.IsNotExist(err) {
			return fmt.Errorf("executable companion would overwrite %q", target)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dest, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

type releaseMeta struct {
	Schema               string            `yaml:"schema"`
	PluginID             string            `yaml:"plugin_id"`
	FactoryKind          string            `yaml:"factory_kind"`
	Module               string            `yaml:"module"`
	Command              string            `yaml:"command"`
	ManifestTmpl         string            `yaml:"manifest_template"`
	Version              string            `yaml:"version"`
	BuildID              string            `yaml:"build_id"`
	Tag                  string            `yaml:"tag"`
	ProtocolMajor        uint32            `yaml:"protocol_major"`
	ProtocolMinMinor     uint32            `yaml:"protocol_min_minor"`
	ProtocolMaxMinor     uint32            `yaml:"protocol_max_minor"`
	ProtocolFeatures     []string          `yaml:"protocol_features"`
	Profiles             []string          `yaml:"profiles"`
	PublishedRootModule  string            `yaml:"published_root_module"`
	ReplacePolicy        string            `yaml:"replace_policy"`
	PrivateCompanions    []string          `yaml:"private_companions"`
	ExecutableCompanions map[string]string `yaml:"executable_companions"`
}

type discoveredRelease struct {
	DirName string
	Root    string // absolute connector module root
	Meta    releaseMeta
}

func main() {
	root := flag.String("root", ".", "repository root")
	profile := flag.String("profile", "minimal", "minimal|full")
	dest := flag.String("dest", "", "install root (staged path; never ProgramFiles in tests)")
	selectCSV := flag.String("select", "", "optional comma-separated connector directory names (not a maintained source list)")
	flag.Parse()
	if strings.TrimSpace(*dest) == "" {
		fmt.Fprintln(os.Stderr, "package_plugins: -dest is required")
		os.Exit(2)
	}
	var selectSet map[string]struct{}
	if s := strings.TrimSpace(*selectCSV); s != "" {
		selectSet = map[string]struct{}{}
		for p := range strings.SplitSeq(s, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			selectSet[p] = struct{}{}
		}
	}
	if err := packageProfile(*root, *profile, *dest, selectSet); err != nil {
		fmt.Fprintf(os.Stderr, "package_plugins: %v\n", err)
		os.Exit(1)
	}
}

func packageProfile(root, profile, dest string, selectSet map[string]struct{}) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	absDest, err := filepath.Abs(dest)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(absDest), 0o755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(filepath.Dir(absDest), ".aip-pkg-staging-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(staging) }()

	access := "owner: installer/admin\nproxy: read,execute\nother: none\n" +
		"runtime_acl: unverified_on_agent\n" +
		"layout_targets: /opt/AIProxer/plugins | /Library/Application Support/AIProxer/plugins | %ProgramFiles%\\AIProxer\\plugins\n"
	if err := os.WriteFile(filepath.Join(staging, "ACCESS.txt"), []byte(access), 0o644); err != nil {
		return err
	}
	platform := runtime.GOOS + "/" + runtime.GOARCH
	index := map[string]any{
		"schema":   "aip.plugin.package.index/v1",
		"profile":  profile,
		"platform": platform,
		"plugins":  []any{},
	}
	if profile == "minimal" {
		if err := writeIndex(staging, index); err != nil {
			return err
		}
		return publishAtomic(staging, absDest)
	}
	if profile != "full" {
		return fmt.Errorf("unknown profile %q", profile)
	}

	rels, err := discoverReleaseMetadata(absRoot)
	if err != nil {
		return err
	}
	selected := make([]discoveredRelease, 0, len(rels))
	for _, r := range rels {
		if selectSet != nil {
			if _, ok := selectSet[r.DirName]; !ok {
				continue
			}
		}
		if !hasProfile(r.Meta.Profiles, profile) {
			continue
		}
		selected = append(selected, r)
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].DirName < selected[j].DirName })

	// Package only connectors whose manifest template claims the native platform.
	// Unsupported connectors are omitted (not built, not indexed). Explicit -select
	// of only unsupported connectors therefore yields a successful empty package —
	// truthful and deterministic, without fabricating Darwin (or other) support.
	selected, err = filterSelectedToNativePlatform(selected, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	if err := validateReleaseSet(selected); err != nil {
		return err
	}

	pluginEntries := make([]any, 0, len(selected))
	for _, r := range selected {
		entry, err := packageOne(staging, r, platform, access)
		if err != nil {
			return err
		}
		pluginEntries = append(pluginEntries, entry)
	}
	index["plugins"] = pluginEntries
	if err := writeIndex(staging, index); err != nil {
		return err
	}
	return publishAtomic(staging, absDest)
}

func discoverReleaseMetadata(root string) ([]discoveredRelease, error) {
	base := filepath.Join(root, "connectors")
	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []discoveredRelease
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dirName := e.Name()
		connRoot := filepath.Join(base, dirName)
		relPath := filepath.Join(connRoot, "release.yaml")
		raw, err := os.ReadFile(relPath)
		if err != nil {
			continue
		}
		meta, err := parseReleaseYAML(raw)
		if err != nil {
			return nil, fmt.Errorf("%s/release.yaml: %w", dirName, err)
		}
		if err := validateReleasePaths(connRoot, meta); err != nil {
			return nil, fmt.Errorf("%s: %w", dirName, err)
		}
		out = append(out, discoveredRelease{DirName: dirName, Root: connRoot, Meta: meta})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DirName < out[j].DirName })
	return out, nil
}

func parseReleaseYAML(raw []byte) (releaseMeta, error) {
	var meta releaseMeta
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&meta); err != nil {
		return releaseMeta{}, err
	}
	if meta.Schema != "aip.connector.release/v1" {
		return releaseMeta{}, fmt.Errorf("unsupported schema %q", meta.Schema)
	}
	for _, req := range []struct {
		name, val string
	}{
		{"plugin_id", meta.PluginID},
		{"factory_kind", meta.FactoryKind},
		{"module", meta.Module},
		{"command", meta.Command},
		{"manifest_template", meta.ManifestTmpl},
		{"version", meta.Version},
		{"build_id", meta.BuildID},
		{"published_root_module", meta.PublishedRootModule},
		{"replace_policy", meta.ReplacePolicy},
	} {
		if strings.TrimSpace(req.val) == "" {
			return releaseMeta{}, fmt.Errorf("missing %s", req.name)
		}
	}
	if len(meta.Profiles) == 0 {
		return releaseMeta{}, fmt.Errorf("missing profiles")
	}
	if meta.Tag == "" {
		meta.Tag = meta.BuildID
	}
	return meta, nil
}

func validateReleasePaths(connRoot string, meta releaseMeta) error {
	absRoot, err := filepath.Abs(connRoot)
	if err != nil {
		return err
	}
	absRoot, err = filepath.EvalSymlinks(absRoot)
	if err != nil {
		// connector root may not resolve on fresh dirs; fall back
		absRoot, _ = filepath.Abs(connRoot)
	}
	check := func(rel string) error {
		rel = filepath.Clean(rel)
		if rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
			return fmt.Errorf("path %q escapes connector root", rel)
		}
		full := filepath.Join(absRoot, rel)
		resolved := full
		if st, err := os.Lstat(full); err == nil {
			if st.Mode()&os.ModeSymlink != 0 {
				target, err := filepath.EvalSymlinks(full)
				if err != nil {
					return err
				}
				resolved = target
			}
		}
		relToRoot, err := filepath.Rel(absRoot, resolved)
		if err != nil || strings.HasPrefix(relToRoot, "..") {
			return fmt.Errorf("path %q escapes connector root", rel)
		}
		return nil
	}
	if err := check(meta.Command); err != nil {
		return fmt.Errorf("command: %w", err)
	}
	if err := check(meta.ManifestTmpl); err != nil {
		return fmt.Errorf("manifest_template: %w", err)
	}
	for _, p := range meta.PrivateCompanions {
		if err := check(p); err != nil {
			return fmt.Errorf("private_companions: %w", err)
		}
	}
	for source, target := range meta.ExecutableCompanions {
		if err := check(source); err != nil {
			return fmt.Errorf("executable_companions: %w", err)
		}
		if !safeCompanionPath(target) {
			return fmt.Errorf("executable_companions: unsafe target %q", target)
		}
	}
	return nil
}

func validateReleaseSet(rels []discoveredRelease) error {
	ids := map[string]string{}
	kinds := map[string]string{}
	for _, r := range rels {
		if prev, ok := ids[r.Meta.PluginID]; ok {
			return fmt.Errorf("duplicate plugin_id %q (%s and %s)", r.Meta.PluginID, prev, r.DirName)
		}
		ids[r.Meta.PluginID] = r.DirName
		if prev, ok := kinds[r.Meta.FactoryKind]; ok {
			return fmt.Errorf("duplicate factory_kind %q (%s and %s)", r.Meta.FactoryKind, prev, r.DirName)
		}
		kinds[r.Meta.FactoryKind] = r.DirName
	}
	return nil
}

func hasProfile(profiles []string, want string) bool {
	return slices.Contains(profiles, want)
}

func packageOne(staging string, r discoveredRelease, platform, access string) (map[string]any, error) {
	pluginDir := filepath.Join(staging, r.DirName)
	binDir := filepath.Join(pluginDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return nil, err
	}
	cmdBase := filepath.Base(filepath.Clean(r.Meta.Command))
	exeName := cmdBase
	if runtime.GOOS == "windows" && !strings.HasSuffix(strings.ToLower(exeName), ".exe") {
		exeName += ".exe"
	}
	outBin := filepath.Join(binDir, exeName)
	absOut, err := filepath.Abs(outBin)
	if err != nil {
		return nil, err
	}
	result := runner.Run(context.Background(), runner.Request{
		Argv:    []string{"go", "build", "-trimpath", "-ldflags=-buildid=", "-o", absOut, r.Meta.Command},
		Dir:     r.Root,
		Env:     []string{"GOWORK=off", "CGO_ENABLED=0"},
		Timeout: packageCommandTimeout,
		Output:  taskrunner.Capture,
		Label:   "package_plugins:" + r.DirName + ":build",
	})
	if result.Kind != taskrunner.Success {
		return nil, fmt.Errorf("build %s: %w", r.DirName, runner.Error(result))
	}
	exeDig, err := fileSHA256(absOut)
	if err != nil {
		return nil, err
	}
	manBody, err := os.ReadFile(filepath.Join(r.Root, r.Meta.ManifestTmpl))
	if err != nil {
		return nil, err
	}
	exeRel := filepath.ToSlash(filepath.Join("bin", cmdBase))
	if runtime.GOOS == "windows" && !strings.HasSuffix(strings.ToLower(exeRel), ".exe") {
		exeRel += ".exe"
	}
	manifest := string(manBody)
	manifest = strings.ReplaceAll(manifest, "REPLACE_SHA256", exeDig)
	manifest = strings.ReplaceAll(manifest, "REPLACE_BUILD_ID", r.Meta.BuildID)
	// Replace common template executable forms.
	for _, old := range []string{
		`"executable": "bin/` + cmdBase + `"`,
		`"executable":"bin/` + cmdBase + `"`,
		`"executable": "bin/aip-backend-localstub"`,
		`"executable": "bin/aip-backend-synthetic"`,
	} {
		manifest = strings.ReplaceAll(manifest, old, fmt.Sprintf(`"executable": %q`, exeRel))
	}
	// Filter platforms to the current OS/arch so the strict manifest parser
	// does not reject a Linux binary that still lists Windows platforms.
	manifest, err = filterManifestPlatforms(manifest, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return nil, fmt.Errorf("filter platforms %s: %w", r.DirName, err)
	}
	manPath := filepath.Join(pluginDir, "plugin.backendplugin.json")
	if err := os.WriteFile(manPath, []byte(manifest), 0o644); err != nil {
		return nil, err
	}
	manDig, err := fileSHA256(manPath)
	if err != nil {
		return nil, err
	}
	if len(r.Meta.PrivateCompanions) == 0 {
		// Preserve prior localstub helper note when private/ exists without explicit list.
		srcPriv := filepath.Join(r.Root, "private")
		if st, err := os.Stat(srcPriv); err == nil && st.IsDir() {
			if err := copyDirContained(srcPriv, filepath.Join(pluginDir, "private"), r.Root); err != nil {
				return nil, err
			}
		} else {
			priv := filepath.Join(pluginDir, "private", "bridge-note.txt")
			if err := os.MkdirAll(filepath.Dir(priv), 0o755); err != nil {
				return nil, err
			}
			if err := os.WriteFile(priv, []byte("connector-local companion; host must not import\n"), 0o644); err != nil {
				return nil, err
			}
		}
	} else {
		for _, rel := range r.Meta.PrivateCompanions {
			src := filepath.Join(r.Root, rel)
			dst := filepath.Join(pluginDir, rel)
			st, err := os.Stat(src)
			if err != nil {
				return nil, err
			}
			if st.IsDir() {
				if err := copyDirContained(src, dst, r.Root); err != nil {
					return nil, err
				}
				continue
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return nil, err
			}
			b, err := os.ReadFile(src)
			if err != nil {
				return nil, err
			}
			if err := os.WriteFile(dst, b, 0o644); err != nil {
				return nil, err
			}
		}
	}
	if err := copyExecutableCompanions(r.Root, binDir, r.Meta.ExecutableCompanions); err != nil {
		return nil, err
	}
	return map[string]any{
		"plugin_id":        r.Meta.PluginID,
		"factory_kind":     r.Meta.FactoryKind,
		"version":          r.Meta.Version,
		"tag":              r.Meta.Tag,
		"platform":         platform,
		"digest":           exeDig,
		"manifest_digest":  manDig,
		"install_root":     r.DirName,
		"path":             r.DirName,
		"manifest":         r.DirName + "/plugin.backendplugin.json",
		"access":           "owner:installer/admin;proxy:read,execute;other:none;runtime_acl:unverified_on_agent",
		"access_text_hash": sha256Hex([]byte(access)),
	}, nil
}

func copyDirContained(src, dst, connRoot string) error {
	absRoot, err := filepath.Abs(connRoot)
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(absRoot); err == nil {
		absRoot = resolved
	}
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := filepath.EvalSymlinks(path)
			if err != nil {
				return fmt.Errorf("private companion symlink %q: %w", path, err)
			}
			relToRoot, err := filepath.Rel(absRoot, target)
			if err != nil || strings.HasPrefix(relToRoot, "..") {
				return fmt.Errorf("path %q escapes connector root", path)
			}
			// Refuse contained symlinks too: never follow links while packaging.
			return fmt.Errorf("path %q escapes connector root", path)
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
}

func publishAtomic(staging, dest string) error {
	bak := ""
	if _, err := os.Stat(dest); err == nil {
		bak = dest + ".bak"
		_ = os.RemoveAll(bak)
		if err := os.Rename(dest, bak); err != nil {
			return err
		}
	}
	if err := os.Rename(staging, dest); err != nil {
		if bak != "" {
			_ = os.Rename(bak, dest)
		}
		return err
	}
	if bak != "" {
		_ = os.RemoveAll(bak)
	}
	return nil
}

func writeIndex(dest string, index map[string]any) error {
	b, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dest, "package-index.json"), append(b, '\n'), 0o644)
}

func fileSHA256(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return sha256Hex(b), nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// filterManifestPlatforms rewrites the manifest JSON so only the entry matching
// goos/goarch remains in "platforms". This prevents the strict manifest parser
// from rejecting a native binary whose template still lists other OS entries
// (e.g. a Linux build with Windows platforms that require .exe).
func filterManifestPlatforms(manifest, goos, goarch string) (string, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(manifest), &m); err != nil {
		return "", err
	}
	rawPlats, ok := m["platforms"]
	if !ok {
		return manifest, nil
	}
	var plats []platformEntry
	if err := json.Unmarshal(rawPlats, &plats); err != nil {
		return "", err
	}
	var kept []map[string]string
	for _, p := range plats {
		if p.OS == goos && p.Arch == goarch {
			kept = append(kept, map[string]string{"os": p.OS, "arch": p.Arch})
		}
	}
	if len(kept) == 0 {
		return "", fmt.Errorf("no platform entry matches %s/%s", goos, goarch)
	}
	b, err := json.Marshal(kept)
	if err != nil {
		return "", err
	}
	m["platforms"] = b
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out), nil
}

type platformEntry struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

func parsePlatforms(manifest string) ([]platformEntry, error) {
	var m struct {
		Platforms []platformEntry `json:"platforms"`
	}
	if err := json.Unmarshal([]byte(manifest), &m); err != nil {
		return nil, err
	}
	return m.Platforms, nil
}

func claimsPlatform(plats []platformEntry, goos, goarch string) bool {
	for _, p := range plats {
		if p.OS == goos && p.Arch == goarch {
			return true
		}
	}
	return false
}

// filterReleasesClaimingPlatform returns releases whose precomputed claims include
// goos/goarch, preserving input order. Missing claim map entries are treated as
// unsupported (omitted).
func filterReleasesClaimingPlatform(rels []discoveredRelease, goos, goarch string, claims map[string][]platformEntry) []discoveredRelease {
	out := make([]discoveredRelease, 0, len(rels))
	for _, r := range rels {
		if claimsPlatform(claims[r.DirName], goos, goarch) {
			out = append(out, r)
		}
	}
	return out
}

func filterSelectedToNativePlatform(selected []discoveredRelease, goos, goarch string) ([]discoveredRelease, error) {
	claims := make(map[string][]platformEntry, len(selected))
	for _, r := range selected {
		body, err := os.ReadFile(filepath.Join(r.Root, r.Meta.ManifestTmpl))
		if err != nil {
			return nil, fmt.Errorf("read platforms %s: %w", r.DirName, err)
		}
		plats, err := parsePlatforms(string(body))
		if err != nil {
			return nil, fmt.Errorf("parse platforms %s: %w", r.DirName, err)
		}
		claims[r.DirName] = plats
	}
	return filterReleasesClaimingPlatform(selected, goos, goarch, claims), nil
}
